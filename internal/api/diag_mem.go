// Memory diagnostics that stay on in release builds, for leaks seen on
// production nodes: a periodic "[mem]" log line (kept with the logs, so
// the growth before an OOM kill can be read afterwards), automatic heap
// and goroutine profiles in <data>/diag/ once the heap gets large, and
// authenticated profile endpoints (reachable through a node's remote
// management like the rest of the API).

package api

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	memLogEvery     = 10 * time.Minute
	memCheckEvery   = time.Minute
	diagKeepSets    = 6                // snapshot sets kept in <data>/diag
	diagDefaultMB   = 1024             // heap in use that triggers the first snapshot
	diagMinInterval = 30 * time.Minute // between snapshots
	diagRearmFactor = 1.5              // the next one once the heap grew this much
)

// diagDir: where snapshots are written.
func (s *Server) diagDir() string { return filepath.Join(s.app.DataDir(), "diag") }

// rssKB: this process's resident memory (Linux; 0 elsewhere).
func rssKB() int64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			n, _ := strconv.ParseInt(strings.Fields(v)[0], 10, 64)
			return n
		}
	}
	return 0
}

// memLine: the figures of a "[mem]" line and of a snapshot's summary.
func (s *Server) memLine() string {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m := s.app.MsessStats()
	ro := s.app.Node().ObjectCounts()
	line := fmt.Sprintf("rss=%dMB heap=%dMB inuse=%dMB sys=%dMB stack=%dMB objects=%d goroutines=%d msess=%d/%d relay=%d peers_of_cache=%d peer_udp=%d",
		rssKB()>>10, ms.HeapAlloc>>20, ms.HeapInuse>>20, ms.Sys>>20, ms.StackInuse>>20, ms.HeapObjects,
		runtime.NumGoroutine(), m["exit_sessions"], m["exit_subflows"], s.app.Node().GetStats().Conns, ro["peers_of_cache"], ro["peer_udp_flows"])
	if src, tgt := s.app.MsessBreakdown(1); len(src) > 0 {
		line += fmt.Sprintf(" msess_top_source=%s:%d msess_top_target=%s:%d", src[0].Key, src[0].N, tgt[0].Key, tgt[0].N)
	}
	return line
}

func diagThresholdMB() uint64 {
	if v, err := strconv.ParseUint(os.Getenv("HY2SCALE_DIAG_HEAP_MB"), 10, 64); err == nil && v > 0 {
		return v
	}
	return diagDefaultMB
}

// startMemWatch logs the memory figures every memLogEvery and writes a
// snapshot when the heap in use passes the threshold (then again only
// after it grew by diagRearmFactor, at most every diagMinInterval).
func (s *Server) startMemWatch() {
	go func() {
		next := diagThresholdMB() << 20
		var last time.Time
		lastLog := time.Now()
		for {
			time.Sleep(memCheckEvery)
			if time.Since(lastLog) >= memLogEvery {
				log.Printf("[mem] %s", s.memLine())
				lastLog = time.Now()
			}
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse < next || time.Since(last) < diagMinInterval {
				continue
			}
			if dir, err := s.writeSnapshot("auto"); err != nil {
				log.Printf("[mem] snapshot: %v", err)
			} else {
				log.Printf("[mem] heap in use %dMB: profiles written to %s (%s)", ms.HeapInuse>>20, dir, s.memLine())
			}
			last = time.Now()
			next = uint64(float64(ms.HeapInuse) * diagRearmFactor)
		}
	}()
}

var snapshotMu sync.Mutex

// writeSnapshot writes heap.pprof, goroutine.txt (stacks grouped with
// counts), goroutine-full.txt (every goroutine with its wait time; capped)
// and summary.txt into <data>/diag/<time>-<why>/, keeping the newest sets.
func (s *Server) writeSnapshot(why string) (string, error) {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	dir := filepath.Join(s.diagDir(), time.Now().UTC().Format("20060102-150405")+"-"+why)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	write := func(name string, fn func(f *os.File) error) error {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		defer f.Close()
		return fn(f)
	}
	runtime.GC() // a heap profile shows the state as of the last GC
	errs := []error{
		write("heap.pprof", func(f *os.File) error { return pprof.Lookup("heap").WriteTo(f, 0) }),
		write("goroutine.txt", func(f *os.File) error { return pprof.Lookup("goroutine").WriteTo(f, 1) }),
		write("goroutine-full.txt", func(f *os.File) error {
			buf := make([]byte, 32<<20)
			n := runtime.Stack(buf, true)
			_, err := f.Write(buf[:n])
			return err
		}),
		write("summary.txt", func(f *os.File) error {
			_, err := fmt.Fprintf(f, "version %s\nuptime %s\n%s\n", Version, time.Since(startTime).Round(time.Second), s.memLine())
			return err
		}),
	}
	for _, err := range errs {
		if err != nil {
			return dir, err
		}
	}
	s.pruneSnapshots()
	return dir, nil
}

func (s *Server) snapshotNames() []string {
	ents, _ := os.ReadDir(s.diagDir())
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) pruneSnapshots() {
	names := s.snapshotNames()
	for len(names) > diagKeepSets {
		os.RemoveAll(filepath.Join(s.diagDir(), names[0]))
		names = names[1:]
	}
}

// diagMemRoutes: the profile and snapshot endpoints (behind login).
func (s *Server) diagMemRoutes(m *http.ServeMux) {
	// GET /api/diag/pprof/{name}?debug=N — a live profile (heap,
	// goroutine, allocs, threadcreate, block, mutex).
	m.HandleFunc("GET /api/diag/pprof/{name}", func(w http.ResponseWriter, r *http.Request) {
		p := pprof.Lookup(r.PathValue("name"))
		if p == nil {
			http.Error(w, "unknown profile", 404)
			return
		}
		debug, _ := strconv.Atoi(r.URL.Query().Get("debug"))
		if r.URL.Query().Get("gc") == "1" {
			runtime.GC()
		}
		if debug > 0 {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		p.WriteTo(w, debug)
	})
	// GET /api/diag/snapshots — the snapshot sets and their files.
	m.HandleFunc("GET /api/diag/snapshots", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		for _, n := range s.snapshotNames() {
			files := []string{}
			ents, _ := os.ReadDir(filepath.Join(s.diagDir(), n))
			for _, e := range ents {
				files = append(files, e.Name())
			}
			out = append(out, map[string]any{"name": n, "files": files})
		}
		writeJSON(w, map[string]any{"threshold_mb": diagThresholdMB(), "snapshots": out})
	})
	// POST /api/diag/snapshots — write one now.
	m.HandleFunc("POST /api/diag/snapshots", func(w http.ResponseWriter, r *http.Request) {
		dir, err := s.writeSnapshot("manual")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]string{"name": filepath.Base(dir)})
	})
	// GET /api/diag/snapshots/{name}/{file}
	m.HandleFunc("GET /api/diag/snapshots/{name}/{file}", func(w http.ResponseWriter, r *http.Request) {
		name, file := r.PathValue("name"), r.PathValue("file")
		if strings.ContainsAny(name+file, `/\`) || strings.HasPrefix(name, ".") || strings.HasPrefix(file, ".") {
			http.Error(w, "bad name", 400)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.diagDir(), name, file))
	})
}

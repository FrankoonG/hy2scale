// Core: diagnostics and logs.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/FrankoonG/hy2scale/core/logbuf"
	"github.com/FrankoonG/hy2scale/internal/app"
)

// diagResolve runs one ResolveViaExit and reports the outcome. Lets
// the operator confirm the relay-routed DNS path is wired correctly:
// upstream reachable, exit dial-able, response parsing OK. Read-only.
func (s *Server) diagResolve(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	exitVia := r.URL.Query().Get("exit_via")
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ips, err := s.app.ResolveViaExit(ctx, name, exitVia)
	resp := map[string]any{
		"name":     name,
		"exit_via": exitVia,
		"ips":      ips,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

// diagPeerHealth — ping ok/fail counters per peer (QUIC-tunnel
// application-level proxy). The hysteria.Client interface doesn't
// expose raw quic-go retransmit / RTT-srtt / pacer-bandwidth, so we
// surface the application-level shape of the same problem: ping
// stream success rate, consecutive failures, and recent byte counts.
// Cumulative since the peer's QUIC session began (counters reset on
// disconnect, not per-window).
func (s *Server) diagPeerHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"peers": s.app.Node().HealthSnapshots()})
}

// diagPeerRebinds — count of [via] rebind-probe arrivals at each
// peer. A stream-level signal: when a peer's QUIC connection
// experiences chronic per-stream disruption (without the connection
// itself dying), downstream streamBridge instances send rebind
// probes at high cadence. Spikes here are the canonical "this one
// peer's tunnel is unhappy but won't admit it" signal.
func (s *Server) diagPeerRebinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"peers": s.app.Node().RebindSnapshots()})
}

// diagPeerLatency — rolling time series of recent latency_ms samples
// per peer (~10 min ring at the 5 s probe cadence). Lets an operator
// pinpoint when a peer's RTT started drifting up, instead of seeing
// only the current point value the topology API surfaces.
func (s *Server) diagPeerLatency(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"peers": s.app.Node().LatencySnapshots()})
}

var startTime = time.Now()

// diagRuntime — process-level counters for leak hunting: goroutines,
// heap, GC, and the relay's own live-object counts. Read-only and cheap
// (one ReadMemStats); safe to poll every few seconds.
func (s *Server) diagRuntime(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st := s.app.Node().GetStats()
	writeJSON(w, map[string]any{
		"goroutines":     runtime.NumGoroutine(),
		"heap_alloc":     ms.HeapAlloc,
		"heap_inuse":     ms.HeapInuse,
		"heap_objects":   ms.HeapObjects,
		"stack_inuse":    ms.StackInuse,
		"sys":            ms.Sys,
		"num_gc":         ms.NumGC,
		"relay_conns":    st.Conns,
		"relay_objects":  s.app.Node().ObjectCounts(),
		"app_objects":    s.app.ObjectCounts(),
		"msess":          s.app.MsessStats(),
		"msess_paths":    s.app.MsessPaths(),
		"uptime_seconds": int64(time.Since(startTime).Seconds()),
	})
}

// handleLogs returns log lines kept by this node (Settings → Storage).
// ?after=SEQ returns only newer lines (from memory); ?limit caps the count
// (newest kept). ?before=SEQ pages back instead: up to limit lines older
// than SEQ, from memory and then from disk, oldest first, with "more" set
// when still older lines exist. While logs are off: {"enabled":false,
// "entries":[]}.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if !logbuf.Default.Enabled() {
		writeJSON(w, map[string]any{"enabled": false, "entries": []logbuf.Entry{}})
		return
	}
	v := r.URL.Query()
	limit, _ := strconv.Atoi(v.Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	if bs := v.Get("before"); bs != "" {
		before, err := strconv.ParseUint(bs, 10, 64)
		if err != nil {
			http.Error(w, "before: a sequence number expected", 400)
			return
		}
		last := logbuf.Default.Last()
		entries := logbuf.Default.Before(before, limit)
		more := false
		if len(entries) < limit {
			b := before
			if len(entries) > 0 {
				b = entries[0].Seq
			}
			older, m := s.app.LogStore().Before(b, limit-len(entries))
			entries = append(older, entries...)
			more = m
		} else if len(entries) > 0 {
			older, _ := s.app.LogStore().Before(entries[0].Seq, 1)
			more = len(older) > 0 || logbuf.Default.Oldest() < entries[0].Seq
		}
		writeJSON(w, map[string]any{"enabled": true, "entries": entries, "last": last, "gap": false, "more": more})
		return
	}
	after, _ := strconv.ParseUint(v.Get("after"), 10, 64)
	entries, last, gap := logbuf.Default.Since(after, limit)
	writeJSON(w, map[string]any{"enabled": true, "entries": entries, "last": last, "gap": gap})
}

// diagPeerHistory — rolled-up time-bucketed metrics (1m / 1h / 1d
// precisions) backing the PathInfoExpand status bars. With ?peer=NAME
// only that peer is returned; otherwise all peers are surfaced so the
// frontend can build per-path views in one round trip.
func (s *Server) diagPeerHistory(w http.ResponseWriter, r *http.Request) {
	h := s.app.History()
	if h == nil {
		writeJSON(w, map[string]any{"peers": map[string]any{}})
		return
	}
	if name := r.URL.Query().Get("peer"); name != "" {
		writeJSON(w, map[string]any{"peer": name, "snapshot": h.Snapshot(name)})
		return
	}
	writeJSON(w, map[string]any{"peers": h.SnapshotAll()})
}

func (s *Server) checkPorts(w http.ResponseWriter, r *http.Request) {
	var req []app.PortConflict
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	conflicts := app.CheckPorts(req)
	writeJSON(w, map[string]any{"conflicts": conflicts})
}

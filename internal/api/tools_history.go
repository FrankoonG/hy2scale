// Targets the tools were run against, newest first, per tool, kept in
// <data>/tools-history.json for the Tools page to offer again.

package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	toolsHistoryDefault = 20
	toolsHistoryCap     = 200
)

// toolsHistory: kinds are "ping", "mtr", "speed" (node paths), "url".
type toolsHistory struct {
	Max   *int                `json:"max,omitempty"` // nil: the default
	Items map[string][]string `json:"items"`
}

var toolsHistoryKinds = []string{"ping", "mtr", "speed", "url"}

type toolsHistoryStore struct {
	mu     sync.Mutex
	path   string
	h      toolsHistory
	loaded bool
}

func (s *toolsHistoryStore) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	if b, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(b, &s.h)
	}
	if s.h.Items == nil {
		s.h.Items = map[string][]string{}
	}
}

func (s *toolsHistoryStore) max() int {
	if s.h.Max == nil {
		return toolsHistoryDefault
	}
	return *s.h.Max
}

func (s *toolsHistoryStore) save() {
	b, _ := json.Marshal(s.h)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, s.path)
	}
}

func (s *toolsHistoryStore) trim() {
	m := s.max()
	for k, v := range s.h.Items {
		if len(v) > m {
			s.h.Items[k] = v[:m]
		}
		if len(s.h.Items[k]) == 0 {
			delete(s.h.Items, k)
		}
	}
}

// add puts targets first in kind's list (moved up when already there).
func (s *toolsHistoryStore) add(kind string, targets ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if s.max() == 0 {
		return
	}
	list := s.h.Items[kind]
	for i := len(targets) - 1; i >= 0; i-- {
		t := strings.TrimSpace(targets[i])
		if t == "" || len(t) > 1024 {
			continue
		}
		out := []string{t}
		for _, x := range list {
			if x != t {
				out = append(out, x)
			}
		}
		list = out
	}
	s.h.Items[kind] = list
	s.trim()
	s.save()
}

func (s *toolsHistoryStore) view() map[string]any {
	items := map[string][]string{}
	for _, k := range toolsHistoryKinds {
		items[k] = append([]string{}, s.h.Items[k]...)
	}
	return map[string]any{"max": s.max(), "items": items}
}

// toolsHistoryRoutes: GET (lists), PUT {max} (the limit; 0 keeps none),
// DELETE (clears).
func (s *Server) toolsHistoryRoutes(m *http.ServeMux) {
	h := &s.toolsHist
	h.path = filepath.Join(s.app.DataDir(), "tools-history.json")
	m.HandleFunc("GET /api/tools/history", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.load()
		writeJSON(w, h.view())
	})
	m.HandleFunc("PUT /api/tools/history", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Max *int `json:"max"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Max == nil || *b.Max < 0 || *b.Max > toolsHistoryCap {
			http.Error(w, "max must be 0..200", 400)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.load()
		h.h.Max = b.Max
		h.trim()
		h.save()
		writeJSON(w, h.view())
	})
	m.HandleFunc("DELETE /api/tools/history", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.load()
		h.h.Items = map[string][]string{}
		h.save()
		writeJSON(w, h.view())
	})
}

// recordTool wraps a tool handler: once the tool has accepted the request
// (it answers 200), its target goes into the history.
func (s *Server) recordTool(kind string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		k, targets := kind, []string{q.Get("host")}
		if kind == "speed" {
			switch q.Get("mode") {
			case "url":
				k, targets = "url", []string{q.Get("url")}
			default:
				targets = []string{q.Get("chain")}
				if q.Has("paths") {
					targets = strings.Split(q.Get("paths"), ",")
				}
			}
		}
		next(&recordWriter{ResponseWriter: w, ok: func() { s.toolsHist.add(k, targets...) }}, r)
	}
}

type recordWriter struct {
	http.ResponseWriter
	ok   func()
	done bool
}

func (w *recordWriter) WriteHeader(code int) {
	if !w.done {
		w.done = true
		if code == http.StatusOK {
			w.ok()
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recordWriter) Write(b []byte) (int, error) {
	if !w.done {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *recordWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection (flushes).
func (w *recordWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

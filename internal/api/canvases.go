// Core: custom topology canvases.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
)

// getTopology returns a tree structure: direct peers with their nested sub-peers.
// getGraphCanvases returns the custom topology canvases:
// { "canvases": [ {id, name, nodes, edges}, ... ] }.
func (s *Server) getGraphCanvases(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{"canvases": s.app.GraphCanvases()})
}

// maxGraphCanvasBody bounds one canvas PUT (2000 nodes + 4000 edges with
// bends fit well inside).
const maxGraphCanvasBody = 2 << 20

// putGraphCanvas creates or replaces one custom canvas. The body is the
// full canvas ({name, nodes, edges}); the id comes from the path. Writes are
// whole-canvas, last writer wins, and every SSE subscriber gets the new
// snapshot.
func (s *Server) putGraphCanvas(w http.ResponseWriter, r *http.Request) {
	var body app.GraphCanvas
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGraphCanvasBody))
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	body.ID = r.PathValue("id")
	if err := app.ValidateGraphCanvas(body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.PutGraphCanvas(body); err != nil {
		code := 500
		if errors.Is(err, app.ErrGraphCanvasLimit) {
			code = 409
		}
		http.Error(w, err.Error(), code)
		return
	}
	s.broadcastGraphCanvases()
	writeJSON(w, map[string]string{"status": "ok"})
}

// deleteGraphCanvas removes one custom canvas.
func (s *Server) deleteGraphCanvas(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteGraphCanvas(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.broadcastGraphCanvases()
	writeJSON(w, map[string]string{"status": "ok"})
}

// broadcastGraphCanvases sends the committed snapshot to every registered
// SSE subscriber. Slow consumers with a full buffer simply drop the update;
// each frame carries the complete list so a missed frame self-heals on the
// next one. Never blocks the write path.
func (s *Server) broadcastGraphCanvases() {
	snap := s.app.GraphCanvases()
	s.layoutMu.Lock()
	subs := make([]chan []app.GraphCanvas, 0, len(s.layoutSubs))
	for _, ch := range s.layoutSubs {
		subs = append(subs, ch)
	}
	s.layoutMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// streamGraphCanvases pushes canvas snapshots to the client as SSE events.
// The first event after connect carries the current state, so the client
// needs no separate GET on mount; later events fire on every write (from
// any session).
func (s *Server) streamGraphCanvases(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable any buffering proxies that would defeat SSE.
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []app.GraphCanvas, 4)
	s.layoutMu.Lock()
	s.layoutSubSeq++
	id := s.layoutSubSeq
	s.layoutSubs[id] = ch
	s.layoutMu.Unlock()
	defer func() {
		s.layoutMu.Lock()
		delete(s.layoutSubs, id)
		s.layoutMu.Unlock()
	}()

	writeEvent := func(cs []app.GraphCanvas) bool {
		payload, err := json.Marshal(map[string]interface{}{"canvases": cs})
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !writeEvent(s.app.GraphCanvases()) {
		return
	}

	// Periodic keepalive so idle connections don't hit HTTP client or
	// proxy timeouts.
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case cs, ok := <-ch:
			if !ok {
				return
			}
			if !writeEvent(cs) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

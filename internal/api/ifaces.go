package api

import (
	"encoding/json"
	"net/http"

	"github.com/FrankoonG/hy2scale/internal/app"
)

// Interfaces page: system interfaces, child interfaces, egress selection.
// See docs/interfaces-design.md.

func (s *Server) registerInterfaceRoutes(authed *http.ServeMux) {
	authed.HandleFunc("GET /api/interfaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.app.Interfaces())
	})
	authed.HandleFunc("POST /api/interfaces", func(w http.ResponseWriter, r *http.Request) {
		var c app.InterfaceConfig
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c, err := s.app.CreateInterface(c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, c)
	})
	authed.HandleFunc("PUT /api/interfaces/{name}", func(w http.ResponseWriter, r *http.Request) {
		var c app.InterfaceConfig
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c, err := s.app.UpdateInterface(r.PathValue("name"), c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, c)
	})
	authed.HandleFunc("GET /api/interfaces/{name}/references", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.app.InterfaceRefs(r.PathValue("name")))
	})
	authed.HandleFunc("DELETE /api/interfaces/{name}", func(w http.ResponseWriter, r *http.Request) {
		refs, err := s.app.DeleteInterface(r.PathValue("name"), r.URL.Query().Get("replacement"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "rewritten": refs})
	})
	authed.HandleFunc("POST /api/interfaces/{name}/renew", func(w http.ResponseWriter, r *http.Request) {
		if err := s.app.RenewInterface(r.PathValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	authed.HandleFunc("PUT /api/egress", func(w http.ResponseWriter, r *http.Request) {
		var e app.EgressConfig
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.app.SetEgress(e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
}

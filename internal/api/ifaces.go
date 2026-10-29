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
	authed.HandleFunc("POST /api/interfaces/{name}/public-ip", func(w http.ResponseWriter, r *http.Request) {
		s.app.RefreshPublicIP(r.PathValue("name"))
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

// selfEgressJSON is this node's egress for the topology.
func selfEgressJSON(a *app.App) json.RawMessage {
	b, _ := json.Marshal(a.SelfEgress())
	return b
}

// publicIPSettings is the public_ip block of GET /api/settings/ui.
func publicIPSettings(cfg app.Config) map[string]any {
	out := map[string]any{"method": "public", "interval": 600, "servers": []string{}}
	if pc := cfg.PublicIP; pc != nil {
		if pc.Method != "" {
			out["method"] = pc.Method
		}
		if pc.Interval > 0 {
			out["interval"] = pc.Interval
		}
		if pc.Servers != nil {
			out["servers"] = pc.Servers
		}
	}
	return out
}

// applyPublicIPSettings validates and stores the public_ip block.
func applyPublicIPSettings(c *app.Config, in *app.PublicIPConfig) {
	pc := *in
	if pc.Method != "peer+public" {
		pc.Method = "public"
	}
	if pc.Interval != 0 && pc.Interval < 60 {
		pc.Interval = 60
	}
	var servers []string
	for _, sv := range pc.Servers {
		if sv != "" {
			servers = append(servers, sv)
		}
	}
	pc.Servers = servers
	c.PublicIP = &pc
}

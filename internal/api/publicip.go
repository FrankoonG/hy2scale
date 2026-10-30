// Add-on public addresses: detection settings and refresh.

package api

import (
	"encoding/json"
	"net/http"

	"github.com/FrankoonG/hy2scale/internal/app"
)

// publicIPRoutes: the routes of the public-address add-on.
func (s *Server) publicIPRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/settings/public-ip", s.getPublicIPSettings)
	m.HandleFunc("PUT /api/settings/public-ip", s.updatePublicIPSettings)
	m.HandleFunc("POST /api/interfaces/{name}/public-ip", func(w http.ResponseWriter, r *http.Request) {
		s.app.RefreshPublicIP(r.PathValue("name"))
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

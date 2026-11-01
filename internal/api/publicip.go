// Add-on public addresses: detection settings and refresh.

package api

import (
	"encoding/json"
	"net/http"

	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
	"github.com/FrankoonG/hy2scale/internal/app"
	"github.com/FrankoonG/hy2scale/internal/publicip"
)

// publicIPRoutes: the routes of the public-address add-on.
func (s *Server) publicIPRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/settings/public-ip", s.getPublicIPSettings)
	m.HandleFunc("PUT /api/settings/public-ip", s.updatePublicIPSettings)
	m.HandleFunc("POST /api/interfaces/{name}/public-ip", func(w http.ResponseWriter, r *http.Request) {
		s.app.RefreshPublicIP(r.PathValue("name"))
		writeJSON(w, map[string]any{"ok": true})
	})
	m.HandleFunc("GET /api/peer-egress", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.app.ProbedEgresses())
	})
}

// selfEgressJSON is this node's egress for the topology.
func selfEgressJSON(a *app.App) json.RawMessage {
	b, _ := json.Marshal(a.SelfEgress())
	return b
}

// cleanEgress is a peer's egress as the topology shows it: per family only
// the address and whether it is known. Nodes no longer send a country
// (1.4.0 pre-releases did; geo information comes from the local GeoIP
// databases), so one received from such a node is dropped. nil / null =
// unknown; anything unparsable is unknown too.
func cleanEgress(raw json.RawMessage) json.RawMessage {
	if raw == nil || string(raw) == "null" {
		return raw
	}
	var e publicip.SelfEgress
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	b, _ := json.Marshal(e)
	return b
}

// cleanAttrs: attrs with the egress attribute cleaned (a copy; the
// received map is shared).
func cleanAttrs(a proto.Attrs) proto.Attrs {
	e, ok := a[proto.AttrEgress]
	if !ok {
		return a
	}
	out := make(proto.Attrs, len(a))
	for k, v := range a {
		out[k] = v
	}
	if c := cleanEgress(e); c != nil {
		out[proto.AttrEgress] = c
	} else {
		delete(out, proto.AttrEgress)
	}
	return out
}

// cleanTreeEgress cleans the egress of every node of a topology subtree.
func cleanTreeEgress(ps []relay.TreePeer) []relay.TreePeer {
	if len(ps) == 0 {
		return ps
	}
	out := make([]relay.TreePeer, len(ps))
	for i, p := range ps {
		p.Egress, p.Attrs = cleanEgress(p.Egress), cleanAttrs(p.Attrs)
		p.Children = cleanTreeEgress(p.Children)
		out[i] = p
	}
	return out
}

// unknownEgress: a topology node's egress is not reported.
func unknownEgress(raw json.RawMessage) bool { return raw == nil || string(raw) == "null" }

// probedTreeEgress fills the probed egress of every node of a (cleaned)
// topology subtree whose egress is not reported; a reported one is kept.
func (s *Server) probedTreeEgress(ps []relay.TreePeer) []relay.TreePeer {
	for i := range ps {
		if unknownEgress(ps[i].Egress) {
			if e, ok := s.app.ProbedEgress(ps[i].Name); ok {
				ps[i].Egress, ps[i].EgressProbed = e, true
			}
		}
		ps[i].Children = s.probedTreeEgress(ps[i].Children)
	}
	return ps
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

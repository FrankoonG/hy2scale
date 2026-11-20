// Links: the share links of one user and the host links of this node
// carry (the public host setting, else the detected address).

package api

import (
	"net/http"
	"strings"

	"github.com/FrankoonG/hy2scale/internal/app"
)

func (s *Server) linkRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/users/{id}/links", s.userLinks)
	m.HandleFunc("GET /api/share-host", s.getShareHost)
}

func lanStrings() []string {
	out := []string{}
	for _, ip := range app.LANAddrs() {
		out = append(out, ip.String())
	}
	return out
}

func (s *Server) userLinks(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByID(r.PathValue("id"))
	if !ok {
		http.Error(w, "user not found", 404)
		return
	}
	out := []app.ProxyLink{}
	// fallback: the host the page was opened through (validated)
	fb := strings.TrimSpace(r.URL.Query().Get("fallback"))
	if !validPublicHost(fb) {
		fb = ""
	}
	out = append(out, s.app.UserLinks(&u, app.LinkOptions{LAN: true, Fallback: fb})...)
	writeJSON(w, out)
}

func (s *Server) getShareHost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"host":      s.app.PublicHost(),
		"detected":  s.app.DetectedPublicHost(),
		"lan_addrs": lanStrings(),
	})
}


package api

import (
	"net/http"
	"runtime"

	"github.com/FrankoonG/hy2scale/core/policy/capture"
)

// System capture routes (docs/v1.4.0-policy.md §3, §7): what the host
// allows, the interfaces layer 2 can take traffic from, and the current
// state.
func (s *Server) policySystemRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/policy/capabilities", func(w http.ResponseWriter, r *http.Request) {
		c := s.app.PolicyCapabilities()
		c.GeoIP = geoStore != nil && geoStore.Available()
		if runtime.GOOS == "linux" {
			c.IfaceTerminal = capture.Avail{Available: true}
		} else {
			c.IfaceTerminal = capture.Avail{Reason: capture.ReasonPlatform}
		}
		writeJSON(w, c)
	})
	m.HandleFunc("GET /api/policy/interfaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.app.PolicyInterfaces())
	})
	m.HandleFunc("GET /api/policy/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.app.PolicyCaptureStatus())
	})
}

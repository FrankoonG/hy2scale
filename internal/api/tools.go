// Add-on tools: network tests run from this node and a terminal on it
// (internal/tools).

package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/FrankoonG/hy2scale/internal/tools"
)

// toolsRoutes: the routes of the tools add-on.
func (s *Server) toolsRoutes(m *http.ServeMux) {
	tools.UserAgent = "hy2scale/" + Version
	m.HandleFunc("GET /api/tools/info", tools.InfoHandler)
	m.HandleFunc("GET /api/tools/ping", s.recordTool("ping", tools.PingHandler))
	m.HandleFunc("GET /api/tools/mtr", s.recordTool("mtr", tools.MTRHandler))
	m.HandleFunc("GET /api/tools/speed", s.recordTool("speed", tools.SpeedHandler(s.dialToolStream)))
	m.HandleFunc("GET /api/tools/terminal", tools.TerminalHandler)
	s.toolsHistoryRoutes(m)
	// speed tests other nodes run toward this one (trusted relay
	// connections only: user credentials never reach the stream, see
	// core/server/access.go)
	s.app.Node().SetSpeedHandler(tools.ServeSpeed)
}

// dialToolStream opens relay stream addr on the node at the end of chain
// (chain[0] a direct peer), as remote management does.
func (s *Server) dialToolStream(ctx context.Context, chain []string, addr string) (net.Conn, error) {
	chain = append([]string(nil), chain...)
	s.mu.RLock()
	for i, name := range chain {
		if newID, ok := s.oldNodeIDs[name]; ok {
			chain[i] = newID
		}
	}
	s.mu.RUnlock()
	chain = s.app.LocalPath(chain)
	if len(chain) == 0 {
		return nil, fmt.Errorf("the path names only this node")
	}
	// the hops a user's exit may take, and no other (nested rules); a hop
	// that is offline is left to the dial, which says why
	if st := s.app.PathStatus(strings.Join(chain, "/")); !st.OK && st.Reason != "offline" {
		return nil, fmt.Errorf("%s: path refused at %s (%s)", strings.Join(chain, "/"), chain[st.Hop], st.Reason)
	}
	n := s.app.Node()
	target := chain[len(chain)-1]
	if p, ok := s.app.Exit().NodeInfoAt(chain); ok && p.Native || len(chain) == 1 && n.NativeMap()[target] {
		return nil, fmt.Errorf("%s is a plain hysteria2 server: no speed test", target)
	}
	if n.IsRestricted(chain[0]) {
		return nil, fmt.Errorf("not available through node access by user")
	}
	var conn net.Conn
	var err error
	if len(chain) == 1 {
		conn, err = n.DialTCP(ctx, chain[0], addr)
	} else {
		conn, err = n.DialVia(ctx, chain, addr)
	}
	if err != nil {
		return nil, fmt.Errorf("tunnel to %s failed: %v", strings.Join(chain, "/"), err)
	}
	return conn, nil
}

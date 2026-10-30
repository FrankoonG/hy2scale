package app

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/core/exit"
	"github.com/FrankoonG/hy2scale/core/msess"
	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
	"github.com/FrankoonG/hy2scale/core/tlsstore"
)

// MsessMinVersion is the first release whose exit side speaks msess
// (lossless quality/aggregate). The UI flags older nodes against it.
const MsessMinVersion = exit.MsessMinVersion

// exitHost: the engine's view of this node (core/exit.Host).
type exitHost struct{ a *App }

func (h exitHost) SelfID() string { return h.a.store.Get().NodeID }

func (h exitHost) Nested(path string) bool {
	pc, ok := h.a.store.Get().Peers[path]
	return ok && pc.Nested
}

func (h exitHost) Disabled(name string) bool {
	cfg := h.a.store.Get()
	if pc, ok := cfg.Peers[name]; ok && pc.Disabled {
		return true
	}
	if strings.Contains(name, "/") {
		return false
	}
	for _, cl := range cfg.Clients {
		if cl.Name == name && cl.Disabled {
			return true
		}
	}
	return false
}

func (h exitHost) PathLossGrace() time.Duration {
	return time.Duration(h.a.store.Get().PathLossGraceS) * time.Second
}

func (h exitHost) DialLocal(ctx context.Context, network, addr string) (net.Conn, error) {
	return h.a.dialDirect(ctx, network, addr)
}

func (h exitHost) Lookup(ctx context.Context, name string) ([]net.IP, error) {
	return h.a.egressLookup(ctx, name)
}

func (a *App) Exit() *exit.Engine { return a.exit }

// SanitizeExitMode: see exit.SanitizeExitMode.
func SanitizeExitMode(exitPaths []string, exitMode string) (string, error) {
	return exit.SanitizeExitMode(exitPaths, exitMode)
}

func (a *App) dialExitWithPaths(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, error) {
	return a.exit.Dial(ctx, exit.Route{Exit: exitVia, Paths: exitPaths, Mode: exitMode}, "tcp", addr)
}

func (a *App) dialExitUDPPaths(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, error) {
	return a.exit.Dial(ctx, exit.Route{Exit: exitVia, Paths: exitPaths, Mode: exitMode}, "udp", addr)
}

func (a *App) dialExit(ctx context.Context, exitVia, addr string) (net.Conn, error) {
	return a.exit.DialPath(ctx, exitVia, addr)
}

func (a *App) isExitPathDisabled(path string) bool { return a.exit.PathDisabled(path) }

func (a *App) findPathsTo(target string) []string { return a.exit.FindPaths(target) }

func (a *App) nodeInfoAt(parts []string) (relay.PeerInfo, bool) { return a.exit.NodeInfoAt(parts) }

func (a *App) chainTags(parts []string) bool { return a.exit.ChainTags(parts) }

func (a *App) tagTarget(ctx context.Context, parts []string, addr string) string {
	return a.exit.TagTarget(ctx, parts, addr)
}

func (a *App) handleBondStream(addr string, conn net.Conn) error {
	return a.exit.HandleBondStream(addr, conn)
}

// IsBondStream: addr opens a path of a bonded stream.
func IsBondStream(addr string) bool { return exit.IsBondStream(addr) }

func rewriteLocalAddr(addr string) string { return exit.RewriteLocalAddr(addr) }

func withRestrict(ctx context.Context, r proto.Restrict) context.Context {
	return exit.WithRestrict(ctx, r)
}

func restrictOf(ctx context.Context) proto.Restrict { return exit.RestrictOf(ctx) }

// PathLossGrace is the effective no-path grace for new sessions.
func (a *App) PathLossGrace() time.Duration { return a.exit.PathLossGrace() }

// MsessPaths reports the ingress prober's per-path scores (diagnostics).
func (a *App) MsessPaths() []msess.PathScore { return a.exit.MsessPaths() }

// MsessStats reports session-layer counters for /api/diag/runtime.
func (a *App) MsessStats() map[string]int { return a.exit.MsessStats() }

func splitPath(s string) []string {
	var parts []string
	for _, p := range strings.Split(s, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// TLSStore: the node's certificate store (core/tlsstore).
type TLSStore = tlsstore.Store

// CertInfo describes a stored certificate.
type CertInfo = tlsstore.CertInfo

func NewTLSStore(dataDir string) *TLSStore { return tlsstore.New(dataDir) }

func (h exitHost) DNSUpstreams() string { return h.a.store.Get().DNS }

// ApplyDNSResolver installs the resolver settings and purges its cache.
func ApplyDNSResolver(c DNSResolverConfig) { exit.ApplyResolver(c) }

// PurgeDNSCache empties the resolver cache.
func PurgeDNSCache() { exit.PurgeResolverCache() }

// ResolveViaExit resolves name through exitVia (exit.Engine.Resolve).
func (a *App) ResolveViaExit(ctx context.Context, name, exitVia string) ([]string, error) {
	return a.exit.Resolve(ctx, name, exitVia)
}

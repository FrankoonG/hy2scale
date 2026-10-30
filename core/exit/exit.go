// Package exit carries flows from this node to their exit: through a path
// of peers (the nested iron rules, docs/nested-rules.md), in direct,
// quality (failover) or aggregate mode, over lossless multi-path sessions
// (core/msess) when the exit speaks them, or out of this node itself.
//
// It also serves the exit side: sessions and bonded streams arriving from
// other nodes are dialled here, under the restrictions their tags carry.
package exit

import (
	"context"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
)

// Host is what the engine needs from the node it runs in.
type Host interface {
	// SelfID: the local node id.
	SelfID() string
	// Nested: the peer path ("a", "a/b") is nested-enabled here.
	Nested(path string) bool
	// Disabled: the peer (by name) or the peer path is disabled here.
	Disabled(name string) bool
	// DNSUpstreams: the upstream resolvers, comma-separated (port 53 when
	// omitted; "" = defaults).
	DNSUpstreams() string
	// PathLossGrace: the configured no-path grace (0 = default).
	PathLossGrace() time.Duration
	// DialLocal dials addr from this node (its egress), applying the
	// restrictions carried by ctx (RestrictOf).
	DialLocal(ctx context.Context, network, addr string) (net.Conn, error)
	// Lookup resolves a name the way DialLocal does.
	Lookup(ctx context.Context, name string) ([]net.IP, error)
}

// Route is where a flow leaves: Exit is the exit path ("" = this node),
// Paths the alternative paths to it and Mode "" (direct), "quality" or
// "aggregate".
type Route struct {
	Exit  string
	Paths []string
	Mode  string
}

// Engine dials flows to their exits and serves the exit side.
type Engine struct {
	node *relay.Node
	host Host
	ctx  context.Context
	ms   *msessState

	activePathMu sync.RWMutex
	activePaths  map[string]string // exit → last winning path
}

// New installs the exit side on n (sessions, bonded streams) and returns
// the engine; Start gives it its lifetime.
func New(n *relay.Node, h Host) *Engine {
	e := &Engine{node: n, host: h, ctx: context.Background(), activePaths: map[string]string{}}
	e.initMsess()
	return e
}

// Start: ctx bounds the exit-side dials started by the engine itself.
func (e *Engine) Start(ctx context.Context) { e.ctx = ctx }

// Dial opens a flow to addr ("host:port") on network "tcp" or "udp" through
// the route. Restrictions carried by ctx apply at the exit.
func (e *Engine) Dial(ctx context.Context, r Route, network, addr string) (net.Conn, error) {
	if network == "udp" {
		return e.dialExitUDPPaths(ctx, r.Exit, r.Paths, r.Mode, addr)
	}
	return e.dialExitWithPaths(ctx, r.Exit, r.Paths, r.Mode, addr)
}

// DialPath dials addr over one exit path in direct mode.
func (e *Engine) DialPath(ctx context.Context, exitVia, addr string) (net.Conn, error) {
	return e.dialExit(ctx, exitVia, addr)
}

// PathDisabled: a hop or a prefix of the path is disabled here.
func (e *Engine) PathDisabled(path string) bool { return e.isExitPathDisabled(path) }

// Authorised: every hop before the last one of parts is nested-enabled.
func (e *Engine) Authorised(parts []string) bool { return e.authorised(parts) }

// FindPaths: the paths this node knows to the node target.
func (e *Engine) FindPaths(target string) []string { return e.findPathsTo(target) }

// NodeInfoAt: what this node knows of the last hop of parts.
func (e *Engine) NodeInfoAt(parts []string) (relay.PeerInfo, bool) { return e.nodeInfoAt(parts) }

// ChainTags: every node of parts applies restriction tags.
func (e *Engine) ChainTags(parts []string) bool { return e.chainTags(parts) }

// ActivePath: the path the last quality connection to exit took.
func (e *Engine) ActivePath(exit string) string {
	e.activePathMu.RLock()
	defer e.activePathMu.RUnlock()
	return e.activePaths[exit]
}

// ActivePaths: ActivePath of every exit.
func (e *Engine) ActivePaths() map[string]string {
	e.activePathMu.RLock()
	defer e.activePathMu.RUnlock()
	cp := make(map[string]string, len(e.activePaths))
	for k, v := range e.activePaths {
		cp[k] = v
	}
	return cp
}

// HandleBondStream serves one path of a bonded stream arriving here.
func (e *Engine) HandleBondStream(addr string, conn net.Conn) error {
	return e.handleBondStream(addr, conn)
}

// Counts: object counts for diagnostics.
func (e *Engine) Counts() map[string]int {
	return map[string]int{"bond_receivers": syncMapLen(&bondReceivers), "adaptive_scores": syncMapLen(&adaptiveScores)}
}

func syncMapLen(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ any) bool { n++; return true })
	return n
}

func (e *Engine) dialLocal(ctx context.Context, network, addr string) (net.Conn, error) {
	return e.host.DialLocal(ctx, network, addr)
}

// Restrictions on what a flow may reach. They ride on the dial context
// here; a flow leaving through another node carries them as a tag on its
// target (proto.Restrict), applied by a 1.4.0+ exit to its own dial.

// WithRestrict adds r to the restrictions carried by ctx.
func WithRestrict(ctx context.Context, r proto.Restrict) context.Context {
	return proto.ContextRestrict(ctx, r)
}

// RestrictOf: the restrictions carried by ctx.
func RestrictOf(ctx context.Context) proto.Restrict { return proto.RestrictFrom(ctx) }

// TagTarget adds the restrictions carried by ctx to a target dialled
// through parts, when every node on it understands the tag.
func (e *Engine) TagTarget(ctx context.Context, parts []string, addr string) string {
	return e.tagTarget(ctx, parts, addr)
}

var debugMode = func() bool {
	v := strings.ToLower(os.Getenv("DEBUG"))
	return v == "true" || v == "1" || v == "yes"
}()

func debugLog(format string, args ...any) {
	if debugMode {
		log.Printf(format, args...)
	}
}

// DialLocalTCP dials addr from this node (its egress).
func (e *Engine) DialLocalTCP(ctx context.Context, addr string) (net.Conn, error) {
	return e.dialLocal(ctx, "tcp", addr)
}

// DialLocalUDP opens a datagram flow to addr from this node.
func (e *Engine) DialLocalUDP(ctx context.Context, addr string) (net.Conn, error) {
	return e.dialLocal(ctx, "udp", addr)
}

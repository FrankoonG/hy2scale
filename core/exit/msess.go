package exit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/msess"
	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
)

// MsessMinVersion is the first release whose exit side speaks msess
// (lossless quality/aggregate). The UI flags older nodes against it.
const MsessMinVersion = "1.3.10"

// ConnHintMinVersion is the first release whose relay hops read a
// relay.ConnHint (carry the next hop over a chosen connection).
const ConnHintMinVersion = "1.3.12"

// msessLegacyTTL: an exit that refused a session is re-tried after this.
const msessLegacyTTL = 5 * time.Minute

type msessState struct {
	server *msess.Server
	// restricted: sessions whose subflows arrived with restrictions (an
	// access client's or a restricted user's); its targets are checked.
	restricted *msess.Server
	prober     *msess.Prober
	mu         sync.Mutex
	legacy     map[string]time.Time // exit → don't try msess before
	noted      map[string]time.Time // exit → last "needs update" log
}

// initMsess wires the exit side (subflows arriving on relay.StreamMsess)
// and the shared path prober used by this node's ingress sessions.
func (e *Engine) initMsess() {
	e.ms = &msessState{legacy: map[string]time.Time{}, noted: map[string]time.Time{}}
	server := func(r proto.Restrict) *msess.Server {
		return msess.NewServer(msess.ServerConfig{
			DialTarget: func(ctx context.Context, addr string) (net.Conn, error) {
				return e.dialLocal(WithRestrict(ctx, r), "tcp", RewriteLocalAddr(addr))
			},
			DialPacketTarget: func(ctx context.Context, addr string) (net.Conn, error) {
				return e.dialLocal(WithRestrict(ctx, r), "udp", RewriteLocalAddr(addr))
			},
			Logf: log.Printf,
		})
	}
	e.ms.server = server(0)
	e.ms.restricted = server(proto.DenyInternal)
	e.ms.prober = msess.NewProber(nil)
	e.node.SetMsessHandler(func(c net.Conn) {
		if relay.RestrictOf(c)&proto.DenyInternal != 0 {
			e.ms.restricted.Handle(c)
			return
		}
		e.ms.server.Handle(c)
	})
}

func (m *msessState) isLegacy(exit string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.legacy[exit]
	if ok && time.Now().After(until) {
		delete(m.legacy, exit)
		return false
	}
	return ok
}

// noteLegacy logs (at most every 10 min per exit) that an exit is too old
// for lossless failover, so operators know which node to update.
func (m *msessState) noteLegacy(exit, version string) {
	m.mu.Lock()
	last := m.noted[exit]
	due := time.Since(last) > 10*time.Minute
	if due {
		m.noted[exit] = time.Now()
	}
	m.mu.Unlock()
	if due {
		log.Printf("[msess] exit %s runs %s: connections through it have no lossless failover until it is updated to %s+",
			exit, version, MsessMinVersion)
	}
}

func (m *msessState) markLegacy(exits []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range exits {
		m.legacy[e] = time.Now().Add(msessLegacyTTL)
	}
}

// dialMsess opens a lossless multi-path session for quality/aggregate.
// ok=false means "not applicable" (direct mode, local exit, legacy exit):
// the caller continues with the pre-1.3.10 dispatch.
func (e *Engine) dialMsess(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, bool, error) {
	return e.dialMsessKind(ctx, exitVia, exitPaths, exitMode, addr, false)
}

// dialMsessPacket: the UDP counterpart — one exit-side UDP socket per flow
// that survives path changes (a re-created flow changed the source port).
func (e *Engine) dialMsessPacket(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, bool, error) {
	return e.dialMsessKind(ctx, exitVia, exitPaths, exitMode, addr, true)
}

func (e *Engine) dialMsessKind(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string, packet bool) (net.Conn, bool, error) {
	var mode msess.Mode
	switch exitMode {
	case "quality", "stability":
		mode = msess.ModeSelector
	case "aggregate", "speed":
		mode = msess.ModeBond
	default:
		return nil, false, nil
	}
	paths := e.msessPaths(exitVia, exitPaths, RestrictOf(ctx))
	if len(paths) == 0 {
		return nil, false, nil
	}
	cfg := msess.DialConfig{Mode: mode, Target: addr, Paths: paths, Prober: e.ms.prober,
		Grace: e.PathLossGrace(), Logf: log.Printf}
	dial := msess.Dial
	if packet {
		dial = msess.DialPacket
	}
	c, err := dial(ctx, cfg)
	if errors.Is(err, msess.ErrUnsupported) {
		exits := map[string]bool{}
		var list []string
		for _, p := range paths {
			if !exits[p.Exit] {
				exits[p.Exit] = true
				list = append(list, p.Exit)
			}
		}
		e.markLegacyIf(err, list)
		return nil, false, nil
	}
	if errors.Is(err, msess.ErrBusy) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	active := c.Exit()
	for _, sf := range c.Subflows() {
		if sf.Active || mode == msess.ModeBond {
			active, _, _ = strings.Cut(sf.Path, "@") // "x@1" is carrier 1 of path x
			break
		}
	}
	e.activePathMu.Lock()
	e.activePaths[exitVia] = active
	e.activePathMu.Unlock()
	return c, true, nil
}

// msessPaths builds the candidate carriers: every configured exit path
// (or exit_via), auto-expanded with one-intermediate alternatives when no
// explicit list was given, split per QUIC connection for multi-address
// first hops, minus exits known not to speak msess.
func (e *Engine) msessPaths(exitVia string, exitPaths []string, r proto.Restrict) []msess.Path {
	specs := exitPaths
	if len(specs) == 0 {
		specs = []string{exitVia}
	}
	var chains [][]string
	seen := map[string]bool{}
	add := func(parts []string) {
		k := strings.Join(parts, "/")
		if !seen[k] {
			seen[k] = true
			chains = append(chains, parts)
		}
	}
	for _, sp := range specs {
		if sp == "" || e.isExitPathDisabled(sp) {
			continue
		}
		parts, err := e.exitChain(sp)
		if err != nil || len(parts) == 0 {
			continue
		}
		if e.node.HasPeer(parts[0]) {
			add(parts)
		}
		if len(exitPaths) <= 1 && len(parts) == 1 {
			for _, alt := range e.msessAltPaths(parts[0]) {
				add(alt)
			}
		}
	}
	// The preferred (first configured) exit can't do sessions: keep the
	// pre-1.3.10 behaviour for this user rather than silently moving them
	// to another exit while the mesh is only partly upgraded.
	if len(chains) > 0 && e.msessLegacy(chains[0]) {
		return nil
	}
	var out []msess.Path
	for _, parts := range chains {
		if !e.msessLegacy(parts) {
			out = append(out, e.msessCarriers(parts, r)...)
		}
	}
	return out
}

// msessLegacy: the exit of parts is known not to speak msess (an older
// hy2scale, or a native hysteria server).
func (e *Engine) msessLegacy(parts []string) bool {
	v, native := e.nodeVersionAt(parts)
	if native {
		return true
	}
	if v != "" && !relay.VersionAtLeast(v, MsessMinVersion) {
		e.ms.noteLegacy(parts[len(parts)-1], v)
		return true
	}
	return e.ms.isLegacy(parts[len(parts)-1])
}

// msessCarriers: the subflow routes of one exit chain — one per QUIC
// connection of every hop reached over several addresses. Subflow i takes
// connection i (modulo their number) at each such hop: the first hop
// locally, a later hop through a relay.ConnHint read by the node dialing it.
func (e *Engine) msessCarriers(parts []string, r proto.Restrict) []msess.Path {
	exit := parts[len(parts)-1]
	name := strings.Join(parts, "/")
	hop := parts[0]
	v, _ := e.nodeVersionAt(parts)
	capable := v != "" && relay.VersionAtLeast(v, MsessMinVersion)
	// The flow's restrictions ride on every subflow address, when every
	// node on the chain understands the tag (access.go).
	if r != 0 && !e.chainTags(parts) {
		r = 0
	}
	subflow := func(hello []byte) string { return proto.WithRestrict(relay.MsessAddr(hello), r) }
	if len(parts) > 1 {
		first := e.node.PeerConnCount(hop)
		conns := e.hopConns(parts)
		n := first
		for _, c := range conns {
			if c > n {
				n = c
			}
		}
		if n > msess.MaxSubflows {
			n = msess.MaxSubflows
		}
		if n <= 1 {
			chain := parts
			return []msess.Path{{Name: name, Exit: exit, Capable: capable,
				Dial: func(ctx context.Context) (net.Conn, error) {
					return e.node.DialVia(ctx, chain, relay.StreamMsess)
				},
				Open: func(ctx context.Context, hello []byte) (net.Conn, error) {
					c, err := e.node.DialVia(ctx, chain, subflow(hello))
					return c, msessOpenErr(err)
				}}}
		}
		out := make([]msess.Path, 0, n)
		for i := 0; i < n; i++ {
			chain := append([]string(nil), parts...)
			for j, c := range conns {
				if c > 1 {
					chain[j+1] = relay.ConnHint(parts[j+1], i%c)
				}
			}
			idx := -1
			if first > 1 {
				idx = i % first
			}
			out = append(out, msess.Path{Name: fmt.Sprintf("%s@%d", name, i), Exit: exit, Capable: capable,
				Dial: func(ctx context.Context) (net.Conn, error) {
					return e.node.DialViaIdx(ctx, chain, relay.StreamMsess, idx)
				},
				Open: func(ctx context.Context, hello []byte) (net.Conn, error) {
					c, err := e.node.DialViaIdx(ctx, chain, subflow(hello), idx)
					return c, msessOpenErr(err)
				}})
		}
		return out
	}
	if k := e.node.PeerConnCount(hop); k > 1 {
		out := make([]msess.Path, 0, k)
		for i := 0; i < k; i++ {
			idx := i
			out = append(out, msess.Path{Name: fmt.Sprintf("%s@%d", name, i), Exit: exit, Capable: capable,
				Dial: func(ctx context.Context) (net.Conn, error) {
					return e.node.DialTCPIdx(ctx, hop, relay.StreamMsess, idx)
				},
				Open: func(ctx context.Context, hello []byte) (net.Conn, error) {
					c, err := e.node.DialTCPIdx(ctx, hop, subflow(hello), idx)
					return c, msessOpenErr(err)
				}})
		}
		return out
	}
	return []msess.Path{{Name: name, Exit: exit, Capable: capable,
		Dial: func(ctx context.Context) (net.Conn, error) {
			return e.node.DialTCP(ctx, hop, relay.StreamMsess)
		},
		Open: func(ctx context.Context, hello []byte) (net.Conn, error) {
			c, err := e.node.DialTCP(ctx, hop, subflow(hello))
			return c, msessOpenErr(err)
		}}}
}

// hopConns: for each hop after the first, the number of connections the
// node dialing it holds to it — 0 where that node can't take a ConnHint
// (older than ConnHintMinVersion) or the count isn't known.
func (e *Engine) hopConns(parts []string) []int {
	out := make([]int, len(parts)-1)
	for j := 1; j < len(parts); j++ {
		if v, _ := e.nodeVersionAt(parts[:j]); v == "" || !relay.VersionAtLeast(v, ConnHintMinVersion) {
			continue
		}
		kids, ok := e.node.PeersOfCached(strings.Join(parts[:j], "/"))
		if !ok {
			continue
		}
		for _, k := range kids {
			if k.Name == parts[j] {
				out[j-1] = k.Conns
				break
			}
		}
	}
	return out
}

// errSubflowDialed: the far end took the subflow address for a TCP target
// and tried to dial it — a definitive sign it predates msess.
var errSubflowDialed = errors.New("subflow address dialed as a target")

// msessOpenErr maps a remote dial error while opening a subflow.
func msessOpenErr(err error) error {
	if err != nil && errors.Is(err, relay.ErrRemoteDial) {
		return fmt.Errorf("%w: %w: %v", msess.ErrUnsupported, errSubflowDialed, err)
	}
	return err
}

// markLegacyIf remembers exits as legacy only on the definitive signal; a
// stream closed during the handshake of an exit whose version is not known
// yet (it just reconnected) only sends this one connection the old way.
func (e *Engine) markLegacyIf(err error, exits []string) {
	if errors.Is(err, errSubflowDialed) {
		log.Printf("[msess] %v: exits %v use the legacy dispatch for %s", err, exits, msessLegacyTTL)
		e.ms.markLegacy(exits)
	}
}

// dialMsessDirect carries a direct-mode connection over a session pinned
// to the configured exit chain: a lost carrier or a reconnected hop within
// the no-path grace is survived byte-exact, and with no path for longer
// the connection closes. ok=false: the exit predates msess.
func (e *Engine) dialMsessDirect(ctx context.Context, parts []string, addr string) (net.Conn, bool, error) {
	if e.msessLegacy(parts) {
		return nil, false, nil
	}
	exit := parts[len(parts)-1]
	c, err := msess.Dial(ctx, msess.DialConfig{Mode: msess.ModeSelector, Target: addr,
		Paths: e.msessCarriers(parts, RestrictOf(ctx)), Prober: e.ms.prober, Grace: e.PathLossGrace(),
		NoProbeWait: true, Logf: log.Printf})
	if errors.Is(err, msess.ErrUnsupported) {
		e.markLegacyIf(err, []string{exit})
		return nil, false, nil
	}
	if errors.Is(err, msess.ErrBusy) {
		return nil, false, nil // this connection only: plain stream
	}
	if err != nil {
		return nil, true, err
	}
	return c, true, nil
}

// msessAltPaths: target reached through one intermediate direct peer,
// using only that peer's cached sub-peer list (iron rule 3) and the
// nested gate (rule 2). No live queries on the dial path.
func (e *Engine) msessAltPaths(target string) [][]string {
	self := map[string]bool{e.node.Name(): true, e.host.SelfID(): true}
	var out [][]string
	for _, p := range e.node.Peers() {
		if p.Name == target || p.Native || self[p.Name] {
			continue
		}
		if !e.authorised([]string{p.Name, target}) || e.isExitPathDisabled(p.Name+"/"+target) {
			continue
		}
		kids, ok := e.node.PeersOfCached(p.Name)
		if !ok {
			continue
		}
		for _, k := range kids {
			if k.Name == target {
				out = append(out, []string{p.Name, target})
				break
			}
		}
	}
	return out
}

// nodeVersionAt: advertised version of the last hop of parts ("" = unknown)
// and whether it is a native hysteria server.
func (e *Engine) nodeVersionAt(parts []string) (string, bool) {
	p, _ := e.nodeInfoAt(parts)
	return p.Version, p.Native
}

// nodeInfoAt: what this node knows of the last hop of parts (a direct
// peer's own report, or the parent's cached peer list).
func (e *Engine) nodeInfoAt(parts []string) (relay.PeerInfo, bool) {
	last := parts[len(parts)-1]
	var infos []relay.PeerInfo
	if len(parts) == 1 {
		infos = e.node.Peers()
	} else if kids, ok := e.node.PeersOfCached(strings.Join(parts[:len(parts)-1], "/")); ok {
		infos = kids
	}
	for _, p := range infos {
		if p.Name == last {
			return p, true
		}
	}
	return relay.PeerInfo{}, false
}

// PathLossGrace is the effective no-path grace for new sessions.
func (e *Engine) PathLossGrace() time.Duration {
	return msess.ClampGrace(e.host.PathLossGrace())
}

// MsessPaths reports the ingress prober's per-path scores (diagnostics).
func (e *Engine) MsessPaths() []msess.PathScore { return e.ms.prober.Snapshot() }

// MsessStats reports session-layer counters for /api/diag/runtime.
func (e *Engine) MsessStats() map[string]int {
	st, rs := e.ms.server.Stats(), e.ms.restricted.Stats()
	return map[string]int{"exit_sessions": st.Sessions + rs.Sessions, "exit_subflows": st.Subflows + rs.Subflows,
		"exit_pending": st.Pending + rs.Pending, "probed_paths": e.ms.prober.Count()}
}

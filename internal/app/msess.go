package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/internal/msess"
	"github.com/FrankoonG/hy2scale/internal/relay"
)

// MsessMinVersion is the first release whose exit side speaks msess
// (lossless quality/aggregate). The UI flags older nodes against it.
const MsessMinVersion = "1.3.10"

// msessLegacyTTL: an exit that refused a session is re-tried after this.
const msessLegacyTTL = 5 * time.Minute

type msessState struct {
	server *msess.Server
	prober *msess.Prober
	mu     sync.Mutex
	legacy map[string]time.Time // exit → don't try msess before
	noted  map[string]time.Time // exit → last "needs update" log
}

// initMsess wires the exit side (subflows arriving on relay.StreamMsess)
// and the shared path prober used by this node's ingress sessions.
func (a *App) initMsess() {
	a.ms = &msessState{legacy: map[string]time.Time{}, noted: map[string]time.Time{}}
	a.ms.server = msess.NewServer(msess.ServerConfig{
		DialTarget: func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", rewriteLocalAddr(addr))
		},
		DialPacketTarget: func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", rewriteLocalAddr(addr))
		},
		Logf: log.Printf,
	})
	a.ms.prober = msess.NewProber(nil)
	a.node.SetMsessHandler(a.ms.server.Handle)
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
func (a *App) dialMsess(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, bool, error) {
	return a.dialMsessKind(ctx, exitVia, exitPaths, exitMode, addr, false)
}

// dialMsessPacket: the UDP counterpart — one exit-side UDP socket per flow
// that survives path changes (a re-created flow changed the source port).
func (a *App) dialMsessPacket(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, bool, error) {
	return a.dialMsessKind(ctx, exitVia, exitPaths, exitMode, addr, true)
}

func (a *App) dialMsessKind(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string, packet bool) (net.Conn, bool, error) {
	var mode msess.Mode
	switch exitMode {
	case "quality", "stability":
		mode = msess.ModeSelector
	case "aggregate", "speed":
		mode = msess.ModeBond
	default:
		return nil, false, nil
	}
	paths := a.msessPaths(exitVia, exitPaths)
	if len(paths) == 0 {
		return nil, false, nil
	}
	cfg := msess.DialConfig{Mode: mode, Target: addr, Paths: paths, Prober: a.ms.prober,
		Grace: a.PathLossGrace(), Logf: log.Printf}
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
		a.markLegacyIf(err, list)
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
	a.activePathMu.Lock()
	a.activePaths[exitVia] = active
	a.activePathMu.Unlock()
	return c, true, nil
}

// msessPaths builds the candidate carriers: every configured exit path
// (or exit_via), auto-expanded with one-intermediate alternatives when no
// explicit list was given, split per QUIC connection for multi-address
// first hops, minus exits known not to speak msess.
func (a *App) msessPaths(exitVia string, exitPaths []string) []msess.Path {
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
		if sp == "" || a.isExitPathDisabled(sp) {
			continue
		}
		parts, err := a.exitChain(sp)
		if err != nil || len(parts) == 0 {
			continue
		}
		if a.node.HasPeer(parts[0]) {
			add(parts)
		}
		if len(exitPaths) <= 1 && len(parts) == 1 {
			for _, alt := range a.msessAltPaths(parts[0]) {
				add(alt)
			}
		}
	}
	// The preferred (first configured) exit can't do sessions: keep the
	// pre-1.3.10 behaviour for this user rather than silently moving them
	// to another exit while the mesh is only partly upgraded.
	if len(chains) > 0 && a.msessLegacy(chains[0]) {
		return nil
	}
	var out []msess.Path
	for _, parts := range chains {
		if !a.msessLegacy(parts) {
			out = append(out, a.msessCarriers(parts)...)
		}
	}
	return out
}

// msessLegacy: the exit of parts is known not to speak msess (an older
// hy2scale, or a native hysteria server).
func (a *App) msessLegacy(parts []string) bool {
	v, native := a.nodeVersionAt(parts)
	if native {
		return true
	}
	if v != "" && !relay.VersionAtLeast(v, MsessMinVersion) {
		a.ms.noteLegacy(parts[len(parts)-1], v)
		return true
	}
	return a.ms.isLegacy(parts[len(parts)-1])
}

// msessCarriers: the subflow routes of one exit chain — one per QUIC
// connection when the first hop is reached over several addresses.
func (a *App) msessCarriers(parts []string) []msess.Path {
	exit := parts[len(parts)-1]
	name := strings.Join(parts, "/")
	hop := parts[0]
	v, _ := a.nodeVersionAt(parts)
	capable := v != "" && relay.VersionAtLeast(v, MsessMinVersion)
	if len(parts) > 1 {
		chain := parts
		return []msess.Path{{Name: name, Exit: exit, Capable: capable,
			Dial: func(ctx context.Context) (net.Conn, error) {
				return a.node.DialVia(ctx, chain, relay.StreamMsess)
			},
			Open: func(ctx context.Context, hello []byte) (net.Conn, error) {
				c, err := a.node.DialVia(ctx, chain, relay.MsessAddr(hello))
				return c, msessOpenErr(err)
			}}}
	}
	if k := a.node.PeerConnCount(hop); k > 1 {
		out := make([]msess.Path, 0, k)
		for i := 0; i < k; i++ {
			idx := i
			out = append(out, msess.Path{Name: fmt.Sprintf("%s@%d", name, i), Exit: exit, Capable: capable,
				Dial: func(ctx context.Context) (net.Conn, error) {
					return a.node.DialTCPIdx(ctx, hop, relay.StreamMsess, idx)
				},
				Open: func(ctx context.Context, hello []byte) (net.Conn, error) {
					c, err := a.node.DialTCPIdx(ctx, hop, relay.MsessAddr(hello), idx)
					return c, msessOpenErr(err)
				}})
		}
		return out
	}
	return []msess.Path{{Name: name, Exit: exit, Capable: capable,
		Dial: func(ctx context.Context) (net.Conn, error) {
			return a.node.DialTCP(ctx, hop, relay.StreamMsess)
		},
		Open: func(ctx context.Context, hello []byte) (net.Conn, error) {
			c, err := a.node.DialTCP(ctx, hop, relay.MsessAddr(hello))
			return c, msessOpenErr(err)
		}}}
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
func (a *App) markLegacyIf(err error, exits []string) {
	if errors.Is(err, errSubflowDialed) {
		log.Printf("[msess] %v: exits %v use the legacy dispatch for %s", err, exits, msessLegacyTTL)
		a.ms.markLegacy(exits)
	}
}

// dialMsessDirect carries a direct-mode connection over a session pinned
// to the configured exit chain: a lost carrier or a reconnected hop within
// the no-path grace is survived byte-exact, and with no path for longer
// the connection closes. ok=false: the exit predates msess.
func (a *App) dialMsessDirect(ctx context.Context, parts []string, addr string) (net.Conn, bool, error) {
	if a.msessLegacy(parts) {
		return nil, false, nil
	}
	exit := parts[len(parts)-1]
	c, err := msess.Dial(ctx, msess.DialConfig{Mode: msess.ModeSelector, Target: addr,
		Paths: a.msessCarriers(parts), Prober: a.ms.prober, Grace: a.PathLossGrace(),
		NoProbeWait: true, Logf: log.Printf})
	if errors.Is(err, msess.ErrUnsupported) {
		a.markLegacyIf(err, []string{exit})
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
func (a *App) msessAltPaths(target string) [][]string {
	cfg := a.store.Get()
	self := map[string]bool{a.node.Name(): true, cfg.NodeID: true}
	var out [][]string
	for _, p := range a.node.Peers() {
		if p.Name == target || p.Native || self[p.Name] {
			continue
		}
		if !isPathAuthorised([]string{p.Name, target}, cfg) || a.isExitPathDisabled(p.Name+"/"+target) {
			continue
		}
		kids, ok := a.node.PeersOfCached(p.Name)
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
func (a *App) nodeVersionAt(parts []string) (string, bool) {
	last := parts[len(parts)-1]
	var infos []relay.PeerInfo
	if len(parts) == 1 {
		infos = a.node.Peers()
	} else if kids, ok := a.node.PeersOfCached(strings.Join(parts[:len(parts)-1], "/")); ok {
		infos = kids
	}
	for _, p := range infos {
		if p.Name == last {
			return p.Version, p.Native
		}
	}
	return "", false
}

// PathLossGrace is the effective no-path grace for new sessions.
func (a *App) PathLossGrace() time.Duration {
	return msess.ClampGrace(time.Duration(a.store.Get().PathLossGraceS) * time.Second)
}

// MsessPaths reports the ingress prober's per-path scores (diagnostics).
func (a *App) MsessPaths() []msess.PathScore { return a.ms.prober.Snapshot() }

// MsessStats reports session-layer counters for /api/diag/runtime.
func (a *App) MsessStats() map[string]int {
	st := a.ms.server.Stats()
	return map[string]int{"exit_sessions": st.Sessions, "exit_subflows": st.Subflows,
		"exit_pending": st.Pending, "probed_paths": a.ms.prober.Count()}
}

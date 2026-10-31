package exit

import (
	"context"
	"errors"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/msess"
	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
)

func splitPath(s string) []string {
	var parts []string
	for _, p := range splitOn(s, '/') {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// localExitGateway caches the Docker gateway IP for local exit rewriting.
// When running in Docker, connecting to the host's own IP causes services
// to see connections from themselves (Docker NAT hairpin). Using the Docker
// gateway (host.docker.internal) instead makes the source appear as 127.0.0.1.
var localExitGateway = sync.OnceValue(func() string {
	// Explicit override via environment variable
	if gw := os.Getenv("LOCAL_EXIT_GATEWAY"); gw != "" {
		log.Printf("[exit] using LOCAL_EXIT_GATEWAY=%s", gw)
		return gw
	}
	// Auto-detect Docker gateway
	ips, err := net.LookupHost("host.docker.internal")
	if err == nil && len(ips) > 0 {
		log.Printf("[exit] detected Docker gateway: %s", ips[0])
		return ips[0]
	}
	return ""
})

// RewriteLocalAddr replaces loopback destinations with the Docker gateway.
// Only rewrites 127.0.0.0/8 addresses — public IPs and hostnames pass through unchanged.
// This fixes Docker NAT hairpin (container accessing host services via localhost)
// without breaking relay exit traffic to the internet.
func RewriteLocalAddr(addr string) string {
	gw := localExitGateway()
	if gw == "" {
		return addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		rewritten := net.JoinHostPort(gw, port)
		log.Printf("[exit] rewriting loopback %s → %s", addr, rewritten)
		return rewritten
	}
	return addr
}

// dialExit routes traffic through an exit path, stripping the local node name prefix.
// dialExitWithMode routes traffic with the specified exit mode.
// mode: "" = direct, "quality" = adaptive failover, "aggregate" = load balance
// SanitizeExitMode checks and sanitizes exit_mode.
// - Clears mode if no meaningful exit paths exist
// - Aggregate mode requires all paths to reach the same final exit node
// Returns the (possibly corrected) mode and any validation error.
func SanitizeExitMode(exitPaths []string, exitMode string) (string, error) {
	if exitMode == "" {
		return "", nil
	}
	// Filter empty paths
	var filled []string
	for _, p := range exitPaths {
		if p != "" {
			filled = append(filled, p)
		}
	}
	// No paths → reset mode to direct
	if len(filled) == 0 {
		return "", nil
	}
	// Aggregate: all paths must end at the same node
	if exitMode == "aggregate" || exitMode == "speed" {
		if len(filled) > 1 {
			var target string
			for _, p := range filled {
				parts := strings.Split(p, "/")
				last := parts[len(parts)-1]
				if target == "" {
					target = last
				} else if last != target {
					return "", fmt.Errorf("aggregate mode requires all paths to reach the same exit node, but got %q and %q", target, last)
				}
			}
		}
	}
	return exitMode, nil
}

func (e *Engine) dialExitWithMode(ctx context.Context, exitVia, exitMode, addr string) (net.Conn, error) {
	// Reject traffic through disabled nodes
	if exitVia != "" && e.isExitPathDisabled(exitVia) {
		return nil, fmt.Errorf("exit path %q is disabled", exitVia)
	}
	if exitMode != "" && exitVia != "" && !strings.Contains(exitVia, "/") {
		var conn net.Conn
		var err error
		switch exitMode {
		case "quality", "stability":
			conn, err = e.dialAdaptive(ctx, exitVia, addr)
		case "aggregate", "speed":
			conn, err = e.dialBond(ctx, exitVia, addr)
		}
		if conn != nil {
			return wrapIdleTimeoutCtx(ctx, conn), err
		}
		if err != nil {
			return nil, err
		}
	}
	return e.dialExit(ctx, exitVia, addr)
}

// dialExitWithPaths races exit_paths concurrently with staggered start (quality failover).
// First path starts immediately, subsequent paths start after 2s stagger.
// First successful connection wins; others are closed.
// If exitPaths is empty or mode is aggregate, falls back to dialExitWithMode.
func (e *Engine) dialExitWithPaths(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, error) {
	af := e.auditExit(ctx, exitVia, exitPaths, exitMode)
	// quality / aggregate: lossless multi-path session when the exit speaks
	// msess (1.3.10+); otherwise the legacy dispatch below.
	if conn, ok, err := e.dialMsess(ctx, exitVia, exitPaths, exitMode, addr); ok {
		return conn, err
	}
	paths := exitPaths
	if len(paths) == 0 {
		paths = []string{exitVia}
	}
	// Aggregate mode: no failover, use primary path
	switch exitMode {
	case "aggregate", "speed":
		return e.dialExitWithMode(ctx, exitVia, exitMode, addr)
	}
	// Filter out disabled paths (checks both individual hops and qualified path prefixes)
	var active []string
	for _, p := range paths {
		if !e.isExitPathDisabled(p) {
			active = append(active, p)
		}
	}
	if len(active) == 0 {
		return nil, fmt.Errorf("all exit paths disabled")
	}
	if len(active) == 1 {
		af.SetExitPath(e.stripSelf(active[0]))
		return e.dialExit(ctx, active[0], addr)
	}
	// Race paths concurrently with 2s stagger
	type result struct {
		conn net.Conn
		err  error
		path string
	}
	raceCtx, raceCancel := context.WithTimeout(ctx, 30*time.Second)
	defer raceCancel()
	ch := make(chan result, len(active))
	for i, p := range active {
		p := p
		go func(idx int) {
			// Stagger: first path immediate, rest wait 2s per position
			if idx > 0 {
				select {
				case <-time.After(2 * time.Second):
				case <-raceCtx.Done():
					ch <- result{nil, raceCtx.Err(), p}
					return
				}
			}
			// Per-path timeout: 10s max per individual dial attempt
			pathCtx, pathCancel := context.WithTimeout(raceCtx, 10*time.Second)
			defer pathCancel()
			conn, err := e.dialExit(pathCtx, p, addr)
			ch <- result{conn, err, p}
		}(i)
	}
	// Return first success immediately, clean up others in background
	var lastErr error
	returned := 0
	for returned < len(active) {
		r := <-ch
		returned++
		if r.err != nil {
			lastErr = r.err
			if r.err != context.Canceled {
				log.Printf("[exit] path %s failed: %v", r.path, r.err)
			}
			continue
		}
		// First success: cancel others and return immediately
		raceCancel()
		// Record the winning path for UI display
		e.activePathMu.Lock()
		e.activePaths[exitVia] = r.path
		e.activePathMu.Unlock()
		af.SetExitPath(e.stripSelf(r.path))
		if r.path != active[0] {
			log.Printf("[exit] failover: %s (primary %s unavailable)", r.path, active[0])
		}
		// Drain remaining results in background to close late connections
		go func() {
			for returned < len(active) {
				late := <-ch
				returned++
				if late.conn != nil {
					late.conn.Close()
				}
			}
		}()
		return r.conn, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("all exit paths exhausted")
}

// isExitPathDisabled: a hop of exitVia, or a path prefix of it, is
// disabled here.
func (e *Engine) isExitPathDisabled(exitVia string) bool {
	parts := strings.Split(exitVia, "/")
	for i := 1; i <= len(parts); i++ {
		if e.host.Disabled(strings.Join(parts[:i], "/")) {
			return true
		}
	}
	for _, hop := range parts {
		if e.host.Disabled(hop) {
			return true
		}
	}
	return false
}

// exitChain normalises exit_via into the hop list after this node and
// enforces the nested iron rules; shared by every dial path so routing and
// the UI keep one predicate.
func (e *Engine) exitChain(exitVia string) ([]string, error) {
	parts := splitPath(exitVia)
	// Strip leading self label (either our display name or our node_id)
	// so exit_via expressed as either "hub/au/…" or "188c762f/au/…" both
	// normalize to the same remaining chain.
	if len(parts) > 0 && (parts[0] == e.node.Name() || parts[0] == e.host.SelfID()) {
		parts = parts[1:]
	}
	// Rule 1 (including self identity): reject paths that revisit any node.
	// Self identity covers BOTH the display name and node_id — neither may
	// reappear mid-path or as a terminal hop, regardless of which one was
	// used at the root. Matches the UI ancestor filter.
	seen := map[string]bool{e.node.Name(): true, e.host.SelfID(): true}
	for _, h := range parts {
		if seen[h] {
			log.Printf("[exit] rejecting loop path %q (revisits %q)", exitVia, h)
			return nil, fmt.Errorf("exit path %q loops back through %q", exitVia, h)
		}
		seen[h] = true
	}
	// Rule 2: every intermediate hop must have nested=true on our side
	// before the path can be dialed. A single nested=false anywhere along
	// the chain makes the exit unreachable, matching the UI — red node =
	// undialable. This closes the "route bypasses nested gate" hole that
	// pre-rule-3 findPathsTo left open.
	if !e.authorised(parts) {
		log.Printf("[exit] rejecting unauthorised path %q (nested gate closed on an ancestor)", exitVia)
		return nil, fmt.Errorf("exit path %q not authorised by local nested config", exitVia)
	}
	return parts, nil
}

// dialExit routes traffic through an exit path (direct mode, no adaptive/LB).
func (e *Engine) dialExit(ctx context.Context, exitVia, addr string) (net.Conn, error) {
	parts, err := e.exitChain(exitVia)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return e.dialLocal(ctx, "tcp", RewriteLocalAddr(addr))
	}
	if len(parts) == 1 {
		// If target is not a direct peer, find a path through intermediates
		if !e.node.HasPeer(parts[0]) {
			paths := e.findPathsTo(parts[0])
			if len(paths) > 0 {
				pick := paths[mrand.IntN(len(paths))]
				log.Printf("[exit] %s not direct peer, using path: %s", parts[0], pick)
				return e.dialExit(ctx, pick, addr)
			}
		}
	}
	// A first hop that is down right now may come back: keep trying for the
	// no-path grace, like an established connection would.
	deadline := time.Now().Add(e.PathLossGrace())
	for {
		conn, err := e.dialExitChain(ctx, parts, addr)
		if err == nil {
			return conn, nil
		}
		var de *msess.DialError
		if errors.Is(err, relay.ErrRemoteDial) || errors.As(err, &de) {
			return nil, err // the exit answered: the target itself is unreachable
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// dialExitChain makes one attempt over an explicit chain: a lossless
// session when the exit speaks msess (1.3.10+), else a plain relay stream
// that ends with its QUIC connection.
func (e *Engine) dialExitChain(ctx context.Context, parts []string, addr string) (net.Conn, error) {
	if conn, ok, err := e.dialMsessDirect(ctx, parts, addr); ok {
		return conn, err
	}
	var conn net.Conn
	var err error
	addr = e.tagTarget(ctx, parts, addr)
	if len(parts) == 1 {
		conn, err = e.node.DialTCP(ctx, parts[0], addr)
	} else {
		conn, err = e.node.DialVia(ctx, parts, addr)
	}
	if err != nil {
		return nil, err
	}
	return wrapIdleTimeoutCtx(e.node.PeerCtx(parts[0]), conn), nil
}

// dialExitUDP opens a UDP connection through the specified exit chain.
// Multi-hop is now supported via Node.DialUDPVia (the UDP analog of
// TCP's DialVia) — each intermediate hop's nodeOutbound.UDP parses the
// via-encoded target address and forwards along the chain.
func (e *Engine) dialExitUDP(ctx context.Context, exitVia, addr string) (net.Conn, error) {
	parts := splitPath(exitVia)
	if len(parts) > 0 && (parts[0] == e.node.Name() || parts[0] == e.host.SelfID()) {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return e.dialLocal(ctx, "udp", RewriteLocalAddr(addr))
	}
	// Indirect single-hop: target peer is reachable only through a known
	// path. Re-enter via the resolved path.
	if len(parts) == 1 && !e.node.HasPeer(parts[0]) {
		paths := e.findPathsTo(parts[0])
		if len(paths) > 0 {
			return e.dialExitUDP(ctx, paths[mrand.IntN(len(paths))], addr)
		}
	}
	// Retry-with-backoff: when the first hop is an INBOUND peer, its
	// reverse-control stream (p.ctrlW) is populated asynchronously
	// after the peer registers — DialUDPVia can return
	// "peer X control not ready" during the convergence window
	// (typically <5s in production, longer after a fresh compose
	// recreate). 30s/2s retry.
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for {
		conn, err := e.node.DialUDPVia(ctx, parts, e.tagTarget(ctx, parts, addr))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		// Only retry on the specific transient errors. Other errors
		// (e.g. peer not connected at all) bubble up immediately.
		es := err.Error()
		if !strings.Contains(es, "control not ready") && !strings.Contains(es, "not connected") {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// dialExitUDPPaths races UDP exit_paths concurrently with the same
// 2-second stagger as dialExitWithPaths (TCP). Falls back to single
// dialExitUDP when only one path is active. The UDP equivalent of
// dialExitWithPaths used by IKEv2 / L2TP transparent UDP proxies.
func (e *Engine) dialExitUDPPaths(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, error) {
	af := e.auditExit(ctx, exitVia, exitPaths, exitMode)
	// quality / aggregate: packet session (exit keeps one UDP socket while
	// the carrying path changes) when the exit speaks msess.
	if conn, ok, err := e.dialMsessPacket(ctx, exitVia, exitPaths, exitMode, addr); ok {
		return conn, err
	}
	paths := exitPaths
	if len(paths) == 0 {
		paths = []string{exitVia}
	}
	var active []string
	for _, p := range paths {
		if !e.isExitPathDisabled(p) {
			active = append(active, p)
		}
	}
	if len(active) == 0 {
		return nil, fmt.Errorf("all UDP exit paths disabled")
	}
	if len(active) == 1 {
		af.SetExitPath(e.stripSelf(active[0]))
		return e.dialExitUDP(ctx, active[0], addr)
	}
	type result struct {
		conn net.Conn
		err  error
		path string
	}
	raceCtx, raceCancel := context.WithTimeout(ctx, 30*time.Second)
	defer raceCancel()
	ch := make(chan result, len(active))
	for i, p := range active {
		p := p
		go func(idx int) {
			if idx > 0 {
				select {
				case <-time.After(2 * time.Second):
				case <-raceCtx.Done():
					ch <- result{nil, raceCtx.Err(), p}
					return
				}
			}
			pathCtx, pathCancel := context.WithTimeout(raceCtx, 10*time.Second)
			defer pathCancel()
			conn, err := e.dialExitUDP(pathCtx, p, addr)
			ch <- result{conn, err, p}
		}(i)
	}
	var lastErr error
	returned := 0
	for returned < len(active) {
		r := <-ch
		returned++
		if r.err != nil {
			lastErr = r.err
			continue
		}
		raceCancel()
		af.SetExitPath(e.stripSelf(r.path))
		go func() {
			for returned < len(active) {
				late := <-ch
				returned++
				if late.conn != nil {
					late.conn.Close()
				}
			}
		}()
		return r.conn, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("all UDP exit paths exhausted")
}

// stripSelf removes a leading self label (name or node id) from an exit
// path and the connection hints of its hops.
func (e *Engine) stripSelf(path string) string {
	parts := splitPath(path)
	if len(parts) > 0 && (parts[0] == e.node.Name() || parts[0] == e.host.SelfID()) {
		parts = parts[1:]
	}
	for i, p := range parts {
		parts[i], _ = relay.SplitConnHint(p)
	}
	return strings.Join(parts, "/")
}

// auditExit records an exit through other nodes on the flow ctx carries
// (an exit that is this node itself is recorded by the local dial).
func (e *Engine) auditExit(ctx context.Context, exitVia string, exitPaths []string, exitMode string) *audit.Flow {
	af := audit.FromContext(ctx)
	if af == nil {
		return nil
	}
	p := e.stripSelf(exitVia)
	if p == "" && len(exitPaths) > 0 {
		p = e.stripSelf(exitPaths[0])
	}
	if p != "" {
		af.SetExitNode(p, exitMode)
	}
	return af
}

func splitOn(s string, sep byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

// chainTags: every node of parts applies restriction tags.
func (e *Engine) chainTags(parts []string) bool {
	for i := range parts {
		if p, ok := e.nodeInfoAt(parts[:i+1]); !ok || p.Native || !p.RestrictTags {
			return false
		}
	}
	return true
}

// tagTarget adds the restrictions carried by ctx to a target dialled
// through parts, when every node on it understands the tag.
func (e *Engine) tagTarget(ctx context.Context, parts []string, addr string) string {
	r := RestrictOf(ctx)
	if r == 0 || len(parts) == 0 || !e.chainTags(parts) {
		return addr
	}
	return proto.WithRestrict(addr, r)
}

package app

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"strings"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/exit"
	"github.com/FrankoonG/hy2scale/core/policy"
	"github.com/FrankoonG/hy2scale/core/sockmark"
)

// Routing of a flow (docs/v1.4.0-policy.md §2.5). Every source has an exit
// selection:
//
//   - a policy set: the flow enters only that set (its default end is the
//     default terminal); the direct entry is not consulted;
//   - an exit group (non-empty paths): the flow leaves through it, no policy
//     is evaluated;
//   - nothing (direct): the direct entry, when set, else the default
//     terminal.
//
// Relay transit (other nodes leaving through this one) and node-access
// users count as "nothing". Terminals: exit → the exit engine; iface → a
// pinned interface (Linux); default → this node's direct dial (egress);
// deny → a *policy.DenyError the inbound answers (reject / drop).

// exitSel is a source's exit selection.
type exitSel struct {
	policy string // policy set id ("" = none)
	via    string
	paths  []string
	mode   string
}

func selOf(pol, via string, paths []string, mode string) exitSel {
	return exitSel{policy: pol, via: via, paths: paths, mode: mode}
}

// set reports whether the selection is not "nothing".
func (s exitSel) set() bool { return s.policy != "" || s.via != "" || len(s.paths) > 0 }

func (s exitSel) start() policy.Start {
	if s.policy != "" {
		return policy.AtPolicy(s.policy)
	}
	paths := s.paths
	if len(paths) == 0 && s.via != "" {
		paths = []string{s.via}
	}
	if len(paths) > 0 {
		return policy.ViaExit(paths, s.mode)
	}
	return policy.Direct
}

func userSel(u *UserConfig) exitSel {
	if u == nil {
		return exitSel{}
	}
	return selOf(u.ExitPolicy, u.ExitVia, u.ExitPaths, u.ExitMode)
}

func proxySel(pc *ProxyConfig) exitSel {
	if pc == nil {
		return exitSel{}
	}
	return selOf(pc.ExitPolicy, pc.ExitVia, pc.ExitPaths, pc.ExitMode)
}

// flowSrc is the source side of a flow's policy fields.
type flowSrc struct {
	kind string // policy.Kind*
	user string // internal user id (WireGuard: the peer name)
	node string // relay: the node it came from
	addr string // client address
}

// userID: the internal id of a user by name ("" = none).
func (a *App) userID(name string) string {
	if name == "" {
		return ""
	}
	a.usersMu.RLock()
	u, ok := a.userIndex[name]
	a.usersMu.RUnlock()
	if !ok {
		return ""
	}
	return u.ID
}

// userSrc is the flow source of a user's client.
func (a *App) userSrc(kind string, u *UserConfig, addr net.Addr) flowSrc {
	s := flowSrc{kind: kind}
	if u != nil {
		s.user = u.ID
	}
	if addr != nil {
		s.addr = addr.String()
	}
	return s
}

// newPolicyConn builds the policy connection of a flow to dst.
func (a *App) newPolicyConn(src flowSrc, network, dst string) *policy.Conn {
	c := &policy.Conn{SrcKind: src.kind, User: src.user, SrcNode: src.node, Proto: network}
	if src.addr != "" {
		c.SetSrcAddr(src.addr)
	}
	c.SetDst(dst)
	a.fillPolicyHooks(c)
	return c
}

func (a *App) fillPolicyHooks(c *policy.Conn) {
	if c.GeoIP == nil {
		if p := policyGeoIP.Load(); p != nil {
			c.GeoIP = *p
		}
	}
	if c.DomainOf == nil {
		if p := policyDomains.Load(); p != nil {
			c.DomainOf = *p
		}
	}
	if c.Resolve == nil {
		c.Resolve = a.policyResolve
	}
}

// policyResolve: dst_ip of a name destination, through the egress.
func (a *App) policyResolve(ctx context.Context, host string) (netip.Addr, error) {
	ips, err := a.egressLookup(ctx, host)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, ip := range ips {
		if ad, ok := netip.AddrFromSlice(ip); ok {
			return ad.Unmap(), nil
		}
	}
	return netip.Addr{}, errors.New("no address")
}

// routed is one flow's routing: evaluated once (lazily), then executed by
// dial, or by routeFunc/here inside the sniffing dial.
type routed struct {
	a       *App
	start   policy.Start
	c       *policy.Conn
	network string
	addr    string // the requested destination
	rewrite bool   // a local dial rewrites addr (exit.RewriteLocalAddr)
	done    bool
	d       policy.Decision
}

func (a *App) route(sel exitSel, src flowSrc, network, addr string) *routed {
	return &routed{a: a, start: sel.start(), c: a.newPolicyConn(src, network, addr), network: network, addr: addr}
}

// decide evaluates the flow once and records the decision on its audit
// record.
func (r *routed) decide(ctx context.Context) policy.Decision {
	if r.done {
		return r.d
	}
	r.done = true
	r.d = r.a.pol.Evaluate(ctx, r.c, r.start)
	if af := audit.FromContext(ctx); af != nil {
		af.SetPolicy(policy.ChainText(r.d.Chain), r.d.Policy, r.d.Terminal.Type)
	}
	return r.d
}

// denyErr records a deny on the flow and returns its error.
func denyErr(ctx context.Context, d policy.Decision) error {
	t := d.Terminal
	e := &policy.DenyError{Reject: t.Reject, Reason: d.Reason}
	audit.FromContext(ctx).Block(e.Error())
	return e
}

func exitRouteOf(t policy.Terminal) exit.Route {
	return exit.Route{Exit: t.Paths[0], Paths: t.Paths, Mode: t.Mode}
}

// dial executes the decision.
func (r *routed) dial(ctx context.Context) (net.Conn, error) {
	d := r.decide(ctx)
	ctx = policy.WithDecided(ctx)
	switch d.Terminal.Type {
	case policy.TermDeny:
		return nil, denyErr(ctx, d)
	case policy.TermExit:
		target := r.addr
		// a name recovered from the client goes to the exit, which resolves it
		if r.c.DstHost == "" && r.c.Sniffed != "" {
			target = net.JoinHostPort(r.c.Sniffed, addrPort(r.addr))
		}
		return r.a.exit.Dial(ctx, exitRouteOf(d.Terminal), r.network, target)
	}
	addr := r.addr
	if r.rewrite {
		addr = rewriteLocalAddr(addr)
	}
	if d.Terminal.Type == policy.TermIface {
		return r.a.dialIface(ctx, d.Terminal, r.network, addr)
	}
	return r.a.dialDirect(ctx, r.network, addr)
}

func addrPort(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}

// routeFunc is the route of a sniffing dial (exit.DialSniffed): the first
// call names the sniffed host when there is one; every call answers the
// same decision.
func (r *routed) routeFunc(ctx context.Context) exit.RouteFunc {
	return func(addr string) (string, []string, string, error) {
		if !r.done {
			if h, _, err := net.SplitHostPort(addr); err == nil && net.ParseIP(strings.Trim(h, "[]")) == nil && h != r.c.DstHost {
				r.c.Sniffed = policy.NormalizeDomain(h)
			}
		}
		d := r.decide(ctx)
		switch d.Terminal.Type {
		case policy.TermDeny:
			return "", nil, "", denyErr(ctx, d)
		case policy.TermExit:
			rt := exitRouteOf(d.Terminal)
			return rt.Exit, rt.Paths, rt.Mode, nil
		}
		return "", nil, "", nil
	}
}

// here dials from this node per the decision (default or iface).
func (r *routed) here(ctx context.Context, addr string) (net.Conn, error) {
	ctx = policy.WithDecided(ctx)
	if r.done && r.d.Terminal.Type == policy.TermIface {
		return r.a.dialIface(ctx, r.d.Terminal, "tcp", rewriteLocalAddr(addr))
	}
	return r.a.dialDirect(ctx, "tcp", rewriteLocalAddr(addr))
}

// dialIface executes an iface terminal.
func (a *App) dialIface(ctx context.Context, t policy.Terminal, network, addr string) (net.Conn, error) {
	if runtime.GOOS != "linux" && !a.ifaces.IsChild(t.Iface) {
		audit.FromContext(ctx).Fail(audit.ResultError, policy.ErrIfaceUnavailable)
		return nil, policy.ErrIfaceUnavailable
	}
	c, err := a.ifaces.DialVia(ctx, t.Iface, t.Src, network, addr)
	if af := audit.FromContext(ctx); af != nil && err == nil {
		audit.MarkDirect(af, c)
	}
	return c, err
}

// dialRoutedTCP dials addr ("host:port", host an IP or a name) for a flow
// of src with selection sel.
func (a *App) dialRoutedTCP(ctx context.Context, sel exitSel, src flowSrc, addr string) (net.Conn, error) {
	return a.route(sel, src, "tcp", addr).dial(ctx)
}

// dialRoutedUDP is the UDP counterpart of dialRoutedTCP: one connected
// datagram flow to addr.
func (a *App) dialRoutedUDP(ctx context.Context, sel exitSel, src flowSrc, addr string) (net.Conn, error) {
	return a.route(sel, src, "udp", addr).dial(ctx)
}

// dialSniffedTCP dials a flow to the IP destination dst whose client sent
// peeked first (sniff_override.go).
func (a *App) dialSniffedTCP(ctx context.Context, r *routed, dst string, peeked []byte, waited, localClient bool) (net.Conn, error) {
	ctx = policy.WithDecided(ctx)
	return a.exit.DialSniffed(ctx, r.routeFunc(ctx), r.here, dst, peeked, waited, localClient)
}

// dialRelayLocal dials a target from this node for a flow the exit engine
// or the relay layer carries here: a decided flow's own dial, else a relay
// transit flow, which enters the direct entry.
func (a *App) dialRelayLocal(ctx context.Context, network, addr string) (net.Conn, error) {
	if policy.Decided(ctx) {
		return a.dialDirect(ctx, network, addr)
	}
	prog := a.pol.Program()
	if prog.Direct() == "" {
		// fast path: no direct entry
		if af := audit.FromContext(ctx); af != nil {
			af.SetPolicy("", "", policy.TermDefault)
		}
		return a.dialDirect(policy.WithDecided(ctx), network, addr)
	}
	return a.route(exitSel{}, a.relaySrc(ctx), network, addr).dial(ctx)
}

// relayPlainDial dials a peer's plain reverse-dial target (core/node):
// through the direct entry when one is set, else as before (a marked system
// dial, outside the egress).
func (a *App) relayPlainDial(ctx context.Context, network, addr string) (net.Conn, error) {
	if a.pol.Program().Direct() == "" {
		if af := audit.FromContext(ctx); af != nil {
			af.SetPolicy("", "", policy.TermDefault)
		}
		return sockmark.DialContext(ctx, network, addr)
	}
	return a.route(exitSel{}, a.relaySrc(ctx), network, addr).dial(ctx)
}

// relaySrc: the source of a flow other nodes (or node-access users) send
// out through here, as recorded in ctx (core/server, core/exit, core/node).
func (a *App) relaySrc(ctx context.Context) flowSrc {
	s, _ := policy.SourceFrom(ctx)
	if s.Kind == "" {
		s.Kind = policy.KindRelay
	}
	user := s.User
	if s.Kind == policy.KindAccess {
		user = a.userID(s.User) // the stream names the user; conditions use its id
	}
	return flowSrc{kind: s.Kind, user: user, node: s.Node, addr: s.Addr}
}

// relayLocalTCP is the plain-target dial of a node's stream (core/server
// LocalTCP): host-name recovery as before, routed through the direct
// entry.
func (a *App) relayLocalTCP(ctx context.Context, addr string, bridged bool) (net.Conn, error) {
	if policy.Decided(ctx) || a.pol.Program().Direct() == "" {
		return relayPlainTCP(ctx, a.exit, addr, bridged)
	}
	r := a.route(exitSel{}, a.relaySrc(ctx), "tcp", addr)
	r.rewrite = true
	if !bridged && exit.WantSniff(addr) {
		return exit.NewDeferredDial(func(peeked []byte, waited bool) (net.Conn, error) {
			return a.dialSniffedTCP(ctx, r, addr, peeked, waited, false)
		}), nil
	}
	return r.dial(ctx)
}

// relayPlainTCP: the pre-policy plain-target dial (core/server.LocalTCP).
func relayPlainTCP(ctx context.Context, ex *exit.Engine, addr string, bridged bool) (net.Conn, error) {
	here := func(ctx context.Context, a string) (net.Conn, error) {
		return ex.DialLocalTCP(ctx, exit.RewriteLocalAddr(a))
	}
	if !bridged && exit.WantSniff(addr) {
		none := func(string) (string, []string, string, error) { return "", nil, "", nil }
		return exit.NewDeferredDial(func(peeked []byte, waited bool) (net.Conn, error) {
			return ex.DialSniffed(ctx, none, here, addr, peeked, waited, false)
		}), nil
	}
	return here(ctx, addr)
}

// denyClose answers a deny on a client stream: reject resets it (RST),
// drop closes it quietly. Reports whether err was a deny.
func denyClose(c net.Conn, err error) bool {
	d, ok := policy.AsDeny(err)
	if !ok {
		return false
	}
	if d.Reject {
		resetConn(c)
	}
	c.Close()
	return true
}

// resetConn makes the next Close send a RST.
func resetConn(c net.Conn) {
	for i := 0; i < 4 && c != nil; i++ {
		if tc, ok := c.(*net.TCPConn); ok {
			tc.SetLinger(0)
			return
		}
		switch u := c.(type) {
		case interface{ NetConn() net.Conn }:
			c = u.NetConn()
		case interface{ Unwrap() net.Conn }:
			c = u.Unwrap()
		default:
			return
		}
	}
}

// isReject reports whether err is a policy reject (vs drop).
func isReject(err error) bool {
	d, ok := policy.AsDeny(err)
	return ok && d.Reject
}

// ---------------------------------------------------------------- runtime

// policyRuntime implements policy.Runtime for the capture backend.
type policyRuntime struct{ a *App }

// PolicyRuntime evaluates and executes captured flows.
func (a *App) PolicyRuntime() policy.Runtime { return policyRuntime{a} }

func (rt policyRuntime) dial(ctx context.Context, c *policy.Conn, s policy.Start, network string) (net.Conn, policy.Decision, error) {
	rt.a.fillPolicyHooks(c)
	if c.Proto == "" {
		c.Proto = network
	}
	r := &routed{a: rt.a, start: s, c: c, network: network, addr: c.Target()}
	conn, err := r.dial(ctx)
	return conn, r.d, err
}

func (rt policyRuntime) DialTCP(ctx context.Context, c *policy.Conn, s policy.Start) (net.Conn, policy.Decision, error) {
	return rt.dial(ctx, c, s, "tcp")
}

func (rt policyRuntime) DialUDP(ctx context.Context, c *policy.Conn, s policy.Start) (net.Conn, policy.Decision, error) {
	return rt.dial(ctx, c, s, "udp")
}

// ---------------------------------------------------------------- test

// PolicyTestSource / PolicyTestDest: the body of POST /api/policy/test.
type PolicyTestSource struct {
	Kind  string `json:"kind"`
	User  string `json:"user,omitempty"` // internal id (a username is accepted)
	IP    string `json:"ip,omitempty"`
	Port  int    `json:"port,omitempty"`
	Iface string `json:"iface,omitempty"`
	MAC   string `json:"mac,omitempty"`
	Node  string `json:"node,omitempty"`
}

type PolicyTestDest struct {
	Host  string `json:"host,omitempty"`
	IP    string `json:"ip,omitempty"`
	Port  int    `json:"port"`
	Proto string `json:"proto"`
}

// PolicyTest evaluates a described connection without dialling. start:
// "" (by source: the user's or WireGuard peer's selection, else direct),
// "direct", or a policy set id.
func (a *App) PolicyTest(ctx context.Context, start string, src PolicyTestSource, dst PolicyTestDest) (policy.Decision, error) {
	cfg := a.store.Get()
	c := &policy.Conn{SrcKind: strings.ToLower(src.Kind), SrcNode: src.Node, InIface: src.Iface, SrcMAC: src.MAC,
		Proto: strings.ToLower(dst.Proto)}
	if c.Proto == "" {
		c.Proto = "tcp"
	}
	if c.SrcKind == "" {
		c.SrcKind = policy.KindSOCKS5
	}
	var user *UserConfig
	for i := range cfg.Users {
		if cfg.Users[i].ID == src.User || (src.User != "" && cfg.Users[i].Username == src.User) {
			user = &cfg.Users[i]
			break
		}
	}
	switch {
	case c.SrcKind == policy.KindWireGuard:
		c.User = src.User
	case user != nil:
		c.User = user.ID
	default:
		c.User = src.User
	}
	if src.IP != "" {
		ip, err := netip.ParseAddr(src.IP)
		if err != nil {
			return policy.Decision{}, &policy.Error{Path: "source.ip", Msg: "invalid address"}
		}
		c.SrcIP = ip.Unmap()
	}
	if src.Port < 0 || src.Port > 65535 || dst.Port < 0 || dst.Port > 65535 {
		return policy.Decision{}, &policy.Error{Path: "dest.port", Msg: "invalid port"}
	}
	c.SrcPort, c.DstPort = uint16(src.Port), uint16(dst.Port)
	if dst.IP != "" {
		ip, err := netip.ParseAddr(dst.IP)
		if err != nil {
			return policy.Decision{}, &policy.Error{Path: "dest.ip", Msg: "invalid address"}
		}
		c.DstIP = ip.Unmap()
	}
	if dst.Host != "" {
		if ip, err := netip.ParseAddr(dst.Host); err == nil {
			c.DstIP = ip.Unmap()
		} else {
			c.DstHost = dst.Host
		}
	}
	if !c.DstIP.IsValid() && c.DstHost == "" {
		return policy.Decision{}, &policy.Error{Path: "dest", Msg: "host or ip required"}
	}
	a.fillPolicyHooks(c)
	var s policy.Start
	switch start {
	case "direct":
		s = policy.Direct
	case "":
		var sel exitSel
		if c.SrcKind == policy.KindWireGuard {
			if wg := cfg.WireGuard; wg != nil {
				for _, p := range wg.Peers {
					if p.Name == src.User {
						sel = selOf(p.ExitPolicy, p.ExitVia, p.ExitPaths, p.ExitMode)
					}
				}
			}
		} else if user != nil && c.SrcKind != policy.KindRelay && c.SrcKind != policy.KindLocal &&
			c.SrcKind != policy.KindForward && c.SrcKind != policy.KindAccess {
			sel = userSel(user)
		}
		s = sel.start()
	default:
		if !a.pol.Program().HasPolicy(start) {
			return policy.Decision{}, &policy.Error{Path: "start", Msg: "unknown policy set"}
		}
		s = policy.AtPolicy(start)
	}
	return a.pol.Evaluate(ctx, c, s), nil
}

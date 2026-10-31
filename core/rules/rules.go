// Package rules routes destinations (IPs, CIDRs, ranges, domains) to
// exits, per flow (L4), and on Linux captures this host's matching traffic
// into transparent listeners (capture_linux.go).
package rules

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/exit"
	"github.com/FrankoonG/hy2scale/core/netfilter"
)

// The rule engine routes destinations (IPs, CIDRs, ranges, domains) to
// exits, per flow (L4). The rule table always exists: it routes the flows
// of VPN and proxy users without their own exit (see route.go). Where the
// node has host networking and NET_ADMIN it also captures this host's
// matching traffic — its own and that of hosts routed through it — into
// transparent listeners on loopback (rules_capture_linux.go).
type Engine struct {
	host       Host
	ex         *exit.Engine
	mu         sync.Mutex
	cancel     context.CancelFunc
	appliedIPT map[string][]string // ruleID → capture lines (captureLines format)
	domainIPs  map[string][]string // ruleID → resolved IPs (for domain rules)
	dnsCancel  context.CancelFunc
	destToExit sync.Map // "ruleID|target" → ruleExitInfo
	mode       captureMode
	mode6      captureMode
}

// Rule is a routing rule (IP or domain based).
type Rule struct {
	ID        string   `yaml:"id" json:"id"`
	Name      string   `yaml:"name" json:"name"`
	Type      string   `yaml:"type" json:"type"`       // "ip" or "domain"
	Targets   []string `yaml:"targets" json:"targets"` // IPs/CIDRs/ranges or domains
	ExitVia   string   `yaml:"exit_via" json:"exit_via"`
	ExitPaths []string `yaml:"exit_paths,omitempty" json:"exit_paths,omitempty"`
	ExitMode  string   `yaml:"exit_mode,omitempty" json:"exit_mode,omitempty"` // ""|"quality"|"aggregate"
	Enabled   bool     `yaml:"enabled" json:"enabled"`
	// Priority resolves overlaps between rules: the higher Priority wins,
	// then the more specific target. Default 0. (The pre-1.4.0 use_tun field
	// is ignored: every rule is carried per flow.)
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`
}

// Host is what the rule engine needs from the node.
type Host interface {
	// Rules: the configured rules.
	Rules() []Rule
	// MeshAddrs: the node's own mesh sockets, never captured — its server
	// listen address ("" = none) and every outbound peer address, as
	// "host:ports".
	MeshAddrs() (listen string, peers []string)
}

const (
	ruleProxyPort    = 12380
	ruleUDPProxyPort = 12381
	ruleBypassMark   = 0x1234 // SO_MARK of our own dials: never captured
)

// New returns the engine of a node; its rule table is empty until Start.
func New(host Host, ex *exit.Engine) *Engine {
	return &Engine{host: host, ex: ex, appliedIPT: make(map[string][]string), domainIPs: make(map[string][]string)}
}

// Start builds the rule table and, when the node may, the capture.
func (e *Engine) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	if !netfilter.CheckHostNetwork() {
		debugLog("[rules] not in host network mode: no capture")
	} else if ok, _ := netfilter.CheckCapability(); !ok {
		debugLog("[rules] no NET_ADMIN: no capture")
	} else {
		e.startCapture(ctx)
	}
	e.mu.Lock()
	for _, r := range e.host.Rules() {
		if r.Enabled {
			e.applyRule(r)
		}
	}
	e.mu.Unlock()
	dnsCtx, dnsCancel := context.WithCancel(ctx)
	e.dnsCancel = dnsCancel
	go e.dnsRefreshLoop(dnsCtx)
	log.Printf("[rules] engine started (capture=%s, ipv6 capture=%s)", e.mode, e.mode6)
}

type captureMode int

const (
	captureNone captureMode = iota
	captureDNAT
	captureTProxy
)

func (m captureMode) String() string {
	switch m {
	case captureTProxy:
		return "tproxy"
	case captureDNAT:
		return "dnat"
	}
	return "none"
}

func (m captureMode) table() string {
	if m == captureTProxy {
		return "mangle"
	}
	return "nat"
}

// Stop removes the capture and stops the engine.
func (e *Engine) Stop() {
	e.mu.Lock()
	for id := range e.appliedIPT {
		e.removeRuleLocked(id)
	}
	e.mu.Unlock()
	captureTeardown()
	if e.dnsCancel != nil {
		e.dnsCancel()
	}
	if e.cancel != nil {
		e.cancel()
	}
	log.Printf("[rules] engine stopped")
}

// CaptureMode reports how this host's own and routed traffic is captured:
// "tproxy", "dnat" (this node only) or "none" (rules then apply to VPN and
// proxy users only).
func (e *Engine) CaptureMode() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mode.String()
}

// ExitFor returns the exit of the enabled routing rule that owns host (an
// IP literal or a host name), for flows of users without their own exit.
func (e *Engine) ExitFor(host string) (via, mode string, paths []string, ok bool) {
	via, mode, paths = e.lookupExitFull(host)
	return via, mode, paths, via != ""
}

// RuleFor is ExitFor plus the name of the rule.
func (e *Engine) RuleFor(host string) (via, mode string, paths []string, name string, ok bool) {
	info := e.lookupRule(host)
	if info == nil || info.exitVia == "" {
		return "", "", nil, "", false
	}
	return info.exitVia, info.exitMode, info.exitPaths, info.name, true
}

// Apply enables a routing rule.
func (e *Engine) Apply(r Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.applyRule(r)
}

// Remove disables a routing rule.
func (e *Engine) Remove(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removeRuleLocked(id)
}

type ruleExitInfo struct {
	ruleID    string
	name      string
	target    string // IP, CIDR, a-b range, or "name:<domain>"
	priority  int
	exitVia   string
	exitMode  string
	exitPaths []string
}

func (e *Engine) applyRule(r Rule) {
	e.removeRuleLocked(r.ID)
	switch r.Type {
	case "ip":
		var lines []string
		for _, target := range r.Targets {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			lines = append(lines, e.captureLines(target)...)
			e.registerTargetExit(r, target)
		}
		e.installLines(r.ID, lines)
		log.Printf("[rules] applied IP rule %q (prio %d): %d targets → exit %s", r.Name, r.Priority, len(r.Targets), r.ExitVia)
	case "domain":
		e.applyDomainRule(r)
	}
}

func (e *Engine) installLines(id string, lines []string) {
	for _, l := range lines {
		runCaptureLine(l, false)
	}
	e.appliedIPT[id] = lines
}

// applyDomainRule registers the domains themselves (for flows that carry a
// host name) and their current A/AAAA addresses (for IP flows and capture).
func (e *Engine) applyDomainRule(r Rule) {
	e.removeRuleLocked(r.ID)
	var lines, resolved []string
	for _, domain := range r.Targets {
		domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if domain == "" {
			continue
		}
		// "*.example.com" = its subdomains: matched by name, nothing to resolve
		if base, wild := strings.CutPrefix(domain, "*."); wild {
			e.registerTargetExit(r, "name:"+base)
			continue
		}
		e.registerTargetExit(r, "name:"+domain)
		// through the exit's resolver when DNSResolver is on (keeps a
		// polluted local resolv.conf out of the table)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ips, err := e.ex.Resolve(ctx, domain, r.ExitVia)
		cancel()
		if err != nil {
			debugLog("[rules] DNS resolve %s: %v", domain, err)
			continue
		}
		for _, ip := range ips {
			if net.ParseIP(ip) == nil {
				continue
			}
			lines = append(lines, e.captureLines(ip)...)
			e.registerTargetExit(r, ip)
			resolved = append(resolved, ip)
		}
	}
	e.installLines(r.ID, lines)
	e.domainIPs[r.ID] = resolved
	log.Printf("[rules] applied domain rule %q: %d domains → %d IPs → exit %s", r.Name, len(r.Targets), len(resolved), r.ExitVia)
}

func (e *Engine) registerTargetExit(r Rule, target string) {
	name := r.Name
	if name == "" {
		name = r.ID
	}
	e.destToExit.Store(r.ID+"|"+target, ruleExitInfo{ruleID: r.ID, name: name, target: target, priority: r.Priority,
		exitVia: r.ExitVia, exitMode: r.ExitMode, exitPaths: r.ExitPaths})
}

func (e *Engine) removeRuleLocked(id string) {
	for _, l := range e.appliedIPT[id] {
		runCaptureLine(l, true)
	}
	delete(e.appliedIPT, id)
	delete(e.domainIPs, id)
	e.destToExit.Range(func(k, v any) bool {
		if v.(ruleExitInfo).ruleID == id {
			e.destToExit.Delete(k)
		}
		return true
	})
}

// lookupExitFull returns the exit of the rule that owns host: the highest
// Priority among the rules whose targets contain it, then the most specific
// target, then the lowest rule ID — the same answer for every flow.
func (e *Engine) lookupExitFull(host string) (string, string, []string) {
	best := e.lookupRule(host)
	if best == nil {
		return "", "", nil
	}
	return best.exitVia, best.exitMode, best.exitPaths
}

// lookupRule: the rule that owns host (nil = none).
func (e *Engine) lookupRule(host string) *ruleExitInfo {
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	name := ""
	if ip == nil {
		name = strings.ToLower(strings.TrimSuffix(host, "."))
		if name == "" {
			return nil
		}
	}
	var best *ruleExitInfo
	bestSpec := -1
	e.destToExit.Range(func(_, v any) bool {
		info := v.(ruleExitInfo)
		var spec int
		if ip != nil {
			spec = targetSpecificity(info.target, ip)
		} else {
			spec = nameSpecificity(info.target, name)
		}
		if spec < 0 {
			return true
		}
		if best == nil || info.priority > best.priority ||
			(info.priority == best.priority && (spec > bestSpec || (spec == bestSpec && info.ruleID < best.ruleID))) {
			i := info
			best, bestSpec = &i, spec
		}
		return true
	})
	return best
}

// targetSpecificity reports how narrowly target (IP, CIDR or a-b range, of
// either family) matches ip, in prefix bits, or -1 if it does not.
func targetSpecificity(target string, ip net.IP) int {
	if strings.HasPrefix(target, "name:") {
		return -1
	}
	if strings.Contains(target, "/") {
		_, cidr, err := net.ParseCIDR(target)
		if err != nil || !cidr.Contains(ip) {
			return -1
		}
		ones, _ := cidr.Mask.Size()
		return ones
	}
	if lo, hi, ok := strings.Cut(target, "-"); ok {
		start, end := normIP(net.ParseIP(strings.TrimSpace(lo))), normIP(net.ParseIP(strings.TrimSpace(hi)))
		x := normIP(ip)
		if start == nil || end == nil || len(start) != len(x) || len(end) != len(x) ||
			bytesCompare(x, start) < 0 || bytesCompare(x, end) > 0 {
			return -1
		}
		return commonPrefixBits(start, end)
	}
	if t := net.ParseIP(target); t != nil && t.Equal(ip) {
		return len(normIP(ip)) * 8
	}
	return -1
}

// nameSpecificity matches a host name against a "name:<domain>" target
// (the domain or any subdomain); longer domains are more specific.
func nameSpecificity(target, name string) int {
	d, ok := strings.CutPrefix(target, "name:")
	if !ok || (name != d && !strings.HasSuffix(name, "."+d)) {
		return -1
	}
	return len(d)
}

func normIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

func commonPrefixBits(a, b []byte) int {
	n := 0
	for i := range a {
		x := a[i] ^ b[i]
		if x == 0 {
			n += 8
			continue
		}
		for x&0x80 == 0 {
			n++
			x <<= 1
		}
		break
	}
	return n
}

func bytesCompare(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// serveProxy handles captured TCP connections.
func (e *Engine) serveProxy(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		go e.handleConn(ctx, conn)
	}
}

// capturedDst is a captured connection's original destination: TPROXY
// leaves it as the local address, IPv4 DNAT needs SO_ORIGINAL_DST.
func capturedDst(conn net.Conn) (string, error) {
	la, ok := conn.LocalAddr().(*net.TCPAddr)
	if ok && !la.IP.Equal(net.ParseIP(ruleProxyIP)) && !la.IP.Equal(net.ParseIP(ruleProxyIP6)) {
		return la.String(), nil
	}
	return netfilter.OriginalDst(conn)
}

func (e *Engine) handleConn(parentCtx context.Context, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	origDst, err := capturedDst(conn)
	if err != nil {
		debugLog("[rules] original destination: %v", err)
		return
	}
	host, _, _ := net.SplitHostPort(origDst)
	var exitVia, exitMode string
	var exitPaths []string
	af := audit.Begin(audit.InRules, "tcp")
	if info := e.lookupRule(host); info != nil {
		exitVia, exitMode, exitPaths = info.exitVia, info.exitMode, info.exitPaths
		af.SetRule(info.name)
	}
	af.SetSrc(conn.RemoteAddr().String(), "")
	af.SetDst(origDst)
	ctx = audit.NewContext(ctx, af)
	var remote net.Conn
	if exitVia == "" {
		// no rule owns it any more (removed while the flow was in flight)
		remote, err = dialTCPMarked(origDst, ruleBypassMark)
		if err == nil {
			audit.MarkDirect(af, remote)
		}
	} else {
		// Fail closed: a rule-matched flow only ever leaves through its exit.
		remote, err = e.ex.Dial(ctx, exit.Route{Exit: exitVia, Paths: exitPaths, Mode: exitMode}, "tcp", origDst)
	}
	if err != nil {
		af.End(err)
		log.Printf("[rules] %s via %q: %v", origDst, exitVia, err)
		return
	}
	debugLog("[rules] %s connected via %q", origDst, exitVia)
	remote = audit.Track(af, remote, false)
	defer remote.Close()
	done := make(chan struct{})
	go func() {
		copyCtx(ctx, remote, conn)
		halfCloseAndBound(remote)
		done <- struct{}{}
	}()
	copyCtx(ctx, conn, remote)
	halfCloseAndBound(conn)
	<-done
}

const (
	ruleUDPIdle     = 120 * time.Second
	ruleUDPDNSIdle  = 20 * time.Second
	ruleUDPMaxFlows = 4096
	ruleUDPQueue    = 64
)

// udpFlow is one captured UDP flow: a (client, original destination) pair
// carried through the exit by one exit-side socket.
type udpFlow struct {
	key    string
	client *net.UDPAddr
	dst    *net.UDPAddr
	idle   time.Duration
	last   atomic.Int64
	// reply sends a datagram back to the client from dst
	reply func([]byte) error
	// local: the connected transparent socket (tproxy), closed with the flow
	local *net.UDPConn

	mu     sync.Mutex
	remote net.Conn
	queue  [][]byte
	closed bool

	af *audit.Flow // audit record (nil = not recorded)
}

func (f *udpFlow) touch() { f.last.Store(time.Now().UnixNano()) }

// send forwards a client datagram, queueing it while the exit dial is
// still in flight.
func (f *udpFlow) send(b []byte) {
	f.touch()
	f.af.Add(int64(len(b)), 0)
	f.mu.Lock()
	r := f.remote
	if r == nil && !f.closed && len(f.queue) < ruleUDPQueue {
		f.queue = append(f.queue, append([]byte(nil), b...))
	}
	f.mu.Unlock()
	if r != nil {
		r.Write(b)
	}
}

func (f *udpFlow) close() {
	f.mu.Lock()
	f.closed = true
	r := f.remote
	f.queue = nil
	f.mu.Unlock()
	if r != nil {
		r.Close()
	}
	if f.local != nil {
		f.local.Close()
	}
}

// serveUDPProxy handles captured UDP on one listener. Each (client,
// destination) pair is its own flow and exit-side socket, so one client
// socket may talk to many destinations. Under TPROXY the destination comes
// with every datagram and replies leave from it; under DNAT conntrack
// supplies it and un-NATs the replies sent from the listener.
func (e *Engine) serveUDPProxy(ctx context.Context, ln *net.UDPConn) {
	var mu sync.Mutex
	flows := map[string]*udpFlow{}
	dnatDst := map[string]*net.UDPAddr{} // DNAT mode: client → original destination
	full := false
	remove := func(f *udpFlow) {
		mu.Lock()
		if flows[f.key] == f {
			delete(flows, f.key)
			if d := dnatDst[f.client.String()]; d != nil && d.String() == f.dst.String() {
				delete(dnatDst, f.client.String())
			}
		}
		mu.Unlock()
		f.close()
	}
	go func() {
		<-ctx.Done()
		mu.Lock()
		all := make([]*udpFlow, 0, len(flows))
		for _, f := range flows {
			all = append(all, f)
		}
		mu.Unlock()
		for _, f := range all {
			f.close()
		}
	}()
	listenerAddr := ln.LocalAddr().(*net.UDPAddr)
	buf := make([]byte, 65535)
	oob := make([]byte, 256)
	for {
		n, oobn, _, src, err := ln.ReadMsgUDP(buf, oob)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		dst := udpOrigDst(oob[:oobn])
		tproxied := dst != nil && !(dst.IP.Equal(listenerAddr.IP) && dst.Port == listenerAddr.Port)
		if !tproxied {
			mu.Lock()
			dst = dnatDst[src.String()]
			mu.Unlock()
			if dst == nil {
				if dst, _ = net.ResolveUDPAddr("udp", conntrackOrigDst("udp", src.String(), ruleUDPProxyPort)); dst == nil {
					debugLog("[rules] UDP from %s: no original destination", src)
					continue
				}
			}
		}
		key := src.String() + ">" + dst.String()
		mu.Lock()
		f := flows[key]
		if f == nil {
			if len(flows) >= ruleUDPMaxFlows {
				if !full {
					log.Printf("[rules] UDP flow limit %d reached, dropping new flows", ruleUDPMaxFlows)
				}
				full = true
				mu.Unlock()
				continue
			}
			full = false
			f = &udpFlow{key: key, client: src, dst: dst, idle: ruleUDPIdle}
			if dst.Port == 53 {
				f.idle = ruleUDPDNSIdle
			}
			f.touch()
			flows[key] = f
			if !tproxied {
				dnatDst[src.String()] = dst
			}
			mu.Unlock()
			if !e.startUDPFlow(ctx, f, tproxied, ln, func() { remove(f) }) {
				remove(f)
				continue
			}
		} else {
			mu.Unlock()
		}
		f.send(buf[:n])
	}
}

// startUDPFlow sets up the client side of a new flow and dials its exit in
// the background. Returns false when the flow cannot be served.
func (e *Engine) startUDPFlow(ctx context.Context, f *udpFlow, tproxied bool, ln *net.UDPConn, done func()) bool {
	f.af = audit.Begin(audit.InRules, "udp")
	f.af.SetSrc(f.client.String(), "")
	f.af.SetDst(f.dst.String())
	if tproxied {
		local, err := dialUDPAs(f.dst, f.client)
		if err != nil {
			f.af.End(err)
			log.Printf("[rules] UDP %s → %s: reply socket: %v", f.client, f.dst, err)
			return false
		}
		f.local = local
		f.reply = func(b []byte) error { _, err := local.Write(b); return err }
		// later datagrams of this flow arrive on the connected socket
		go func() {
			b := make([]byte, 65535)
			for {
				n, err := local.Read(b)
				if err != nil {
					return
				}
				f.send(b[:n])
			}
		}()
	} else {
		client := f.client
		f.reply = func(b []byte) error { _, err := ln.WriteToUDP(b, client); return err }
	}
	go func() {
		var ferr error
		defer func() { f.af.End(ferr) }()
		defer done()
		var exitVia, exitMode string
		var exitPaths []string
		if info := e.lookupRule(f.dst.IP.String()); info != nil {
			exitVia, exitMode, exitPaths = info.exitVia, info.exitMode, info.exitPaths
			f.af.SetRule(info.name)
		}
		var remote net.Conn
		var err error
		if exitVia == "" {
			remote, err = dialUDPMarked(f.dst.String(), ruleBypassMark)
			if err == nil {
				audit.MarkDirect(f.af, remote)
			}
		} else {
			// Fail closed: never dialled from this node when the exit is down.
			remote, err = e.ex.Dial(audit.NewContext(ctx, f.af), exit.Route{Exit: exitVia, Paths: exitPaths, Mode: exitMode}, "udp", f.dst.String())
		}
		if err != nil {
			ferr = err
			log.Printf("[rules] UDP %s → %s via %q: %v", f.client, f.dst, exitVia, err)
			return
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			remote.Close()
			return
		}
		f.remote = remote
		queued := f.queue
		f.queue = nil
		f.mu.Unlock()
		for _, b := range queued {
			remote.Write(b)
		}
		debugLog("[rules] UDP flow %s → %s via %q", f.client, f.dst, exitVia)
		b := make([]byte, 65535)
		for {
			remote.SetReadDeadline(time.Now().Add(f.idle))
			n, err := remote.Read(b)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() &&
					time.Since(time.Unix(0, f.last.Load())) < f.idle {
					continue // the client is still sending
				}
				return
			}
			f.touch()
			f.af.Add(0, int64(n))
			f.reply(b[:n])
		}
	}()
	return true
}

// dnsRefreshLoop periodically re-resolves domain rules.
func (e *Engine) dnsRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.refreshDomainRules()
		}
	}
}

func (e *Engine) refreshDomainRules() {
	rules := e.host.Rules()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range rules {
		if r.Enabled && r.Type == "domain" {
			e.applyDomainRule(r)
		}
	}
}

// copyCtx copies data respecting context cancellation.
func copyCtx(ctx context.Context, dst, src net.Conn) {
	done := make(chan struct{})
	go func() {
		io.Copy(dst, src)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		src.Close()
		dst.Close()
		<-done
	}
}

// halfCloseAndBound ends c's write side (a relay stream that can't carry
// a half-close stays open) and bounds how long its read side may linger.
func halfCloseAndBound(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else if nh, ok := c.(interface{ NoHalfClose() bool }); !ok || !nh.NoHalfClose() {
		c.Close()
	}
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
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

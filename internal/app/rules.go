package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The rule engine routes destinations (IPs, CIDRs, ranges, domains) to
// exits, per flow (L4). The rule table always exists: it routes the flows
// of VPN and proxy users without their own exit (see route.go). Where the
// node has host networking and NET_ADMIN it also captures this host's
// matching traffic — its own and that of hosts routed through it — into
// transparent listeners on loopback (rules_capture_linux.go).
type ruleEngine struct {
	app        *App
	mu         sync.Mutex
	cancel     context.CancelFunc
	appliedIPT map[string][]string // ruleID → capture lines (captureLines format)
	domainIPs  map[string][]string // ruleID → resolved IPs (for domain rules)
	dnsCancel  context.CancelFunc
	destToExit sync.Map // "ruleID|target" → ruleExitInfo
	mode       captureMode
	mode6      captureMode
}

var ruleEng *ruleEngine

const (
	ruleProxyPort    = 12380
	ruleUDPProxyPort = 12381
	ruleBypassMark   = 0x1234 // SO_MARK of our own dials: never captured
)

// StartRuleEngine builds the rule table and, when the node may, the capture.
func (a *App) StartRuleEngine() {
	cfg := a.store.Get()
	e := &ruleEngine{
		app:        a,
		appliedIPT: make(map[string][]string),
		domainIPs:  make(map[string][]string),
	}
	ctx, cancel := context.WithCancel(a.appCtx)
	e.cancel = cancel
	if !CheckHostNetwork() {
		debugLog("[rules] not in host network mode: no capture")
	} else if ok, _ := CheckCapability(); !ok {
		debugLog("[rules] no NET_ADMIN: no capture")
	} else {
		e.startCapture(ctx, cfg)
	}
	e.mu.Lock()
	ruleEng = e
	for _, r := range cfg.Rules {
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

// startCapture opens the loopback listeners and installs each family's
// capture whose listeners are up.
func (e *ruleEngine) startCapture(ctx context.Context, cfg Config) {
	captureTeardown()
	exec.Command("ip", "addr", "add", ruleProxyIP+"/32", "dev", "lo").Run()
	for _, v6 := range []bool{false, true} {
		ip := ruleProxyIP
		if v6 {
			ip = "[" + ruleProxyIP6 + "]"
		}
		ln, err := listenTCPTransparent(fmt.Sprintf("%s:%d", ip, ruleProxyPort))
		if err != nil {
			log.Printf("[rules] capture listen %s: %v", ip, err)
			continue
		}
		uln, err := listenUDPTransparent(fmt.Sprintf("%s:%d", ip, ruleUDPProxyPort))
		if err != nil {
			ln.Close()
			log.Printf("[rules] capture UDP listen %s: %v", ip, err)
			continue
		}
		mode := captureSetup(v6, cfg)
		if mode == captureNone {
			ln.Close()
			uln.Close()
			continue
		}
		if v6 {
			e.mode6 = mode
		} else {
			e.mode = mode
		}
		go e.serveProxy(ctx, ln)
		go e.serveUDPProxy(ctx, uln)
		go func() { <-ctx.Done(); ln.Close(); uln.Close() }()
	}
	if e.mode == captureDNAT {
		log.Printf("[rules] capture=dnat: only this node's own traffic is captured (no TPROXY)")
	}
}

// StopRuleEngine removes the capture and stops the engine.
func (a *App) StopRuleEngine() {
	e := ruleEng
	if e == nil {
		return
	}
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

// RuleCaptureMode reports how this host's own and routed traffic is
// captured: "tproxy", "dnat" (this node only) or "none" (rules then apply
// to VPN and proxy users only).
func RuleCaptureMode() string {
	if ruleEng == nil {
		return captureNone.String()
	}
	return ruleEng.mode.String()
}

// RuleEngineAvailable reports whether routing rules can be used (always:
// without capture they still route VPN and proxy users).
func RuleEngineAvailable() bool { return true }

// RuleExitFor returns the exit of the enabled routing rule that owns host
// (an IP literal or a host name), for flows of users without their own exit.
func RuleExitFor(host string) (via, mode string, paths []string, ok bool) {
	if ruleEng == nil {
		return "", "", nil, false
	}
	via, mode, paths = ruleEng.lookupExitFull(host)
	return via, mode, paths, via != ""
}

// ApplyRule enables a routing rule.
func (a *App) ApplyRule(r RoutingRule) {
	if ruleEng == nil {
		return
	}
	ruleEng.mu.Lock()
	defer ruleEng.mu.Unlock()
	ruleEng.applyRule(r)
}

// RemoveRule disables a routing rule.
func (a *App) RemoveRule(id string) {
	if ruleEng == nil {
		return
	}
	ruleEng.mu.Lock()
	defer ruleEng.mu.Unlock()
	ruleEng.removeRuleLocked(id)
}

type ruleExitInfo struct {
	ruleID    string
	target    string // IP, CIDR, a-b range, or "name:<domain>"
	priority  int
	exitVia   string
	exitMode  string
	exitPaths []string
}

func (e *ruleEngine) applyRule(r RoutingRule) {
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

func (e *ruleEngine) installLines(id string, lines []string) {
	for _, l := range lines {
		runCaptureLine(l, false)
	}
	e.appliedIPT[id] = lines
}

// applyDomainRule registers the domains themselves (for flows that carry a
// host name) and their current A/AAAA addresses (for IP flows and capture).
func (e *ruleEngine) applyDomainRule(r RoutingRule) {
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
		ips, err := e.app.ResolveViaExit(ctx, domain, r.ExitVia)
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

func (e *ruleEngine) registerTargetExit(r RoutingRule, target string) {
	e.destToExit.Store(r.ID+"|"+target, ruleExitInfo{ruleID: r.ID, target: target, priority: r.Priority,
		exitVia: r.ExitVia, exitMode: r.ExitMode, exitPaths: r.ExitPaths})
}

func (e *ruleEngine) removeRuleLocked(id string) {
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
func (e *ruleEngine) lookupExitFull(host string) (string, string, []string) {
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	name := ""
	if ip == nil {
		name = strings.ToLower(strings.TrimSuffix(host, "."))
		if name == "" {
			return "", "", nil
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
	if best == nil {
		return "", "", nil
	}
	return best.exitVia, best.exitMode, best.exitPaths
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
func (e *ruleEngine) serveProxy(ctx context.Context, ln net.Listener) {
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
	return getOriginalDst(conn)
}

func (e *ruleEngine) handleConn(parentCtx context.Context, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	origDst, err := capturedDst(conn)
	if err != nil {
		debugLog("[rules] original destination: %v", err)
		return
	}
	host, _, _ := net.SplitHostPort(origDst)
	exitVia, exitMode, exitPaths := e.lookupExitFull(host)
	var remote net.Conn
	if exitVia == "" {
		// no rule owns it any more (removed while the flow was in flight)
		remote, err = dialTCPMarked(origDst, ruleBypassMark)
	} else {
		// Fail closed: a rule-matched flow only ever leaves through its exit.
		remote, err = e.app.dialExitWithPaths(ctx, exitVia, exitPaths, exitMode, origDst)
	}
	if err != nil {
		log.Printf("[rules] %s via %q: %v", origDst, exitVia, err)
		return
	}
	debugLog("[rules] %s connected via %q", origDst, exitVia)
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
}

func (f *udpFlow) touch() { f.last.Store(time.Now().UnixNano()) }

// send forwards a client datagram, queueing it while the exit dial is
// still in flight.
func (f *udpFlow) send(b []byte) {
	f.touch()
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
func (e *ruleEngine) serveUDPProxy(ctx context.Context, ln *net.UDPConn) {
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
func (e *ruleEngine) startUDPFlow(ctx context.Context, f *udpFlow, tproxied bool, ln *net.UDPConn, done func()) bool {
	if tproxied {
		local, err := dialUDPAs(f.dst, f.client)
		if err != nil {
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
		defer done()
		exitVia, exitMode, exitPaths := e.lookupExitFull(f.dst.IP.String())
		var remote net.Conn
		var err error
		if exitVia == "" {
			remote, err = dialUDPMarked(f.dst.String(), ruleBypassMark)
		} else {
			// Fail closed: never dialled from this node when the exit is down.
			remote, err = e.app.dialExitUDPPaths(ctx, exitVia, exitPaths, exitMode, f.dst.String())
		}
		if err != nil {
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
			f.reply(b[:n])
		}
	}()
	return true
}

// dnsRefreshLoop periodically re-resolves domain rules.
func (e *ruleEngine) dnsRefreshLoop(ctx context.Context) {
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

func (e *ruleEngine) refreshDomainRules() {
	cfg := e.app.store.Get()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range cfg.Rules {
		if r.Enabled && r.Type == "domain" {
			e.applyDomainRule(r)
		}
	}
}

// CRUD operations for rules

func (a *App) AddRule(r RoutingRule) {
	a.store.Update(func(c *Config) {
		c.Rules = append(c.Rules, r)
	})
	if r.Enabled {
		a.ApplyRule(r)
	}
}

func (a *App) UpdateRule(id string, r RoutingRule) {
	a.RemoveRule(id)
	a.store.Update(func(c *Config) {
		for i, existing := range c.Rules {
			if existing.ID == id {
				c.Rules[i] = r
				return
			}
		}
	})
	if r.Enabled {
		a.ApplyRule(r)
	}
}

func (a *App) DeleteRule(id string) {
	a.RemoveRule(id)
	a.store.Update(func(c *Config) {
		for i, r := range c.Rules {
			if r.ID == id {
				c.Rules = append(c.Rules[:i], c.Rules[i+1:]...)
				return
			}
		}
	})
}

func (a *App) ToggleRule(id string, enabled bool) {
	var rule RoutingRule
	a.store.Update(func(c *Config) {
		for i, r := range c.Rules {
			if r.ID == id {
				c.Rules[i].Enabled = enabled
				rule = c.Rules[i]
				return
			}
		}
	})
	if enabled {
		a.ApplyRule(rule)
	} else {
		a.RemoveRule(id)
	}
}

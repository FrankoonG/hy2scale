package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

// ruleEngine manages iptables-based routing rules on the host.
// It redirects matching traffic to a local transparent proxy,
// which then dials through the specified exit node.
type ruleEngine struct {
	app         *App
	mu          sync.Mutex
	proxyAddr   string // transparent proxy listen address
	proxyPort   int
	listener    net.Listener
	cancel      context.CancelFunc
	appliedIPT  map[string][]string // ruleID → iptables args (for cleanup)
	domainIPs   map[string][]string // ruleID → resolved IPs (for domain rules)
	dnsCancel   context.CancelFunc
	destToExit  sync.Map // "ip:port" → exitVia (for proxy routing)
}

var (
	ruleEng     *ruleEngine
	ruleEngOnce sync.Once
)

const (
	ruleProxyPort    = 12380
	ruleUDPProxyPort = 12381
	ruleBypassMark   = 0x1234 // SO_MARK value to exclude proxy traffic from REDIRECT
)

// StartRuleEngine initializes the rule engine if in host network mode.
func (a *App) StartRuleEngine() {
	if !CheckHostNetwork() {
		debugLog("[rules] not in host network mode, rule engine disabled")
		return
	}
	if ok, _ := CheckCapability(); !ok {
		debugLog("[rules] no NET_ADMIN capability, rule engine disabled")
		return
	}

	// Auto-restore TUN mode from saved config
	cfg := a.store.Get()
	if cfg.TunMode != nil && cfg.TunMode.Enabled && !ipfwdActive.Load() {
		log.Printf("[rules] restoring TUN mode from config (mode=%s)", cfg.TunMode.Mode)
		if err := a.EnableTunMode(cfg.TunMode.Mode); err != nil {
			log.Printf("[rules] TUN restore failed: %v, falling back to proxy", err)
			// Clear the enabled flag so we don't get stuck in "starting"
			a.store.Update(func(c *Config) {
				if c.TunMode != nil {
					c.TunMode.Enabled = false
				}
			})
		} else {
			return // TUN mode handles everything
		}
	}

	ruleEngOnce = sync.Once{} // allow re-init
	ruleEng = &ruleEngine{
		app:        a,
		proxyPort:  ruleProxyPort,
		proxyAddr:  fmt.Sprintf("127.0.0.99:%d", ruleProxyPort),
		appliedIPT: make(map[string][]string),
		domainIPs:  make(map[string][]string),
	}

	// Add the proxy bind address to loopback
	run("ip", "addr", "add", "127.0.0.99/32", "dev", "lo")

	ctx, cancel := context.WithCancel(a.appCtx)
	ruleEng.cancel = cancel

	// Start transparent TCP proxy
	ln, err := net.Listen("tcp", ruleEng.proxyAddr)
	if err != nil {
		log.Printf("[rules] proxy listen error: %v", err)
		return
	}
	ruleEng.listener = ln
	go ruleEng.serveProxy(ctx)
	go func() { <-ctx.Done(); ln.Close() }()

	// Start UDP+TCP proxy — add bypass rules in both OUTPUT and PREROUTING
	for _, chain := range []string{"OUTPUT", "PREROUTING"} {
		for _, proto := range []string{"tcp", "udp"} {
			iptRun(iptVariant(), "-t", "nat", "-I", chain, "-m", "mark", "--mark",
				fmt.Sprintf("0x%x", ruleBypassMark), "-p", proto, "-j", "RETURN")
		}
	}
	go ruleEng.serveUDPProxy(ctx)

	log.Printf("[rules] engine started, proxy on %s", ruleEng.proxyAddr)

	// Exclude relay peer ports from rules (prevent DNAT intercepting QUIC)
	cfg = a.store.Get()
	for _, cl := range cfg.Clients {
		addr := extractPrimaryAddr(cl.Addr)
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		ips, _ := net.LookupHost(host)
		for _, ip := range ips {
			for _, chain := range []string{"OUTPUT", "PREROUTING"} {
				for _, proto := range []string{"tcp", "udp"} {
					iptRun(iptVariant(), "-t", "nat", "-I", chain,
						"-d", ip, "-p", proto, "--dport", port, "-j", "RETURN")
				}
			}
		}
	}

	// Apply all enabled rules
	for _, r := range cfg.Rules {
		if r.Enabled {
			ruleEng.applyRule(r)
		}
	}

	// Start periodic DNS resolver for domain rules
	dnsCtx, dnsCancel := context.WithCancel(ctx)
	ruleEng.dnsCancel = dnsCancel
	go ruleEng.dnsRefreshLoop(dnsCtx)
}

// StopRuleEngine removes all rules and stops the proxy.
func (a *App) StopRuleEngine() {
	if ruleEng == nil {
		return
	}
	ruleEng.mu.Lock()
	defer ruleEng.mu.Unlock()
	// Remove all iptables rules
	for id := range ruleEng.appliedIPT {
		ruleEng.removeIPTRulesLocked(id)
	}
	if ruleEng.dnsCancel != nil {
		ruleEng.dnsCancel()
	}
	if ruleEng.cancel != nil {
		ruleEng.cancel()
	}
	if ruleEng.listener != nil {
		ruleEng.listener.Close()
	}
	log.Printf("[rules] engine stopped")
}

// RuleEngineAvailable returns true if the rule engine can operate.
func RuleEngineAvailable() bool {
	return CheckHostNetwork()
}

// TunModeActive returns whether TUN IP forwarding is active.
func TunModeActive() bool {
	return ipfwdActive.Load()
}

// EnableTunMode stops proxy mode and starts TUN IP forwarding.
func (a *App) EnableTunMode(mode string) error {
	// Stop proxy mode (removes iptables DNAT rules)
	a.StopRuleEngine()

	// Collect targets for TUN
	cfg := a.store.Get()
	var targets []ipfwdTarget
	for _, r := range cfg.Rules {
		if !r.Enabled || r.Type != "ip" {
			continue
		}
		if mode == "mixed" && !a.isRuleRoutable(r) {
			continue // skip non-routable targets in mixed mode
		}
		targets = append(targets, ipfwdTarget{
			cidrs:   r.Targets,
			exitVia: r.ExitVia,
		})
	}

	if err := a.StartIPForwarding(targets); err != nil {
		// Fallback: restart proxy mode
		a.StartRuleEngine()
		return err
	}

	// Save config BEFORE restarting proxy (so auto-restore check sees correct state)
	a.store.Update(func(c *Config) {
		c.TunMode = &TunModeConfig{Enabled: true, Mode: mode}
	})

	// Re-start proxy mode for rules NOT handled by TUN (mixed mode)
	if mode == "mixed" {
		a.startProxyForNonTunRules()
	}
	return nil
}

// DisableTunMode stops TUN and restarts proxy mode.
func (a *App) DisableTunMode() {
	a.StopIPForwarding()
	a.StopRuleEngine() // close any existing proxy listener first

	// Update config
	a.store.Update(func(c *Config) {
		if c.TunMode != nil {
			c.TunMode.Enabled = false
		}
	})

	// Restart proxy mode
	a.StartRuleEngine()
}

func (a *App) isRuleRoutable(r RoutingRule) bool {
	return isTargetRoutableCheck(r.Targets)
}

// startProxyForNonTunRules starts the proxy engine for rules not handled by TUN.
func (a *App) startProxyForNonTunRules() {
	// Re-init proxy for non-routable rules only
	a.StartRuleEngine()
	// The applyIPRule will skip routable targets since they're handled by TUN
}

// ApplyRule enables a routing rule (adds iptables + proxy mapping).
func (a *App) ApplyRule(r RoutingRule) {
	if ruleEng == nil {
		return
	}
	ruleEng.mu.Lock()
	defer ruleEng.mu.Unlock()
	ruleEng.applyRule(r)
}

// RemoveRule disables a routing rule (removes iptables + proxy mapping).
func (a *App) RemoveRule(id string) {
	if ruleEng == nil {
		return
	}
	ruleEng.mu.Lock()
	defer ruleEng.mu.Unlock()
	ruleEng.removeIPTRulesLocked(id)
	// Clean dest→exit mappings for this rule
	ruleEng.destToExit.Range(func(k, v any) bool {
		if exit, ok := v.(ruleExitInfo); ok && exit.ruleID == id {
			ruleEng.destToExit.Delete(k)
		}
		return true
	})
}

type ruleExitInfo struct {
	ruleID   string
	exitVia  string
	exitMode string
}

func (e *ruleEngine) applyRule(r RoutingRule) {
	switch r.Type {
	case "ip":
		e.applyIPRule(r)
	case "domain":
		e.applyDomainRule(r)
	}
}

func (e *ruleEngine) applyIPRule(r RoutingRule) {
	// Remove old rules for this ID first
	e.removeIPTRulesLocked(r.ID)

	if ipfwdActive.Load() {
		// TUN mode: add ip routing rules to capture traffic into TUN
		e.applyIPRuleTun(r)
		return
	}

	// Proxy mode: DNAT to transparent proxy
	var iptArgs []string
	udpProxyAddr := fmt.Sprintf("127.0.0.99:%d", ruleUDPProxyPort)
	for _, target := range r.Targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		for _, chain := range []string{"OUTPUT", "PREROUTING"} {
			if strings.Contains(target, "-") && !strings.Contains(target, "/") {
				for _, proto := range []string{"tcp", "udp"} {
					dst := e.proxyAddr
					if proto == "udp" {
						dst = udpProxyAddr
					}
					args := []string{"-t", "nat", "-A", chain,
						"-m", "iprange", "--dst-range", target,
						"-p", proto, "-j", "DNAT",
						"--to-destination", dst}
					iptRun(iptVariant(), args...)
					iptArgs = append(iptArgs, strings.Join(args, " "))
				}
			} else {
				for _, proto := range []string{"tcp", "udp"} {
					dst := e.proxyAddr
					if proto == "udp" {
						dst = udpProxyAddr
					}
					args := []string{"-t", "nat", "-A", chain,
						"-d", target,
						"-p", proto, "-j", "DNAT",
						"--to-destination", dst}
					iptRun(iptVariant(), args...)
					iptArgs = append(iptArgs, strings.Join(args, " "))
				}
			}
		}
		e.registerTargetExit(r.ID, target, r.ExitVia, r.ExitMode)
	}
	e.appliedIPT[r.ID] = iptArgs
	log.Printf("[rules] applied IP rule %q: %d targets → exit %s (proxy)", r.Name, len(r.Targets), r.ExitVia)
}

// applyIPRuleTun adds ip routing rules to capture traffic into the TUN device.
func (e *ruleEngine) applyIPRuleTun(r RoutingRule) {
	var ipRuleArgs []string
	for _, target := range r.Targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		// ip rule add to <target> lookup <table> priority 100
		run("ip", "rule", "add", "to", target, "lookup", ipfwdTable, "priority", "100")
		ipRuleArgs = append(ipRuleArgs, "to "+target+" lookup "+ipfwdTable+" priority 100")
		e.registerTargetExit(r.ID, target, r.ExitVia, r.ExitMode)
	}
	// Store for cleanup (use appliedIPT with "iprule:" prefix to distinguish)
	for _, arg := range ipRuleArgs {
		e.appliedIPT[r.ID] = append(e.appliedIPT[r.ID], "iprule:"+arg)
	}
	// Update TUN target list for packet routing
	if ipfwdEng != nil {
		ipfwdEng.addTargets(r.ID, r.Targets, r.ExitVia)
	}
	log.Printf("[rules] applied IP rule %q: %d targets → exit %s (tun)", r.Name, len(r.Targets), r.ExitVia)
}

// isTargetRoutableCheck checks if the first target in the list is reachable
// via the host's routing table (e.g. through a WG tunnel or direct route).
func isTargetRoutableCheck(targets []string) bool {
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		// Extract a test IP from the target (CIDR or range)
		testIP := t
		if idx := strings.Index(t, "/"); idx >= 0 {
			testIP = t[:idx]
		}
		if idx := strings.Index(t, "-"); idx >= 0 {
			testIP = t[:idx]
		}
		ip := net.ParseIP(testIP)
		if ip == nil {
			continue
		}
		// Try dialing UDP with a very short timeout to check routability
		conn, err := net.DialTimeout("udp", net.JoinHostPort(testIP, "1"), 100*time.Millisecond)
		if err != nil {
			return false
		}
		localAddr := conn.LocalAddr().String()
		conn.Close()
		// If local address is NOT loopback and NOT the target IP itself,
		// the target is routable via a real interface
		host, _, _ := net.SplitHostPort(localAddr)
		if host != "" && host != "127.0.0.1" && host != testIP {
			log.Printf("[rules] target %s routable via %s", testIP, host)
			return true
		}
		return false
	}
	return false
}

func (e *ruleEngine) applyDomainRule(r RoutingRule) {
	e.removeIPTRulesLocked(r.ID)

	var ruleArgs []string
	var resolvedIPs []string
	for _, domain := range r.Targets {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		ips, err := net.LookupHost(domain)
		if err != nil {
			debugLog("[rules] DNS resolve %s: %v", domain, err)
			continue
		}
		for _, ip := range ips {
			if ipfwdActive.Load() {
				// TUN mode: add ip rule for each resolved IP
				target := ip + "/32"
				run("ip", "rule", "add", "to", target, "lookup", ipfwdTable, "priority", "100")
				ruleArgs = append(ruleArgs, "iprule:to "+target+" lookup "+ipfwdTable+" priority 100")
			} else {
				// Proxy mode: DNAT
				udpDst := fmt.Sprintf("127.0.0.99:%d", ruleUDPProxyPort)
				for _, chain := range []string{"OUTPUT", "PREROUTING"} {
					for _, proto := range []string{"tcp", "udp"} {
						dst := e.proxyAddr
						if proto == "udp" {
							dst = udpDst
						}
						args := []string{"-t", "nat", "-A", chain,
							"-d", ip,
							"-p", proto, "-j", "DNAT",
							"--to-destination", dst}
						iptRun(iptVariant(), args...)
						ruleArgs = append(ruleArgs, strings.Join(args, " "))
					}
				}
			}
			resolvedIPs = append(resolvedIPs, ip)
			e.destToExit.Store(ip, ruleExitInfo{ruleID: r.ID, exitVia: r.ExitVia, exitMode: r.ExitMode})
		}
	}
	e.appliedIPT[r.ID] = ruleArgs
	e.domainIPs[r.ID] = resolvedIPs
	// Update TUN target list for domain resolved IPs
	if ipfwdActive.Load() && ipfwdEng != nil {
		var cidrs []string
		for _, ip := range resolvedIPs {
			cidrs = append(cidrs, ip+"/32")
		}
		ipfwdEng.addTargets(r.ID, cidrs, r.ExitVia)
	}
	mode := "proxy"
	if ipfwdActive.Load() {
		mode = "tun"
	}
	log.Printf("[rules] applied domain rule %q: %d domains → %d IPs → exit %s (%s)",
		r.Name, len(r.Targets), len(resolvedIPs), r.ExitVia, mode)
}

func (e *ruleEngine) registerTargetExit(ruleID, target, exitVia, exitMode string) {
	e.destToExit.Store(target, ruleExitInfo{ruleID: ruleID, exitVia: exitVia, exitMode: exitMode})
}

func (e *ruleEngine) removeIPTRulesLocked(id string) {
	args, ok := e.appliedIPT[id]
	if !ok {
		return
	}
	for _, argStr := range args {
		// Handle ip rule entries (TUN mode)
		if strings.HasPrefix(argStr, "iprule:") {
			ruleSpec := strings.TrimPrefix(argStr, "iprule:")
			parts := append([]string{"rule", "del"}, strings.Fields(ruleSpec)...)
			run("ip", parts...)
			continue
		}
		// Handle iptables entries (proxy mode)
		parts := strings.Fields(argStr)
		for i, p := range parts {
			if p == "-A" {
				parts[i] = "-D"
				break
			}
		}
		cmd := iptExec(iptVariant(), parts...)
		cmd.Run()
	}
	// Remove TUN targets
	if ipfwdEng != nil {
		ipfwdEng.removeTargetsForRule(id)
	}
	delete(e.appliedIPT, id)
	delete(e.domainIPs, id)
}

// lookupExit finds the exit route and mode for a destination IP.
func (e *ruleEngine) lookupExit(dstIP string) (string, string) {
	var resultVia, resultMode string
	e.destToExit.Range(func(k, v any) bool {
		target := k.(string)
		info := v.(ruleExitInfo)
		if target == dstIP {
			resultVia, resultMode = info.exitVia, info.exitMode
			return false
		}
		if strings.Contains(target, "/") {
			_, cidr, err := net.ParseCIDR(target)
			if err == nil && cidr.Contains(net.ParseIP(dstIP)) {
				resultVia, resultMode = info.exitVia, info.exitMode
				return false
			}
		}
		if strings.Contains(target, "-") && !strings.Contains(target, "/") {
			parts := strings.SplitN(target, "-", 2)
			if len(parts) == 2 {
				startIP := net.ParseIP(strings.TrimSpace(parts[0]))
				endIP := net.ParseIP(strings.TrimSpace(parts[1]))
				ip := net.ParseIP(dstIP)
				if startIP != nil && endIP != nil && ip != nil {
					if bytesCompare(ip.To4(), startIP.To4()) >= 0 &&
						bytesCompare(ip.To4(), endIP.To4()) <= 0 {
						resultVia, resultMode = info.exitVia, info.exitMode
						return false
					}
				}
			}
		}
		return true
	})
	return resultVia, resultMode
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

// serveProxy handles redirected TCP connections.
func (e *ruleEngine) serveProxy(ctx context.Context) {
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		go e.handleConn(ctx, conn)
	}
}

func (e *ruleEngine) handleConn(parentCtx context.Context, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	origDst, err := getOriginalDst(conn)
	if err != nil {
		debugLog("[rules] getOriginalDst failed: %v", err)
		return
	}

	host, _, _ := net.SplitHostPort(origDst)
	exitVia, exitMode := e.lookupExit(host)
	if exitVia == "" {
		debugLog("[rules] no exit for %s, direct", origDst)
		remote, err := net.DialTimeout("tcp", origDst, 10*time.Second)
		if err != nil {
			return
		}
		defer remote.Close()
		done := make(chan struct{})
		go func() { copyCtx(ctx, remote, conn); cancel(); done <- struct{}{} }()
		copyCtx(ctx, conn, remote)
		cancel()
		<-done
		return
	}

	log.Printf("[rules] %s → exit %s mode=%s", origDst, exitVia, exitMode)
	// Route through hy2 relay network (exit node dials the actual destination)
	remote, err := e.app.dialExitWithPaths(ctx, exitVia, []string{exitVia}, exitMode, origDst)
	if err != nil {
		// Fallback: direct dial with SO_MARK bypass (for TUN/routing mode)
		remote, err = dialTCPMarked(origDst, ruleBypassMark)
		if err != nil {
			log.Printf("[rules] dial %s error: %v", origDst, err)
			return
		}
		log.Printf("[rules] %s connected (direct fallback) local=%s remote=%s", origDst, remote.LocalAddr(), remote.RemoteAddr())
	} else {
		log.Printf("[rules] %s connected via %s", origDst, exitVia)
	}
	defer remote.Close()
	done := make(chan struct{})
	go func() {
		n, err := copyCtx(ctx, remote, conn)
		log.Printf("[rules] %s relay→client: %d bytes, err=%v", origDst, n, err)
		done <- struct{}{}
	}()
	n, err2 := copyCtx(ctx, conn, remote)
	log.Printf("[rules] %s client→relay: %d bytes, err=%v", origDst, n, err2)
	<-done
}

// serveUDPProxy handles redirected UDP packets.
// Uses conntrack to recover original destination since IP_RECVORIGDSTADDR
// doesn't work for OUTPUT chain REDIRECT/DNAT on Linux.
func (e *ruleEngine) serveUDPProxy(ctx context.Context) {
	udpAddr, _ := net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.99:%d", ruleUDPProxyPort))
	udpConn, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		log.Printf("[rules] UDP proxy listen error: %v", err)
		return
	}
	go func() { <-ctx.Done(); udpConn.Close() }()

	buf := make([]byte, 65535)
	log.Printf("[rules] UDP proxy listening on %s", udpAddr)
	for {
		n, srcAddr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[rules] UDP read error: %v", err)
			continue
		}
		log.Printf("[rules] UDP pkt from %s, %d bytes", srcAddr, n)

		data := make([]byte, n)
		copy(data, buf[:n])

		// Check if there's an existing session for this source
		srcKey := srcAddr.String()
		udpSessionsMu.Lock()
		sess, ok := udpSessions[srcKey]
		udpSessionsMu.Unlock()
		if ok {
			sess.lastActivity = time.Now()
			sess.remote.Write(data)
			continue
		}

		// New source: look up original destination via conntrack
		log.Printf("[rules] UDP new session for %s, querying conntrack", srcKey)
		origDst := conntrackOrigDst("udp", srcAddr.String(), ruleUDPProxyPort)
		log.Printf("[rules] UDP conntrack result for %s: %q", srcKey, origDst)
		if origDst == "" {
			debugLog("[rules] UDP no conntrack for %s", srcAddr)
			continue
		}
		origAddr, _ := net.ResolveUDPAddr("udp4", origDst)
		if origAddr == nil {
			continue
		}

		e.createUDPSession(ctx, srcAddr, origAddr, data, udpConn)
	}
}

// udpSession tracks an ongoing UDP relay for a specific src→dst pair.
type udpSession struct {
	remote net.Conn
	lastActivity time.Time
}

var (
	udpSessions   = make(map[string]*udpSession) // "srcIP:srcPort→dstIP:dstPort" → session
	udpSessionsMu sync.Mutex
)

func (e *ruleEngine) createUDPSession(ctx context.Context, src, origDst *net.UDPAddr, data []byte, listener *net.UDPConn) {
	srcKey := src.String()

	// New session: create relay connection
	host := origDst.IP.String()
	exitVia, _ := e.lookupExit(host)

	// Dial destination directly with SO_MARK bypass (like native hy2 server).
	remote, err := dialUDPMarked(origDst.String(), ruleBypassMark)
	if err != nil {
		udpSessionsMu.Unlock()
		debugLog("[rules] UDP dial %s: %v", origDst, err)
		return
	}

	sess := &udpSession{remote: remote, lastActivity: time.Now()}
	udpSessionsMu.Lock()
	udpSessions[srcKey] = sess
	udpSessionsMu.Unlock()

	debugLog("[rules] UDP session %s → %s (exit=%s)", src, origDst, exitVia)

	// Forward the first packet
	remote.Write(data)

	// Start reverse relay: remote → listener (runs until idle timeout)
	go func() {
		defer func() {
			udpSessionsMu.Lock()
			delete(udpSessions, srcKey)
			udpSessionsMu.Unlock()
			remote.Close()
		}()
		buf := make([]byte, 65535)
		for {
			remote.SetReadDeadline(time.Now().Add(60 * time.Second))
			n, err := remote.Read(buf)
			if err != nil {
				return
			}
			sess.lastActivity = time.Now()
			listener.WriteToUDP(buf[:n], src)
		}
	}()
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

// getOriginalDst is defined in l2tp.go — reuse it for rules proxy.
// It uses SO_ORIGINAL_DST to get the pre-DNAT destination.

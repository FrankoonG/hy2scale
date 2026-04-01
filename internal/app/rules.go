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

	// Start UDP proxy — add bypass rule so proxy's own outbound UDP isn't REDIRECT'd
	iptRun("iptables-legacy", "-t", "nat", "-I", "OUTPUT", "-m", "mark", "--mark",
		fmt.Sprintf("0x%x", ruleBypassMark), "-p", "udp", "-j", "RETURN")
	go ruleEng.serveUDPProxy(ctx)

	log.Printf("[rules] engine started, proxy on %s", ruleEng.proxyAddr)

	// Exclude relay peer ports from rules (prevent REDIRECT intercepting QUIC)
	cfg := a.store.Get()
	for _, cl := range cfg.Clients {
		addr := extractPrimaryAddr(cl.Addr)
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		ips, _ := net.LookupHost(host)
		for _, ip := range ips {
			for _, proto := range []string{"tcp", "udp"} {
				iptRun("iptables-legacy", "-t", "nat", "-I", "OUTPUT",
					"-d", ip, "-p", proto, "--dport", port, "-j", "RETURN")
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

	var iptArgs []string
	udpProxyAddr := fmt.Sprintf("127.0.0.99:%d", ruleUDPProxyPort)
	for _, target := range r.Targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if strings.Contains(target, "-") && !strings.Contains(target, "/") {
			// IP range — TCP DNAT + UDP REDIRECT
			tcpArgs := []string{"-t", "nat", "-A", "OUTPUT",
				"-m", "iprange", "--dst-range", target,
				"-p", "tcp", "-j", "DNAT",
				"--to-destination", e.proxyAddr}
			iptRun("iptables-legacy", tcpArgs...)
			iptArgs = append(iptArgs, strings.Join(tcpArgs, " "))

			udpArgs := []string{"-t", "nat", "-A", "OUTPUT",
				"-m", "iprange", "--dst-range", target,
				"-p", "udp", "-j", "DNAT",
				"--to-destination", udpProxyAddr}
			iptRun("iptables-legacy", udpArgs...)
			iptArgs = append(iptArgs, strings.Join(udpArgs, " "))
		} else {
			// Single IP or CIDR — TCP DNAT + UDP REDIRECT
			tcpArgs := []string{"-t", "nat", "-A", "OUTPUT",
				"-d", target,
				"-p", "tcp", "-j", "DNAT",
				"--to-destination", e.proxyAddr}
			iptRun("iptables-legacy", tcpArgs...)
			iptArgs = append(iptArgs, strings.Join(tcpArgs, " "))

			udpArgs := []string{"-t", "nat", "-A", "OUTPUT",
				"-d", target,
				"-p", "udp", "-j", "DNAT",
				"--to-destination", udpProxyAddr}
			iptRun("iptables-legacy", udpArgs...)
			iptArgs = append(iptArgs, strings.Join(udpArgs, " "))
		}
		// Register all IPs for this target in destToExit
		e.registerTargetExit(r.ID, target, r.ExitVia, r.ExitMode)
	}
	e.appliedIPT[r.ID] = iptArgs
	log.Printf("[rules] applied IP rule %q: %d targets → exit %s", r.Name, len(r.Targets), r.ExitVia)
}

func (e *ruleEngine) applyDomainRule(r RoutingRule) {
	e.removeIPTRulesLocked(r.ID)

	var iptArgs []string
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
		udpDst := fmt.Sprintf("127.0.0.99:%d", ruleUDPProxyPort)
		for _, ip := range ips {
			tcpArgs := []string{"-t", "nat", "-A", "OUTPUT",
				"-d", ip,
				"-p", "tcp", "-j", "DNAT",
				"--to-destination", e.proxyAddr}
			iptRun("iptables-legacy", tcpArgs...)
			iptArgs = append(iptArgs, strings.Join(tcpArgs, " "))

			udpArgs := []string{"-t", "nat", "-A", "OUTPUT",
				"-d", ip,
				"-p", "udp", "-j", "DNAT",
				"--to-destination", udpDst}
			iptRun("iptables-legacy", udpArgs...)
			iptArgs = append(iptArgs, strings.Join(udpArgs, " "))

			resolvedIPs = append(resolvedIPs, ip)
			e.destToExit.Store(ip, ruleExitInfo{ruleID: r.ID, exitVia: r.ExitVia, exitMode: r.ExitMode})
		}
	}
	e.appliedIPT[r.ID] = iptArgs
	e.domainIPs[r.ID] = resolvedIPs
	log.Printf("[rules] applied domain rule %q: %d domains → %d IPs → exit %s",
		r.Name, len(r.Targets), len(resolvedIPs), r.ExitVia)
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
		parts := strings.Fields(argStr)
		// Change -A to -D for deletion
		for i, p := range parts {
			if p == "-A" {
				parts[i] = "-D"
				break
			}
		}
		cmd := iptExec("iptables-legacy", parts...)
		cmd.Run()
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

func (e *ruleEngine) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

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
		go func() { copyCtx(ctx, remote, conn); done <- struct{}{} }()
		copyCtx(ctx, conn, remote)
		<-done
		return
	}

	debugLog("[rules] %s → exit %s mode=%s", origDst, exitVia, exitMode)
	remote, err := e.app.dialExitWithMode(ctx, exitVia, exitMode, origDst)
	if err != nil {
		debugLog("[rules] dial exit %s error: %v", exitVia, err)
		return
	}
	defer remote.Close()
	done := make(chan struct{})
	go func() { copyCtx(ctx, remote, conn); done <- struct{}{} }()
	copyCtx(ctx, conn, remote)
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
		log.Printf("[rules] UDP packet from %s, %d bytes", srcAddr, n)

		// Look up original destination via conntrack
		origDst := conntrackOrigDst("udp", srcAddr.String(), ruleUDPProxyPort)
		if origDst == "" {
			debugLog("[rules] UDP no conntrack for %s", srcAddr)
			continue
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		origAddr, _ := net.ResolveUDPAddr("udp4", origDst)
		if origAddr == nil {
			continue
		}

		go e.handleUDPPacket(ctx, srcAddr, origAddr, data, udpConn)
	}
}

func (e *ruleEngine) handleUDPPacket(ctx context.Context, src, origDst *net.UDPAddr, data []byte, listener *net.UDPConn) {
	host := origDst.IP.String()
	exitVia, _ := e.lookupExit(host)

	var remote net.Conn
	var err error
	if exitVia != "" {
		log.Printf("[rules] UDP %s → exit %s", origDst, exitVia)
		remote, err = e.app.dialExitUDP(ctx, exitVia, origDst.String())
		if err != nil {
			log.Printf("[rules] UDP dialExitUDP %s: %v, falling back to direct", origDst, err)
		}
	}
	if remote == nil || err != nil {
		remote, err = dialUDPMarked(origDst.String(), ruleBypassMark)
	}
	if err != nil {
		log.Printf("[rules] UDP dial %s: %v", origDst, err)
		return
	}
	defer remote.Close()

	if _, err := remote.Write(data); err != nil {
		log.Printf("[rules] UDP write %s: %v", origDst, err)
		return
	}
	log.Printf("[rules] UDP sent %d bytes to %s", len(data), origDst)

	remote.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp := make([]byte, 65535)
	rn, err := remote.Read(resp)
	if err != nil {
		log.Printf("[rules] UDP read %s: %v", origDst, err)
		return
	}
	log.Printf("[rules] UDP reply %d bytes from %s", rn, origDst)

	listener.WriteToUDP(resp[:rn], src)
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

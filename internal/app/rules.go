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

const ruleProxyPort = 12380

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

	// Start transparent proxy
	ln, err := net.Listen("tcp", ruleEng.proxyAddr)
	if err != nil {
		log.Printf("[rules] proxy listen error: %v", err)
		return
	}
	ruleEng.listener = ln
	go ruleEng.serveProxy(ctx)
	go func() { <-ctx.Done(); ln.Close() }()

	log.Printf("[rules] engine started, proxy on %s", ruleEng.proxyAddr)

	// Apply all enabled rules
	cfg := a.store.Get()
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
	ruleID  string
	exitVia string
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
	for _, target := range r.Targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		// Support: single IP, CIDR, IP range (x.x.x.x-y.y.y.y)
		if strings.Contains(target, "-") && !strings.Contains(target, "/") {
			// IP range: use -m iprange
			args := []string{"-t", "nat", "-A", "OUTPUT",
				"-m", "iprange", "--dst-range", target,
				"-p", "tcp", "-j", "DNAT",
				"--to-destination", e.proxyAddr}
			iptRun("iptables-legacy", args...)
			iptArgs = append(iptArgs, strings.Join(args, " "))
		} else {
			// Single IP or CIDR
			args := []string{"-t", "nat", "-A", "OUTPUT",
				"-d", target,
				"-p", "tcp", "-j", "DNAT",
				"--to-destination", e.proxyAddr}
			iptRun("iptables-legacy", args...)
			iptArgs = append(iptArgs, strings.Join(args, " "))
		}
		// Register all IPs for this target in destToExit
		e.registerTargetExit(r.ID, target, r.ExitVia)
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
		for _, ip := range ips {
			args := []string{"-t", "nat", "-A", "OUTPUT",
				"-d", ip,
				"-p", "tcp", "-j", "DNAT",
				"--to-destination", e.proxyAddr}
			iptRun("iptables-legacy", args...)
			iptArgs = append(iptArgs, strings.Join(args, " "))
			resolvedIPs = append(resolvedIPs, ip)
			e.destToExit.Store(ip, ruleExitInfo{ruleID: r.ID, exitVia: r.ExitVia})
		}
	}
	e.appliedIPT[r.ID] = iptArgs
	e.domainIPs[r.ID] = resolvedIPs
	log.Printf("[rules] applied domain rule %q: %d domains → %d IPs → exit %s",
		r.Name, len(r.Targets), len(resolvedIPs), r.ExitVia)
}

func (e *ruleEngine) registerTargetExit(ruleID, target, exitVia string) {
	// For CIDR/range, we store the CIDR itself as key; proxy will match
	e.destToExit.Store(target, ruleExitInfo{ruleID: ruleID, exitVia: exitVia})
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

// lookupExit finds the exit route for a destination IP.
func (e *ruleEngine) lookupExit(dstIP string) string {
	// Direct IP match
	var result string
	e.destToExit.Range(func(k, v any) bool {
		target := k.(string)
		info := v.(ruleExitInfo)
		if target == dstIP {
			result = info.exitVia
			return false
		}
		// CIDR match
		if strings.Contains(target, "/") {
			_, cidr, err := net.ParseCIDR(target)
			if err == nil && cidr.Contains(net.ParseIP(dstIP)) {
				result = info.exitVia
				return false
			}
		}
		// Range match (x.x.x.x-y.y.y.y)
		if strings.Contains(target, "-") && !strings.Contains(target, "/") {
			parts := strings.SplitN(target, "-", 2)
			if len(parts) == 2 {
				startIP := net.ParseIP(strings.TrimSpace(parts[0]))
				endIP := net.ParseIP(strings.TrimSpace(parts[1]))
				ip := net.ParseIP(dstIP)
				if startIP != nil && endIP != nil && ip != nil {
					if bytesCompare(ip.To4(), startIP.To4()) >= 0 &&
						bytesCompare(ip.To4(), endIP.To4()) <= 0 {
						result = info.exitVia
						return false
					}
				}
			}
		}
		return true
	})
	return result
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
	exitVia := e.lookupExit(host)
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

	debugLog("[rules] %s → exit %s", origDst, exitVia)
	remote, err := e.app.dialExit(ctx, exitVia, origDst)
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

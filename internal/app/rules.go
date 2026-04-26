package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
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
// Each rule decides independently whether to use TUN (via r.UseTun) or normal
// relay proxy; the TUN engine starts lazily when the first TUN rule is
// applied and stops automatically when the last one is removed.
func (a *App) StartRuleEngine() {
	if !CheckHostNetwork() {
		debugLog("[rules] not in host network mode, rule engine disabled")
		return
	}
	if ok, _ := CheckCapability(); !ok {
		debugLog("[rules] no NET_ADMIN capability, rule engine disabled")
		return
	}

	cfg := a.store.Get()
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

// TunModeActive returns whether the TUN IP forwarding engine is running.
func TunModeActive() bool {
	return ipfwdActive.Load()
}

// RuleUsesTun reports whether an enabled rule is actually running through
// the TUN path right now (as opposed to the relay proxy fallback). A rule
// with UseTun=true only takes the TUN path when the exit peer is TUN-capable
// and the target is locally routable.
func (a *App) RuleUsesTun(r RoutingRule) bool {
	if !r.Enabled || r.Type != "ip" || !r.UseTun || r.ExitVia == "" {
		return false
	}
	if !a.node.IsPeerTunCapable(r.ExitVia) {
		return false
	}
	return a.isRuleRoutable(r)
}

func (a *App) isRuleRoutable(r RoutingRule) bool {
	return isTargetRoutableCheck(r.Targets)
}

// reconcileRuleModes re-evaluates every enabled rule's TUN eligibility and
// re-applies any whose current mode (proxy vs TUN) doesn't match what
// RuleUsesTun says it should be. Called periodically so rules flip to TUN
// once their exit peer comes online, and fall back to proxy when it drops.
func (a *App) reconcileRuleModes() {
	if ruleEng == nil {
		return
	}
	cfg := a.store.Get()
	ruleEng.mu.Lock()
	// Snapshot current mode per rule: if eng.appliedIPT[id] has any entry
	// starting with "iprule:" it is currently on the TUN path, otherwise proxy.
	currentOnTun := make(map[string]bool)
	for id, args := range ruleEng.appliedIPT {
		for _, a := range args {
			if len(a) > 7 && a[:7] == "iprule:" {
				currentOnTun[id] = true
				break
			}
		}
	}
	ruleEng.mu.Unlock()

	for _, r := range cfg.Rules {
		if !r.Enabled || r.Type != "ip" {
			continue
		}
		wantTun := a.RuleUsesTun(r)
		haveTun := currentOnTun[r.ID]
		if wantTun != haveTun {
			// Re-read the rule right before applying to avoid overwriting a
			// concurrent UpdateRule that just persisted new fields (priority,
			// exit_via, etc.) between our snapshot and now.
			latest := a.store.Get()
			var fresh *RoutingRule
			for i := range latest.Rules {
				if latest.Rules[i].ID == r.ID {
					fresh = &latest.Rules[i]
					break
				}
			}
			if fresh == nil || !fresh.Enabled {
				continue
			}
			if a.RuleUsesTun(*fresh) == haveTun {
				continue // state already matches after re-read
			}
			log.Printf("[rules] reconcile %s: re-apply (wantTun=%v haveTun=%v)", fresh.Name, !haveTun, haveTun)
			ruleEng.mu.Lock()
			ruleEng.removeIPTRulesLocked(fresh.ID)
			ruleEng.mu.Unlock()
			a.ensureTunEngineForRules()
			ruleEng.mu.Lock()
			ruleEng.applyRule(*fresh)
			ruleEng.mu.Unlock()
		}
	}
	// After reconciling, stop TUN engine if nothing uses it
	a.ensureTunEngineForRules()
}

// StartRuleReconciler periodically re-evaluates rule modes (TUN vs proxy).
func (a *App) StartRuleReconciler(ctx context.Context) {
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.reconcileRuleModes()
			}
		}
	}()
}

// ensureTunEngineForRules starts the TUN engine if any enabled rule currently
// qualifies for TUN, or stops it if none do. Safe to call repeatedly.
func (a *App) ensureTunEngineForRules() {
	cfg := a.store.Get()
	var tunTargets []ipfwdTarget
	for _, r := range cfg.Rules {
		if !a.RuleUsesTun(r) {
			continue
		}
		tunTargets = append(tunTargets, ipfwdTarget{
			cidrs:   r.Targets,
			exitVia: r.ExitVia,
			ruleID:  r.ID,
		})
	}
	if len(tunTargets) == 0 {
		if ipfwdActive.Load() {
			a.StopIPForwarding()
		}
		return
	}
	if !ipfwdActive.Load() {
		if err := a.StartIPForwarding(tunTargets); err != nil {
			log.Printf("[rules] TUN engine start failed: %v — affected rules will run as proxy", err)
		}
	}
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
	ruleEng.removeIPTRulesLocked(id)
	ruleEng.destToExit.Range(func(k, v any) bool {
		if exit, ok := v.(ruleExitInfo); ok && exit.ruleID == id {
			ruleEng.destToExit.Delete(k)
		}
		return true
	})
	ruleEng.mu.Unlock()
	// After removing, re-evaluate whether the TUN engine should still run.
	a.ensureTunEngineForRules()
}

type ruleExitInfo struct {
	ruleID    string
	exitVia   string
	exitMode  string
	exitPaths []string
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

	// Per-rule TUN: if the rule requests TUN AND the exit peer is capable AND
	// the target is routable, take the TUN path. Otherwise fall through to
	// the normal proxy DNAT path.
	if e.app.RuleUsesTun(r) {
		// Lazily start the TUN engine if this is the first TUN rule
		e.app.ensureTunEngineForRules()
		if ipfwdActive.Load() {
			e.applyIPRuleTun(r)
			return
		}
		log.Printf("[rules] rule %q requested TUN but engine failed to start — falling back to proxy", r.Name)
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
		e.registerTargetExit(r.ID, target, r.ExitVia, r.ExitMode, r.ExitPaths)
	}
	e.appliedIPT[r.ID] = iptArgs
	log.Printf("[rules] applied IP rule %q (prio %d): %d targets → exit %s (proxy)", r.Name, r.Priority, len(r.Targets), r.ExitVia)
	// Re-sort proxy chain so higher-priority rules are evaluated first.
	e.resortProxyChain()
}

// resortProxyChain removes every proxy (DNAT) iptables entry installed by
// the rule engine and re-inserts them in priority order (desc). Rules with
// higher Priority end up earlier in the chain so they win on CIDR overlap.
// TUN rules are untouched (they live in the routing layer via ip rule).
func (e *ruleEngine) resortProxyChain() {
	cfg := e.app.store.Get()
	idToRule := make(map[string]RoutingRule)
	for _, r := range cfg.Rules {
		idToRule[r.ID] = r
	}
	type entry struct {
		id   string
		rule RoutingRule
		args []string
	}
	var proxyEntries []entry
	for id, args := range e.appliedIPT {
		r, ok := idToRule[id]
		if !ok || !r.Enabled {
			continue
		}
		isProxy := false
		for _, a := range args {
			if !strings.HasPrefix(a, "iprule:") {
				isProxy = true
				break
			}
		}
		if !isProxy {
			continue
		}
		proxyEntries = append(proxyEntries, entry{id, r, args})
	}
	if len(proxyEntries) <= 1 {
		return
	}
	// Remove every proxy line
	for _, pe := range proxyEntries {
		for _, argStr := range pe.args {
			if strings.HasPrefix(argStr, "iprule:") {
				continue
			}
			parts := strings.Fields(argStr)
			for i, p := range parts {
				if p == "-A" {
					parts[i] = "-D"
					break
				}
			}
			iptExec(iptVariant(), parts...).Run()
		}
	}
	// Sort: Priority desc, then ID asc for deterministic tie-break
	sort.SliceStable(proxyEntries, func(i, j int) bool {
		if proxyEntries[i].rule.Priority != proxyEntries[j].rule.Priority {
			return proxyEntries[i].rule.Priority > proxyEntries[j].rule.Priority
		}
		return proxyEntries[i].id < proxyEntries[j].id
	})
	// Re-add in sorted order (each chain is -A, so first added = first match)
	for _, pe := range proxyEntries {
		for _, argStr := range pe.args {
			if strings.HasPrefix(argStr, "iprule:") {
				continue
			}
			parts := strings.Fields(argStr)
			iptRun(iptVariant(), parts...)
		}
	}
}

// applyIPRuleTun adds ip routing rules to capture traffic into the TUN device.
// Rule Priority (user-facing, higher=wins) maps to Linux ip rule priority
// (kernel, lower=wins) as prio = 100 - clamp(Priority, -99, 99). Default
// Priority 0 → kernel priority 100 (the historical value).
func (e *ruleEngine) applyIPRuleTun(r RoutingRule) {
	kernelPrio := 100 - r.Priority
	if kernelPrio < 1 {
		kernelPrio = 1
	}
	if kernelPrio > 32765 {
		kernelPrio = 32765
	}
	prioStr := fmt.Sprintf("%d", kernelPrio)
	var ipRuleArgs []string
	for _, target := range r.Targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		run("ip", "rule", "add", "to", target, "lookup", ipfwdTable, "priority", prioStr)
		ipRuleArgs = append(ipRuleArgs, "to "+target+" lookup "+ipfwdTable+" priority "+prioStr)
		e.registerTargetExit(r.ID, target, r.ExitVia, r.ExitMode, r.ExitPaths)
	}
	// Store for cleanup (use appliedIPT with "iprule:" prefix to distinguish)
	for _, arg := range ipRuleArgs {
		e.appliedIPT[r.ID] = append(e.appliedIPT[r.ID], "iprule:"+arg)
	}
	// Update TUN target list for packet routing
	if ipfwdEng != nil {
		ipfwdEng.addTargets(r.ID, r.Targets, r.ExitVia)
	}
	log.Printf("[rules] applied IP rule %q (prio %d, kernel %s): %d targets → exit %s (tun)", r.Name, r.Priority, prioStr, len(r.Targets), r.ExitVia)
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
			e.destToExit.Store(ip, ruleExitInfo{ruleID: r.ID, exitVia: r.ExitVia, exitMode: r.ExitMode, exitPaths: r.ExitPaths})
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

func (e *ruleEngine) registerTargetExit(ruleID, target, exitVia, exitMode string, exitPaths []string) {
	e.destToExit.Store(target, ruleExitInfo{ruleID: ruleID, exitVia: exitVia, exitMode: exitMode, exitPaths: exitPaths})
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
	via, mode, _ := e.lookupExitFull(dstIP)
	return via, mode
}

// lookupExitFull is like lookupExit but also returns the configured exit_paths
// so callers can honour quality-mode failover across multiple paths.
func (e *ruleEngine) lookupExitFull(dstIP string) (string, string, []string) {
	var resultVia, resultMode string
	var resultPaths []string
	e.destToExit.Range(func(k, v any) bool {
		target := k.(string)
		info := v.(ruleExitInfo)
		if target == dstIP {
			resultVia, resultMode, resultPaths = info.exitVia, info.exitMode, info.exitPaths
			return false
		}
		if strings.Contains(target, "/") {
			_, cidr, err := net.ParseCIDR(target)
			if err == nil && cidr.Contains(net.ParseIP(dstIP)) {
				resultVia, resultMode, resultPaths = info.exitVia, info.exitMode, info.exitPaths
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
						resultVia, resultMode, resultPaths = info.exitVia, info.exitMode, info.exitPaths
						return false
					}
				}
			}
		}
		return true
	})
	return resultVia, resultMode, resultPaths
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
	exitVia, exitMode, exitPaths := e.lookupExitFull(host)
	if exitVia == "" {
		debugLog("[rules] no exit for %s, direct", origDst)
		remote, err := net.DialTimeout("tcp", origDst, 10*time.Second)
		if err != nil {
			return
		}
		defer remote.Close()
		done := make(chan struct{})
		go func() {
			// Upload: client → remote.
			copyCtx(ctx, remote, conn)
			halfCloseWriteOrClose(remote)
			done <- struct{}{}
		}()
		// Download: remote → client.
		copyCtx(ctx, conn, remote)
		halfCloseWriteOrClose(conn)
		<-done
		cancel()
		return
	}

	log.Printf("[rules] %s → exit %s mode=%s", origDst, exitVia, exitMode)
	// Route through hy2 relay network (exit node dials the actual destination).
	// exitPaths is honoured for quality failover; empty falls back to single primary.
	remote, err := e.app.dialExitWithPaths(ctx, exitVia, exitPaths, exitMode, origDst)
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

package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// TUN-based IP packet forwarding engine.
// Captures raw IP packets via kernel TUN, forwards through QUIC relay,
// exit node injects them into its kernel. Preserves end-to-end TCP/UDP
// sessions (no proxy connection splitting).

const (
	ipfwdTunName = "hy2fwd0"
	ipfwdTunIP   = "169.254.98.1"
	ipfwdTunMTU  = 1400
	ipfwdTable   = "101"
)

var ipfwdActive atomic.Bool

type ipfwdEngine struct {
	app    *App
	tunFd  *tunDevice
	cancel context.CancelFunc

	// Per-peer QUIC IP tunnel streams (entry side, full TUN capable exits)
	streams      map[string]*ipfwdStream
	reconnecting map[string]bool
	streamsMu    sync.Mutex

	// Dynamic target list (updated by hot rule changes)
	targets   []ipfwdTarget
	targetsMu sync.RWMutex

	// Applied routing rules for cleanup
	appliedRoutes []string
}

type ipfwdStream struct {
	conn   net.Conn
	mu     sync.Mutex
	closed bool
}

// tunDevice wraps a kernel TUN fd using raw syscalls to avoid Go poller issues.
type tunDevice struct {
	fd   int
	file *os.File // kept for Close()
	name string
}

func (t *tunDevice) Read(b []byte) (int, error) {
	return syscall.Read(t.fd, b)
}

func (t *tunDevice) Write(b []byte) (int, error) {
	return syscall.Write(t.fd, b)
}

func (t *tunDevice) Close() error {
	return t.file.Close()
}

var ipfwdEng *ipfwdEngine

// StartIPForwarding sets up TUN device and policy routing for IP packet forwarding.
func (a *App) StartIPForwarding(targets []ipfwdTarget) error {
	if ipfwdActive.Load() {
		return nil
	}

	tunFile, err := openKernelTun(ipfwdTunName)
	if err != nil {
		return fmt.Errorf("tun_ipfwd: create TUN: %w", err)
	}

	// Get raw fd for syscall Read/Write (avoids Go poller "not pollable" issue)
	rawFd, err := syscall.Dup(int(tunFile.Fd()))
	if err != nil {
		tunFile.Close()
		return fmt.Errorf("tun_ipfwd: dup fd: %w", err)
	}
	// Set blocking mode for raw fd
	syscall.SetNonblock(rawFd, false)

	// Configure TUN interface — no IP address to avoid kernel using it as source
	run("ip", "link", "set", ipfwdTunName, "up", "mtu", fmt.Sprintf("%d", ipfwdTunMTU))

	// Disable reverse-path filter: gvisor compat mode writes SYN-ACK packets
	// to the TUN with spoofed source IPs (e.g. 8.8.8.8) that aren't routable
	// via this interface in the main table. rp_filter=1 (strict) drops them.
	run("sysctl", "-w", "net.ipv4.conf."+ipfwdTunName+".rp_filter=0")
	run("sysctl", "-w", "net.ipv4.conf.all.rp_filter=0")

	// Default route in table 101 via TUN.
	// Specify src to avoid kernel picking a local IP that may conflict on exit node.
	mainIP := getMainIP()
	if mainIP != "" {
		run("ip", "route", "replace", "default", "dev", ipfwdTunName, "src", mainIP, "table", ipfwdTable)
	} else {
		run("ip", "route", "replace", "default", "dev", ipfwdTunName, "table", ipfwdTable)
	}

	ctx, cancel := context.WithCancel(a.appCtx)
	eng := &ipfwdEngine{
		app:          a,
		tunFd:        &tunDevice{fd: rawFd, file: tunFile, name: ipfwdTunName},
		cancel:       cancel,
		streams:      make(map[string]*ipfwdStream),
		reconnecting: make(map[string]bool),
	}

	// Add routing rules for each target
	for _, t := range targets {
		for _, cidr := range t.cidrs {
			rule := fmt.Sprintf("to %s lookup %s priority 100", cidr, ipfwdTable)
			run("ip", "rule", "add", "to", cidr, "lookup", ipfwdTable, "priority", "100")
			eng.appliedRoutes = append(eng.appliedRoutes, rule)
		}
	}

	// Exclude relay peer IPs from TUN routing (prevent capturing QUIC traffic)
	cfg := a.store.Get()
	for _, cl := range cfg.Clients {
		addr := extractPrimaryAddr(cl.Addr)
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		ips, _ := net.LookupHost(host)
		for _, ip := range ips {
			run("ip", "rule", "add", "to", ip+"/32", "lookup", "main", "priority", "50")
			eng.appliedRoutes = append(eng.appliedRoutes, fmt.Sprintf("to %s/32 lookup main priority 50", ip))
		}
	}

	eng.targets = targets
	ipfwdEng = eng
	ipfwdActive.Store(true)

	// Start TUN read loop (entry side: reads packets, sends to exit peer)
	go eng.tunReadLoop(ctx)

	log.Printf("[tun-ipfwd] started, %d targets", len(targets))
	return nil
}

// StopIPForwarding tears down TUN device and routing.
func (a *App) StopIPForwarding() {
	if !ipfwdActive.Load() {
		return
	}
	eng := ipfwdEng
	if eng == nil {
		return
	}

	eng.cancel()

	// Close all streams
	eng.streamsMu.Lock()
	for _, s := range eng.streams {
		s.conn.Close()
	}
	eng.streams = nil
	eng.streamsMu.Unlock()

	// Remove routing rules
	for _, rule := range eng.appliedRoutes {
		parts := strings.Fields("ip rule del " + rule)
		run(parts[0], parts[1:]...)
	}

	// Remove route table and TUN
	run("ip", "route", "flush", "table", ipfwdTable)
	run("ip", "link", "del", ipfwdTunName)

	if eng.tunFd != nil {
		syscall.Close(eng.tunFd.fd)
		eng.tunFd.file.Close()
	}

	ipfwdActive.Store(false)
	ipfwdEng = nil
	log.Printf("[tun-ipfwd] stopped")
}

type ipfwdTarget struct {
	cidrs   []string
	exitVia string
	ruleID  string
}

// tunReadLoop reads raw IP packets from TUN, determines exit peer, sends through QUIC.
func (eng *ipfwdEngine) tunReadLoop(ctx context.Context) {
	buf := make([]byte, ipfwdTunMTU+100)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := eng.tunFd.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[tun-ipfwd] TUN read error: %v", err)
			return
		}
		if n < 20 {
			continue // too small for IP header
		}

		pkt := buf[:n]

		// Parse destination IP from IP header
		version := pkt[0] >> 4
		if version != 4 {
			continue // IPv4 only for now
		}
		dstIP := net.IP(pkt[16:20])

		// Find exit peer for this destination
		exitPeer := ""
		eng.targetsMu.RLock()
		for _, t := range eng.targets {
			for _, cidr := range t.cidrs {
				_, ipNet, err := net.ParseCIDR(cidr)
				if err != nil {
					// Try as single IP
					if tip := net.ParseIP(cidr); tip != nil && tip.Equal(dstIP) {
						exitPeer = t.exitVia
						break
					}
					continue
				}
				if ipNet.Contains(dstIP) {
					exitPeer = t.exitVia
					break
				}
			}
			if exitPeer != "" {
				break
			}
		}
		eng.targetsMu.RUnlock()
		if exitPeer == "" {
			continue
		}

		// Decide compat vs full-TUN per packet by querying current peer
		// capability — the answer can change as peers connect/disconnect, and
		// caching at rule-apply time would freeze a wrong decision when peers
		// hadn't registered yet.
		if !eng.app.node.IsPeerTunCapable(exitPeer) {
			// Compat mode: lazily start gvisor stack on first compat packet,
			// then inject for L7 (TCP/UDP) extraction and relay proxying.
			if !tunCaptureActive.Load() {
				eng.ensureCompatStack()
			}
			debugLog("[tun-ipfwd] compat→ inject %d bytes dst=%s via=%s", len(pkt), dstIP, exitPeer)
			eng.injectToNetstack(pkt)
			continue
		}

		// Full TUN mode: send raw IP frame to exit via QUIC stream
		stream := eng.getStream(ctx, exitPeer)
		if stream == nil {
			continue
		}

		frame := make([]byte, 2+n)
		binary.BigEndian.PutUint16(frame[:2], uint16(n))
		copy(frame[2:], pkt)
		stream.mu.Lock()
		_, err = stream.conn.Write(frame)
		stream.mu.Unlock()
		if err != nil {
			debugLog("[tun-ipfwd] write to %s: %v", exitPeer, err)
			eng.removeStream(exitPeer)
		}
	}
}

// getStream returns an existing healthy stream or nil (never blocks).
// If no stream exists, kicks off async reconnect in the background.
func (eng *ipfwdEngine) getStream(ctx context.Context, peerName string) *ipfwdStream {
	eng.streamsMu.Lock()
	defer eng.streamsMu.Unlock()

	if s, ok := eng.streams[peerName]; ok && !s.closed {
		return s
	}

	// No healthy stream — trigger async reconnect (if not already running)
	if _, reconnecting := eng.reconnecting[peerName]; !reconnecting {
		eng.reconnecting[peerName] = true
		go eng.asyncConnect(ctx, peerName)
	}
	return nil
}

// asyncConnect dials the exit peer in the background and installs the stream.
// It also monitors the first-hop peer's connection health: when the relay peer
// disconnects (PeerCtx cancelled), the TUN stream is torn down immediately
// instead of waiting for QUIC idle timeout. The next incoming packet will
// trigger a fresh asyncConnect via getStream.
func (eng *ipfwdEngine) asyncConnect(ctx context.Context, peerName string) {
	defer func() {
		eng.streamsMu.Lock()
		delete(eng.reconnecting, peerName)
		eng.streamsMu.Unlock()
	}()

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()

	conn, err := eng.app.node.DialIPTun(dialCtx, peerName)
	if err != nil {
		debugLog("[tun-ipfwd] dial %s failed: %v", peerName, err)
		return
	}

	s := &ipfwdStream{conn: conn}
	eng.streamsMu.Lock()
	eng.streams[peerName] = s
	eng.streamsMu.Unlock()

	log.Printf("[tun-ipfwd] IP tunnel to %s established", peerName)

	// Watch first-hop peer liveness. When relay detects peer disconnect
	// (3 failed pings → cancel peerCtx), tear down this stream immediately.
	firstHop := peerName
	if parts := strings.Split(peerName, "/"); len(parts) > 0 {
		if parts[0] == eng.app.node.Name() && len(parts) > 1 {
			firstHop = parts[1]
		} else {
			firstHop = parts[0]
		}
	}
	peerCtx := eng.app.node.PeerCtx(firstHop)
	go func() {
		select {
		case <-ctx.Done():
		case <-peerCtx.Done():
			log.Printf("[tun-ipfwd] first-hop %s disconnected, tearing down tunnel to %s", firstHop, peerName)
			s.conn.Close()
		}
	}()

	// Reverse reader: exit peer → TUN (blocks until stream dies)
	eng.readFromStream(ctx, s)
	eng.removeStream(peerName)
}

func (eng *ipfwdEngine) removeStream(peerName string) {
	eng.streamsMu.Lock()
	defer eng.streamsMu.Unlock()
	if s, ok := eng.streams[peerName]; ok {
		s.closed = true
		s.conn.Close()
		delete(eng.streams, peerName)
	}
}

// readFromStream reads framed IP packets from QUIC stream and writes to TUN.
func (eng *ipfwdEngine) readFromStream(ctx context.Context, s *ipfwdStream) {
	hdr := make([]byte, 2)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if _, err := io.ReadFull(s.conn, hdr); err != nil {
			return
		}
		pktLen := binary.BigEndian.Uint16(hdr)
		if pktLen == 0 || pktLen > 65535 {
			return
		}
		pkt := make([]byte, pktLen)
		if _, err := io.ReadFull(s.conn, pkt); err != nil {
			return
		}
		eng.tunFd.Write(pkt)
	}
}

// handleExitIPTun handles incoming IP tunnel streams on the exit node.
// Reads IP packets from QUIC stream, writes to a local TUN device.
// Return packets from TUN are sent back through the stream.
func (eng *ipfwdEngine) handleExitIPTun(ctx context.Context, peerName string, stream net.Conn) {
	log.Printf("[tun-ipfwd] exit tunnel from %s", peerName)

	// Create exit-side TUN device
	exitTunName := fmt.Sprintf("hy2exit%d", exitTunCounter.Add(1)%100)
	exitTunFile, err := openKernelTun(exitTunName)
	if err != nil {
		log.Printf("[tun-ipfwd] exit TUN create error: %v", err)
		stream.Close()
		return
	}
	exitRawFd, err := syscall.Dup(int(exitTunFile.Fd()))
	if err != nil {
		exitTunFile.Close()
		log.Printf("[tun-ipfwd] exit TUN dup fd error: %v", err)
		stream.Close()
		return
	}
	syscall.SetNonblock(exitRawFd, false)
	exitTun := &tunDevice{fd: exitRawFd, file: exitTunFile, name: exitTunName}
	defer exitTun.Close()
	defer syscall.Close(exitRawFd)
	defer func() {
		run("ip", "link", "del", exitTunName)
	}()

	// Configure exit TUN — no IP address, raw packet forwarding
	run("ip", "link", "set", exitTunName, "up", "mtu", fmt.Sprintf("%d", ipfwdTunMTU))

	// Disable reverse path filter (source IPs from tunnel don't have local routes)
	run("sysctl", "-w", "net.ipv4.conf."+exitTunName+".rp_filter=0")
	run("sysctl", "-w", "net.ipv4.conf.all.rp_filter=0")
	run("sysctl", "-w", "net.ipv4.ip_forward=1")

	// Enable IP forwarding
	run("sysctl", "-w", "net.ipv4.ip_forward=1")
	run("sysctl", "-w", "net.ipv4.conf."+exitTunName+".forwarding=1")
	run("sysctl", "-w", "net.ipv4.conf.all.forwarding=1")

	exitMark := fmt.Sprintf("0x%x", 0x3456+exitTunCounter.Load())
	exitTable := fmt.Sprintf("%d", 102+int(exitTunCounter.Load()))

	// Connmark-based return routing:
	// 1. Packets from exit TUN get connmarked
	// 2. Reply packets carry the connmark → set fwmark → route to exit TUN
	// Use BOTH iptables and iptables-legacy (Docker may use either)
	for _, ipt := range []string{"iptables-legacy", "iptables"} {
		iptRun(ipt, "-t", "mangle", "-A", "PREROUTING",
			"-i", exitTunName, "-j", "CONNMARK", "--set-mark", exitMark)
		iptRun(ipt, "-t", "mangle", "-A", "PREROUTING",
			"!", "-i", exitTunName, "-m", "connmark", "--mark", exitMark,
			"-j", "MARK", "--set-mark", exitMark)
	}
	run("ip", "rule", "add", "fwmark", exitMark, "lookup", exitTable, "priority", "90")
	run("ip", "route", "add", "default", "dev", exitTunName, "table", exitTable)

	// MASQUERADE ALL outbound from TUN so replies come back to container.
	// Use physdev/mark approach: mark packets from TUN in mangle FORWARD,
	// then MASQUERADE marked packets in POSTROUTING.
	fwdMark := fmt.Sprintf("0x%x", 0x4456+exitTunCounter.Load())
	for _, ipt := range []string{"iptables-legacy", "iptables"} {
		iptRun(ipt, "-t", "mangle", "-A", "FORWARD",
			"-i", exitTunName, "-j", "MARK", "--set-mark", fwdMark)
		iptRun(ipt, "-t", "nat", "-A", "POSTROUTING",
			"-m", "mark", "--mark", fwdMark, "-j", "MASQUERADE")
	}

	exitCtx, exitCancel := context.WithCancel(ctx)
	defer exitCancel()
	defer func() {
		for _, ipt := range []string{"iptables-legacy", "iptables"} {
			iptRun(ipt, "-t", "mangle", "-D", "PREROUTING",
				"-i", exitTunName, "-j", "CONNMARK", "--set-mark", exitMark)
			iptRun(ipt, "-t", "mangle", "-D", "PREROUTING",
				"!", "-i", exitTunName, "-m", "connmark", "--mark", exitMark,
				"-j", "MARK", "--set-mark", exitMark)
			iptRun(ipt, "-t", "mangle", "-D", "FORWARD",
				"-i", exitTunName, "-j", "MARK", "--set-mark", fwdMark)
			iptRun(ipt, "-t", "nat", "-D", "POSTROUTING",
				"-m", "mark", "--mark", fwdMark, "-j", "MASQUERADE")
		}
		run("ip", "rule", "del", "fwmark", exitMark, "lookup", exitTable)
		run("ip", "route", "flush", "table", exitTable)
	}()

	// QUIC stream → exit TUN (forward direction)
	go func() {
		defer exitCancel()
		hdr := make([]byte, 2)
		for {
			if _, err := io.ReadFull(stream, hdr); err != nil {
				return
			}
			pktLen := binary.BigEndian.Uint16(hdr)
			if pktLen == 0 || pktLen > 65535 {
				return
			}
			pkt := make([]byte, pktLen)
			if _, err := io.ReadFull(stream, pkt); err != nil {
				return
			}
			exitTun.Write(pkt)
		}
	}()

	// Exit TUN → QUIC stream (return direction)
	buf := make([]byte, ipfwdTunMTU+100)
	for {
		select {
		case <-exitCtx.Done():
			return
		default:
		}

		n, err := exitTun.Read(buf)
		if err != nil {
			log.Printf("[tun-ipfwd] exit %s: read error: %v", exitTunName, err)
			return
		}
		frame := make([]byte, 2+n)
		binary.BigEndian.PutUint16(frame[:2], uint16(n))
		copy(frame[2:], buf[:n])
		if _, err := stream.Write(frame); err != nil {
			return
		}
	}
}

var exitTunCounter atomic.Int32

// registerExitIPTunHandler registers the IP tunnel handler on this node.
// Any node can be an exit for TUN mode, so this runs at startup.
func (a *App) registerExitIPTunHandler(ctx context.Context) {
	a.node.SetIPTunHandler(func(peerName string, stream net.Conn) {
		eng := &ipfwdEngine{app: a}
		eng.handleExitIPTun(ctx, peerName, stream)
	})
}

// addTargets adds or replaces CIDR→exit mappings in the TUN forwarding table.
func (eng *ipfwdEngine) addTargets(ruleID string, cidrs []string, exitVia string) {
	eng.targetsMu.Lock()
	defer eng.targetsMu.Unlock()
	eng.targets = filterTargets(eng.targets, ruleID)
	eng.targets = append(eng.targets, ipfwdTarget{cidrs: cidrs, exitVia: exitVia, ruleID: ruleID})
	// Close stale streams (forces reconnect to new exit)
	eng.streamsMu.Lock()
	for k, s := range eng.streams {
		if s.closed {
			delete(eng.streams, k)
		}
	}
	eng.streamsMu.Unlock()
}

// removeTargetsForRule removes targets by rule ID.
func (eng *ipfwdEngine) removeTargetsForRule(ruleID string) {
	eng.targetsMu.Lock()
	defer eng.targetsMu.Unlock()
	eng.targets = filterTargets(eng.targets, ruleID)
}

// injectToNetstack feeds a raw IP packet into the gvisor capture stack
// for L7 (TCP/UDP) extraction and relay proxy forwarding.
func (eng *ipfwdEngine) injectToNetstack(pkt []byte) {
	tunCaptureMu.Lock()
	inst := tunCaptureInst
	tunCaptureMu.Unlock()
	if inst == nil || inst.ep == nil {
		debugLog("[tun-ipfwd] compat inject: no capture stack yet")
		return
	}
	pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(append([]byte(nil), pkt...)),
	})
	switch pkt[0] >> 4 {
	case 4:
		inst.ep.InjectInbound(header.IPv4ProtocolNumber, pkb)
	case 6:
		inst.ep.InjectInbound(header.IPv6ProtocolNumber, pkb)
	}
	pkb.DecRef()
}

// ensureCompatStack starts the gvisor netstack + capture forwarders if not already running.
// It also bridges gvisor output back to the kernel TUN so TCP handshakes complete.
func (eng *ipfwdEngine) ensureCompatStack() {
	if tunCaptureActive.Load() {
		return
	}
	ep, gvStack, err := createCaptureStack(ipfwdTunMTU)
	if err != nil {
		log.Printf("[tun-ipfwd] compat stack creation failed: %v", err)
		return
	}

	// Install forwarders BEFORE activating — prevents race where an injected
	// SYN arrives before handlers are set (gvisor would RST it).
	installCaptureForwarders(gvStack, eng.app)

	tunCaptureMu.Lock()
	tunCaptureInst = &tunCaptureState{tunFile: eng.tunFd.file, ep: ep, gvStack: gvStack}
	tunCaptureActive.Store(true)
	tunCaptureMu.Unlock()

	// Bridge gvisor output → kernel TUN (SYN-ACK, data replies go back to host)
	go func() {
		for {
			pkt := ep.Read()
			if pkt == nil {
				time.Sleep(time.Millisecond)
				continue
			}
			view := pkt.ToView()
			pkt.DecRef()
			data := view.AsSlice()
			if len(data) > 0 {
				if len(data) >= 20 {
					proto := data[9]
					srcIP := net.IP(data[12:16])
					dstIP := net.IP(data[16:20])
					debugLog("[tun-ipfwd] compat← gvisor %d bytes proto=%d %s→%s",
						len(data), proto, srcIP, dstIP)
				}
				eng.tunFd.Write(data)
			}
		}
	}()

	log.Printf("[tun-ipfwd] compat L7 proxy stack active")
}

// IsExitCompat returns true if the given exit peer cannot perform full TUN
// forwarding (i.e. is "limited" — no NET_ADMIN/no /dev/net/tun on its side).
// This indicates traffic to that exit must use L7 proxy semantics rather than
// raw IP forwarding, regardless of whether THIS node is running TUN locally.
func (a *App) IsExitCompat(exitVia string) bool {
	if exitVia == "" {
		return false
	}
	return !a.node.IsPeerTunCapable(exitVia)
}

func filterTargets(targets []ipfwdTarget, excludeRuleID string) []ipfwdTarget {
	var kept []ipfwdTarget
	for _, t := range targets {
		if t.ruleID != excludeRuleID {
			kept = append(kept, t)
		}
	}
	return kept
}

// getMainIP returns the primary outgoing IP (for src hint in routes).
func getMainIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

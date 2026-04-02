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

	// Per-peer QUIC IP tunnel streams (entry side)
	streams   map[string]*ipfwdStream
	streamsMu sync.Mutex

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
		app:     a,
		tunFd:   &tunDevice{fd: rawFd, file: tunFile, name: ipfwdTunName},
		cancel:  cancel,
		streams: make(map[string]*ipfwdStream),
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

		// Get or create QUIC IP tunnel stream to exit peer
		stream, err := eng.getStream(ctx, exitPeer)
		if err != nil {
			debugLog("[tun-ipfwd] stream to %s: %v", exitPeer, err)
			continue
		}

		// Write framed packet: [2-byte length][packet]
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

// getStream returns or creates a QUIC IP tunnel stream to the exit peer.
func (eng *ipfwdEngine) getStream(ctx context.Context, peerName string) (*ipfwdStream, error) {
	eng.streamsMu.Lock()
	defer eng.streamsMu.Unlock()

	if s, ok := eng.streams[peerName]; ok && !s.closed {
		return s, nil
	}

	// Dial IP tunnel to peer (with timeout to avoid blocking read loop)
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()
	conn, err := eng.app.node.DialIPTun(dialCtx, peerName)
	if err != nil {
		return nil, err
	}

	s := &ipfwdStream{conn: conn}
	eng.streams[peerName] = s

	// Start reverse reader: exit peer → TUN
	go func() {
		defer func() {
			eng.removeStream(peerName)
		}()
		eng.readFromStream(ctx, s)
	}()

	log.Printf("[tun-ipfwd] IP tunnel to %s established", peerName)
	return s, nil
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

// addTargets adds new CIDR→exit mappings to the TUN forwarding table.
func (eng *ipfwdEngine) addTargets(cidrs []string, exitVia string) {
	eng.targetsMu.Lock()
	defer eng.targetsMu.Unlock()
	eng.targets = append(eng.targets, ipfwdTarget{cidrs: cidrs, exitVia: exitVia})
}

// removeTargetsForRule is a no-op placeholder; targets are rebuilt on rule changes.
func (eng *ipfwdEngine) removeTargetsForRule(id string) {
	// Targets are matched by CIDR, not rule ID. The ip rule removal handles routing.
	// The target list is additive and doesn't need per-rule removal since
	// packets that no longer match any ip rule won't reach the TUN.
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

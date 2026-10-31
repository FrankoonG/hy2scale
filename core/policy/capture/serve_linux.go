//go:build linux

package capture

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/policy"
	"github.com/FrankoonG/hy2scale/core/sniff"
)

// listener ports a spec needs: port → (layer kind, dns)
type portUse struct {
	kind string
	dns  bool
}

func neededPorts(s Spec) map[int]portUse {
	out := map[int]portUse{}
	if s.Local != nil {
		out[PortLocal] = portUse{policy.KindLocal, false}
		if s.Local.DNS {
			out[PortDNSLocal] = portUse{policy.KindLocal, true}
		}
	}
	if s.Forward != nil {
		out[PortForward] = portUse{policy.KindForward, false}
		if s.Forward.DNS {
			out[PortDNSForward] = portUse{policy.KindForward, true}
		}
	}
	return out
}

// errV6Listen: an IPv6 listener could not open (IPv6 off in this netns).
var errV6Listen = errors.New("IPv6 listener unavailable")

// openListeners opens the listeners the spec needs and closes the others.
// IPv6 listeners that cannot open turn the family off.
func (m *Manager) openListeners(s Spec) error {
	want := map[string]bool{}
	for port, use := range neededPorts(s) {
		for _, v6 := range []bool{false, true} {
			if v6 && !s.V6 {
				continue
			}
			ip := ListenIP4
			if v6 {
				ip = "[" + ListenIP6 + "]"
			}
			addr := fmt.Sprintf("%s:%d", ip, port)
			for _, proto := range []string{"tcp", "udp"} {
				key := proto + addr
				want[key] = true
				if m.lns[key] != nil {
					continue
				}
				var err error
				if proto == "tcp" {
					var ln net.Listener
					if ln, err = listenTCPTransparent(addr, v6); err == nil {
						m.lns[key] = ln
						go m.serveTCP(ln, use)
					}
				} else {
					var ln *net.UDPConn
					if ln, err = listenUDPTransparent(addr, v6); err == nil {
						m.lns[key] = ln
						go m.serveUDP(ln, use)
					}
				}
				if err != nil {
					if v6 {
						return fmt.Errorf("%w: listen %s %s: %v", errV6Listen, proto, addr, err)
					}
					return fmt.Errorf("listen %s %s: %w", proto, addr, err)
				}
			}
		}
	}
	m.closeListeners(want)
	return nil
}

// closeListeners closes every listener not in keep (nil = all).
func (m *Manager) closeListeners(keep map[string]bool) {
	for k, ln := range m.lns {
		if !keep[k] {
			ln.Close()
			delete(m.lns, k)
		}
	}
}

func setTransparent(fd int, v6 bool) error {
	if v6 {
		return syscall.SetsockoptInt(fd, syscall.SOL_IPV6, 75 /* IPV6_TRANSPARENT */, 1)
	}
	return syscall.SetsockoptInt(fd, syscall.SOL_IP, syscall.IP_TRANSPARENT, 1)
}

func markFD(fd int) {
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_MARK, MarkSocket)
}

func listenTCPTransparent(addr string, v6 bool) (net.Listener, error) {
	network := "tcp4"
	if v6 {
		network = "tcp6"
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var err error
		c.Control(func(fd uintptr) {
			err = setTransparent(int(fd), v6)
			markFD(int(fd))
		})
		return err
	}}
	return lc.Listen(context.Background(), network, addr)
}

func listenUDPTransparent(addr string, v6 bool) (*net.UDPConn, error) {
	network := "udp4"
	if v6 {
		network = "udp6"
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var err error
		c.Control(func(fd uintptr) {
			if err = setTransparent(int(fd), v6); err != nil {
				return
			}
			markFD(int(fd))
			if v6 {
				err = syscall.SetsockoptInt(int(fd), syscall.SOL_IPV6, 74 /* IPV6_RECVORIGDSTADDR */, 1)
			} else {
				err = syscall.SetsockoptInt(int(fd), syscall.SOL_IP, 20 /* IP_RECVORIGDSTADDR */, 1)
			}
		})
		return err
	}}
	pc, err := lc.ListenPacket(context.Background(), network, addr)
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

// udpOrigDst: IP_ORIGDSTADDR / IPV6_ORIGDSTADDR of a datagram.
func udpOrigDst(oob []byte) *net.UDPAddr {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	for _, m := range msgs {
		switch {
		case m.Header.Level == syscall.SOL_IP && m.Header.Type == 20 && len(m.Data) >= 8:
			return &net.UDPAddr{IP: net.IPv4(m.Data[4], m.Data[5], m.Data[6], m.Data[7]),
				Port: int(binary.BigEndian.Uint16(m.Data[2:4]))}
		case m.Header.Level == syscall.SOL_IPV6 && m.Header.Type == 74 && len(m.Data) >= 24:
			return &net.UDPAddr{IP: append(net.IP(nil), m.Data[8:24]...),
				Port: int(binary.BigEndian.Uint16(m.Data[2:4]))}
		}
	}
	return nil
}

// dialUDPAs opens a UDP socket bound to from (any address, transparent)
// and connected to to: replies to a taken-over client come from the
// address it sent to, and its later datagrams of the flow arrive here.
func dialUDPAs(from, to *net.UDPAddr) (*net.UDPConn, error) {
	v6 := from.IP.To4() == nil
	network := "udp4"
	if v6 {
		network = "udp6"
	}
	d := net.Dialer{LocalAddr: from, Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		c.Control(func(fd uintptr) {
			if serr = setTransparent(int(fd), v6); serr != nil {
				return
			}
			syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			markFD(int(fd))
		})
		return serr
	}}
	c, err := d.Dial(network, to.String())
	if err != nil {
		return nil, err
	}
	return c.(*net.UDPConn), nil
}

func addrPort(a net.Addr) netip.AddrPort {
	switch v := a.(type) {
	case *net.TCPAddr:
		return v.AddrPort()
	case *net.UDPAddr:
		return v.AddrPort()
	}
	ap, _ := netip.ParseAddrPort(a.String())
	return ap
}

// newConn builds the policy connection of a taken-over flow.
func (m *Manager) newConn(kind string, src, dst netip.AddrPort, proto string, h *handlers) *policy.Conn {
	c := &policy.Conn{SrcKind: kind, SrcIP: src.Addr().Unmap(), SrcPort: src.Port(), Proto: proto,
		DstIP: dst.Addr().Unmap(), DstPort: dst.Port()}
	if h != nil && h.dns != nil {
		c.DomainOf = h.dns.DomainOf
	}
	if kind == policy.KindForward {
		c.InIface, c.SrcMAC = m.neigh.lookup(c.SrcIP, m.forwardIfaces())
	}
	return c
}

func (m *Manager) forwardIfaces() []string {
	if h := m.box.Load(); h != nil {
		return h.fwd
	}
	return nil
}

// ---------------------------------------------------------------- TCP

func (m *Manager) serveTCP(ln net.Listener, use portUse) {
	defer m.Recover()
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go m.handleTCP(c, use)
	}
}

func (m *Manager) handleTCP(conn net.Conn, use portUse) {
	defer m.Recover()
	h := m.box.Load()
	src, dst := addrPort(conn.RemoteAddr()), addrPort(conn.LocalAddr())
	pc := m.newConn(use.kind, src, dst, "tcp", h)
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	if use.dns {
		defer conn.Close()
		if h != nil && h.dns != nil {
			h.dns.ServeTCP(ctx, pc, conn)
		}
		return
	}
	if h == nil || h.rt == nil {
		resetTCP(conn)
		return
	}
	af := audit.Begin(use.kind, "tcp")
	af.SetSrc(src.String(), "")
	af.SetDst(dst.String())
	var peeked []byte
	pc.Sniff = func(context.Context) string {
		peeked = peekHello(conn)
		name := sniff.Host(peeked)
		if !sniff.Usable(name) {
			return ""
		}
		name = strings.TrimSuffix(strings.ToLower(name), ".")
		af.SetDstHost(name)
		return name
	}
	remote, _, err := h.rt.DialTCP(audit.NewContext(ctx, af), pc, policy.Direct)
	if err != nil {
		af.End(err)
		if d, ok := policy.AsDeny(err); ok && !d.Reject {
			holdTCP(ctx, conn)
		} else {
			resetTCP(conn)
		}
		return
	}
	defer conn.Close()
	remote = audit.Track(af, remote, false)
	defer remote.Close()
	defer af.End(nil)
	if len(peeked) > 0 {
		if _, err := remote.Write(peeked); err != nil {
			return
		}
	}
	relay(conn, remote)
}

// sniffWindow: how long a flow waits for the client's first bytes.
const sniffWindow = 300 * time.Millisecond

// peekHello reads the client's first bytes until they hold a TLS record or
// an HTTP header, the window ends, or the read fails.
func peekHello(conn net.Conn) []byte {
	conn.SetReadDeadline(time.Now().Add(sniffWindow))
	defer conn.SetReadDeadline(time.Time{})
	buf := make([]byte, 0, 2048)
	chunk := make([]byte, 4096)
	for !sniff.Complete(buf) {
		want := sniff.MaxPeek - len(buf)
		if want > len(chunk) {
			want = len(chunk)
		}
		n, err := conn.Read(chunk[:want])
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}

// resetTCP closes with an RST (reject).
func resetTCP(c net.Conn) {
	if t, ok := c.(*net.TCPConn); ok {
		t.SetLinger(0)
	}
	c.Close()
}

// holdTCP: drop — the client hears nothing until it gives up (bounded).
func holdTCP(ctx context.Context, c net.Conn) {
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	go func() { <-ctx.Done(); c.Close() }()
	io.Copy(io.Discard, c)
	resetTCP(c)
}

// relay copies both ways; one side ending half-closes the other and bounds
// how long it may linger.
func relay(a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		io.Copy(b, a)
		halfCloseAndBound(b)
		close(done)
	}()
	io.Copy(a, b)
	halfCloseAndBound(a)
	<-done
}

func halfCloseAndBound(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else if nh, ok := c.(interface{ NoHalfClose() bool }); !ok || !nh.NoHalfClose() {
		c.Close()
	}
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
}

// ---------------------------------------------------------------- UDP

const (
	udpIdle     = 120 * time.Second
	udpDNSIdle  = 20 * time.Second
	udpMaxFlows = 4096
	udpQueue    = 64
)

// udpFlow is one taken-over (client, destination) pair.
type udpFlow struct {
	key    string
	client *net.UDPAddr
	dst    *net.UDPAddr
	idle   time.Duration
	last   atomic.Int64
	local  *net.UDPConn // transparent reply socket (bound to dst, connected to client)
	af     *audit.Flow

	mu     sync.Mutex
	remote net.Conn
	queue  [][]byte
	closed bool
	dead   bool // denied or failed: datagrams are dropped until idle
	// dns: answer locally instead of dialling
	dns func(q []byte)
}

func (f *udpFlow) touch() { f.last.Store(time.Now().UnixNano()) }

func (f *udpFlow) idleFor() time.Duration { return time.Since(time.Unix(0, f.last.Load())) }

func (f *udpFlow) send(b []byte) {
	f.touch()
	if f.dns != nil {
		f.dns(append([]byte(nil), b...))
		return
	}
	f.af.Add(int64(len(b)), 0)
	f.mu.Lock()
	r := f.remote
	if r == nil && !f.closed && !f.dead && len(f.queue) < udpQueue {
		f.queue = append(f.queue, append([]byte(nil), b...))
	}
	f.mu.Unlock()
	if r != nil {
		r.Write(b)
	}
}

func (f *udpFlow) close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	r := f.remote
	f.queue = nil
	f.mu.Unlock()
	if r != nil {
		r.Close()
	}
	f.local.Close()
}

func (m *Manager) serveUDP(ln *net.UDPConn, use portUse) {
	defer m.Recover()
	var mu sync.Mutex
	flows := map[string]*udpFlow{}
	full := false
	remove := func(f *udpFlow) {
		mu.Lock()
		if flows[f.key] == f {
			delete(flows, f.key)
		}
		mu.Unlock()
		f.close()
	}
	defer func() {
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
	// idle reaper
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			mu.Lock()
			var idle []*udpFlow
			for _, f := range flows {
				if f.idleFor() > f.idle {
					idle = append(idle, f)
				}
			}
			mu.Unlock()
			for _, f := range idle {
				remove(f)
			}
		}
	}()
	buf := make([]byte, 65535)
	oob := make([]byte, 256)
	for {
		n, oobn, _, src, err := ln.ReadMsgUDP(buf, oob)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		dst := udpOrigDst(oob[:oobn])
		if dst == nil {
			continue
		}
		key := src.String() + ">" + dst.String()
		mu.Lock()
		f := flows[key]
		if f == nil {
			if len(flows) >= udpMaxFlows {
				if !full {
					log.Printf("[capture] UDP flow limit %d reached, dropping new flows", udpMaxFlows)
				}
				full = true
				mu.Unlock()
				continue
			}
			full = false
			local, err := dialUDPAs(dst, src)
			if err != nil {
				mu.Unlock()
				debugf("[capture] UDP %s → %s: reply socket: %v", src, dst, err)
				continue
			}
			f = &udpFlow{key: key, client: src, dst: dst, idle: udpIdle, local: local}
			if dst.Port == 53 || use.dns {
				f.idle = udpDNSIdle
			}
			f.touch()
			flows[key] = f
			mu.Unlock()
			m.startUDPFlow(f, use, func() { remove(f) })
		} else {
			mu.Unlock()
		}
		f.send(buf[:n])
	}
}

// startUDPFlow reads the flow's later datagrams from its reply socket and
// sets up its far side: the DNS handler, or a Runtime dial in the
// background.
func (m *Manager) startUDPFlow(f *udpFlow, use portUse, done func()) {
	h := m.box.Load()
	src, dst := f.client.AddrPort(), f.dst.AddrPort()
	pc := m.newConn(use.kind, src, dst, "udp", h)
	if use.dns {
		f.dns = func(q []byte) {
			go func() {
				defer m.Recover()
				if h == nil || h.dns == nil {
					return
				}
				ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
				defer cancel()
				resp, err := h.dns.Answer(ctx, pc, q)
				if err == nil && len(resp) > 0 {
					f.local.Write(resp)
				}
			}()
		}
	}
	go func() {
		defer m.Recover()
		b := make([]byte, 65535)
		for {
			n, err := f.local.Read(b)
			if err != nil {
				return
			}
			f.send(b[:n])
		}
	}()
	if use.dns {
		return
	}
	f.af = audit.Begin(use.kind, "udp")
	f.af.SetSrc(src.String(), "")
	f.af.SetDst(dst.String())
	go func() {
		defer m.Recover()
		var ferr error
		defer func() { f.af.End(ferr) }()
		if h == nil || h.rt == nil {
			ferr = errors.New("no runtime")
			done()
			return
		}
		ctx, cancel := context.WithCancel(m.ctx)
		defer cancel()
		remote, _, err := h.rt.DialUDP(audit.NewContext(ctx, f.af), pc, policy.Direct)
		if err != nil {
			// denied or failed: keep the flow (dropping) until it idles out
			ferr = err
			f.mu.Lock()
			f.dead, f.queue = true, nil
			f.mu.Unlock()
			return
		}
		defer done()
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
		for _, q := range queued {
			remote.Write(q)
		}
		b := make([]byte, 65535)
		for {
			remote.SetReadDeadline(time.Now().Add(f.idle))
			n, err := remote.Read(b)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() && f.idleFor() < f.idle {
					continue
				}
				return
			}
			f.touch()
			f.af.Add(0, int64(n))
			f.local.Write(b[:n])
		}
	}()
}

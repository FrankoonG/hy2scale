package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/FrankoonG/hy2scale/core/policy"
	"log"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/xen0bit/veepin/dataplane"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// A userspace VPN server (IKEv2, L2TP/IPsec, OpenVPN) hands its decrypted IP
// packets to a vpnNet: a gvisor stack whose TCP/UDP forwarders terminate every
// flow and dial it onward through the client's exit, exactly like WireGuard.
// Nothing touches the kernel's interfaces, routes or iptables.

// vpnRoute is where one client's traffic goes.
type vpnRoute struct {
	user      string // device-row name; "" when the client has no user
	userID    string // the user's internal id ("" = none)
	policy    string // a policy set selection (overrides the exit fields)
	exitVia   string
	exitPaths []string
	exitMode  string
}

func (r vpnRoute) sel() exitSel { return selOf(r.policy, r.exitVia, r.exitPaths, r.exitMode) }

func (r vpnRoute) src(kind, addr string) flowSrc {
	return flowSrc{kind: kind, user: r.userID, addr: addr}
}

type vpnNet struct {
	proto string // "ikev2" | "l2tp" | "openvpn"
	stack *stack.Stack
	ep    *vpnLink
	ctx   context.Context
	stop  context.CancelFunc

	// clients maps a tunnel address to the user the server authenticated
	// (reported by the server's session callback, once per address: a
	// dual-stack client has an IPv4 and an IPv6 entry).
	mu      sync.RWMutex
	clients map[netip.Addr]string
}

func newVPNNet(parent context.Context, proto string, gateways []netip.Addr, mtu int) (*vpnNet, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4, icmp.NewProtocol6},
	})
	sack := tcpip.TCPSACKEnabled(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: 8 << 20}
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv)
	snd := tcpip.TCPSendBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: 8 << 20}
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &snd)

	ep := newVPNLink(mtu)
	if err := s.CreateNIC(1, ep); err != nil {
		return nil, errors.New(err.String())
	}
	for _, gw := range gateways {
		if !gw.IsValid() {
			continue
		}
		pn := tcpip.NetworkProtocolNumber(ipv4.ProtocolNumber)
		if !gw.Is4() {
			pn = ipv6.ProtocolNumber
		}
		s.AddProtocolAddress(1, tcpip.ProtocolAddress{
			Protocol:          pn,
			AddressWithPrefix: tcpip.AddrFromSlice(gw.AsSlice()).WithPrefix(),
		}, stack.AddressProperties{})
	}
	// Promiscuous + spoofing: the stack accepts packets for any destination,
	// so the forwarders see every flow the client opens.
	s.SetSpoofing(1, true)
	s.SetPromiscuousMode(1, true)
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: 1},
		{Destination: header.IPv6EmptySubnet, NIC: 1},
	})
	ctx, stop := context.WithCancel(parent)
	return &vpnNet{proto: proto, stack: s, ep: ep, ctx: ctx, stop: stop, clients: map[netip.Addr]string{}}, nil
}

// session is the server's OnSession callback.
func (n *vpnNet) session(up bool, user string, addr netip.Addr) {
	n.mu.Lock()
	if up {
		n.clients[addr] = user
	} else if cur, ok := n.clients[addr]; ok && cur == user {
		delete(n.clients, addr)
	}
	n.mu.Unlock()
	if up {
		log.Printf("[%s] client %q up at %s", n.proto, user, addr)
	} else {
		log.Printf("[%s] client %q at %s gone", n.proto, user, addr)
	}
}

func (n *vpnNet) clientUser(ip string) (string, bool) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "", false
	}
	n.mu.RLock()
	u, ok := n.clients[a.Unmap()]
	n.mu.RUnlock()
	return u, ok
}

// clientCount counts clients, not addresses: an IPv6 entry is a client of
// its own only when its user holds no IPv4 address (a dual-stack client's
// v6 address belongs to its v4 one).
func (n *vpnNet) clientCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	v4users := map[string]bool{}
	count := 0
	for a, u := range n.clients {
		if a.Is4() {
			v4users[u] = true
			count++
		}
	}
	for a, u := range n.clients {
		if !a.Is4() && !v4users[u] {
			count++
		}
	}
	return count
}

func (n *vpnNet) close() {
	n.stop()
	n.ep.close()
	n.stack.Close()
}

// vpnLink is a channel endpoint whose outbound side applies backpressure:
// when the server's data path falls behind, the stack's writer waits instead
// of the packet being dropped (channel.Endpoint drops silently on a full
// queue, which a full-speed TCP sender sees as loss and collapses on).
type vpnLink struct {
	*channel.Endpoint
	out  chan *stack.PacketBuffer
	done chan struct{}
	once sync.Once
}

func newVPNLink(mtu int) *vpnLink {
	return &vpnLink{Endpoint: channel.New(1, uint32(mtu), ""), out: make(chan *stack.PacketBuffer, 1024), done: make(chan struct{})}
}

// WritePackets queues outbound packets, waiting (bounded) for room.
func (l *vpnLink) WritePackets(pkts stack.PacketBufferList) (int, tcpip.Error) {
	n := 0
	for _, pkt := range pkts.AsSlice() {
		p := pkt.Clone()
		select {
		case l.out <- p:
			n++
			continue
		case <-l.done:
			p.DecRef()
			return n, &tcpip.ErrClosedForSend{}
		default:
		}
		t := time.NewTimer(2 * time.Second) // a stalled data path must not wedge the stack
		select {
		case l.out <- p:
			n++
			t.Stop()
		case <-l.done:
			t.Stop()
			p.DecRef()
			return n, &tcpip.ErrClosedForSend{}
		case <-t.C:
			p.DecRef()
			return n, nil
		}
	}
	return n, nil
}

func (l *vpnLink) read(ctx context.Context) *stack.PacketBuffer {
	select {
	case p := <-l.out:
		return p
	case <-ctx.Done():
		return nil
	case <-l.done:
		return nil
	}
}

func (l *vpnLink) close() {
	l.once.Do(func() {
		close(l.done)
		l.Endpoint.Close()
		for {
			select {
			case p := <-l.out:
				p.DecRef()
			default:
				return
			}
		}
	})
}

// device adapts the stack's link endpoint to veepin's dataplane.Device.
func (n *vpnNet) device() dataplane.Device { return &vpnDevice{n: n} }

type vpnDevice struct{ n *vpnNet }

func (d *vpnDevice) Name() string { return "hy2scale-" + d.n.proto }

// Read returns the next packet the stack sends towards the clients.
func (d *vpnDevice) Read(buf []byte) (int, error) {
	pkt := d.n.ep.read(d.n.ctx)
	if pkt == nil {
		return 0, dataplane.ErrClosed
	}
	defer pkt.DecRef()
	v := pkt.ToView()
	defer v.Release()
	return copy(buf, v.AsSlice()), nil
}

// ReadBatch blocks for one packet and adds whatever else is already queued,
// so the server can flush a burst with one batched send.
func (d *vpnDevice) ReadBatch(bufs [][]byte, sizes []int) (int, error) {
	pkt := d.n.ep.read(d.n.ctx)
	if pkt == nil {
		return 0, dataplane.ErrClosed
	}
	n := 0
	for {
		v := pkt.ToView()
		sizes[n] = copy(bufs[n], v.AsSlice())
		v.Release()
		pkt.DecRef()
		n++
		if n == len(bufs) {
			return n, nil
		}
		select {
		case pkt = <-d.n.ep.out:
		default:
			return n, nil
		}
	}
}

// Write injects one packet from a client into the stack.
func (d *vpnDevice) Write(p []byte) (int, error) {
	if d.n.ctx.Err() != nil {
		return 0, dataplane.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	var proto tcpip.NetworkProtocolNumber
	switch p[0] >> 4 {
	case 4:
		proto = ipv4.ProtocolNumber
	case 6:
		proto = ipv6.ProtocolNumber
	default:
		return len(p), nil
	}
	pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), p...))})
	d.n.ep.InjectInbound(proto, pkb)
	pkb.DecRef()
	return len(p), nil
}

func (d *vpnDevice) Close() error { return nil } // the vpnNet owns the stack

// installForwarders terminates TCP and UDP. resolve maps a client's tunnel
// address to its route; ok=false drops the flow (no authenticated session).
func (a *App) installVPNForwarders(n *vpnNet, resolve func(srcIP string) (vpnRoute, bool)) {
	tcpFwd := tcp.NewForwarder(n.stack, 0, 4096, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		srcIP := id.RemoteAddress.String()
		route, ok := resolve(srcIP)
		if !ok {
			r.Complete(true)
			return
		}
		dst := net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort)))
		af := beginUserFlowStr(n.proto, "tcp", route.user, net.JoinHostPort(srcIP, strconv.Itoa(int(id.RemotePort))), dst)
		if sniffAfterAccept(dst) {
			conn, ep := acceptForwarded(r, "["+n.proto+"]", srcIP, dst)
			if conn == nil {
				af.End(errors.New("connection not accepted"))
				return
			}
			dctx := audit.NewContext(withRestrict(n.ctx, a.userRestrict(route.user)), af)
			rt := a.route(route.sel(), route.src(n.proto, net.JoinHostPort(srcIP, strconv.Itoa(int(id.RemotePort)))), "tcp", dst)
			remote, ok := a.dialAccepted(dctx, conn, ep, rt, dst)
			if !ok {
				af.End(errors.New("dial failed"))
				return
			}
			go func() {
				defer conn.Close()
				defer remote.Close()
				a.relayUser(conn, remote, route.user, srcIP, n.proto, true, af)
			}()
			return
		}
		// Dial before answering the SYN (the forwarder runs this in its own
		// goroutine): a destination the exit cannot reach is refused with a
		// RST at once, so the client falls back to its other address family
		// instead of waiting on an accepted connection that goes nowhere.
		ctx, cancel := context.WithTimeout(audit.NewContext(n.ctx, af), 15*time.Second)
		remote, err := a.dialVPNTCP(ctx, n.proto, route, net.JoinHostPort(srcIP, strconv.Itoa(int(id.RemotePort))), dst)
		cancel()
		if err != nil {
			af.End(err)
			debugLog("[%s] %s → %s via %q: %v", n.proto, srcIP, dst, route.exitVia, err)
			_, deny := policy.AsDeny(err)
			r.Complete(!deny || isReject(err)) // a policy drop sends nothing
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			af.End(errors.New(terr.String()))
			remote.Close()
			r.Complete(true)
			return
		}
		r.Complete(false)
		ep.SocketOptions().SetKeepAlive(true)
		conn := gonet.NewTCPConn(&wq, ep)
		go func() {
			defer conn.Close()
			defer remote.Close()
			a.relayUser(conn, remote, route.user, srcIP, n.proto, true, af)
		}()
	})
	n.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	udpFwd := udp.NewForwarder(n.stack, func(r *udp.ForwarderRequest) {
		id := r.ID()
		srcIP := id.RemoteAddress.String()
		route, ok := resolve(srcIP)
		if !ok {
			return
		}
		dst := net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort)))
		var wq waiter.Queue
		ep, uerr := r.CreateEndpoint(&wq)
		if uerr != nil {
			return
		}
		conn := gonet.NewUDPConn(&wq, ep)
		go a.relayVPNUDP(n, conn, route, srcIP, int(id.RemotePort), dst)
	})
	n.stack.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
}

// dialVPNTCP dials dst for a client: through its own exit, or (a direct
// client) through the routing rule matching dst, else directly.
func (a *App) dialVPNTCP(ctx context.Context, kind string, route vpnRoute, src, dst string) (net.Conn, error) {
	return a.dialRoutedTCP(withRestrict(ctx, a.userRestrict(route.user)), route.sel(), route.src(kind, src), dst)
}

// relayVPNUDP carries one client UDP flow. The netstack has no conntrack, so
// the flow lives until it has been idle in both directions.
func (a *App) relayVPNUDP(n *vpnNet, conn *gonet.UDPConn, route vpnRoute, srcIP string, srcPort int, dst string) {
	defer conn.Close()
	af := beginUserFlowStr(n.proto, "udp", route.user, net.JoinHostPort(srcIP, strconv.Itoa(srcPort)), dst)
	ctx, cancel := context.WithTimeout(audit.NewContext(n.ctx, af), 15*time.Second)
	remote, err := a.dialRoutedUDP(withRestrict(ctx, a.userRestrict(route.user)), route.sel(),
		route.src(n.proto, net.JoinHostPort(srcIP, strconv.Itoa(srcPort))), dst)
	cancel()
	if err != nil {
		af.End(err)
		debugLog("[%s] udp %s → %s via %q: %v", n.proto, srcIP, dst, route.exitVia, err)
		return
	}
	defer remote.Close()
	defer af.End(nil)

	idle := vpnUDPIdle(dst)
	refresh := func() {
		d := time.Now().Add(idle)
		conn.SetDeadline(d)
		remote.SetDeadline(d)
	}
	refresh()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			k, e := conn.Read(buf)
			if e != nil {
				return
			}
			refresh()
			af.Add(int64(k), 0)
			if _, e := remote.Write(buf[:k]); e != nil {
				return
			}
		}
	}()
	buf := make([]byte, 65535)
	for {
		k, e := remote.Read(buf)
		if e != nil {
			break
		}
		refresh()
		af.Add(0, int64(k))
		if _, e := conn.Write(buf[:k]); e != nil {
			break
		}
	}
	conn.Close()
	<-done
}

// vpnUDPIdle: DNS answers in one round trip; anything else may be a stream
// with long quiet gaps.
func vpnUDPIdle(dst string) time.Duration {
	if _, p, err := net.SplitHostPort(dst); err == nil && p == "53" {
		return 30 * time.Second
	}
	return 180 * time.Second
}

func vpnErr(proto string, err error) error { return fmt.Errorf("%s: %w", proto, err) }

package app

import (
	"errors"
	"net/netip"
	"os"
	"reflect"
	"unsafe"

	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// getStack extracts the unexported gvisor stack from netstack.Net via reflection.
func getStack(tnet *netstack.Net) *stack.Stack {
	v := reflect.ValueOf(tnet).Elem()
	f := v.Field(1)
	ptr := unsafe.Pointer(f.UnsafeAddr())
	return *(**stack.Stack)(ptr)
}

// wgTun is a userspace TUN device backed by gvisor.
// No kernel interfaces — everything runs in userspace.
type wgTun struct {
	ep             *channel.Endpoint
	Stack          *stack.Stack
	events         chan tun.Event
	notifyHandle   *channel.NotificationHandle
	incomingPacket chan *buffer.View
	mtu            int
}

func (t *wgTun) File() *os.File              { return nil }
func (t *wgTun) Events() <-chan tun.Event     { return t.events }
func (t *wgTun) BatchSize() int               { return 1 }
func (t *wgTun) MTU() (int, error)            { return t.mtu, nil }
func (t *wgTun) Name() (string, error)        { return "wg0", nil }

func (t *wgTun) Close() error {
	if t.notifyHandle != nil {
		t.ep.RemoveNotify(t.notifyHandle)
	}
	t.Stack.RemoveNIC(1)
	t.Stack.Close()
	t.ep.Close()
	if t.incomingPacket != nil {
		close(t.incomingPacket)
		t.incomingPacket = nil
	}
	return nil
}

func (t *wgTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	view, ok := <-t.incomingPacket
	if !ok {
		return 0, errors.New("tun closed")
	}
	n, err := view.Read(bufs[0][offset:])
	if err != nil {
		return 0, err
	}
	sizes[0] = n
	return 1, nil
}

func (t *wgTun) Write(bufs [][]byte, offset int) (int, error) {
	for _, buf := range bufs {
		packet := buf[offset:]
		if len(packet) == 0 {
			continue
		}
		pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(packet),
		})
		switch packet[0] >> 4 {
		case 4:
			t.ep.InjectInbound(header.IPv4ProtocolNumber, pkb)
		case 6:
			t.ep.InjectInbound(header.IPv6ProtocolNumber, pkb)
		}
		pkb.DecRef()
	}
	return len(bufs), nil
}

func (t *wgTun) WriteNotify() {
	pkt := t.ep.Read()
	if pkt == nil {
		return
	}
	view := pkt.ToView()
	pkt.DecRef()
	if t.incomingPacket != nil {
		t.incomingPacket <- view
	}
}

// createForwardingTUN creates a userspace TUN with HandleLocal=false for VPN server mode.
func createForwardingTUN(localAddresses []netip.Addr, mtu int) (tun.Device, *stack.Stack, error) {
	s := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol, ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol, udp.NewProtocol,
			icmp.NewProtocol6, icmp.NewProtocol4,
		},
		HandleLocal: false, // Process ALL traffic, not just local
	})

	ep := channel.New(1024, uint32(mtu), "")
	dev := &wgTun{
		ep:             ep,
		Stack:          s,
		events:         make(chan tun.Event, 10),
		incomingPacket: make(chan *buffer.View, 256),
		mtu:            mtu,
	}

	sackOpt := tcpip.TCPSACKEnabled(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sackOpt)

	dev.notifyHandle = ep.AddNotify(dev)

	if tcpipErr := s.CreateNIC(1, ep); tcpipErr != nil {
		return nil, nil, errors.New(tcpipErr.String())
	}

	for _, ip := range localAddresses {
		var pn tcpip.NetworkProtocolNumber
		if ip.Is4() {
			pn = ipv4.ProtocolNumber
		} else {
			pn = ipv6.ProtocolNumber
		}
		addr := tcpip.ProtocolAddress{
			Protocol:          pn,
			AddressWithPrefix: tcpip.AddrFromSlice(ip.AsSlice()).WithPrefix(),
		}
		if tcpipErr := s.AddProtocolAddress(1, addr, stack.AddressProperties{}); tcpipErr != nil {
			return nil, nil, errors.New(tcpipErr.String())
		}
	}

	// Promiscuous + spoofing: accept and respond to all traffic
	s.SetPromiscuousMode(1, true)
	s.SetSpoofing(1, true)


	s.AddRoute(tcpip.Route{Destination: header.IPv4EmptySubnet, NIC: 1})
	s.AddRoute(tcpip.Route{Destination: header.IPv6EmptySubnet, NIC: 1})

	dev.events <- tun.EventUp
	return dev, s, nil
}

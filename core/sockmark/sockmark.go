// Package sockmark marks hy2scale's own sockets (SO_MARK) so the system
// traffic capture (core/policy/capture) never takes them over: every
// outbound socket the node opens — peer (QUIC) sockets, relay and exit
// dials, direct dials, resolvers, HTTP clients, VPN listeners — goes
// through one of these helpers. Linux only; elsewhere they are plain
// dials. Without CAP_NET_ADMIN the mark cannot be set: the helpers then
// dial unmarked (the capture is unavailable there anyway).
package sockmark

import (
	"context"
	"net"
	"syscall"
	"time"
)

// Mark is the SO_MARK of hy2scale's sockets. The capture's OUTPUT chain
// returns early for packets carrying it.
const Mark = 0x1234

// Chain returns a Control hook that marks the socket and then runs ctl
// (nil = none).
func Chain(ctl func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	if ctl == nil {
		return Control
	}
	return func(network, address string, c syscall.RawConn) error {
		if err := Control(network, address, c); err != nil {
			return err
		}
		return ctl(network, address, c)
	}
}

// Dialer returns a copy of d whose sockets are marked.
func Dialer(d net.Dialer) *net.Dialer {
	d.Control = Chain(d.Control)
	return &d
}

// ListenConfig returns a copy of lc whose sockets are marked.
func ListenConfig(lc net.ListenConfig) *net.ListenConfig {
	lc.Control = Chain(lc.Control)
	return &lc
}

// DialContext dials like net.Dialer.DialContext, marked.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return Dialer(net.Dialer{}).DialContext(ctx, network, addr)
}

// DialTimeout dials like net.DialTimeout, marked.
func DialTimeout(network, addr string, timeout time.Duration) (net.Conn, error) {
	return Dialer(net.Dialer{Timeout: timeout}).Dial(network, addr)
}

// ListenUDP opens a UDP socket like net.ListenUDP, marked.
func ListenUDP(network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
	addr := ""
	if laddr != nil {
		addr = laddr.String()
	}
	pc, err := ListenConfig(net.ListenConfig{}).ListenPacket(context.Background(), network, addr)
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

// ListenPacket opens a packet socket like net.ListenPacket, marked.
func ListenPacket(network, addr string) (net.PacketConn, error) {
	return ListenConfig(net.ListenConfig{}).ListenPacket(context.Background(), network, addr)
}

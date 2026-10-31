package dataplane

import (
	"net"
	"net/netip"
	"time"
)

// Datagram is a UDP endpoint owned by the embedder -- typically one share of a
// socket demultiplexed between several servers. ReadFrom reports the local
// address each datagram was sent to, so replies keep being source-pinned.
// (hy2scale patch P2)
type Datagram interface {
	ReadFrom(b []byte) (n int, from *net.UDPAddr, local netip.Addr, err error)
	WriteTo(b []byte, to *net.UDPAddr) (int, error)
	Close() error
	LocalAddr() net.Addr
	SetReadDeadline(t time.Time) error
}

// DatagramBatch is optionally implemented by a Datagram that can send a burst
// to one peer in one call (sendmmsg). (hy2scale P6)
type DatagramBatch interface {
	WriteBatchTo(pkts [][]byte, to *net.UDPAddr) (int, error)
}

// ESPClaimer is optionally implemented by a Datagram shared between several
// ESP servers: each one claims the inbound SPIs it installs, so ESP is handed to
// the server that owns the SA even when two servers' peers share one source
// address and port -- a router running an IKEv2 client and an L2TP/IPsec client
// from one IKE daemon does. (hy2scale P12)
type ESPClaimer interface {
	ClaimESP(spi uint32, claim bool)
}

// ClaimESP claims (or releases) an inbound ESP SPI on the underlying Datagram,
// when it routes by SPI; on a plain socket it does nothing. (hy2scale P12)
func (p *PacketConn) ClaimESP(spi uint32, claim bool) {
	if c, ok := p.virt.(ESPClaimer); ok {
		c.ClaimESP(spi, claim)
	}
}

// ListenFunc opens the server's socket on a port. Servers that accept one use
// it instead of net.ListenUDP.
type ListenFunc func(port int) (*PacketConn, error)

// NewVirtualPacketConn wraps an embedder-owned Datagram in the PacketConn every
// server already speaks.
func NewVirtualPacketConn(d Datagram) *PacketConn {
	p := &PacketConn{
		virt:   d,
		locals: map[netip.AddrPort]localEntry{},
		now:    time.Now,
	}
	p.lastGC = p.now()
	return p
}

// LocalFor is the local address peer last reached this socket on, if known.
func (p *PacketConn) LocalFor(peer *net.UDPAddr) net.IP {
	if local, ok := p.lookup(peer); ok {
		return net.IP(local.Unmap().AsSlice())
	}
	return nil
}

func (p *PacketConn) virtRead(b []byte) (int, *net.UDPAddr, error) {
	n, from, local, err := p.virt.ReadFrom(b)
	if err == nil && from != nil && local.IsValid() {
		p.remember(from, local)
	}
	return n, from, err
}

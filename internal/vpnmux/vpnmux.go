// Package vpnmux shares the IKE (500) and NAT-T (4500) UDP sockets between the
// userspace IKEv2 and L2TP/IPsec servers, which would otherwise each bind both.
//
// Datagrams are classified as they arrive. IKE messages carry their major
// version in the header (2 = IKEv2, 1 = IKEv1, which is what L2TP/IPsec uses),
// and every IKE message records which server owns its sender. ESP goes to the
// server that claimed its SPI; ESP with an unclaimed SPI and NAT keepalives
// follow the owner of their sender address, which NAT-T guarantees is the
// address the peer's IKE came from. The SPI comes first because one sender can
// be a peer of both servers: a router whose IKE daemon runs an IKEv2 client and
// an L2TP/IPsec client to the same node sends both from one address and port.
package vpnmux

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/xen0bit/veepin/dataplane"
)

// Kind is the server a datagram belongs to.
type Kind uint8

const (
	IKEv2 Kind = 1 // IKEv2 (major version 2)
	IKEv1 Kind = 2 // IKEv1, i.e. L2TP/IPsec
)

func (k Kind) String() string {
	switch k {
	case IKEv2:
		return "ikev2"
	case IKEv1:
		return "ikev1"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

const (
	ikeHeaderLen = 28
	queueLen     = 1024
	ownerIdle    = 10 * time.Minute
	ownerMax     = 16384
)

// Mux owns the shared sockets. Sockets are opened by the first Share on a
// port and closed when the last share on it closes.
type Mux struct {
	ikePort, nattPort int

	mu    sync.Mutex
	socks map[int]*socket
}

// New returns a mux for the given IKE and NAT-T ports (normally 500, 4500),
// listening on all addresses.
func New(ikePort, nattPort int) *Mux {
	return &Mux{ikePort: ikePort, nattPort: nattPort, socks: map[int]*socket{}}
}

// Listen returns a dataplane.ListenFunc handing kind its share of each port.
func (m *Mux) Listen(kind Kind) dataplane.ListenFunc {
	return func(port int) (*dataplane.PacketConn, error) {
		sh, err := m.Share(port, kind)
		if err != nil {
			return nil, err
		}
		return dataplane.NewVirtualPacketConn(sh), nil
	}
}

// Share registers kind on port and returns its endpoint.
func (m *Mux) Share(port int, kind Kind) (*Share, error) {
	if port != m.ikePort && port != m.nattPort {
		return nil, fmt.Errorf("vpnmux: port %d is neither the IKE (%d) nor NAT-T (%d) port", port, m.ikePort, m.nattPort)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.socks[port]
	if s == nil {
		c, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
		if err != nil {
			return nil, err
		}
		s = &socket{
			mux: m, port: port, natt: port == m.nattPort,
			pc:     dataplane.NewPacketConn(c),
			shares: map[Kind]*Share{},
			owners: map[netip.AddrPort]owner{},
			spis:   map[uint32]Kind{},
		}
		m.socks[port] = s
		go s.readLoop()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shares[kind] != nil {
		return nil, fmt.Errorf("vpnmux: %s already listening on %d", kind, port)
	}
	sh := &Share{sock: s, kind: kind, ch: make(chan datagram, queueLen), done: make(chan struct{}), wake: make(chan struct{}, 1)}
	s.shares[kind] = sh
	return sh, nil
}

// Stats is a snapshot of one socket's counters.
type Stats struct {
	Port       int
	Delivered  map[Kind]uint64
	Unowned    uint64 // ESP / keepalive from a sender no server owns
	NoListener uint64 // IKE for a kind that has no share
	Overflow   uint64 // dropped on a full queue
	Owners     int
}

// Stats reports per-port counters.
func (m *Mux) Stats() []Stats {
	m.mu.Lock()
	socks := make([]*socket, 0, len(m.socks))
	for _, s := range m.socks {
		socks = append(socks, s)
	}
	m.mu.Unlock()
	out := make([]Stats, 0, len(socks))
	for _, s := range socks {
		s.mu.Lock()
		st := Stats{Port: s.port, Delivered: map[Kind]uint64{}, Unowned: s.unowned,
			NoListener: s.noListener, Overflow: s.overflow, Owners: len(s.owners)}
		for k, v := range s.delivered {
			st.Delivered[k] = v
		}
		s.mu.Unlock()
		out = append(out, st)
	}
	return out
}

type owner struct {
	kind Kind
	seen time.Time
}

type datagram struct {
	b     []byte
	from  *net.UDPAddr
	local netip.Addr
}

type socket struct {
	mux  *Mux
	port int
	natt bool
	pc   *dataplane.PacketConn

	wmu        sync.Mutex // serialises WriteBatch across shares
	mu         sync.Mutex
	shares     map[Kind]*Share
	owners     map[netip.AddrPort]owner
	spis       map[uint32]Kind // inbound ESP SPI -> the server that installed it
	lastGC     time.Time
	delivered  map[Kind]uint64
	unowned    uint64
	noListener uint64
	overflow   uint64
}

func (s *socket) readLoop() {
	buf := make([]byte, 65535)
	for {
		n, from, local, err := s.pc.ReadFromUDPLocal(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if from == nil || n == 0 {
			continue
		}
		s.dispatch(buf[:n], from, local)
	}
}

// classify reports the kind of an IKE message, or ok=false for ESP and NAT
// keepalives, which have to follow their sender.
func (s *socket) classify(pkt []byte) (Kind, bool) {
	ike := pkt
	if s.natt {
		// RFC 3948: on the NAT-T port IKE follows a four-octet zero
		// non-ESP marker; anything else is ESP (or a 0xff keepalive).
		if len(pkt) < 4 || pkt[0]|pkt[1]|pkt[2]|pkt[3] != 0 {
			return 0, false
		}
		ike = pkt[4:]
	}
	if len(ike) < ikeHeaderLen {
		return 0, false
	}
	switch ike[17] >> 4 {
	case 2:
		return IKEv2, true
	case 1:
		return IKEv1, true
	}
	return 0, false
}

func (s *socket) dispatch(pkt []byte, from *net.UDPAddr, local netip.Addr) {
	key := from.AddrPort()
	key = netip.AddrPortFrom(key.Addr().Unmap(), key.Port())
	now := time.Now()

	kind, isIKE := s.classify(pkt)

	s.mu.Lock()
	if isIKE {
		s.noteOwnerLocked(key, kind, now)
	} else if k, ok := s.espOwnerLocked(pkt); ok {
		kind = k
		if o, ok := s.owners[key]; ok {
			o.seen = now
			s.owners[key] = o
		}
	} else {
		o, ok := s.owners[key]
		if !ok {
			s.unowned++
			s.mu.Unlock()
			return
		}
		kind = o.kind
		o.seen = now
		s.owners[key] = o
	}
	sh := s.shares[kind]
	if sh == nil {
		s.noListener++
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	d := datagram{b: append([]byte(nil), pkt...), from: from, local: local}
	select {
	case sh.ch <- d:
		s.mu.Lock()
		if s.delivered == nil {
			s.delivered = map[Kind]uint64{}
		}
		s.delivered[kind]++
		s.mu.Unlock()
	default:
		s.mu.Lock()
		s.overflow++
		s.mu.Unlock()
	}
}

// espOwnerLocked is the server that claimed an ESP packet's SPI. A NAT
// keepalive (one 0xff octet) has none.
func (s *socket) espOwnerLocked(pkt []byte) (Kind, bool) {
	if !s.natt || len(pkt) < 8 {
		return 0, false
	}
	k, ok := s.spis[binary.BigEndian.Uint32(pkt)]
	return k, ok
}

func (s *socket) noteOwnerLocked(key netip.AddrPort, kind Kind, now time.Time) {
	s.owners[key] = owner{kind: kind, seen: now}
	if len(s.owners) > ownerMax || now.Sub(s.lastGC) > time.Minute {
		s.lastGC = now
		for k, o := range s.owners {
			if now.Sub(o.seen) > ownerIdle {
				delete(s.owners, k)
			}
		}
		// Still over the cap: drop the oldest half rather than grow.
		for len(s.owners) > ownerMax {
			var oldest netip.AddrPort
			var t time.Time
			for k, o := range s.owners {
				if t.IsZero() || o.seen.Before(t) {
					oldest, t = k, o.seen
				}
			}
			delete(s.owners, oldest)
		}
	}
}

func (s *socket) release(sh *Share) {
	s.mu.Lock()
	if s.shares[sh.kind] == sh {
		delete(s.shares, sh.kind)
		for spi, k := range s.spis { // a stopped server's SAs are gone with it
			if k == sh.kind {
				delete(s.spis, spi)
			}
		}
	}
	empty := len(s.shares) == 0
	s.mu.Unlock()
	if !empty {
		return
	}
	m := s.mux
	m.mu.Lock()
	if m.socks[s.port] == s {
		s.mu.Lock()
		stillEmpty := len(s.shares) == 0
		s.mu.Unlock()
		if stillEmpty {
			delete(m.socks, s.port)
			s.pc.Close()
		}
	}
	m.mu.Unlock()
}

// Share is one server's endpoint on a shared socket. It implements
// dataplane.Datagram.
type Share struct {
	sock *socket
	kind Kind
	ch   chan datagram

	once sync.Once
	done chan struct{}

	dmu      sync.Mutex
	deadline time.Time
	wake     chan struct{}
}

// ReadFrom returns the next datagram for this share.
func (sh *Share) ReadFrom(b []byte) (int, *net.UDPAddr, netip.Addr, error) {
	for {
		sh.dmu.Lock()
		dl := sh.deadline
		sh.dmu.Unlock()
		var timeout <-chan time.Time
		var t *time.Timer
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, nil, netip.Addr{}, os.ErrDeadlineExceeded
			}
			t = time.NewTimer(d)
			timeout = t.C
		}
		select {
		case d := <-sh.ch:
			stopTimer(t)
			return copy(b, d.b), d.from, d.local, nil
		case <-sh.done:
			stopTimer(t)
			return 0, nil, netip.Addr{}, net.ErrClosed
		case <-timeout:
			return 0, nil, netip.Addr{}, os.ErrDeadlineExceeded
		case <-sh.wake:
			stopTimer(t) // deadline changed: recompute
		}
	}
}

// WriteTo sends from the shared socket, source-pinned to the address the peer
// last reached it on, and claims the peer for this share.
func (sh *Share) WriteTo(b []byte, to *net.UDPAddr) (int, error) {
	select {
	case <-sh.done:
		return 0, net.ErrClosed
	default:
	}
	if to != nil {
		key := to.AddrPort()
		key = netip.AddrPortFrom(key.Addr().Unmap(), key.Port())
		s := sh.sock
		s.mu.Lock()
		if o, ok := s.owners[key]; !ok || o.kind != sh.kind {
			s.noteOwnerLocked(key, sh.kind, time.Now())
		}
		s.mu.Unlock()
	}
	return sh.sock.pc.WriteToUDP(b, to)
}

// ClaimESP routes ESP carrying spi to this share (claim) or stops doing so,
// implementing dataplane.ESPClaimer. A release only drops this share's own
// claim, so a stale release cannot take an SPI the other server now holds.
func (sh *Share) ClaimESP(spi uint32, claim bool) {
	s := sh.sock
	s.mu.Lock()
	if claim {
		s.spis[spi] = sh.kind
	} else if s.spis[spi] == sh.kind {
		delete(s.spis, spi)
	}
	s.mu.Unlock()
}

// WriteBatchTo sends a burst to one peer with one sendmmsg.
func (sh *Share) WriteBatchTo(pkts [][]byte, to *net.UDPAddr) (int, error) {
	select {
	case <-sh.done:
		return 0, net.ErrClosed
	default:
	}
	s := sh.sock
	s.wmu.Lock() // the batch scratch of the socket is single-writer
	defer s.wmu.Unlock()
	return s.pc.WriteBatch(pkts, to)
}

// Close removes this share; the socket closes with its last share.
func (sh *Share) Close() error {
	sh.once.Do(func() {
		close(sh.done)
		sh.sock.release(sh)
	})
	return nil
}

// LocalAddr is the shared socket's bound address.
func (sh *Share) LocalAddr() net.Addr { return sh.sock.pc.LocalAddr() }

// SetReadDeadline bounds ReadFrom.
func (sh *Share) SetReadDeadline(t time.Time) error {
	sh.dmu.Lock()
	sh.deadline = t
	sh.dmu.Unlock()
	select {
	case sh.wake <- struct{}{}:
	default:
	}
	return nil
}

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

var (
	_ dataplane.Datagram      = (*Share)(nil)
	_ dataplane.DatagramBatch = (*Share)(nil)
	_ dataplane.ESPClaimer    = (*Share)(nil)
)

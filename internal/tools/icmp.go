package tools

import (
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// icmpSock is one ICMP endpoint: an unprivileged datagram socket when the
// kernel allows it (net.ipv4.ping_group_range), else a raw one (root or
// CAP_NET_RAW). A raw socket sees every ICMP packet of the host, so its
// packets are matched by echo ID; a datagram socket gets its ID from the
// kernel, which only hands it its own replies.
type icmpSock struct {
	c   *icmp.PacketConn
	v6  bool
	raw bool
	id  int
	p4  *ipv4.PacketConn
	p6  *ipv6.PacketConn
}

func openICMP(v6, rawOnly bool) (*icmpSock, error) {
	nets, addr := []string{"udp4", "ip4:icmp"}, "0.0.0.0"
	if v6 {
		nets, addr = []string{"udp6", "ip6:ipv6-icmp"}, "::"
	}
	if rawOnly {
		nets = nets[1:]
	}
	var first error
	for _, nw := range nets {
		c, err := icmp.ListenPacket(nw, addr)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		s := &icmpSock{c: c, v6: v6, raw: strings.HasPrefix(nw, "ip")}
		if v6 {
			s.p6 = c.IPv6PacketConn()
			s.p6.SetControlMessage(ipv6.FlagHopLimit, true)
		} else {
			s.p4 = c.IPv4PacketConn()
			s.p4.SetControlMessage(ipv4.FlagTTL, true)
		}
		s.id = echoIDs.get()
		return s, nil
	}
	return nil, first
}

func (s *icmpSock) Close() error {
	echoIDs.put(s.id)
	return s.c.Close()
}

// sockReason: short text for a socket error.
func sockReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES), errors.Is(err, os.ErrPermission):
		return "not permitted"
	case errors.Is(err, syscall.EPROTONOSUPPORT), errors.Is(err, syscall.EAFNOSUPPORT):
		return "not supported"
	}
	var se *os.SyscallError
	if errors.As(err, &se) {
		return se.Err.Error()
	}
	return err.Error()
}

// send writes an echo request; ttl > 0 sets the TTL / hop limit first.
func (s *icmpSock) send(dst net.IPAddr, seq int, payload []byte, ttl int) error {
	var typ icmp.Type = ipv4.ICMPTypeEcho
	if s.v6 {
		typ = ipv6.ICMPTypeEchoRequest
	}
	m := icmp.Message{Type: typ, Body: &icmp.Echo{ID: s.id, Seq: seq & 0xffff, Data: payload}}
	b, err := m.Marshal(nil) // ICMPv6: the kernel fills the checksum
	if err != nil {
		return err
	}
	if ttl > 0 {
		if s.v6 {
			err = s.p6.SetHopLimit(ttl)
		} else {
			err = s.p4.SetTTL(ttl)
		}
		if err != nil {
			return err
		}
	}
	var to net.Addr = &net.IPAddr{IP: dst.IP, Zone: dst.Zone}
	if !s.raw {
		to = &net.UDPAddr{IP: dst.IP, Zone: dst.Zone}
	}
	_, err = s.c.WriteTo(b, to)
	return err
}

// icmpIn is one received packet of ours: an echo reply, or an error (time
// exceeded, unreachable) about one of our requests.
type icmpIn struct {
	from net.IP
	echo bool
	seq  int
	ttl  int // of the reply (0: unknown)
	n    int // ICMP message length
	at   time.Time
	dst  net.IP // error: the destination of our request
}

// recv reads until a packet of ours arrives or the deadline set on s.c.
func (s *icmpSock) recv(buf []byte) (icmpIn, error) {
	for {
		var (
			n    int
			ttl  int
			src  net.Addr
			err  error
			prot = 1
		)
		if s.v6 {
			var cm *ipv6.ControlMessage
			n, cm, src, err = s.p6.ReadFrom(buf)
			if cm != nil {
				ttl = cm.HopLimit
			}
			prot = 58
		} else {
			var cm *ipv4.ControlMessage
			n, cm, src, err = s.p4.ReadFrom(buf)
			if cm != nil {
				ttl = cm.TTL
			}
		}
		if err != nil {
			return icmpIn{}, err
		}
		in := icmpIn{from: addrIP(src), ttl: ttl, n: n, at: time.Now()}
		m, err := icmp.ParseMessage(prot, buf[:n])
		if err != nil {
			continue
		}
		switch m.Type {
		case ipv4.ICMPTypeEchoReply, ipv6.ICMPTypeEchoReply:
			e, ok := m.Body.(*icmp.Echo)
			if !ok || s.raw && e.ID != s.id {
				continue
			}
			in.echo, in.seq = true, e.Seq
			return in, nil
		case ipv4.ICMPTypeTimeExceeded, ipv6.ICMPTypeTimeExceeded,
			ipv4.ICMPTypeDestinationUnreachable, ipv6.ICMPTypeDestinationUnreachable:
			var data []byte
			switch b := m.Body.(type) {
			case *icmp.TimeExceeded:
				data = b.Data
			case *icmp.DstUnreach:
				data = b.Data
			}
			dst, id, seq, ok := s.inner(data)
			if !ok || s.raw && id != s.id {
				continue
			}
			in.dst, in.seq = dst, seq
			return in, nil
		}
	}
}

// inner parses the request quoted in an ICMP error: its destination, echo
// ID and sequence.
func (s *icmpSock) inner(d []byte) (net.IP, int, int, bool) {
	if s.v6 {
		if len(d) < 48 || d[6] != 58 || d[40] != byte(ipv6.ICMPTypeEchoRequest) {
			return nil, 0, 0, false
		}
		return net.IP(append([]byte(nil), d[24:40]...)), int(binary.BigEndian.Uint16(d[44:])),
			int(binary.BigEndian.Uint16(d[46:])), true
	}
	if len(d) < 20 {
		return nil, 0, 0, false
	}
	hl := int(d[0]&0x0f) * 4
	if hl < 20 || len(d) < hl+8 || d[9] != 1 || d[hl] != byte(ipv4.ICMPTypeEcho) {
		return nil, 0, 0, false
	}
	return net.IP(append([]byte(nil), d[16:20]...)), int(binary.BigEndian.Uint16(d[hl+4:])),
		int(binary.BigEndian.Uint16(d[hl+6:])), true
}

func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPAddr:
		return v.IP
	case *net.UDPAddr:
		return v.IP
	}
	return nil
}

// echoIDs hands out distinct echo IDs to the sockets open at a time, so
// concurrent raw-socket tests never take each other's replies.
var echoIDs = &idPool{used: map[int]bool{}}

type idPool struct {
	mu   sync.Mutex
	used map[int]bool
}

func (p *idPool) get() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		id := 1 + rand.IntN(0xfffe)
		if !p.used[id] {
			p.used[id] = true
			return id
		}
	}
}

func (p *idPool) put(id int) {
	p.mu.Lock()
	delete(p.used, id)
	p.mu.Unlock()
}

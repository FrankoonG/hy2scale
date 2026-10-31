package vnic

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"time"
)

// Minimal DHCPv4 (RFC 2131 / 2132 / 3442) message codec.

const (
	dhcpDiscover = 1
	dhcpOffer    = 2
	dhcpRequest  = 3
	dhcpDecline  = 4
	dhcpAck      = 5
	dhcpNak      = 6
	dhcpRelease  = 7

	optPad          = 0
	optSubnetMask   = 1
	optRouter       = 3
	optDNS          = 6
	optHostname     = 12
	optDomainName   = 15
	optMTU          = 26
	optBroadcast    = 28
	optRequestedIP  = 50
	optLeaseTime    = 51
	optOverload     = 52
	optMsgType      = 53
	optServerID     = 54
	optParamList    = 55
	optMaxMsgSize   = 57
	optT1           = 58
	optT2           = 59
	optClientID     = 61
	optClasslessRte = 121
	optEnd          = 255

	dhcpFixedLen = 236
	dhcpMagic    = 0x63825363
)

type dhcpMsg struct {
	op     byte
	xid    uint32
	flags  uint16
	ciaddr netip.Addr
	yiaddr netip.Addr
	chaddr [6]byte
	opts   map[byte][]byte
	order  []byte
}

func (m *dhcpMsg) set(code byte, v []byte) {
	if m.opts == nil {
		m.opts = map[byte][]byte{}
	}
	if _, ok := m.opts[code]; !ok {
		m.order = append(m.order, code)
	}
	m.opts[code] = v
}

func (m *dhcpMsg) msgType() byte {
	if v := m.opts[optMsgType]; len(v) == 1 {
		return v[0]
	}
	return 0
}

func addr4(a netip.Addr) []byte {
	if !a.Is4() {
		return make([]byte, 4)
	}
	b := a.As4()
	return b[:]
}

func (m *dhcpMsg) marshal() []byte {
	b := make([]byte, dhcpFixedLen+4, 548)
	b[0] = m.op
	b[1] = 1 // ethernet
	b[2] = 6
	binary.BigEndian.PutUint32(b[4:], m.xid)
	binary.BigEndian.PutUint16(b[10:], m.flags)
	copy(b[12:16], addr4(m.ciaddr))
	copy(b[16:20], addr4(m.yiaddr))
	copy(b[28:34], m.chaddr[:])
	binary.BigEndian.PutUint32(b[dhcpFixedLen:], dhcpMagic)
	for _, c := range m.order {
		v := m.opts[c]
		for len(v) > 255 {
			b = append(b, c, 255)
			b = append(b, v[:255]...)
			v = v[255:]
		}
		b = append(b, c, byte(len(v)))
		b = append(b, v...)
	}
	b = append(b, optEnd)
	for len(b) < 300 { // BOOTP minimum; some servers drop shorter messages
		b = append(b, optPad)
	}
	return b
}

func parseDHCP(b []byte) (*dhcpMsg, error) {
	if len(b) < dhcpFixedLen+4 || binary.BigEndian.Uint32(b[dhcpFixedLen:]) != dhcpMagic {
		return nil, errors.New("not a DHCP message")
	}
	m := &dhcpMsg{op: b[0], xid: binary.BigEndian.Uint32(b[4:]), flags: binary.BigEndian.Uint16(b[10:]), opts: map[byte][]byte{}}
	m.ciaddr = netip.AddrFrom4([4]byte(b[12:16]))
	m.yiaddr = netip.AddrFrom4([4]byte(b[16:20]))
	copy(m.chaddr[:], b[28:34])
	parseOpts := func(o []byte) {
		for i := 0; i < len(o); {
			c := o[i]
			if c == optPad {
				i++
				continue
			}
			if c == optEnd || i+1 >= len(o) {
				return
			}
			n := int(o[i+1])
			if i+2+n > len(o) {
				return
			}
			m.opts[c] = append(m.opts[c], o[i+2:i+2+n]...) // RFC 3396 concatenation
			i += 2 + n
		}
	}
	parseOpts(b[dhcpFixedLen+4:])
	if ov := m.opts[optOverload]; len(ov) == 1 {
		if ov[0]&1 != 0 {
			parseOpts(b[108:236])
		}
		if ov[0]&2 != 0 {
			parseOpts(b[44:108])
		}
	}
	return m, nil
}

func (m *dhcpMsg) addr(code byte) netip.Addr {
	if v := m.opts[code]; len(v) >= 4 {
		return netip.AddrFrom4([4]byte(v[:4]))
	}
	return netip.Addr{}
}

func (m *dhcpMsg) addrs(code byte) []netip.Addr {
	v := m.opts[code]
	var out []netip.Addr
	for i := 0; i+4 <= len(v); i += 4 {
		a := netip.AddrFrom4([4]byte(v[i : i+4]))
		if a.IsValid() && !a.IsUnspecified() {
			out = append(out, a)
		}
	}
	return out
}

func (m *dhcpMsg) seconds(code byte) (time.Duration, bool) {
	v := m.opts[code]
	if len(v) != 4 {
		return 0, false
	}
	s := binary.BigEndian.Uint32(v)
	if s == 0xffffffff {
		return infiniteLease, true
	}
	return time.Duration(s) * time.Second, true
}

const infiniteLease = time.Duration(1<<63 - 1)

// prefixLen returns the subnet mask option as a prefix length (classful
// guess when absent).
func (m *dhcpMsg) prefixLen() int {
	if v := m.opts[optSubnetMask]; len(v) == 4 {
		mask := binary.BigEndian.Uint32(v)
		n := 0
		for mask&0x80000000 != 0 {
			n++
			mask <<= 1
		}
		if mask == 0 && n > 0 {
			return n
		}
	}
	if a := m.yiaddr.As4(); a[0] < 128 {
		return 8
	} else if a[0] < 192 {
		return 16
	}
	return 24
}

// dhcpRoute is one RFC 3442 classless static route.
type dhcpRoute struct {
	Dst netip.Prefix `json:"dst"`
	Via netip.Addr   `json:"via"`
}

func (m *dhcpMsg) classless() []dhcpRoute {
	v := m.opts[optClasslessRte]
	var out []dhcpRoute
	for i := 0; i < len(v); {
		w := int(v[i])
		n := (w + 7) / 8
		if w > 32 || i+1+n+4 > len(v) {
			return nil
		}
		var d [4]byte
		copy(d[:], v[i+1:i+1+n])
		gw := netip.AddrFrom4([4]byte(v[i+1+n : i+5+n]))
		out = append(out, dhcpRoute{Dst: netip.PrefixFrom(netip.AddrFrom4(d), w).Masked(), Via: gw})
		i += 5 + n
	}
	return out
}

// clientIDBytes turns the configured client identifier into option 61 bytes:
// colon/space separated hex is taken verbatim, anything else is text (type 0).
func clientIDBytes(s string) []byte {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	h := strings.NewReplacer(":", "", "-", "", " ", "").Replace(s)
	if strings.ContainsAny(s, ":-") && len(h)%2 == 0 {
		if b, err := hex.DecodeString(h); err == nil && len(b) >= 2 {
			return b
		}
	}
	return append([]byte{0}, s...)
}

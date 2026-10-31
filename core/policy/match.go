package policy

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Leaf values (spec §2.2), parsed once when a program is compiled.

// ipRange is an inclusive address range of one family.
type ipRange struct{ lo, hi netip.Addr }

func (r ipRange) contains(ip netip.Addr) bool {
	return ip.Is4() == r.lo.Is4() && r.lo.Compare(ip) <= 0 && ip.Compare(r.hi) <= 0
}

// ParseIPValue parses an IP leaf value: an address, a CIDR or "a-b".
func ParseIPValue(v string) (lo, hi netip.Addr, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return lo, hi, fmt.Errorf("empty address")
	}
	if strings.Contains(v, "/") {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return lo, hi, fmt.Errorf("invalid CIDR %q", v)
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), unmapBits(p))
		p = p.Masked()
		return p.Addr(), lastAddr(p), nil
	}
	if a, b, ok := strings.Cut(v, "-"); ok {
		lo, err1 := netip.ParseAddr(strings.TrimSpace(a))
		hi, err2 := netip.ParseAddr(strings.TrimSpace(b))
		if err1 != nil || err2 != nil {
			return lo, hi, fmt.Errorf("invalid range %q", v)
		}
		lo, hi = lo.Unmap().WithZone(""), hi.Unmap().WithZone("")
		if lo.Is4() != hi.Is4() || lo.Compare(hi) > 0 {
			return lo, hi, fmt.Errorf("invalid range %q", v)
		}
		return lo, hi, nil
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return lo, hi, fmt.Errorf("invalid address %q", v)
	}
	a = a.Unmap().WithZone("")
	return a, a, nil
}

func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() {
		b := p.Bits() - 96
		if b < 0 {
			b = 0
		}
		return b
	}
	return p.Bits()
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	bits := p.Bits()
	for i := range b {
		for j := 0; j < 8; j++ {
			if i*8+j >= bits {
				b[i] |= 0x80 >> j
			}
		}
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// portRange is an inclusive port range.
type portRange struct{ lo, hi uint16 }

// ParsePortValue parses a port leaf value: "443" or "8000-9000".
func ParsePortValue(v string) (lo, hi uint16, err error) {
	v = strings.TrimSpace(v)
	parse := func(s string) (uint16, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 0 || n > 65535 {
			return 0, fmt.Errorf("invalid port %q", v)
		}
		return uint16(n), nil
	}
	if a, b, ok := strings.Cut(v, "-"); ok {
		if lo, err = parse(a); err != nil {
			return
		}
		if hi, err = parse(b); err != nil {
			return
		}
		if lo > hi {
			return 0, 0, fmt.Errorf("invalid port range %q", v)
		}
		return
	}
	lo, err = parse(v)
	return lo, lo, err
}

// domain match kinds
const (
	domSuffix  = iota // "example.com": the name and its subdomains
	domExact          // "=example.com"
	domKeyword        // "~kw"
	domSubOnly        // "*.example.com": subdomains only (migrated rules)
)

type domainPat struct {
	kind int
	s    string
}

func (p domainPat) match(name string) bool {
	switch p.kind {
	case domExact:
		return name == p.s
	case domKeyword:
		return strings.Contains(name, p.s)
	case domSubOnly:
		return strings.HasSuffix(name, "."+p.s)
	default:
		return name == p.s || strings.HasSuffix(name, "."+p.s)
	}
}

// NormalizeDomain lower-cases a host name and drops a trailing dot.
func NormalizeDomain(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// ParseDomainValue parses a dst_domain leaf value.
func ParseDomainValue(v string) (domainPat, error) {
	v = strings.TrimSpace(v)
	var p domainPat
	switch {
	case strings.HasPrefix(v, "="):
		p = domainPat{domExact, NormalizeDomain(v[1:])}
	case strings.HasPrefix(v, "~"):
		p = domainPat{domKeyword, strings.ToLower(strings.TrimSpace(v[1:]))}
		if p.s == "" {
			return p, fmt.Errorf("empty keyword")
		}
		return p, nil
	case strings.HasPrefix(v, "*."):
		p = domainPat{domSubOnly, NormalizeDomain(v[2:])}
	default:
		p = domainPat{domSuffix, NormalizeDomain(strings.TrimPrefix(v, "."))}
	}
	if p.s == "" {
		return p, fmt.Errorf("empty domain")
	}
	if strings.ContainsAny(p.s, " /\\:@*") {
		return p, fmt.Errorf("invalid domain %q", v)
	}
	return p, nil
}

// ParseASNValue parses a dst_asn leaf value ("13335" or "AS13335").
func ParseASNValue(v string) (uint, error) {
	s := strings.TrimSpace(v)
	if len(s) > 2 && strings.EqualFold(s[:2], "as") {
		s = s[2:]
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid ASN %q", v)
	}
	return uint(n), nil
}

// NormalizeMAC lower-cases a MAC and uses ':' separators.
func NormalizeMAC(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", ":")
}

func validMAC(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return false
	}
	for _, p := range parts {
		if len(p) != 2 {
			return false
		}
		if _, err := strconv.ParseUint(p, 16, 8); err != nil {
			return false
		}
	}
	return true
}

func validKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

func validField(f string) bool {
	for _, x := range Fields {
		if x == f {
			return true
		}
	}
	return false
}

// leaf is a compiled match node.
type leaf struct {
	field   string
	ips     []ipRange
	ports   []portRange
	strs    map[string]bool
	domains []domainPat
	asns    map[uint]bool
}

// checkValue validates one value of field; the error message names the
// problem.
func checkValue(field, v string) error {
	_, err := compileValue(&leaf{field: field}, v)
	return err
}

// compileValue adds v to l.
func compileValue(l *leaf, v string) (*leaf, error) {
	switch l.field {
	case FieldSrcIP, FieldDstIP:
		lo, hi, err := ParseIPValue(v)
		if err != nil {
			return l, err
		}
		l.ips = append(l.ips, ipRange{lo, hi})
	case FieldSrcPort, FieldDstPort:
		lo, hi, err := ParsePortValue(v)
		if err != nil {
			return l, err
		}
		l.ports = append(l.ports, portRange{lo, hi})
	case FieldDstDomain:
		p, err := ParseDomainValue(v)
		if err != nil {
			return l, err
		}
		l.domains = append(l.domains, p)
	case FieldDstASN:
		n, err := ParseASNValue(v)
		if err != nil {
			return l, err
		}
		if l.asns == nil {
			l.asns = map[uint]bool{}
		}
		l.asns[n] = true
	default:
		s := strings.TrimSpace(v)
		switch l.field {
		case FieldDstGeo:
			s = strings.ToUpper(s)
			if len(s) != 2 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
				return l, fmt.Errorf("invalid country code %q", v)
			}
		case FieldSrcMAC:
			s = NormalizeMAC(s)
			if !validMAC(s) {
				return l, fmt.Errorf("invalid MAC %q", v)
			}
		case FieldSrcKind:
			s = strings.ToLower(s)
			if !validKind(s) {
				return l, fmt.Errorf("unknown source kind %q", v)
			}
		case FieldProto:
			s = strings.ToLower(s)
			if s != "tcp" && s != "udp" {
				return l, fmt.Errorf("proto must be tcp or udp")
			}
		}
		if s == "" {
			return l, fmt.Errorf("empty value")
		}
		if l.strs == nil {
			l.strs = map[string]bool{}
		}
		l.strs[s] = true
	}
	return l, nil
}

func matchIP(rs []ipRange, ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap().WithZone("")
	for _, r := range rs {
		if r.contains(ip) {
			return true
		}
	}
	return false
}

func matchPort(rs []portRange, p uint16, ok bool) bool {
	if !ok {
		return false
	}
	for _, r := range rs {
		if p >= r.lo && p <= r.hi {
			return true
		}
	}
	return false
}

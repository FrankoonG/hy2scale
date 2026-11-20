package mdns

import (
	"bytes"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	port      = 5353
	ttlNormal = 120
	ttlLegacy = 10
	topBit    = 0x8000 // cache-flush in records, QU in questions
	maxPerFam = 8      // address records per family in one message
)

const (
	typeA uint8 = 1 << iota
	typeAAAA
)

// fqdn returns name with exactly one trailing dot.
func fqdn(name string) string {
	return strings.TrimSuffix(name, ".") + "."
}

func sameName(n dnsmessage.Name, fq string) bool {
	return strings.EqualFold(n.String(), fq)
}

// split returns the addresses we publish, per family: IPv4 (any but
// loopback/unspecified) and global-unicast IPv6 (never link-local), sorted,
// at most maxPerFam each.
func split(addrs []netip.Addr) (v4, v6 []netip.Addr) {
	for _, a := range addrs {
		a = a.Unmap()
		switch {
		case !a.IsValid() || a.IsLoopback() || a.IsUnspecified() || a.IsMulticast():
		case a.Is4():
			v4 = append(v4, a)
		case a.IsGlobalUnicast():
			v6 = append(v6, a.WithZone(""))
		}
	}
	slices.SortFunc(v4, netip.Addr.Compare)
	slices.SortFunc(v6, netip.Addr.Compare)
	v4 = slices.Compact(v4)
	v6 = slices.Compact(v6)
	if len(v4) > maxPerFam {
		v4 = v4[:maxPerFam]
	}
	if len(v6) > maxPerFam {
		v6 = v6[:maxPerFam]
	}
	return v4, v6
}

func addRecords(b *dnsmessage.Builder, n dnsmessage.Name, addrs []netip.Addr, class dnsmessage.Class, ttl uint32) error {
	for _, a := range addrs {
		h := dnsmessage.ResourceHeader{Name: n, Class: class, TTL: ttl}
		var err error
		if a.Is4() {
			err = b.AResource(h, dnsmessage.AResource{A: a.As4()})
		} else {
			err = b.AAAAResource(h, dnsmessage.AAAAResource{AAAA: a.As16()})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type reply struct {
	msg     []byte
	unicast bool
	probe   bool  // the query was a probe (had authority records)
	types   uint8 // typeA|typeAAAA answered
}

// Answer is the socket-free core of the responder, exported for tests. It
// returns the reply to query for name (e.g. "sg-home.local") using addrs (the
// arrival interface's addresses; link-local IPv6 is dropped), whether to send
// it unicast to the querier, and ok=false when nothing should be sent.
func Answer(query []byte, name string, addrs []netip.Addr, srcPort int) (msg []byte, unicast bool, ok bool) {
	r, ok := answer(query, name, addrs, srcPort)
	return r.msg, r.unicast, ok
}

func answer(query []byte, name string, addrs []netip.Addr, srcPort int) (reply, bool) {
	fq := fqdn(name)
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil || h.Response || h.OpCode != 0 {
		return reply{}, false
	}
	qs, err := p.AllQuestions()
	if err != nil {
		return reply{}, false
	}
	legacy := srcPort != port
	var want uint8
	qu := false
	var echo []dnsmessage.Question
	for _, q := range qs {
		cls := q.Class &^ topBit
		if !sameName(q.Name, fq) || (cls != dnsmessage.ClassINET && cls != dnsmessage.ClassANY) {
			continue
		}
		var t uint8
		switch q.Type {
		case dnsmessage.TypeA:
			t = typeA
		case dnsmessage.TypeAAAA:
			t = typeAAAA
		case dnsmessage.TypeALL:
			t = typeA | typeAAAA
		default:
			continue
		}
		want |= t
		if q.Class&topBit != 0 {
			qu = true
		}
		echo = append(echo, dnsmessage.Question{Name: q.Name, Type: q.Type, Class: cls})
	}
	if want == 0 {
		return reply{}, false
	}
	known, err := p.AllAnswers()
	if err != nil {
		return reply{}, false
	}
	auth, err := p.AllAuthorities()
	if err != nil {
		return reply{}, false
	}
	probe := len(auth) > 0

	v4, v6 := split(addrs)
	if !legacy {
		// Known-answer suppression (RFC 6762 §7.1).
		for _, k := range known {
			if !sameName(k.Header.Name, fq) || k.Header.TTL < ttlNormal/2 {
				continue
			}
			switch b := k.Body.(type) {
			case *dnsmessage.AResource:
				v4 = slices.DeleteFunc(v4, func(a netip.Addr) bool { return a == netip.AddrFrom4(b.A) })
			case *dnsmessage.AAAAResource:
				v6 = slices.DeleteFunc(v6, func(a netip.Addr) bool { return a == netip.AddrFrom16(b.AAAA).Unmap() })
			}
		}
	}
	var ans, extra []netip.Addr
	var types uint8
	if want&typeA != 0 && len(v4) > 0 {
		ans, types = append(ans, v4...), types|typeA
	} else if want&typeA == 0 {
		extra = append(extra, v4...)
	}
	if want&typeAAAA != 0 && len(v6) > 0 {
		ans, types = append(ans, v6...), types|typeAAAA
	} else if want&typeAAAA == 0 {
		extra = append(extra, v6...)
	}
	if len(ans) == 0 {
		return reply{}, false
	}

	hdr := dnsmessage.Header{Response: true, Authoritative: true}
	class, ttl := dnsmessage.ClassINET|topBit, uint32(ttlNormal)
	if legacy {
		hdr.ID = h.ID
		class, ttl = dnsmessage.ClassINET, ttlLegacy
	}
	n, err := dnsmessage.NewName(fq)
	if err != nil {
		return reply{}, false
	}
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), hdr)
	b.EnableCompression()
	if legacy {
		if err := b.StartQuestions(); err != nil {
			return reply{}, false
		}
		for _, q := range echo {
			if err := b.Question(q); err != nil {
				return reply{}, false
			}
		}
	}
	if err := b.StartAnswers(); err != nil || addRecords(&b, n, ans, class, ttl) != nil {
		return reply{}, false
	}
	if !legacy && len(extra) > 0 {
		if err := b.StartAdditionals(); err != nil || addRecords(&b, n, extra, class, ttl) != nil {
			return reply{}, false
		}
	}
	msg, err := b.Finish()
	if err != nil {
		return reply{}, false
	}
	return reply{msg: msg, unicast: legacy || (qu && !probe), probe: probe, types: types}, true
}

// buildProbe builds a probe query (QTYPE ANY, QM) for name with our proposed
// records in the authority section.
func buildProbe(name string, addrs []netip.Addr) ([]byte, error) {
	n, err := dnsmessage.NewName(fqdn(name))
	if err != nil {
		return nil, err
	}
	v4, v6 := split(addrs)
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: n, Type: dnsmessage.TypeALL, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	if err := b.StartAuthorities(); err != nil {
		return nil, err
	}
	if err := addRecords(&b, n, append(v4, v6...), dnsmessage.ClassINET, ttlNormal); err != nil {
		return nil, err
	}
	return b.Finish()
}

// buildAnnounce builds an unsolicited response carrying our records; ttl 0
// makes it a goodbye (sent without the cache-flush bit).
func buildAnnounce(name string, addrs []netip.Addr, ttl uint32) ([]byte, error) {
	n, err := dnsmessage.NewName(fqdn(name))
	if err != nil {
		return nil, err
	}
	v4, v6 := split(addrs)
	class := dnsmessage.ClassINET | topBit
	if ttl == 0 {
		class = dnsmessage.ClassINET
	}
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{Response: true, Authoritative: true})
	b.EnableCompression()
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}
	if err := addRecords(&b, n, append(v4, v6...), class, ttl); err != nil {
		return nil, err
	}
	return b.Finish()
}

// addrRecords walks the answer, authority and additional sections of a parsed
// message (questions already consumed) and calls fn for every live A/AAAA
// record of class IN carrying name. fn returns true to stop. It returns
// false on a malformed message.
func addrRecords(p *dnsmessage.Parser, fq string, sections int, fn func(t dnsmessage.Type, a netip.Addr) bool) bool {
	type sec struct {
		hdr  func() (dnsmessage.ResourceHeader, error)
		skip func() error
	}
	all := []sec{
		{p.AnswerHeader, p.SkipAnswer},
		{p.AuthorityHeader, p.SkipAuthority},
		{p.AdditionalHeader, p.SkipAdditional},
	}
	for i, s := range all {
		want := sections&(1<<i) != 0
		for {
			h, err := s.hdr()
			if err == dnsmessage.ErrSectionDone {
				break
			}
			if err != nil {
				return false
			}
			match := want && sameName(h.Name, fq) && h.TTL > 0 && h.Class&^topBit == dnsmessage.ClassINET
			switch {
			case match && h.Type == dnsmessage.TypeA:
				r, err := p.AResource()
				if err != nil {
					return false
				}
				if fn(h.Type, netip.AddrFrom4(r.A)) {
					return true
				}
			case match && h.Type == dnsmessage.TypeAAAA:
				r, err := p.AAAAResource()
				if err != nil {
					return false
				}
				if fn(h.Type, netip.AddrFrom16(r.AAAA).Unmap()) {
					return true
				}
			default:
				if err := s.skip(); err != nil {
					return false
				}
			}
		}
	}
	return true
}

const (
	secAnswer = 1 << iota
	secAuthority
	secAdditional
)

// Conflicts reports whether msg is an mDNS response carrying a live A/AAAA
// record for name whose address is not in ours, i.e. someone else claims the
// name with different data. With ours nil, any such record counts. Exported
// for tests.
func Conflicts(msg []byte, name string, ours []netip.Addr) bool {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil || !h.Response || p.SkipAllQuestions() != nil {
		return false
	}
	found := false
	addrRecords(&p, fqdn(name), secAnswer|secAuthority|secAdditional, func(_ dnsmessage.Type, a netip.Addr) bool {
		for _, o := range ours {
			if o.Unmap().WithZone("") == a {
				return false
			}
		}
		found = true
		return true
	})
	return found
}

// LosesTiebreak reports whether msg is someone else's probe for name whose
// proposed records sort lexicographically after ours (RFC 6762 §8.2), in
// which case we must defer. ours are the addresses we probe with on the same
// interface. Exported for tests.
func LosesTiebreak(msg []byte, name string, ours []netip.Addr) bool {
	fq := fqdn(name)
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil || h.Response {
		return false
	}
	qs, err := p.AllQuestions()
	if err != nil || !slices.ContainsFunc(qs, func(q dnsmessage.Question) bool { return sameName(q.Name, fq) }) {
		return false
	}
	var theirs [][]byte
	ok := addrRecords(&p, fq, secAuthority, func(t dnsmessage.Type, a netip.Addr) bool {
		theirs = append(theirs, rdataKey(a))
		return false
	})
	if !ok || len(theirs) == 0 {
		return false
	}
	v4, v6 := split(ours)
	var mine [][]byte
	for _, a := range append(v4, v6...) {
		mine = append(mine, rdataKey(a))
	}
	slices.SortFunc(theirs, bytes.Compare)
	theirs = slices.CompactFunc(theirs, bytes.Equal)
	slices.SortFunc(mine, bytes.Compare)
	for i := 0; i < len(theirs) && i < len(mine); i++ {
		if c := bytes.Compare(theirs[i], mine[i]); c != 0 {
			return c > 0
		}
	}
	return len(theirs) > len(mine)
}

// rdataKey orders records as RFC 6762 §8.2 does: class (all IN), type, rdata.
func rdataKey(a netip.Addr) []byte {
	if a.Is4() {
		b := a.As4()
		return append([]byte{0, byte(dnsmessage.TypeA)}, b[:]...)
	}
	b := a.As16()
	return append([]byte{0, byte(dnsmessage.TypeAAAA)}, b[:]...)
}

// SanitizeLabel lowercases s, turns every run of characters outside [a-z0-9]
// into a single '-', trims '-' at both ends and caps the result at 63 bytes.
// It returns "" when nothing usable is left.
func SanitizeLabel(s string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(s) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(c)
			continue
		}
		dash = true
	}
	out := b.String()
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

// candidate returns the n-th name to try: label, label-2, label-3, ...
func candidate(label string, n int) string {
	if n <= 1 {
		return label
	}
	suf := "-" + strconv.Itoa(n)
	if len(label)+len(suf) > 63 {
		label = strings.TrimRight(label[:63-len(suf)], "-")
	}
	return label + suf
}

package dnsproxy

import (
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Records maps answered addresses back to the name that was asked for.
// An entry lives for its answer's TTL, held at least MinHold (clients keep
// using an address a while after its TTL) and at most MaxHold.
type Records struct {
	now     func() time.Time
	minHold time.Duration
	maxHold time.Duration
	max     int

	mu    sync.Mutex
	m     map[netip.Addr]record
	swept time.Time
}

type record struct {
	name string
	exp  time.Time
}

// Record store defaults.
const (
	DefaultMinHold    = 2 * time.Minute
	DefaultMaxHold    = 24 * time.Hour
	DefaultMaxRecords = 65536
)

// NewRecords returns a store (zero values = defaults; now nil = time.Now).
func NewRecords(now func() time.Time, minHold, maxHold time.Duration, max int) *Records {
	if now == nil {
		now = time.Now
	}
	if minHold <= 0 {
		minHold = DefaultMinHold
	}
	if maxHold <= 0 {
		maxHold = DefaultMaxHold
	}
	if maxHold < minHold {
		maxHold = minHold
	}
	if max <= 0 {
		max = DefaultMaxRecords
	}
	return &Records{now: now, minHold: minHold, maxHold: maxHold, max: max, m: map[netip.Addr]record{}}
}

// Add records that ip was handed out for name with ttl.
func (r *Records) Add(name string, ip netip.Addr, ttl time.Duration) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" || !ip.IsValid() {
		return
	}
	hold := ttl
	if hold < r.minHold {
		hold = r.minHold
	}
	if hold > r.maxHold {
		hold = r.maxHold
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[ip.Unmap()] = record{name: name, exp: now.Add(hold)}
	if len(r.m) > r.max {
		r.sweepLocked(now, true)
	} else if now.Sub(r.swept) > time.Minute {
		r.sweepLocked(now, false)
	}
}

// sweepLocked drops expired entries; force: also the soonest-expiring
// tenth when still over the limit.
func (r *Records) sweepLocked(now time.Time, force bool) {
	r.swept = now
	for ip, e := range r.m {
		if !now.Before(e.exp) {
			delete(r.m, ip)
		}
	}
	if !force || len(r.m) <= r.max {
		return
	}
	drop := len(r.m) - r.max + r.max/10
	for ip := range r.m { // map order: arbitrary victims
		if drop <= 0 {
			break
		}
		delete(r.m, ip)
		drop--
	}
}

// Lookup returns the name ip was last handed out for ("" = none or
// expired).
func (r *Records) Lookup(ip netip.Addr) string {
	ip = ip.Unmap()
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[ip]
	if !ok {
		return ""
	}
	if !r.now().Before(e.exp) {
		delete(r.m, ip)
		return ""
	}
	return e.name
}

// Len is the number of entries (expired ones included until swept).
func (r *Records) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.m)
}

// Record records the A/AAAA answers of a response under its question name
// (the name the client asked for, also when the answer went through
// CNAMEs).
func (r *Records) Record(resp []byte) {
	var p dnsmessage.Parser
	h, err := p.Start(resp)
	if err != nil || !h.Response || h.RCode != dnsmessage.RCodeSuccess {
		return
	}
	q, err := p.Question()
	if err != nil {
		return
	}
	name := q.Name.String()
	if err := p.SkipAllQuestions(); err != nil {
		return
	}
	for {
		ah, err := p.AnswerHeader()
		if err != nil {
			return
		}
		ttl := time.Duration(ah.TTL) * time.Second
		switch ah.Type {
		case dnsmessage.TypeA:
			a, err := p.AResource()
			if err != nil {
				return
			}
			r.Add(name, netip.AddrFrom4(a.A), ttl)
		case dnsmessage.TypeAAAA:
			a, err := p.AAAAResource()
			if err != nil {
				return
			}
			r.Add(name, netip.AddrFrom16(a.AAAA), ttl)
		default:
			if err := p.SkipAnswer(); err != nil {
				return
			}
		}
	}
}

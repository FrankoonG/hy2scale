package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dnsResolverDefaults centralises the fallback values applied when the
// operator leaves a DNSResolverConfig field at zero. Mirrored in the UI
// "Restore defaults" button so both sides agree on what "unset" means.
var dnsResolverDefaults = DNSResolverConfig{
	CacheTTL:       300,
	NegativeTTL:    30,
	CacheSize:      1024,
	QueryTimeoutMs: 3000,
}

// dnsResolverDefaultUpstreams is what we fall back to when cfg.DNS is
// empty. Mirrors the WireGuard-conf default (server.go) so both surfaces
// agree on "unset = these public resolvers".
const dnsResolverDefaultUpstreams = "1.1.1.1, 8.8.8.8"

// resolverConfigSnapshot is the runtime view of DNSResolverConfig with
// every zero replaced by its default. Held under an atomic.Pointer so
// updateUISettings can hot-swap it without locking the hot lookup path.
// Upstream list is NOT in the snapshot — it lives on cfg.DNS and is
// read live (same as l2tp/ikev2 do); a DNS-field save purges the cache.
type resolverConfigSnapshot struct {
	enabled      bool
	cacheTTL     time.Duration
	negativeTTL  time.Duration
	cacheSize    int
	queryTimeout time.Duration
}

func newResolverSnapshot(c DNSResolverConfig) *resolverConfigSnapshot {
	pick := func(v, d int) int {
		if v <= 0 {
			return d
		}
		return v
	}
	return &resolverConfigSnapshot{
		enabled:      c.IsEnabled(),
		cacheTTL:     time.Duration(pick(c.CacheTTL, dnsResolverDefaults.CacheTTL)) * time.Second,
		negativeTTL:  time.Duration(pick(c.NegativeTTL, dnsResolverDefaults.NegativeTTL)) * time.Second,
		cacheSize:    pick(c.CacheSize, dnsResolverDefaults.CacheSize),
		queryTimeout: time.Duration(pick(c.QueryTimeoutMs, dnsResolverDefaults.QueryTimeoutMs)) * time.Millisecond,
	}
}

// parseDNSUpstreams splits cfg.DNS (comma-separated, e.g.
// "8.8.8.8,1.1.1.1" or "8.8.8.8:53, [::1]:5353") into a list of
// host:port endpoints suitable for net.Dial. Missing port → :53.
// Empty input → the dnsResolverDefaultUpstreams baseline so the
// resolver still has a path even if the operator never set DNS.
func parseDNSUpstreams(s string) []string {
	if strings.TrimSpace(s) == "" {
		s = dnsResolverDefaultUpstreams
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(p); err != nil {
			p = net.JoinHostPort(p, "53")
		}
		out = append(out, p)
	}
	return out
}

// resolverActive holds the live snapshot. Never nil after init.
var resolverActive atomic.Pointer[resolverConfigSnapshot]

func init() {
	resolverActive.Store(newResolverSnapshot(DNSResolverConfig{}))
}

// ApplyDNSResolver hot-reloads the runtime snapshot and purges the
// cache. Called from updateUISettings so the operator's edits land
// without a process restart.
func ApplyDNSResolver(c DNSResolverConfig) {
	resolverActive.Store(newResolverSnapshot(c))
	resolverCache.purge()
}

func dnsResolverSnapshot() *resolverConfigSnapshot {
	s := resolverActive.Load()
	if s == nil {
		return newResolverSnapshot(DNSResolverConfig{})
	}
	return s
}

// dnsCacheEntry is one (exit, name) tuple's IPs. ips=nil + a TTL value
// = negative cache entry (recently failed, don't keep retrying).
type dnsCacheEntry struct {
	ips      []string
	expireAt time.Time
}

// dnsCache is a tiny LRU + TTL cache. Keyed by `<exit>/<name>` because
// the same name resolved through two different exits can legitimately
// return different IPs (CDN region selection, etc.). Cache MUST scope
// to exit identity or you get cross-pollination between rules that
// target the same domain via different peers.
type dnsCache struct {
	mu      sync.Mutex
	entries map[string]*dnsCacheEntry
	order   []string
}

func newDNSCache() *dnsCache {
	return &dnsCache{entries: make(map[string]*dnsCacheEntry)}
}

func (c *dnsCache) get(key string) (*dnsCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	for i, k := range c.order {
		if k == key {
			c.order = append(append([]string{}, c.order[:i]...), c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
	return e, time.Now().Before(e.expireAt)
}

func (c *dnsCache) set(key string, e *dnsCacheEntry, capacity int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = e
	for len(c.entries) > capacity && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *dnsCache) purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*dnsCacheEntry)
	c.order = nil
}

var resolverCache = newDNSCache()

// dnsLookupOverExit performs DNS queries for `name` (A records) by
// dialing each configured upstream THROUGH the rule's exit until one
// answers. The relay layer just sees a regular TCP target
// ("upstream:53") — no special peer support required, this works
// against any TCP-forwarding hy2-compatible peer including vanilla hy2
// and pre-1.3.4 hy2scale. The "DNS-via-exit" semantic is implemented
// purely by virtue of the TCP packet originating at the exit (so the
// upstream resolver sees a query from a clean source IP, not from the
// polluted entry's network).
//
// Upstream list comes from cfg.DNS — the same field that feeds
// VPN-client DNS push. One operator config knob.
func (a *App) dnsLookupOverExit(ctx context.Context, name, exitVia string) ([]string, error) {
	snap := dnsResolverSnapshot()
	upstreams := parseDNSUpstreams(a.store.Get().DNS)
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("no upstream DNS configured")
	}
	var lastErr error
	for _, ups := range upstreams {
		ips, err := a.dnsQueryOne(ctx, name, exitVia, ups, snap.queryTimeout)
		if err == nil {
			return ips, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// dnsQueryOne does a single TCP/53 lookup against `upstream` via the
// rule's exit. Returns the list of A-record IPs, or an error covering
// every failure mode (dial, write, framing, RCODE, no-answer).
func (a *App) dnsQueryOne(ctx context.Context, name, exitVia, upstream string, timeout time.Duration) ([]string, error) {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := a.dialExit(dialCtx, exitVia, upstream)
	if err != nil {
		return nil, fmt.Errorf("dial upstream %s via %s: %w", upstream, exitVia, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))

	var msg dnsmessage.Message
	msg.Header = dnsmessage.Header{ID: uint16(time.Now().UnixNano()), RecursionDesired: true}
	q, err := dnsmessage.NewName(dnsName(name))
	if err != nil {
		return nil, fmt.Errorf("encode name: %w", err)
	}
	msg.Questions = []dnsmessage.Question{{Name: q, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}
	wire, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	frame := make([]byte, 2+len(wire))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(wire)))
	copy(frame[2:], wire)
	if _, err := conn.Write(frame); err != nil {
		return nil, fmt.Errorf("write query: %w", err)
	}

	// io.ReadFull treats "read all bytes, then EOF" as success — which
	// is what happens when the relay-side TCP server closes the conn
	// after writing the reply.
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read response length: %w", err)
	}
	respLen := binary.BigEndian.Uint16(lenBuf[:])
	if respLen == 0 || respLen > 65535 {
		return nil, fmt.Errorf("bad response length %d", respLen)
	}
	respBuf := make([]byte, respLen)
	if _, err := io.ReadFull(conn, respBuf); err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(respBuf); err != nil {
		return nil, fmt.Errorf("unpack: %w", err)
	}
	if resp.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("rcode %s", resp.RCode.String())
	}

	var ips []string
	for _, ans := range resp.Answers {
		if ans.Header.Type != dnsmessage.TypeA {
			continue
		}
		ar, ok := ans.Body.(*dnsmessage.AResource)
		if !ok {
			continue
		}
		ips = append(ips, net.IP(ar.A[:]).String())
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no A records in response")
	}
	return ips, nil
}

func dnsName(s string) string {
	s = strings.TrimSuffix(s, ".")
	return s + "."
}

// ResolveViaExit is the public entry point. Returns IPs the way
// net.LookupHost does. Behaviour:
//
//   - if DNSResolver.Enabled == false, fall straight through to
//     net.LookupHost on this node (legacy / off-by-default path).
//   - else, dial the configured upstream resolver THROUGH the rule's
//     own `exitVia` and speak DNS-over-TCP. Each rule's DNS naturally
//     rides the same exit chain as its data plane — a rule with
//     exit_via=us resolves through us, a rule with exit_via=jp
//     resolves through jp. No global "DNS exit pin"; the per-rule
//     exit is the only routing dimension.
//   - the relay-layer plumbing is just plain TCP forwarding to
//     upstream:53, so this works against ANY hy2-compatible peer
//     (vanilla hy2 server, older hy2scale, etc.). No new wire
//     protocol is added — the "DNS via exit" semantic falls out
//     naturally from the upstream seeing the query coming from the
//     exit's clean IP rather than from the polluted entry.
//
// Cache key is `<exit>/<name>` so the same domain queried through
// different exits is cached independently — geo-DNS / CDN region
// answers stay distinct per route.
//
// Falls back to net.LookupHost if the relay query fails AND no fresh
// cache entry exists (stale-while-revalidate; an upstream blip won't
// abruptly flip a rule back to the polluted host resolver).
func (a *App) ResolveViaExit(ctx context.Context, name, exitVia string) ([]string, error) {
	snap := dnsResolverSnapshot()
	if !snap.enabled {
		return net.LookupHost(name)
	}
	if exitVia == "" {
		// Resolver enabled but caller passed no exit — there's no
		// remote side to ride, so fall back to local lookup. Covers
		// rules with empty exit_via (e.g. direct-to-internet entries
		// that bypass the relay entirely).
		return net.LookupHost(name)
	}

	cacheKey := exitVia + "/" + name
	if e, fresh := resolverCache.get(cacheKey); fresh {
		if e.ips == nil {
			return nil, fmt.Errorf("cached lookup failure for %s via %s", name, exitVia)
		}
		return append([]string(nil), e.ips...), nil
	}

	ips, err := a.dnsLookupOverExit(ctx, name, exitVia)
	if err != nil {
		// Stale-while-revalidate
		if e, _ := resolverCache.get(cacheKey); e != nil && e.ips != nil {
			log.Printf("[dns] %s via %s: lookup failed (%v), serving stale cache", name, exitVia, err)
			return append([]string(nil), e.ips...), nil
		}
		resolverCache.set(cacheKey, &dnsCacheEntry{ips: nil, expireAt: time.Now().Add(snap.negativeTTL)}, snap.cacheSize)
		log.Printf("[dns] %s via %s: lookup failed (%v), falling back to net.LookupHost", name, exitVia, err)
		return net.LookupHost(name)
	}
	resolverCache.set(cacheKey, &dnsCacheEntry{ips: ips, expireAt: time.Now().Add(snap.cacheTTL)}, snap.cacheSize)
	return append([]string(nil), ips...), nil
}

// PurgeDNSCache wipes every entry. Wired into updateUISettings so
// switching upstream takes effect on the next call rather than after
// the existing TTLs naturally expire.
func PurgeDNSCache() {
	resolverCache.purge()
}

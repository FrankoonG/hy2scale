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
	Upstream:           "1.1.1.1:53",
	CacheMinTTL:        30,
	CacheMaxTTL:        3600,
	CacheSize:          1024,
	NegativeTTL:        30,
	QueryTimeoutMs:     3000,
	RefreshIntervalSec: 60,
}

// resolverConfigSnapshot is the runtime view of DNSResolverConfig with
// every zero replaced by its default. Held under an atomic.Pointer so
// updateUISettings can hot-swap it without locking the hot lookup path.
type resolverConfigSnapshot struct {
	enabled         bool
	upstream        string
	exitVia         string
	cacheMinTTL     time.Duration
	cacheMaxTTL     time.Duration
	cacheSize       int
	negativeTTL     time.Duration
	queryTimeout    time.Duration
	refreshInterval time.Duration
}

func newResolverSnapshot(c DNSResolverConfig) *resolverConfigSnapshot {
	pick := func(v, d int) int {
		if v <= 0 {
			return d
		}
		return v
	}
	pickStr := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	return &resolverConfigSnapshot{
		enabled:         c.Enabled,
		upstream:        pickStr(c.Upstream, dnsResolverDefaults.Upstream),
		exitVia:         c.ExitVia,
		cacheMinTTL:     time.Duration(pick(c.CacheMinTTL, dnsResolverDefaults.CacheMinTTL)) * time.Second,
		cacheMaxTTL:     time.Duration(pick(c.CacheMaxTTL, dnsResolverDefaults.CacheMaxTTL)) * time.Second,
		cacheSize:       pick(c.CacheSize, dnsResolverDefaults.CacheSize),
		negativeTTL:     time.Duration(pick(c.NegativeTTL, dnsResolverDefaults.NegativeTTL)) * time.Second,
		queryTimeout:    time.Duration(pick(c.QueryTimeoutMs, dnsResolverDefaults.QueryTimeoutMs)) * time.Millisecond,
		refreshInterval: time.Duration(pick(c.RefreshIntervalSec, dnsResolverDefaults.RefreshIntervalSec)) * time.Second,
	}
}

// resolverActive holds the live snapshot. Never nil after init.
var resolverActive atomic.Pointer[resolverConfigSnapshot]

func init() {
	resolverActive.Store(newResolverSnapshot(DNSResolverConfig{}))
}

// ApplyDNSResolver is called by updateUISettings whenever the operator
// touches the DNS-resolver section. Hot-reloads the live snapshot and
// purges the cache so a new upstream/exitVia takes effect immediately
// rather than after the existing TTLs naturally expire.
func ApplyDNSResolver(c DNSResolverConfig) {
	resolverActive.Store(newResolverSnapshot(c))
	resolverCache.purge()
}

// DNSResolverActive returns the current live snapshot — the read side
// of the atomic-pointer hot-reload pattern.
func dnsResolverSnapshot() *resolverConfigSnapshot {
	s := resolverActive.Load()
	if s == nil {
		// Defensive: should never happen because of init() above.
		return newResolverSnapshot(DNSResolverConfig{})
	}
	return s
}

// dnsCacheEntry is one resolved name's IPs and the absolute time at which
// the entry expires. Negative caching uses ips=nil + expireAt.
type dnsCacheEntry struct {
	ips      []net.IP
	expireAt time.Time
}

// dnsCache is a tiny LRU + TTL cache. Lookup returns (entry, fresh) where
// fresh=true means the entry is still within its TTL. Stale entries are
// also returned (stale-while-revalidate) but the caller is expected to
// kick off an async refresh.
type dnsCache struct {
	mu      sync.Mutex
	entries map[string]*dnsCacheEntry
	order   []string // LRU order, head = oldest
}

func newDNSCache() *dnsCache {
	return &dnsCache{entries: make(map[string]*dnsCacheEntry)}
}

func (c *dnsCache) get(name string) (*dnsCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[name]
	if !ok {
		return nil, false
	}
	// Bubble to MRU
	for i, k := range c.order {
		if k == name {
			c.order = append(append([]string{}, c.order[:i]...), c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, name)
	return e, time.Now().Before(e.expireAt)
}

func (c *dnsCache) set(name string, e *dnsCacheEntry, capacity int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[name]; !exists {
		c.order = append(c.order, name)
	}
	c.entries[name] = e
	for len(c.entries) > capacity && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

// purge drops every cached entry. Called when the operator changes
// upstream / exit so old answers don't haunt the new config.
func (c *dnsCache) purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*dnsCacheEntry)
	c.order = nil
}

// resolverCache lives at app scope; one cache, lifetime = process.
var resolverCache = newDNSCache()

// dnsLookupOverExit performs one DNS query for `name` (A records) by
// opening a TCP/53 connection to the configured upstream THROUGH the
// relay (entry → exit → upstream). Returns the resolved IPs and the
// raw (un-clamped) TTL the upstream reported.
//
// `exitForRule` is the exit_via from the calling rule; the resolver
// either pins to its own ExitVia setting (if non-empty) or reuses the
// rule's exit so the DNS query travels the same chain as the data path.
func (a *App) dnsLookupOverExit(ctx context.Context, name, exitForRule string) ([]net.IP, time.Duration, error) {
	snap := dnsResolverSnapshot()
	if !snap.enabled {
		return nil, 0, fmt.Errorf("resolver disabled")
	}
	exitVia := snap.exitVia
	if exitVia == "" {
		exitVia = exitForRule
	}
	if exitVia == "" {
		return nil, 0, fmt.Errorf("no exit specified for DNS lookup")
	}

	dialCtx, cancel := context.WithTimeout(ctx, snap.queryTimeout)
	defer cancel()
	conn, err := a.dialExit(dialCtx, exitVia, snap.upstream)
	if err != nil {
		return nil, 0, fmt.Errorf("dial upstream %s via %s: %w", snap.upstream, exitVia, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(snap.queryTimeout))

	// Build A query
	var msg dnsmessage.Message
	msg.Header = dnsmessage.Header{ID: uint16(time.Now().UnixNano()), RecursionDesired: true}
	q, err := dnsmessage.NewName(dnsName(name))
	if err != nil {
		return nil, 0, fmt.Errorf("encode name: %w", err)
	}
	msg.Questions = []dnsmessage.Question{{Name: q, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}
	wire, err := msg.Pack()
	if err != nil {
		return nil, 0, fmt.Errorf("pack: %w", err)
	}
	// TCP/53 framing: 2-byte length prefix, big-endian.
	frame := make([]byte, 2+len(wire))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(wire)))
	copy(frame[2:], wire)
	if _, err := conn.Write(frame); err != nil {
		return nil, 0, fmt.Errorf("write query: %w", err)
	}

	// Read response. Use io.ReadFull rather than a hand-rolled loop —
	// io.ReadFull treats "got the bytes but the next read would EOF" as
	// success (returns nil), which is exactly what happens when the
	// upstream DNS server closes the TCP connection after writing the
	// reply (common, especially when the relay-side hysteria server
	// half-closes after the exit's outbound finishes).
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return nil, 0, fmt.Errorf("read response length: %w", err)
	}
	respLen := binary.BigEndian.Uint16(lenBuf[:])
	if respLen == 0 || respLen > 65535 {
		return nil, 0, fmt.Errorf("bad response length %d", respLen)
	}
	respBuf := make([]byte, respLen)
	if _, err := io.ReadFull(conn, respBuf); err != nil {
		return nil, 0, fmt.Errorf("read response body: %w", err)
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(respBuf); err != nil {
		return nil, 0, fmt.Errorf("unpack: %w", err)
	}
	if resp.RCode != dnsmessage.RCodeSuccess {
		return nil, 0, fmt.Errorf("rcode %s", resp.RCode.String())
	}

	var ips []net.IP
	var minTTL uint32 = ^uint32(0)
	for _, ans := range resp.Answers {
		if ans.Header.Type != dnsmessage.TypeA {
			continue
		}
		ar, ok := ans.Body.(*dnsmessage.AResource)
		if !ok {
			continue
		}
		ips = append(ips, net.IP(ar.A[:]))
		if ans.Header.TTL < minTTL {
			minTTL = ans.Header.TTL
		}
	}
	if minTTL == ^uint32(0) {
		minTTL = 0
	}
	return ips, time.Duration(minTTL) * time.Second, nil
}

func dnsName(s string) string {
	s = strings.TrimSuffix(s, ".")
	return s + "."
}

// ResolveViaExit is the public entry point. Returns IPs the way
// net.LookupHost does, but uses the relay-routed resolver when enabled
// in config. Caller passes `exitForRule` so the DNS query naturally
// travels the same exit chain as the data it's resolving for (unless
// DNSResolver.ExitVia is set, in which case that pin wins).
//
// Falls back to net.LookupHost when:
//   - DNSResolverConfig.Enabled == false
//   - the upstream call fails (any reason — unreachable, timeout,
//     SERVFAIL, …) and no fresh cache entry exists. Stale cache wins
//     over fallback so a transient blip doesn't flip every rule back
//     to the polluted host resolver.
func (a *App) ResolveViaExit(ctx context.Context, name, exitForRule string) ([]string, error) {
	snap := dnsResolverSnapshot()
	if !snap.enabled {
		return net.LookupHost(name)
	}

	if e, fresh := resolverCache.get(name); fresh {
		return ipStrings(e.ips), nilOrEmptyErr(e)
	}

	ips, ttl, err := a.dnsLookupOverExit(ctx, name, exitForRule)
	if err != nil {
		// Stale-while-revalidate: prefer stale cache hit over a
		// fallback to the polluted local resolver.
		if e, _ := resolverCache.get(name); e != nil && e.ips != nil {
			log.Printf("[dns] %s: upstream failed (%v), serving stale cache", name, err)
			return ipStrings(e.ips), nil
		}
		// Negative-cache: don't hammer upstream on a bad name.
		resolverCache.set(name, &dnsCacheEntry{ips: nil, expireAt: time.Now().Add(snap.negativeTTL)}, snap.cacheSize)
		// As a last resort, try the host resolver — better to leak
		// to local DNS once than to break the rule entirely. The
		// operator already opted IN to relay-resolved DNS so this is
		// a soft fallback for diagnostic clarity, not a regression.
		log.Printf("[dns] %s: upstream failed (%v), falling back to net.LookupHost", name, err)
		return net.LookupHost(name)
	}

	// Clamp TTL into [min, max].
	if ttl < snap.cacheMinTTL {
		ttl = snap.cacheMinTTL
	}
	if ttl > snap.cacheMaxTTL {
		ttl = snap.cacheMaxTTL
	}
	resolverCache.set(name, &dnsCacheEntry{ips: ips, expireAt: time.Now().Add(ttl)}, snap.cacheSize)
	return ipStrings(ips), nil
}

func ipStrings(ips []net.IP) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}

func nilOrEmptyErr(e *dnsCacheEntry) error {
	if e.ips == nil {
		return fmt.Errorf("no answer")
	}
	return nil
}

// PurgeDNSCache wipes every entry. Wired into updateUISettings so
// switching upstream/exitVia takes effect on the next call rather than
// after the existing TTLs naturally expire.
func PurgeDNSCache() {
	resolverCache.purge()
}

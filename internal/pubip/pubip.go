// Package pubip detects the public (external) IPv4 or IPv6 address seen by
// the internet when connecting through a specific interface.
//
// The caller supplies the dial and resolve functions, so the probe traffic
// (including DNS) leaves through whatever interface or user-space stack the
// caller binds them to. The package only ever asks for the requested family:
// STUN probes dial "udp4"/"udp6", HTTP probes dial "tcp4"/"tcp6", and DNS is
// resolved with "ip4"/"ip6". There is never a fallback to the other family.
//
// Server entries:
//
//	stun:host:port        RFC 5389/8489 Binding request (port defaults to 3478)
//	https://host/path     plain-text IP or Cloudflare-trace style "ip=" body
//	http://host/path      same, without TLS (meant for private/test servers)
package pubip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/core/sockmark"
)

// DialFunc dials a connection. network is always one of tcp4, tcp6, udp4,
// udp6 and address is an "ip:port" literal of the matching family.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// ResolveFunc resolves host to addresses. network is "ip4" or "ip6".
type ResolveFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)

// Result is the outcome of one Detect call.
type Result struct {
	IP      netip.Addr // detected address; invalid when Err != nil
	Server  string     // server entry that answered (or the last one tried on failure)
	Err     error
	Checked time.Time
}

// PerServerTimeout bounds the time spent on a single server entry
// (resolution, dial and exchange). The overall Detect call is additionally
// bounded by its ctx.
var PerServerTimeout = 4 * time.Second

// stunRetransmit is the delay before the single STUN retransmission.
const stunRetransmit = 1500 * time.Millisecond

// maxAddrsPerServer limits how many resolved addresses of one host are tried.
const maxAddrsPerServer = 2

// DefaultServers is the built-in server list, tried in order. Global services
// come first, China-oriented ones last; a failure moves on to the next entry.
// Family-specific hosts are fine: an IPv4-only host simply fails (no AAAA)
// for family 6 and is skipped.
var DefaultServers = []string{
	// Cloudflare anycast STUN; A + AAAA. Fastest answer in practice.
	"stun:stun.cloudflare.com:3478",
	// Google public STUN, a second operator for UDP; A + AAAA.
	"stun:stun.l.google.com:19302",
	// Cloudflare trace over HTTPS: TCP/443 fallback where UDP is filtered;
	// A + AAAA; body is key=value lines with "ip=".
	"https://www.cloudflare.com/cdn-cgi/trace",
	// ipify (not Cloudflare-hosted, keeps HTTP from a single operator);
	// api64 is the dual-stack name; plain-text body.
	"https://api64.ipify.org",
	// China-oriented entries last.
	// Xiaomi STUN; IPv4 only (no AAAA, skipped for family 6).
	"stun:stun.miwifi.com:3478",
	// Mango TV (hitv.com) STUN; A + AAAA, a CN STUN with IPv6.
	"stun:stun.hitv.com:3478",
	// DNSPod (Tencent) per-family plain-text endpoints, as used by DDNS
	// clients; ipv4.* has only A, ipv6.* only AAAA.
	"https://ipv4.ddnspod.com",
	"https://ipv6.ddnspod.com",
}

// Detect tries servers in order and returns the first valid address of the
// requested family (4 or 6). If every server fails, the last error is
// returned. dial and resolve may be nil, in which case the system dialer and
// resolver are used (no interface binding).
func Detect(ctx context.Context, family int, dial DialFunc, resolve ResolveFunc, servers []string) Result {
	res := Result{}
	if family != 4 && family != 6 {
		res.Err = fmt.Errorf("pubip: invalid family %d", family)
		res.Checked = time.Now()
		return res
	}
	if len(servers) == 0 {
		res.Err = errors.New("pubip: no servers")
		res.Checked = time.Now()
		return res
	}
	if dial == nil {
		dial = sockmark.Dialer(net.Dialer{}).DialContext
	}
	if resolve == nil {
		resolve = net.DefaultResolver.LookupNetIP
	}
	p := &prober{family: family, dial: dial, resolve: resolve}
	var lastErr error
	for _, s := range servers {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("%w (last: %v)", err, lastErr)
			}
			break
		}
		res.Server = s
		sctx, cancel := context.WithTimeout(ctx, PerServerTimeout)
		ip, err := p.probe(sctx, s)
		cancel()
		if err == nil {
			res.IP = ip
			res.Err = nil
			res.Checked = time.Now()
			return res
		}
		lastErr = fmt.Errorf("pubip: %s: %w", s, err)
	}
	res.Err = lastErr
	res.Checked = time.Now()
	return res
}

type prober struct {
	family  int
	dial    DialFunc
	resolve ResolveFunc
}

func (p *prober) suffix() string {
	if p.family == 6 {
		return "6"
	}
	return "4"
}

func (p *prober) probe(ctx context.Context, server string) (netip.Addr, error) {
	switch {
	case strings.HasPrefix(server, "stun:"):
		return p.probeSTUN(ctx, strings.TrimPrefix(server, "stun:"))
	case strings.HasPrefix(server, "https://"), strings.HasPrefix(server, "http://"):
		return p.probeHTTP(ctx, server)
	default:
		return netip.Addr{}, fmt.Errorf("unsupported server entry")
	}
}

// lookup returns up to maxAddrsPerServer addresses of the probe family for
// host. IP literals are accepted only when they match the family.
func (p *prober) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		if !p.familyOK(a) {
			return nil, fmt.Errorf("address %s is not IPv%d", a, p.family)
		}
		return []netip.Addr{a}, nil
	}
	addrs, err := p.resolve(ctx, "ip"+p.suffix(), host)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		a = a.Unmap()
		if p.familyOK(a) {
			out = append(out, a)
			if len(out) == maxAddrsPerServer {
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no IPv%d address for %s", p.family, host)
	}
	return out, nil
}

func (p *prober) familyOK(a netip.Addr) bool {
	if p.family == 4 {
		return a.Is4()
	}
	return a.Is6() && !a.Is4In6()
}

// validAnswer reports whether a is a usable public-address answer of the
// probe family.
func (p *prober) validAnswer(a netip.Addr) error {
	if !a.IsValid() {
		return errors.New("invalid address in answer")
	}
	a = a.Unmap()
	if !p.familyOK(a) {
		return fmt.Errorf("answer %s is not IPv%d", a, p.family)
	}
	if a.Zone() != "" || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() ||
		a.IsLinkLocalUnicast() || a.IsInterfaceLocalMulticast() {
		return fmt.Errorf("answer %s is not a usable public address", a)
	}
	return nil
}

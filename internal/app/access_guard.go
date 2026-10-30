package app

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/FrankoonG/hy2scale/core/proto"
)

// Restrictions on what a flow may reach from the node that dials it. A
// user's restrictions ride on the dial context here; flows that leave
// through another node carry them as a tag on the target (proto.Restrict),
// which a 1.4.0+ exit applies to its own dial.

type restrictKey struct{}

// withRestrict adds r to the restrictions carried by ctx.
func withRestrict(ctx context.Context, r proto.Restrict) context.Context {
	if r == 0 {
		return ctx
	}
	return context.WithValue(ctx, restrictKey{}, restrictOf(ctx)|r)
}

// restrictOf: the restrictions carried by ctx.
func restrictOf(ctx context.Context) proto.Restrict {
	r, _ := ctx.Value(restrictKey{}).(proto.Restrict)
	return r
}

// userRestrict: the restrictions of a user's flows.
func (a *App) userRestrict(username string) proto.Restrict {
	a.usersMu.RLock()
	u, ok := a.userIndex[username]
	a.usersMu.RUnlock()
	if ok && u.DenyInternal {
		return proto.DenyInternal
	}
	return 0
}

var errInternalDenied = errors.New("destination is an internal address")

// fixedInternal: loopback, private, link-local, shared and other
// non-global ranges.
var fixedInternal = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
)

// localNets caches this node's own addresses and on-link prefixes (system
// interfaces and child interfaces).
type localNets struct {
	mu     sync.Mutex
	at     time.Time
	addrs  map[netip.Addr]bool
	prefix []netip.Prefix
}

func (a *App) localNets() (map[netip.Addr]bool, []netip.Prefix) {
	ln := &a.lnets
	ln.mu.Lock()
	defer ln.mu.Unlock()
	if time.Since(ln.at) < 10*time.Second && ln.addrs != nil {
		return ln.addrs, ln.prefix
	}
	addrs := map[netip.Addr]bool{}
	var pfx []netip.Prefix
	if ias, err := net.InterfaceAddrs(); err == nil {
		for _, ia := range ias {
			if ipn, ok := ia.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
					ones, _ := ipn.Mask.Size()
					ip = ip.Unmap()
					addrs[ip] = true
					if p, err := ip.Prefix(ones); err == nil {
						pfx = append(pfx, p)
					}
				}
			}
		}
	}
	if a.ifaces != nil {
		for _, c := range a.store.Get().Interfaces {
			i := a.ifaces.child(c.Name)
			if i == nil {
				continue
			}
			st := i.Status()
			for _, ai := range append(st.IPv4.Addrs, st.IPv6.Addrs...) {
				addrs[ai.Prefix.Addr().Unmap()] = true
				pfx = append(pfx, ai.Prefix.Masked())
			}
		}
	}
	// this node's public addresses behind NAT
	if a.ifaces != nil && a.ifaces.pub != nil {
		p := a.ifaces.pub
		p.mu.Lock()
		for _, r := range p.results {
			if r.IP.IsValid() {
				addrs[r.IP.Unmap()] = true
			}
		}
		p.mu.Unlock()
	}
	ln.addrs, ln.prefix, ln.at = addrs, pfx, time.Now()
	return addrs, pfx
}

// isInternal: ip is not a public destination as seen from this node — a
// non-global range, one of this node's own addresses or networks, or a
// destination the system routes specifically (a LAN, an explicit route).
func (a *App) isInternal(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && nat64Prefix.Contains(ip) {
		b := ip.As16()
		ip = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	for _, p := range fixedInternal {
		if p.Contains(ip) {
			return true
		}
	}
	addrs, pfx := a.localNets()
	if addrs[ip] {
		return true
	}
	for _, p := range pfx {
		if p.Contains(ip) {
			return true
		}
	}
	return systemRoutes(ip)
}

// checkTarget refuses ip when ctx denies internal destinations.
func (a *App) checkTarget(ctx context.Context, ip netip.Addr) error {
	if restrictOf(ctx)&proto.DenyInternal != 0 && a.isInternal(ip) {
		return errInternalDenied
	}
	return nil
}

// targetControl is a net.Dialer Control hook applying checkTarget to the
// address actually dialled (after name resolution); nil when ctx carries
// no restriction.
func (a *App) targetControl(ctx context.Context) func(network, address string, c syscall.RawConn) error {
	if restrictOf(ctx)&proto.DenyInternal == 0 {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return errInternalDenied
		}
		return a.checkTarget(ctx, ap.Addr())
	}
}

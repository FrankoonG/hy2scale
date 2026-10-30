package iface

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/FrankoonG/hy2scale/core/proto"
	"github.com/FrankoonG/hy2scale/core/vnic"
)

// Restrictions on this node's own dials: a flow that may not reach
// internal destinations (proto.DenyInternal) is refused one before any
// socket is opened.

// ErrInternalDenied: a restricted flow tried an internal destination.
var ErrInternalDenied = errors.New("destination is an internal address")

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

func (m *Manager) localNets() (map[netip.Addr]bool, []netip.Prefix) {
	ln := &m.lnets
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
	m.mu.Lock()
	var kids []*vnic.Iface
	for _, r := range m.children {
		if r.iface != nil {
			kids = append(kids, r.iface)
		}
	}
	own := m.OwnAddrs
	m.mu.Unlock()
	for _, i := range kids {
		st := i.Status()
		for _, ai := range append(st.IPv4.Addrs, st.IPv6.Addrs...) {
			addrs[ai.Prefix.Addr().Unmap()] = true
			pfx = append(pfx, ai.Prefix.Masked())
		}
	}
	// this node's addresses known elsewhere (public addresses behind NAT)
	if own != nil {
		for _, ip := range own() {
			addrs[ip.Unmap()] = true
		}
	}
	ln.addrs, ln.prefix, ln.at = addrs, pfx, time.Now()
	return addrs, pfx
}

// IsInternal: ip is not a public destination as seen from this node — a
// non-global range, one of this node's own addresses or networks, or a
// destination the system routes specifically (a LAN, an explicit route).
func (m *Manager) IsInternal(ip netip.Addr) bool {
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
	addrs, pfx := m.localNets()
	if addrs[ip] {
		return true
	}
	for _, p := range pfx {
		if p.Contains(ip) {
			return true
		}
	}
	return SystemRoutes(ip)
}

// checkTarget refuses ip when ctx denies internal destinations.
func (m *Manager) checkTarget(ctx context.Context, ip netip.Addr) error {
	if proto.RestrictFrom(ctx)&proto.DenyInternal != 0 && m.IsInternal(ip) {
		return ErrInternalDenied
	}
	return nil
}

// targetControl is a net.Dialer Control hook applying checkTarget to the
// address actually dialled (after name resolution); nil when ctx carries
// no restriction.
func (m *Manager) targetControl(ctx context.Context) func(network, address string, c syscall.RawConn) error {
	if proto.RestrictFrom(ctx)&proto.DenyInternal == 0 {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return ErrInternalDenied
		}
		return m.checkTarget(ctx, ap.Addr())
	}
}

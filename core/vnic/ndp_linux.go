//go:build linux

package vnic

import (
	"net/netip"
	"sort"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// ndpState records what the netstack learns from Router Advertisements
// (SLAAC addresses, default routers, on-link prefixes, RDNSS). Its callbacks
// must not call into the stack, so they only record and kick the control loop.
type ndpState struct {
	kick  func()
	clock tcpip.Clock

	mu      sync.Mutex
	seq     int
	addrs   map[tcpip.Address]*slaacAddr
	routers []tcpip.Address
	onlink  map[tcpip.Subnet]bool
	rdnss   []rdnssEntry
	dup     map[tcpip.Address]bool
	dadOK   map[tcpip.Address]bool
	ra      bool
}

type slaacAddr struct {
	awp   tcpip.AddressWithPrefix
	seq   int
	lt    stack.AddressLifetimes
	state stack.AddressAssignmentState
}

type rdnssEntry struct {
	addr    tcpip.Address
	expires time.Time // zero = no expiry
}

var _ ipv6.NDPDispatcher = (*ndpState)(nil)

func (d *ndpState) changed() {
	if d.kick != nil {
		d.kick()
	}
}

func (d *ndpState) OnDuplicateAddressDetectionResult(_ tcpip.NICID, a tcpip.Address, res stack.DADResult) {
	d.mu.Lock()
	if d.dup == nil {
		d.dup, d.dadOK = map[tcpip.Address]bool{}, map[tcpip.Address]bool{}
	}
	delete(d.dup, a)
	delete(d.dadOK, a)
	switch res.(type) {
	case *stack.DADDupAddrDetected:
		d.dup[a] = true
	case *stack.DADSucceeded:
		d.dadOK[a] = true
	}
	d.mu.Unlock()
	d.changed()
}

func (d *ndpState) OnOffLinkRouteUpdated(_ tcpip.NICID, sn tcpip.Subnet, router tcpip.Address, _ header.NDPRoutePreference) {
	d.mu.Lock()
	d.ra = true
	if sn.Prefix() == 0 {
		found := false
		for _, r := range d.routers {
			if r == router {
				found = true
			}
		}
		if !found {
			d.routers = append(d.routers, router)
		}
	}
	d.mu.Unlock()
	d.changed()
}

func (d *ndpState) OnOffLinkRouteInvalidated(_ tcpip.NICID, sn tcpip.Subnet, router tcpip.Address) {
	d.mu.Lock()
	if sn.Prefix() == 0 {
		for k, r := range d.routers {
			if r == router {
				d.routers = append(d.routers[:k], d.routers[k+1:]...)
				break
			}
		}
	}
	d.mu.Unlock()
	d.changed()
}

func (d *ndpState) OnOnLinkPrefixDiscovered(_ tcpip.NICID, sn tcpip.Subnet) {
	d.mu.Lock()
	d.ra = true
	if d.onlink == nil {
		d.onlink = map[tcpip.Subnet]bool{}
	}
	d.onlink[sn] = true
	d.mu.Unlock()
	d.changed()
}

func (d *ndpState) OnOnLinkPrefixInvalidated(_ tcpip.NICID, sn tcpip.Subnet) {
	d.mu.Lock()
	delete(d.onlink, sn)
	d.mu.Unlock()
	d.changed()
}

type slaacAddrDisp struct {
	d *ndpState
	a tcpip.Address
}

func (s slaacAddrDisp) OnChanged(lt stack.AddressLifetimes, st stack.AddressAssignmentState) {
	s.d.mu.Lock()
	if e := s.d.addrs[s.a]; e != nil {
		e.lt, e.state = lt, st
	}
	s.d.mu.Unlock()
	s.d.changed()
}

func (s slaacAddrDisp) OnRemoved(stack.AddressRemovalReason) {
	s.d.mu.Lock()
	delete(s.d.addrs, s.a)
	s.d.mu.Unlock()
	s.d.changed()
}

func (d *ndpState) OnAutoGenAddress(_ tcpip.NICID, awp tcpip.AddressWithPrefix) stack.AddressDispatcher {
	d.mu.Lock()
	d.ra = true
	if d.addrs == nil {
		d.addrs = map[tcpip.Address]*slaacAddr{}
	}
	d.seq++
	d.addrs[awp.Address] = &slaacAddr{awp: awp, seq: d.seq, state: stack.AddressTentative}
	d.mu.Unlock()
	d.changed()
	return slaacAddrDisp{d: d, a: awp.Address}
}

func (d *ndpState) OnAutoGenAddressDeprecated(_ tcpip.NICID, awp tcpip.AddressWithPrefix) {
	d.mu.Lock()
	if e := d.addrs[awp.Address]; e != nil {
		e.lt.Deprecated = true
	}
	d.mu.Unlock()
	d.changed()
}

func (d *ndpState) OnAutoGenAddressInvalidated(_ tcpip.NICID, awp tcpip.AddressWithPrefix) {
	d.mu.Lock()
	delete(d.addrs, awp.Address)
	d.mu.Unlock()
	d.changed()
}

func (d *ndpState) OnRecursiveDNSServerOption(_ tcpip.NICID, addrs []tcpip.Address, lifetime time.Duration) {
	d.mu.Lock()
	for _, a := range addrs {
		k := -1
		for j, e := range d.rdnss {
			if e.addr == a {
				k = j
			}
		}
		if lifetime == 0 {
			if k >= 0 {
				d.rdnss = append(d.rdnss[:k], d.rdnss[k+1:]...)
			}
			continue
		}
		var exp time.Time
		if lifetime < 100*365*24*time.Hour {
			exp = time.Now().Add(lifetime)
		}
		if k >= 0 {
			d.rdnss[k].expires = exp
		} else {
			d.rdnss = append(d.rdnss, rdnssEntry{addr: a, expires: exp})
		}
	}
	d.mu.Unlock()
	d.changed()
}

func (d *ndpState) OnDNSSearchListOption(tcpip.NICID, []string, time.Duration) {}

func (d *ndpState) OnDHCPv6Configuration(tcpip.NICID, ipv6.DHCPv6ConfigurationFromNDPRA) {}

// ---------------------------------------------------------------- queries

func toNetip(a tcpip.Address) netip.Addr {
	n, _ := netip.AddrFromSlice(a.AsSlice())
	return n
}

// addrInfos returns the assigned global SLAAC addresses, preferred ones first.
func (d *ndpState) addrInfos() []AddrInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	var list []*slaacAddr
	for _, e := range d.addrs {
		if e.state != stack.AddressAssigned || header.IsV6LinkLocalUnicastAddress(e.awp.Address) {
			continue
		}
		list = append(list, e)
	}
	sort.Slice(list, func(a, b int) bool {
		if list[a].lt.Deprecated != list[b].lt.Deprecated {
			return !list[a].lt.Deprecated
		}
		return list[a].seq < list[b].seq
	})
	var now tcpip.MonotonicTime
	if d.clock != nil {
		now = d.clock.NowMonotonic()
	}
	out := make([]AddrInfo, 0, len(list))
	for _, e := range list {
		ai := AddrInfo{Prefix: netip.PrefixFrom(toNetip(e.awp.Address), e.awp.PrefixLen), Source: "slaac"}
		if e.lt.ValidUntil != (tcpip.MonotonicTime{}) && d.clock != nil {
			if left := e.lt.ValidUntil.Sub(now); left > 0 && left < 100*365*24*time.Hour {
				ai.Expires = time.Now().Add(left).Truncate(time.Second)
			}
		}
		out = append(out, ai)
	}
	return out
}

func (d *ndpState) router() netip.Addr {
	if d == nil {
		return netip.Addr{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.routers) == 0 {
		return netip.Addr{}
	}
	return toNetip(d.routers[0])
}

func (d *ndpState) dnsServers() []netip.Addr {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []netip.Addr
	now := time.Now()
	keep := d.rdnss[:0]
	for _, e := range d.rdnss {
		if !e.expires.IsZero() && now.After(e.expires) {
			continue
		}
		keep = append(keep, e)
		out = append(out, toNetip(e.addr))
	}
	d.rdnss = keep
	return out
}

func (d *ndpState) onlinkPrefixes() []netip.Prefix {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []netip.Prefix
	for sn := range d.onlink {
		out = append(out, netip.PrefixFrom(toNetip(sn.ID()), sn.Prefix()))
	}
	return out
}

func (d *ndpState) sawRA() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ra
}

// dadDone reports whether DAD for a has succeeded.
func (d *ndpState) dadDone(a tcpip.Address) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dadOK[a]
}

func (d *ndpState) dupes() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for a := range d.dup {
		return toNetip(a).String()
	}
	return ""
}

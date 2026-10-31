package iface

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

// ---------------------------------------------------------------- routes

// SystemRoutes reports whether the system routes ip specifically (loopback,
// link-local, multicast, or a main-table route more specific than the
// default) — such destinations bypass the egress.
func SystemRoutes(ip netip.Addr) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	r := readRoutes()
	for _, p := range r.specific {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// routeInfo is a snapshot of the main routing table.
type routeInfo struct {
	specific []netip.Prefix    // non-default routes
	gateway4 map[string]string // iface → default gateway
	gateway6 map[string]string
}

var routeCache struct {
	mu   sync.Mutex
	at   time.Time
	info *routeInfo
}

// readRoutes reads the main routing table (cached 3 s).
func readRoutes() *routeInfo {
	routeCache.mu.Lock()
	defer routeCache.mu.Unlock()
	if routeCache.info != nil && time.Since(routeCache.at) < 3*time.Second {
		return routeCache.info
	}
	routeCache.info, routeCache.at = loadRoutes(), time.Now()
	return routeCache.info
}

// probeTargets: addresses whose route stands for "the internet".
var probeTargets = map[int]string{4: "1.1.1.1:53", 6: "[2606:4700:4700::1111]:53"}

var defIfaceCache struct {
	mu   sync.Mutex
	at   [7]time.Time
	name [7]string
}

// DefaultRouteIface is the interface the system routes the internet
// through for fam, honouring policy routing ("" = none). No packet is sent.
// Cached for 3 s.
func DefaultRouteIface(fam int) string {
	defIfaceCache.mu.Lock()
	defer defIfaceCache.mu.Unlock()
	if time.Since(defIfaceCache.at[fam]) < 3*time.Second {
		return defIfaceCache.name[fam]
	}
	n := lookupDefaultIface(fam)
	defIfaceCache.at[fam], defIfaceCache.name[fam] = time.Now(), n
	return n
}

func lookupDefaultIface(fam int) string {
	c, err := net.Dial(FamilyNet("udp", fam), probeTargets[fam])
	if err != nil {
		return ""
	}
	src := c.LocalAddr().(*net.UDPAddr).IP
	c.Close()
	ifs, _ := net.Interfaces()
	for _, ifi := range ifs {
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(src) {
				return ifi.Name
			}
		}
	}
	return ""
}

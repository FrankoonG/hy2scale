package mdns

import (
	"net"
	"net/netip"
	"slices"
	"strings"
)

// skipPrefixes are virtual / container interfaces we never answer on.
var skipPrefixes = []string{
	"docker", "veth", "virbr", "tun", "tap", "utun", "wg", "tailscale", "zt",
	"lo", "cni", "flannel", "cali", "vxlan", "kube",
}

// skipName reports whether an interface is virtual by its name. "br-" only
// matches Docker's per-network bridges (br-<12 hex>), so a LAN bridge such as
// OpenWrt's br-lan is still eligible.
func skipName(name string) bool {
	for _, p := range skipPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	if rest, ok := strings.CutPrefix(name, "br-"); ok && len(rest) == 12 {
		return strings.Trim(rest, "0123456789abcdef") == ""
	}
	return false
}

type ifaceInfo struct {
	ifi    net.Interface
	addrs  []netip.Addr // published: IPv4 + global IPv6, sorted
	has4   bool
	j4, j6 bool // group joined per family
}

// scanInterfaces returns the interfaces to serve, keyed by index.
func scanInterfaces(skip func(string) bool) map[int]*ifaceInfo {
	out := map[int]*ifaceInfo{}
	ifs, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if skipName(ifi.Name) || (skip != nil && skip(ifi.Name)) {
			continue
		}
		as, err := ifi.Addrs()
		if err != nil {
			continue
		}
		var raw []netip.Addr
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					raw = append(raw, ip.Unmap())
				}
			}
		}
		v4, v6 := split(raw)
		if len(v4) == 0 && len(v6) == 0 {
			continue
		}
		out[ifi.Index] = &ifaceInfo{ifi: ifi, addrs: append(v4, v6...), has4: len(v4) > 0}
	}
	return out
}

func sameAddrs(a, b []netip.Addr) bool { return slices.Equal(a, b) }

package capture

import (
	"net"
	"net/netip"
	"sort"
)

// LocalNets lists the subnets of this host's interfaces (sorted CIDRs,
// both families): loopback interfaces, link-local subnets and host
// addresses (/32, /128, which addrtype LOCAL covers) are left out.
func LocalNets() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			p, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := p.Addr().Unmap()
			if ip.IsLinkLocalUnicast() || ip.IsLoopback() || p.Bits() >= ip.BitLen() {
				continue
			}
			s := netip.PrefixFrom(ip, p.Bits()).Masked().String()
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

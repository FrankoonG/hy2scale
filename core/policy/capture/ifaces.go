package capture

import (
	"sort"
	"strings"
)

// FilterInterfaces applies the interface list filter of §3.2: no
// loopback, no interface hy2scale owns (own, nil = none), no veth
// enslaved to a bridge (listed as the bridge's members instead), nothing
// down or without an address.
func FilterInterfaces(raw []RawIface, own func(name string) bool) []Interface {
	members := map[string][]string{}
	for _, r := range raw {
		if r.Master != "" {
			members[r.Master] = append(members[r.Master], r.Name)
		}
	}
	out := []Interface{}
	for _, r := range raw {
		switch {
		case r.Loopback || r.Type == "loopback":
			continue
		case IsOwnIface(r.Name) || (own != nil && own(r.Name)):
			continue
		case r.Type == "veth" && r.Master != "":
			continue
		case !r.Up || len(r.Addrs) == 0:
			continue
		}
		typ := r.Type
		if typ == "" {
			typ = "ether"
		}
		it := Interface{Name: r.Name, Type: typ, Addrs: append([]string{}, r.Addrs...), Up: r.Up}
		if m := members[r.Name]; len(m) > 0 {
			sort.Strings(m)
			it.Members = m
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// IsOwnIface: names hy2scale itself gives kernel interfaces (its
// capability probe bridge, pre-1.4.0 TUN devices).
func IsOwnIface(name string) bool {
	return strings.HasPrefix(name, "hy2") || strings.HasPrefix(name, "h2s")
}

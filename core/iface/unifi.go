package iface

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// UniFi OS describes its network in udapi-net-cfg.json: the front-panel
// ports (which the kernel may show as VLANs on a switch CPU port), the
// switch, bridges (LAN networks) and the WANs. On such a gateway the
// interface view follows that model instead of guessing from sysfs.

// UnifiNetConfigPath is where UniFi OS keeps its network configuration
// (HY2SCALE_UNIFI_NETCFG overrides it).
func UnifiNetConfigPath() string {
	if p := os.Getenv("HY2SCALE_UNIFI_NETCFG"); p != "" {
		return p
	}
	return "/data/udapi-config/udapi-net-cfg.json"
}

// UnifiNet is the part of a UniFi network configuration the interface view
// uses.
type UnifiNet struct {
	types    map[string]string // interface id → UniFi type (ethernet, switch, vlan, bridge, …)
	switched map[string]bool   // ports of a switch
	bridged  map[string]bool   // members of a bridge
	vlanOf   map[string]string // vlan interface → the interface it sits on
	wan      map[string]bool
}

type udapiNetCfg struct {
	Interfaces []struct {
		Identification struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"identification"`
		Switch *struct {
			Ports []struct {
				Interface struct {
					ID string `json:"id"`
				} `json:"interface"`
			} `json:"ports"`
		} `json:"switch"`
		Bridge *struct {
			Interfaces []struct {
				ID string `json:"id"`
			} `json:"interfaces"`
		} `json:"bridge"`
		VLAN *struct {
			Interface struct {
				ID string `json:"id"`
			} `json:"interface"`
		} `json:"vlan"`
		Addresses []struct {
			Origin string `json:"origin"`
		} `json:"addresses"`
	} `json:"interfaces"`
	Services struct {
		WanFailover *struct {
			WanInterfaces []struct {
				Interface string `json:"interface"`
			} `json:"wanInterfaces"`
		} `json:"wanFailover"`
	} `json:"services"`
}

// ParseUnifiNet reads a udapi-net-cfg.json document.
func ParseUnifiNet(b []byte) (*UnifiNet, error) {
	var c udapiNetCfg
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	u := &UnifiNet{types: map[string]string{}, switched: map[string]bool{}, bridged: map[string]bool{},
		vlanOf: map[string]string{}, wan: map[string]bool{}}
	dynamic := map[string]bool{}
	for _, it := range c.Interfaces {
		id := it.Identification.ID
		if id == "" {
			continue
		}
		u.types[id] = it.Identification.Type
		if it.Switch != nil {
			for _, p := range it.Switch.Ports {
				u.switched[p.Interface.ID] = true
			}
		}
		if it.Bridge != nil {
			for _, m := range it.Bridge.Interfaces {
				u.bridged[m.ID] = true
			}
		}
		if it.VLAN != nil && it.VLAN.Interface.ID != "" {
			u.vlanOf[id] = it.VLAN.Interface.ID
		}
		for _, a := range it.Addresses {
			switch a.Origin {
			case "dhcp", "pppoe", "dhcpv6", "slaac":
				dynamic[id] = true
			}
		}
	}
	if wf := c.Services.WanFailover; wf != nil && len(wf.WanInterfaces) > 0 {
		// The list also holds UniFi's own VPN clients (wgclt*), which are
		// not in the interface table: only Ethernet-side WANs count.
		for _, w := range wf.WanInterfaces {
			if _, ok := u.types[w.Interface]; ok {
				u.wan[w.Interface] = true
			}
		}
	} else {
		// No WAN list: an interface taking its address from upstream
		// (DHCP, PPPoE, SLAAC) outside any bridge is a WAN.
		for id := range dynamic {
			if !u.bridged[id] && u.types[id] != "bridge" {
				u.wan[id] = true
			}
		}
	}
	return u, nil
}

// Managed reports whether UniFi OS configures the interface.
func (u *UnifiNet) Managed(name string) bool { _, ok := u.types[name]; return ok }

// Port: a front-panel Ethernet port.
func (u *UnifiNet) Port(name string) bool { return u.types[name] == "ethernet" }

// Internal: plumbing between the switch and the bridges (the switch's CPU
// port, VLANs that only join a bridge), not a network of its own.
func (u *UnifiNet) Internal(name string) bool {
	switch u.types[name] {
	case "switch":
		return true
	case "vlan":
		return u.bridged[name]
	}
	return false
}

// Role: "wan" or "lan" (a bridge), "" otherwise.
func (u *UnifiNet) Role(name string) string {
	switch {
	case u.wan[name]:
		return "wan"
	case u.types[name] == "bridge":
		return "lan"
	}
	return ""
}

// Parent: a child interface may sit on name — a WAN, the port under a WAN
// VLAN, or a port that is neither a switch port nor a bridge member. LAN
// bridges are not offered (a child there is untested).
func (u *UnifiNet) Parent(name string) bool {
	if u.wan[name] {
		return true
	}
	if !u.Port(name) || u.switched[name] || u.bridged[name] {
		return false
	}
	return true
}

var unifiCache struct {
	mu    sync.Mutex
	at    time.Time
	path  string
	mtime time.Time
	net   *UnifiNet
}

// unifiNet returns this gateway's UniFi network configuration, nil when the
// node is not on UniFi OS (or the file can't be read). Re-read when the
// file changes; checked at most every 3 s.
func unifiNet() *UnifiNet {
	unifiCache.mu.Lock()
	defer unifiCache.mu.Unlock()
	p := UnifiNetConfigPath()
	if p == unifiCache.path && time.Since(unifiCache.at) < 3*time.Second {
		return unifiCache.net
	}
	unifiCache.at, unifiCache.path = time.Now(), p
	st, err := os.Stat(p) // follows the symlink to the current version
	if err != nil {
		unifiCache.net, unifiCache.mtime = nil, time.Time{}
		return nil
	}
	if unifiCache.net != nil && st.ModTime().Equal(unifiCache.mtime) {
		return unifiCache.net
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return unifiCache.net
	}
	u, err := ParseUnifiNet(b)
	if err != nil {
		return unifiCache.net // keep the last good one
	}
	unifiCache.net, unifiCache.mtime = u, st.ModTime()
	return u
}

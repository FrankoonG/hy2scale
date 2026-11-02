package iface

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"syscall"

	"github.com/FrankoonG/hy2scale/core/vnic"
)

// IfaceAddr is one address of an interface.
type IfaceAddr struct {
	Prefix  string `json:"prefix"`
	Source  string `json:"source"` // system | dhcp | static | slaac
	Expires int64  `json:"expires"`
}

// IfaceFamilyView is one family of an interface as the API shows it.
type IfaceFamilyView struct {
	Mode     string      `json:"mode"`  // system | off | dhcp | static | slaac
	State    string      `json:"state"` // ok | acquiring | failed | off
	Error    string      `json:"error"`
	Addrs    []IfaceAddr `json:"addrs"`
	Gateway  string      `json:"gateway"`
	DNS      []string    `json:"dns"`
	Hostname string      `json:"hostname"`
	ClientID string      `json:"client_id"`
}

// PublicView is a detected public address.
type PublicView struct {
	IP      string `json:"ip"`
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Checked int64  `json:"checked"`
	Direct  bool   `json:"direct"`
}

// Usable says whether an interface can carry a family's direct flows.
type Usable struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"` // family_off | no_address | no_route | down
}

// IfaceView is one interface in GET /api/interfaces.
type IfaceView struct {
	Name            string                 `json:"name"`
	Kind            string                 `json:"kind"` // physical | system | child
	Role            string                 `json:"role,omitempty"` // UniFi OS: wan | lan
	Parent          string                 `json:"parent"`
	Container       bool                   `json:"container"`
	Type            string                 `json:"type,omitempty"` // link type (vnic.LinkType); child interfaces: ""
	Mode            string                 `json:"mode,omitempty"`
	Enabled         *bool                  `json:"enabled,omitempty"`
	VLAN            *VLANConfig            `json:"vlan,omitempty"`
	MTU             int                    `json:"mtu"`
	Up              bool                   `json:"up"`
	Error           string                 `json:"error,omitempty"`
	MAC             string                 `json:"mac"`
	Wireless        bool                   `json:"wireless"`
	CanHaveChildren bool                   `json:"can_have_children"`
	IPv4            IfaceFamilyView        `json:"ipv4"`
	IPv6            IfaceFamilyView        `json:"ipv6"`
	Public          map[string]*PublicView `json:"public"`
	EgressUsable    map[string]Usable      `json:"egress_usable"`
	RxBytes         uint64                 `json:"rx_bytes"`
	TxBytes         uint64                 `json:"tx_bytes"`
}

// InterfacesView is GET /api/interfaces.
type InterfacesView struct {
	Capability struct {
		Children bool   `json:"children"`
		Reason   string `json:"reason"`
	} `json:"capability"`
	Egress     EgressConfig `json:"egress"`
	Interfaces []IfaceView  `json:"interfaces"`
}

// isContainerIface: interfaces Docker / CNI create for containers.
func isContainerIface(name string) bool {
	return strings.HasPrefix(name, "veth") || name == "docker0" ||
		(strings.HasPrefix(name, "br-") && len(name) == 15) ||
		strings.HasPrefix(name, "cni") || strings.HasPrefix(name, "flannel")
}

// systemIfaceView describes a kernel interface.
func (m *Manager) systemIfaceView(ifi net.Interface, routes *routeInfo) IfaceView {
	v := IfaceView{
		Name: ifi.Name, Kind: "system", MTU: ifi.MTU, MAC: ifi.HardwareAddr.String(),
		Up:        ifi.Flags&net.FlagUp != 0,
		Container: isContainerIface(ifi.Name),
		Wireless:  vnic.IsWireless(ifi.Name),
		Type:      vnic.LinkType(ifi.Name),
		Public:    map[string]*PublicView{"ipv4": nil, "ipv6": nil},
	}
	if CanParent(ifi.Name) {
		v.Kind = "physical"
		v.CanHaveChildren = true
	}
	if u := unifiNet(); u != nil && u.Managed(ifi.Name) {
		// UniFi OS: its own model of ports, switch and networks.
		switch {
		case u.Port(ifi.Name):
			v.Kind, v.Type = "physical", "ethernet"
		case u.Internal(ifi.Name):
			v.Kind = "system"
		}
		v.Role = u.Role(ifi.Name)
	}
	v.IPv4 = IfaceFamilyView{Mode: "system", State: "ok", Addrs: []IfaceAddr{}, DNS: []string{}}
	v.IPv6 = IfaceFamilyView{Mode: "system", State: "ok", Addrs: []IfaceAddr{}, DNS: []string{}}
	addrs, _ := ifi.Addrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		ones, _ := ipn.Mask.Size()
		ad := IfaceAddr{Prefix: fmt.Sprintf("%s/%d", ipn.IP, ones), Source: "system"}
		if ipn.IP.To4() != nil {
			v.IPv4.Addrs = append(v.IPv4.Addrs, ad)
		} else {
			v.IPv6.Addrs = append(v.IPv6.Addrs, ad)
		}
	}
	v.IPv4.Gateway = routes.gateway4[ifi.Name]
	v.IPv6.Gateway = routes.gateway6[ifi.Name]
	for _, fv := range []*IfaceFamilyView{&v.IPv4, &v.IPv6} {
		if len(fv.Addrs) == 0 {
			fv.State = "off"
		}
	}
	v.EgressUsable = map[string]Usable{
		"ipv4": kernelUsable(ifi, 4, len(v.IPv4.Addrs) > 0),
		"ipv6": kernelUsable(ifi, 6, len(v.IPv6.Addrs) > 0),
	}
	return v
}

func kernelUsable(ifi net.Interface, fam int, hasAddr bool) Usable {
	switch {
	case ifi.Flags&net.FlagUp == 0:
		return Usable{Reason: "down"}
	case ifi.Flags&net.FlagLoopback != 0:
		return Usable{Reason: "no_route"}
	case !hasAddr:
		return Usable{Reason: "no_address"}
	case !kernelRoutes(ifi.Name, fam):
		return Usable{Reason: "no_route"}
	}
	return Usable{OK: true}
}

// childView describes a child interface.
func (m *Manager) childView(c InterfaceConfig, i *vnic.Iface, startErr error) IfaceView {
	enabled := c.Enabled
	vlan := c.VLAN
	v := IfaceView{
		Name: c.Name, Kind: "child", Parent: c.Parent, Mode: c.Mode, Enabled: &enabled, VLAN: &vlan,
		MTU: c.MTU, MAC: c.MAC, Wireless: vnic.IsWireless(c.Parent),
		Public: map[string]*PublicView{"ipv4": nil, "ipv6": nil},
	}
	fam := func(cf IfaceFamily, st vnic.FamilyStatus) IfaceFamilyView {
		fv := IfaceFamilyView{Mode: cf.Mode, State: "off", Addrs: []IfaceAddr{}, DNS: []string{},
			Hostname: cf.Hostname, ClientID: cf.ClientID}
		if cf.Mode == "off" || !c.Enabled {
			if !c.Enabled && cf.Mode != "off" {
				for _, a := range cf.Addrs {
					fv.Addrs = append(fv.Addrs, IfaceAddr{Prefix: a, Source: "static"})
				}
				fv.Gateway, fv.DNS = cf.Gateway, append(fv.DNS, cf.DNS...)
			}
			return fv
		}
		fv.State, fv.Error = st.State, st.Error
		if fv.State == "" {
			fv.State = "acquiring"
		}
		if cf.Mode == "static" {
			// the configured values, in order (the edit dialog reads them)
			for _, a := range cf.Addrs {
				fv.Addrs = append(fv.Addrs, IfaceAddr{Prefix: a, Source: "static"})
			}
			fv.Gateway, fv.DNS = cf.Gateway, append(fv.DNS, cf.DNS...)
			return fv
		}
		for _, a := range st.Addrs {
			var exp int64
			if !a.Expires.IsZero() {
				exp = a.Expires.Unix()
			}
			fv.Addrs = append(fv.Addrs, IfaceAddr{Prefix: a.Prefix.String(), Source: a.Source, Expires: exp})
		}
		if st.Gateway.IsValid() {
			fv.Gateway = st.Gateway.String()
		}
		for _, d := range st.DNS {
			fv.DNS = append(fv.DNS, d.String())
		}
		return fv
	}
	var st vnic.Status
	if i != nil {
		st = i.Status()
		v.Up, v.Error = st.Up, st.Error
		v.RxBytes, v.TxBytes = st.RxBytes, st.TxBytes
	} else if startErr != nil {
		v.Error = startErr.Error()
	}
	v.IPv4 = fam(c.IPv4, st.IPv4)
	v.IPv6 = fam(c.IPv6, st.IPv6)
	usable := func(cf IfaceFamily, fv IfaceFamilyView) Usable {
		switch {
		case cf.Mode == "off":
			return Usable{Reason: "family_off"}
		case !c.Enabled || i == nil || !v.Up:
			return Usable{Reason: "down"}
		case len(fv.Addrs) == 0 || fv.State != "ok":
			return Usable{Reason: "no_address"}
		}
		return Usable{OK: true}
	}
	v.EgressUsable = map[string]Usable{"ipv4": usable(c.IPv4, v.IPv4), "ipv6": usable(c.IPv6, v.IPv6)}
	return v
}

// View is GET /api/interfaces.
func (m *Manager) View() InterfacesView {
	var out InterfacesView
	m.mu.Lock()
	children := m.cfgs
	fill := m.Fill
	m.mu.Unlock()
	if fill == nil {
		fill = func(*IfaceView) {}
	}
	out.Egress = m.Selection()
	routes := readRoutes()
	ifs, _ := net.Interfaces()
	var phys string
	for _, ifi := range ifs {
		v := m.systemIfaceView(ifi, routes)
		fill(&v)
		out.Interfaces = append(out.Interfaces, v)
		if v.CanHaveChildren && phys == "" {
			phys = ifi.Name
		}
	}
	m.mu.Lock()
	runs := map[string]*childRun{}
	for k, r := range m.children {
		runs[k] = r
	}
	m.mu.Unlock()
	for _, c := range children {
		var i *vnic.Iface
		var err error
		if r := runs[c.Name]; r != nil {
			i, err = r.iface, r.err
		}
		v := m.childView(c, i, err)
		fill(&v)
		out.Interfaces = append(out.Interfaces, v)
	}
	sortIfaces(out.Interfaces, routes)
	switch {
	case phys == "":
		// typically a container on a bridge network
		out.Capability.Reason = "not_host_network"
	default:
		if err := vnic.CanCreate(phys); err != nil {
			out.Capability.Reason = err.Error()
			if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) {
				out.Capability.Reason = "no_net_raw"
			}
		} else {
			out.Capability.Children = true
		}
	}
	return out
}

// sortIfaces: physical NICs, then interfaces with a default route, then
// the rest; container interfaces last; children right after their parent
// (the client nests them).
func sortIfaces(v []IfaceView, routes *routeInfo) {
	rank := func(x IfaceView) int {
		switch {
		case x.Kind == "child":
			return 5
		case x.Kind == "physical":
			return 0
		case x.Container:
			return 4
		case routes.gateway4[x.Name] != "" || routes.gateway6[x.Name] != "":
			return 1
		case len(x.IPv4.Addrs)+len(x.IPv6.Addrs) > 0:
			return 2
		}
		return 3
	}
	sort.SliceStable(v, func(i, j int) bool {
		ri, rj := rank(v[i]), rank(v[j])
		if ri != rj {
			return ri < rj
		}
		return v[i].Name < v[j].Name
	})
}

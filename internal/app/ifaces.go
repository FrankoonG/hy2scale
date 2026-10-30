package app

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/FrankoonG/hy2scale/core/iface"
	"github.com/FrankoonG/hy2scale/internal/publicip"
	"github.com/FrankoonG/hy2scale/internal/pubip"
)

// Interfaces: the system's own interfaces (read only) and the child
// interfaces hy2scale runs on physical NICs with their own MAC. A built-in
// child exists only inside this process (packet socket + userspace stack);
// nothing is added to the system. See docs/interfaces-design.md.

// Interface configuration types (core/iface).
type (
	InterfaceConfig = iface.InterfaceConfig
	VLANConfig      = iface.VLANConfig
	IfaceFamily     = iface.IfaceFamily
	EgressConfig    = iface.EgressConfig
	IfaceAddr       = iface.IfaceAddr
	IfaceFamilyView = iface.IfaceFamilyView
	PublicView      = iface.PublicView
	Usable          = iface.Usable
	IfaceView       = iface.IfaceView
	InterfacesView  = iface.InterfacesView
)

// PublicIPConfig: how the public address of an interface is detected
// (the publicip add-on).
type PublicIPConfig = publicip.Config

// ifaceManager: the interfaces (core/iface) and their public addresses.
type ifaceManager struct {
	*iface.Manager
	a   *App
	pub *publicip.Detector
}

// pubHost: the public-address add-on's view of this node.
type pubHost struct{ a *App }

func (h pubHost) Config() *publicip.Config { return h.a.store.Get().PublicIP }

func (h pubHost) Children() []iface.InterfaceConfig { return h.a.store.Get().Interfaces }

func (h pubHost) PeerAddr(ctx context.Context, name string, fam int) pubip.Result {
	return h.a.peerReportedAddr(ctx, name, fam)
}

func newIfaceManager(a *App) *ifaceManager {
	m := &ifaceManager{Manager: iface.New(filepath.Join(a.dataDir, "ifaces")), a: a}
	m.pub = publicip.New(m.Manager, pubHost{a})
	m.OnChange = m.pub.Kick
	m.OwnAddrs = m.pub.Addrs
	m.Fill = m.pub.Fill
	return m
}

// apply brings the interfaces in line with cfg.
func (m *ifaceManager) apply(cfg Config) {
	m.SetEgress(cfg.Egress)
	m.Apply(cfg.Interfaces)
}

// ---------------------------------------------------------------- mutations

// IfaceRef is something that uses an interface.
type IfaceRef struct {
	Type    string `json:"type"` // egress | node_address
	Family  string `json:"family,omitempty"`
	Node    string `json:"node,omitempty"`
	Address string `json:"address,omitempty"`
}

// refsTo lists what in cfg uses interface name.
func refsTo(cfg *Config, name string) []IfaceRef {
	refs := []IfaceRef{}
	if cfg.Egress != nil {
		if cfg.Egress.IPv4 == name {
			refs = append(refs, IfaceRef{Type: "egress", Family: "ipv4"})
		}
		if cfg.Egress.IPv6 == name {
			refs = append(refs, IfaceRef{Type: "egress", Family: "ipv6"})
		}
	}
	for _, cl := range cfg.Clients {
		for _, a := range clientAddrList(cl) {
			if _, b, err := splitAddrBind(a); err == nil && b.iface == name {
				refs = append(refs, IfaceRef{Type: "node_address", Node: cl.Name, Address: a})
			}
		}
		if cl.Punch != nil {
			for _, bd := range cl.Punch.Binds {
				if bindIface(bd) == name {
					refs = append(refs, IfaceRef{Type: "node_address", Node: cl.Name, Address: "@punch#" + bd})
				}
			}
		}
	}
	return refs
}

// clientAddrList: a node's configured addresses (primary first).
func clientAddrList(cl ClientEntry) []string {
	if len(cl.Addrs) > 0 {
		return cl.Addrs
	}
	if cl.Addr != "" {
		return []string{cl.Addr}
	}
	return nil
}

// bindIface: the interface of a punch binding "iface" / "iface/src" / "/src".
func bindIface(bd string) string {
	if i := strings.IndexByte(bd, '/'); i >= 0 {
		return bd[:i]
	}
	return bd
}

// rebind rewrites a binding suffix from interface old to repl.
func rebind(addr, old, repl string) string {
	i := strings.LastIndexByte(addr, '#')
	if i < 0 {
		return addr
	}
	suf := addr[i+1:]
	ifc, rest := suf, ""
	if j := strings.IndexByte(suf, '/'); j >= 0 {
		ifc, rest = suf[:j], suf[j:]
	}
	if ifc != old {
		return addr
	}
	return addr[:i+1] + repl + rest
}

// InterfaceRefs lists what uses an interface.
func (a *App) InterfaceRefs(name string) []IfaceRef {
	cfg := a.store.Get()
	return refsTo(&cfg, name)
}

// Interfaces is GET /api/interfaces.
func (a *App) Interfaces() InterfacesView { return a.ifaces.View() }

// CreateInterface adds a child interface.
func (a *App) CreateInterface(c InterfaceConfig) (InterfaceConfig, error) {
	var err error
	a.store.Update(func(cfg *Config) {
		if err = c.Validate(cfg.Interfaces, true); err != nil {
			return
		}
		cfg.Interfaces = append(cfg.Interfaces, c)
	})
	if err != nil {
		return c, err
	}
	a.ifaces.apply(a.store.Get())
	return c, nil
}

// UpdateInterface changes a child interface. A new parent moves it to that
// NIC; a new name renames it, and everything that referred to the old name
// (egress, node addresses, punch bindings) follows. Empty name / parent
// keep the current ones.
func (a *App) UpdateInterface(name string, c InterfaceConfig) (InterfaceConfig, error) {
	if c.Name == "" {
		c.Name = name
	}
	var refs []IfaceRef
	err := fmt.Errorf("no interface %q", name)
	a.store.Update(func(cfg *Config) {
		for i := range cfg.Interfaces {
			if cfg.Interfaces[i].Name != name {
				continue
			}
			if c.Parent == "" {
				c.Parent = cfg.Interfaces[i].Parent
			}
			if err = c.Validate(cfg.Interfaces, false); err != nil {
				return
			}
			if c.Name != name {
				if err = iface.NameFree(cfg.Interfaces, c.Name); err != nil {
					return
				}
				refs = refsTo(cfg, name)
				moveIfaceRefs(cfg, name, c.Name)
			}
			cfg.Interfaces[i] = c
			return
		}
	})
	if err != nil {
		return c, err
	}
	if c.Name != name {
		// Keep the DHCP lease and other saved state under the new name.
		a.ifaces.RenameState(name, c.Name)
	}
	a.ifaces.apply(a.store.Get())
	a.restartRefNodes(refs)
	return c, nil
}

// moveIfaceRefs points everything that uses interface from at to.
func moveIfaceRefs(cfg *Config, from, to string) {
	if cfg.Egress != nil {
		if cfg.Egress.IPv4 == from {
			cfg.Egress.IPv4 = to
		}
		if cfg.Egress.IPv6 == from {
			cfg.Egress.IPv6 = to
		}
	}
	for ci := range cfg.Clients {
		cl := &cfg.Clients[ci]
		cl.Addr = rebind(cl.Addr, from, to)
		for k := range cl.Addrs {
			cl.Addrs[k] = rebind(cl.Addrs[k], from, to)
		}
		if cl.Punch != nil {
			for k, bd := range cl.Punch.Binds {
				if bindIface(bd) == from {
					cl.Punch.Binds[k] = to + bd[len(from):]
				}
			}
		}
	}
}

// restartRefNodes reconnects the nodes whose addresses were rebound.
func (a *App) restartRefNodes(refs []IfaceRef) {
	restarted := map[string]bool{}
	for _, r := range refs {
		if r.Type != "node_address" || restarted[r.Node] {
			continue
		}
		restarted[r.Node] = true
		for _, cl := range a.store.Get().Clients {
			if cl.Name == r.Node && !cl.Disabled {
				a.StopClient(cl.Name)
				a.StartClient(cl)
			}
		}
	}
}

// DeleteInterface removes a child interface; everything that used it uses
// replacement (another existing interface) instead.
func (a *App) DeleteInterface(name, replacement string) ([]IfaceRef, error) {
	if replacement == "" || replacement == name {
		return nil, fmt.Errorf("a replacement interface is required")
	}
	if !a.interfaceExists(replacement) {
		return nil, fmt.Errorf("no interface %q", replacement)
	}
	var refs []IfaceRef
	err := fmt.Errorf("no interface %q", name)
	a.store.Update(func(cfg *Config) {
		idx := -1
		for i := range cfg.Interfaces {
			if cfg.Interfaces[i].Name == name {
				idx = i
			}
		}
		if idx < 0 {
			return
		}
		err = nil
		refs = refsTo(cfg, name)
		cfg.Interfaces = append(cfg.Interfaces[:idx], cfg.Interfaces[idx+1:]...)
		moveIfaceRefs(cfg, name, replacement)
	})
	if err != nil {
		return nil, err
	}
	a.ifaces.apply(a.store.Get())
	a.restartRefNodes(refs)
	return refs, nil
}

// interfaceExists: a kernel interface or a configured child.
func (a *App) interfaceExists(name string) bool {
	if _, err := net.InterfaceByName(name); err == nil {
		return true
	}
	for _, c := range a.store.Get().Interfaces {
		if c.Name == name {
			return true
		}
	}
	return false
}

// RenewInterface renews a child's DHCP lease.
func (a *App) RenewInterface(name string) error {
	return a.ifaces.Renew(name)
}

// RefreshPublicIP re-detects an interface's public addresses now.
func (a *App) RefreshPublicIP(name string) {
	a.ifaces.pub.DetectNow(name)
}

// isChildIface reports whether name is a configured child interface.
func (a *App) isChildIface(name string) bool { return a.ifaces.IsChild(name) }

// BindableInterfaces: what a node address can be bound to — the system's
// interfaces and the child interfaces.
func (a *App) BindableInterfaces() []IfaceInfo {
	out := SystemInterfaces()
	for _, c := range a.store.Get().Interfaces {
		info := IfaceInfo{Name: c.Name, Kind: "child", Parent: c.Parent, Addrs: []string{}}
		if i := a.ifaces.Child(c.Name); i != nil {
			st := i.Status()
			info.Up = st.Up
			for _, ad := range append(st.IPv4.Addrs, st.IPv6.Addrs...) {
				info.Addrs = append(info.Addrs, ad.Prefix.Addr().String())
			}
		}
		out = append(out, info)
	}
	return out
}

// installChildListen lets node addresses bound to a child interface open
// their UDP socket on it.
func (a *App) installChildListen() { childListen = a.ifaces.ChildListen }

// SelfEgress reports this node's egress (the publicip add-on).
func (a *App) SelfEgress() publicip.SelfEgress { return a.ifaces.pub.Self() }

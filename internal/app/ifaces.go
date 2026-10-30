package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/FrankoonG/hy2scale/internal/vnic"
)

// Interfaces: the system's own interfaces (read only) and the child
// interfaces hy2scale runs on physical NICs with their own MAC. A built-in
// child exists only inside this process (packet socket + userspace stack);
// nothing is added to the system. See docs/interfaces-design.md.

// InterfaceConfig is a child interface.
type InterfaceConfig struct {
	Name    string      `yaml:"name" json:"name"`
	Parent  string      `yaml:"parent" json:"parent"`
	Mode    string      `yaml:"mode" json:"mode"` // "builtin" | "system"
	MAC     string      `yaml:"mac" json:"mac"`
	VLAN    VLANConfig  `yaml:"vlan" json:"vlan"`
	MTU     int         `yaml:"mtu,omitempty" json:"mtu"`
	Enabled bool        `yaml:"enabled" json:"enabled"`
	IPv4    IfaceFamily `yaml:"ipv4" json:"ipv4"`
	IPv6    IfaceFamily `yaml:"ipv6" json:"ipv6"`
}

// VLANConfig: tagged (802.1Q, ID) or untagged.
type VLANConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	ID      int  `yaml:"id,omitempty" json:"id"`
}

// IfaceFamily is one address family of a child interface. Mode: IPv4
// "off" | "dhcp" | "static"; IPv6 "off" | "slaac" | "static".
type IfaceFamily struct {
	Mode     string   `yaml:"mode" json:"mode"`
	Addrs    []string `yaml:"addrs,omitempty" json:"addrs"`
	Gateway  string   `yaml:"gateway,omitempty" json:"gateway"`
	DNS      []string `yaml:"dns,omitempty" json:"dns"`
	Hostname string   `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	ClientID string   `yaml:"client_id,omitempty" json:"client_id,omitempty"`
}

// EgressConfig names the interface direct IPv4 / IPv6 flows leave through
// ("" = that family is not provided).
type EgressConfig struct {
	IPv4 string `yaml:"ipv4" json:"ipv4"`
	IPv6 string `yaml:"ipv6" json:"ipv6"`
}

// PublicIPConfig: how the public address of an interface is detected.
type PublicIPConfig struct {
	Method   string   `yaml:"method,omitempty" json:"method"` // "public" | "peer+public"
	Interval int      `yaml:"interval,omitempty" json:"interval"`
	Servers  []string `yaml:"servers,omitempty" json:"servers"`
}

var ifaceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,14}$`)

// canParent: child interfaces go on physical NICs. HY2SCALE_CHILD_PARENTS
// (comma-separated) adds interfaces that lead to the physical network
// although the kernel does not call them physical — e.g. the veth uplink of
// an LXC container.
func canParent(name string) bool {
	if vnic.IsPhysical(name) {
		return true
	}
	for _, n := range strings.Split(os.Getenv("HY2SCALE_CHILD_PARENTS"), ",") {
		if strings.TrimSpace(n) == name && name != "" {
			return true
		}
	}
	return false
}

// nameFree: no kernel interface or configured child is called name.
func nameFree(cfg *Config, name string) error {
	if _, err := net.InterfaceByName(name); err == nil {
		return fmt.Errorf("an interface named %q already exists", name)
	}
	for _, o := range cfg.Interfaces {
		if o.Name == name {
			return fmt.Errorf("an interface named %q already exists", name)
		}
	}
	return nil
}

// validate checks a child interface against the configuration it joins.
func (c *InterfaceConfig) validate(cfg *Config, creating bool) error {
	if !ifaceNameRe.MatchString(c.Name) {
		return fmt.Errorf("invalid name %q", c.Name)
	}
	if creating {
		if err := nameFree(cfg, c.Name); err != nil {
			return err
		}
	}
	if !canParent(c.Parent) {
		return fmt.Errorf("%q is not a physical interface", c.Parent)
	}
	switch c.Mode {
	case "", "builtin":
		c.Mode = "builtin"
	case "system":
		return fmt.Errorf("system mode is not available yet")
	default:
		return fmt.Errorf("invalid mode %q", c.Mode)
	}
	if c.MAC == "" {
		c.MAC = vnic.RandomMAC().String()
	}
	hw, err := net.ParseMAC(c.MAC)
	if err != nil || len(hw) != 6 || hw[0]&1 != 0 {
		return fmt.Errorf("invalid MAC %q", c.MAC)
	}
	c.MAC = hw.String()
	if c.VLAN.Enabled && (c.VLAN.ID < 1 || c.VLAN.ID > 4094) {
		return fmt.Errorf("VLAN ID must be 1–4094")
	}
	if !c.VLAN.Enabled {
		c.VLAN.ID = 0
	}
	if c.MTU != 0 && (c.MTU < 576 || c.MTU > 9000) {
		return fmt.Errorf("MTU must be 576–9000")
	}
	if err := c.IPv4.validate(4); err != nil {
		return fmt.Errorf("IPv4: %w", err)
	}
	if err := c.IPv6.validate(6); err != nil {
		return fmt.Errorf("IPv6: %w", err)
	}
	return nil
}

func (f *IfaceFamily) validate(fam int) error {
	switch f.Mode {
	case "", "off":
		f.Mode = "off"
	case "dhcp":
		if fam != 4 {
			return fmt.Errorf("invalid mode %q", f.Mode)
		}
	case "slaac":
		if fam != 6 {
			return fmt.Errorf("invalid mode %q", f.Mode)
		}
	case "static":
		if len(f.Addrs) == 0 {
			return fmt.Errorf("static needs an address")
		}
	default:
		return fmt.Errorf("invalid mode %q", f.Mode)
	}
	if f.Mode != "static" {
		f.Addrs, f.Gateway = nil, ""
	}
	if f.Mode != "dhcp" {
		f.Hostname, f.ClientID = "", ""
	}
	for _, a := range f.Addrs {
		p, err := netip.ParsePrefix(a)
		if err != nil || p.Addr().Is4() != (fam == 4) || p.Addr().Is4In6() {
			return fmt.Errorf("invalid address %q", a)
		}
	}
	if f.Gateway != "" {
		g, err := netip.ParseAddr(f.Gateway)
		if err != nil || g.Is4() != (fam == 4) {
			return fmt.Errorf("invalid gateway %q", f.Gateway)
		}
	}
	for _, d := range f.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return fmt.Errorf("invalid DNS %q", d)
		}
	}
	return nil
}

// vnicConfig converts a child's configuration for the runtime.
func (c InterfaceConfig) vnicConfig(stateDir string, onChange func()) vnic.Config {
	hw, _ := net.ParseMAC(c.MAC)
	vc := vnic.Config{
		Name: c.Name, Parent: c.Parent, MAC: hw, MTU: c.MTU,
		StateDir: stateDir, OnChange: onChange,
	}
	if c.VLAN.Enabled {
		vc.VLAN = c.VLAN.ID
	}
	vc.IPv4 = vnic.Family4{Mode: c.IPv4.Mode, Hostname: c.IPv4.Hostname, ClientID: c.IPv4.ClientID}
	vc.IPv4.Addrs, vc.IPv4.Gateway, vc.IPv4.DNS = parseFamily(c.IPv4)
	vc.IPv6 = vnic.Family6{Mode: c.IPv6.Mode}
	vc.IPv6.Addrs, vc.IPv6.Gateway, vc.IPv6.DNS = parseFamily(c.IPv6)
	return vc
}

func parseFamily(f IfaceFamily) (addrs []netip.Prefix, gw netip.Addr, dns []netip.Addr) {
	for _, a := range f.Addrs {
		if p, err := netip.ParsePrefix(a); err == nil {
			addrs = append(addrs, p)
		}
	}
	gw, _ = netip.ParseAddr(f.Gateway)
	for _, d := range f.DNS {
		if a, err := netip.ParseAddr(d); err == nil {
			dns = append(dns, a)
		}
	}
	return
}

// ifaceManager runs the child interfaces and keeps what the API shows.
type ifaceManager struct {
	a *App

	mu       sync.Mutex
	children map[string]*childRun
	pub      *pubState
}

type childRun struct {
	cfg   InterfaceConfig
	iface *vnic.Iface
	err   error
}

func newIfaceManager(a *App) *ifaceManager {
	m := &ifaceManager{a: a, children: map[string]*childRun{}}
	m.pub = newPubState(m)
	return m
}

// child returns a running child interface by name.
func (m *ifaceManager) child(name string) *vnic.Iface {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.children[name]; r != nil {
		return r.iface
	}
	return nil
}

func (m *ifaceManager) stateDir() string {
	return filepath.Join(m.a.dataDir, "ifaces")
}

// apply brings the running children in line with cfg: starts new ones,
// restarts changed ones, stops removed or disabled ones.
func (m *ifaceManager) apply(cfg Config) {
	want := map[string]InterfaceConfig{}
	for _, c := range cfg.Interfaces {
		want[c.Name] = c
	}
	m.mu.Lock()
	var stop []*vnic.Iface
	for name, r := range m.children {
		c, ok := want[name]
		if !ok || !sameIface(c, r.cfg) || !c.Enabled {
			if r.iface != nil {
				stop = append(stop, r.iface)
			}
			delete(m.children, name)
			if !ok {
				os.Remove(filepath.Join(m.stateDir(), name+".json"))
			}
		}
	}
	var start []InterfaceConfig
	for name, c := range want {
		if _, ok := m.children[name]; !ok {
			m.children[name] = &childRun{cfg: c}
			if c.Enabled {
				start = append(start, c)
			}
		}
	}
	m.mu.Unlock()
	for _, i := range stop {
		i.Close()
	}
	for _, c := range start {
		m.start(c)
	}
	m.a.refreshEgress()
}

func sameIface(a, b InterfaceConfig) bool {
	return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b)
}

func (m *ifaceManager) start(c InterfaceConfig) {
	os.MkdirAll(m.stateDir(), 0700)
	name := c.Name
	i, err := vnic.Start(m.a.appCtx, c.vnicConfig(m.stateDir(), func() { m.changed(name) }))
	if err != nil {
		log.Printf("[iface] %s: %v", name, err)
	}
	m.mu.Lock()
	r := m.children[name]
	if r == nil || !sameIface(r.cfg, c) {
		m.mu.Unlock()
		if i != nil {
			i.Close()
		}
		return
	}
	r.iface, r.err = i, err
	m.mu.Unlock()
	m.changed(name)
}

// changed runs when a child's addresses or state change.
func (m *ifaceManager) changed(name string) {
	m.a.refreshEgress()
	m.pub.kick(name)
}

// closeAll stops every child (DHCP release); used at shutdown.
func (m *ifaceManager) closeAll() {
	m.mu.Lock()
	var all []*vnic.Iface
	for _, r := range m.children {
		if r.iface != nil {
			all = append(all, r.iface)
		}
	}
	m.children = map[string]*childRun{}
	m.mu.Unlock()
	for _, i := range all {
		i.Close()
	}
}

// ---------------------------------------------------------------- API view

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
	// Country: ISO 3166-1 alpha-2 code of IP ("" = unknown yet).
	Country string `json:"country,omitempty"`
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
func (m *ifaceManager) systemIfaceView(ifi net.Interface, routes *routeInfo) IfaceView {
	v := IfaceView{
		Name: ifi.Name, Kind: "system", MTU: ifi.MTU, MAC: ifi.HardwareAddr.String(),
		Up:        ifi.Flags&net.FlagUp != 0,
		Container: isContainerIface(ifi.Name),
		Wireless:  vnic.IsWireless(ifi.Name),
		Type:      vnic.LinkType(ifi.Name),
		Public:    map[string]*PublicView{"ipv4": nil, "ipv6": nil},
	}
	if canParent(ifi.Name) {
		v.Kind = "physical"
		v.CanHaveChildren = true
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
func (m *ifaceManager) childView(c InterfaceConfig, i *vnic.Iface, startErr error) IfaceView {
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
func (m *ifaceManager) View() InterfacesView {
	var out InterfacesView
	cfg := m.a.store.Get()
	out.Egress = m.a.egressSelection()
	routes := readRoutes()
	ifs, _ := net.Interfaces()
	var phys string
	for _, ifi := range ifs {
		v := m.systemIfaceView(ifi, routes)
		m.pub.fill(&v)
		out.Interfaces = append(out.Interfaces, v)
		if v.Kind == "physical" && phys == "" {
			phys = ifi.Name
		}
	}
	m.mu.Lock()
	runs := map[string]*childRun{}
	for k, r := range m.children {
		runs[k] = r
	}
	m.mu.Unlock()
	for _, c := range cfg.Interfaces {
		var i *vnic.Iface
		var err error
		if r := runs[c.Name]; r != nil {
			i, err = r.iface, r.err
		}
		v := m.childView(c, i, err)
		m.pub.fill(&v)
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
		if err = c.validate(cfg, true); err != nil {
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
			if err = c.validate(cfg, false); err != nil {
				return
			}
			if c.Name != name {
				if err = nameFree(cfg, c.Name); err != nil {
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
		os.Rename(filepath.Join(a.ifaces.stateDir(), name+".json"), filepath.Join(a.ifaces.stateDir(), c.Name+".json"))
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
	i := a.ifaces.child(name)
	if i == nil {
		return fmt.Errorf("interface %q is not running", name)
	}
	return i.Renew()
}

// RefreshPublicIP re-detects an interface's public addresses now.
func (a *App) RefreshPublicIP(name string) {
	a.ifaces.pub.detectNow(name)
}

// childDialer is the dial function of a running child, or nil.
func (a *App) childDialer(name string) func(context.Context, string, string) (net.Conn, error) {
	if i := a.ifaces.child(name); i != nil {
		return i.DialContext
	}
	return nil
}

// isChildIface reports whether name is a configured child interface.
func (a *App) isChildIface(name string) bool {
	for _, c := range a.store.Get().Interfaces {
		if c.Name == name {
			return true
		}
	}
	return false
}

// BindableInterfaces: what a node address can be bound to — the system's
// interfaces and the child interfaces.
func (a *App) BindableInterfaces() []IfaceInfo {
	out := SystemInterfaces()
	for _, c := range a.store.Get().Interfaces {
		info := IfaceInfo{Name: c.Name, Kind: "child", Parent: c.Parent, Addrs: []string{}}
		if i := a.ifaces.child(c.Name); i != nil {
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
func (a *App) installChildListen() {
	childListen = func(iface string, raddr net.IP) (net.PacketConn, bool, error) {
		if !a.isChildIface(iface) {
			return nil, false, nil
		}
		if raddr == nil {
			return nil, true, nil
		}
		i := a.ifaces.child(iface)
		if i == nil {
			return nil, true, fmt.Errorf("interface %s is not running", iface)
		}
		network, laddr := "udp4", netip.AddrPortFrom(netip.IPv4Unspecified(), 0)
		if raddr.To4() == nil {
			network, laddr = "udp6", netip.AddrPortFrom(netip.IPv6Unspecified(), 0)
		}
		pc, err := i.ListenPacket(network, laddr)
		return pc, true, err
	}
}

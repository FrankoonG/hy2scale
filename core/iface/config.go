package iface

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"

	"github.com/FrankoonG/hy2scale/core/vnic"
)

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

var ifaceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,14}$`)

// CanParent: child interfaces go on physical NICs. HY2SCALE_CHILD_PARENTS
// (comma-separated) adds interfaces that lead to the physical network
// although the kernel does not call them physical — e.g. the veth uplink of
// an LXC container.
func CanParent(name string) bool {
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

// NameFree: no kernel interface or configured child is called name.
func NameFree(children []InterfaceConfig, name string) error {
	if _, err := net.InterfaceByName(name); err == nil {
		return fmt.Errorf("an interface named %q already exists", name)
	}
	for _, o := range children {
		if o.Name == name {
			return fmt.Errorf("an interface named %q already exists", name)
		}
	}
	return nil
}

// Validate checks a child interface against the children it joins and
// normalises it.
func (c *InterfaceConfig) Validate(children []InterfaceConfig, creating bool) error {
	if !ifaceNameRe.MatchString(c.Name) {
		return fmt.Errorf("invalid name %q", c.Name)
	}
	if creating {
		if err := NameFree(children, c.Name); err != nil {
			return err
		}
	}
	if !CanParent(c.Parent) {
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

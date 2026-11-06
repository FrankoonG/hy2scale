// Package iface is the node's network side: the system's interfaces (read
// only), the child interfaces hy2scale runs on physical NICs with their own
// MAC (core/vnic: a packet socket and a userspace stack, nothing added to
// the system), the egress the node's own direct flows leave through, and
// the restrictions applied to those flows.
//
// Linux is the implemented platform. Elsewhere child interfaces report
// themselves unsupported, the routing table is not read and only the
// interface the system routes through can carry the egress: with no
// selection every flow simply follows the system's default route.
package iface

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/FrankoonG/hy2scale/core/vnic"
)

// Manager runs the child interfaces and the egress.
type Manager struct {
	stateDir string
	ctx      context.Context

	mu       sync.Mutex
	cfgs     []InterfaceConfig
	children map[string]*childRun
	sel      *EgressConfig

	// OnChange runs when a child's addresses or state change.
	OnChange func(name string)
	// OwnAddrs: this node's addresses known elsewhere (e.g. its public
	// address behind NAT), counted as internal destinations.
	OwnAddrs func() []netip.Addr
	// Fill decorates an interface of the View (e.g. its public address).
	Fill func(*IfaceView)

	egress atomic.Pointer[egressSet]
	lnets  localNets
}

type childRun struct {
	cfg   InterfaceConfig
	iface *vnic.Iface
	err   error
}

// New: stateDir keeps the children's state (DHCP leases).
func New(stateDir string) *Manager {
	m := &Manager{stateDir: stateDir, ctx: context.Background(), children: map[string]*childRun{}}
	m.refreshEgress()
	return m
}

// Start: ctx bounds the children started from now on.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
}

// StateDir is where the children keep their state.
func (m *Manager) StateDir() string { return m.stateDir }

// Child returns a running child interface by name.
func (m *Manager) Child(name string) *vnic.Iface {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.children[name]; r != nil {
		return r.iface
	}
	return nil
}

// IsChild: name is a configured child interface.
func (m *Manager) IsChild(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.cfgs {
		if c.Name == name {
			return true
		}
	}
	return false
}

// Apply brings the running children in line with children: starts new
// ones, restarts changed ones, stops removed or disabled ones.
func (m *Manager) Apply(children []InterfaceConfig) {
	want := map[string]InterfaceConfig{}
	for _, c := range children {
		want[c.Name] = c
	}
	m.mu.Lock()
	m.cfgs = append([]InterfaceConfig(nil), children...)
	var stop []*vnic.Iface
	for name, r := range m.children {
		c, ok := want[name]
		if !ok || !sameIface(c, r.cfg) || !c.Enabled {
			if r.iface != nil {
				stop = append(stop, r.iface)
			}
			delete(m.children, name)
			if !ok {
				os.Remove(filepath.Join(m.stateDir, name+".json"))
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
	m.refreshEgress()
}

func sameIface(a, b InterfaceConfig) bool {
	return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b)
}

func (m *Manager) start(c InterfaceConfig) {
	os.MkdirAll(m.stateDir, 0700)
	name := c.Name
	m.mu.Lock()
	ctx := m.ctx
	m.mu.Unlock()
	i, err := vnic.Start(ctx, c.vnicConfig(m.stateDir, func() { m.changed(name) }))
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
func (m *Manager) changed(name string) {
	m.refreshEgress()
	if m.OnChange != nil {
		m.OnChange(name)
	}
}

// RenameState keeps a renamed child's saved state.
func (m *Manager) RenameState(from, to string) {
	os.Rename(filepath.Join(m.stateDir, from+".json"), filepath.Join(m.stateDir, to+".json"))
}

// Close stops every child (DHCP release); used at shutdown.
func (m *Manager) Close() {
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

// Renew renews a child's DHCP lease.
func (m *Manager) Renew(name string) error {
	i := m.Child(name)
	if i == nil {
		return fmt.Errorf("interface %q is not running", name)
	}
	return i.Renew()
}

// ChildListen opens a UDP socket toward raddr on child interface iface
// (raddr nil: only report whether iface is a child). ok=false: iface is
// not a child interface.
func (m *Manager) ChildListen(iface string, raddr net.IP) (net.PacketConn, bool, error) {
	if !m.IsChild(iface) {
		return nil, false, nil
	}
	if raddr == nil {
		return nil, true, nil
	}
	i := m.Child(iface)
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

// ChildListenPort opens a UDP socket of family v6/v4 on child interface
// iface at local port (0 = any) — hole punching reuses one port for its
// probes and the punch. ok=false: iface is not a child interface.
func (m *Manager) ChildListenPort(iface string, v6 bool, port int) (net.PacketConn, bool, error) {
	if !m.IsChild(iface) {
		return nil, false, nil
	}
	i := m.Child(iface)
	if i == nil {
		return nil, true, fmt.Errorf("interface %s is not running", iface)
	}
	network, any := "udp4", netip.IPv4Unspecified()
	if v6 {
		network, any = "udp6", netip.IPv6Unspecified()
	}
	pc, err := i.ListenPacket(network, netip.AddrPortFrom(any, uint16(port)))
	return pc, true, err
}

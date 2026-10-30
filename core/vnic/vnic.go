// Package vnic runs "built-in" child interfaces: hy2scale joins the cable of
// a physical NIC as an extra host with its own MAC address, entirely in
// userspace. Frames are received and sent through AF_PACKET sockets on the
// parent and terminated by a gVisor netstack per interface; addresses come
// from DHCP / SLAAC / static configuration. Nothing is created in the kernel
// (no interface, address, route or rule), so the OS does not see the child.
//
// The interface only originates connections (DialContext / ListenPacket /
// LookupIP). Unsolicited inbound TCP/UDP is dropped silently; ARP, NDP and
// ICMP echo are answered.
package vnic

import (
	"crypto/rand"
	"net"
	"net/netip"
	"time"
)

// Family4 is the IPv4 configuration of a child interface.
type Family4 struct {
	Mode     string         // "off" | "dhcp" | "static" ("" = off)
	Addrs    []netip.Prefix // static: first = primary
	Gateway  netip.Addr
	DNS      []netip.Addr
	Hostname string // dhcp option 12, optional
	ClientID string // dhcp option 61, optional ("01:aa:bb:.." hex = raw bytes, otherwise text)
}

// Family6 is the IPv6 configuration of a child interface.
type Family6 struct {
	Mode    string // "off" | "slaac" | "static" ("" = off)
	Addrs   []netip.Prefix
	Gateway netip.Addr
	DNS     []netip.Addr
}

// Config describes one child interface.
type Config struct {
	Name     string
	Parent   string // physical NIC
	MAC      net.HardwareAddr
	VLAN     int // 0 = untagged, 1..4094 = 802.1Q
	MTU      int // 0 = parent's
	IPv4     Family4
	IPv6     Family6
	StateDir string // lease persistence: <StateDir>/<Name>.json ("" = none)
	OnChange func() // called (async-safe) when addresses / state change
}

// AddrInfo is one address of a child interface.
type AddrInfo struct {
	Prefix  netip.Prefix
	Source  string    // "dhcp" | "static" | "slaac"
	Expires time.Time // zero = no expiry
}

// FamilyStatus is the runtime state of one address family.
type FamilyStatus struct {
	State   string // "off" | "acquiring" | "ok" | "failed"
	Error   string
	Addrs   []AddrInfo // primary first
	Gateway netip.Addr
	DNS     []netip.Addr
}

// Status is a snapshot of a child interface.
type Status struct {
	Name             string
	Up               bool   // packet socket open on the parent
	Error            string // parent-level error (missing parent, no permission, ...)
	MAC              string
	IPv4             FamilyStatus
	IPv6             FamilyStatus
	RxBytes, TxBytes uint64
}

// Family states.
const (
	StateOff       = "off"
	StateAcquiring = "acquiring"
	StateOK        = "ok"
	StateFailed    = "failed"
)

// RandomMAC returns a random locally administered unicast MAC address.
func RandomMAC() net.HardwareAddr {
	m := make(net.HardwareAddr, 6)
	if _, err := rand.Read(m); err != nil {
		panic(err)
	}
	m[0] = (m[0] | 0x02) &^ 0x01
	return m
}

func modeOf(m string) string {
	if m == "" {
		return "off"
	}
	return m
}

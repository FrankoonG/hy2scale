// Package capture takes over this host's system traffic for the policy
// engine (spec docs/v1.4.0-policy.md §3): layer 1 is the host's own
// outgoing connections (mangle OUTPUT mark → reroute to loopback →
// TPROXY), layer 2 is traffic forwarded through selected interfaces
// (mangle PREROUTING TPROXY). Taken-over flows are handed to a
// policy.Runtime starting at the direct entry; DNS queries (port 53) can
// go to a DNS handler (core/dnsproxy) instead.
//
// Linux only (iptables-legacy or iptables-nft through core/netfilter);
// elsewhere the capture reports itself unavailable. Rule generation, the
// interface filter and the heartbeat logic are platform independent so
// tests can check them anywhere.
package capture

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/FrankoonG/hy2scale/core/policy"
	"github.com/FrankoonG/hy2scale/core/sockmark"
)

// Fixed values of the capture. The listeners are bound to loopback only
// (never reachable from another host); each layer has its own pair of
// ports so a flow's layer is known from the port it arrives on.
const (
	ListenIP4 = "127.0.0.99"
	ListenIP6 = "::1"

	PortLocal      = 12390 // layer 1 TCP+UDP
	PortForward    = 12391 // layer 2 TCP+UDP
	PortDNSLocal   = 12392 // layer 1 DNS (TCP+UDP)
	PortDNSForward = 12393 // layer 2 DNS (TCP+UDP)

	// MarkSocket: hy2scale's own sockets (core/sockmark) — never captured.
	MarkSocket = sockmark.Mark
	// MarkRoute: packets and connections taken over (fwmark of the
	// loopback route, connmark of captured connections).
	MarkRoute = 0x1235

	RouteTable   = "102"
	RulePriority = "9"

	Set4 = "h2s_alive4"
	Set6 = "h2s_alive6"

	HeartbeatTimeout  = 30 * time.Second
	HeartbeatInterval = 10 * time.Second
)

// Reason codes of unavailable features (Avail.Reason).
const (
	ReasonPlatform   = "platform"    // not Linux
	ReasonPrivilege  = "privilege"   // no CAP_NET_ADMIN
	ReasonIptables   = "iptables"    // no working iptables
	ReasonTProxy     = "tproxy"      // the kernel / iptables has no TPROXY
	ReasonIpset      = "ipset"       // no ipset (heartbeat: exit cleanup only)
	ReasonCgroupV1   = "cgroup_v1"   // not on the unified cgroup hierarchy
	ReasonCgroupRoot = "cgroup_root" // the process sits in the (namespace) root cgroup
	ReasonXtCgroup   = "xt_cgroup"   // iptables cgroup match unavailable
)

// Avail is one feature's availability.
type Avail struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// ForwardAvail is layer 2's availability plus the IP forwarding state.
type ForwardAvail struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	IPForward bool   `json:"ip_forward"`
}

// SystemAvail: both layers.
type SystemAvail struct {
	Local   Avail        `json:"local"`
	Forward ForwardAvail `json:"forward"`
}

// Capabilities is what this host's capture can do. GeoIP and the iface
// terminal are not the capture's to know; the API fills them in.
type Capabilities struct {
	System        SystemAvail `json:"system"`
	Heartbeat     Avail       `json:"heartbeat"`
	IfaceTerminal Avail       `json:"iface_terminal"`
	GeoIP         bool        `json:"geoip"`
	DNSProxy      Avail       `json:"dns_proxy"`
	// Cgroup: the second guard (hy2scale's cgroup excluded) — extra field.
	Cgroup Avail `json:"cgroup"`
}

// LayerStatus is one layer's current state.
type LayerStatus struct {
	Active bool   `json:"active"`
	Error  string `json:"error"`
}

// HeartbeatStatus: ok = the last refresh succeeded; last = its time (unix
// seconds, 0 = never).
type HeartbeatStatus struct {
	OK   bool  `json:"ok"`
	Last int64 `json:"last"`
}

// Status is GET /api/policy/status.
type Status struct {
	Local     LayerStatus     `json:"local"`
	Forward   LayerStatus     `json:"forward"`
	Heartbeat HeartbeatStatus `json:"heartbeat"`
}

// DNSHandler answers DNS queries taken over on port 53 (core/dnsproxy).
type DNSHandler interface {
	// Answer returns the response to one query message.
	Answer(ctx context.Context, c *policy.Conn, query []byte) ([]byte, error)
	// ServeTCP serves a DNS-over-TCP connection until it ends.
	ServeTCP(ctx context.Context, c *policy.Conn, conn net.Conn)
	// DomainOf names an address from the recorded answers ("" = none).
	DomainOf(ip netip.Addr) string
}

// Options configure the capture; Apply is called again on every change.
type Options struct {
	System  policy.System
	Runtime policy.Runtime
	// DNS: the DNS proxy (nil = none; the layers' dns option is ignored).
	DNS DNSHandler
	// MeshAddrs: the node's mesh sockets, never captured — its server
	// listen address ("" = none) and every outbound peer address, as
	// "host:ports".
	MeshAddrs func() (listen string, peers []string)
}

// Interface is one entry of GET /api/policy/interfaces.
type Interface struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Addrs   []string `json:"addrs"`
	Up      bool     `json:"up"`
	Members []string `json:"members,omitempty"`
}

// RawIface is a system interface before filtering (§3.2).
type RawIface struct {
	Name     string
	Type     string // vnic.LinkType ("" = ethernet)
	Up       bool
	Loopback bool
	Master   string   // the bridge/bond it is enslaved to ("" = none)
	Addrs    []string // CIDRs, link-local excluded
}

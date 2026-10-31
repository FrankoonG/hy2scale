package capture

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/FrankoonG/hy2scale/core/policy"
)

// Cmd is one system command of a plan.
type Cmd struct {
	Tool string // iptables | ip6tables | ip | ip6 | ipset
	Args []string
	// Repeat: run until it fails (removing every copy of a hook / rule).
	Repeat bool
	// Optional: a failure is not an error (teardown, probes).
	Optional bool
}

func (c Cmd) String() string { return c.Tool + " " + strings.Join(c.Args, " ") }

// Spec is the capture to install.
type Spec struct {
	Local   *LocalSpec   // nil = layer 1 off
	Forward *ForwardSpec // nil = layer 2 off
	V4, V6  bool         // families to install
	// Cgroup: hy2scale's cgroup v2 path, excluded in OUTPUT ("" = no guard).
	Cgroup string
	// MeshListen: the server listen address ("host:ports"; "" = none).
	MeshListen string
	// MeshPeers: resolved peer addresses ("ip:ports", IPv6 bracketed).
	MeshPeers []string
}

// LocalSpec is layer 1.
type LocalSpec struct {
	DNS       bool // port 53 to the DNS proxy
	Heartbeat bool // on_stop open: gated by the heartbeat sets
}

// ForwardSpec is layer 2.
type ForwardSpec struct {
	Interfaces []string
	Sources    []string // CIDR, IP or a-b range; empty = any
	DNS        bool
	Heartbeat  bool
}

// Chains of the capture (mangle table, both families).
const (
	chainPre    = "H2S_POL_PRE"
	chainOut    = "H2S_POL_OUT"
	chainL1     = "H2S_POL_L1"
	chainL1TP   = "H2S_POL_L1TP"
	chainMark   = "H2S_POL_MARK"
	chainL2     = "H2S_POL_L2"
	chainL2New  = "H2S_POL_L2NEW"
	chainL2TP   = "H2S_POL_L2TP"
	chainProbe  = "H2S_POL_PROBE"
	legacyPre   = "H2S_RULES_PRE" // pre-1.4.0 rule capture
	legacyOut   = "H2S_RULES_OUT" // pre-1.4.0 rule capture
	legacyProbe = "H2S_RULES_PROBE"
)

var allChains = []string{chainPre, chainOut, chainL1, chainL1TP, chainMark, chainL2, chainL2New, chainL2TP}

func hex(v int) string { return fmt.Sprintf("0x%x", v) }

func iptTool(v6 bool) string {
	if v6 {
		return "ip6tables"
	}
	return "iptables"
}

func ipTool(v6 bool) string {
	if v6 {
		return "ip6"
	}
	return "ip"
}

func setName(v6 bool) string {
	if v6 {
		return Set6
	}
	return Set4
}

// excluded destinations, besides addrtype LOCAL: loopback, multicast,
// broadcast, link-local, unspecified.
func excluded(v6 bool) []string {
	if v6 {
		return []string{"::1/128", "::/128", "fe80::/10", "ff00::/8"}
	}
	return []string{"127.0.0.0/8", "0.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "255.255.255.255/32"}
}

// Install returns the commands that install spec (after Teardown).
func Install(s Spec) ([]Cmd, error) {
	var out []Cmd
	for _, v6 := range []bool{false, true} {
		if (v6 && !s.V6) || (!v6 && !s.V4) {
			continue
		}
		cmds, err := installFamily(s, v6)
		if err != nil {
			return nil, err
		}
		out = append(out, cmds...)
	}
	return out, nil
}

func installFamily(s Spec, v6 bool) ([]Cmd, error) {
	tool := iptTool(v6)
	var out []Cmd
	m := func(args ...string) {
		out = append(out, Cmd{Tool: tool, Args: append([]string{"-t", "mangle"}, args...)})
	}
	onIP := ListenIP4
	if v6 {
		onIP = ListenIP6
	}
	tproxy := func(chain string, proto string, dport string, port int) {
		args := []string{"-A", chain, "-p", proto}
		if dport != "" {
			args = append(args, "--dport", dport)
		}
		args = append(args, "-j", "TPROXY", "--on-ip", onIP, "--on-port", fmt.Sprint(port),
			"--tproxy-mark", hex(MarkRoute)+"/0xffffffff")
		m(args...)
	}
	hb := func(chain string) {
		m("-A", chain, "-m", "set", "!", "--match-set", setName(v6), "dst", "-j", "RETURN")
	}
	excl := func(chain string) {
		m("-A", chain, "-m", "addrtype", "--dst-type", "LOCAL", "-j", "RETURN")
		for _, d := range excluded(v6) {
			m("-A", chain, "-d", d, "-j", "RETURN")
		}
	}

	l2srcs, l2ok, err := familySources(s.Forward, v6)
	if err != nil {
		return nil, err
	}
	local := s.Local != nil
	fwd := s.Forward != nil && l2ok && len(s.Forward.Interfaces) > 0
	if !local && !fwd {
		return nil, nil
	}
	if fwd {
		for _, ifn := range s.Forward.Interfaces {
			if !validIfName(ifn) {
				return nil, fmt.Errorf("bad interface name %q", ifn)
			}
		}
	}

	for _, ch := range allChains {
		m("-N", ch)
	}

	if local {
		// OUTPUT: mark this host's own new connections (and later packets
		// of the marked ones) for the loopback route
		m("-A", chainOut, "-m", "mark", "--mark", hex(MarkSocket), "-j", "RETURN")
		if s.Cgroup != "" {
			m("-A", chainOut, "-m", "cgroup", "--path", s.Cgroup, "-j", "RETURN")
		}
		m("-A", chainOut, "-j", chainL1)
		if s.Local.Heartbeat {
			hb(chainL1)
		}
		excl(chainL1)
		if s.MeshListen != "" {
			if _, ports, ok := splitHostPorts(s.MeshListen); ok {
				for _, proto := range []string{"udp", "tcp"} {
					m("-A", chainL1, "-p", proto, "-m", "multiport", "--sports", ports, "-j", "RETURN")
				}
			}
		}
		for _, p := range s.MeshPeers {
			h, ports, ok := splitHostPorts(p)
			if !ok {
				continue
			}
			ip, err := netip.ParseAddr(h)
			if err != nil || ip.Unmap().Is6() != v6 {
				continue
			}
			for _, proto := range []string{"udp", "tcp"} {
				m("-A", chainL1, "-d", ip.Unmap().String(), "-p", proto, "-m", "multiport", "--dports", ports, "-j", "RETURN")
			}
		}
		m("-A", chainL1, "-m", "conntrack", "--ctdir", "REPLY", "-j", "RETURN")
		m("-A", chainL1, "-m", "connmark", "--mark", hex(MarkRoute), "-j", "MARK", "--set-mark", hex(MarkRoute))
		for _, proto := range []string{"tcp", "udp"} {
			m("-A", chainL1, "-p", proto, "-m", "conntrack", "--ctstate", "NEW", "-j", chainMark)
		}
		m("-A", chainMark, "-j", "MARK", "--set-mark", hex(MarkRoute))
		m("-A", chainMark, "-j", "CONNMARK", "--set-mark", hex(MarkRoute))

		// PREROUTING: the marked packets come back in on lo
		m("-A", chainPre, "-i", "lo", "-m", "mark", "--mark", hex(MarkRoute), "-j", chainL1TP)
		if s.Local.DNS {
			tproxy(chainL1TP, "udp", "53", PortDNSLocal)
			tproxy(chainL1TP, "tcp", "53", PortDNSLocal)
		}
		tproxy(chainL1TP, "tcp", "", PortLocal)
		tproxy(chainL1TP, "udp", "", PortLocal)
	}

	if fwd {
		for _, ifn := range s.Forward.Interfaces {
			m("-A", chainPre, "-i", ifn, "-j", chainL2)
		}
		if s.Forward.Heartbeat {
			hb(chainL2)
		}
		// later packets of connections taken over at their start
		m("-A", chainL2, "-m", "connmark", "--mark", hex(MarkRoute), "-m", "conntrack", "--ctdir", "ORIGINAL", "-j", chainL2TP)
		m("-A", chainL2, "-m", "conntrack", "!", "--ctstate", "NEW", "-j", "RETURN")
		excl(chainL2)
		if len(l2srcs) == 0 {
			m("-A", chainL2, "-j", chainL2New)
		}
		for _, src := range l2srcs {
			if strings.Contains(src, "-") {
				m("-A", chainL2, "-m", "iprange", "--src-range", src, "-j", chainL2New)
			} else {
				m("-A", chainL2, "-s", src, "-j", chainL2New)
			}
		}
		for _, proto := range []string{"tcp", "udp"} {
			m("-A", chainL2New, "-p", proto, "-j", "CONNMARK", "--set-mark", hex(MarkRoute))
		}
		m("-A", chainL2New, "-m", "connmark", "--mark", hex(MarkRoute), "-j", chainL2TP)
		if s.Forward.DNS {
			tproxy(chainL2TP, "udp", "53", PortDNSForward)
			tproxy(chainL2TP, "tcp", "53", PortDNSForward)
		}
		tproxy(chainL2TP, "tcp", "", PortForward)
		tproxy(chainL2TP, "udp", "", PortForward)
	}

	// policy route of the marked packets: deliver locally (via lo)
	def := "0.0.0.0/0"
	if v6 {
		def = "::/0"
	}
	ipt := ipTool(v6)
	out = append(out,
		Cmd{Tool: ipt, Args: []string{"rule", "add", "fwmark", hex(MarkRoute) + "/0xffffffff", "lookup", RouteTable, "priority", RulePriority}},
		Cmd{Tool: ipt, Args: []string{"route", "replace", "local", def, "dev", "lo", "table", RouteTable}},
	)
	// hooks last: nothing is captured before the chains are complete
	m("-I", "PREROUTING", "1", "-j", chainPre)
	if local {
		m("-I", "OUTPUT", "1", "-j", chainOut)
	}
	return out, nil
}

// familySources: the layer 2 sources of one family. ok=false: sources are
// set but none is of this family (layer 2 has nothing to take there).
func familySources(f *ForwardSpec, v6 bool) ([]string, bool, error) {
	if f == nil || len(f.Sources) == 0 {
		return nil, true, nil
	}
	var out []string
	for _, s := range f.Sources {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		is6, norm, err := parseSource(s)
		if err != nil {
			return nil, false, err
		}
		if is6 == v6 {
			out = append(out, norm)
		}
	}
	return out, len(out) > 0, nil
}

func parseSource(s string) (v6 bool, norm string, err error) {
	if lo, hi, ok := strings.Cut(s, "-"); ok {
		a, e1 := netip.ParseAddr(strings.TrimSpace(lo))
		b, e2 := netip.ParseAddr(strings.TrimSpace(hi))
		if e1 != nil || e2 != nil || a.Is4() != b.Is4() || b.Less(a) {
			return false, "", fmt.Errorf("bad source range %q", s)
		}
		return a.Is6(), a.String() + "-" + b.String(), nil
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return false, "", fmt.Errorf("bad source %q", s)
		}
		return p.Addr().Is6(), p.Masked().String(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return false, "", fmt.Errorf("bad source %q", s)
	}
	return a.Is6(), a.String(), nil
}

// Teardown returns the commands that remove everything any capture
// installed (this version's and the pre-1.4.0 rule capture's), in both
// families. Every command is optional.
func Teardown() []Cmd {
	var out []Cmd
	for _, v6 := range []bool{false, true} {
		tool := iptTool(v6)
		o := func(repeat bool, args ...string) {
			out = append(out, Cmd{Tool: tool, Args: args, Repeat: repeat, Optional: true})
		}
		o(true, "-t", "mangle", "-D", "PREROUTING", "-j", chainPre)
		o(true, "-t", "mangle", "-D", "OUTPUT", "-j", chainOut)
		for _, ch := range allChains {
			o(false, "-t", "mangle", "-F", ch)
		}
		for _, ch := range allChains {
			o(false, "-t", "mangle", "-X", ch)
		}
		for _, tbl := range []string{"mangle", "nat"} {
			o(true, "-t", tbl, "-D", "PREROUTING", "-j", legacyPre)
			o(true, "-t", tbl, "-D", "OUTPUT", "-j", legacyOut)
			for _, ch := range []string{legacyPre, legacyOut} {
				o(false, "-t", tbl, "-F", ch)
				o(false, "-t", tbl, "-X", ch)
			}
		}
		ipt := ipTool(v6)
		out = append(out,
			Cmd{Tool: ipt, Args: []string{"rule", "del", "fwmark", hex(MarkRoute) + "/0xffffffff", "lookup", RouteTable}, Repeat: true, Optional: true},
			Cmd{Tool: ipt, Args: []string{"rule", "del", "fwmark", hex(MarkRoute), "lookup", RouteTable}, Repeat: true, Optional: true},
			Cmd{Tool: ipt, Args: []string{"route", "flush", "table", RouteTable}, Optional: true},
		)
	}
	return out
}

// HeartbeatCreate returns the commands creating one family's heartbeat
// set and filling it.
func HeartbeatCreate(v6 bool) []Cmd {
	fam := "inet"
	if v6 {
		fam = "inet6"
	}
	return append([]Cmd{{Tool: "ipset", Args: []string{"create", setName(v6), "hash:net", "family", fam,
		"timeout", fmt.Sprint(int(HeartbeatTimeout / 1e9)), "-exist"}}}, HeartbeatRefresh(v6)...)
}

// HeartbeatRefresh returns the commands re-adding one family's entries
// (resetting their timeout).
func HeartbeatRefresh(v6 bool) []Cmd {
	halves := []string{"0.0.0.0/1", "128.0.0.0/1"}
	if v6 {
		halves = []string{"::/1", "8000::/1"}
	}
	var out []Cmd
	for _, h := range halves {
		out = append(out, Cmd{Tool: "ipset", Args: []string{"add", setName(v6), h,
			"timeout", fmt.Sprint(int(HeartbeatTimeout / 1e9)), "-exist"}})
	}
	return out
}

// HeartbeatDestroy removes both heartbeat sets (after the rules that
// reference them).
func HeartbeatDestroy() []Cmd {
	return []Cmd{
		{Tool: "ipset", Args: []string{"destroy", Set4}, Optional: true},
		{Tool: "ipset", Args: []string{"destroy", Set6}, Optional: true},
	}
}

// SpecFor turns the configuration into a Spec. block: only the layers
// whose on_stop is block (what stays installed after the process stops).
func SpecFor(sys policy.System, dns bool, block bool) Spec {
	var s Spec
	l, f := sys.Local, sys.Forward
	if l.Enabled && (!block || l.OnStop == policy.OnStopBlock) {
		s.Local = &LocalSpec{DNS: dns && l.DNS, Heartbeat: !block && l.OnStop != policy.OnStopBlock}
	}
	if f.Enabled && len(f.Interfaces) > 0 && (!block || f.OnStop == policy.OnStopBlock) {
		s.Forward = &ForwardSpec{Interfaces: append([]string{}, f.Interfaces...),
			Sources: append([]string{}, f.Sources...), DNS: dns && f.DNS, Heartbeat: !block && f.OnStop != policy.OnStopBlock}
	}
	return s
}

// NeedsHeartbeat reports whether a spec gates any layer on the heartbeat.
func (s Spec) NeedsHeartbeat() bool {
	return (s.Local != nil && s.Local.Heartbeat) || (s.Forward != nil && s.Forward.Heartbeat)
}

// Empty: nothing to install.
func (s Spec) Empty() bool { return s.Local == nil && s.Forward == nil }

// splitHostPorts parses "host:portspec" (portspec "443", "443,8443",
// "20000-20010") into host and an iptables multiport list. ok=false when
// there is no port or the list is too long for multiport (15 entries).
func splitHostPorts(addr string) (string, string, bool) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", false
	}
	host, spec := strings.Trim(addr[:i], "[]"), addr[i+1:]
	var parts []string
	n := 0
	for _, p := range strings.Split(spec, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "-") {
			n += 2
		} else {
			n++
		}
		parts = append(parts, strings.ReplaceAll(p, "-", ":"))
	}
	if len(parts) == 0 || n > 15 {
		return "", "", false
	}
	return host, strings.Join(parts, ","), true
}

// SplitHostPorts is splitHostPorts for callers resolving peer hosts.
func SplitHostPorts(addr string) (host, ports string, ok bool) { return splitHostPorts(addr) }

func validIfName(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == '/' || r == ':' || r == '!' || r > '~' {
			return false
		}
	}
	return true
}

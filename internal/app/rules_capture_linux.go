package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// Capture layer of the rule engine: hands this host's matching traffic to
// the transparent listeners, which are bound to loopback only (127.0.0.99,
// ::1) and so never reachable from another host.
//
//	tproxy  mangle PREROUTING TPROXY; the node's own traffic is marked in
//	        mangle OUTPUT and routed back in through lo (fwmark → table with
//	        "local default dev lo"). Destinations stay unrewritten, so hosts
//	        routed through this node are captured too, and UDP gets its
//	        original destination per datagram. IPv4 and IPv6 alike.
//	dnat    IPv4 only, when TPROXY is missing: nat DNAT to 127.0.0.99. Only
//	        the node's own traffic: DNAT to a loopback address from another
//	        host is a martian and is dropped, which keeps matching traffic
//	        from leaving untunnelled.
//	none    no capture for that family (no privileges, or IPv6 without
//	        TPROXY). Rules still route VPN and proxy users' flows.
//
// All lines live in the engine's own chains (H2S_RULES_PRE / H2S_RULES_OUT)
// of iptables and ip6tables, so stop removes everything with a flush.

const (
	ruleChainPre   = "H2S_RULES_PRE"
	ruleChainOut   = "H2S_RULES_OUT"
	ruleTProxyMark = 0x1235
	ruleTProxyTab  = "102"
	ruleTProxyPrio = "9"
	ruleProxyIP    = "127.0.0.99"
	ruleProxyIP6   = "::1"
)

type captureMode int

const (
	captureNone captureMode = iota
	captureDNAT
	captureTProxy
)

func (m captureMode) String() string {
	switch m {
	case captureTProxy:
		return "tproxy"
	case captureDNAT:
		return "dnat"
	}
	return "none"
}

func (m captureMode) table() string {
	if m == captureTProxy {
		return "mangle"
	}
	return "nat"
}

// ip6Variant picks the ip6tables binary the same way iptVariant does.
var ip6Variant = sync.OnceValue(func() string {
	if out, err := exec.Command("ip6tables", "-V").CombinedOutput(); err == nil && strings.Contains(string(out), "nf_tables") {
		return "ip6tables"
	}
	if exec.Command("ip6tables-legacy", "-L", "-n").Run() == nil {
		return "ip6tables-legacy"
	}
	return "ip6tables"
})

func iptProg(v6 bool) string {
	if v6 {
		return ip6Variant()
	}
	return iptVariant()
}

func iptOK(v6 bool, args ...string) bool {
	return iptExec(iptProg(v6), args...).Run() == nil
}

func ipCmd(v6 bool, args ...string) error {
	if v6 {
		args = append([]string{"-6"}, args...)
	}
	return exec.Command("ip", args...).Run()
}

// captureTeardown removes everything the capture layer installs, in both
// families and tables (also leftovers of a process that died uncleanly).
func captureTeardown() {
	mark := fmt.Sprintf("0x%x", ruleTProxyMark)
	for _, v6 := range []bool{false, true} {
		for _, tbl := range []string{"mangle", "nat"} {
			for _, pair := range [][2]string{{"PREROUTING", ruleChainPre}, {"OUTPUT", ruleChainOut}} {
				for iptOK(v6, "-t", tbl, "-D", pair[0], "-j", pair[1]) {
				}
				iptOK(v6, "-t", tbl, "-F", pair[1])
				iptOK(v6, "-t", tbl, "-X", pair[1])
			}
		}
		for ipCmd(v6, "rule", "del", "fwmark", mark, "lookup", ruleTProxyTab) == nil {
		}
		ipCmd(v6, "route", "flush", "table", ruleTProxyTab)
	}
}

// captureSetup creates one family's chains with their fixed head
// (exclusions) and returns the mode it runs in.
func captureSetup(v6 bool, cfg Config) captureMode {
	mode := captureNone
	switch {
	case os.Getenv("HY2SCALE_RULES_NO_TPROXY") == "" && tproxySupported(v6):
		mode = captureTProxy
	case !v6:
		mode = captureDNAT
	}
	if mode == captureNone {
		return mode
	}
	tbl := mode.table()
	prog := iptProg(v6)
	iptOK(v6, "-t", tbl, "-N", ruleChainPre)
	iptOK(v6, "-t", tbl, "-N", ruleChainOut)
	head := func(chain string, args ...string) {
		iptRun(prog, append([]string{"-t", tbl, "-A", chain}, args...)...)
	}
	head(ruleChainOut, "-m", "mark", "--mark", fmt.Sprintf("0x%x", ruleBypassMark), "-j", "RETURN")
	// traffic to this node itself is never captured
	loop := "127.0.0.0/8"
	if v6 {
		loop = "::1/128"
	}
	for _, ch := range []string{ruleChainPre, ruleChainOut} {
		if !iptOK(v6, "-t", tbl, "-A", ch, "-m", "addrtype", "--dst-type", "LOCAL", "-j", "RETURN") {
			head(ch, "-d", loop, "-j", "RETURN")
		}
	}
	// the mesh's own sockets: our server port (replies to inbound peers) and
	// every address of every outbound peer
	if cfg.Server != nil {
		if _, ports, ok := splitHostPorts(stripAddrBind(cfg.Server.Listen)); ok {
			for _, proto := range []string{"udp", "tcp"} {
				head(ruleChainOut, "-p", proto, "-m", "multiport", "--sports", ports, "-j", "RETURN")
			}
		}
	}
	for _, cl := range cfg.Clients {
		for _, a := range cl.AllAddrs() {
			if IsPunchAddr(a) {
				continue
			}
			host, ports, ok := splitHostPorts(stripAddrBind(a))
			if !ok {
				continue
			}
			ips, _ := net.LookupHost(host)
			for _, ip := range ips {
				if strings.Contains(ip, ":") != v6 {
					continue
				}
				for _, ch := range []string{ruleChainPre, ruleChainOut} {
					for _, proto := range []string{"udp", "tcp"} {
						head(ch, "-d", ip, "-p", proto, "-m", "multiport", "--dports", ports, "-j", "RETURN")
					}
				}
			}
		}
	}
	if mode == captureTProxy {
		mark := fmt.Sprintf("0x%x", ruleTProxyMark)
		def := "0.0.0.0/0"
		if v6 {
			def = "::/0"
		}
		ipCmd(v6, "rule", "add", "fwmark", mark, "lookup", ruleTProxyTab, "priority", ruleTProxyPrio)
		ipCmd(v6, "route", "replace", "local", def, "dev", "lo", "table", ruleTProxyTab)
	}
	iptOK(v6, "-t", tbl, "-I", "PREROUTING", "-j", ruleChainPre)
	iptOK(v6, "-t", tbl, "-I", "OUTPUT", "-j", ruleChainOut)
	return mode
}

// tproxySupported tries a TPROXY line in a scratch chain.
func tproxySupported(v6 bool) bool {
	const probe = "H2S_RULES_PROBE"
	defer func() {
		iptOK(v6, "-t", "mangle", "-F", probe)
		iptOK(v6, "-t", "mangle", "-X", probe)
	}()
	iptOK(v6, "-t", "mangle", "-N", probe)
	dst, on := "127.255.255.254/32", ruleProxyIP
	if v6 {
		dst, on = "fd00::fffe/128", ruleProxyIP6
	}
	return iptOK(v6, "-t", "mangle", "-A", probe, "-d", dst, "-p", "udp",
		"-j", "TPROXY", "--on-ip", on, "--on-port", fmt.Sprint(ruleUDPProxyPort),
		"--tproxy-mark", fmt.Sprintf("0x%x/0xffffffff", ruleTProxyMark))
}

// captureLines returns the lines that capture one target (IP, CIDR or a-b
// range, either family), each prefixed "4 " or "6 " for its iptables, or
// nil when that family is not captured.
func (e *ruleEngine) captureLines(target string) []string {
	v6 := strings.Contains(target, ":")
	mode, on := e.mode, ruleProxyIP
	if v6 {
		mode, on = e.mode6, ruleProxyIP6
	}
	if mode == captureNone {
		return nil
	}
	var match []string
	if strings.Contains(target, "-") && !strings.Contains(target, "/") {
		match = []string{"-m", "iprange", "--dst-range", target}
	} else {
		match = []string{"-d", target}
	}
	tbl := mode.table()
	fam := "4 "
	if v6 {
		fam = "6 "
	}
	var out []string
	for _, proto := range []string{"tcp", "udp"} {
		port := ruleProxyPort
		if proto == "udp" {
			port = ruleUDPProxyPort
		}
		pre := append([]string{"-t", tbl, "-A", ruleChainPre}, match...)
		outc := append([]string{"-t", tbl, "-A", ruleChainOut}, match...)
		if mode == captureTProxy {
			pre = append(pre, "-p", proto, "-j", "TPROXY", "--on-ip", on,
				"--on-port", fmt.Sprint(port), "--tproxy-mark", fmt.Sprintf("0x%x/0xffffffff", ruleTProxyMark))
			outc = append(outc, "-p", proto, "-j", "MARK", "--set-mark", fmt.Sprintf("0x%x", ruleTProxyMark))
		} else {
			dst := fmt.Sprintf("%s:%d", on, port)
			pre = append(pre, "-p", proto, "-j", "DNAT", "--to-destination", dst)
			outc = append(outc, "-p", proto, "-j", "DNAT", "--to-destination", dst)
		}
		out = append(out, fam+strings.Join(pre, " "), fam+strings.Join(outc, " "))
	}
	return out
}

// runCaptureLine adds (or with del, removes) one captureLines entry.
func runCaptureLine(line string, del bool) {
	v6 := strings.HasPrefix(line, "6 ")
	parts := strings.Fields(line[2:])
	if del {
		for i, p := range parts {
			if p == "-A" {
				parts[i] = "-D"
				break
			}
		}
		iptExec(iptProg(v6), parts...).Run()
		return
	}
	iptRun(iptProg(v6), parts...)
}

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

// setTransparent sets IP_TRANSPARENT / IPV6_TRANSPARENT on a socket.
func setTransparent(fd int, v6 bool) error {
	if v6 {
		return syscall.SetsockoptInt(fd, syscall.SOL_IPV6, 75 /* IPV6_TRANSPARENT */, 1)
	}
	return syscall.SetsockoptInt(fd, syscall.SOL_IP, syscall.IP_TRANSPARENT, 1)
}

// listenTCPTransparent listens on addr with the transparent option, so
// TPROXY can hand it connections for any destination (it also accepts
// DNAT'ed ones).
func listenTCPTransparent(addr string) (net.Listener, error) {
	v6 := strings.HasPrefix(addr, "[")
	network := "tcp4"
	if v6 {
		network = "tcp6"
	}
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		c.Control(func(fd uintptr) { setTransparent(int(fd), v6) })
		return nil
	}}
	return lc.Listen(context.Background(), network, addr)
}

// listenUDPTransparent listens on addr with the transparent option and the
// original-destination control message enabled.
func listenUDPTransparent(addr string) (*net.UDPConn, error) {
	v6 := strings.HasPrefix(addr, "[")
	network := "udp4"
	if v6 {
		network = "udp6"
	}
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var err error
		c.Control(func(fd uintptr) {
			setTransparent(int(fd), v6)
			if v6 {
				err = syscall.SetsockoptInt(int(fd), syscall.SOL_IPV6, 74 /* IPV6_RECVORIGDSTADDR */, 1)
			} else {
				err = syscall.SetsockoptInt(int(fd), syscall.SOL_IP, 20 /* IP_RECVORIGDSTADDR */, 1)
			}
		})
		return err
	}}
	pc, err := lc.ListenPacket(context.Background(), network, addr)
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

// udpOrigDst extracts IP_ORIGDSTADDR / IPV6_ORIGDSTADDR from a datagram's
// control messages.
func udpOrigDst(oob []byte) *net.UDPAddr {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	for _, m := range msgs {
		switch {
		case m.Header.Level == syscall.SOL_IP && m.Header.Type == 20 && len(m.Data) >= 8:
			return &net.UDPAddr{
				IP:   net.IPv4(m.Data[4], m.Data[5], m.Data[6], m.Data[7]),
				Port: int(binary.BigEndian.Uint16(m.Data[2:4])),
			}
		case m.Header.Level == syscall.SOL_IPV6 && m.Header.Type == 74 && len(m.Data) >= 24:
			return &net.UDPAddr{
				IP:   append(net.IP(nil), m.Data[8:24]...),
				Port: int(binary.BigEndian.Uint16(m.Data[2:4])),
			}
		}
	}
	return nil
}

// dialUDPAs opens a UDP socket bound to from (any address, transparent) and
// connected to to: replies to a TPROXY'd client must come from the address
// it sent to. Connected, it also receives that client's later datagrams for
// the flow (TPROXY prefers the exact-match socket).
func dialUDPAs(from, to *net.UDPAddr) (*net.UDPConn, error) {
	v6 := from.IP.To4() == nil
	network := "udp4"
	if v6 {
		network = "udp6"
	}
	d := net.Dialer{
		LocalAddr: from,
		Control: func(network, address string, c syscall.RawConn) error {
			var serr error
			c.Control(func(fd uintptr) {
				if serr = setTransparent(int(fd), v6); serr != nil {
					return
				}
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, ruleBypassMark)
			})
			return serr
		},
	}
	c, err := d.Dial(network, to.String())
	if err != nil {
		return nil, err
	}
	return c.(*net.UDPConn), nil
}

// dialUDPMarked creates a UDP connection with SO_MARK set to bypass the capture.
func dialUDPMarked(addr string, mark int) (net.Conn, error) {
	d := net.Dialer{Control: func(network, address string, c syscall.RawConn) error {
		var err error
		c.Control(func(fd uintptr) {
			err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
		})
		return err
	}}
	return d.Dial("udp", addr)
}

// dialTCPMarked creates a TCP connection with SO_MARK set to bypass the capture.
func dialTCPMarked(addr string, mark int) (net.Conn, error) {
	d := net.Dialer{Control: func(network, address string, c syscall.RawConn) error {
		var err error
		c.Control(func(fd uintptr) {
			err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
		})
		return err
	}}
	return d.Dial("tcp", addr)
}

// conntrackOrigDst looks up the original destination of a DNAT'ed UDP flow
// (IPv4 DNAT capture only: IP_RECVORIGDSTADDR doesn't report OUTPUT-chain
// DNAT).
func conntrackOrigDst(proto, srcAddr string, dstPort int) string {
	_, srcPort, err := net.SplitHostPort(srcAddr)
	if err != nil {
		return ""
	}
	out, err := exec.Command("conntrack", "-L", "-p", proto).CombinedOutput()
	if err != nil {
		debugLog("[rules] conntrack -L: %v", err)
		return ""
	}
	// reply tuple: sport=<proxy port> dport=<client port>; take the original
	// tuple's dst/dport (the first occurrences on the line)
	needle := fmt.Sprintf("sport=%d dport=%s", dstPort, srcPort)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		var dst, dport string
		for _, f := range strings.Fields(line) {
			if strings.HasPrefix(f, "dst=") && dst == "" {
				dst = f[4:]
			} else if strings.HasPrefix(f, "dport=") && dport == "" {
				dport = f[6:]
			}
		}
		if dst != "" && dport != "" && dst != "127.0.0.1" && dst != ruleProxyIP {
			return net.JoinHostPort(dst, dport)
		}
	}
	return ""
}

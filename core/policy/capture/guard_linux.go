//go:build linux

package capture

import (
	"context"
	"log"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The installed capture is not the only owner of the host's rules: network
// managers drop routing policy rules they did not create when they start
// (systemd-networkd's ManageForeignRoutingPolicyRules, restarted by
// needrestart after a library update), firewall frontends reload their
// tables. The guard notices a missing piece — on a routing-rule change and
// every GuardInterval — and puts it back.

// GuardInterval: how often the installed pieces are checked.
const GuardInterval = 10 * time.Second

// missing is what the check found absent, per family.
type missing struct {
	rule, route, accept, hooks [2]bool
}

func (x missing) any() bool {
	for f := 0; f < 2; f++ {
		if x.rule[f] || x.route[f] || x.accept[f] || x.hooks[f] {
			return true
		}
	}
	return false
}

// String names the missing pieces ("ipv4 rule, ipv6 hooks").
func (x missing) String() string {
	var out []string
	for f, fam := range []string{"ipv4", "ipv6"} {
		for _, p := range []struct {
			on   bool
			name string
		}{{x.rule[f], "rule"}, {x.route[f], "route"}, {x.accept[f], "input accept"}, {x.hooks[f], "hooks"}} {
			if p.on {
				out = append(out, fam+" "+p.name)
			}
		}
	}
	return strings.Join(out, ", ")
}

// check looks for every piece of the installed spec.
func check(s Spec) missing {
	var x missing
	for f, v6 := range []bool{false, true} {
		if (v6 && !s.V6) || (!v6 && !s.V4) {
			continue
		}
		x.rule[f] = !hasRule(v6)
		x.route[f] = !hasRoute(v6)
		tool := iptTool(v6)
		x.accept[f] = runCmd(Cmd{Tool: tool, Args: []string{"-t", "filter", "-C", "INPUT", "-m", "mark", "--mark", hex(MarkRoute), "-j", "ACCEPT"}}) != nil
		hooks := runCmd(Cmd{Tool: tool, Args: []string{"-t", "mangle", "-C", "PREROUTING", "-j", chainPre}}) != nil
		if s.Local != nil && runCmd(Cmd{Tool: tool, Args: []string{"-t", "mangle", "-C", "OUTPUT", "-j", chainOut}}) != nil {
			hooks = true
		}
		x.hooks[f] = hooks
	}
	return x
}

// repairCmds: the commands that put the missing pieces back. Missing hooks
// mean the tables were rewritten: the whole iptables part is installed
// again (chains included); rule, route and the INPUT accept are restored
// on their own.
func repairCmds(s Spec, x missing) ([]Cmd, error) {
	full, err := Install(s)
	if err != nil {
		return nil, err
	}
	var out []Cmd
	for f, v6 := range []bool{false, true} {
		ipt, ip := iptTool(v6), ipTool(v6)
		if x.hooks[f] {
			// remove what is left of this family's chains first
			for _, c := range Teardown() {
				if c.Tool == ipt && len(c.Args) > 1 && c.Args[1] == "mangle" {
					out = append(out, c)
				}
			}
		}
		for _, c := range full {
			switch {
			case c.Tool == ip && len(c.Args) > 0 && c.Args[0] == "rule":
				if x.rule[f] {
					out = append(out, c)
				}
			case c.Tool == ip && len(c.Args) > 0 && c.Args[0] == "route":
				if x.route[f] {
					out = append(out, c)
				}
			case c.Tool == ipt && len(c.Args) > 1 && c.Args[1] == "filter":
				if x.accept[f] {
					out = append(out, c)
				}
			case c.Tool == ipt:
				if x.hooks[f] {
					out = append(out, c)
				}
			}
		}
	}
	return out, nil
}

// guard runs until ctx ends: a check on every routing-rule change (netlink)
// and every GuardInterval.
func (m *Manager) guard(ctx context.Context) {
	defer m.Recover()
	kick := make(chan struct{}, 1)
	go watchRules(ctx, kick)
	t := time.NewTicker(GuardInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
			// let a burst of changes settle
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
		m.repairOnce(ctx)
	}
}

// repairOnce checks the installed capture and restores missing pieces.
func (m *Manager) repairOnce(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || !m.installed {
		return
	}
	x := check(m.spec)
	if !x.any() {
		return
	}
	cmds, err := repairCmds(m.spec, x)
	if err == nil {
		err = runAll(cmds)
	}
	m.status.Repairs++
	m.status.LastRepair = time.Now().Unix()
	m.status.LastRepaired = x.String()
	if err != nil {
		log.Printf("[capture] %s removed by another program; restoring failed: %v", x, err)
		return
	}
	log.Printf("[capture] %s removed by another program; restored", x)
}

// watchRules signals kick on every routing policy rule change (both
// families). Without a netlink socket the periodic check still runs.
func watchRules(ctx context.Context, kick chan<- struct{}) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return
	}
	groups := uint32(1<<(unix.RTNLGRP_IPV4_RULE-1) | 1<<(unix.RTNLGRP_IPV6_RULE-1))
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		unix.Close(fd)
		return
	}
	stop := context.AfterFunc(ctx, func() { unix.Close(fd) })
	defer stop()
	buf := make([]byte, 16<<10)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			if ctx.Err() != nil || err != unix.EINTR {
				return
			}
			continue
		}
		if n > 0 {
			select {
			case kick <- struct{}{}:
			default:
			}
		}
	}
}

// hasRule: the policy rule sending marked packets to the capture's table.
func hasRule(v6 bool) bool {
	fam := unix.AF_INET
	if v6 {
		fam = unix.AF_INET6
	}
	b, err := syscall.NetlinkRIB(unix.RTM_GETRULE, fam)
	if err != nil {
		return true // cannot tell: do not touch
	}
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return true
	}
	table, _ := strconv.Atoi(RouteTable)
	const hdr = 12 // struct fib_rule_hdr
	for _, msg := range msgs {
		if msg.Header.Type != unix.RTM_NEWRULE || len(msg.Data) < hdr {
			continue
		}
		tbl := uint32(msg.Data[4])
		var mark uint32
		hasMark := false
		for a := msg.Data[hdr:]; len(a) >= 4; {
			l := int(*(*uint16)(unsafe.Pointer(&a[0])))
			typ := *(*uint16)(unsafe.Pointer(&a[2]))
			if l < 4 || l > len(a) {
				break
			}
			v := a[4:l]
			switch typ {
			case unix.FRA_TABLE:
				if len(v) >= 4 {
					tbl = *(*uint32)(unsafe.Pointer(&v[0]))
				}
			case unix.FRA_FWMARK:
				if len(v) >= 4 {
					mark, hasMark = *(*uint32)(unsafe.Pointer(&v[0])), true
				}
			}
			adv := (l + 3) &^ 3
			if adv > len(a) {
				break
			}
			a = a[adv:]
		}
		if hasMark && mark == MarkRoute && tbl == uint32(table) {
			return true
		}
	}
	return false
}

// hasRoute: the local default route of the capture's table.
func hasRoute(v6 bool) bool {
	fam := unix.AF_INET
	if v6 {
		fam = unix.AF_INET6
	}
	b, err := syscall.NetlinkRIB(unix.RTM_GETROUTE, fam)
	if err != nil {
		return true
	}
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return true
	}
	table, _ := strconv.Atoi(RouteTable)
	for _, msg := range msgs {
		if msg.Header.Type != unix.RTM_NEWROUTE || len(msg.Data) < unix.SizeofRtMsg {
			continue
		}
		rt := (*unix.RtMsg)(unsafe.Pointer(&msg.Data[0]))
		if rt.Dst_len != 0 || rt.Type != unix.RTN_LOCAL {
			continue
		}
		tbl := uint32(rt.Table)
		if attrs, err := syscall.ParseNetlinkRouteAttr(&msg); err == nil {
			for _, at := range attrs {
				if at.Attr.Type == unix.RTA_TABLE && len(at.Value) >= 4 {
					tbl = *(*uint32)(unsafe.Pointer(&at.Value[0]))
				}
			}
		}
		if tbl == uint32(table) {
			return true
		}
	}
	return false
}

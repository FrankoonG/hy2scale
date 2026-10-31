//go:build linux

package capture

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/hy2scale/core/netfilter"
	"github.com/FrankoonG/hy2scale/core/policy"
	"github.com/FrankoonG/hy2scale/core/sockmark"
)

// Manager installs, serves and removes the capture.
type Manager struct {
	mu        sync.Mutex
	box       atomic.Pointer[handlers]
	spec      Spec // installed (Empty = nothing)
	installed bool
	cleaned   bool // stale rules removed once
	sys       policy.System
	hb        *Heartbeat
	hbCancel  context.CancelFunc
	lns       map[string]io2 // "tcp4:12390" → listener
	ctx       context.Context
	cancel    context.CancelFunc
	status    Status
	neigh     neighCache

	capsOnce sync.Once
	caps     Capabilities
	v6ok     bool
	v6off    bool // the IPv6 loopback listener could not open
}

// handlers: what taken-over flows are handed to (swapped on Apply).
type handlers struct {
	rt  policy.Runtime
	dns DNSHandler
	fwd []string // layer 2 interfaces
}

// io2 is an open listener (TCP or UDP).
type io2 interface{ Close() error }

// New returns an idle manager.
func New() *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{lns: map[string]io2{}, ctx: ctx, cancel: cancel}
	m.hb = &Heartbeat{Run: runCmd}
	return m
}

// Capabilities probes (once) what the host allows; ip_forward is read live.
func (m *Manager) Capabilities() Capabilities {
	m.capsOnce.Do(m.probe)
	c := m.caps
	c.System.Forward.IPForward = ipForward()
	return c
}

func (m *Manager) probe() {
	c := &m.caps
	fail := func(r string) {
		c.System.Local = Avail{Reason: r}
		c.System.Forward = ForwardAvail{Reason: r}
		c.DNSProxy = Avail{Reason: r}
		c.Heartbeat = Avail{Reason: r}
		c.Cgroup = Avail{Reason: r}
	}
	if ok, _ := netfilter.CheckCapability(); !ok || !sockmark.Available() {
		fail(ReasonPrivilege)
		return
	}
	if !netfilter.Available() {
		fail(ReasonIptables)
		return
	}
	ipt := func(v6 bool, args ...string) bool { return runCmd(Cmd{Tool: iptTool(v6), Args: args}) == nil }
	probeChain := func(v6 bool, rule ...string) bool {
		ipt(v6, "-t", "mangle", "-N", chainProbe)
		defer func() {
			ipt(v6, "-t", "mangle", "-F", chainProbe)
			ipt(v6, "-t", "mangle", "-X", chainProbe)
		}()
		return ipt(v6, append([]string{"-t", "mangle", "-A", chainProbe}, rule...)...)
	}
	tp := func(v6 bool) bool {
		dst, on := "127.255.255.254/32", ListenIP4
		if v6 {
			dst, on = "fd00::fffe/128", ListenIP6
		}
		return probeChain(v6, "-d", dst, "-p", "udp", "-j", "TPROXY", "--on-ip", on, "--on-port", fmt.Sprint(PortLocal),
			"--tproxy-mark", hex(MarkRoute)+"/0xffffffff") &&
			probeChain(v6, "-m", "conntrack", "--ctstate", "NEW", "-m", "connmark", "--mark", hex(MarkRoute), "-j", "CONNMARK", "--set-mark", hex(MarkRoute))
	}
	if !tp(false) {
		fail(ReasonTProxy)
		return
	}
	m.v6ok = tp(true)
	c.System.Local = Avail{Available: true}
	c.System.Forward = ForwardAvail{Available: true}
	c.DNSProxy = Avail{Available: true}

	// heartbeat: ipset and the iptables set match
	const probeSet = "h2s_probe"
	hbOK := runCmd(Cmd{Tool: "ipset", Args: []string{"create", probeSet, "hash:net", "family", "inet", "timeout", "1", "-exist"}}) == nil &&
		probeChain(false, "-m", "set", "--match-set", probeSet, "dst", "-j", "RETURN")
	runCmd(Cmd{Tool: "ipset", Args: []string{"destroy", probeSet}})
	if hbOK {
		c.Heartbeat = Avail{Available: true}
	} else {
		c.Heartbeat = Avail{Reason: ReasonIpset}
	}

	// cgroup guard
	path, reason := ownCgroup()
	if reason == "" && !probeChain(false, "-m", "cgroup", "--path", path, "-j", "RETURN") {
		reason = ReasonXtCgroup
	}
	if reason == "" {
		c.Cgroup = Avail{Available: true}
		cgroupPath = path
	} else {
		c.Cgroup = Avail{Reason: reason}
	}
}

var cgroupPath string

// ownCgroup: this process's cgroup v2 path, or why there is none.
func ownCgroup() (string, string) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", ReasonCgroupV1
	}
	return parseCgroup(string(b))
}

func ipForward() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// Status is the current state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	st := m.status
	m.mu.Unlock()
	st.Heartbeat = m.hb.Status()
	return st
}

// Interfaces lists the interfaces layer 2 can take traffic from.
func (m *Manager) Interfaces(own func(string) bool) []Interface {
	return FilterInterfaces(systemIfaces(), own)
}

// Apply (re)configures the capture. The first call also removes rules a
// previous process left behind.
func (m *Manager) Apply(o Options) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	caps := m.Capabilities()
	m.box.Store(&handlers{rt: o.Runtime, dns: o.DNS, fwd: o.System.Forward.Interfaces})
	m.sys = o.System

	st := Status{}
	sys := o.System
	if sys.Local.Enabled && !caps.System.Local.Available {
		st.Local.Error = caps.System.Local.Reason
		sys.Local.Enabled = false
	}
	if sys.Forward.Enabled && !caps.System.Forward.Available {
		st.Forward.Error = caps.System.Forward.Reason
		sys.Forward.Enabled = false
	}
	if !caps.System.Local.Available {
		m.status = st
		return nil
	}
	if !m.cleaned {
		m.teardownLocked()
		m.cleaned = true
	}
	spec := SpecFor(sys, o.DNS != nil, false)
	spec.V4, spec.V6 = true, m.v6ok && !m.v6off
	if !caps.Heartbeat.Available {
		if spec.Local != nil {
			spec.Local.Heartbeat = false
		}
		if spec.Forward != nil {
			spec.Forward.Heartbeat = false
		}
	}
	if caps.Cgroup.Available && spec.Local != nil {
		spec.Cgroup = cgroupPath
	}
	if spec.Local != nil && o.MeshAddrs != nil {
		spec.MeshListen, spec.MeshPeers = resolveMesh(o.MeshAddrs())
	}

	if m.installed && reflect.DeepEqual(spec, m.spec) {
		m.status = withActive(st, spec)
		return nil
	}
	m.teardownLocked()
	if spec.Empty() {
		m.closeListeners(nil)
		m.status = st
		return nil
	}
	err := m.openListeners(spec)
	if errors.Is(err, errV6Listen) {
		log.Printf("[capture] %v: IPv4 only", err)
		spec.V6, m.v6off = false, true
		err = m.openListeners(spec)
	}
	if err != nil {
		m.closeListeners(nil)
		m.status = withError(st, spec, err)
		return err
	}
	if spec.NeedsHeartbeat() {
		if err := m.hb.Start(spec.V4, spec.V6); err != nil {
			log.Printf("[capture] heartbeat: %v", err)
		}
		ctx, cancel := context.WithCancel(m.ctx)
		m.hbCancel = cancel
		go func() {
			defer m.Recover()
			m.hb.Loop(ctx, HeartbeatInterval)
		}()
	}
	cmds, err := Install(spec)
	if err == nil {
		err = runAll(cmds)
	}
	if err != nil {
		m.teardownLocked()
		m.closeListeners(nil)
		m.status = withError(st, spec, err)
		log.Printf("[capture] install: %v", err)
		return err
	}
	m.spec, m.installed = spec, true
	m.status = withActive(st, spec)
	log.Printf("[capture] installed: local=%v forward=%v ipv6=%v heartbeat=%v cgroup=%q",
		spec.Local != nil, spec.Forward != nil, spec.V6, spec.NeedsHeartbeat(), spec.Cgroup)
	return nil
}

func withActive(st Status, s Spec) Status {
	st.Local.Active = s.Local != nil
	st.Forward.Active = s.Forward != nil
	return st
}

func withError(st Status, s Spec, err error) Status {
	if s.Local != nil {
		st.Local.Error = err.Error()
	}
	if s.Forward != nil {
		st.Forward.Error = err.Error()
	}
	return st
}

// teardownLocked removes every rule, route and heartbeat set.
func (m *Manager) teardownLocked() {
	if m.hbCancel != nil {
		m.hbCancel()
		m.hbCancel = nil
	}
	m.hb.Stop()
	runTeardown()
	for _, c := range HeartbeatDestroy() {
		runCmd(c)
	}
	m.installed = false
	m.spec = Spec{}
}

// Stop removes the capture at exit: open layers are removed, block layers
// stay installed (their traffic is dropped while the process is gone).
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cleaned {
		return
	}
	m.teardownLocked()
	m.cancel()
	m.closeListeners(nil)
	block := SpecFor(m.sys, false, true)
	if block.Empty() {
		log.Printf("[capture] removed")
		return
	}
	block.V4, block.V6 = true, m.v6ok && !m.v6off
	if cmds, err := Install(block); err == nil {
		if err := runAll(cmds); err != nil {
			log.Printf("[capture] block on stop: %v", err)
		}
	}
	log.Printf("[capture] stopped; blocking layers kept: local=%v forward=%v", block.Local != nil, block.Forward != nil)
}

// Recover is deferred by goroutines: a panic removes the capture (as Stop)
// before it propagates.
func (m *Manager) Recover() {
	if r := recover(); r != nil {
		m.Stop()
		panic(r)
	}
}

// ---------------------------------------------------------------- commands

func toolCmd(c Cmd) *exec.Cmd {
	switch c.Tool {
	case "iptables":
		return netfilter.Exec(netfilter.Prog(false), c.Args...)
	case "ip6tables":
		return netfilter.Exec(netfilter.Prog(true), c.Args...)
	case "ip6":
		return exec.Command("ip", append([]string{"-6"}, c.Args...)...)
	}
	return exec.Command(c.Tool, c.Args...)
}

func runCmd(c Cmd) error {
	out, err := toolCmd(c).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", c, strings.TrimSpace(string(out)))
	}
	return nil
}

func runAll(cmds []Cmd) error {
	for _, c := range cmds {
		if err := runRepeat(c); err != nil && !c.Optional {
			return err
		}
	}
	return nil
}

func runRepeat(c Cmd) error {
	if !c.Repeat {
		return runCmd(c)
	}
	for i := 0; i < 32; i++ {
		if err := runCmd(c); err != nil {
			return nil
		}
	}
	return nil
}

// runTeardown runs Teardown with the active iptables and, when installed,
// the other variant too (rules left by a run with another variant).
func runTeardown() {
	runAll(Teardown())
	for _, v6 := range []bool{false, true} {
		cur := netfilter.Prog(v6)
		for _, alt := range []string{"iptables-legacy", "iptables-nft"} {
			if v6 {
				alt = strings.Replace(alt, "iptables", "ip6tables", 1)
			}
			if alt == cur {
				continue
			}
			if _, err := exec.LookPath(alt); err != nil {
				continue
			}
			for _, c := range Teardown() {
				if c.Tool != iptTool(v6) {
					continue
				}
				for i := 0; i < 32; i++ {
					if exec.Command(alt, c.Args...).Run() != nil || !c.Repeat {
						break
					}
				}
			}
		}
	}
}

// resolveMesh resolves the peer hosts to addresses (with their ports).
func resolveMesh(listen string, peers []string) (string, []string) {
	r := &net.Resolver{PreferGo: true, Dial: sockmark.Dialer(net.Dialer{}).DialContext}
	var out []string
	for _, p := range peers {
		h, _, ok := splitHostPorts(p)
		if !ok {
			continue
		}
		spec := p[strings.LastIndex(p, ":")+1:]
		if ip := net.ParseIP(h); ip != nil {
			out = append(out, net.JoinHostPort(ip.String(), spec))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ips, _ := r.LookupHost(ctx, h)
		cancel()
		for _, ip := range ips {
			out = append(out, net.JoinHostPort(ip, spec))
		}
	}
	sort.Strings(out)
	return listen, out
}

// Package publicip is the public-address add-on: it finds the public
// address of every interface that reaches the internet through public
// services, and publishes the egress interfaces' as this
// node's "egress" attribute (core/proto.AttrEgress). The core never calls
// these services; it only carries the attribute.
package publicip

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/iface"
	"github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
	"github.com/FrankoonG/hy2scale/internal/pubip"
)

// Public address detection: for every interface that can reach the
// internet (the egress interfaces, the running children and the system's
// default-route interfaces), each family is checked through that
// interface against public services — never through hy2scale peers unless
// the operator chose "peer+public" (node-to-node traffic stays plain hy2).

type pubKey struct {
	iface string
	fam   int
}

// Config: how the public address of an interface is detected.
type Config struct {
	Method   string   `yaml:"method,omitempty" json:"method"` // "public" | "peer+public"
	Interval int      `yaml:"interval,omitempty" json:"interval"`
	Servers  []string `yaml:"servers,omitempty" json:"servers"`
}

// Host is what the detector needs from the node.
type Host interface {
	// Config: the configured detection (nil = defaults).
	Config() *Config
	// Children: the configured child interfaces.
	Children() []iface.InterfaceConfig
	// PeerAddr asks a peer reached through iface which address it sees
	// this node at ("peer+public").
	PeerAddr(ctx context.Context, iface string, fam int) pubip.Result
}

// Detector runs the detection.
type Detector struct {
	m    *iface.Manager
	host Host
	ctx  context.Context

	mu      sync.Mutex
	results map[pubKey]pubip.Result
	running map[pubKey]bool
	lastLoc map[string]string // iface → its local addresses, to notice changes
	kickCh  chan string
}

// New: the detector of the interfaces of m; Run starts it.
func New(m *iface.Manager, h Host) *Detector {
	return &Detector{m: m, host: h, ctx: context.Background(), results: map[pubKey]pubip.Result{}, running: map[pubKey]bool{},
		lastLoc: map[string]string{}, kickCh: make(chan string, 64)}
}

func (p *Detector) settings() (interval time.Duration, servers []string, method string) {
	pc := p.host.Config()
	interval, servers, method = 10*time.Minute, pubip.DefaultServers, "public"
	if pc != nil {
		if pc.Interval >= 60 {
			interval = time.Duration(pc.Interval) * time.Second
		}
		if len(pc.Servers) > 0 {
			servers = pc.Servers
		}
		if pc.Method == "peer+public" {
			method = pc.Method
		}
	}
	return
}

// Servers: the configured detection servers (the built-in list when none).
func (p *Detector) Servers() []string {
	_, servers, _ := p.settings()
	return servers
}

// targets lists the (interface, family) pairs worth checking.
func (p *Detector) targets() []pubKey {
	seen := map[pubKey]bool{}
	var out []pubKey
	add := func(name string, fam int) {
		k := pubKey{name, fam}
		if name != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sel := p.m.Selection()
	add(sel.IPv4, 4)
	add(sel.IPv6, 6)
	add(iface.DefaultRouteIface(4), 4)
	add(iface.DefaultRouteIface(6), 6)
	for _, c := range p.host.Children() {
		if !c.Enabled {
			continue
		}
		if c.IPv4.Mode != "off" {
			add(c.Name, 4)
		}
		if c.IPv6.Mode != "off" {
			add(c.Name, 6)
		}
	}
	return out
}

// Kick re-checks interface name ("" = all).
func (p *Detector) Kick(name string) {
	select {
	case p.kickCh <- name:
	default:
	}
}

// KickAll re-checks every interface.
func (p *Detector) KickAll() { p.Kick("") }

// DetectNow re-checks one interface (both families) and waits a little for
// the result so the API can return it.
func (p *Detector) DetectNow(name string) {
	var wg sync.WaitGroup
	for _, k := range p.targets() {
		if k.iface == name {
			wg.Add(1)
			go func(k pubKey) { defer wg.Done(); p.detect(k) }(k)
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// Run is the scheduler: every interval, on kicks, and when an interface's
// local addresses change. It also publishes the egress attribute on n.
func (p *Detector) Run(ctx context.Context, n *node.Node) {
	p.ctx = ctx
	go p.publish(ctx, n)
	interval, _, _ := p.settings()
	full := time.NewTimer(5 * time.Second)
	poll := time.NewTicker(30 * time.Second)
	defer full.Stop()
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-full.C:
			for _, k := range p.targets() {
				go p.detect(k)
			}
			interval, _, _ = p.settings()
			full.Reset(interval)
		case name := <-p.kickCh:
			for _, k := range p.targets() {
				if name == "" || k.iface == name {
					go p.detect(k)
				}
			}
			if name == "" { // all re-checked: the next full round is one (current) interval away
				if !full.Stop() {
					select {
					case <-full.C:
					default:
					}
				}
				interval, _, _ = p.settings()
				full.Reset(interval)
			}
		case <-poll.C:
			for _, k := range p.targets() {
				loc := p.localAddrs(k.iface)
				p.mu.Lock()
				changed := p.lastLoc[k.iface] != loc
				p.lastLoc[k.iface] = loc
				p.mu.Unlock()
				if changed {
					go p.detect(k)
				}
			}
		}
	}
}

// localAddrs: a fingerprint of an interface's current addresses.
func (p *Detector) localAddrs(name string) string {
	var s string
	for _, ip := range p.local(name) {
		s += ip.String() + ","
	}
	return s
}

func (p *Detector) local(name string) []netip.Addr {
	if i := p.m.Child(name); i != nil {
		st := i.Status()
		var out []netip.Addr
		for _, a := range append(st.IPv4.Addrs, st.IPv6.Addrs...) {
			out = append(out, a.Prefix.Addr())
		}
		return out
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, _ := ifi.Addrs()
	var out []netip.Addr
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}

// detect checks one interface and family.
func (p *Detector) detect(k pubKey) {
	p.mu.Lock()
	if p.running[k] {
		p.mu.Unlock()
		return
	}
	p.running[k] = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.running, k)
		p.mu.Unlock()
	}()

	_, servers, method := p.settings()
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()
	var res pubip.Result
	if method == "peer+public" {
		res = p.host.PeerAddr(ctx, k.iface, k.fam)
	}
	if !res.IP.IsValid() {
		dial, resolve := p.funcs(k)
		res = pubip.Detect(ctx, k.fam, dial, resolve, servers)
	}
	p.mu.Lock()
	p.results[k] = res
	p.mu.Unlock()
}

// funcs: dial and resolve through the interface.
func (p *Detector) funcs(k pubKey) (pubip.DialFunc, pubip.ResolveFunc) {
	name := k.iface
	if p.m.IsChild(name) {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
				i := p.m.Child(name)
				if i == nil {
					return nil, errNotRunning(name)
				}
				return i.DialContext(ctx, network, addr)
			}, func(ctx context.Context, network, host string) ([]netip.Addr, error) {
				i := p.m.Child(name)
				if i == nil {
					return nil, errNotRunning(name)
				}
				return i.LookupIP(ctx, network, host)
			}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
			return iface.KernelDial(ctx, name, k.fam, network, addr, true)
		}, func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, network, host)
		}
}

type errNotRunning string

func (e errNotRunning) Error() string { return "interface " + string(e) + " is not running" }

// view is the API form of one result (nil = not checked).
func (p *Detector) view(name string, fam int) *iface.PublicView {
	p.mu.Lock()
	r, ok := p.results[pubKey{name, fam}]
	p.mu.Unlock()
	if !ok {
		return nil
	}
	v := &iface.PublicView{OK: r.IP.IsValid()}
	if !r.Checked.IsZero() {
		v.Checked = r.Checked.Unix()
	}
	if v.OK {
		v.IP = r.IP.String()
		for _, l := range p.local(name) {
			if l == r.IP {
				v.Direct = true
			}
		}
	} else if r.Err != nil {
		v.Error = r.Err.Error()
	}
	return v
}

// Fill sets an interface's public addresses in its API view.
func (p *Detector) Fill(v *iface.IfaceView) {
	v.Public["ipv4"] = p.view(v.Name, 4)
	v.Public["ipv6"] = p.view(v.Name, 6)
}

// SelfEgress is what this node tells its peers about its egress: per
// family the public address of the selected interface, or null when the
// family is not provided.
type SelfEgress struct {
	IPv4 *EgressPublic `json:"ipv4"`
	IPv6 *EgressPublic `json:"ipv6"`
}

// EgressPublic is one family of SelfEgress.
type EgressPublic struct {
	IP string `json:"ip"`
	OK bool   `json:"ok"`
}

// Self reports this node's egress.
func (p *Detector) Self() SelfEgress {
	sel := p.m.Selection()
	one := func(name string, fam int) *EgressPublic {
		if name == "" {
			return nil
		}
		e := &EgressPublic{}
		if v := p.view(name, fam); v != nil && v.OK {
			e.IP, e.OK = v.IP, true
		}
		return e
	}
	return SelfEgress{IPv4: one(sel.IPv4, 4), IPv6: one(sel.IPv6, 6)}
}

// SelfJSON is Self as JSON (the egress attribute).
func (p *Detector) SelfJSON() []byte {
	b, _ := json.Marshal(p.Self())
	return b
}

// publish keeps the egress attribute current.
func (p *Detector) publish(ctx context.Context, n *node.Node) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	var last string
	for {
		if b := p.SelfJSON(); string(b) != last {
			last = string(b)
			n.SetAttr(proto.AttrEgress, json.RawMessage(b))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Addrs: the detected public addresses (this node's own, for the
// internal-destination check).
func (p *Detector) Addrs() []netip.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []netip.Addr
	for _, r := range p.results {
		if r.IP.IsValid() {
			out = append(out, r.IP)
		}
	}
	return out
}

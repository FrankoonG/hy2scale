package app

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/iface"
	"github.com/FrankoonG/hy2scale/internal/ipcountry"
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

type pubState struct {
	m *ifaceManager

	mu      sync.Mutex
	results map[pubKey]pubip.Result
	running map[pubKey]bool
	lastLoc map[string]string // iface → its local addresses, to notice changes
	kickCh  chan string

	// The country of each public address, looked up by this node itself
	// (peers receive it with the egress, never look it up).
	countries    map[netip.Addr]countryEntry
	countryRun   map[netip.Addr]bool
	countryRetry map[netip.Addr]*time.Timer // pending retry after a failure
}

type countryEntry struct {
	code  string
	at    time.Time
	fails int // consecutive failed lookups
}

const (
	countryTTL      = 24 * time.Hour
	countryRetryMin = time.Minute
	countryRetryMax = 30 * time.Minute
)

// retryAfter: how long a failed lookup stands before trying again.
func (e countryEntry) retryAfter() time.Duration {
	d := countryRetryMin
	for i := 1; i < e.fails && d < countryRetryMax; i++ {
		d *= 2
	}
	if d > countryRetryMax {
		d = countryRetryMax
	}
	return d
}

func newPubState(m *ifaceManager) *pubState {
	return &pubState{m: m, results: map[pubKey]pubip.Result{}, running: map[pubKey]bool{},
		lastLoc: map[string]string{}, kickCh: make(chan string, 64),
		countries: map[netip.Addr]countryEntry{}, countryRun: map[netip.Addr]bool{},
		countryRetry: map[netip.Addr]*time.Timer{}}
}

func (p *pubState) settings() (interval time.Duration, servers []string, method string) {
	pc := p.m.a.store.Get().PublicIP
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

// targets lists the (interface, family) pairs worth checking.
func (p *pubState) targets() []pubKey {
	a := p.m.a
	seen := map[pubKey]bool{}
	var out []pubKey
	add := func(name string, fam int) {
		k := pubKey{name, fam}
		if name != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sel := a.egressSelection()
	add(sel.IPv4, 4)
	add(sel.IPv6, 6)
	add(iface.DefaultRouteIface(4), 4)
	add(iface.DefaultRouteIface(6), 6)
	for _, c := range a.store.Get().Interfaces {
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

func (p *pubState) kick(name string) {
	select {
	case p.kickCh <- name:
	default:
	}
}

func (p *pubState) kickAll() { p.kick("") }

// detectNow re-checks one interface (both families) and waits a little for
// the result so the API can return it.
func (p *pubState) detectNow(name string) {
	var wg sync.WaitGroup
	for _, k := range p.targets() {
		if k.iface == name {
			wg.Add(1)
			go func(k pubKey) { defer wg.Done(); p.detect(k, true) }(k)
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// run is the scheduler: every interval, on kicks, and when an interface's
// local addresses change.
func (p *pubState) run(ctx context.Context) {
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
				go p.detect(k, false)
			}
			interval, _, _ = p.settings()
			full.Reset(interval)
		case name := <-p.kickCh:
			for _, k := range p.targets() {
				if name == "" || k.iface == name {
					go p.detect(k, false)
				}
			}
		case <-poll.C:
			for _, k := range p.targets() {
				loc := p.localAddrs(k.iface)
				p.mu.Lock()
				changed := p.lastLoc[k.iface] != loc
				p.lastLoc[k.iface] = loc
				p.mu.Unlock()
				if changed {
					go p.detect(k, false)
				}
			}
		}
	}
}

// localAddrs: a fingerprint of an interface's current addresses.
func (p *pubState) localAddrs(name string) string {
	var s string
	for _, ip := range p.local(name) {
		s += ip.String() + ","
	}
	return s
}

func (p *pubState) local(name string) []netip.Addr {
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

// detect checks one interface and family; manual = an operator's refresh.
func (p *pubState) detect(k pubKey, manual bool) {
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
	ctx, cancel := context.WithTimeout(p.m.a.appCtx, 30*time.Second)
	defer cancel()
	var res pubip.Result
	if method == "peer+public" {
		res = p.m.a.peerReportedAddr(ctx, k.iface, k.fam)
	}
	if !res.IP.IsValid() {
		dial, resolve := p.funcs(k)
		res = pubip.Detect(ctx, k.fam, dial, resolve, servers)
	}
	p.mu.Lock()
	p.results[k] = res
	p.mu.Unlock()
	if res.IP.IsValid() {
		go p.lookupCountry(k, res.IP, manual)
	}
}

// lookupCountry finds the country of ip through the interface it was
// detected on, unless a recent answer is cached. A failure is retried on
// its own, backing off, while ip is still that interface's address; an
// operator's refresh (manual) retries it at once.
func (p *pubState) lookupCountry(k pubKey, ip netip.Addr, manual bool) {
	ip = ip.Unmap()
	p.mu.Lock()
	e, ok := p.countries[ip]
	fresh := ok && time.Since(e.at) < countryTTL
	if ok && e.fails > 0 {
		fresh = !manual && time.Since(e.at) < e.retryAfter()
	}
	if fresh || p.countryRun[ip] {
		p.mu.Unlock()
		return
	}
	p.countryRun[ip] = true
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(p.m.a.appCtx, 30*time.Second)
	defer cancel()
	dial, resolve := p.funcs(k)
	code, _ := ipcountry.Lookup(ctx, k.fam, ip, dial, resolve)

	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.countryRun, ip)
	if t := p.countryRetry[ip]; t != nil {
		t.Stop()
		delete(p.countryRetry, ip)
	}
	// forget addresses not looked at for a while (dynamic addresses)
	for a, x := range p.countries {
		if a != ip && p.countryRetry[a] == nil && time.Since(x.at) > countryTTL+countryRetryMax {
			delete(p.countries, a)
		}
	}
	if code != "" {
		p.countries[ip] = countryEntry{code: code, at: time.Now()}
		return
	}
	// A known answer stays shown through a failed refresh. Only scheduled
	// attempts lengthen the back-off, not an operator's refreshes.
	n := countryEntry{code: e.code, at: time.Now(), fails: e.fails + 1}
	if manual && e.fails > 0 {
		n.fails = e.fails
	}
	p.countries[ip] = n
	p.countryRetry[ip] = time.AfterFunc(n.retryAfter(), func() {
		p.mu.Lock()
		cur := p.results[k].IP.Unmap() == ip
		delete(p.countryRetry, ip)
		p.mu.Unlock()
		if cur && p.m.a.appCtx.Err() == nil {
			p.lookupCountry(k, ip, false)
		}
	})
}

// country is the cached country code of ip ("" = unknown).
func (p *pubState) country(ip netip.Addr) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.countries[ip.Unmap()].code
}

// funcs: dial and resolve through the interface.
func (p *pubState) funcs(k pubKey) (pubip.DialFunc, pubip.ResolveFunc) {
	name := k.iface
	if p.m.a.isChildIface(name) {
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
func (p *pubState) view(name string, fam int) *PublicView {
	p.mu.Lock()
	r, ok := p.results[pubKey{name, fam}]
	p.mu.Unlock()
	if !ok {
		return nil
	}
	v := &PublicView{OK: r.IP.IsValid()}
	if !r.Checked.IsZero() {
		v.Checked = r.Checked.Unix()
	}
	if v.OK {
		v.IP = r.IP.String()
		v.Country = p.country(r.IP)
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

// fill sets an interface's public addresses in its API view.
func (p *pubState) fill(v *IfaceView) {
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
	// Country is the ISO 3166-1 alpha-2 code this node found for IP.
	Country string `json:"country,omitempty"`
}

// SelfEgress reports this node's egress.
func (a *App) SelfEgress() SelfEgress {
	sel := a.egressSelection()
	one := func(name string, fam int) *EgressPublic {
		if name == "" {
			return nil
		}
		e := &EgressPublic{}
		if v := a.ifaces.pub.view(name, fam); v != nil && v.OK {
			e.IP, e.OK, e.Country = v.IP, true, v.Country
		}
		return e
	}
	return SelfEgress{IPv4: one(sel.IPv4, 4), IPv6: one(sel.IPv6, 6)}
}

// selfEgressJSON serves the relay's egress stream.
func (a *App) selfEgressJSON() []byte {
	b, _ := json.Marshal(a.SelfEgress())
	return b
}

// addrs: the detected public addresses (this node's own, for the
// internal-destination check).
func (p *pubState) addrs() []netip.Addr {
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

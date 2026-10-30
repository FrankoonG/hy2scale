package app

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Egress: the interface this node's direct flows leave through, one per
// address family ("" = the family is not provided). It covers every flow
// this node dials itself — its own users' direct traffic and traffic other
// nodes send through it as their exit. Destinations the system routes
// specifically (loopback, LAN subnets, explicit routes) are dialled by the
// system as before; the egress only replaces the default route.

var (
	errNoEgress4 = errors.New("this node provides no IPv4 egress")
	errNoEgress6 = errors.New("this node provides no IPv6 egress")
)

// egressPort dials and resolves through one interface for one family.
type egressPort struct {
	iface string
	fam   int
	dial  func(ctx context.Context, network, addr string) (net.Conn, error)
	look  func(ctx context.Context, host string) ([]netip.Addr, error)
}

// egressSet is the current egress of both families. legacy: no selection
// was ever made (the system routes everything, as before 1.4.0).
type egressSet struct {
	legacy bool
	v4, v6 *egressPort
}

func (e *egressSet) port(fam int) *egressPort {
	if fam == 4 {
		return e.v4
	}
	return e.v6
}

// egressSelection is the configured selection (legacy = what the system
// routes through now).
func (a *App) egressSelection() EgressConfig {
	if e := a.store.Get().Egress; e != nil {
		return *e
	}
	return EgressConfig{IPv4: defaultRouteIface(4), IPv6: defaultRouteIface(6)}
}

// refreshEgress rebuilds the dialers after a selection or interface change.
func (a *App) refreshEgress() {
	cfg := a.store.Get()
	set := &egressSet{legacy: cfg.Egress == nil}
	if cfg.Egress != nil {
		set.v4 = a.makePort(cfg.Egress.IPv4, 4)
		set.v6 = a.makePort(cfg.Egress.IPv6, 6)
	}
	a.egress.Store(set)
}

func (a *App) makePort(name string, fam int) *egressPort {
	if name == "" {
		return nil
	}
	nw := "ip4"
	if fam == 6 {
		nw = "ip6"
	}
	p := &egressPort{iface: name, fam: fam}
	if a.isChildIface(name) {
		p.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			i := a.ifaces.child(name)
			if i == nil {
				return nil, fmt.Errorf("interface %s is not running", name)
			}
			return i.DialContext(ctx, network, addr)
		}
		p.look = func(ctx context.Context, host string) ([]netip.Addr, error) {
			i := a.ifaces.child(name)
			if i == nil {
				return nil, fmt.Errorf("interface %s is not running", name)
			}
			return i.LookupIP(ctx, nw, host)
		}
		return p
	}
	p.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return kernelDial(ctx, name, fam, network, addr, defaultRouteIface(fam) != name)
	}
	p.look = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, nw, host)
	}
	return p
}

// kernelDial dials through a system interface. bind: pin the socket to the
// interface (it is not the one the system would choose).
func kernelDial(ctx context.Context, iface string, fam int, network, addr string, bind bool) (net.Conn, error) {
	d := net.Dialer{}
	if strings.HasPrefix(network, "tcp") {
		d.Timeout = 10 * time.Second
	} else {
		d.Timeout = 5 * time.Second
	}
	if bind {
		d.Control = func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
			}); err != nil {
				return err
			}
			if serr != nil {
				return fmt.Errorf("bind to %s: %w", iface, serr)
			}
			return nil
		}
	}
	return d.DialContext(ctx, familyNet(network, fam), addr)
}

// familyNet narrows "tcp"/"udp" to one family.
func familyNet(network string, fam int) string {
	network = strings.TrimRight(network, "46")
	if fam == 4 {
		return network + "4"
	}
	return network + "6"
}

// dialDirect dials addr ("host:port") from this node through the egress.
// network is "tcp" or "udp".
func (a *App) dialDirect(ctx context.Context, network, addr string) (net.Conn, error) {
	set := a.egress.Load()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// A restricted flow (access_guard.go): an address is checked here, a
	// name once resolved, before any socket is opened.
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if err := a.checkTarget(ctx, ip); err != nil {
			return nil, err
		}
	}
	if set == nil || set.legacy {
		return plainDialCtl(ctx, network, addr, a.targetControl(ctx))
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		ip = ip.Unmap()
		if systemRoutes(ip) {
			return plainDial(ctx, network, addr)
		}
		fam := 4
		if ip.Is6() {
			fam = 6
		}
		p := set.port(fam)
		if p == nil {
			if fam == 4 {
				return nil, errNoEgress4
			}
			return nil, errNoEgress6
		}
		return p.dial(ctx, familyNet(network, fam), addr)
	}

	// A name: each family resolved through its own egress.
	type answer struct {
		fam   int
		addrs []netip.Addr
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ch := make(chan answer, 2)
	n := 0
	for _, fam := range []int{6, 4} {
		p := set.port(fam)
		if p == nil {
			continue
		}
		n++
		go func(fam int, p *egressPort) {
			addrs, _ := p.look(lctx, host)
			ch <- answer{fam, addrs}
		}(fam, p)
	}
	if n == 0 {
		return nil, errors.New("this node provides no egress")
	}
	var v4, v6 []netip.Addr
	for ; n > 0; n-- {
		r := <-ch
		if r.fam == 4 {
			v4 = r.addrs
		} else {
			v6 = r.addrs
		}
	}
	type target struct {
		ip  netip.Addr
		fam int
	}
	var order []target
	// IPv6 first, IPv4 interleaved (RFC 8305); at most two of each.
	for i := 0; i < 2; i++ {
		if i < len(v6) {
			order = append(order, target{v6[i].Unmap(), 6})
		}
		if i < len(v4) {
			order = append(order, target{v4[i].Unmap(), 4})
		}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("lookup %s: no address", host)
	}
	dialOne := func(ctx context.Context, t target) (net.Conn, error) {
		if err := a.checkTarget(ctx, t.ip); err != nil {
			return nil, err
		}
		hp := net.JoinHostPort(t.ip.String(), port)
		if systemRoutes(t.ip) {
			return plainDial(ctx, network, hp)
		}
		return set.port(t.fam).dial(ctx, familyNet(network, t.fam), hp)
	}
	if !strings.HasPrefix(network, "tcp") || len(order) == 1 {
		var lastErr error
		for _, t := range order {
			c, err := dialOne(ctx, t)
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return raceDial(ctx, order, dialOne)
}

// raceDial starts the next attempt 250 ms after the previous one (or as
// soon as it fails); the first connection wins.
func raceDial[T any](ctx context.Context, order []T, dial func(context.Context, T) (net.Conn, error)) (net.Conn, error) {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type res struct {
		c   net.Conn
		err error
	}
	results := make(chan res, len(order))
	next, pending := 0, 0
	var lastErr error
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			if next < len(order) {
				t := order[next]
				next++
				pending++
				go func() {
					c, err := dial(rctx, t)
					results <- res{c, err}
				}()
				timer.Reset(250 * time.Millisecond)
			}
		case r := <-results:
			pending--
			if r.err == nil {
				cancel()
				go func(n int) { // close the losers
					for ; n > 0; n-- {
						if l := <-results; l.c != nil {
							l.c.Close()
						}
					}
				}(pending)
				return r.c, nil
			}
			lastErr = r.err
			if next < len(order) {
				timer.Reset(0)
			} else if pending == 0 {
				return nil, lastErr
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func plainDial(ctx context.Context, network, addr string) (net.Conn, error) {
	return plainDialCtl(ctx, network, addr, nil)
}

// plainDialCtl: plainDial with a socket Control hook (nil = none).
func plainDialCtl(ctx context.Context, network, addr string, ctl func(string, string, syscall.RawConn) error) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second, Control: ctl}
	if strings.HasPrefix(network, "udp") {
		d.Timeout = 5 * time.Second
	}
	return d.DialContext(ctx, network, addr)
}

// ---------------------------------------------------------------- routes

// systemRoutes reports whether the system routes ip specifically (loopback,
// link-local, multicast, or a main-table route more specific than the
// default) — such destinations bypass the egress.
func systemRoutes(ip netip.Addr) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	r := readRoutes()
	for _, p := range r.specific {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// routeInfo is a snapshot of the main routing table.
type routeInfo struct {
	specific []netip.Prefix    // non-default routes
	gateway4 map[string]string // iface → default gateway
	gateway6 map[string]string
}

var routeCache struct {
	mu   sync.Mutex
	at   time.Time
	info *routeInfo
}

// readRoutes parses /proc/net/route and /proc/net/ipv6_route (cached 3 s).
func readRoutes() *routeInfo {
	routeCache.mu.Lock()
	defer routeCache.mu.Unlock()
	if routeCache.info != nil && time.Since(routeCache.at) < 3*time.Second {
		return routeCache.info
	}
	r := &routeInfo{gateway4: map[string]string{}, gateway6: map[string]string{}}
	if f, err := os.Open("/proc/net/route"); err == nil {
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 8 {
				continue
			}
			dst, gw, mask := le32(fl[1]), le32(fl[2]), le32(fl[7])
			bits := maskBits(mask)
			if bits == 0 {
				if gw.IsValid() && !gw.IsUnspecified() {
					r.gateway4[fl[0]] = gw.String()
				}
				continue
			}
			r.specific = append(r.specific, netip.PrefixFrom(dst, bits))
		}
		f.Close()
	}
	if f, err := os.Open("/proc/net/ipv6_route"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 10 || fl[9] == "lo" {
				continue
			}
			dst, plen, gw := hex16(fl[0]), hexInt(fl[1]), hex16(fl[4])
			if plen == 0 {
				if gw.IsValid() && !gw.IsUnspecified() {
					r.gateway6[fl[9]] = gw.String()
				}
				continue
			}
			if dst.IsLinkLocalUnicast() || dst.IsMulticast() {
				continue
			}
			r.specific = append(r.specific, netip.PrefixFrom(dst, plen))
		}
		f.Close()
	}
	routeCache.info, routeCache.at = r, time.Now()
	return r
}

func le32(h string) netip.Addr {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 4 {
		return netip.Addr{}
	}
	return netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]})
}

func hex16(h string) netip.Addr {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 16 {
		return netip.Addr{}
	}
	return netip.AddrFrom16([16]byte(b))
}

func hexInt(h string) int {
	v, _ := strconv.ParseInt(h, 16, 32)
	return int(v)
}

func maskBits(m netip.Addr) int {
	if !m.IsValid() {
		return 0
	}
	b := m.As4()
	n := 0
	for _, x := range b {
		for ; x&0x80 != 0; x <<= 1 {
			n++
		}
	}
	return n
}

// probeTargets: addresses whose route stands for "the internet".
var probeTargets = map[int]string{4: "1.1.1.1:53", 6: "[2606:4700:4700::1111]:53"}

var defIfaceCache struct {
	mu   sync.Mutex
	at   [7]time.Time
	name [7]string
}

// defaultRouteIface is the interface the system routes the internet
// through for fam, honouring policy routing ("" = none). No packet is sent.
// Cached for 3 s.
func defaultRouteIface(fam int) string {
	defIfaceCache.mu.Lock()
	defer defIfaceCache.mu.Unlock()
	if time.Since(defIfaceCache.at[fam]) < 3*time.Second {
		return defIfaceCache.name[fam]
	}
	n := lookupDefaultIface(fam)
	defIfaceCache.at[fam], defIfaceCache.name[fam] = time.Now(), n
	return n
}

func lookupDefaultIface(fam int) string {
	c, err := net.Dial(familyNet("udp", fam), probeTargets[fam])
	if err != nil {
		return ""
	}
	src := c.LocalAddr().(*net.UDPAddr).IP
	c.Close()
	ifs, _ := net.Interfaces()
	for _, ifi := range ifs {
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(src) {
				return ifi.Name
			}
		}
	}
	return ""
}

// kernelRoutes reports whether the system can route the internet out of
// iface for fam (a socket pinned to it finds a route). No packet is sent.
func kernelRoutes(iface string, fam int) bool {
	d := net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		c.Control(func(fd uintptr) {
			serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
		})
		return serr
	}}
	c, err := d.Dial(familyNet("udp", fam), probeTargets[fam])
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// ---------------------------------------------------------------- selection

// SetEgress selects the egress interfaces ("" = family not provided).
func (a *App) SetEgress(e EgressConfig) error {
	view := a.ifaces.View()
	usable := map[string]IfaceView{}
	for _, v := range view.Interfaces {
		usable[v.Name] = v
	}
	for fam, name := range map[string]string{"ipv4": e.IPv4, "ipv6": e.IPv6} {
		if name == "" {
			continue
		}
		v, ok := usable[name]
		if !ok {
			return fmt.Errorf("no interface %q", name)
		}
		if u := v.EgressUsable[fam]; !u.OK && u.Reason != "no_address" {
			return fmt.Errorf("%s cannot carry %s: %s", name, fam, u.Reason)
		}
	}
	a.store.Update(func(c *Config) { ec := e; c.Egress = &ec })
	a.refreshEgress()
	a.ifaces.pub.kickAll()
	return nil
}

// initEgress fixes the first selection: the interfaces the system routes
// the internet through. Until IPv4 has a route the selection stays unset
// (the system routes, as before), so a node booting before its network is
// up is not locked out; IPv6 (often configured seconds later by SLAAC) gets
// up to 90 s more before it is recorded as not provided.
func (a *App) initEgress(ctx context.Context) {
	start := time.Now()
	for {
		if a.store.Get().Egress != nil {
			return
		}
		v4, v6 := defaultRouteIface(4), defaultRouteIface(6)
		if v4 != "" && (v6 != "" || time.Since(start) > 90*time.Second) {
			e := EgressConfig{IPv4: v4, IPv6: v6}
			a.store.Update(func(c *Config) {
				if c.Egress == nil {
					c.Egress = &e
				}
			})
			log.Printf("[egress] IPv4 via %s, IPv6 via %q", e.IPv4, e.IPv6)
			a.refreshEgress()
			a.ifaces.pub.kickAll()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// egressPointer holds the live dialers.
type egressPointer = atomic.Pointer[egressSet]

// egressLookup resolves a name the way dialDirect does: each family
// through its own egress (the system resolver before any selection).
func (a *App) egressLookup(ctx context.Context, name string) ([]net.IP, error) {
	set := a.egress.Load()
	if set == nil || set.legacy {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, name)
		var ips []net.IP
		for _, ad := range addrs {
			ips = append(ips, ad.IP)
		}
		return ips, err
	}
	var ips []net.IP
	var lastErr error
	for _, fam := range []int{6, 4} {
		p := set.port(fam)
		if p == nil {
			continue
		}
		addrs, err := p.look(ctx, name)
		if err != nil {
			lastErr = err
		}
		for _, ad := range addrs {
			ips = append(ips, net.IP(ad.Unmap().AsSlice()))
		}
	}
	if len(ips) == 0 && lastErr == nil {
		lastErr = fmt.Errorf("lookup %s: no address", name)
	}
	return ips, lastErr
}

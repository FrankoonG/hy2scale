package iface

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"
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

// Selection is the selected egress (no selection yet: what the system
// routes through now).
func (m *Manager) Selection() EgressConfig {
	m.mu.Lock()
	e := m.sel
	m.mu.Unlock()
	if e != nil {
		return *e
	}
	return EgressConfig{IPv4: DefaultRouteIface(4), IPv6: DefaultRouteIface(6)}
}

// SetEgress installs the egress selection (nil = none yet: the system
// routes everything).
func (m *Manager) SetEgress(e *EgressConfig) {
	if e != nil {
		c := *e
		e = &c
	}
	m.mu.Lock()
	m.sel = e
	m.mu.Unlock()
	m.refreshEgress()
}

// refreshEgress rebuilds the dialers after a selection or interface change.
func (m *Manager) refreshEgress() {
	m.mu.Lock()
	sel := m.sel
	m.mu.Unlock()
	set := &egressSet{legacy: sel == nil}
	if sel != nil {
		set.v4 = m.makePort(sel.IPv4, 4)
		set.v6 = m.makePort(sel.IPv6, 6)
	}
	m.egress.Store(set)
}

func (m *Manager) makePort(name string, fam int) *egressPort {
	if name == "" {
		return nil
	}
	nw := "ip4"
	if fam == 6 {
		nw = "ip6"
	}
	p := &egressPort{iface: name, fam: fam}
	if m.IsChild(name) {
		p.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			i := m.Child(name)
			if i == nil {
				return nil, fmt.Errorf("interface %s is not running", name)
			}
			return i.DialContext(ctx, network, addr)
		}
		p.look = func(ctx context.Context, host string) ([]netip.Addr, error) {
			i := m.Child(name)
			if i == nil {
				return nil, fmt.Errorf("interface %s is not running", name)
			}
			return i.LookupIP(ctx, nw, host)
		}
		return p
	}
	p.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return KernelDial(ctx, name, fam, network, addr, DefaultRouteIface(fam) != name)
	}
	p.look = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, nw, host)
	}
	return p
}

// KernelDial dials through a system interface. bind: pin the socket to the
// interface (it is not the one the system would choose).
func KernelDial(ctx context.Context, iface string, fam int, network, addr string, bind bool) (net.Conn, error) {
	d := net.Dialer{}
	if strings.HasPrefix(network, "tcp") {
		d.Timeout = 10 * time.Second
	} else {
		d.Timeout = 5 * time.Second
	}
	if bind {
		d.Control = bindControl(iface)
	}
	return d.DialContext(ctx, FamilyNet(network, fam), addr)
}

// FamilyNet narrows "tcp"/"udp" to one family.
func FamilyNet(network string, fam int) string {
	network = strings.TrimRight(network, "46")
	if fam == 4 {
		return network + "4"
	}
	return network + "6"
}

// Dial dials addr ("host:port") from this node through the egress, network
// "tcp" or "udp". The restrictions carried by ctx (proto.RestrictFrom)
// apply to the address actually dialled.
func (m *Manager) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	set := m.egress.Load()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// A restricted flow (access_guard.go): an address is checked here, a
	// name once resolved, before any socket is opened.
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if err := m.checkTarget(ctx, ip); err != nil {
			return nil, err
		}
	}
	if set == nil || set.legacy {
		return PlainDialCtl(ctx, network, addr, m.targetControl(ctx))
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		ip = ip.Unmap()
		if SystemRoutes(ip) {
			return PlainDial(ctx, network, addr)
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
		return p.dial(ctx, FamilyNet(network, fam), addr)
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
		if err := m.checkTarget(ctx, t.ip); err != nil {
			return nil, err
		}
		hp := net.JoinHostPort(t.ip.String(), port)
		if SystemRoutes(t.ip) {
			return PlainDial(ctx, network, hp)
		}
		return set.port(t.fam).dial(ctx, FamilyNet(network, t.fam), hp)
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
	return RaceDial(ctx, order, dialOne)
}

// RaceDial starts the next attempt 250 ms after the previous one (or as
// soon as it fails); the first connection wins.
func RaceDial[T any](ctx context.Context, order []T, dial func(context.Context, T) (net.Conn, error)) (net.Conn, error) {
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

// PlainDial dials through the system routes.
func PlainDial(ctx context.Context, network, addr string) (net.Conn, error) {
	return PlainDialCtl(ctx, network, addr, nil)
}

// PlainDialCtl: PlainDial with a socket Control hook (nil = none).
func PlainDialCtl(ctx context.Context, network, addr string, ctl func(string, string, syscall.RawConn) error) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second, Control: ctl}
	if strings.HasPrefix(network, "udp") {
		d.Timeout = 5 * time.Second
	}
	return d.DialContext(ctx, network, addr)
}

// Lookup resolves a name the way Dial does: each family through its own
// egress (the system resolver before any selection).
func (m *Manager) Lookup(ctx context.Context, name string) ([]net.IP, error) {
	set := m.egress.Load()
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

package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/FrankoonG/hy2scale/core/iface"
)

// Egress: the interface this node's direct flows leave through, one per
// address family (core/iface).

// dialDirect dials addr ("host:port") from this node through the egress;
// the restrictions carried by ctx apply.
func (a *App) dialDirect(ctx context.Context, network, addr string) (net.Conn, error) {
	return a.ifaces.Dial(ctx, network, addr)
}

// egressLookup resolves a name through the egress.
func (a *App) egressLookup(ctx context.Context, name string) ([]net.IP, error) {
	return a.ifaces.Lookup(ctx, name)
}

// egressSelection is the configured selection (none yet: what the system
// routes through now).
func (a *App) egressSelection() EgressConfig { return a.ifaces.Selection() }

// refreshEgress installs the configured selection.
func (a *App) refreshEgress() { a.ifaces.SetEgress(a.store.Get().Egress) }

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
		v4, v6 := iface.DefaultRouteIface(4), iface.DefaultRouteIface(6)
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

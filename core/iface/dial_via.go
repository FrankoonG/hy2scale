package iface

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/FrankoonG/hy2scale/core/sockmark"
)

// DialVia dials addr ("host:port") through the interface name — a child
// interface (its own stack), else a system interface the socket is pinned
// to — from the source address src ("" = the interface's choice). Used by
// the policy terminal `iface` (pinning needs Linux). The restrictions
// carried by ctx apply.
func (m *Manager) DialVia(ctx context.Context, name, src, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if err := m.checkTarget(ctx, ip); err != nil {
			return nil, err
		}
	}
	if m.IsChild(name) {
		i := m.Child(name)
		if i == nil {
			return nil, fmt.Errorf("interface %s is not running", name)
		}
		return i.DialContext(ctx, network, addr)
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	if strings.HasPrefix(network, "udp") {
		d.Timeout = 5 * time.Second
	}
	if src != "" {
		sip, err := netip.ParseAddr(src)
		if err != nil {
			return nil, fmt.Errorf("invalid source address %q", src)
		}
		sip = sip.Unmap()
		fam := 4
		if sip.Is6() {
			fam = 6
		}
		network = FamilyNet(network, fam)
		if strings.HasPrefix(network, "udp") {
			d.LocalAddr = &net.UDPAddr{IP: sip.AsSlice()}
		} else {
			d.LocalAddr = &net.TCPAddr{IP: sip.AsSlice()}
		}
	}
	bind := bindControl(name)
	tc := m.targetControl(ctx)
	d.Control = sockmark.Chain(func(nw, a string, c syscall.RawConn) error {
		if tc != nil {
			if err := tc(nw, a, c); err != nil {
				return err
			}
		}
		return bind(nw, a, c)
	})
	return d.DialContext(ctx, network, addr)
}

package policy

import (
	"context"
	"net"
	"net/netip"
	"strconv"
)

// Source describes where a flow came from, for layers that dial without
// knowing it (a relay or session target dialled by the exit engine).
type Source struct {
	Kind string // Kind*; "" = relay
	User string // internal user id
	Node string // relay: the node the flow comes from
	Addr string // client "ip:port"
}

type sourceKey struct{}
type decidedKey struct{}

// WithSource records the flow's source in ctx.
func WithSource(ctx context.Context, s Source) context.Context {
	return context.WithValue(ctx, sourceKey{}, s)
}

// SourceFrom returns the source WithSource recorded.
func SourceFrom(ctx context.Context) (Source, bool) {
	s, ok := ctx.Value(sourceKey{}).(Source)
	return s, ok
}

// WithDecided marks ctx as carrying a flow whose route is already decided:
// a local dial under it is the decision's own, not a new flow.
func WithDecided(ctx context.Context) context.Context {
	if Decided(ctx) {
		return ctx
	}
	return context.WithValue(ctx, decidedKey{}, true)
}

// Decided reports WithDecided.
func Decided(ctx context.Context) bool {
	v, _ := ctx.Value(decidedKey{}).(bool)
	return v
}

// SetSrcAddr fills SrcIP/SrcPort from "ip:port" or a net.Addr string.
func (c *Conn) SetSrcAddr(addr string) {
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		c.SrcIP, c.SrcPort = ap.Addr().Unmap(), ap.Port()
		return
	}
	if h, p, err := net.SplitHostPort(addr); err == nil {
		if a, err := netip.ParseAddr(h); err == nil {
			c.SrcIP = a.Unmap().WithZone("")
		}
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			c.SrcPort = uint16(n)
		}
		return
	}
	if a, err := netip.ParseAddr(addr); err == nil {
		c.SrcIP = a.Unmap().WithZone("")
	}
}

// SetDst fills DstIP/DstHost/DstPort from "host:port".
func (c *Conn) SetDst(addr string) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	if n, err := strconv.Atoi(p); err == nil && n >= 0 && n < 65536 {
		c.DstPort = uint16(n)
	}
	if a, err := netip.ParseAddr(h); err == nil {
		c.DstIP = a.Unmap().WithZone("")
		c.DstHost = ""
		return
	}
	c.DstHost = h
}

// DstDomain is the destination name known so far (requested or sniffed;
// "" = none) without triggering lookups.
func (c *Conn) DstDomain() string {
	if c.DstHost != "" {
		return NormalizeDomain(c.DstHost)
	}
	if c.Sniffed != "" {
		return NormalizeDomain(c.Sniffed)
	}
	return c.lazy.domain
}

// Target is the address a terminal dials: the requested name, else the
// destination address, with the port.
func (c *Conn) Target() string {
	port := strconv.Itoa(int(c.DstPort))
	if c.DstHost != "" {
		return net.JoinHostPort(c.DstHost, port)
	}
	return net.JoinHostPort(c.DstIP.String(), port)
}

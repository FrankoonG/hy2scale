//go:build linux

package vnic

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// LookupIP resolves host through the interface's own DNS servers (DHCP /
// RDNSS / static), over the interface's stack. network is "ip4" (A), "ip6"
// (AAAA) or "ip" (both, A first).
func (i *Iface) LookupIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	var types []dnsmessage.Type
	switch network {
	case "ip4":
		types = []dnsmessage.Type{dnsmessage.TypeA}
	case "ip6":
		types = []dnsmessage.Type{dnsmessage.TypeAAAA}
	case "ip":
		types = []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA}
	default:
		return nil, &net.DNSError{Err: "unsupported network " + network, Name: host}
	}
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		a = a.Unmap().WithZone("")
		if (network == "ip4" && !a.Is4()) || (network == "ip6" && a.Is4()) {
			return nil, &net.DNSError{Err: "address family mismatch", Name: host, IsNotFound: true}
		}
		return []netip.Addr{a}, nil
	}
	servers := i.dnsServers()
	if len(servers) == 0 {
		return nil, &net.DNSError{Err: fmt.Sprintf("no DNS server on interface %s", i.cfg.Name), Name: host}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
	}
	var out []netip.Addr
	var firstErr error
	for _, t := range types {
		addrs, err := i.resolve(ctx, servers, host, t)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		out = append(out, addrs...)
	}
	if len(out) == 0 {
		if firstErr == nil {
			firstErr = &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return nil, firstErr
	}
	return out, nil
}

// dnsServers lists the interface's DNS servers whose family has an address.
func (i *Iface) dnsServers() []netip.Addr {
	st := i.Status()
	var out []netip.Addr
	if len(st.IPv4.Addrs) > 0 {
		for _, s := range st.IPv4.DNS {
			out = append(out, s.Unmap())
		}
	}
	for _, s := range st.IPv6.DNS {
		if s.Is4() || s.Is4In6() {
			if len(st.IPv4.Addrs) > 0 {
				out = append(out, s.Unmap())
			}
		} else if len(st.IPv6.Addrs) > 0 || s.IsLinkLocalUnicast() {
			out = append(out, s)
		}
	}
	return out
}

func (i *Iface) resolve(ctx context.Context, servers []netip.Addr, host string, t dnsmessage.Type) ([]netip.Addr, error) {
	name := host
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	qn, err := dnsmessage.NewName(name)
	if err != nil {
		return nil, &net.DNSError{Err: "invalid name", Name: host}
	}
	var lastErr error
	for _, srv := range servers {
		if ctx.Err() != nil {
			break
		}
		sctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		addrs, rcode, err := i.query(sctx, srv, qn, t)
		cancel()
		if err != nil {
			lastErr = &net.DNSError{Err: err.Error(), Name: host, Server: srv.String(), IsTimeout: errors.Is(err, context.DeadlineExceeded)}
			continue
		}
		switch rcode {
		case dnsmessage.RCodeSuccess:
			return addrs, nil
		case dnsmessage.RCodeNameError:
			return nil, &net.DNSError{Err: "no such host", Name: host, Server: srv.String(), IsNotFound: true}
		default:
			lastErr = &net.DNSError{Err: "server answered " + rcode.String(), Name: host, Server: srv.String()}
		}
	}
	if lastErr == nil {
		lastErr = &net.DNSError{Err: ctx.Err().Error(), Name: host, IsTimeout: true}
	}
	return nil, lastErr
}

func (i *Iface) query(ctx context.Context, srv netip.Addr, qn dnsmessage.Name, t dnsmessage.Type) ([]netip.Addr, dnsmessage.RCode, error) {
	var idb [2]byte
	rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	q := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: qn, Type: t, Class: dnsmessage.ClassINET}},
	}
	pkt, err := q.Pack()
	if err != nil {
		return nil, 0, err
	}
	addr := netip.AddrPortFrom(srv, 53).String()
	resp, err := i.exchangeUDP(ctx, addr, pkt, id)
	if err != nil {
		return nil, 0, err
	}
	if resp.Header.Truncated {
		if resp, err = i.exchangeTCP(ctx, addr, pkt, id); err != nil {
			return nil, 0, err
		}
	}
	var out []netip.Addr
	for _, a := range resp.Answers {
		switch b := a.Body.(type) {
		case *dnsmessage.AResource:
			if t == dnsmessage.TypeA {
				out = append(out, netip.AddrFrom4(b.A))
			}
		case *dnsmessage.AAAAResource:
			if t == dnsmessage.TypeAAAA {
				out = append(out, netip.AddrFrom16(b.AAAA))
			}
		}
	}
	return out, resp.Header.RCode, nil
}

func (i *Iface) exchangeUDP(ctx context.Context, addr string, pkt []byte, id uint16) (*dnsmessage.Message, error) {
	c, err := i.DialContext(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Now()) })
	defer stop()
	if d, ok := ctx.Deadline(); ok {
		c.SetDeadline(d)
	}
	if _, err := c.Write(pkt); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	for {
		n, err := c.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		var m dnsmessage.Message
		if m.Unpack(buf[:n]) != nil || m.Header.ID != id || !m.Header.Response {
			continue
		}
		return &m, nil
	}
}

func (i *Iface) exchangeTCP(ctx context.Context, addr string, pkt []byte, id uint16) (*dnsmessage.Message, error) {
	c, err := i.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Now()) })
	defer stop()
	if d, ok := ctx.Deadline(); ok {
		c.SetDeadline(d)
	}
	b := make([]byte, 2+len(pkt))
	binary.BigEndian.PutUint16(b, uint16(len(pkt)))
	copy(b[2:], pkt)
	if _, err := c.Write(b); err != nil {
		return nil, err
	}
	var lb [2]byte
	if _, err := io.ReadFull(c, lb[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(lb[:]))
	if _, err := io.ReadFull(c, resp); err != nil {
		return nil, err
	}
	var m dnsmessage.Message
	if err := m.Unpack(resp); err != nil {
		return nil, err
	}
	if m.Header.ID != id {
		return nil, errors.New("DNS id mismatch")
	}
	return &m, nil
}

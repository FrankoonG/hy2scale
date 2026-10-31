// Package dnsproxy answers DNS queries taken over by the system capture
// (core/policy/capture, port 53) with the node's DNS settings, and records
// which name each answered address was handed out for ("domain → IPs",
// kept for the answer's TTL) so policy conditions on dst_domain can name
// the IP destinations of later connections.
package dnsproxy

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/FrankoonG/hy2scale/core/policy"
	"github.com/FrankoonG/hy2scale/core/sockmark"
)

// DefaultUpstreams is used when no DNS server is configured (the same
// default as the node's resolver).
const DefaultUpstreams = "1.1.1.1, 8.8.8.8"

// Config configures a Proxy.
type Config struct {
	// Upstreams: the servers to ask, "host:port" (nil or empty →
	// DefaultUpstreams).
	Upstreams func() []string
	// Dial opens a connection ("udp" or "tcp") to upstream on behalf of
	// the client c (nil = a direct, marked dial).
	Dial func(ctx context.Context, c *policy.Conn, network, upstream string) (net.Conn, error)
	// Timeout bounds one upstream exchange (default 3 s).
	Timeout time.Duration
	// Records: where answers are recorded (nil = a new default store).
	Records *Records
}

// Proxy answers queries.
type Proxy struct {
	cfg Config
	rec *Records
}

// New returns a proxy.
func New(cfg Config) *Proxy {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	rec := cfg.Records
	if rec == nil {
		rec = NewRecords(nil, 0, 0, 0)
	}
	return &Proxy{cfg: cfg, rec: rec}
}

// Records is the proxy's record store.
func (p *Proxy) Records() *Records { return p.rec }

// DomainOf names ip from the recorded answers ("" = none).
func (p *Proxy) DomainOf(ip netip.Addr) string { return p.rec.Lookup(ip) }

func (p *Proxy) upstreams() []string {
	var u []string
	if p.cfg.Upstreams != nil {
		u = p.cfg.Upstreams()
	}
	if len(u) == 0 {
		u = ParseUpstreams(DefaultUpstreams)
	}
	return u
}

func (p *Proxy) dial(ctx context.Context, c *policy.Conn, network, upstream string) (net.Conn, error) {
	if p.cfg.Dial != nil {
		return p.cfg.Dial(ctx, c, network, upstream)
	}
	return sockmark.DialContext(ctx, network, upstream)
}

// Answer returns the response to one query message: the first upstream
// that answers (over UDP; a truncated answer is asked again over TCP), or
// SERVFAIL when none does. Answers are recorded.
func (p *Proxy) Answer(ctx context.Context, c *policy.Conn, query []byte) ([]byte, error) {
	if len(query) < 12 {
		return nil, errors.New("short query")
	}
	var lastErr error
	for _, up := range p.upstreams() {
		resp, err := p.exchange(ctx, c, "udp", up, query)
		if err == nil && truncated(resp) {
			if r2, err2 := p.exchange(ctx, c, "tcp", up, query); err2 == nil {
				resp = r2
			}
		}
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		p.rec.Record(resp)
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no upstream")
	}
	if sf := ServFail(query); sf != nil {
		return sf, nil
	}
	return nil, lastErr
}

func (p *Proxy) exchange(ctx context.Context, c *policy.Conn, network, upstream string, query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	conn, err := p.dial(ctx, c, network, upstream)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	id := binary.BigEndian.Uint16(query[:2])
	if network == "tcp" {
		if err := writeTCPMsg(conn, query); err != nil {
			return nil, err
		}
		resp, err := readTCPMsg(conn)
		if err != nil {
			return nil, err
		}
		if len(resp) < 12 || binary.BigEndian.Uint16(resp[:2]) != id {
			return nil, errors.New("mismatched response")
		}
		return resp, nil
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if n >= 12 && binary.BigEndian.Uint16(buf[:2]) == id {
			return append([]byte(nil), buf[:n]...), nil
		}
	}
}

// ServeTCP serves DNS over TCP on conn until it ends or idles 30 s.
func (p *Proxy) ServeTCP(ctx context.Context, c *policy.Conn, conn net.Conn) {
	go func() { <-ctx.Done(); conn.Close() }()
	for {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		q, err := readTCPMsg(conn)
		if err != nil {
			return
		}
		resp, err := p.Answer(ctx, c, q)
		if err != nil || resp == nil {
			return
		}
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if writeTCPMsg(conn, resp) != nil {
			return
		}
	}
}

func readTCPMsg(r io.Reader) ([]byte, error) {
	var l [2]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(l[:]))
	if n == 0 {
		return nil, errors.New("empty message")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

func writeTCPMsg(w io.Writer, m []byte) error {
	if len(m) > 65535 {
		return errors.New("message too long")
	}
	b := make([]byte, 2+len(m))
	binary.BigEndian.PutUint16(b, uint16(len(m)))
	copy(b[2:], m)
	_, err := w.Write(b)
	return err
}

func truncated(resp []byte) bool { return len(resp) >= 3 && resp[2]&0x02 != 0 }

// ServFail builds a SERVFAIL response to query (nil when it does not
// parse).
func ServFail(query []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil
	}
	qs, err := p.AllQuestions()
	if err != nil {
		return nil
	}
	m := dnsmessage.Message{Header: dnsmessage.Header{ID: h.ID, Response: true, OpCode: h.OpCode,
		RecursionDesired: h.RecursionDesired, RecursionAvailable: true, RCode: dnsmessage.RCodeServerFailure},
		Questions: qs}
	b, err := m.Pack()
	if err != nil {
		return nil
	}
	return b
}

// ParseUpstreams splits a comma-separated server list ("8.8.8.8,
// [2606:4700::1111]:53") into host:port entries (missing port → 53).
func ParseUpstreams(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(p); err != nil {
			p = net.JoinHostPort(strings.Trim(p, "[]"), "53")
		}
		out = append(out, p)
	}
	return out
}

// UpstreamConn builds the policy connection of a query sent upstream on
// behalf of the client c: the client's source fields, the upstream as
// destination.
func UpstreamConn(c *policy.Conn, network, upstream string) *policy.Conn {
	u := &policy.Conn{Proto: network}
	if c != nil {
		u.SrcKind, u.User, u.SrcNode = c.SrcKind, c.User, c.SrcNode
		u.SrcIP, u.SrcPort, u.InIface, u.SrcMAC = c.SrcIP, c.SrcPort, c.InIface, c.SrcMAC
	}
	u.SetDst(upstream)
	return u
}

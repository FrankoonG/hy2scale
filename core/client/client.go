// Package client is the node-access client: it logs in to a hy2scale node
// (the gateway) with a user's hy2 credentials, reads the nodes the gateway
// sees, and dials targets through any of them — directly, or as carriers of
// a multi-path session (core/msess) for quality / aggregate exits.
//
// It holds no local node: nothing can connect to it, and the gateway never
// lists it. A hy2scale instance uses the same primitives for its
// "node access by user" upstreams.
package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/msess"
	"github.com/FrankoonG/hy2scale/core/proto"
	hyclient "github.com/apernet/hysteria/core/v2/client"
	coreErrs "github.com/apernet/hysteria/core/v2/errors"
)

// ErrRefused: the server did not accept node access with these credentials
// (not a hy2scale node, an older one, or the user may not).
var ErrRefused = errors.New("node access refused")

// Hello opens a hello stream on an hy2 client and returns the gateway's
// answer.
func Hello(ctx context.Context, c hyclient.Client, h proto.Hello) (proto.HelloReply, error) {
	var rep proto.HelloReply
	s, err := c.TCP(proto.StreamHello)
	if err != nil {
		var dialErr coreErrs.DialError
		if errors.As(err, &dialErr) {
			return rep, ErrRefused
		}
		return rep, err
	}
	defer s.Close()
	if d, ok := ctx.Deadline(); ok {
		s.SetDeadline(d)
	} else {
		s.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if err := proto.WriteJSON(s, h); err != nil {
		return rep, err
	}
	if err := proto.ReadJSON(s, &rep); err != nil || rep.Name == "" {
		// a node that answers hello by closing it (an older hy2scale, or one
		// that took the credentials as a full node's)
		return rep, ErrRefused
	}
	return rep, nil
}

// Config describes one gateway.
type Config struct {
	// Server is "host:port" of the gateway's hy2 server.
	Server   string
	Password string
	// TLS: SNI (default "hy2scale"), Insecure, CA (PEM).
	SNI      string
	Insecure bool
	CA       string
	// Bandwidth hints in bytes/sec (0 = 1 Gbps).
	MaxTx, MaxRx uint64
	// Instance identifies this client across its connections; Name is shown
	// to the gateway's operator.
	Instance string
	Name     string
	Version  string
}

// Session is one logged-in connection to a gateway.
type Session struct {
	cfg     Config
	c       hyclient.Client
	Gateway proto.HelloReply
}

// Dial connects to the gateway and says hello. ErrRefused: the gateway does
// not offer node access to these credentials.
func Dial(ctx context.Context, cfg Config) (*Session, error) {
	addr, err := net.ResolveUDPAddr("udp", cfg.Server)
	if err != nil {
		return nil, err
	}
	tc := hyclient.TLSConfig{InsecureSkipVerify: cfg.Insecure, ServerName: cfg.SNI}
	if tc.ServerName == "" {
		tc.ServerName = "hy2scale"
	}
	if cfg.CA != "" {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM([]byte(cfg.CA))
		tc.RootCAs = pool
	}
	_ = tls.VersionTLS13
	tx, rx := cfg.MaxTx, cfg.MaxRx
	if tx == 0 {
		tx = 125000000
	}
	if rx == 0 {
		rx = 125000000
	}
	c, _, err := hyclient.NewClient(&hyclient.Config{
		ServerAddr: addr,
		Auth:       cfg.Password,
		TLSConfig:  tc,
		QUICConfig: hyclient.QUICConfig{
			InitialStreamReceiveWindow: 8 << 20, MaxStreamReceiveWindow: 8 << 20,
			InitialConnectionReceiveWindow: 16 << 20, MaxConnectionReceiveWindow: 16 << 20,
		},
		BandwidthConfig: hyclient.BandwidthConfig{MaxTx: tx, MaxRx: rx},
	})
	if err != nil {
		return nil, err
	}
	rep, err := Hello(ctx, c, proto.Hello{Name: cfg.Name, Instance: cfg.Instance, Version: cfg.Version})
	if err != nil {
		c.Close()
		return nil, err
	}
	return &Session{cfg: cfg, c: c, Gateway: rep}, nil
}

// Close ends the session.
func (s *Session) Close() error { return s.c.Close() }

// Node is one node the gateway sees, with what it sees below it.
type Node struct {
	Name         string          `json:"name"`
	ExitNode     bool            `json:"exit_node"`
	Direction    string          `json:"direction"`
	Native       bool            `json:"native,omitempty"`
	LatencyMs    int             `json:"latency_ms"`
	Version      string          `json:"version,omitempty"`
	Connected    *bool           `json:"connected,omitempty"`
	Egress       json.RawMessage `json:"egress,omitempty"`
	RestrictTags bool            `json:"rt,omitempty"`
	Attrs        proto.Attrs     `json:"attrs,omitempty"`
	Children     []Node          `json:"children,omitempty"`
}

// Online: the gateway (or the parent) reports the node connected.
func (n Node) Online() bool { return n.Connected == nil || *n.Connected }

// Nodes reads the tree the gateway sees. Latencies of deeper levels are
// one-hop values measured by each parent.
func (s *Session) Nodes(ctx context.Context) ([]Node, error) {
	st, err := s.c.TCP(proto.StreamListPeer)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	if d, ok := ctx.Deadline(); ok {
		st.SetDeadline(d)
	}
	b, err := io.ReadAll(io.LimitReader(st, 8<<20))
	if err != nil {
		return nil, err
	}
	var out []Node
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Ping measures one round trip to the gateway.
func (s *Session) Ping(ctx context.Context) (time.Duration, error) {
	t0 := time.Now()
	st, err := s.c.TCP(proto.StreamPing)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(10 * time.Second))
	var b [1]byte
	if _, err := io.ReadFull(st, b[:]); err != nil {
		return 0, err
	}
	return time.Since(t0), nil
}

// Attrs returns the gateway's attributes (core/proto.Attrs).
func (s *Session) Attrs(ctx context.Context) (proto.Attrs, error) {
	st, err := s.c.TCP(proto.StreamAttrs)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	b, err := io.ReadAll(io.LimitReader(st, 64<<10))
	if err != nil {
		return nil, err
	}
	var a proto.Attrs
	return a, json.Unmarshal(b, &a)
}

// Egress returns the gateway's egress (public address and country per
// family, JSON).
func (s *Session) Egress(ctx context.Context) (json.RawMessage, error) {
	st, err := s.c.TCP(proto.StreamEgress)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	b, err := io.ReadAll(io.LimitReader(st, 64<<10))
	return b, err
}

// target: the stream address for addr through path (hops after the
// gateway; empty = the gateway itself is the exit).
func target(path []string, addr string) string {
	if len(path) == 0 {
		return addr
	}
	return proto.BuildViaAddr(strings.Join(path, "/"), addr)
}

// DialTCP dials addr ("host:port") leaving through path: the hops after
// the gateway, the last one being the exit (empty = exit at the gateway,
// following the user's route there).
func (s *Session) DialTCP(ctx context.Context, path []string, addr string) (net.Conn, error) {
	return s.c.TCP(target(path, addr))
}

// DialUDP opens one UDP flow to addr through path.
func (s *Session) DialUDP(ctx context.Context, path []string, addr string) (net.Conn, error) {
	uc, err := s.c.UDP()
	if err != nil {
		return nil, err
	}
	return &udpConn{uc: uc, addr: target(path, addr), peer: addr}, nil
}

// Path is a candidate route for multi-path sessions.
func (s *Session) Path(path []string) msess.Path {
	exit := s.Gateway.Name
	if len(path) > 0 {
		exit = path[len(path)-1]
	}
	name := strings.Join(append([]string{s.Gateway.Name}, path...), "/")
	return msess.Path{Name: name, Exit: exit, Capable: true,
		Dial: func(ctx context.Context) (net.Conn, error) {
			return s.c.TCP(target(path, proto.StreamMsess))
		},
		Open: func(ctx context.Context, hello []byte) (net.Conn, error) {
			return s.c.TCP(target(path, proto.MsessAddr(hello)))
		}}
}

// DialSession dials addr over a multi-path session across paths (each a
// hop list after the gateway, all ending at the same exit for aggregate).
// mode: msess.ModeSelector (quality) or msess.ModeBond (aggregate).
func (s *Session) DialSession(ctx context.Context, mode msess.Mode, paths [][]string, addr string) (net.Conn, error) {
	var ps []msess.Path
	for _, p := range paths {
		ps = append(ps, s.Path(p))
	}
	if len(ps) == 0 {
		return nil, fmt.Errorf("no paths")
	}
	return msess.Dial(ctx, msess.DialConfig{Mode: mode, Target: addr, Paths: ps, Prober: prober()})
}

var (
	proberOnce sync.Once
	shared     *msess.Prober
)

func prober() *msess.Prober {
	proberOnce.Do(func() { shared = msess.NewProber(nil) })
	return shared
}

// udpConn carries one UDP flow over the hy2 UDP session.
type udpConn struct {
	uc   interface {
		Send([]byte, string) error
		Receive() ([]byte, string, error)
		Close() error
	}
	addr string // request address (via-encoded)
	peer string
}

func (u *udpConn) Read(b []byte) (int, error) {
	data, _, err := u.uc.Receive()
	if err != nil {
		return 0, err
	}
	return copy(b, data), nil
}

func (u *udpConn) Write(b []byte) (int, error) {
	if err := u.uc.Send(b, u.addr); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (u *udpConn) Close() error                       { return u.uc.Close() }
func (u *udpConn) LocalAddr() net.Addr                { return &net.UDPAddr{} }
func (u *udpConn) RemoteAddr() net.Addr               { a, _ := net.ResolveUDPAddr("udp", u.peer); return a }
func (u *udpConn) SetDeadline(t time.Time) error      { return nil }
func (u *udpConn) SetReadDeadline(t time.Time) error  { return nil }
func (u *udpConn) SetWriteDeadline(t time.Time) error { return nil }

package node

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/msess"
	"github.com/FrankoonG/hy2scale/core/proto"
)

// Audit of relay traffic: flows other nodes send through this one (it is
// their exit, or a hop on their chain) are recorded with the peer they came
// from. Inbound peers are known by the address of their connection (the
// register stream), outbound ones by name on the streams they ask us to
// dial (NamedConn).

// inboundAddrs maps the client address of an inbound peer's connection,
// and its IP, to the peer's name.
type inboundAddrs struct {
	mu   sync.RWMutex
	addr map[string]string
	ip   map[string]string
}

func (a *inboundAddrs) set(addr, name string) {
	if addr == "" {
		return
	}
	a.mu.Lock()
	if a.addr == nil {
		a.addr, a.ip = map[string]string{}, map[string]string{}
	}
	if len(a.addr) > 8192 {
		a.addr = map[string]string{}
	}
	a.addr[addr] = name
	if h, _, err := net.SplitHostPort(addr); err == nil {
		if len(a.ip) > 4096 {
			a.ip = map[string]string{}
		}
		a.ip[h] = name
	}
	a.mu.Unlock()
}

func (a *inboundAddrs) del(addr, name string) {
	a.mu.Lock()
	if a.addr[addr] == name {
		delete(a.addr, addr)
	}
	a.mu.Unlock()
}

func (a *inboundAddrs) get(addr string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if n, ok := a.addr[addr]; ok {
		return n
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return a.ip[h]
	}
	return ""
}

// PeerNameAt: the inbound peer whose connection comes from addr ("" =
// unknown).
func (n *Node) PeerNameAt(addr string) string { return n.inAddrs.get(addr) }

// NamedConn is a stream known to come from a peer (by name).
type NamedConn struct {
	net.Conn
	Peer string
}

// PeerName: the peer the stream comes from.
func (c *NamedConn) PeerName() string { return c.Peer }

// Unwrap: the stream.
func (c *NamedConn) Unwrap() net.Conn { return c.Conn }

// NoHalfClose: relay streams carry no half-close.
func (c *NamedConn) NoHalfClose() bool {
	nh, ok := c.Conn.(interface{ NoHalfClose() bool })
	return ok && nh.NoHalfClose()
}

// SourceOf: the client address and peer name a relay stream comes from.
func (n *Node) SourceOf(c net.Conn) (src, peer string) {
	for i := 0; c != nil && i < 8; i++ {
		if pn, ok := c.(interface{ PeerName() string }); ok && peer == "" {
			peer = pn.PeerName()
		}
		if pa, ok := c.(interface{ PeerAddr() string }); ok && src == "" {
			src = pa.PeerAddr()
		}
		w, ok := c.(interface{ Unwrap() net.Conn })
		if !ok {
			break
		}
		c = w.Unwrap()
	}
	if src != "" && peer == "" {
		peer = n.PeerNameAt(src)
	}
	return src, peer
}

// Unwrap: the stream (audit source lookup).
func (c *RestrictConn) Unwrap() net.Conn { return c.Conn }

// Unwrap: the stream (audit source lookup).
func (c *prefixedConn) Unwrap() net.Conn { return c.Conn }

// BeginRelay starts the audit record of a flow arriving on stream (from a
// peer, or a node-access client the stream says it is) to target. nil when
// recording is off.
func (n *Node) BeginRelay(stream net.Conn, network, target string) *audit.Flow {
	if !audit.Enabled() {
		return nil
	}
	inb, user, ok := audit.IdentityOf(stream)
	if !ok {
		inb = audit.InRelay
	}
	f := audit.Begin(inb, network)
	src, peer := n.SourceOf(stream)
	if ok {
		peer = "" // a user's client is no peer
	}
	f.SetUser(user)
	f.SetSrc(src, peer)
	f.SetDst(target)
	return f
}

type srcKey struct{}

type srcInfo struct {
	stream net.Conn
}

// WithSource keeps the stream a session arrived on in ctx, for BeginRelayCtx.
func WithSource(ctx context.Context, stream net.Conn) context.Context {
	if !audit.Enabled() {
		return ctx
	}
	return context.WithValue(ctx, srcKey{}, srcInfo{stream})
}

// BeginRelayCtx is BeginRelay for the stream WithSource kept in ctx.
func (n *Node) BeginRelayCtx(ctx context.Context, network, target string) *audit.Flow {
	if !audit.Enabled() {
		return nil
	}
	si, _ := ctx.Value(srcKey{}).(srcInfo)
	if si.stream == nil {
		f := audit.Begin(audit.InRelay, network)
		f.SetDst(target)
		return f
	}
	return n.BeginRelay(si.stream, network, target)
}

// AuditTarget: the target of a relay stream as recorded (its restriction
// tag removed); "" when the stream is mesh-internal (sessions, bonded
// streams, control): those are recorded where their content is known.
func AuditTarget(addr string) string {
	base, _ := proto.ParseRestrict(addr)
	if IsRelayStream(base) || strings.HasPrefix(base, "_") {
		return ""
	}
	return base
}

// BeginTransit starts the record of a stream this node carries on along
// chain to target (a hop of another node's path). A session subflow is
// recorded by the target its HELLO names (a join by the target its
// session opened with, when this hop saw that); probes and control streams
// are not recorded. nil when recording is off.
func (n *Node) BeginTransit(stream net.Conn, chain, target string) *audit.Flow {
	if !audit.Enabled() {
		return nil
	}
	dst, network, ok := n.transitTarget(target)
	if !ok {
		return nil
	}
	f := n.BeginRelay(stream, network, dst)
	f.SetExitNode(ChainPath(chain), "")
	return f
}

func (n *Node) transitTarget(addr string) (dst, network string, ok bool) {
	base, _ := proto.ParseRestrict(addr)
	if proto.IsMsessAddr(base) {
		hi, ok := msess.DescribeHello(proto.MsessHello(base))
		if !ok {
			return "", "", false
		}
		switch hi.Kind {
		case "open":
			n.sessTargets.put(hi.SID, hi.Target, "tcp")
			return hi.Target, "tcp", true
		case "open-packet":
			n.sessTargets.put(hi.SID, hi.Target, "udp")
			return hi.Target, "udp", true
		case "join":
			t, nw := n.sessTargets.get(hi.SID)
			if nw == "" {
				nw = "tcp"
			}
			return t, nw, true
		}
		return "", "", false // probe
	}
	if t := AuditTarget(addr); t != "" {
		return t, "tcp", true
	}
	return "", "", false
}

// sessTargets remembers the targets of sessions opened through this hop,
// for the subflows that join them later.
type sessTargets struct {
	mu sync.Mutex
	m  map[[16]byte]sessTarget
}

type sessTarget struct {
	target, network string
	at              time.Time
}

func (s *sessTargets) put(sid [16]byte, target, network string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[[16]byte]sessTarget{}
	}
	if len(s.m) >= 8192 {
		cut := time.Now().Add(-time.Hour)
		for k, v := range s.m {
			if v.at.Before(cut) {
				delete(s.m, k)
			}
		}
		if len(s.m) >= 8192 {
			s.m = map[[16]byte]sessTarget{}
		}
	}
	s.m[sid] = sessTarget{target, network, time.Now()}
}

func (s *sessTargets) get(sid [16]byte) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.m[sid]
	return v.target, v.network
}

// ChainPath: a via chain as recorded (connection hints removed).
func ChainPath(chain string) string {
	parts := strings.Split(chain, "/")
	for i, p := range parts {
		parts[i], _ = SplitConnHint(p)
	}
	return strings.Join(parts, "/")
}

// Package server is the node's hy2 server ("本机"): other nodes connect to
// it with the node credentials, and the streams they open are relay
// streams (handed to core/node), bonded or session subflows and plain
// targets (served by core/exit). Credentials that are not a node's — users —
// are the business of an add-on (Users); without one they are refused.
//
// The server is optional: a node without one only dials out.
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/exit"
	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
	hyserver "github.com/apernet/hysteria/core/v2/server"
)

// NodeID is the auth ID of the node credentials. Users authenticate as
// UserPrefix + name.
const (
	NodeID     = "system"
	UserPrefix = "user:"
)

// Users serves the streams of user credentials (auth ID UserPrefix+name).
type Users interface {
	TCP(ctx context.Context, authID, peerAddr, addr string, bridged bool) (net.Conn, error)
	UDP(ctx context.Context, authID, peerAddr, addr string) (hyserver.UDPConn, error)
}

// ConnEvents is told about client connections (optional).
type ConnEvents interface {
	Connect(addr net.Addr, id string, tx uint64)
	Disconnect(addr net.Addr, id string, err error)
}

// Config of one server.
type Config struct {
	Conn net.PacketConn
	Cert tls.Certificate
	// Auth decides the auth ID of a client: NodeID for a node,
	// UserPrefix+name for a user.
	Auth hyserver.Authenticator
	// Events, Users: optional.
	Events ConnEvents
	Users  Users
	// WrapAPI wraps the stream of a remote-management request
	// (proto.StreamAPI) with its auth ID (optional).
	WrapAPI func(c net.Conn, authID string) net.Conn
	// LocalTCP dials a plain target of a node's stream from this node
	// (nil: through the exit engine with host-name recovery).
	LocalTCP func(ctx context.Context, addr string, bridged bool) (net.Conn, error)
	// Access grants node access by user (access.go); nil: none.
	Access Access
	// MaxChain bounds the via chains of node access (nil: 5).
	MaxChain func() int
}

// New builds the server; ctx bounds the streams it serves.
func New(ctx context.Context, n *relay.Node, ex *exit.Engine, cfg Config) (hyserver.Server, error) {
	ob := &outbound{n: n, ex: ex, cfg: cfg, ctx: ctx}
	return hyserver.NewServer(&hyserver.Config{
		Conn:        cfg.Conn,
		TLSConfig:   hyserver.TLSConfig{Certificates: []tls.Certificate{cfg.Cert}},
		EventLogger: &events{cfg.Events},
		QUICConfig: hyserver.QUICConfig{
			// 8 MB stream / 16 MB connection, smaller than upstream hy2 on
			// purpose: every hop of a relay chain holds its own window, so
			// large windows pre-acknowledge far more than the slowest link
			// carries before it pushes back.
			InitialStreamReceiveWindow:     8388608,
			MaxStreamReceiveWindow:         8388608,
			InitialConnectionReceiveWindow: 16777216,
			MaxConnectionReceiveWindow:     16777216,
			MaxIncomingStreams:             4096,
		},
		Authenticator: cfg.Auth,
		Outbound:      ob,
	})
}

// events stashes the auth ID and client address of each request for the
// Outbound call that follows on the same goroutine (stash.go).
type events struct{ ev ConnEvents }

func (e *events) Connect(addr net.Addr, id string, tx uint64) {
	if e.ev != nil {
		e.ev.Connect(addr, id, tx)
	}
}

func (e *events) Disconnect(addr net.Addr, id string, err error) {
	if e.ev != nil {
		e.ev.Disconnect(addr, id, err)
	}
}

func (e *events) TCPRequest(addr net.Addr, id, reqAddr string) { stash(addr, id) }

func (e *events) TCPError(addr net.Addr, id, reqAddr string, err error) { take() }

func (e *events) UDPRequest(addr net.Addr, id string, sessionID uint32, reqAddr string) {
	stash(addr, id)
}

func (e *events) UDPError(addr net.Addr, id string, sessionID uint32, err error) {}

type outbound struct {
	n   *relay.Node
	ex  *exit.Engine
	cfg Config
	ctx context.Context
}

// CheckUDP: hysteria's per-destination check of a UDP session's later
// packets. Every packet already goes through the session's WriteTo, which
// applies our own policy, so nothing is refused here.
func (o *outbound) CheckUDP(string) error { return nil }

func (o *outbound) TCP(reqAddr string) (net.Conn, error) {
	// An old relay's "#bridge=<id>" tag comes off before anything else.
	addr, _, bridged := relay.ParseBridgeAddr(reqAddr)
	authID, peerAddr := take()
	if strings.HasPrefix(authID, UserPrefix) {
		// A user credential never reaches mesh-internal streams (a via
		// would take it past its own exit, register would make it a
		// peer) unless node access grants the few access.go lists.
		if relay.IsRelayStream(addr) || exit.IsBondStream(addr) {
			g, err := o.grant(authID)
			if err != nil {
				return nil, err
			}
			return o.accessTCP(authID, peerAddr, addr, g)
		}
		if o.cfg.Users == nil {
			return nil, fmt.Errorf("%s: user streams are not served here", authID)
		}
		return o.cfg.Users.TCP(o.ctx, authID, peerAddr, addr, bridged)
	}
	// A target that arrived with restrictions, tagged by the node that
	// admitted the flow.
	if base, r := proto.ParseRestrict(addr); r != 0 && !relay.IsRelayStream(addr) {
		if exit.IsBondStream(base) {
			return o.bond(addr, peerAddr, ""), nil
		}
		return o.relayLocal(exit.WithRestrict(o.ctx, r), base, bridged, peerAddr)
	}
	if relay.IsRelayStream(addr) {
		c1, c2 := net.Pipe()
		var local net.Conn = c1
		if peerAddr != "" {
			local = &PeerAddrConn{Conn: c1, Peer: peerAddr}
		}
		if addr == proto.StreamAPI && o.cfg.WrapAPI != nil {
			local = o.cfg.WrapAPI(local, authID)
		}
		go o.n.HandleStream(o.ctx, addr, local)
		return c2, nil
	}
	if exit.IsBondStream(addr) {
		return o.bond(addr, peerAddr, ""), nil
	}
	return o.relayLocal(o.ctx, addr, bridged, peerAddr)
}

// relayLocal dials a node's plain target from here, recorded as the relay
// flow of the peer at peerAddr.
func (o *outbound) relayLocal(ctx context.Context, addr string, bridged bool, peerAddr string) (net.Conn, error) {
	af := o.n.BeginRelay(&PeerAddrConn{Peer: peerAddr}, "tcp", addr)
	c, err := o.local(audit.NewContext(ctx, af), addr, bridged)
	if err != nil {
		af.End(err)
		return nil, err
	}
	return audit.Track(af, c, false), nil
}

// grant: the node access of a user's stream, or why it is refused.
func (o *outbound) grant(authID string) (Grant, error) {
	if o.cfg.Access == nil {
		return Grant{}, fmt.Errorf("relay stream not permitted for %s", authID)
	}
	g, ok, err := o.cfg.Access.Grant(authID)
	switch {
	case err != nil:
		return Grant{}, err
	case !ok:
		return Grant{}, fmt.Errorf("relay stream not permitted for %s", authID)
	}
	return g, nil
}

func (o *outbound) bond(addr, peerAddr, user string) net.Conn {
	c1, c2 := net.Pipe()
	var c net.Conn = c1
	if peerAddr != "" {
		pc := &PeerAddrConn{Conn: c1, Peer: peerAddr}
		if user != "" {
			pc.Inbound, pc.User = audit.InAccess, user
		}
		c = pc
	}
	go func() {
		if err := o.ex.HandleBondStream(addr, c); err != nil {
			log.Printf("[bond-rx] handle error: %v", err)
			c1.Close()
		}
	}()
	return c2
}

// local dials a node's plain target from here. A stream to an IP waits for
// the client's first bytes: the client behind the node resolved elsewhere.
func (o *outbound) local(ctx context.Context, addr string, bridged bool) (net.Conn, error) {
	if o.cfg.LocalTCP != nil {
		return o.cfg.LocalTCP(ctx, addr, bridged)
	}
	return LocalTCP(ctx, o.ex, addr, bridged)
}

// LocalTCP is the default plain-target dial of a node's stream.
func LocalTCP(ctx context.Context, ex *exit.Engine, addr string, bridged bool) (net.Conn, error) {
	here := func(ctx context.Context, a string) (net.Conn, error) {
		return ex.DialLocalTCP(ctx, exit.RewriteLocalAddr(a))
	}
	if !bridged && exit.WantSniff(addr) {
		none := func(string) (string, []string, string, error) { return "", nil, "", nil }
		return exit.NewDeferredDial(func(peeked []byte, waited bool) (net.Conn, error) {
			return ex.DialSniffed(ctx, none, here, addr, peeked, waited, false)
		}), nil
	}
	return here(ctx, addr)
}

func (o *outbound) UDP(reqAddr string) (hyserver.UDPConn, error) {
	authID, peerAddr := take()
	if strings.HasPrefix(authID, UserPrefix) {
		if chain, target, ok := relay.ParseVia(reqAddr); ok {
			g, err := o.grant(authID)
			if err != nil {
				return nil, err
			}
			return o.accessUDP(authID, peerAddr, chain, target, g)
		}
		if o.cfg.Users == nil {
			return nil, fmt.Errorf("%s: user streams are not served here", authID)
		}
		return o.cfg.Users.UDP(o.ctx, authID, peerAddr, reqAddr)
	}
	src := &PeerAddrConn{Peer: peerAddr}
	// Multi-hop UDP: the chain loses one hop here.
	if chain, target, ok := relay.ParseVia(reqAddr); ok {
		var af *audit.Flow
		if t := relay.AuditTarget(target); t != "" {
			af = o.n.BeginRelay(src, "udp", t)
			af.SetExitNode(relay.ChainPath(chain), "")
		}
		c, err := o.n.DialUDPVia(o.ctx, strings.Split(chain, "/"), target)
		if err != nil {
			af.End(err)
			return nil, err
		}
		return &UDPAdapter{Conn: audit.Track(af, c, false), Addr: target}, nil
	}
	base, r := proto.ParseRestrict(reqAddr)
	af := o.n.BeginRelay(src, "udp", base)
	conn, err := o.ex.DialLocalUDP(audit.NewContext(exit.WithRestrict(o.ctx, r), af), exit.RewriteLocalAddr(base))
	if err != nil {
		af.End(err)
		return nil, err
	}
	return &realUDP{conn: audit.Track(af, conn, false), addr: reqAddr}, nil
}

// UDPAdapter serves a connected datagram flow (to Addr) as an hy2 UDP
// conn.
type UDPAdapter struct {
	Conn net.Conn
	Addr string
}

func (a *UDPAdapter) ReadFrom(b []byte) (int, string, error) {
	n, err := a.Conn.Read(b)
	return n, a.Addr, err
}

func (a *UDPAdapter) WriteTo(b []byte, _ string) (int, error) { return a.Conn.Write(b) }

func (a *UDPAdapter) Close() error { return a.Conn.Close() }

type realUDP struct {
	conn net.Conn
	addr string
}

func (u *realUDP) ReadFrom(b []byte) (int, string, error) {
	u.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	n, err := u.conn.Read(b)
	return n, u.addr, err
}

func (u *realUDP) WriteTo(b []byte, addr string) (int, error) { return u.conn.Write(b) }

func (u *realUDP) Close() error { return u.conn.Close() }

// PeerAddrConn is a relay stream whose RemoteAddr is the client's address
// (the stream itself is a pipe). Inbound/User: the node-access client the
// stream belongs to (audit identity; "" = a node's stream).
type PeerAddrConn struct {
	net.Conn
	Peer          string
	Inbound, User string
}

// AuditIdentity: the client identity the stream carries (audit.Carrier).
func (c *PeerAddrConn) AuditIdentity() (string, string) { return c.Inbound, c.User }

// Unwrap: the pipe.
func (c *PeerAddrConn) Unwrap() net.Conn { return c.Conn }

type strAddr string

func (s strAddr) Network() string { return "tcp" }
func (s strAddr) String() string  { return string(s) }

func (c *PeerAddrConn) RemoteAddr() net.Addr {
	if c.Peer == "" {
		return c.Conn.RemoteAddr()
	}
	return strAddr(c.Peer)
}

// PeerAddr: the client's address ("" = unknown).
func (c *PeerAddrConn) PeerAddr() string { return c.Peer }

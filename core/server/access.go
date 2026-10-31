package server

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/core/exit"
	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
	hyserver "github.com/apernet/hysteria/core/v2/server"
)

// Node access by user, gateway side (core/proto/access.go). A user the
// Access add-on grants node access may, besides proxying, open the streams
// listed there. Every via chain is validated here before it is forwarded:
// nodes further away only see this node's identity. The user's
// restrictions travel to 1.4.0+ exits as a tag on the target.

// Grant is what a user may do by node access.
type Grant struct {
	Mode     string         // proto.ModeRestricted / proto.ModeFull
	Restrict proto.Restrict // applied to its flows
}

// Access decides node access of user credentials (the users add-on).
type Access interface {
	// Grant: the node access of authID (ok=false: none). err refuses a user
	// that may not be served now (expired, over its limit).
	Grant(authID string) (g Grant, ok bool, err error)
	// Seen records a client's stream (h: its hello, when it says one).
	Seen(authID, peerAddr string, h *proto.Hello)
	// Hello: the hello the client at peerAddr said (nil = none yet).
	Hello(peerAddr string) *proto.Hello
	// Meter accounts a user's traffic stream.
	Meter(authID, peerAddr string, c net.Conn) net.Conn
}

// ErrAccessRefused: a stream node access does not permit.
var ErrAccessRefused = errors.New("not permitted for node access")

// AccessAuthID is the auth ID of a full-mode user's management stream:
// trusted like a full peer, recorded as that user.
func AccessAuthID(user string) string { return "access:" + user }

// accessTCP serves a reserved stream (or a bond stream) of a user with
// node access.
func (o *outbound) accessTCP(authID, peerAddr, addr string, g Grant) (net.Conn, error) {
	acc := o.cfg.Access
	acc.Seen(authID, peerAddr, nil)
	user := strings.TrimPrefix(authID, UserPrefix)
	full := g.Mode == proto.ModeFull
	r := g.Restrict
	switch {
	case full && addr == proto.StreamAPI:
		// management of this node (the relay-trust setting applies)
		return o.relayPipe(addr, peerAddr, AccessAuthID(user)), nil
	case full && (addr == proto.StreamPunch || addr == proto.StreamSpeed):
		return o.relayPipe(addr, peerAddr, ""), nil
	case addr == proto.StreamHello:
		c1, c2 := net.Pipe()
		go o.serveHello(authID, peerAddr, g, c1)
		return c2, nil
	case addr == proto.StreamListPeer || addr == proto.StreamPing ||
		addr == proto.StreamEgress || addr == proto.StreamWhoami || addr == proto.StreamAttrs:
		return o.relayPipe(addr, peerAddr, ""), nil
	case exit.IsBondStream(addr):
		base, cr := proto.ParseRestrict(addr)
		return acc.Meter(authID, peerAddr, o.bond(proto.WithRestrict(base, cr|r))), nil
	case proto.IsMsessAddr(addr):
		base, cr := proto.ParseRestrict(addr)
		return acc.Meter(authID, peerAddr, o.relayPipe(proto.WithRestrict(base, cr|r), peerAddr, "")), nil
	}
	chain, target, ok := proto.ParseVia(addr)
	if !ok {
		return nil, fmt.Errorf("%s: %w", addr, ErrAccessRefused)
	}
	fwd, err := o.accessVia(peerAddr, chain, target, g)
	if err != nil {
		log.Printf("[access] %s: refused %q: %v", user, addr, err)
		return nil, err
	}
	if _, t, _ := proto.ParseVia(fwd); proto.IsRelayStream(t) && !proto.IsMsessAddr(t) {
		return o.relayPipe(fwd, peerAddr, ""), nil // control stream (full mode): not metered
	}
	return acc.Meter(authID, peerAddr, o.relayPipe(fwd, peerAddr, "")), nil
}

// accessUDP serves a via-encoded UDP request of a user with node access.
func (o *outbound) accessUDP(authID, peerAddr, chain, target string, g Grant) (hyserver.UDPConn, error) {
	o.cfg.Access.Seen(authID, peerAddr, nil)
	fwd, err := o.accessVia(peerAddr, chain, target, g)
	if err != nil {
		log.Printf("[access] %s: refused UDP via %s: %v", strings.TrimPrefix(authID, UserPrefix), chain, err)
		return nil, err
	}
	nchain, ntarget, _ := proto.ParseVia(fwd)
	c, err := o.n.DialUDPVia(o.ctx, strings.Split(nchain, "/"), ntarget)
	if err != nil {
		return nil, err
	}
	return &UDPAdapter{Conn: o.cfg.Access.Meter(authID, peerAddr, c), Addr: target}, nil
}

// relayPipe hands a stream to the relay dispatcher as if it had arrived
// from a peer (the local end reports the client's address); a management
// stream carries authID.
func (o *outbound) relayPipe(addr, peerAddr, authID string) net.Conn {
	c1, c2 := net.Pipe()
	var local net.Conn = c1
	if peerAddr != "" {
		local = &PeerAddrConn{Conn: c1, Peer: peerAddr}
	}
	if authID != "" && o.cfg.WrapAPI != nil {
		local = o.cfg.WrapAPI(local, authID)
	}
	go o.n.HandleStream(o.ctx, addr, local)
	return c2
}

// serveHello answers an access client's hello.
func (o *outbound) serveHello(authID, peerAddr string, g Grant, c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	var h proto.Hello
	if err := proto.ReadJSON(c, &h); err != nil {
		return
	}
	o.cfg.Access.Seen(authID, peerAddr, &h)
	proto.WriteJSON(c, proto.HelloReply{Name: o.n.Name(), NodeID: o.n.NodeID(),
		Version: relay.NodeVersion, PV: relay.ProtocolVersion, ExitNode: o.n.IsExit(), Mode: g.Mode})
}

// accessVia validates a via request and returns the address to forward:
// the chain must be one this node shows (a direct peer, then the
// nested-authorised children it caches, no node twice, neither this node
// nor the client), the target a real address, a msess subflow or a bond
// stream (full mode: also management, punching and nested lists further
// away). The user's restrictions are added as a tag when every node on the
// chain understands it.
func (o *outbound) accessVia(peerAddr, chain, target string, g Grant) (string, error) {
	full := g.Mode == proto.ModeFull
	hops := strings.Split(chain, "/")
	parts := make([]string, len(hops))
	for i, h := range hops {
		parts[i], _ = proto.SplitConnHint(h)
	}
	maxDepth := 5
	if o.cfg.MaxChain != nil {
		maxDepth = o.cfg.MaxChain()
	}
	if len(parts) == 0 || len(parts) > maxDepth+1 {
		return "", fmt.Errorf("chain length %d", len(parts))
	}
	seen := map[string]bool{o.n.Name(): true, o.n.NodeID(): true}
	if h := o.cfg.Access.Hello(peerAddr); h != nil {
		if h.Name != "" {
			seen[h.Name] = true
		}
		if h.NodeID != "" {
			seen[h.NodeID] = true
		}
	}
	for i, p := range parts {
		if p == "" || seen[p] {
			return "", fmt.Errorf("hop %q repeats or is not allowed", p)
		}
		seen[p] = true
		if i == 0 {
			if !o.n.HasPeer(p) {
				return "", fmt.Errorf("%s is not a peer", p)
			}
			continue
		}
		prefix := strings.Join(parts[:i], "/")
		if !o.ex.Authorised(parts[:i+1]) {
			return "", fmt.Errorf("%s is not shown here", strings.Join(parts[:i+1], "/"))
		}
		kids, ok := o.n.PeersOfCached(prefix)
		found := false
		for _, k := range kids {
			if ok && k.Name == p {
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%s is not shown here", strings.Join(parts[:i+1], "/"))
		}
	}
	if o.ex.PathDisabled(strings.Join(parts, "/")) {
		return "", fmt.Errorf("%s is disabled", strings.Join(parts, "/"))
	}
	// a native (plain hysteria) node can only be the last hop
	for i := 0; i < len(parts)-1; i++ {
		if p, _ := o.ex.NodeInfoAt(parts[:i+1]); p.Native {
			return "", fmt.Errorf("%s cannot relay", parts[i])
		}
	}
	base, cr := proto.ParseRestrict(target)
	switch {
	case proto.IsMsessAddr(base), exit.IsBondStream(base):
	case full && (base == proto.StreamAPI || base == proto.StreamPunch || base == proto.StreamListPeer ||
		base == proto.StreamSpeed):
		return proto.BuildViaAddr(chain, base), nil
	case proto.IsRelayStream(base):
		return "", fmt.Errorf("target %s: %w", base, ErrAccessRefused)
	default:
		if _, _, err := net.SplitHostPort(base); err != nil {
			return "", fmt.Errorf("target %q: %v", base, err)
		}
	}
	tagged := base
	if r := g.Restrict | cr; r != 0 && o.ex.ChainTags(parts) {
		tagged = proto.WithRestrict(base, r)
	}
	return proto.BuildViaAddr(chain, tagged), nil
}

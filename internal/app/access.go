package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/proto"
	"github.com/FrankoonG/hy2scale/internal/relay"
	hyclient "github.com/apernet/hysteria/core/v2/client"
	hyserver "github.com/apernet/hysteria/core/v2/server"
)

// Node access by user, gateway side (core/proto/access.go). A user with
// NodeAccess may, besides proxying, open the streams listed there. Every
// via chain is validated here before it is forwarded: nodes further away
// only see this node's identity. The user's restrictions travel to 1.4.0+
// exits as a tag on the target.

// accessState: the access clients connected now (one per connection).
type accessState struct {
	mu      sync.Mutex
	clients map[string]*AccessClient // connection (remote address) → client
}

// AccessClient is a connected access client as shown in the user's devices.
type AccessClient struct {
	User     string      `json:"user"`
	Addr     string      `json:"addr"`
	Hello    proto.Hello `json:"hello"`
	Since    time.Time   `json:"since"`
	LastSeen time.Time   `json:"last_seen"`
}

func newAccessState() *accessState {
	return &accessState{clients: map[string]*AccessClient{}}
}

// AccessClients lists the access clients that said hello in the last
// minutes, one row per instance.
func (a *App) AccessClients() []AccessClient {
	s := a.acc
	s.mu.Lock()
	defer s.mu.Unlock()
	byInst := map[string]*AccessClient{}
	var out []AccessClient
	for k, c := range s.clients {
		if time.Since(c.LastSeen) > 3*time.Minute {
			delete(s.clients, k)
			continue
		}
		key := c.User + "|" + c.Hello.Instance
		if c.Hello.Instance == "" {
			key = c.User + "|" + c.Addr
		}
		if prev := byInst[key]; prev != nil {
			if c.LastSeen.After(prev.LastSeen) {
				prev.LastSeen = c.LastSeen
			}
			continue
		}
		cp := *c
		byInst[key] = &cp
	}
	for _, c := range byInst {
		out = append(out, *c)
	}
	return out
}

func (a *App) touchAccess(user, addr string, h *proto.Hello) *AccessClient {
	s := a.acc
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[addr]
	now := time.Now()
	if c == nil || c.User != user {
		c = &AccessClient{User: user, Addr: addr, Since: now}
		s.clients[addr] = c
	}
	if h != nil {
		c.Hello = *h
	}
	c.LastSeen = now
	return c
}

func (a *App) accessClient(addr string) *AccessClient {
	s := a.acc
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clients[addr]
}

// nodeAccessUser: authID is a user allowed node access.
func (a *App) nodeAccessUser(authID string) bool {
	name, ok := strings.CutPrefix(authID, "user:")
	if !ok {
		return false
	}
	a.usersMu.RLock()
	u, found := a.userIndex[name]
	a.usersMu.RUnlock()
	return found && u.NodeAccess
}

// accessMode: the node-access mode of a user ("" = no node access).
func (a *App) accessMode(user string) string {
	a.usersMu.RLock()
	u, found := a.userIndex[user]
	a.usersMu.RUnlock()
	switch {
	case !found || !u.NodeAccess:
		return ""
	case u.NodeAccessMode == proto.ModeFull:
		return proto.ModeFull
	}
	return proto.ModeRestricted
}

var errAccessRefused = errors.New("not permitted for node access")

// accessTCP serves a reserved stream (or a bond stream) opened by a
// node-access user.
func (o *nodeOutbound) accessTCP(authID, peerAddr, addr string) (net.Conn, error) {
	a := o.app
	if err := a.hy2UserUsable(authID); err != nil {
		return nil, err
	}
	user := strings.TrimPrefix(authID, "user:")
	a.touchAccess(user, peerAddr, nil)
	r := a.userRestrict(user)
	full := a.accessMode(user) == proto.ModeFull
	switch {
	case full && addr == proto.StreamAPI:
		// management of this node: trusted like a full peer (the
		// relay-trust setting applies), recorded as this user
		return o.relayPipeAs(addr, peerAddr, "access:"+user), nil
	case full && addr == proto.StreamPunch:
		return o.relayPipe(addr, peerAddr), nil
	case addr == proto.StreamHello:
		c1, c2 := net.Pipe()
		go a.serveHello(user, peerAddr, c1)
		return c2, nil
	case addr == proto.StreamListPeer || addr == proto.StreamPing ||
		addr == proto.StreamEgress || addr == proto.StreamWhoami:
		return o.relayPipe(addr, peerAddr), nil
	case IsBondStream(addr):
		base, cr := proto.ParseRestrict(addr)
		c1, c2 := net.Pipe()
		go func() {
			if err := a.handleBondStream(proto.WithRestrict(base, cr|r), c1); err != nil {
				c1.Close()
			}
		}()
		return a.meterHy2As(authID, peerAddr, "hy2-node", c2), nil
	case proto.IsMsessAddr(addr):
		base, cr := proto.ParseRestrict(addr)
		return a.meterHy2As(authID, peerAddr, "hy2-node", o.relayPipe(proto.WithRestrict(base, cr|r), peerAddr)), nil
	}
	chain, target, ok := proto.ParseVia(addr)
	if !ok {
		return nil, fmt.Errorf("%s: %w", addr, errAccessRefused)
	}
	fwd, err := a.accessVia(user, peerAddr, chain, target, r)
	if err != nil {
		log.Printf("[access] %s: refused %q: %v", user, addr, err)
		return nil, err
	}
	if _, t, _ := proto.ParseVia(fwd); proto.IsRelayStream(t) && !proto.IsMsessAddr(t) {
		return o.relayPipe(fwd, peerAddr), nil // control stream (full mode): not metered
	}
	return a.meterHy2As(authID, peerAddr, "hy2-node", o.relayPipe(fwd, peerAddr)), nil
}

// accessUDP serves a via-encoded UDP request of a node-access user.
func (o *nodeOutbound) accessUDP(authID, peerAddr, chain, target string) (hyserver.UDPConn, error) {
	a := o.app
	if err := a.hy2UserUsable(authID); err != nil {
		return nil, err
	}
	user := strings.TrimPrefix(authID, "user:")
	a.touchAccess(user, peerAddr, nil)
	fwd, err := a.accessVia(user, peerAddr, chain, target, a.userRestrict(user))
	if err != nil {
		log.Printf("[access] %s: refused UDP via %s: %v", user, chain, err)
		return nil, err
	}
	nchain, ntarget, _ := proto.ParseVia(fwd)
	c, err := o.node.DialUDPVia(o.ctx, strings.Split(nchain, "/"), ntarget)
	if err != nil {
		return nil, err
	}
	return &netConnUDPAdapter{nc: a.meterHy2As(authID, peerAddr, "hy2-node", c), addr: target}, nil
}

// relayPipe hands a stream to the relay dispatcher as if it had arrived
// from a peer (the local end reports the client's address).
func (o *nodeOutbound) relayPipe(addr, peerAddr string) net.Conn {
	return o.relayPipeAs(addr, peerAddr, "")
}

// relayPipeAs: relayPipe whose API stream carries authID (see RelayAuthConn).
func (o *nodeOutbound) relayPipeAs(addr, peerAddr, authID string) net.Conn {
	c1, c2 := net.Pipe()
	var local net.Conn = c1
	if peerAddr != "" {
		local = &peerAddrConn{Conn: c1, peerAddr: peerAddr}
	}
	if authID != "" {
		local = &RelayAuthConn{Conn: local, AuthID: authID}
	}
	go o.node.HandleStream(o.ctx, addr, local)
	return c2
}

// serveHello answers an access client's hello.
func (a *App) serveHello(user, peerAddr string, c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	var h proto.Hello
	if err := proto.ReadJSON(c, &h); err != nil {
		return
	}
	a.touchAccess(user, peerAddr, &h)
	cfg := a.store.Get()
	proto.WriteJSON(c, proto.HelloReply{Name: a.node.Name(), NodeID: a.node.NodeID(),
		Version: relay.NodeVersion, PV: relay.ProtocolVersion, ExitNode: cfg.ExitNode,
		Mode: a.accessMode(user)})
}

// accessVia validates an access client's via request and returns the
// address to forward: the chain must be one this node shows (direct peer,
// then nested-authorised children it caches, no node twice, neither this
// node nor the client), the target a real address, a msess subflow or a
// bond stream. The user's restrictions are added as a tag when every node
// on the chain understands it.
func (a *App) accessVia(user, peerAddr, chain, target string, r proto.Restrict) (string, error) {
	cfg := a.store.Get()
	full := a.accessMode(user) == proto.ModeFull
	hops := strings.Split(chain, "/")
	parts := make([]string, len(hops))
	for i, h := range hops {
		parts[i], _ = proto.SplitConnHint(h)
	}
	maxDepth := cfg.MaxNestedDepth
	if maxDepth <= 0 {
		maxDepth = 5
	}
	if len(parts) == 0 || len(parts) > maxDepth+1 {
		return "", fmt.Errorf("chain length %d", len(parts))
	}
	seen := map[string]bool{a.node.Name(): true, cfg.NodeID: true}
	if c := a.accessClient(peerAddr); c != nil {
		if c.Hello.Name != "" {
			seen[c.Hello.Name] = true
		}
		if c.Hello.NodeID != "" {
			seen[c.Hello.NodeID] = true
		}
	}
	for i, p := range parts {
		if p == "" || seen[p] {
			return "", fmt.Errorf("hop %q repeats or is not allowed", p)
		}
		seen[p] = true
		if i == 0 {
			if !a.node.HasPeer(p) {
				return "", fmt.Errorf("%s is not a peer", p)
			}
			continue
		}
		prefix := strings.Join(parts[:i], "/")
		if !isPathAuthorised(parts[:i+1], cfg) {
			return "", fmt.Errorf("%s is not shown here", strings.Join(parts[:i+1], "/"))
		}
		kids, ok := a.node.PeersOfCached(prefix)
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
	if a.isExitPathDisabled(strings.Join(parts, "/")) {
		return "", fmt.Errorf("%s is disabled", strings.Join(parts, "/"))
	}
	// a native (plain hysteria) node can only be the last hop
	for i := 0; i < len(parts)-1; i++ {
		if _, native := a.nodeVersionAt(parts[:i+1]); native {
			return "", fmt.Errorf("%s cannot relay", parts[i])
		}
	}
	base, cr := proto.ParseRestrict(target)
	switch {
	case proto.IsMsessAddr(base), IsBondStream(base):
	case full && (base == proto.StreamAPI || base == proto.StreamPunch || base == proto.StreamListPeer):
		// management / punching / nested lists further away (full mode):
		// the node there sees this node as the caller
		return proto.BuildViaAddr(chain, base), nil
	case proto.IsRelayStream(base):
		return "", fmt.Errorf("target %s: %w", base, errAccessRefused)
	default:
		if _, _, err := net.SplitHostPort(base); err != nil {
			return "", fmt.Errorf("target %q: %v", base, err)
		}
	}
	tagged := base
	if r|cr != 0 && a.chainTags(parts) {
		tagged = proto.WithRestrict(base, r|cr)
	}
	return proto.BuildViaAddr(chain, tagged), nil
}

// chainTags: every node of parts applies restriction tags.
func (a *App) chainTags(parts []string) bool {
	for i := range parts {
		if p, ok := a.nodeInfoAt(parts[:i+1]); !ok || p.Native || !p.RestrictTags {
			return false
		}
	}
	return true
}

// tagTarget adds the restrictions carried by ctx to a target dialled
// through parts, when every node on it understands the tag.
func (a *App) tagTarget(ctx context.Context, parts []string, addr string) string {
	r := restrictOf(ctx)
	if r == 0 || len(parts) == 0 || !a.chainTags(parts) {
		return addr
	}
	return proto.WithRestrict(addr, r)
}

// ---------------------------------------------------------------- client side

// AccessUser is ClientEntry.Access for node access by user. Not needed:
// connecting is adaptive (register, then hello, then plain hy2); an entry
// with it only skips the register attempt.
const AccessUser = "user"

// accessNote: the last outcome of node access per entry (client Addr):
// "" = accepted, else why the node is used as a plain hy2 server.
var accessNotes sync.Map

func (a *App) noteAccess(addr string, err error) {
	switch {
	case err == nil || !errors.Is(err, relay.ErrAccessRefused):
		accessNotes.Store(addr, "")
	default:
		accessNotes.Store(addr, "refused")
	}
}

// AccessRefused: the node of entry addr refused node access (it is used as
// a plain hy2 server).
func (a *App) AccessRefused(addr string) bool {
	v, _ := accessNotes.Load(addr)
	return v == "refused"
}

// showAccessTree enables nested discovery for a gateway reached by node
// access the first time it is seen (its tree is what it is used for).
func (a *App) showAccessTree(name string) {
	if _, ok := a.store.Get().Peers[name]; ok {
		return
	}
	a.store.Update(func(c *Config) {
		if c.Peers == nil {
			c.Peers = map[string]PeerConfig{}
		}
		if _, ok := c.Peers[name]; !ok {
			c.Peers[name] = PeerConfig{Nested: true}
		}
	})
	a.node.SetNestedDiscovery(name, true)
}

// verifyAccessConn checks that an extra connection of an access entry
// reaches the same gateway.
func (a *App) verifyAccessConn(ctx context.Context, c hyclient.Client, expected string) string {
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rep, err := relay.AccessHello(hctx, c, proto.Hello{Name: a.node.Name(), NodeID: a.node.NodeID(),
		Instance: a.node.InstanceID(), Version: relay.NodeVersion})
	switch {
	case errors.Is(err, relay.ErrAccessRefused):
		return "native"
	case err != nil:
		return "offline"
	case rep.Name != expected:
		return "mismatch"
	}
	return "online"
}

// restrictedDial dials a target this node reached with a restriction tag.
func (a *App) restrictedDial(ctx context.Context, network, addr string, r proto.Restrict) (net.Conn, error) {
	return a.dialDirect(withRestrict(ctx, r), network, rewriteLocalAddr(addr))
}

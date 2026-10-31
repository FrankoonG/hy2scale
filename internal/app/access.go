package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/proto"
	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/server"
	hyclient "github.com/apernet/hysteria/core/v2/client"
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

// accessGrants: node access of user credentials for the server
// (core/server.Access).
type accessGrants struct{ a *App }

func (g accessGrants) Grant(authID string) (server.Grant, bool, error) {
	a := g.a
	if !a.nodeAccessUser(authID) {
		return server.Grant{}, false, nil
	}
	if err := a.hy2UserUsable(authID); err != nil {
		return server.Grant{}, false, err
	}
	user := strings.TrimPrefix(authID, "user:")
	return server.Grant{Mode: a.accessMode(user), Restrict: a.userRestrict(user)}, true, nil
}

func (g accessGrants) Seen(authID, peerAddr string, h *proto.Hello) {
	g.a.touchAccess(strings.TrimPrefix(authID, "user:"), peerAddr, h)
}

func (g accessGrants) Hello(peerAddr string) *proto.Hello {
	if c := g.a.accessClient(peerAddr); c != nil {
		h := c.Hello
		return &h
	}
	return nil
}

func (g accessGrants) Meter(authID, peerAddr string, c net.Conn) net.Conn {
	return g.a.meterHy2As(authID, peerAddr, "hy2-node", c, nil) // recorded by the relay layer
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
	return a.dialRelayLocal(withRestrict(ctx, r), network, rewriteLocalAddr(addr))
}

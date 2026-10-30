package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	accessclient "github.com/FrankoonG/hy2scale/core/client"
	"github.com/FrankoonG/hy2scale/core/proto"
	hyclient "github.com/apernet/hysteria/core/v2/client"
)

// Restrictions arriving with a stream (core/proto/access.go): a msess
// subflow hands them to the msess handler on its connection; a target this
// node dials for a reverse-dial request goes through the restricted dialer.

// RestrictConn is a stream that arrived with restrictions for what its
// session may reach.
type RestrictConn struct {
	net.Conn
	R proto.Restrict
}

func restrictConn(c net.Conn, r proto.Restrict) net.Conn {
	if r == 0 {
		return c
	}
	return &RestrictConn{Conn: c, R: r}
}

// RestrictOf: the restrictions a stream arrived with.
func RestrictOf(c net.Conn) proto.Restrict {
	if rc, ok := c.(*RestrictConn); ok {
		return rc.R
	}
	return 0
}

// SetRestrictedDial registers how this node dials a target under
// restrictions (the app checks the destination after name resolution).
func (n *Node) SetRestrictedDial(f func(ctx context.Context, network, addr string, r proto.Restrict) (net.Conn, error)) {
	n.restrictedDial = f
}

var errNoRestrictedDial = errors.New("relay: restricted target without a restricted dialer")

// ErrAccessRefused: the node did not accept node access with these
// credentials (not a hy2scale node, an older one, or the user may not).
var ErrAccessRefused = accessclient.ErrRefused

// AccessHello opens a hello stream on client and returns the gateway's
// answer (core/client).
func AccessHello(ctx context.Context, client hyclient.Client, h proto.Hello) (proto.HelloReply, error) {
	return accessclient.Hello(ctx, client, h)
}

// AttachAccess attaches a gateway reached with a user's credentials (node
// access by user). Unlike AttachTo it never registers: the gateway does not
// list this node, cannot open streams toward it, and the peer is marked
// Restricted. onID receives the gateway's name; returning false aborts.
// The peer stays until ctx ends or the health check drops it.
func (n *Node) AttachAccess(ctx context.Context, peerName string, client hyclient.Client, onID func(string) bool) error {
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	rep, err := AccessHello(hctx, client, proto.Hello{Name: n.name, NodeID: n.nodeID,
		Instance: n.InstanceID(), Version: NodeVersion})
	cancel()
	if err != nil {
		return err
	}
	name := rep.Name
	if onID != nil && !onID(name) {
		return fmt.Errorf("relay: gateway %s refused (name %q)", peerName, name)
	}
	childCtx, childCancel := context.WithCancel(ctx)
	p := &peer{
		info: PeerInfo{Name: name, Direction: "outbound", Version: rep.Version, NodeID: rep.NodeID,
			PV: rep.PV, ExitNode: rep.ExitNode, EgressCapable: true, RestrictTags: true,
			Restricted: rep.Mode != proto.ModeFull, Invisible: true,
			Incompatible: !isCompatible(rep.Version), Unsupported: rep.PV > ProtocolVersion},
		client:  client,
		waiting: make(map[string]chan net.Conn),
		ctx:     childCtx,
		cancel:  childCancel,
		dialNak: true,
	}
	n.mu.Lock()
	old := n.peers[name]
	if old != nil && old.info.Direction == "inbound" {
		// the same node also dials us as a full peer: keep that one
		n.mu.Unlock()
		childCancel()
		return fmt.Errorf("relay: %s is already a full peer", name)
	}
	if old != nil && len(old.extraConns) > 0 {
		p.extraConns, p.connAddrs, p.connStatuses = old.extraConns, old.connAddrs, old.connStatuses
	}
	n.peers[name] = p
	n.mu.Unlock()
	if old != nil {
		if old.cancel != nil {
			old.cancel()
		}
		if old.client != nil {
			old.client.Close()
		}
	}
	defer func() {
		childCancel()
		n.mu.Lock()
		if cur, ok := n.peers[name]; ok && cur == p {
			delete(n.peers, name)
		}
		n.mu.Unlock()
	}()
	select {
	case <-childCtx.Done():
		return fmt.Errorf("relay: gateway %s: health check triggered disconnect", peerName)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsInvisible: peer name is attached by node access by user.
func (n *Node) IsInvisible(name string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	p, ok := n.peers[name]
	return ok && p.info.Invisible
}

// IsRestricted: peer name is reached through node access by user in
// restricted mode (traffic only).
func (n *Node) IsRestricted(name string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	p, ok := n.peers[name]
	return ok && p.info.Restricted
}

// InstanceID identifies this node's process across its connections to a
// gateway (its access device).
func (n *Node) InstanceID() string {
	n.instOnce.Do(func() {
		var b [8]byte
		rand.Read(b[:])
		n.inst = hex.EncodeToString(b[:])
	})
	if n.nodeID != "" {
		return n.nodeID + "-" + n.inst
	}
	return n.inst
}

func (n *Node) dialRestricted(ctx context.Context, network, addr string, r proto.Restrict) (net.Conn, error) {
	if n.restrictedDial == nil {
		return nil, errNoRestrictedDial
	}
	return n.restrictedDial(ctx, network, addr, r)
}

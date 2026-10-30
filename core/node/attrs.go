package node

import (
	"encoding/json"
	"sync"

	"github.com/FrankoonG/hy2scale/core/proto"
)

// Node attributes (core/proto.Attrs): this node's are set by the
// application and served on StreamAttrs; a direct peer's are fetched by the
// application (AttrsOf) and recorded here (SetPeerAttrs), and travel on in
// the peer lists this node serves.

type attrStore struct {
	mu    sync.RWMutex
	local proto.Attrs
	peers sync.Map // peer name → proto.Attrs
}

// SetAttr sets one of this node's attributes (nil removes it).
func (n *Node) SetAttr(key string, value json.RawMessage) {
	n.attrs.mu.Lock()
	defer n.attrs.mu.Unlock()
	if n.attrs.local == nil {
		n.attrs.local = proto.Attrs{}
	}
	if value == nil {
		delete(n.attrs.local, key)
		return
	}
	n.attrs.local[key] = value
}

// Attrs returns a copy of this node's attributes.
func (n *Node) Attrs() proto.Attrs {
	n.attrs.mu.RLock()
	defer n.attrs.mu.RUnlock()
	out := make(proto.Attrs, len(n.attrs.local))
	for k, v := range n.attrs.local {
		out[k] = v
	}
	return out
}

func (n *Node) attrsJSON() []byte {
	b, _ := json.Marshal(n.Attrs())
	return b
}

// SetPeerAttrs records a direct peer's attributes (nil clears them).
func (n *Node) SetPeerAttrs(peerName string, a proto.Attrs) {
	if a == nil {
		n.attrs.peers.Delete(peerName)
		return
	}
	n.attrs.peers.Store(peerName, a)
}

// PeerAttrs: a direct peer's attributes as last recorded.
func (n *Node) PeerAttrs(peerName string) proto.Attrs {
	if v, ok := n.attrs.peers.Load(peerName); ok {
		return v.(proto.Attrs)
	}
	return nil
}

// AttrsOf fetches a direct peer's attributes: StreamAttrs, or for a peer
// that only serves its egress, that as AttrEgress.
func (n *Node) AttrsOf(peerName string) (proto.Attrs, error) {
	n.mu.RLock()
	p, ok := n.peers[peerName]
	var capable, egress bool
	if ok {
		capable, egress = p.info.AttrsCapable, p.info.EgressCapable
	}
	n.mu.RUnlock()
	if capable {
		b, err := n.queryStream(peerName, proto.StreamAttrs, 64<<10)
		if err != nil {
			return nil, err
		}
		var a proto.Attrs
		if err := json.Unmarshal(b, &a); err != nil {
			return nil, err
		}
		return a, nil
	}
	if egress {
		b, err := n.queryStream(peerName, streamEgress, 4096)
		if err != nil || !json.Valid(b) {
			return nil, err
		}
		return proto.Attrs{proto.AttrEgress: json.RawMessage(b)}, nil
	}
	return nil, nil
}

// withAttrs fills a peer record's attributes (and the legacy egress field).
func (n *Node) withAttrs(name string, info *PeerInfo) {
	if a := n.PeerAttrs(name); a != nil {
		info.Attrs = a
		if e, ok := a[proto.AttrEgress]; ok {
			info.Egress = e
		}
	}
}

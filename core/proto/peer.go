package proto

import "encoding/json"

// Attrs are a node's attributes: data points the application sets (by key)
// and nodes carry to each other unchanged — in peer lists at every nested
// level and on StreamAttrs. The node layer gives them no meaning.
type Attrs map[string]json.RawMessage

// Well-known attribute keys (conventions between applications).
const (
	// AttrEgress: the node's egress — per address family the public address
	// ({"ipv4":{"ip","ok"},"ipv6":…}, null = family not provided). 1.4.0
	// pre-releases also sent a "country" per family; readers ignore it.
	AttrEgress = "egress"
)

// PeerInfo describes a peer as reported in peer lists.
type PeerInfo struct {
	Name      string `json:"name"`
	ExitNode  bool   `json:"exit_node"`
	Direction string `json:"direction"`
	Native    bool   `json:"native"`
	LatencyMs int    `json:"latency_ms"`
	Version   string `json:"version,omitempty"`
	// NodeID is the remote's reported stable identifier. Used by the
	// bidirectional-slot logic to prove "this incoming register is from the
	// same logical instance as the existing primary". Empty for old peers.
	NodeID       string `json:"node_id,omitempty"`
	Incompatible bool   `json:"incompatible,omitempty"`
	Conflict     bool   `json:"conflict,omitempty"`
	TunCapable   bool   `json:"tun_capable,omitempty"`
	// PV is the remote's wire-protocol generation. Higher than ours means
	// the remote may speak frames we can't parse.
	PV          int  `json:"pv,omitempty"`
	Unsupported bool `json:"unsupported,omitempty"`
	// Conns: QUIC connections the reporting node holds to this peer (one
	// per configured address); omitted for one.
	Conns int `json:"conns,omitempty"`
	// EgressCapable: the peer answers egress / whoami streams (1.4.0+).
	EgressCapable bool `json:"egress_capable,omitempty"`
	// Egress: the peer's egress as last fetched; absent = unknown. Kept
	// for consumers that predate Attrs; equals Attrs[AttrEgress].
	Egress json.RawMessage `json:"egress,omitempty"`
	// AttrsCapable: the peer answers StreamAttrs.
	AttrsCapable bool `json:"attrs_capable,omitempty"`
	// Attrs: the node's attributes as last fetched.
	Attrs Attrs `json:"attrs,omitempty"`
	// RestrictTags: the node applies restriction tags on targets
	// (access.go); only such chains get one.
	RestrictTags bool `json:"rt,omitempty"`
	// Restricted: reached through node access by user in restricted mode —
	// traffic only, no management, punching or registration (access.go).
	Restricted bool `json:"restricted,omitempty"`
	// Invisible: this node is attached by node access by user (not
	// registered there); set on the gateway entry only.
	Invisible bool `json:"invisible,omitempty"`
}

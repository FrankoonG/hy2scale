package proto

import "encoding/json"

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
	// Egress: the peer's egress as last fetched; absent = unknown.
	Egress json.RawMessage `json:"egress,omitempty"`
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

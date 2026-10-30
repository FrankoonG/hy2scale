package proto

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

// Node access by user ("节点接入（用户）"): a client logs in to a node (the
// gateway) with a user's hy2 credentials and, when the user may, uses the
// nodes the gateway sees as exits. It never registers as a peer: the
// gateway does not list it, and it may open only
//
//	StreamHello, StreamListPeer, StreamPing, StreamEgress, StreamWhoami
//	  directly at the gateway;
//	a via chain the gateway can see, to a real target, a msess subflow or a
//	  legacy bond stream;
//	a real target, a msess subflow or a bond stream at the gateway itself.
//
// The gateway validates every chain before forwarding: nodes further away
// see the gateway's own identity and cannot tell such traffic apart. What
// they must still enforce travels as a restriction tag on the target.

// AccessMinVersion is the first release that serves node access and
// understands restriction tags.
const AccessMinVersion = "1.4.0"

// Hello is what an access client sends on StreamHello.
type Hello struct {
	// Name and NodeID: the client's node identity ("" for a terminal
	// client); the gateway leaves the client out of what it reports.
	Name   string `json:"name,omitempty"`
	NodeID string `json:"node_id,omitempty"`
	// Instance identifies one client across its connections (several
	// addresses), so they count as one device.
	Instance string `json:"instance"`
	Version  string `json:"version"`
}

// HelloReply is the gateway's answer.
type HelloReply struct {
	Name     string `json:"name"`
	NodeID   string `json:"node_id,omitempty"`
	Version  string `json:"version"`
	PV       int    `json:"pv"`
	ExitNode bool   `json:"exit_node"`
}

// WriteJSON writes v as one length-prefixed JSON message.
func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteString(w, string(b))
}

// ReadJSON reads one length-prefixed JSON message into v.
func ReadJSON(r io.Reader, v any) error {
	s, err := ReadString(r)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), v)
}

// Restrict is the set of restrictions an exit applies to a stream it dials
// for an access client.
type Restrict uint32

const (
	// DenyInternal: no loopback, private, link-local or on-link
	// destinations, nor the exit's own addresses.
	DenyInternal Restrict = 1 << iota
)

// RestrictTag precedes the restriction set at the end of a target.
const RestrictTag = "#r="

// WithRestrict appends the restriction tag to a target (a real address, a
// msess subflow or a bond stream). r == 0 leaves addr as is.
func WithRestrict(addr string, r Restrict) string {
	if r == 0 {
		return addr
	}
	return addr + RestrictTag + strconv.FormatUint(uint64(r), 10)
}

// ParseRestrict strips the restriction tag from a target.
func ParseRestrict(addr string) (string, Restrict) {
	i := strings.LastIndex(addr, RestrictTag)
	if i < 0 {
		return addr, 0
	}
	v, err := strconv.ParseUint(addr[i+len(RestrictTag):], 10, 32)
	if err != nil {
		return addr, 0
	}
	return addr[:i], Restrict(v)
}

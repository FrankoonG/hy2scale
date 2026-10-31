// Package proto is the hy2scale node protocol carried over hysteria 2: the
// well-known stream addresses, the peer-list records, the multi-hop ("via")
// address encoding and the tags carried on stream addresses.
//
// Every stream a node opens is an hy2 TCP request whose address is either a
// real target ("host:port") or one of the reserved names below.
package proto

import (
	"encoding/binary"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
)

// Reserved stream addresses.
const (
	StreamRegister = "_relay_register_:0"
	StreamCtrlS2C  = "_relay_s2c_ctrl_:0"
	StreamListPeer = "_relay_list_peers_:0"
	StreamPing     = "_relay_ping_:0"
	StreamLatency  = "_relay_latency_:0"
	// StreamAPI reaches a node's own web API (remote management).
	StreamAPI = "_relay_api_:0"
	// StreamPunch carries hole-punch signalling to the node at the end of a
	// path; nodes on the way only forward it.
	StreamPunch = "_relay_punch_:0"
	// StreamEgress returns the node's egress (public address and country per
	// family, JSON); StreamWhoami the address the stream's connection comes
	// from.
	StreamEgress = "_relay_egress_:0"
	StreamWhoami = "_relay_whoami_:0"
	// StreamMsess is the subflow address of the multi-path session layer
	// (core/msess).
	StreamMsess = "_relay_msess_:0"
	// StreamHello and StreamTree serve node access by user (see access.go).
	StreamHello = "_relay_hello_:0"
	StreamTree  = "_relay_tree_:0"
	// StreamAttrs returns the node's attributes (Attrs, JSON).
	StreamAttrs = "_relay_attrs_:0"
	// StreamSpeed carries a throughput test (internal/tools): after a short
	// header the node sends zeros or discards what it receives for a while.
	StreamSpeed = "_relay_speed_:0"

	ViaPrefix    = "_relay_via_"
	DataPrefix   = "_relay_data_"
	DataSuffix   = ":0"
	IPTunPrefix  = "_relay_iptun_"
	NakPrefix    = "_relay_nak_"
	RebindPrefix = "_relay_rebind_"
	// MsessHelloTag: a subflow request may carry its HELLO (hex) after this
	// tag, saving the round trip of a separate write.
	MsessHelloTag = "#h="
	// BridgeTag: pre-1.3.10 nodes tagged exit streams with "#bridge=<id>".
	BridgeTag = "#bridge="
)

// IsRelayStream reports whether addr is a reserved stream rather than a
// real target.
func IsRelayStream(addr string) bool {
	switch addr {
	case StreamRegister, StreamCtrlS2C, StreamListPeer, StreamPing, StreamLatency,
		StreamAPI, StreamPunch, StreamEgress, StreamWhoami, StreamHello, StreamTree, StreamAttrs,
		StreamSpeed:
		return true
	}
	return strings.HasPrefix(addr, ViaPrefix) ||
		strings.HasPrefix(addr, DataPrefix) ||
		strings.HasPrefix(addr, IPTunPrefix) ||
		strings.HasPrefix(addr, NakPrefix) ||
		IsMsessAddr(addr) ||
		strings.HasPrefix(addr, RebindPrefix)
}

// MsessAddr is the subflow address carrying hello.
func MsessAddr(hello []byte) string {
	return StreamMsess + MsessHelloTag + hex.EncodeToString(hello)
}

// IsMsessAddr: addr opens a msess subflow (with or without a HELLO).
func IsMsessAddr(addr string) bool {
	return addr == StreamMsess || strings.HasPrefix(addr, StreamMsess+MsessHelloTag)
}

// MsessHello returns the HELLO carried in a subflow address (nil = none or
// malformed).
func MsessHello(addr string) []byte {
	h, ok := strings.CutPrefix(addr, StreamMsess+MsessHelloTag)
	if !ok {
		return nil
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil
	}
	return b
}

// BuildViaAddr encodes a multi-hop request for the first hop: remaining is
// the slash-joined chain after it, addr the final target.
func BuildViaAddr(remaining, addr string) string {
	return ViaPrefix + remaining + "_" + addr + ":0"
}

// ParseVia decodes a via address into the remaining chain and the target.
func ParseVia(reqAddr string) (chain, target string, ok bool) {
	if !strings.HasPrefix(reqAddr, ViaPrefix) {
		return "", "", false
	}
	rest := reqAddr[len(ViaPrefix):]
	idx := strings.Index(rest, "_")
	if idx <= 0 {
		return "", "", false
	}
	return rest[:idx], strings.TrimSuffix(rest[idx+1:], ":0"), true
}

// ConnHint names a hop in a via chain together with the connection its
// dialer should carry it over: "x@1" = peer x on its connection 1.
func ConnHint(name string, idx int) string { return name + "@" + strconv.Itoa(idx) }

// SplitConnHint: "x@1" → ("x", 1); "x" → ("x", -1).
func SplitConnHint(seg string) (string, int) {
	i := strings.LastIndexByte(seg, '@')
	if i <= 0 {
		return seg, -1
	}
	k, err := strconv.Atoi(seg[i+1:])
	if err != nil || k < 0 {
		return seg, -1
	}
	return seg[:i], k
}

// StripConnHints: "a/x@1" → "a/x".
func StripConnHints(chain string) string {
	if !strings.Contains(chain, "@") {
		return chain
	}
	parts := strings.Split(chain, "/")
	for i, p := range parts {
		parts[i], _ = SplitConnHint(p)
	}
	return strings.Join(parts, "/")
}

// ParseBridgeAddr strips an old peer's bridge tag: (target, id, tagged).
func ParseBridgeAddr(addr string) (string, string, bool) {
	idx := strings.Index(addr, BridgeTag)
	if idx < 0 {
		return addr, "", false
	}
	return addr[:idx], addr[idx+len(BridgeTag):], true
}

// WriteString writes a length-prefixed (uint16) string.
func WriteString(w io.Writer, s string) error {
	b := []byte(s)
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(b)))
	if _, err := w.Write(lb[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// ReadString reads a length-prefixed (uint16) string.
func ReadString(r io.Reader) (string, error) {
	var lb [2]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return "", err
	}
	buf := make([]byte, binary.BigEndian.Uint16(lb[:]))
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

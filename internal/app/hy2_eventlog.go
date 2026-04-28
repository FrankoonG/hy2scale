package app

import (
	"net"
	"runtime"
	"strconv"
	"sync"
)

// hyserver's Authenticator returns the auth ID *during* connection setup, but
// its Outbound.TCP only receives the requested target address — no auth
// context. To gate `_relay_api_:0` streams on whether the underlying QUIC
// connection authenticated as a peer node ("system") versus an ordinary
// proxy user ("user:<name>"), we exploit the fact that hyserver calls
// EventLogger.TCPRequest immediately before Outbound.TCP *in the same
// goroutine* (apernet/hysteria/core/v2 server.go:280-285). Stashing the
// authID under the current goroutine ID makes it available to the very
// next Outbound.TCP call without modifying the upstream interfaces.
//
// Caveat: this relies on the same-goroutine call sequence. If a future
// hyserver release decouples the two callbacks or hops goroutines between
// them, the loadGoIDAuth lookup will return "" and the relay-API bypass
// will fail safe (default-deny). The vendored hyserver version is pinned
// in go.mod so we'd notice during a deliberate upgrade.
var goidAuthID sync.Map // map[uint64]string  goroutine ID → hyserver auth ID

// goid parses runtime.Stack output to recover the current goroutine's
// numeric identifier. The format "goroutine <N> [<state>]:" has been
// stable since Go 1.5; the worst case on a parser fault is an authID
// lookup miss, which falls through to the safe default-deny path.
func goid() uint64 {
	var buf [32]byte
	n := runtime.Stack(buf[:], false)
	line := buf[:n]
	if len(line) < 10 {
		return 0
	}
	line = line[len("goroutine "):]
	end := 0
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	id, _ := strconv.ParseUint(string(line[:end]), 10, 64)
	return id
}

// setGoIDAuth records the current goroutine's hyserver authID. Called from
// hy2Auth.TCPRequest; cleared by takeGoIDAuth or by an explicit Delete on
// the error path (hy2Auth.TCPError) to prevent stale entries piling up if
// the goroutine is reused.
func setGoIDAuth(id string) {
	goidAuthID.Store(goid(), id)
}

// takeGoIDAuth atomically reads and deletes the authID for the current
// goroutine. Returns "" if no entry — caller treats that as "unauthenticated"
// (default-deny for the relay-API path).
func takeGoIDAuth() string {
	v, ok := goidAuthID.LoadAndDelete(goid())
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// EventLogger implementation on *hy2Auth — only TCPRequest does real work.
// Connect/Disconnect/UDP*/TCPError are no-ops here; other observability
// (per-conn stats, Disconnect logging) lives elsewhere in the app.

func (a *hy2Auth) Connect(addr net.Addr, id string, tx uint64)            {}
func (a *hy2Auth) Disconnect(addr net.Addr, id string, err error)         {}
func (a *hy2Auth) TCPRequest(addr net.Addr, id, reqAddr string)           { setGoIDAuth(id) }
func (a *hy2Auth) TCPError(addr net.Addr, id, reqAddr string, err error)  { takeGoIDAuth() }
func (a *hy2Auth) UDPRequest(addr net.Addr, id string, sessionID uint32, reqAddr string) {
}
func (a *hy2Auth) UDPError(addr net.Addr, id string, sessionID uint32, err error) {}

// RelayAuthConn wraps the hub-side end of the net.Pipe used to deliver a
// `_relay_api_:0` stream to the local API server, carrying the hyserver
// authID that authenticated the originating QUIC connection. The api
// package's relaySrv ConnContext type-asserts this on Accept and threads
// the authID into the request context, so authMiddleware can apply
// RelayAdminPassthrough only when authID == "system".
type RelayAuthConn struct {
	net.Conn
	AuthID string
}

// RelayAuthID exposes the carried authID without requiring callers to
// import this package's concrete type — they can use the small interface
// `interface{ RelayAuthID() string }` instead.
func (c *RelayAuthConn) RelayAuthID() string { return c.AuthID }

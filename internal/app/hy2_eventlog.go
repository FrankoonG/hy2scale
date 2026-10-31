package app

import (
	"net"
	"sync"
)

// hy2Auth is also the server's ConnEvents (core/server); the per-request
// auth stash lives there.
func (a *hy2Auth) Connect(addr net.Addr, id string, tx uint64)    {}
func (a *hy2Auth) Disconnect(addr net.Addr, id string, err error) {}

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

// syncMapLen counts entries of a sync.Map (diagnostics only — O(n)).
func syncMapLen(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ any) bool { n++; return true })
	return n
}

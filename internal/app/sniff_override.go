package app

import (
	"log"
	"net"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/internal/sniff"
)

// peekFirstChunk reads up to sniff.MinPeek bytes from `conn` with a short
// deadline so we don't stall on protocols where the server speaks first
// (SMTP, FTP, SSH). Returns whatever arrived within the window; the bytes
// are NOT pushed back into the stream — the caller forwards them verbatim
// to the upstream after dialing, so this is safe and consume-once.
//
// Window choice: TLS Client Hello and HTTP request lines are typically
// produced within milliseconds of the client receiving its proxy SUCCESS
// reply. 300 ms is conservative enough to cover slow links while still
// being invisible end-to-end. On timeout (server-speaks-first) we return
// the partial buffer (often nil); sniffOverrideHost then no-ops and the
// dial proceeds with the original IP.
func peekFirstChunk(conn net.Conn) []byte {
	buf := make([]byte, sniff.MinPeek)
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, _ := conn.Read(buf)
	_ = conn.SetReadDeadline(time.Time{}) // clear
	if n <= 0 {
		return nil
	}
	return buf[:n]
}

// deferredDialConn defers the upstream net.Dial until the first call to
// Write (which carries the first client-supplied bytes) — giving the
// sniffer a chance to recover a hostname from a TLS Client Hello / HTTP
// Host header and replace an IP-form destination with the real name
// before any TCP SYN goes out. Used by the hy2 server path where the
// listener requires a synchronously-returned net.Conn before it knows
// what bytes the client will send.
//
// Read blocks until the first Write completes the dial. If the
// underlying protocol is server-speaks-first (SSH/SMTP), the first Read
// from the upstream will be ahead of any client Write — to keep that
// from hanging forever, Read enforces a 300 ms wall-clock budget on the
// "no-write-seen-yet" case and then dials with the original destination
// (preserving legacy behaviour for these protocols).
//
// Close cancels everything; Set*Deadline are best-effort plumb-throughs
// once the upstream exists.
type deferredDialConn struct {
	dialFn func(addr string) (net.Conn, error)
	target string // original "host:port" from the proxy request

	mu     sync.Mutex
	done   chan struct{} // closed once `actual` (or `dialErr`) is set
	actual net.Conn
	dialErr error

	rDeadline, wDeadline time.Time
}

// newDeferredDialConn returns a wrapper that will dial `target` (or a
// sniff-overridden form of it) lazily on first Write. `dial` must accept
// the maybe-overridden address and return the real upstream conn.
func newDeferredDialConn(target string, dial func(addr string) (net.Conn, error)) *deferredDialConn {
	return &deferredDialConn{
		dialFn: dial,
		target: target,
		done:   make(chan struct{}),
	}
}

// completeDial commits the upstream — exactly one of (actual, dialErr)
// is set, and `done` is closed so blocked Reads / Closes can proceed.
// Repeated calls are a no-op.
func (c *deferredDialConn) completeDial(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return
	default:
	}
	conn, err := c.dialFn(addr)
	c.actual = conn
	c.dialErr = err
	if conn != nil {
		if !c.rDeadline.IsZero() {
			_ = conn.SetReadDeadline(c.rDeadline)
		}
		if !c.wDeadline.IsZero() {
			_ = conn.SetWriteDeadline(c.wDeadline)
		}
	}
	close(c.done)
}

func (c *deferredDialConn) Write(b []byte) (int, error) {
	select {
	case <-c.done:
		// Already dialed (e.g. server-speaks-first triggered a fallback
		// dial). Just forward.
		if c.dialErr != nil {
			return 0, c.dialErr
		}
		return c.actual.Write(b)
	default:
	}
	// First write — peek `b` for sniffing, then dial.
	dst := sniffOverrideHost(c.target, b)
	c.completeDial(dst)
	if c.dialErr != nil {
		return 0, c.dialErr
	}
	return c.actual.Write(b)
}

func (c *deferredDialConn) Read(b []byte) (int, error) {
	select {
	case <-c.done:
	case <-time.After(300 * time.Millisecond):
		// Server-speaks-first guard: we've been asked to Read before any
		// Write happened. Dial with original target now so the upstream
		// can produce its greeting.
		c.completeDial(c.target)
	}
	if c.dialErr != nil {
		return 0, c.dialErr
	}
	return c.actual.Read(b)
}

func (c *deferredDialConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
	default:
		c.dialErr = net.ErrClosed
		close(c.done)
	}
	if c.actual != nil {
		return c.actual.Close()
	}
	return nil
}

// LocalAddr / RemoteAddr / SetDeadline are best-effort. Pre-dial they
// can't reflect a real socket, so we return placeholders or buffer the
// deadline for post-dial application. Hysteria treats these as opaque.
func (c *deferredDialConn) LocalAddr() net.Addr {
	if c.actual != nil {
		return c.actual.LocalAddr()
	}
	return &net.TCPAddr{}
}
func (c *deferredDialConn) RemoteAddr() net.Addr {
	if c.actual != nil {
		return c.actual.RemoteAddr()
	}
	return &net.TCPAddr{}
}
func (c *deferredDialConn) SetDeadline(t time.Time) error {
	c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}
func (c *deferredDialConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rDeadline = t
	actual := c.actual
	c.mu.Unlock()
	if actual != nil {
		return actual.SetReadDeadline(t)
	}
	return nil
}
func (c *deferredDialConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.wDeadline = t
	actual := c.actual
	c.mu.Unlock()
	if actual != nil {
		return actual.SetWriteDeadline(t)
	}
	return nil
}

// sniffOverrideHost replaces an IP-form `host:port` destination with one
// whose host part comes from the application-layer hostname recovered from
// the client's first-chunk payload (TLS Client Hello SNI, HTTP Host).
//
// Use case: a DNS-poisoned client resolved `www.google.com` to a fake
// IP, dialed THAT IP through our proxy, and our listener received ATYP=
// IPv4 with the fake IP. Forwarding that IP literally to the exit's
// net.Dial dials the wrong server. By peeking at the next bytes the
// client sends (which is the TLS Client Hello for HTTPS, or the HTTP
// request for plain HTTP), we recover the real hostname; the exit's
// own clean resolver then turns it back into a real IP.
//
// Behaviour:
//   - If the destination's host is NOT a literal IP, returns dst unchanged
//     (client already sent a hostname, nothing to sniff — the legacy
//     hostname-via-exit path already handles it).
//   - If the destination IS an IP but `peeked` contains no usable hostname
//     (truncated, non-TLS/non-HTTP, malformed), returns dst unchanged.
//   - If a hostname is recovered, returns "<sniffed>:<port>" using the
//     ORIGINAL port from dst.
//
// The peeked bytes are NOT consumed here — the caller must still forward
// them verbatim to the upstream after dialing.
func sniffOverrideHost(dst string, peeked []byte) string {
	host, port, err := net.SplitHostPort(dst)
	if err != nil || net.ParseIP(host) == nil {
		return dst
	}
	sniffed := sniff.Host(peeked)
	if sniffed == "" {
		return dst
	}
	log.Printf("[sniff] override %s → %s:%s (recovered from %d-byte peek)",
		dst, sniffed, port, len(peeked))
	return net.JoinHostPort(sniffed, port)
}

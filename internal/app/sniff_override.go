package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/internal/sniff"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// Host-name recovery for flows whose client asked for an IP (typically one
// a polluted resolver handed it): the client's first bytes (TLS Client Hello
// SNI or HTTP Host) name the real site, and where the flow is dialled from
// decides whether that name replaces the IP.
//
//   - The flow leaves through another node (the user's exit or a rule's
//     exit): the name is sent, and the exit resolves it.
//   - The flow is dialled here and the client is on another host: the name
//     is resolved here instead of where the client resolved it. The
//     client's IP is kept when it is one of the answers (or the name does
//     not resolve here), so a correct answer is never second-guessed.
//   - The flow is dialled here and the client is on this host: it resolved
//     with this host's resolver, so its IP is kept; the name only selects
//     a routing rule.
//
// A rule matching the name wins over one matching the IP.

// sniffWindow is how long a flow waits for the client's first bytes.
// Protocols where the server speaks first (SMTP, FTP, MySQL) pay it until
// their destination lands in the miss cache.
const sniffWindow = 300 * time.Millisecond

// peekHello reads the client's first bytes until they hold a whole TLS
// record or HTTP header (sniff.Complete), the window ends, or the read
// fails; prefix holds bytes already read from conn. waited reports that the
// window ended first. The bytes are consumed: the caller forwards them before
// relaying.
func peekHello(conn net.Conn, prefix []byte) (peeked []byte, waited bool) {
	_ = conn.SetReadDeadline(time.Now().Add(sniffWindow))
	defer conn.SetReadDeadline(time.Time{})
	buf := append(make([]byte, 0, 2048), prefix...)
	chunk := make([]byte, 4096)
	for !sniff.Complete(buf) {
		want := sniff.MaxPeek - len(buf)
		if want > len(chunk) {
			want = len(chunk)
		}
		n, err := conn.Read(chunk[:want])
		buf = append(buf, chunk[:n]...)
		if err != nil {
			var ne net.Error
			waited = errors.As(err, &ne) && ne.Timeout()
			break
		}
	}
	return buf, waited
}

// sniffName returns the usable host name the client's first bytes carry
// for an IP destination dst, or "".
func sniffName(dst string, peeked []byte) string {
	host, _, err := net.SplitHostPort(dst)
	if err != nil || net.ParseIP(host) == nil {
		return ""
	}
	name := sniff.Host(peeked)
	if !sniff.Usable(name) {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(name), ".")
}

// wantSniff reports whether a flow to dst should wait for the client's
// first bytes: dst is an IP and has not repeatedly failed to yield a name.
func wantSniff(dst string) bool {
	host, _, err := net.SplitHostPort(dst)
	if err != nil || net.ParseIP(host) == nil {
		return false
	}
	return !sniffMisses.skip(dst)
}

// isLoopbackAddr reports whether a client address is on this host.
func isLoopbackAddr(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// flowRoute maps a destination to the exit a flow takes ("" = dial here).
type flowRoute func(addr string) (via string, paths []string, mode string, err error)

// exitRoute is the route of a user flow: its own exit, else the matching
// rule's exit, else here.
func (a *App) exitRoute(exitVia string, exitPaths []string, exitMode string) flowRoute {
	return func(addr string) (string, []string, string, error) {
		return a.routeFor(exitVia, exitPaths, exitMode, addr)
	}
}

// dialSniffedTCP dials a flow to the IP destination dst whose client sent
// peeked first (waited: the sniff window ended before a whole hello). here
// dials from this node (nil = through the egress); localClient marks a client on this
// host. The caller forwards peeked once the connection is up.
func (a *App) dialSniffedTCP(ctx context.Context, route flowRoute, here func(context.Context, string) (net.Conn, error),
	dst string, peeked []byte, waited, localClient bool) (net.Conn, error) {
	if here == nil {
		here = func(ctx context.Context, addr string) (net.Conn, error) { return a.dialDirect(ctx, "tcp", addr) }
	}
	name := sniffName(dst, peeked)
	sniffMisses.note(dst, name != "", waited)

	target := dst
	via, paths, mode, err := "", []string(nil), "", error(nil)
	if name != "" {
		_, port, _ := net.SplitHostPort(dst)
		target = net.JoinHostPort(name, port)
		if via, paths, mode, err = route(target); err != nil {
			return nil, err
		}
	}
	if via == "" {
		if via, paths, mode, err = route(dst); err != nil {
			return nil, err
		}
	}
	switch {
	case via != "":
		if target != dst {
			debugLog("[sniff] %s → %s via %s", dst, target, via)
		}
		return a.dialExitWithPaths(ctx, via, paths, mode, target)
	case localClient:
		return here(ctx, dst)
	case target == dst:
		return here(ctx, dst)
	default:
		return a.dialResolved(ctx, here, dst, name)
	}
}

// dialResolved dials name (resolved here) for a client that asked for the
// IP dst: dst itself when it is one of the answers or nothing resolves,
// else the answers of dst's family first.
func (a *App) dialResolved(ctx context.Context, here func(context.Context, string) (net.Conn, error), dst, name string) (net.Conn, error) {
	host, port, _ := net.SplitHostPort(dst)
	ip := net.ParseIP(host)
	ips := sniffDNS.lookup(ctx, name, a.egressLookup)
	if len(ips) == 0 {
		return here(ctx, dst)
	}
	var same, other []net.IP
	for _, a := range ips {
		if a.Equal(ip) {
			return here(ctx, dst)
		}
		if (a.To4() != nil) == (ip.To4() != nil) {
			same = append(same, a)
		} else {
			other = append(other, a)
		}
	}
	debugLog("[sniff] %s → %s", dst, name)
	var err error
	for i, a := range append(same, other...) {
		if i == 3 {
			break
		}
		var c net.Conn
		if c, err = here(ctx, net.JoinHostPort(a.String(), port)); err == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, err
}

// sniffDNS caches this node's answers for sniffed names: a page opens many
// connections to one name, and each would otherwise wait for a lookup.
var sniffDNS = &nameCache{entries: make(map[string]*nameEntry)}

const (
	nameTTL    = time.Minute
	nameNegTTL = 10 * time.Second
	nameCap    = 4096
)

type nameEntry struct {
	ips   []net.IP
	until time.Time
}

type nameCache struct {
	mu      sync.Mutex
	entries map[string]*nameEntry
}

func (c *nameCache) lookup(ctx context.Context, name string, resolve func(context.Context, string) ([]net.IP, error)) []net.IP {
	now := time.Now()
	c.mu.Lock()
	if e := c.entries[name]; e != nil && now.Before(e.until) {
		c.mu.Unlock()
		return e.ips
	}
	c.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	ips, _ := resolve(lctx, name)
	cancel()
	ttl := nameTTL
	if len(ips) == 0 {
		ttl = nameNegTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= nameCap {
		for k, v := range c.entries {
			if now.After(v.until) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < nameCap {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[name] = &nameEntry{ips: ips, until: now.Add(ttl)}
	return ips
}

// sniffAfterAccept reports whether a VPN or WireGuard flow to dst is
// accepted before it is dialled, so the client's hello can be read: IPv4
// web ports only. Every other flow is dialled before its SYN is answered, so
// a destination the exit cannot reach is refused at once (an IPv6 client
// then falls back to IPv4; polluted answers are IPv4 anyway).
func sniffAfterAccept(dst string) bool {
	host, port, err := net.SplitHostPort(dst)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return false
	}
	switch port {
	case "80", "443", "8080", "8443":
		return wantSniff(dst)
	}
	return false
}

// acceptForwarded answers a forwarded SYN; nil when the endpoint could not
// be created (the SYN is refused).
func acceptForwarded(r *tcp.ForwarderRequest, tag, src, dst string) (*gonet.TCPConn, tcpip.Endpoint) {
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		debugLog("%s %s → %s: %v", tag, src, dst, terr)
		r.Complete(true)
		return nil, nil
	}
	r.Complete(false)
	ep.SocketOptions().SetKeepAlive(true)
	return gonet.NewTCPConn(&wq, ep), ep
}

// dialAccepted reads the hello of an accepted flow to dst, dials it and
// forwards the hello. On failure the client connection is reset.
func (a *App) dialAccepted(ctx context.Context, conn *gonet.TCPConn, ep tcpip.Endpoint, route flowRoute, dst string) (net.Conn, bool) {
	peeked, waited := peekHello(conn, nil)
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	remote, err := a.dialSniffedTCP(dctx, route, nil, dst, peeked, waited, false)
	cancel()
	if err == nil && len(peeked) > 0 {
		if _, err = remote.Write(peeked); err != nil {
			remote.Close()
		}
	}
	if err != nil {
		debugLog("[sniff] %s: %v", dst, err)
		ep.Abort()
		conn.Close()
		return nil, false
	}
	return remote, true
}

// sniffMisses remembers destinations whose clients repeatedly sent no
// usable hello within the window, so later flows there stop waiting.
var sniffMisses = &missCache{entries: make(map[string]*missEntry)}

const (
	missLimit = 5
	missTTL   = 10 * time.Minute
	missCap   = 4096
)

type missEntry struct {
	n     int
	until time.Time
}

type missCache struct {
	mu      sync.Mutex
	entries map[string]*missEntry
}

func (m *missCache) skip(dst string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[dst]
	if e == nil {
		return false
	}
	if time.Now().After(e.until) {
		delete(m.entries, dst)
		return false
	}
	return e.n >= missLimit
}

// note records a sniff outcome: a name clears dst, a window that ended
// without one counts as a miss.
func (m *missCache) note(dst string, named, waited bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if named {
		delete(m.entries, dst)
		return
	}
	if !waited {
		return
	}
	now := time.Now()
	e := m.entries[dst]
	if e == nil {
		if len(m.entries) >= missCap {
			for k, v := range m.entries {
				if now.After(v.until) {
					delete(m.entries, k)
				}
			}
			for k := range m.entries {
				if len(m.entries) < missCap {
					break
				}
				delete(m.entries, k)
			}
		}
		e = &missEntry{}
		m.entries[dst] = e
	}
	e.n++
	e.until = now.Add(missTTL)
}

// deferredDialConn is the connection the hy2 server gets for a flow to an
// IP: the server needs a net.Conn before the client has sent anything, so
// the client's writes are buffered until they hold a whole hello or the
// window ends, and only then is the flow dialled (with the buffered bytes
// sent first). Reads wait for the dial, so a server that speaks first gets
// its connection when the window ends.
type deferredDialConn struct {
	dial func(peeked []byte, waited bool) (net.Conn, error)

	mu      sync.Mutex
	pending []byte
	timer   *time.Timer
	started bool          // the dial has begun; writes wait for done
	done    chan struct{} // closed once actual or dialErr is set
	closed  bool
	actual  net.Conn
	dialErr error

	rDeadline, wDeadline time.Time
}

func newDeferredDialConn(dial func(peeked []byte, waited bool) (net.Conn, error)) *deferredDialConn {
	c := &deferredDialConn{dial: dial, done: make(chan struct{})}
	c.timer = time.AfterFunc(sniffWindow, func() { c.commit(true) })
	return c
}

// commit dials once with the bytes buffered so far and sends them.
func (c *deferredDialConn) commit(waited bool) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.timer.Stop()
	peeked := c.pending
	c.pending = nil
	c.mu.Unlock()

	conn, err := c.dial(peeked, waited)
	if err == nil && len(peeked) > 0 {
		if _, werr := conn.Write(peeked); werr != nil {
			conn.Close()
			conn, err = nil, werr
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		if conn != nil {
			conn.Close()
		}
		return
	}
	c.actual, c.dialErr = conn, err
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
	c.mu.Lock()
	if !c.started {
		c.pending = append(c.pending, b...)
		complete := sniff.Complete(c.pending)
		c.mu.Unlock()
		if !complete {
			return len(b), nil
		}
		c.commit(false)
		<-c.done
		if c.dialErr != nil {
			return 0, c.dialErr
		}
		return len(b), nil
	}
	c.mu.Unlock()
	<-c.done
	if c.dialErr != nil {
		return 0, c.dialErr
	}
	return c.actual.Write(b)
}

func (c *deferredDialConn) Read(b []byte) (int, error) {
	<-c.done
	if c.dialErr != nil {
		return 0, c.dialErr
	}
	return c.actual.Read(b)
}

func (c *deferredDialConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.started = true
	c.timer.Stop()
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

// LocalAddr / RemoteAddr / SetDeadline are best-effort. Before the dial
// they can't reflect a real socket, so they return placeholders or keep the
// deadline for the dialled connection. Hysteria treats these as opaque.
func (c *deferredDialConn) LocalAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.actual != nil {
		return c.actual.LocalAddr()
	}
	return &net.TCPAddr{}
}

func (c *deferredDialConn) RemoteAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
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

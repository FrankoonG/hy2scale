package app

import (
	"context"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// userMeter counts one user connection's bytes while they flow, not only
// when the connection ends: the device row shows live totals, and a
// connection that crosses the user's traffic limit is closed instead of
// running on (the limit used to be checked only when a connection opened).
type userMeter struct {
	a       *App
	user    string  // "" = no per-user accounting
	dev     *Device // nil = no device row
	pending atomic.Int64
	flushed atomic.Int64 // unix nanos of the last flush
	over    atomic.Bool
	stop    func() // closes the connection once the user is over the limit
}

const (
	meterFlushBytes = 256 << 10
	meterFlushEvery = time.Second
)

func (a *App) newUserMeter(user, sid string, stop func()) *userMeter {
	if user == "__psk__" { // IKEv2 PSK / shared-key clients: no user account
		user = ""
	}
	m := &userMeter{a: a, user: user, dev: a.Sessions.device(sid), stop: stop}
	m.flushed.Store(time.Now().UnixNano())
	return m
}

// add records bytes from the user (up) and to the user (down).
func (m *userMeter) add(up, down int64) {
	if m.dev != nil {
		m.dev.TxBytes.Add(up)
		m.dev.RxBytes.Add(down)
	}
	if m.user == "" {
		return
	}
	if m.pending.Add(up+down) >= meterFlushBytes ||
		time.Now().UnixNano()-m.flushed.Load() >= int64(meterFlushEvery) {
		m.flush()
	}
}

func (m *userMeter) flush() {
	m.flushed.Store(time.Now().UnixNano())
	if n := m.pending.Swap(0); n > 0 {
		m.a.RecordTraffic(m.user, n)
	}
	if m.a.overTrafficLimit(m.user) && m.over.CompareAndSwap(false, true) {
		log.Printf("[traffic] %s reached its traffic limit: closing the connection", m.user)
		if m.stop != nil {
			go m.stop()
		}
	}
}

// done adds what is still pending when the connection ends.
func (m *userMeter) done() {
	if m.user == "" {
		return
	}
	if n := m.pending.Swap(0); n > 0 {
		m.a.RecordTraffic(m.user, n)
	}
}

// userSide wraps the connection to the user: reads are upload.
func (m *userMeter) userSide(c net.Conn) net.Conn { return &meteredConn{Conn: c, m: m, readUp: true} }

// remoteSide wraps the connection to the target: reads are download.
func (m *userMeter) remoteSide(c net.Conn) net.Conn { return &meteredConn{Conn: c, m: m} }

type meteredConn struct {
	net.Conn
	m       *userMeter
	readUp  bool
	onClose func()
	once    sync.Once
}

func (c *meteredConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		if c.readUp {
			c.m.add(int64(n), 0)
		} else {
			c.m.add(0, int64(n))
		}
	}
	return n, err
}

func (c *meteredConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		if c.readUp {
			c.m.add(0, int64(n))
		} else {
			c.m.add(int64(n), 0)
		}
	}
	return n, err
}

func (c *meteredConn) Close() error {
	if c.onClose != nil {
		c.once.Do(c.onClose)
	}
	return c.Conn.Close()
}

// NoHalfClose passes the wrapped conn's half-close capability through.
func (c *meteredConn) NoHalfClose() bool {
	nh, ok := c.Conn.(interface{ NoHalfClose() bool })
	return ok && nh.NoHalfClose()
}

// relayUser pipes a user connection to its dialed remote with a device row
// and live accounting. halfClose forwards FINs (proxy protocols); transparent
// L3 streams close as a whole.
func (a *App) relayUser(conn, remote net.Conn, username, remoteIP, protocol string, halfClose bool) {
	ctx, cancel := context.WithCancel(context.Background())
	sid := a.Sessions.Connect(username, remoteIP, protocol, cancel)
	m := a.newUserMeter(username, sid, cancel)
	uc := m.userSide(conn)
	done := make(chan struct{})
	go func() {
		// Upload: client → remote. (copyCtx(dst, src) reads src, writes dst.)
		copyCtx(ctx, remote, uc)
		// Forward the client's FIN; don't cancel the response direction,
		// the server reply may still be draining back.
		if halfClose {
			halfCloseAndBound(remote)
		}
		done <- struct{}{}
	}()
	// Download: remote → client.
	copyCtx(ctx, uc, remote)
	if halfClose {
		// Forward FIN to the client and bound the other goroutine's read so
		// a hung client doesn't leak this session.
		halfCloseAndBound(conn)
	}
	<-done
	cancel()
	a.Sessions.Disconnect(sid, 0, 0)
	m.done()
}

// meterHy2 wraps an hy2 user's outbound stream (TCP or UDP): the hysteria
// server reads the target from it (download) and writes the user's bytes
// into it (upload). The device row lives as long as the stream.
func (a *App) meterHy2(authID, peerAddr string, c net.Conn) net.Conn {
	name := strings.TrimPrefix(authID, "user:")
	ip := peerAddr
	if h, _, err := net.SplitHostPort(peerAddr); err == nil {
		ip = h
	}
	sid := a.Sessions.Connect(name, ip, "hy2", func() { c.Close() })
	m := a.newUserMeter(name, sid, func() { c.Close() })
	mc := m.remoteSide(c).(*meteredConn)
	mc.onClose = func() {
		a.Sessions.Disconnect(sid, 0, 0)
		m.done()
	}
	return mc
}

// overTrafficLimit: the user has used up its traffic limit (live counter).
func (a *App) overTrafficLimit(username string) bool {
	if username == "" {
		return false
	}
	a.usersMu.RLock()
	u, ok := a.userIndex[username]
	a.usersMu.RUnlock()
	return ok && u.TrafficLimit > 0 && atomic.LoadInt64(&u.TrafficUsed) >= u.TrafficLimit
}

// userExpired: the user's expiry date has passed.
func userExpired(u *UserConfig) bool {
	if u.ExpiryDate == "" {
		return false
	}
	t, err := time.Parse("2006-01-02", u.ExpiryDate)
	return err == nil && time.Now().After(t)
}

package app

import (
	"net"
	"time"
)

// idleTimeoutConn wraps a net.Conn with a goroutine-based read timeout.
// If Read() doesn't return within the timeout, the connection is closed.
// This prevents dead relay streams from hanging forever when QUIC reconnects.
type idleTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleTimeoutConn) Read(b []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := c.Conn.Read(b)
		ch <- result{n, err}
	}()
	select {
	case r := <-ch:
		return r.n, r.err
	case <-time.After(c.timeout):
		c.Conn.Close()
		return 0, net.ErrClosed
	}
}

const defaultIdleTimeout = 30 * time.Second

func wrapIdleTimeout(conn net.Conn) net.Conn {
	return &idleTimeoutConn{Conn: conn, timeout: defaultIdleTimeout}
}

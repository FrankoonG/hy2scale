package app

import (
	"context"
	"net"
	"time"
)

// idleTimeoutConn wraps a net.Conn with a context-aware read timeout.
//
// History: Originally used a fixed 30s timeout to clean up dead relay streams
// after QUIC reconnection (see docs/stuck-request-investigation.md).
// But 30s kills legitimate idle connections like SSH.
//
// Current approach:
//   - Long idle timeout (10 minutes) as a safety net for truly dead streams
//   - Context cancellation for immediate cleanup when QUIC connection drops
//   - Normal idle connections (SSH) are unaffected because 10 min >> SSH idle
type idleTimeoutConn struct {
	net.Conn
	timeout time.Duration
	ctx     context.Context
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
	case <-c.ctx.Done():
		// QUIC connection dropped or caller canceled — close immediately
		c.Conn.Close()
		return 0, net.ErrClosed
	case <-time.After(c.timeout):
		// Safety net: truly dead stream that nothing else caught
		c.Conn.Close()
		return 0, net.ErrClosed
	}
}

const defaultIdleTimeout = 10 * time.Minute

func wrapIdleTimeout(conn net.Conn) net.Conn {
	return wrapIdleTimeoutCtx(context.Background(), conn)
}

func wrapIdleTimeoutCtx(ctx context.Context, conn net.Conn) net.Conn {
	return &idleTimeoutConn{Conn: conn, timeout: defaultIdleTimeout, ctx: ctx}
}

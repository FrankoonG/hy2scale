package relay

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// bridgedConn wraps a relay stream in a bridge-aware net.Conn.
// On the requester side, when Read/Write fails (stream died), it suspends
// and attempts rebind by opening a new stream to the exit node.
type bridgedConn struct {
	bridge   *streamBridge
	node     *Node
	peerName string

	mu         sync.Mutex
	stream     net.Conn
	closed     bool
	rebinding  bool          // true while tryRebind is in progress
	rebindDone chan struct{} // closed when rebind completes (success or failure)
	lastRead   atomic.Int64  // unix nanos of last successful Read
	lastWrite  atomic.Int64  // unix nanos of last successful Write

	// rebindsWithoutData counts consecutive successful rebinds that
	// haven't yielded any Read data yet. The exit-side bridge is
	// single-step: once the exit-side app-conn closes (e.g. HTTP
	// response finished), the exit removes its bridge from the map.
	// Any further rebind request from us gets "rebind rejected: not
	// found" and the new QUIC stream is immediately closed — which
	// makes the next Read/Write fail again, triggering another rebind.
	// Without a cap, this spirals into a 500ms-ticker self-spin,
	// flooding both ends' logs and wasting QUIC stream churn. We
	// reset this counter on any successful Read (n > 0).
	rebindsWithoutData atomic.Int32

	// For multi-hop via: re-open via chain on rebind instead of using _relay_rebind_
	viaPath       []string
	viaTargetAddr string

	// Single monitor goroutine that closes the stream when bridge dies
	monitorOnce sync.Once
}

// startMonitor launches a single goroutine (per conn) that watches bridge.ctx
// and closes the underlying stream when the bridge is killed. This replaces
// the per-Read/per-Write goroutine+channel pattern.
func (c *bridgedConn) startMonitor() {
	c.monitorOnce.Do(func() {
		go func() {
			<-c.bridge.ctx.Done()
			// Close the current stream (may have been replaced by rebind)
			c.mu.Lock()
			s := c.stream
			c.mu.Unlock()
			if s != nil {
				s.Close()
			}
		}()
	})
}

// DialTCPBridged is like DialTCP but returns a connection that survives
// QUIC reconnects by rebinding to new streams transparently.
func (n *Node) DialTCPBridged(peerName, addr string) (net.Conn, string, error) {
	n.mu.RLock()
	p, ok := n.peers[peerName]
	n.mu.RUnlock()
	if !ok {
		return nil, "", fmt.Errorf("relay: peer %q not connected", peerName)
	}
	if n.isPeerBlocked(p) {
		return nil, "", fmt.Errorf("relay: peer %q is blocked (incompatible or conflict)", peerName)
	}
	if p.client == nil {
		return nil, "", fmt.Errorf("relay: peer %q is inbound (no client)", peerName)
	}

	bridge := n.bridges.Create(peerName, addr, nil)

	// Encode bridge ID in address so exit node can track it
	taggedAddr := addr + "#bridge=" + bridge.id
	cl := p.pickClient()
	stream, err := cl.TCP(taggedAddr)
	if err != nil {
		n.bridges.Remove(bridge.id)
		return nil, "", err
	}
	bridge.mu.Lock()
	bridge.relayStream = stream
	bridge.mu.Unlock()

	bc := &bridgedConn{
		bridge:   bridge,
		node:     n,
		peerName: peerName,
		stream:   n.wrapConn(peerName, stream),
	}
	bc.startMonitor()
	return bc, bridge.id, nil
}

func (c *bridgedConn) Read(b []byte) (int, error) {
	for {
		c.mu.Lock()
		s := c.stream
		c.mu.Unlock()
		if s == nil || c.closed {
			return 0, net.ErrClosed
		}

		n, err := s.Read(b)
		if n > 0 {
			c.lastRead.Store(time.Now().UnixNano())
			// Any byte of payload means the rebound stream is actually
			// working end-to-end; reset the zombie-rebind cap.
			c.rebindsWithoutData.Store(0)
		}
		if err == nil || n > 0 {
			return n, err
		}

		// Read error — check if bridge was killed
		if c.bridge.ctx.Err() != nil {
			return 0, net.ErrClosed
		}

		// Remote sent FIN / our side closed / half-close cases — these
		// are *not* transport failures. Rebind exists to survive QUIC
		// connection blips on long-lived sessions, not to resurrect
		// connections the peer has already finished with. Propagate the
		// close to the caller so the upper io.Copy terminates cleanly
		// and the bridge dies naturally. This is the main fix for the
		// "rebind rejected" storm observed on exit peers like AUB when
		// HTTP short-connection traffic dominates the requester's path.
		if isCleanClose(err) {
			return 0, err
		}

		// Defense-in-depth: if prior rebinds produced no Read data
		// before dying, the exit-side bridge is almost certainly gone
		// (Rebind rejected) — further attempts can't recover. Stop.
		if c.rebindsWithoutData.Load() >= maxZombieRebinds {
			return 0, err
		}

		// Try rebind
		if !c.tryRebind() {
			return 0, err
		}
		// Rebind succeeded — retry with new stream
	}
}

func (c *bridgedConn) Write(b []byte) (int, error) {
	for {
		c.mu.Lock()
		s := c.stream
		c.mu.Unlock()
		if s == nil || c.closed {
			return 0, net.ErrClosed
		}

		// Check for sustained congestion (direct connections only, not multi-hop via).
		if len(c.viaPath) == 0 {
			lastR := c.lastRead.Load()
			if lastR > 0 && c.lastWrite.Load() > lastR {
				stall := time.Since(time.Unix(0, lastR))
				if stall > bridgeCongestionTO {
					log.Printf("[bridge] %s congested (no read for %v), proactive rebind", c.bridge.id, stall.Round(time.Second))
					s.Close()
					if !c.tryRebind() {
						return 0, net.ErrClosed
					}
					continue
				}
			}
		}

		n, err := s.Write(b)
		if err == nil {
			c.lastWrite.Store(time.Now().UnixNano())
			return n, err
		}
		// Partial write with error: return what was written. The loop
		// below would otherwise rebind and re-Write the full buffer,
		// which duplicates the n bytes already transmitted on the old
		// stream. io.Copy and most stdlib writers correctly break out
		// on (n, err), so the caller will close the bridgedConn and
		// we'll never re-enter this path with the same byte range.
		if n > 0 {
			c.lastWrite.Store(time.Now().UnixNano())
			return n, err
		}

		// Write error — check if bridge was killed
		if c.bridge.ctx.Err() != nil {
			return 0, net.ErrClosed
		}

		// Clean close on the write side (peer's read half closed, or
		// we closed locally). Same reasoning as Read: rebinding here
		// produces the self-spin storm against an already-dead
		// exit-side bridge.
		if isCleanClose(err) {
			return 0, err
		}

		if c.rebindsWithoutData.Load() >= maxZombieRebinds {
			return 0, err
		}

		if !c.tryRebind() {
			return 0, err
		}
	}
}

// isCleanClose tells apart a normal stream-close (remote FIN, our
// side Close, half-close) from a network-level failure worth trying
// to rebind through. QUIC stream libraries surface remote FIN as
// io.EOF; half-reads as io.ErrUnexpectedEOF; local Close() as
// net.ErrClosed. None of these should trigger rebind — the peer has
// definitively finished with this stream and no new stream to the
// exit can stitch back to the now-gone exit-side bridge.
func isCleanClose(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed)
}

// Ceiling on how many times Read/Write may push through a rebind
// without ever seeing a payload byte. Two strikes are enough to
// conclude the exit-side bridge is gone and give up; bigger values
// let the storm run longer before we break the loop.
const maxZombieRebinds = 2

func (c *bridgedConn) tryRebind() bool {
	c.mu.Lock()
	if c.rebinding {
		done := c.rebindDone
		c.mu.Unlock()
		<-done
		return bridgeState(c.bridge.state.Load()) == bridgeActive
	}
	c.rebinding = true
	c.rebindDone = make(chan struct{})
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.rebinding = false
		close(c.rebindDone)
		c.mu.Unlock()
	}()

	c.bridge.suspend()

	deadline := time.After(bridgeTimeout)
	// Poll aggressively at first (500ms), then slow to 2s
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	attempts := 0

	for {
		select {
		case <-deadline:
			c.bridge.die()
			c.node.bridges.Remove(c.bridge.id)
			return false
		case <-c.bridge.ctx.Done():
			return false
		case <-ticker.C:
			attempts++
			// Slow down after initial burst
			if attempts == 6 {
				ticker.Reset(2 * time.Second)
			}
			c.node.mu.RLock()
			p, ok := c.node.peers[c.peerName]
			c.node.mu.RUnlock()
			if !ok || p.client == nil {
				continue
			}

			cl := p.pickClient()
			var rebindAddr string
			if len(c.viaPath) > 1 {
				// Multi-hop via: re-open via chain with bridge tag for rebind
				remaining := ""
				for i := 1; i < len(c.viaPath); i++ {
					if i > 1 {
						remaining += "/"
					}
					remaining += c.viaPath[i]
				}
				taggedAddr := c.viaTargetAddr + "#bridge=" + c.bridge.id
				rebindAddr = streamViaPrefix + remaining + "_" + taggedAddr + ":0"
			} else {
				rebindAddr = bridgeRebindAddr + c.bridge.id + ":0"
			}
			newStream, err := cl.TCP(rebindAddr)
			if err != nil {
				continue
			}

			c.mu.Lock()
			// Close the old wrapper before replacing it — countedConn.Close
			// is what decrements n.conns and releases the dead QUIC stream
			// reference. Overwriting c.stream without closing orphans the
			// old countedConn, leaking one conn-count per successful rebind
			// (observable as monotonically growing "active streams" on nodes
			// with unstable upstream links that trigger frequent rebinds).
			oldStream := c.stream
			c.stream = c.node.wrapConn(c.peerName, newStream)
			c.bridge.mu.Lock()
			c.bridge.relayStream = newStream
			c.bridge.state.Store(int32(bridgeActive))
			c.bridge.mu.Unlock()
			// Reset congestion tracking so proactive rebind doesn't fire immediately
			now := time.Now().UnixNano()
			c.lastRead.Store(now)
			c.lastWrite.Store(0)
			c.mu.Unlock()
			if oldStream != nil {
				oldStream.Close()
			}
			// Count this rebind — Read resets the counter when it sees
			// a byte of payload on the new stream. Two consecutive
			// rebinds without any payload trigger the zombie cap in
			// Read/Write and stop the spin.
			c.rebindsWithoutData.Add(1)
			// Monitor goroutine still watches same bridge.ctx — no restart needed
			log.Printf("[bridge] %s rebound on requester side (peer %s)", c.bridge.id, c.peerName)
			return true
		}
	}
}

func (c *bridgedConn) Close() error {
	c.closed = true
	c.bridge.die()
	c.node.bridges.Remove(c.bridge.id)
	c.mu.Lock()
	s := c.stream
	c.mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}

func (c *bridgedConn) LocalAddr() net.Addr {
	c.mu.Lock()
	s := c.stream
	c.mu.Unlock()
	if s != nil {
		return s.LocalAddr()
	}
	return nil
}

func (c *bridgedConn) RemoteAddr() net.Addr {
	c.mu.Lock()
	s := c.stream
	c.mu.Unlock()
	if s != nil {
		return s.RemoteAddr()
	}
	return nil
}

func (c *bridgedConn) SetDeadline(t time.Time) error      { return nil }
func (c *bridgedConn) SetReadDeadline(t time.Time) error   { return nil }
func (c *bridgedConn) SetWriteDeadline(t time.Time) error  { return nil }

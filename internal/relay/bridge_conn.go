package relay

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// bridgedConn wraps a relay stream in a bridge-aware net.Conn.
// On the requester side, when Read/Write fails (stream died), it suspends
// and attempts rebind by opening a new stream to the exit node.
type bridgedConn struct {
	bridge   *streamBridge
	node     *Node
	peerName string

	mu     sync.Mutex
	stream net.Conn
	closed bool
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
	if p.client == nil {
		return nil, "", fmt.Errorf("relay: peer %q is inbound (no client)", peerName)
	}

	cl := p.pickClient()
	stream, err := cl.TCP(addr)
	if err != nil {
		return nil, "", err
	}

	bridge := n.bridges.create(peerName, addr, stream)

	bc := &bridgedConn{
		bridge:   bridge,
		node:     n,
		peerName: peerName,
		stream:   n.wrapConn(peerName, stream),
	}
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
		if err == nil || n > 0 {
			return n, err
		}

		// Stream died — try rebind
		if !c.tryRebind() {
			return 0, err
		}
		// Rebind succeeded — retry read with new stream
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

		n, err := s.Write(b)
		if err == nil {
			return n, err
		}

		// Stream died — try rebind
		if !c.tryRebind() {
			return 0, err
		}
		// Rebind succeeded — retry write with new stream
	}
}

func (c *bridgedConn) tryRebind() bool {
	c.bridge.suspend()

	// Wait for peer to reconnect (up to 30s)
	deadline := time.After(bridgeTimeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			c.bridge.die()
			c.node.bridges.remove(c.bridge.id)
			return false
		case <-c.bridge.ctx.Done():
			return false
		case <-ticker.C:
			// Try to open a rebind stream to the peer
			c.node.mu.RLock()
			p, ok := c.node.peers[c.peerName]
			c.node.mu.RUnlock()
			if !ok || p.client == nil {
				continue // peer not reconnected yet
			}

			cl := p.pickClient()
			rebindAddr := bridgeRebindAddr + c.bridge.id + ":0"
			newStream, err := cl.TCP(rebindAddr)
			if err != nil {
				log.Printf("[bridge] rebind dial failed for %s: %v", c.bridge.id, err)
				continue
			}

			// Rebind succeeded
			c.mu.Lock()
			c.stream = c.node.wrapConn(c.peerName, newStream)
			c.bridge.mu.Lock()
			c.bridge.relayStream = newStream
			c.bridge.state.Store(int32(bridgeActive))
			c.bridge.mu.Unlock()
			c.mu.Unlock()
			log.Printf("[bridge] %s rebound on requester side (peer %s)", c.bridge.id, c.peerName)
			return true
		}
	}
}

func (c *bridgedConn) Close() error {
	c.closed = true
	c.bridge.die()
	c.node.bridges.remove(c.bridge.id)
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

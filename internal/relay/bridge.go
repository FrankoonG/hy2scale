package relay

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	bridgeTimeout    = 30 * time.Second // max wait for rebind before giving up
	bridgeBufSize    = 4 * 1024 * 1024  // 4MB per direction
	bridgeRebindAddr = "_relay_rebind_"
)

// bridgeState represents the lifecycle of a stream bridge.
type bridgeState int32

const (
	bridgeActive    bridgeState = 0
	bridgeSuspended bridgeState = 1
	bridgeDead      bridgeState = 2
)

// streamBridge sits between an application connection and a relay QUIC stream.
// When the relay stream dies (QUIC reconnect), the bridge suspends and waits
// for a new stream to rebind, then resumes data transfer transparently.
type streamBridge struct {
	id       string
	peerName string
	addr     string // original dial address (for rebind request)

	mu          sync.Mutex
	state       atomic.Int32 // bridgeState
	relayStream net.Conn     // current QUIC stream (replaceable)
	suspendedAt time.Time

	// Channels for coordinating rebind
	rebindCh chan net.Conn // receives new stream on rebind
	ctx      context.Context
	cancel   context.CancelFunc

	// Stats
	txBytes atomic.Int64
	rxBytes atomic.Int64
}

// bridgeManager tracks all active bridges on a node for rebind lookup.
type bridgeManager struct {
	mu      sync.RWMutex
	bridges map[string]*streamBridge // id → bridge
	seq     atomic.Uint64
}

func newBridgeManager() *bridgeManager {
	return &bridgeManager{bridges: make(map[string]*streamBridge)}
}

func (m *bridgeManager) nextID() string {
	return fmt.Sprintf("sb_%d", m.seq.Add(1))
}

// create registers a new bridge for an active connection.
func (m *bridgeManager) create(peerName, addr string, relayStream net.Conn) *streamBridge {
	id := m.nextID()
	ctx, cancel := context.WithCancel(context.Background())
	b := &streamBridge{
		id:          id,
		peerName:    peerName,
		addr:        addr,
		relayStream: relayStream,
		rebindCh:    make(chan net.Conn, 1),
		ctx:         ctx,
		cancel:      cancel,
	}
	m.mu.Lock()
	m.bridges[id] = b
	m.mu.Unlock()
	return b
}

// lookup finds a suspended bridge for rebinding.
func (m *bridgeManager) lookup(id string) *streamBridge {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bridges[id]
}

// remove cleans up a dead bridge.
func (m *bridgeManager) remove(id string) {
	m.mu.Lock()
	delete(m.bridges, id)
	m.mu.Unlock()
}

// rebind delivers a new stream to a suspended bridge.
// Returns true if the bridge was found and rebind succeeded.
func (m *bridgeManager) rebind(id string, newStream net.Conn) bool {
	b := m.lookup(id)
	if b == nil {
		return false
	}
	if bridgeState(b.state.Load()) != bridgeSuspended {
		return false
	}
	select {
	case b.rebindCh <- newStream:
		return true
	default:
		return false
	}
}

// suspend marks the bridge as waiting for rebind. Called when relay stream dies.
func (b *streamBridge) suspend() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bridgeState(b.state.Load()) != bridgeActive {
		return
	}
	b.state.Store(int32(bridgeSuspended))
	b.suspendedAt = time.Now()
	log.Printf("[bridge] %s suspended (peer %s, addr %s)", b.id, b.peerName, b.addr)
}

// waitRebind blocks until a new stream arrives or timeout.
// Returns the new stream, or nil if timeout/canceled.
func (b *streamBridge) waitRebind() net.Conn {
	timeout := bridgeTimeout - time.Since(b.suspendedAt)
	if timeout <= 0 {
		return nil
	}
	select {
	case stream := <-b.rebindCh:
		b.mu.Lock()
		b.relayStream = stream
		b.state.Store(int32(bridgeActive))
		b.mu.Unlock()
		log.Printf("[bridge] %s rebound (peer %s)", b.id, b.peerName)
		return stream
	case <-time.After(timeout):
		return nil
	case <-b.ctx.Done():
		return nil
	}
}

// die marks the bridge as dead and cleans up.
func (b *streamBridge) die() {
	b.state.Store(int32(bridgeDead))
	b.cancel()
	log.Printf("[bridge] %s dead (peer %s)", b.id, b.peerName)
}

// RunRelay runs the bidirectional relay between appConn and the relay stream,
// handling suspend/rebind transparently. Blocks until the connection ends.
func (b *streamBridge) RunRelay(appConn net.Conn, mgr *bridgeManager) {
	defer func() {
		b.die()
		mgr.remove(b.id)
		appConn.Close()
		if b.relayStream != nil {
			b.relayStream.Close()
		}
	}()

	for {
		stream := b.relayStream
		if stream == nil {
			return
		}

		// Bidirectional copy with early termination detection
		done := make(chan struct{})
		var copyErr atomic.Value

		// relay → app
		go func() {
			_, err := io.Copy(appConn, stream)
			if err != nil {
				copyErr.Store(err)
			}
			close(done)
		}()

		// app → relay
		_, err := io.Copy(stream, appConn)
		if err != nil {
			copyErr.Store(err)
		}

		// Wait for the other direction to finish
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			stream.Close() // force close to unblock the other direction
			<-done
		}

		// Check if this is a recoverable error (stream died, not app died)
		// If app connection is also dead, no point in rebinding
		if !isStreamError(copyErr.Load()) {
			return // app-side error, clean exit
		}

		// Stream died — try to rebind
		b.suspend()

		newStream := b.waitRebind()
		if newStream == nil {
			// Timeout or canceled — give up
			return
		}

		// Rebind succeeded — loop back to relay with new stream
		stream.Close()
	}
}

// isStreamError checks if the error looks like a relay stream failure
// (as opposed to the application side closing normally).
func isStreamError(errVal any) bool {
	if errVal == nil {
		return false // clean close, no error
	}
	err, ok := errVal.(error)
	if !ok {
		return false
	}
	// net.ErrClosed from idleTimeoutConn or QUIC stream death
	if err == net.ErrClosed {
		return true
	}
	// io.EOF is normal close, not a stream error
	if err == io.EOF {
		return false
	}
	// Any other error is likely a stream issue
	return true
}

// HandleBridgeAddr processes a bridge-tagged address from the exit node.
// Returns (targetAddr, bridgeID, isBridged).
func ParseBridgeAddr(addr string) (string, string, bool) {
	idx := strings.Index(addr, "#bridge=")
	if idx < 0 {
		return addr, "", false
	}
	return addr[:idx], addr[idx+8:], true
}

// TryRebind attempts to rebind a suspended bridge. Returns a net.Conn pipe
// if successful (caller should return this as the TCP connection), or nil.
func (m *bridgeManager) TryRebind(bridgeID string) net.Conn {
	b := m.lookup(bridgeID)
	if b == nil || bridgeState(b.state.Load()) != bridgeSuspended {
		return nil
	}
	c1, c2 := net.Pipe()
	if m.rebind(bridgeID, c1) {
		return c2
	}
	c1.Close()
	c2.Close()
	return nil
}

// CreateWithID creates a bridge with a specific ID (from the requester).
// Returns a net.Conn pipe — the caller uses one end for the hy2 server,
// and the bridge uses the other end internally.
func (m *bridgeManager) CreateWithID(bridgeID, addr string, targetConn net.Conn) net.Conn {
	ctx, cancel := context.WithCancel(context.Background())
	b := &streamBridge{
		id:       bridgeID,
		addr:     addr,
		rebindCh: make(chan net.Conn, 1),
		ctx:      ctx,
		cancel:   cancel,
	}
	m.mu.Lock()
	m.bridges[bridgeID] = b
	m.mu.Unlock()

	c1, c2 := net.Pipe()
	b.relayStream = c1
	b.state.Store(int32(bridgeActive))
	go b.RunRelay(targetConn, m)
	return c2
}

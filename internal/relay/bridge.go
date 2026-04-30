package relay

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	bridgeTimeout      = 90 * time.Second // max wait for rebind before giving up (must exceed QUIC idle timeout)
	bridgeCongestionTO = 8 * time.Second  // bidirectional silence threshold: stream considered stuck → proactive rebind
	bridgeRebindAddr   = "_relay_rebind_"
)

// ConnState captures the metrics a RebindTrigger evaluates.
// Values are unix nanoseconds; zero means "no activity yet".
type ConnState struct {
	LastRead  int64
	LastWrite int64
	ViaPath   []string // current via path; len==0 means direct (single-hop bridge)
}

// RebindTrigger decides whether a bridged stream should be torn down and
// rebound. Triggers run on the writer hot path; ShouldRebind must be O(1).
// Implementations may consider any field of ConnState; new fields will be
// added without breaking compatibility.
type RebindTrigger interface {
	ShouldRebind(s ConnState) (yes bool, reason string)
}

// BidirectionalIdleTrigger fires when *both* directions have been silent
// for Timeout. Replaces the old "no Read while Writing" rule, which
// wrongly fired during legitimate one-way uploads (e.g. HTTP POST while
// the server hadn't responded yet) and slow downloads. Bidirectional
// silence remains a strong signal of a truly stuck stream — the same
// signal the cn-xinchang storm fixes were aimed at.
type BidirectionalIdleTrigger struct {
	Timeout time.Duration
}

// ShouldRebind implements RebindTrigger.
func (t BidirectionalIdleTrigger) ShouldRebind(s ConnState) (bool, string) {
	last := s.LastRead
	if s.LastWrite > last {
		last = s.LastWrite
	}
	if last == 0 {
		// Stream just opened; no activity to compare against. Treat as healthy.
		return false, ""
	}
	stall := time.Since(time.Unix(0, last))
	if stall > t.Timeout {
		return true, fmt.Sprintf("idle both ways for %v", stall.Round(time.Second))
	}
	return false, ""
}

// RebindOpts allows callers to override defaults when explicitly invoking
// rebind. Currently only the via path can be overridden — left as the sole
// member so the type is the obvious extension point for future cross-path
// migration work.
type RebindOpts struct {
	ViaPath []string // if non-nil, replace bridgedConn.viaPath for this rebind
}

// defaultTriggers returns the trigger set installed on every bridgedConn
// created in 1.3.1.
//
// Empty by default. The earlier "no Read while Writing for 8 s" rule
// (and its bidirectional successor) cannot distinguish "stream is
// stuck" from "upstream is rate-limited and the chain has nothing to
// flush right now" — both look like silence on the wire. Once the
// trigger fires it tears down the stream, breaking otherwise-healthy
// long slow transfers.
//
// Liveness for v1.3.1 is enforced by three other layers, all of which
// stay in place:
//
//   1. QUIC's own connection-level idle timeout (30 s) drops a
//      truly dead transport.
//   2. idleTimeoutConn (10 min) catches application-level dead
//      streams when QUIC misses them.
//   3. The traffic-aware health-check disconnect (this commit's
//      sibling change) tears down a peer whose probes fail AND whose
//      byte counters haven't moved.
//
// The RebindTrigger interface remains as the integration point for
// future, smarter triggers (path-quality migration, BBR-aware
// stalling, etc.), so opting into one is one constructor argument
// away — but the bug-prone universal default goes.
func defaultTriggers() []RebindTrigger {
	return nil
}

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
	return fmt.Sprintf("sb_%x_%d", time.Now().UnixNano()&0xFFFF, m.seq.Add(1))
}

// Create registers a new bridge for an active connection.
func (m *bridgeManager) Create(peerName, addr string, relayStream net.Conn) *streamBridge {
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

// Lookup finds a suspended bridge for rebinding.
func (m *bridgeManager) Lookup(id string) *streamBridge {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bridges[id]
}

// Remove cleans up a dead bridge.
func (m *bridgeManager) Remove(id string) {
	m.mu.Lock()
	delete(m.bridges, id)
	m.mu.Unlock()
}

// Rebind delivers a new stream to a suspended bridge.
// Returns true if the bridge was found and rebind succeeded.
func (m *bridgeManager) Rebind(id string, newStream net.Conn) bool {
	b := m.Lookup(id)
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
		mgr.Remove(b.id)
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

		// errSource distinguishes stream-side vs app-side failures.
		// streamSide=true means the relay (QUIC) stream had a TRANSPORT
		// failure — worth suspending for a rebind. A *clean* stream
		// close (io.EOF / net.ErrClosed) means the requester is done
		// with this connection on purpose: app-close propagated through
		// HUB → relay-stream → us → exit's copyTwoWay → net.Pipe() c2 →
		// our pipe-end c1 → here as readErr=io.EOF. Treating that as a
		// transport failure makes RunRelay suspend and wait the full
		// 90s bridgeTimeout for a rebind that never comes — which is
		// exactly the "app-close vs relay-close lag" bug. Mark clean
		// closes as app-side so the if-!streamSide branch terminates
		// immediately. Real QUIC drops surface as quic.* errors, NOT
		// io.EOF, so the resilience case is preserved.
		type errSource struct {
			streamSide bool // true = recoverable relay-stream failure
		}
		errCh := make(chan errSource, 2)

		// relay → app: read from stream, write to appConn.
		// If stream.Read fails → stream died. If appConn.Write fails → app died.
		go func() {
			buf := make([]byte, 32*1024)
			for {
				nr, readErr := stream.Read(buf)
				if nr > 0 {
					if _, writeErr := appConn.Write(buf[:nr]); writeErr != nil {
						errCh <- errSource{streamSide: false}
						return
					}
				}
				if readErr != nil {
					errCh <- errSource{streamSide: !isCleanClose(readErr)}
					return
				}
			}
		}()

		// app → relay: read from appConn, write to stream.
		// If appConn.Read fails → app died. If stream.Write fails → stream died.
		go func() {
			buf := make([]byte, 32*1024)
			for {
				nr, readErr := appConn.Read(buf)
				if nr > 0 {
					if _, writeErr := stream.Write(buf[:nr]); writeErr != nil {
						errCh <- errSource{streamSide: !isCleanClose(writeErr)}
						return
					}
				}
				if readErr != nil {
					errCh <- errSource{streamSide: false}
					return
				}
			}
		}()

		// Wait for the first error
		first := <-errCh

		// Close stream to unblock goroutines, set short deadline on appConn
		stream.Close()
		appConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		select {
		case <-errCh: // drain second goroutine
		case <-time.After(5 * time.Second):
		}
		appConn.SetReadDeadline(time.Time{})

		if !first.streamSide {
			return // app-side close, no point rebinding
		}

		// Stream died — try to rebind
		b.suspend()

		newStream := b.waitRebind()
		if newStream == nil {
			return
		}

		// Rebind succeeded — loop back to relay with new stream
	}
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
	b := m.Lookup(bridgeID)
	if b == nil || bridgeState(b.state.Load()) != bridgeSuspended {
		return nil
	}
	c1, c2 := net.Pipe()
	if m.Rebind(bridgeID, c1) {
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

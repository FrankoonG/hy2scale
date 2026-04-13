package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Bond multiplexes a single TCP flow across multiple relay paths for bandwidth
// aggregation. Each path carries framed chunks with sequence numbers. The
// receiver reorders chunks and delivers them in order.
//
// Architecture:
//   app.dialBond() — called by dialLoadBalance when exit_mode=speed
//   ├── opens N relay streams (one per path) to the same exit node
//   ├── runs bondWriter (splits outgoing data across paths by weight)
//   └── runs bondReader (reorders incoming chunks by sequence number)
//
// The exit node sees N separate relay connections to the same target address,
// but with a bond session header that tells it to merge them.
//
// Frame format (per chunk on each path stream):
//   [4 bytes] bond_id   — identifies which bond session this belongs to
//   [4 bytes] seq       — global sequence number across all paths
//   [2 bytes] len       — chunk payload length (0 = keepalive/control)
//   [N bytes] data      — chunk payload
//
// Control frames (len=0):
//   seq=0: bond setup (followed by 2-byte path_index)
//   seq=0xFFFFFFFF: bond teardown

const (
	bondFrameHeaderSize = 10 // 4 + 4 + 2
	bondChunkSize       = 32768 // 32KB default chunk
	bondMinChunkSize    = 4096
	bondMaxChunkSize    = 65535
	bondSetupSeq        = 0
	bondWeightSeq       = 0xFFFFFFFE
	bondTeardownSeq     = 0xFFFFFFFF
)

// bondSession is the sender-side state for a multi-path bond.
type bondSession struct {
	id       uint32
	target   string // exit node name
	addr     string // destination address (e.g. 1.1.1.1:443)

	mu       sync.Mutex
	paths    []*bondPath
	writeSeq atomic.Uint32
	readSeq  atomic.Uint32

	// Reorder buffer for incoming data
	reorderMu   sync.Mutex
	reorderBuf  map[uint32][]byte // seq → data
	reorderNext uint32            // next expected seq
	reorderCh   chan struct{}      // signal when new data arrives
	seqPath     map[uint32]int    // seq → which path index sent it (for skip on death)

	// Dynamic buffer limit
	bufferLimit int // max bytes in reorder buffer

	closed   atomic.Bool
	closeCh  chan struct{}
}

// bondPath represents one path in the bond.
type bondPath struct {
	name    string   // path string (e.g. "au" or "jp/au")
	conn    net.Conn // relay stream
	index   int
	weight  float64  // 0.0–1.0, proportional send allocation
	rttMs   float64  // estimated RTT
	healthy bool
	txBytes int64
	mu      sync.Mutex
}

var bondIDCounter atomic.Uint32

// dialBond creates a bonded multi-path connection to the target exit node.
func (a *App) dialBond(ctx context.Context, target, addr string) (net.Conn, error) {
	paths := a.findPathsTo(target)
	if len(paths) == 0 {
		return nil, fmt.Errorf("bond: no paths to %s", target)
	}
	if len(paths) == 1 {
		debugLog("[bond] %s: only 1 path, using direct", target)
		return a.dialPath(ctx, paths[0], addr)
	}

	// Filter paths: exclude those with RTT > 3x the best path's RTT
	// High-latency paths cause reorder stalls that hurt more than they help
	if len(paths) > 2 {
		sortPathsByScore(target, paths)
		bestRTT := getPathScore(target, paths[0]).emaRTT
		if bestRTT < 10 {
			bestRTT = 10 // minimum 10ms baseline
		}
		maxRTT := bestRTT * 3
		var filtered []string
		for _, p := range paths {
			ps := getPathScore(target, p)
			if ps.samples == 0 || ps.emaRTT <= maxRTT {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) >= 2 {
			log.Printf("[bond] %s: filtered %d→%d paths (maxRTT=%.0fms)", target, len(paths), len(filtered), maxRTT)
			paths = filtered
		}
	}

	bondID := bondIDCounter.Add(1)
	sess := &bondSession{
		id:          bondID,
		target:      target,
		addr:        addr,
		reorderBuf:  make(map[uint32][]byte),
		reorderNext: 1,
		reorderCh:   make(chan struct{}, 1),
		seqPath:     make(map[uint32]int),
		bufferLimit: 2 * 1024 * 1024, // 2MB initial
		closeCh:     make(chan struct{}),
	}

	log.Printf("[bond] %s: starting session %d with %d paths: %v", target, bondID, len(paths), paths)

	// Open a relay stream per path, each targeting the same addr
	// The exit node will see these as separate TCP connections to addr
	// We need the exit node to know they're bonded — we use a special
	// bond address prefix so the exit node can handle it.
	bondAddr := fmt.Sprintf("_bond_%d_%s", bondID, addr)

	var opened []*bondPath
	for i, p := range paths {
		// Pin each path to a specific QUIC client index to ensure
		// duplicate peer paths (multi-IP) use different connections.
		conn, err := a.dialPathIdx(ctx, p, bondAddr, i)
		if err != nil {
			log.Printf("[bond] %s: path %s failed to open: %v", target, p, err)
			// Add as unhealthy — health monitor will retry later
			bp := &bondPath{
				name:    p,
				index:   i,
				weight:  0,
				healthy: false,
			}
			opened = append(opened, bp)
			continue
		}

		bp := &bondPath{
			name:    p,
			conn:    conn,
			index:   i,
			weight:  1.0 / float64(len(paths)),
			healthy: true,
		}
		opened = append(opened, bp)

		// Send bond setup frame
		if err := writeBondSetup(conn, bondID, i); err != nil {
			log.Printf("[bond] %s: path %s setup failed: %v", target, p, err)
			conn.Close()
			continue
		}
	}

	healthyCount := 0
	for _, bp := range opened {
		if bp.healthy {
			healthyCount++
		}
	}
	if healthyCount == 0 {
		return nil, fmt.Errorf("bond: all paths to %s failed", target)
	}

	sess.paths = opened
	sess.updateWeights()

	log.Printf("[bond] %s: session %d active with %d/%d paths (%d healthy)", target, bondID, len(opened), len(paths), healthyCount)

	// Create a net.Conn-compatible pipe:
	// - app side reads/writes to userConn
	// - bond goroutines read/write to bondConn
	userConn, bondConn := net.Pipe()

	// Start writer: reads from bondConn, splits to paths
	go sess.runWriter(bondConn)

	// Start readers: read from each path, feed into reorder buffer
	for _, bp := range opened {
		if bp.healthy && bp.conn != nil {
			go sess.runPathReader(bp)
		}
	}

	// Start deliverer: delivers reordered data to bondConn
	go sess.runDeliverer(bondConn)

	// Start health monitor
	go sess.runHealthMonitor(a)

	return userConn, nil
}

// writeBondSetup sends the initial setup frame on a path stream.
func writeBondSetup(w net.Conn, bondID uint32, pathIndex int) error {
	var buf [bondFrameHeaderSize + 2]byte
	binary.BigEndian.PutUint32(buf[0:4], bondID)
	binary.BigEndian.PutUint32(buf[4:8], bondSetupSeq)
	binary.BigEndian.PutUint16(buf[8:10], 2) // payload = 2 bytes (path index)
	binary.BigEndian.PutUint16(buf[10:12], uint16(pathIndex))
	_, err := w.Write(buf[:])
	return err
}

// runWriter reads data from the local side and distributes chunks across paths.
func (sess *bondSession) runWriter(src net.Conn) {
	buf := make([]byte, bondChunkSize)
	var totalBytes int64

	for {
		if sess.closed.Load() {
			return
		}

		n, err := src.Read(buf)
		if n > 0 {
			totalBytes += int64(n)
			seq := sess.writeSeq.Add(1)
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			// Select path by weighted round-robin. Wait if none available.
			bp := sess.selectPath(int(seq))
			if bp == nil {
				// Wait up to 10s for a path to become healthy
				for i := 0; i < 50; i++ {
					time.Sleep(200 * time.Millisecond)
					if sess.closed.Load() {
						return
					}
					bp = sess.selectPath(int(seq))
					if bp != nil {
						break
					}
				}
				if bp == nil {
					log.Printf("[bond] %d: no healthy path after 10s for seq %d", sess.id, seq)
					return
				}
			}

			if err2 := sess.writeChunk(bp, seq, chunk); err2 != nil {
				debugLog("[bond] %d: write to path %s failed: %v", sess.id, bp.name, err2)
				bp.mu.Lock()
				bp.healthy = false
				bp.mu.Unlock()
				sess.updateWeights()
				// Retry on another path
				bp2 := sess.selectPath(int(seq))
				if bp2 != nil {
					sess.writeChunk(bp2, seq, chunk)
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				debugLog("[bond] %d: writer read error: %v", sess.id, err)
			}
			return
		}
	}
}

// writeChunk sends a framed chunk on a specific path.
func (sess *bondSession) writeChunk(bp *bondPath, seq uint32, data []byte) error {
	var hdr [bondFrameHeaderSize]byte
	binary.BigEndian.PutUint32(hdr[0:4], sess.id)
	binary.BigEndian.PutUint32(hdr[4:8], seq)
	binary.BigEndian.PutUint16(hdr[8:10], uint16(len(data)))

	bp.mu.Lock()
	defer bp.mu.Unlock()
	if _, err := bp.conn.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := bp.conn.Write(data); err != nil {
		return err
	}
	bp.txBytes += int64(len(data))
	return nil
}

// selectPath picks the next path using weighted selection.
func (sess *bondSession) selectPath(seq int) *bondPath {
	sess.mu.Lock()
	defer sess.mu.Unlock()

	var healthy []*bondPath
	var totalWeight float64
	for _, bp := range sess.paths {
		bp.mu.Lock()
		h := bp.healthy
		w := bp.weight
		bp.mu.Unlock()
		if h {
			healthy = append(healthy, bp)
			totalWeight += w
		}
	}
	if len(healthy) == 0 {
		return nil
	}
	if len(healthy) == 1 {
		return healthy[0]
	}

	// Weighted round-robin: use cumulative weight thresholds
	pos := float64(seq) / totalWeight
	pos = pos - math.Floor(pos) // normalize to 0..1
	pos *= totalWeight

	var cumulative float64
	for _, bp := range healthy {
		bp.mu.Lock()
		w := bp.weight
		bp.mu.Unlock()
		cumulative += w
		if pos < cumulative {
			return bp
		}
	}
	return healthy[len(healthy)-1]
}

// runPathReader reads framed chunks from a single path and feeds into reorder buffer.
func (sess *bondSession) runPathReader(bp *bondPath) {
	bp.mu.Lock()
	conn := bp.conn
	bp.mu.Unlock()
	if conn == nil {
		return
	}
	var hdr [bondFrameHeaderSize]byte
	for {
		if sess.closed.Load() {
			return
		}

		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			if sess.closed.Load() {
				return
			}
			log.Printf("[bond] %d: path %s read error: %v", sess.id, bp.name, err)
			bp.mu.Lock()
			bp.healthy = false
			// Close the connection to signal the remote receiver
			if bp.conn != nil {
				bp.conn.Close()
			}
			bp.mu.Unlock()
			sess.updateWeights()
			sess.skipReorderGaps()
			return
		}

		// bondID := binary.BigEndian.Uint32(hdr[0:4])
		seq := binary.BigEndian.Uint32(hdr[4:8])
		length := binary.BigEndian.Uint16(hdr[8:10])

		if seq == bondTeardownSeq {
			debugLog("[bond] %d: path %s received teardown", sess.id, bp.name)
			bp.mu.Lock()
			bp.healthy = false
			bp.mu.Unlock()
			// Check if all paths got teardown → close session
			sess.mu.Lock()
			allDone := true
			for _, p := range sess.paths {
				p.mu.Lock()
				h := p.healthy
				p.mu.Unlock()
				if h {
					allDone = false
					break
				}
			}
			sess.mu.Unlock()
			if allDone {
				sess.close()
			}
			return
		}

		if length == 0 {
			continue // keepalive
		}

		data := make([]byte, length)
		if _, err := io.ReadFull(conn, data); err != nil {
			debugLog("[bond] %d: path %s read data error: %v", sess.id, bp.name, err)
			bp.mu.Lock()
			bp.healthy = false
			bp.mu.Unlock()
			return
		}

		// Insert into reorder buffer
		sess.reorderMu.Lock()
		sess.reorderBuf[seq] = data
		sess.reorderMu.Unlock()

		// Signal deliverer
		select {
		case sess.reorderCh <- struct{}{}:
		default:
		}
	}
}

// runDeliverer delivers reordered data to the local side in sequence order.
func (sess *bondSession) runDeliverer(dst net.Conn) {
	defer dst.Close()
	for {
		if sess.closed.Load() {
			return
		}

		// Try to deliver contiguous chunks
		delivered := false
		sess.reorderMu.Lock()
		for {
			data, ok := sess.reorderBuf[sess.reorderNext]
			if !ok {
				break
			}
			delete(sess.reorderBuf, sess.reorderNext)
			sess.reorderNext++
			sess.reorderMu.Unlock()

			if _, err := dst.Write(data); err != nil {
				debugLog("[bond] %d: deliverer write error: %v", sess.id, err)
				sess.close()
				return
			}
			delivered = true
			sess.reorderMu.Lock()
		}
		bufSize := len(sess.reorderBuf)
		sess.reorderMu.Unlock()

		if !delivered {
			if bufSize > 0 {
				// If a path is dead, skip gaps aggressively (no waiting)
				if sess.hasDeadPaths() {
					sess.skipReorderGaps()
					continue
				}
				// Spin waiting for the next expected seq
				spinIters := 50 + bufSize*20
				if spinIters > 500 {
					spinIters = 500
				}
				spun := false
				for i := 0; i < spinIters; i++ {
					time.Sleep(100 * time.Microsecond)
					sess.reorderMu.Lock()
					_, ready := sess.reorderBuf[sess.reorderNext]
					sess.reorderMu.Unlock()
					if ready {
						spun = true
						break
					}
				}
				if spun {
					continue
				}
			}
			timeout := 100 * time.Millisecond
			if bufSize == 0 {
				timeout = 5 * time.Second
			}
			select {
			case <-sess.reorderCh:
			case <-time.After(timeout):
				if bufSize > 0 {
					sess.skipReorderGaps()
				}
			case <-sess.closeCh:
				return
			}
		}
	}
}

// shouldSkipSeq returns true if we should skip the current missing seq
// (because the path responsible for it is dead).
func (sess *bondSession) shouldSkipSeq() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	healthyCount := 0
	for _, bp := range sess.paths {
		bp.mu.Lock()
		if bp.healthy {
			healthyCount++
		}
		bp.mu.Unlock()
	}
	// If we lost paths, some seqs will never arrive
	return healthyCount < len(sess.paths)
}

// skipReorderGaps advances reorderNext past any missing sequences
// when a path dies. Finds the lowest buffered seq and jumps to it.
func (sess *bondSession) skipReorderGaps() {
	sess.reorderMu.Lock()
	skipped := 0
	for {
		if _, ok := sess.reorderBuf[sess.reorderNext]; ok {
			break
		}
		var minSeq uint32
		found := false
		for s := range sess.reorderBuf {
			if s > sess.reorderNext && (!found || s < minSeq) {
				minSeq = s
				found = true
			}
		}
		if !found {
			break
		}
		skipped += int(minSeq - sess.reorderNext)
		sess.reorderNext = minSeq
	}
	sess.reorderMu.Unlock()
	if skipped > 0 {
		if skipped > 3 {
			log.Printf("[bond] %d: skipped %d missing seqs", sess.id, skipped)
		}
		select {
		case sess.reorderCh <- struct{}{}:
		default:
		}
	}
}

// hasDeadPaths returns true if any bond path is unhealthy.
func (sess *bondSession) hasDeadPaths() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, bp := range sess.paths {
		bp.mu.Lock()
		h := bp.healthy
		bp.mu.Unlock()
		if !h {
			return true
		}
	}
	return false
}

// updateWeights recalculates path weights based on RTT.
func (sess *bondSession) updateWeights() {
	sess.mu.Lock()
	defer sess.mu.Unlock()

	var healthy []*bondPath
	for _, bp := range sess.paths {
		bp.mu.Lock()
		if bp.healthy {
			healthy = append(healthy, bp)
		}
		bp.mu.Unlock()
	}

	if len(healthy) == 0 {
		return
	}

	// Weight = 1/RTT (inverse proportional to latency)
	var sumInvRTT float64
	for _, bp := range healthy {
		bp.mu.Lock()
		rtt := bp.rttMs
		bp.mu.Unlock()
		if rtt < 1 {
			rtt = 1 // minimum 1ms
		}
		sumInvRTT += 1.0 / rtt
	}

	for _, bp := range healthy {
		bp.mu.Lock()
		rtt := bp.rttMs
		if rtt < 1 {
			rtt = 1
		}
		bp.weight = (1.0 / rtt) / sumInvRTT
		bp.mu.Unlock()
	}

	// Update dynamic buffer limit based on RTT spread
	var minRTT, maxRTT float64 = math.MaxFloat64, 0
	for _, bp := range healthy {
		bp.mu.Lock()
		if bp.rttMs < minRTT {
			minRTT = bp.rttMs
		}
		if bp.rttMs > maxRTT {
			maxRTT = bp.rttMs
		}
		bp.mu.Unlock()
	}
	deltaMs := maxRTT - minRTT
	if deltaMs < 50 {
		deltaMs = 50 // minimum 50ms buffer
	}
	// Buffer = delta_RTT * estimated_throughput (assume 50Mbps max aggregate)
	sess.bufferLimit = int(deltaMs * 50000 / 8) // delta_ms * 50Mbps / 8 = bytes
	if sess.bufferLimit < 512*1024 {
		sess.bufferLimit = 512 * 1024 // minimum 512KB
	}
	if sess.bufferLimit > 8*1024*1024 {
		sess.bufferLimit = 8 * 1024 * 1024 // max 8MB
	}

	debugLog("[bond] %d: weights updated: %d healthy, buffer=%dKB, delta_rtt=%.0fms",
		sess.id, len(healthy), sess.bufferLimit/1024, deltaMs)
}

// runHealthMonitor periodically checks path health and updates weights.
func (sess *bondSession) runHealthMonitor(a *App) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if sess.closed.Load() {
				return
			}

			// Update RTT from latency prober
			for _, bp := range sess.paths {
				parts := splitPath(bp.name)
				if len(parts) > 0 {
					firstHop := parts[0]
					latMs := a.node.GetLatency(firstHop)
					bp.mu.Lock()
					if latMs > 0 {
						bp.rttMs = float64(latMs)
					} else if bp.rttMs == 0 {
						bp.rttMs = 100 // default estimate
					}
					bp.mu.Unlock()
				}
			}

			sess.updateWeights()

			// Send weight hints to receiver (via path 0, which is always connected)
			sess.sendWeightHints()

			// Try to reopen dead paths
			for _, bp := range sess.paths {
				bp.mu.Lock()
				dead := !bp.healthy
				bp.mu.Unlock()
				if dead {
					sess.tryReopenPath(a, bp)
				}
			}

		case <-sess.closeCh:
			return
		}
	}
}

// tryReopenPath attempts to reconnect a failed path.
func (sess *bondSession) tryReopenPath(a *App, bp *bondPath) {
	bondAddr := fmt.Sprintf("_bond_%d_%s", sess.id, sess.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := a.dialPathIdx(ctx, bp.name, bondAddr, bp.index)
	if err != nil {
		return
	}

	if err := writeBondSetup(conn, sess.id, bp.index); err != nil {
		conn.Close()
		return
	}

	bp.mu.Lock()
	oldConn := bp.conn
	bp.conn = conn
	bp.healthy = true
	bp.mu.Unlock()

	if oldConn != nil {
		oldConn.Close()
	}

	log.Printf("[bond] %d: path %s reconnected", sess.id, bp.name)
	sess.updateWeights()

	// Start new reader for this path
	go sess.runPathReader(bp)
}

// sendWeightHints sends weight update to the receiver via path 0.
// Format: [bondFrameHeader with seq=bondWeightSeq] [N * (1B pathIndex, 1B weight%)]
func (sess *bondSession) sendWeightHints() {
	sess.mu.Lock()
	var firstHealthy *bondPath
	var payload []byte
	for _, bp := range sess.paths {
		bp.mu.Lock()
		if bp.healthy && firstHealthy == nil {
			firstHealthy = bp
		}
		w := byte(0)
		if bp.healthy {
			w = byte(bp.weight * 100) // 0-100 percentage
		}
		payload = append(payload, byte(bp.index), w)
		bp.mu.Unlock()
	}
	sess.mu.Unlock()

	if firstHealthy == nil || len(payload) == 0 {
		return
	}

	var hdr [bondFrameHeaderSize]byte
	binary.BigEndian.PutUint32(hdr[0:4], sess.id)
	binary.BigEndian.PutUint32(hdr[4:8], bondWeightSeq)
	binary.BigEndian.PutUint16(hdr[8:10], uint16(len(payload)))

	firstHealthy.mu.Lock()
	firstHealthy.conn.Write(hdr[:])
	firstHealthy.conn.Write(payload)
	firstHealthy.mu.Unlock()
}

func (sess *bondSession) close() {
	if sess.closed.CompareAndSwap(false, true) {
		close(sess.closeCh)

		// Send teardown on all paths
		for _, bp := range sess.paths {
			bp.mu.Lock()
			if bp.conn != nil {
				var hdr [bondFrameHeaderSize]byte
				binary.BigEndian.PutUint32(hdr[0:4], sess.id)
				binary.BigEndian.PutUint32(hdr[4:8], bondTeardownSeq)
				binary.BigEndian.PutUint16(hdr[8:10], 0)
				bp.conn.Write(hdr[:])
				bp.conn.Close()
			}
			bp.mu.Unlock()
		}
		log.Printf("[bond] %d: session closed", sess.id)
	}
}

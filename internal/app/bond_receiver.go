package app

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"strings"
	"sync"
	"time"
)

// Bond receiver — runs on the exit node.
// When the exit node receives connections with _bond_<id>_<addr> as target,
// it assembles them into a single TCP connection to the real target.

const bondAddrPrefix = "_bond_"

// IsBondStream returns true if the address is a bond stream.
func IsBondStream(addr string) bool {
	return strings.HasPrefix(addr, bondAddrPrefix)
}

// parseBondAddr extracts bond ID and real target address.
// Format: _bond_<id>_<realaddr>
func parseBondAddr(addr string) (uint32, string, error) {
	s := strings.TrimPrefix(addr, bondAddrPrefix)
	idx := strings.Index(s, "_")
	if idx < 0 {
		return 0, "", fmt.Errorf("bond: invalid address format: %s", addr)
	}
	var id uint32
	if _, err := fmt.Sscanf(s[:idx], "%d", &id); err != nil {
		return 0, "", fmt.Errorf("bond: invalid bond id: %s", s[:idx])
	}
	realAddr := s[idx+1:]
	return id, realAddr, nil
}

// bondReceiver manages the exit side of a bond session.
type bondReceiver struct {
	id       uint32
	realAddr string
	target   net.Conn // TCP connection to real destination

	mu             sync.Mutex
	paths          map[int]net.Conn  // pathIndex → relay stream
	pathTxBytes    map[int]int64     // bytes written per path
	pathWriteNs    map[int]int64     // cumulative write time in ns per path
	pathWriteCount map[int]int64     // write count per path
	pathWeights    map[int]int64     // sender-provided weight hints (0-100)
	writeSeq       uint32            // for return traffic

	// Reorder buffer for incoming (sender→exit) traffic
	reorderMu   sync.Mutex
	reorderBuf  map[uint32][]byte
	reorderNext uint32
	reorderCh   chan struct{}

	closed  bool
	closeCh chan struct{}
}

// Global registry of active bond receivers on this node.
var (
	bondReceivers   sync.Map // bondID → *bondReceiver
)

// handleBondStream is called by nodeOutbound.TCP when it receives a _bond_ address.
// Each path stream calls this; the first one creates the receiver and target connection.
func (a *App) handleBondStream(bondAddr string, pathConn net.Conn) error {
	bondID, realAddr, err := parseBondAddr(bondAddr)
	if err != nil {
		return err
	}

	// Read the setup frame from pathConn
	var hdr [bondFrameHeaderSize]byte
	if _, err := io.ReadFull(pathConn, hdr[:]); err != nil {
		return fmt.Errorf("bond receiver: setup read failed: %w", err)
	}

	frameID := binary.BigEndian.Uint32(hdr[0:4])
	seq := binary.BigEndian.Uint32(hdr[4:8])
	length := binary.BigEndian.Uint16(hdr[8:10])

	if frameID != bondID || seq != bondSetupSeq || length != 2 {
		return fmt.Errorf("bond receiver: invalid setup frame (id=%d seq=%d len=%d)", frameID, seq, length)
	}

	var idxBuf [2]byte
	if _, err := io.ReadFull(pathConn, idxBuf[:]); err != nil {
		return fmt.Errorf("bond receiver: setup read path index failed: %w", err)
	}
	pathIndex := int(binary.BigEndian.Uint16(idxBuf[:]))

	// Get or create receiver
	var recv *bondReceiver
	v, loaded := bondReceivers.LoadOrStore(bondID, &bondReceiver{
		id:          bondID,
		realAddr:    realAddr,
		paths:          make(map[int]net.Conn),
		pathTxBytes:    make(map[int]int64),
		pathWriteNs:    make(map[int]int64),
		pathWriteCount: make(map[int]int64),
		pathWeights:    make(map[int]int64),
		reorderBuf:  make(map[uint32][]byte),
		reorderNext: 1,
		reorderCh:   make(chan struct{}, 1),
		closeCh:     make(chan struct{}),
	})
	recv = v.(*bondReceiver)

	// First path: create target connection
	if !loaded {
		targetConn, err := net.DialTimeout("tcp", realAddr, 10*time.Second)
		if err != nil {
			bondReceivers.Delete(bondID)
			return fmt.Errorf("bond receiver: dial target %s failed: %w", realAddr, err)
		}
		recv.target = targetConn
		log.Printf("[bond-rx] %d: session created, target=%s", bondID, realAddr)

		// Start return traffic writer after paths settle
		go func() {
			// Wait for multiple paths to connect (up to 500ms)
			deadline := time.After(500 * time.Millisecond)
			for {
				recv.mu.Lock()
				n := len(recv.paths)
				recv.mu.Unlock()
				if n >= 2 {
					break
				}
				select {
				case <-deadline:
					log.Printf("[bond-rx] %d: starting with %d paths (timeout)", recv.id, n)
					goto start
				case <-time.After(10 * time.Millisecond):
				}
			}
		start:
			recv.runReturnWriter()
		}()

		// Start deliverer (reorder buffer → target)
		go recv.runDeliverer()

		// Cleanup on close
		go func() {
			<-recv.closeCh
			bondReceivers.Delete(bondID)
		}()
	}

	// Register this path
	recv.mu.Lock()
	recv.paths[pathIndex] = pathConn
	recv.mu.Unlock()
	log.Printf("[bond-rx] %d: path %d connected", bondID, pathIndex)

	// Read data frames from this path
	recv.readPath(pathIndex, pathConn)
	return nil
}

// readPath reads framed chunks from a single inbound path stream.
func (recv *bondReceiver) readPath(pathIndex int, conn net.Conn) {
	var hdr [bondFrameHeaderSize]byte
	for {
		select {
		case <-recv.closeCh:
			return
		default:
		}

		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			if recv.isClosed() {
				return
			}
			debugLog("[bond-rx] %d: path %d read error: %v", recv.id, pathIndex, err)
			recv.mu.Lock()
			delete(recv.paths, pathIndex)
			recv.mu.Unlock()
			recv.skipReorderGaps()
			return
		}

		// bondID := binary.BigEndian.Uint32(hdr[0:4])
		seq := binary.BigEndian.Uint32(hdr[4:8])
		length := binary.BigEndian.Uint16(hdr[8:10])

		if seq == bondTeardownSeq {
			debugLog("[bond-rx] %d: teardown from path %d", recv.id, pathIndex)
			recv.close()
			return
		}

		if seq == bondWeightSeq {
			// Weight update from sender: [pathIdx, weight%] pairs
			if length > 0 {
				wdata := make([]byte, length)
				io.ReadFull(conn, wdata)
				recv.mu.Lock()
				for i := 0; i+1 < len(wdata); i += 2 {
					idx := int(wdata[i])
					w := int64(wdata[i+1]) // weight as percentage
					recv.pathWeights[idx] = w
				}
				recv.mu.Unlock()
				debugLog("[bond-rx] %d: received weight update: %v", recv.id, recv.pathWeights)
			}
			continue
		}

		if length == 0 {
			continue
		}

		data := make([]byte, length)
		if _, err := io.ReadFull(conn, data); err != nil {
			debugLog("[bond-rx] %d: path %d data read error: %v", recv.id, pathIndex, err)
			return
		}

		recv.reorderMu.Lock()
		recv.reorderBuf[seq] = data
		recv.reorderMu.Unlock()

		select {
		case recv.reorderCh <- struct{}{}:
		default:
		}
	}
}

// runDeliverer delivers reordered data to the target TCP connection.
func (recv *bondReceiver) runDeliverer() {
	for {
		if recv.isClosed() {
			return
		}

		delivered := false
		recv.reorderMu.Lock()
		for {
			data, ok := recv.reorderBuf[recv.reorderNext]
			if !ok {
				break
			}
			delete(recv.reorderBuf, recv.reorderNext)
			recv.reorderNext++
			recv.reorderMu.Unlock()

			if _, err := recv.target.Write(data); err != nil {
				debugLog("[bond-rx] %d: target write error: %v", recv.id, err)
				return
			}
			delivered = true
			recv.reorderMu.Lock()
		}
		bufSize := len(recv.reorderBuf)
		recv.reorderMu.Unlock()

		if !delivered {
			timeout := 200 * time.Millisecond
			if bufSize == 0 {
				timeout = 5 * time.Second
			}
			select {
			case <-recv.reorderCh:
			case <-time.After(timeout):
				if bufSize > 0 {
					recv.mu.Lock()
					pathCount := len(recv.paths)
					recv.mu.Unlock()
					if pathCount == 0 {
						return
					}
					recv.reorderMu.Lock()
					recv.reorderNext++
					recv.reorderMu.Unlock()
				}
			case <-recv.closeCh:
				return
			}
		}
	}
}

// runReturnWriter reads from the target and sends back through bond paths.
// Return traffic is sent through all healthy paths with the same framing.
// Does NOT close the receiver — the sender's teardown or path disconnection handles that.
func (recv *bondReceiver) runReturnWriter() {
	buf := make([]byte, bondChunkSize)
	pathBytes := make(map[int]int64)

	for {
		if recv.isClosed() {
			return
		}

		n, err := recv.target.Read(buf)
		if n > 0 {
			recv.writeSeq++
			seq := recv.writeSeq
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			// Send on weighted path
			pathIdx, bp := recv.selectReturnPathIdx(int(seq))
			if bp == nil {
				debugLog("[bond-rx] %d: no path for return seq %d", recv.id, seq)
				return
			}
			pathBytes[pathIdx] += int64(n)
			recv.mu.Lock()
			recv.pathTxBytes[pathIdx] += int64(n)
			recv.mu.Unlock()

			var hdr [bondFrameHeaderSize]byte
			binary.BigEndian.PutUint32(hdr[0:4], recv.id)
			binary.BigEndian.PutUint32(hdr[4:8], seq)
			binary.BigEndian.PutUint16(hdr[8:10], uint16(len(chunk)))

			writeStart := time.Now()
			if _, werr := bp.Write(hdr[:]); werr != nil {
				debugLog("[bond-rx] %d: return write header error: %v", recv.id, werr)
				continue
			}
			if _, werr := bp.Write(chunk); werr != nil {
				debugLog("[bond-rx] %d: return write data error: %v", recv.id, werr)
				continue
			}
			writeNs := time.Since(writeStart).Nanoseconds()
			recv.mu.Lock()
			recv.pathWriteNs[pathIdx] += writeNs
			recv.pathWriteCount[pathIdx]++
			recv.mu.Unlock()
		}
		if err != nil {
			// Log distribution summary
			log.Printf("[bond-rx] %d: return writer done, distribution: %v", recv.id, pathBytes)
			return
		}
	}
}

// selectReturnPathIdx picks the path that is most "behind" its target share.
// Uses sender-provided weight hints if available, otherwise equal distribution.
func (recv *bondReceiver) selectReturnPathIdx(seq int) (int, net.Conn) {
	recv.mu.Lock()
	defer recv.mu.Unlock()

	if len(recv.paths) == 0 {
		return -1, nil
	}

	// Calculate total bytes sent
	var totalTx int64
	for idx := range recv.paths {
		totalTx += recv.pathTxBytes[idx]
	}
	if totalTx == 0 {
		totalTx = 1
	}

	// Check if we have weight hints from sender
	hasWeights := false
	var totalWeight int64
	for idx := range recv.paths {
		if w, ok := recv.pathWeights[idx]; ok && w > 0 {
			hasWeights = true
			totalWeight += w
		}
	}

	var bestIdx int = -1
	var bestGap float64 = -math.MaxFloat64

	for idx := range recv.paths {
		var targetShare float64
		if hasWeights && totalWeight > 0 {
			w := recv.pathWeights[idx]
			if w <= 0 {
				continue // weight=0 means don't use
			}
			targetShare = float64(w) / float64(totalWeight)
		} else {
			targetShare = 1.0 / float64(len(recv.paths))
		}
		actualShare := float64(recv.pathTxBytes[idx]) / float64(totalTx)
		gap := targetShare - actualShare // positive = behind target, needs more
		if gap > bestGap {
			bestGap = gap
			bestIdx = idx
		}
	}

	if bestIdx < 0 {
		for idx := range recv.paths {
			return idx, recv.paths[idx]
		}
	}
	return bestIdx, recv.paths[bestIdx]
}

func (recv *bondReceiver) skipReorderGaps() {
	recv.reorderMu.Lock()
	skipped := 0
	for {
		if _, ok := recv.reorderBuf[recv.reorderNext]; ok {
			break
		}
		hasAhead := false
		for s := recv.reorderNext + 1; s < recv.reorderNext+100; s++ {
			if _, ok := recv.reorderBuf[s]; ok {
				hasAhead = true
				break
			}
		}
		if !hasAhead {
			break
		}
		recv.reorderNext++
		skipped++
	}
	recv.reorderMu.Unlock()
	if skipped > 0 {
		log.Printf("[bond-rx] %d: skipped %d missing seqs after path death", recv.id, skipped)
		select {
		case recv.reorderCh <- struct{}{}:
		default:
		}
	}
}

func (recv *bondReceiver) isClosed() bool {
	select {
	case <-recv.closeCh:
		return true
	default:
		return false
	}
}

func (recv *bondReceiver) close() {
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if recv.closed {
		return
	}
	recv.closed = true
	close(recv.closeCh)

	if recv.target != nil {
		recv.target.Close()
	}
	for _, c := range recv.paths {
		c.Close()
	}
	log.Printf("[bond-rx] %d: session closed", recv.id)
}

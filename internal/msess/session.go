package msess

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sort"
	"sync"
	"time"
)

// Mode selects the subflow scheduler.
type Mode uint8

const (
	ModeSelector Mode = 1 // quality: one active subflow, lossless switch
	ModeBond     Mode = 2 // aggregate: every subflow carries data
)

// Tunables. Variables so tests can shrink the time scales.
var (
	Window       = 8 << 20               // max unacked bytes per direction (= a relay stream's window)
	SegmentSize  = 16 << 10              // DATA payload
	AckEvery     = 64 << 10              // ACK after this many delivered bytes...
	AckDelay     = 20 * time.Millisecond // ...or this long after the first undelivered ACK
	PingBusy     = 50 * time.Millisecond // per-subflow PING while data is in flight
	PingIdle     = 10 * time.Second      // PING on an idle subflow
	DeadMin      = 3 * time.Second       // floor for "no PONG" subflow death
	OrphanBudget = 15 * time.Second      // default grace: no live subflow for this long → abort
	LingerBudget = 90 * time.Second      // closed but not drained for this long → abort
	WriteTimeout = 30 * time.Second      // a subflow write stuck this long is dead

	PacketQueue = 1 << 20           // bytes of datagrams buffered per direction (oldest dropped)
	PacketPing  = time.Second       // per-subflow PING while a packet session is active
	PacketIdle  = 180 * time.Second // packet session without any datagram for this long → closed
)

// ErrAborted is returned after the session was reset by either side.
var ErrAborted = errors.New("msess: session aborted")

type span struct {
	off uint64
	n   int
}

type oooSeg struct {
	off  uint64
	data []byte
}

type pingRec struct {
	at      time.Time
	written uint64
}

type subflow struct {
	s     *session
	id    uint32
	path  string
	conn  net.Conn
	br    *bufio.Reader
	ctrl  [][]byte // queued control frames (guarded by s.mu)
	infl  []span   // DATA spans written here and not yet acked
	txSeq uint32
	rxSeq uint32

	written   uint64 // DATA payload bytes written
	pongMark  uint64 // written-watermark proven delivered by the latest PONG
	rateMark  uint64 // pongMark at the start of the current rate interval
	rateAt    time.Time
	pings     map[uint32]pingRec
	pingSeq   uint32
	lastPing  time.Time
	capPinged uint64 // written-watermark covered by the last cap-hit PING
	srtt      time.Duration
	minRTT    time.Duration
	rate      float64 // bytes/s, decaying max of PONG-proven delivery rate
	added     time.Time
	lastRx    time.Time // last frame received

	dead     bool
	retiring bool
}

func (sf *subflow) inflight() uint64 { return sf.written - sf.pongMark }

// capacity: in-flight bytes this subflow may hold, ≈ two rounds of its
// proven delivery rate. Keeps a subflow's own queue (and thus PING
// latency) short so death detection and migration stay fast. In-flight is
// released only by PONGs (one per PingBusy), so a round must span the PING
// interval: otherwise the rate estimate (≤ cap per interval) and the cap
// hold each other down.
func (sf *subflow) capacity() uint64 {
	rtt := sf.minRTT
	if rtt <= 0 {
		rtt = 50 * time.Millisecond
	}
	c := uint64(2 * sf.rate * (rtt + PingBusy + 50*time.Millisecond).Seconds())
	if c < 128<<10 {
		c = 128 << 10
	}
	if c > uint64(Window) {
		c = uint64(Window)
	}
	return c
}

// deadline: how long the oldest outstanding PING may stay unanswered.
func (sf *subflow) deadline() time.Duration {
	d := 3 * sf.srtt
	if sf.rate > 0 {
		d += time.Duration(float64(sf.inflight()) / sf.rate * float64(time.Second))
	}
	if d < DeadMin {
		d = DeadMin
	}
	return d
}

type session struct {
	mu   sync.Mutex
	cond *sync.Cond

	id       [16]byte
	mode     Mode
	isServer bool
	logf     func(string, ...any)
	onEvent  func(ev string, sf *subflow) // path manager hook (client side)

	// send
	sq       ring // bytes [sBase, sBase+sq.Len())
	sBase    uint64
	sNext    uint64 // first never-sent offset
	retx     []span // ascending, need (re)sending
	wFin     bool
	finOff   uint64
	finOn    *subflow
	finAcked bool
	lastAdv  time.Time // sBase last advanced
	rescueAt uint64    // sBase value last rescued (^0 = none)

	// receive
	rq        ring   // in-order, undelivered; its first byte is offset rRead
	rRead     uint64 // delivered to consumer == our ACK
	ooo       []oooSeg
	oooBytes  int
	peerFin   bool
	peerFinAt uint64
	rDiscard  bool
	ackSent   uint64
	ackFlags  byte
	ackDue    time.Time
	reAck     bool // the carrier of our last ACK may have died with it

	subs    []*subflow
	nextSub uint32
	active  *subflow // selector data carrier

	// packet (datagram) sessions: UDP flows. Unreliable by design — only the
	// carrier survives path changes and the exit keeps one target socket.
	packet     bool
	dgOut      [][]byte
	dgOutBytes int
	dgIn       [][]byte
	dgInBytes  int
	lastDgram  time.Time

	closed    bool // app closed its side
	closedAt  time.Time
	dead      bool
	err       error
	noSubFrom time.Time
	orphan    time.Duration // 0 = OrphanBudget; path manager may shorten
	done      chan struct{}

	rDeadline, wDeadline time.Time
	rTimer, wTimer       *time.Timer
}

func newSession(id [16]byte, mode Mode, isServer bool, logf func(string, ...any)) *session {
	s := &session{id: id, mode: mode, isServer: isServer, logf: logf, done: make(chan struct{}),
		noSubFrom: time.Now(), lastAdv: time.Now(), rescueAt: ^uint64(0)}
	s.cond = sync.NewCond(&s.mu)
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	go s.manage()
	return s
}

func (s *session) sidShort() string { return fmt.Sprintf("%x", s.id[:4]) }

// ---------------------------------------------------------------- app side

func (s *session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.packet {
		return s.writeDgramLocked(p)
	}
	n := 0
	for len(p) > 0 {
		for !s.dead && !s.wFin && s.sq.Len() >= Window {
			if !s.wDeadline.IsZero() && !time.Now().Before(s.wDeadline) {
				return n, os.ErrDeadlineExceeded
			}
			s.cond.Wait()
		}
		if s.dead {
			return n, s.errLocked()
		}
		if s.wFin {
			return n, net.ErrClosed
		}
		if !s.wDeadline.IsZero() && !time.Now().Before(s.wDeadline) {
			return n, os.ErrDeadlineExceeded
		}
		k := Window - s.sq.Len()
		if k > len(p) {
			k = len(p)
		}
		s.sq.push(p[:k], Window)
		p = p[k:]
		n += k
		s.cond.Broadcast()
	}
	return n, nil
}

func (s *session) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.packet {
		return s.readDgramLocked(p)
	}
	for s.rq.Len() == 0 {
		if s.peerFin && s.rRead >= s.peerFinAt {
			return 0, io.EOF
		}
		if s.dead {
			return 0, s.errLocked()
		}
		if s.rDiscard {
			return 0, net.ErrClosed
		}
		if !s.rDeadline.IsZero() && !time.Now().Before(s.rDeadline) {
			return 0, os.ErrDeadlineExceeded
		}
		s.cond.Wait()
	}
	n := s.rq.read(p)
	s.rRead += uint64(n)
	s.maybeAckLocked(false)
	return n, nil
}

func (s *session) writeDgramLocked(p []byte) (int, error) {
	if s.dead || s.closed {
		return 0, s.errLocked()
	}
	if len(p) > 0xFFFF-hdrLen-4 {
		return 0, errors.New("msess: datagram too large")
	}
	s.dgOut = append(s.dgOut, append([]byte(nil), p...))
	s.dgOutBytes += len(p)
	for s.dgOutBytes > PacketQueue && len(s.dgOut) > 1 { // no path yet: keep the newest
		s.dgOutBytes -= len(s.dgOut[0])
		s.dgOut = s.dgOut[1:]
	}
	s.lastDgram = time.Now()
	s.cond.Broadcast()
	return len(p), nil
}

func (s *session) readDgramLocked(p []byte) (int, error) {
	for len(s.dgIn) == 0 {
		if s.dead || s.closed {
			return 0, s.errLocked()
		}
		if !s.rDeadline.IsZero() && !time.Now().Before(s.rDeadline) {
			return 0, os.ErrDeadlineExceeded
		}
		s.cond.Wait()
	}
	d := s.dgIn[0]
	s.dgIn = s.dgIn[1:]
	s.dgInBytes -= len(d)
	return copy(p, d), nil
}

// CloseWrite ends our direction after everything written so far.
func (s *session) CloseWrite() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.wFin {
		s.wFin = true
		s.finOff = s.sBase + uint64(s.sq.Len())
		s.cond.Broadcast()
	}
	return nil
}

// Close ends the app's use: pending writes are still delivered (like
// TCP), further inbound data is discarded, and the session tears down
// once both directions are complete or LingerBudget passes.
func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.packet { // no FIN for datagrams: tell the exit to drop its socket
		s.closed = true
		s.abortLocked(net.ErrClosed, true)
		return nil
	}
	s.closed = true
	s.closedAt = time.Now()
	if !s.wFin {
		s.wFin = true
		s.finOff = s.sBase + uint64(s.sq.Len())
	}
	s.rDiscard = true
	if s.rq.Len() > 0 {
		s.rRead += uint64(s.rq.Len())
		s.rq.reset()
	}
	s.maybeAckLocked(true)
	s.cond.Broadcast()
	return nil
}

func (s *session) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rDeadline = t
	s.rTimer = s.armLocked(s.rTimer, t)
	return nil
}

func (s *session) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wDeadline = t
	s.wTimer = s.armLocked(s.wTimer, t)
	return nil
}

func (s *session) armLocked(tm *time.Timer, t time.Time) *time.Timer {
	if tm != nil {
		tm.Stop()
	}
	if t.IsZero() {
		return nil
	}
	return time.AfterFunc(time.Until(t), func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
}

func (s *session) errLocked() error {
	if s.err != nil {
		return s.err
	}
	return ErrAborted
}

// abort resets the session (both sides) with err.
func (s *session) abort(err error) {
	s.mu.Lock()
	s.abortLocked(err, true)
	s.mu.Unlock()
}

func (s *session) abortLocked(err error, tellPeer bool) {
	if s.dead {
		return
	}
	s.dead = true
	s.err = err
	if tellPeer {
		msg := []byte(err.Error())
		if len(msg) > 512 {
			msg = msg[:512]
		}
		for _, sf := range s.subs {
			sf.queueCtrl(fRst, msg)
		}
	}
	subs := append([]*subflow(nil), s.subs...) // killLocked edits s.subs in place
	s.cond.Broadcast()
	close(s.done)
	// give writers a moment to flush the RST, then drop the carriers
	go func() {
		time.Sleep(200 * time.Millisecond)
		for _, sf := range subs {
			sf.conn.Close()
		}
	}()
}

// ---------------------------------------------------------------- subflows

// attach adds a connected subflow (handshake already done).
func (s *session) attach(conn net.Conn, br *bufio.Reader, id uint32, path string) *subflow {
	sf := &subflow{s: s, id: id, path: path, conn: conn, br: br, pings: map[uint32]pingRec{},
		added: time.Now(), rateAt: time.Now()}
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		conn.Close()
		return nil
	}
	s.subs = append(s.subs, sf)
	if s.mode == ModeSelector && s.active == nil && !s.isServer {
		s.active = sf
	}
	if s.isServer && s.mode == ModeSelector && s.active == nil {
		s.active = sf
	}
	s.noSubFrom = time.Time{}
	sf.queuePingLocked(time.Now())
	s.cond.Broadcast()
	s.mu.Unlock()
	go sf.readLoop()
	go sf.writeLoop()
	return sf
}

func (sf *subflow) queueCtrl(typ byte, payload []byte) {
	b := appendHdr(make([]byte, 0, hdrLen+len(payload)), typ, 0, len(payload))
	sf.ctrl = append(sf.ctrl, append(b, payload...))
}

func (sf *subflow) queuePingLocked(now time.Time) {
	sf.pingSeq++
	var p [12]byte
	binary.BigEndian.PutUint32(p[:4], sf.pingSeq)
	binary.BigEndian.PutUint64(p[4:], uint64(now.UnixNano()))
	sf.queueCtrl(fPing, p[:])
	sf.lastPing = now
	// the watermark is taken when the PING is actually written (writeLoop)
	sf.pings[sf.pingSeq] = pingRec{at: now, written: ^uint64(0)}
	if len(sf.pings) > 64 {
		oldest := uint32(0)
		first := true
		for id := range sf.pings {
			if first || id < oldest {
				oldest, first = id, false
			}
		}
		delete(sf.pings, oldest)
	}
}

// kill drops a subflow and requeues everything it had in flight.
func (s *session) killLocked(sf *subflow, why error) {
	if sf.dead {
		return
	}
	sf.dead = true
	for _, sp := range sf.infl {
		s.requeueLocked(sp)
	}
	sf.infl = nil
	if s.finOn == sf {
		s.finOn = nil
	}
	for i, x := range s.subs {
		if x == sf {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			break
		}
	}
	if s.active == sf {
		s.active = nil
	}
	if len(s.subs) == 0 && s.noSubFrom.IsZero() {
		// The path stopped working when it last delivered anything, not when
		// its death was noticed (up to PingIdle+deadline later on an idle
		// session): the grace counts from then.
		s.noSubFrom = time.Now()
		if !sf.lastRx.IsZero() && sf.lastRx.Before(s.noSubFrom) {
			s.noSubFrom = sf.lastRx
		}
	}
	s.reAck = true
	go sf.conn.Close()
	if !s.dead {
		s.logf("[msess] %s: subflow %d (%s) down: %v", s.sidShort(), sf.id, sf.path, why)
	}
	s.cond.Broadcast()
	if s.onEvent != nil && !s.dead {
		s.onEvent("down", sf)
	}
}

func (s *session) kill(sf *subflow, why error) {
	s.mu.Lock()
	s.killLocked(sf, why)
	s.mu.Unlock()
}

// requeueLocked schedules sp (clipped to unacked) for retransmission.
func (s *session) requeueLocked(sp span) {
	end := sp.off + uint64(sp.n)
	if end <= s.sBase {
		return
	}
	if sp.off < s.sBase {
		sp = span{s.sBase, int(end - s.sBase)}
	}
	i := sort.Search(len(s.retx), func(i int) bool { return s.retx[i].off >= sp.off })
	s.retx = append(s.retx, span{})
	copy(s.retx[i+1:], s.retx[i:])
	s.retx[i] = sp
}

// eligibleLocked: may sf carry DATA now?
func (s *session) eligibleLocked(sf *subflow) bool {
	if sf.dead || sf.retiring {
		return false
	}
	if s.mode == ModeSelector {
		return s.active == sf
	}
	return true
}

// nextDataLocked picks the next DATA span for sf, or ok=false.
func (s *session) nextDataLocked(sf *subflow) (span, bool) {
	if !s.eligibleLocked(sf) || sf.inflight() >= sf.capacity() {
		return span{}, false
	}
	for len(s.retx) > 0 {
		sp := s.retx[0]
		end := sp.off + uint64(sp.n)
		if end <= s.sBase {
			s.retx = s.retx[1:]
			continue
		}
		if sp.off < s.sBase {
			sp = span{s.sBase, int(end - s.sBase)}
		}
		if sp.n > SegmentSize {
			s.retx[0] = span{sp.off + uint64(SegmentSize), sp.n - SegmentSize}
			sp.n = SegmentSize
		} else {
			s.retx = s.retx[1:]
		}
		return sp, true
	}
	end := s.sBase + uint64(s.sq.Len())
	if s.sNext < s.sBase {
		s.sNext = s.sBase
	}
	if s.sNext < end {
		n := end - s.sNext
		if n > uint64(SegmentSize) {
			n = uint64(SegmentSize)
		}
		sp := span{s.sNext, int(n)}
		s.sNext += n
		return sp, true
	}
	return span{}, false
}

// capPingLocked: data waits on this subflow's in-flight cap and no PING has
// been sent since its last DATA. In-flight is released only by PONGs, so
// this makes the release RTT-clocked instead of waiting for the PING timer.
func (sf *subflow) capPingLocked() bool {
	s := sf.s
	if sf.capPinged == sf.written || sf.inflight() < sf.capacity() || !s.eligibleLocked(sf) {
		return false
	}
	if len(s.retx) == 0 && s.sNext >= s.sBase+uint64(s.sq.Len()) {
		return false // nothing waiting
	}
	sf.capPinged = sf.written
	sf.queuePingLocked(time.Now())
	return true
}

func (s *session) needFinLocked(sf *subflow) bool {
	return s.wFin && !s.finAcked && s.finOn == nil && len(s.retx) == 0 &&
		s.sNext >= s.finOff && s.eligibleLocked(sf)
}

func (sf *subflow) writeLoop() {
	s := sf.s
	buf := make([]byte, 0, hdrLen+12+SegmentSize)
	for {
		s.mu.Lock()
		var out []byte
		var pingIDs []uint32
		for {
			if sf.dead {
				s.mu.Unlock()
				return
			}
			if len(sf.ctrl) > 0 {
				for _, c := range sf.ctrl {
					if c[1] == fPing {
						pingIDs = append(pingIDs, binary.BigEndian.Uint32(c[hdrLen:]))
					}
					sf.txSeq++
					binary.BigEndian.PutUint32(c[2:6], sf.txSeq)
					out = append(out, c...)
				}
				sf.ctrl = sf.ctrl[:0]
				break
			}
			if s.dead {
				s.mu.Unlock()
				return
			}
			if s.packet {
				if len(s.dgOut) > 0 && s.eligibleLocked(sf) {
					d := s.dgOut[0]
					s.dgOut = s.dgOut[1:]
					s.dgOutBytes -= len(d)
					sf.txSeq++
					buf = appendHdr(buf[:0], fDgram, sf.txSeq, 4+len(d))
					buf = binary.BigEndian.AppendUint32(buf, dataCRC(0, d))
					out = append(buf, d...)
					break
				}
				s.cond.Wait()
				continue
			}
			if sp, ok := s.nextDataLocked(sf); ok {
				sf.txSeq++
				buf = appendHdr(buf[:0], fData, sf.txSeq, 12+sp.n)
				buf = binary.BigEndian.AppendUint64(buf, sp.off)
				buf = append(buf, 0, 0, 0, 0) // CRC, filled below
				buf = s.sq.appendTo(buf, int(sp.off-s.sBase), sp.n)
				binary.BigEndian.PutUint32(buf[len(buf)-sp.n-4:], dataCRC(sp.off, buf[len(buf)-sp.n:]))
				out = buf
				sf.infl = append(sf.infl, sp)
				sf.written += uint64(sp.n)
				break
			}
			if sf.capPingLocked() {
				continue // blocked on capacity: a PING now releases it one RTT later
			}
			if s.needFinLocked(sf) {
				var p [8]byte
				binary.BigEndian.PutUint64(p[:], s.finOff)
				sf.txSeq++
				out = append(appendHdr(nil, fFin, sf.txSeq, 8), p[:]...)
				s.finOn = sf
				break
			}
			s.cond.Wait()
		}
		for _, id := range pingIDs {
			if r, ok := sf.pings[id]; ok {
				r.written = sf.written
				sf.pings[id] = r
			}
		}
		s.mu.Unlock()
		sf.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
		if _, err := sf.conn.Write(out); err != nil {
			s.kill(sf, fmt.Errorf("write: %w", err))
			return
		}
	}
}

func (sf *subflow) readLoop() {
	s := sf.s
	buf := make([]byte, 12+SegmentSize)
	for {
		f, err := readFrame(sf.br, buf)
		if err != nil {
			s.kill(sf, fmt.Errorf("read: %w", err))
			return
		}
		s.mu.Lock()
		sf.lastRx = time.Now()
		sf.rxSeq++
		if f.fseq != sf.rxSeq {
			// a lossy bridge rebind on an old intermediate hop spliced the
			// stream: this carrier can't be trusted any more
			s.killLocked(sf, fmt.Errorf("frame seq %d, want %d", f.fseq, sf.rxSeq))
			s.mu.Unlock()
			return
		}
		if err := s.handleFrameLocked(sf, f); err != nil {
			s.killLocked(sf, err)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

func (s *session) handleFrameLocked(sf *subflow, f frame) error {
	p := f.payload
	switch f.typ {
	case fData:
		if len(p) < 12 {
			return errBadFrame
		}
		off, data := binary.BigEndian.Uint64(p), p[12:]
		if binary.BigEndian.Uint32(p[8:12]) != dataCRC(off, data) {
			return errors.New("DATA checksum mismatch")
		}
		s.onDataLocked(off, data)
		if s.oooBytes > Window+SegmentSize || s.rq.Len() > Window+SegmentSize {
			return errors.New("receive window exceeded")
		}
	case fAck:
		if len(p) < 9 {
			return errBadFrame
		}
		s.onAckLocked(binary.BigEndian.Uint64(p), p[8])
	case fPing:
		if len(p) < 12 {
			return errBadFrame
		}
		sf.queueCtrl(fPong, append([]byte(nil), p[:12]...))
		s.cond.Broadcast()
	case fPong:
		if len(p) < 12 {
			return errBadFrame
		}
		sf.onPongLocked(binary.BigEndian.Uint32(p[:4]))
	case fFin:
		if len(p) < 8 {
			return errBadFrame
		}
		off := binary.BigEndian.Uint64(p)
		if s.peerFin && off != s.peerFinAt {
			return errors.New("conflicting FIN")
		}
		s.peerFin, s.peerFinAt = true, off
		s.maybeAckLocked(true)
		s.cond.Broadcast()
	case fRst:
		s.abortLocked(fmt.Errorf("%w: peer: %s", ErrAborted, string(p)), false)
	case fDgram:
		if len(p) < 4 {
			return errBadFrame
		}
		d := p[4:]
		if binary.BigEndian.Uint32(p[:4]) != dataCRC(0, d) {
			return errors.New("DGRAM checksum mismatch")
		}
		if !s.packet {
			return errors.New("DGRAM on a stream session")
		}
		s.dgIn = append(s.dgIn, append([]byte(nil), d...))
		s.dgInBytes += len(d)
		for s.dgInBytes > PacketQueue && len(s.dgIn) > 1 {
			s.dgInBytes -= len(s.dgIn[0])
			s.dgIn = s.dgIn[1:]
		}
		s.lastDgram = time.Now()
		s.cond.Broadcast()
	case fRole:
		if len(p) < 1 {
			return errBadFrame
		}
		if s.isServer && s.mode == ModeSelector && p[0] == 1 && s.active != sf {
			if old := s.active; old != nil {
				for _, sp := range old.infl {
					s.requeueLocked(sp)
				}
				old.infl = nil
			}
			s.active = sf
			s.cond.Broadcast()
		}
	}
	return nil
}

func (s *session) onDataLocked(off uint64, data []byte) {
	end := off + uint64(len(data))
	next := s.rRead + uint64(s.rq.Len())
	if end <= next || len(data) == 0 {
		// a retransmission of delivered bytes: the sender missed our ACK
		if s.ackDue.IsZero() {
			s.ackDue = time.Now()
		}
		s.reAck = true
		return
	}
	if off <= next {
		s.appendInOrderLocked(data[next-off:])
		// pull now-contiguous out-of-order segments
		for len(s.ooo) > 0 {
			next = s.rRead + uint64(s.rq.Len())
			sg := s.ooo[0]
			if sg.off > next {
				break
			}
			s.ooo = s.ooo[1:]
			s.oooBytes -= len(sg.data)
			if e := sg.off + uint64(len(sg.data)); e > next {
				s.appendInOrderLocked(sg.data[next-sg.off:])
			}
		}
		s.cond.Broadcast()
		return
	}
	i := sort.Search(len(s.ooo), func(i int) bool { return s.ooo[i].off >= off })
	if i < len(s.ooo) && s.ooo[i].off == off && len(s.ooo[i].data) >= len(data) {
		return // duplicate
	}
	seg := oooSeg{off, append([]byte(nil), data...)}
	if i < len(s.ooo) && s.ooo[i].off == off {
		s.oooBytes += len(data) - len(s.ooo[i].data)
		s.ooo[i] = seg
		return
	}
	s.ooo = append(s.ooo, oooSeg{})
	copy(s.ooo[i+1:], s.ooo[i:])
	s.ooo[i] = seg
	s.oooBytes += len(data)
}

func (s *session) appendInOrderLocked(b []byte) {
	if s.rDiscard {
		s.rRead += uint64(len(b))
		s.maybeAckLocked(false)
		return
	}
	s.rq.push(b, Window+2*SegmentSize)
}

func (s *session) onAckLocked(next uint64, flags byte) {
	if next > s.sBase {
		end := s.sBase + uint64(s.sq.Len())
		if next > end {
			next = end // never ack beyond what we sent
		}
		s.sq.drop(int(next - s.sBase))
		s.sBase = next
		s.lastAdv = time.Now()
		for _, sf := range s.subs {
			k := 0
			for k < len(sf.infl) && sf.infl[k].off+uint64(sf.infl[k].n) <= next {
				k++
			}
			sf.infl = sf.infl[k:]
		}
		s.cond.Broadcast()
	}
	if flags&ackFinDelivered != 0 && s.wFin && next >= s.finOff && !s.finAcked {
		s.finAcked = true
		s.cond.Broadcast()
	}
}

func (sf *subflow) onPongLocked(id uint32) {
	r, ok := sf.pings[id]
	if !ok {
		return
	}
	now := time.Now()
	// drop this and every older ping (streams are FIFO)
	for k := range sf.pings {
		if k <= id {
			delete(sf.pings, k)
		}
	}
	rtt := now.Sub(r.at)
	if sf.srtt == 0 {
		sf.srtt = rtt
	} else {
		sf.srtt = (7*sf.srtt + rtt) / 8
	}
	if sf.minRTT == 0 || rtt < sf.minRTT {
		sf.minRTT = rtt
	}
	if r.written != ^uint64(0) {
		if r.written > sf.pongMark {
			sf.pongMark = r.written
		} else {
			sf.rateMark, sf.rateAt = sf.pongMark, now // nothing new: idle is not slowness
		}
		// PONGs can arrive every RTT (cap-hit PINGs): sample over ≥ 5 ms
		if dt := now.Sub(sf.rateAt).Seconds(); dt >= 0.005 && sf.pongMark > sf.rateMark {
			sample := float64(sf.pongMark-sf.rateMark) / dt
			sf.rate *= math.Pow(0.95, dt/0.05) // decaying max filter: -5% per 50 ms
			if sample > sf.rate {
				sf.rate = sample
			}
			sf.rateMark, sf.rateAt = sf.pongMark, now
		}
	}
	sf.s.cond.Broadcast()
}

// maybeAckLocked queues an ACK when enough was delivered or force.
func (s *session) maybeAckLocked(force bool) {
	var flags byte
	if s.peerFin && s.rRead >= s.peerFinAt {
		flags |= ackFinDelivered
	}
	pending := s.rRead - s.ackSent
	if s.reAck {
		force = true
	}
	if pending == 0 && flags == s.ackFlags && !force {
		return
	}
	if !force && pending < uint64(AckEvery) && flags == s.ackFlags {
		if s.ackDue.IsZero() {
			s.ackDue = time.Now().Add(AckDelay)
		}
		return
	}
	sf := s.ackCarrierLocked()
	if sf == nil {
		return // resent on the next subflow via the manager
	}
	var p [9]byte
	binary.BigEndian.PutUint64(p[:8], s.rRead)
	p[8] = flags
	sf.queueCtrl(fAck, p[:])
	s.ackSent, s.ackFlags, s.ackDue, s.reAck = s.rRead, flags, time.Time{}, false
	s.cond.Broadcast()
}

func (s *session) ackCarrierLocked() *subflow {
	if s.active != nil && !s.active.dead {
		return s.active
	}
	var best *subflow
	for _, sf := range s.subs {
		if sf.dead {
			continue
		}
		if best == nil || (sf.srtt > 0 && (best.srtt == 0 || sf.srtt < best.srtt)) {
			best = sf
		}
	}
	return best
}

// ---------------------------------------------------------------- manager

// manage runs timers: 50 ms while anything is in flight, 500 ms when the
// session is idle (thousands of idle sessions must stay cheap).
func (s *session) manage() {
	t := time.NewTimer(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case now := <-t.C:
			s.mu.Lock()
			s.tickLocked(now)
			busy := s.busyLocked(now)
			s.mu.Unlock()
			if busy {
				t.Reset(50 * time.Millisecond)
			} else {
				t.Reset(500 * time.Millisecond)
			}
		}
	}
}

func (s *session) busyLocked(now time.Time) bool {
	if s.packet && (len(s.dgOut) > 0 || now.Sub(s.lastDgram) < 5*time.Second) {
		return true
	}
	if s.sq.Len() > 0 || len(s.retx) > 0 || len(s.ooo) > 0 || s.rRead != s.ackSent || !s.ackDue.IsZero() ||
		s.reAck || (s.wFin && !s.finAcked) || len(s.subs) == 0 || s.closed {
		return true
	}
	for _, sf := range s.subs {
		if len(sf.pings) > 0 || now.Sub(sf.added) < 5*time.Second {
			return true
		}
	}
	return false
}

func (s *session) tickLocked(now time.Time) {
	if s.dead {
		return
	}
	busy := s.sq.Len() > 0 || len(s.retx) > 0 || (s.wFin && !s.finAcked)
	for _, sf := range append([]*subflow(nil), s.subs...) {
		// death: oldest unanswered PING too old
		var oldest time.Time
		for _, r := range sf.pings {
			if oldest.IsZero() || r.at.Before(oldest) {
				oldest = r.at
			}
		}
		if !oldest.IsZero() && now.Sub(oldest) > sf.deadline() {
			s.killLocked(sf, fmt.Errorf("no PONG for %s", now.Sub(oldest).Round(time.Millisecond)))
			continue
		}
		iv := PingIdle
		switch {
		case s.packet && (now.Sub(s.lastDgram) < 10*time.Second || now.Sub(sf.added) < 5*time.Second):
			iv = PacketPing // a datagram flow is live: keep death detection fast, cheaply
		case !s.packet && (busy || sf.inflight() > 0 || now.Sub(sf.added) < 5*time.Second):
			iv = PingBusy
		}
		if now.Sub(sf.lastPing) >= iv {
			sf.queuePingLocked(now)
			s.cond.Broadcast()
		}
	}
	// ACK timer / an ACK that found no carrier earlier
	if (!s.ackDue.IsZero() && !now.Before(s.ackDue)) || s.rRead != s.ackSent || s.reAck {
		s.maybeAckLocked(true)
	}
	// FIN carrier died before the FIN was confirmed: FIN again
	if s.finOn != nil && s.finOn.dead {
		s.finOn = nil
	}
	if s.mode == ModeBond {
		s.rescueLocked(now)
	}
	// completion
	if s.finAcked && s.peerFin && s.rRead >= s.peerFinAt && s.ackFlags&ackFinDelivered != 0 && s.ackSent >= s.peerFinAt {
		s.finishLocked()
		return
	}
	// The app closed its side entirely and everything it wrote has been
	// delivered: whatever the peer still sends would only be discarded, so
	// reset instead of pulling it across (TCP close() semantics).
	if s.closed && s.finAcked && !(s.peerFin && s.rRead >= s.peerFinAt) {
		s.abortLocked(errors.New("closed by application"), true)
		return
	}
	if s.packet && now.Sub(s.lastDgram) > PacketIdle {
		s.abortLocked(fmt.Errorf("%w: idle", ErrAborted), true)
		return
	}
	if s.closed && now.Sub(s.closedAt) > LingerBudget {
		s.abortLocked(fmt.Errorf("%w: linger budget", ErrAborted), true)
		return
	}
	if len(s.subs) == 0 && !s.noSubFrom.IsZero() && now.Sub(s.noSubFrom) > s.orphanBudgetLocked() {
		s.abortLocked(fmt.Errorf("%w: no path for %s", ErrAborted, s.orphanBudgetLocked()), false)
		return
	}
	if s.onEvent != nil {
		s.onEvent("tick", nil)
	}
}

// orphanBudget can be shortened by the client path manager (another exit
// is available, so waiting the full budget helps nobody).
func (s *session) orphanBudgetLocked() time.Duration {
	if s.orphan > 0 {
		return s.orphan
	}
	return OrphanBudget
}

// Grace bounds: how long a session may outlive its last path.
const (
	MinGrace = 3 * time.Second
	MaxGrace = 300 * time.Second
)

// ClampGrace maps a configured grace (0 = default) into [MinGrace, MaxGrace].
func ClampGrace(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return OrphanBudget
	case d < MinGrace:
		return MinGrace
	case d > MaxGrace:
		return MaxGrace
	}
	return d
}

// rescueLocked duplicates the segment holding back the window on a faster
// subflow once it has waited well past the fastest subflow's RTT.
func (s *session) rescueLocked(now time.Time) {
	if s.sBase == s.rescueAt || s.sq.Len() == 0 {
		return
	}
	var fastest time.Duration
	for _, sf := range s.subs {
		if sf.srtt > 0 && (fastest == 0 || sf.srtt < fastest) {
			fastest = sf.srtt
		}
	}
	wait := 3 * fastest
	if wait < 300*time.Millisecond {
		wait = 300 * time.Millisecond
	}
	if now.Sub(s.lastAdv) < wait {
		return
	}
	for _, sf := range s.subs {
		for _, sp := range sf.infl {
			if sp.off <= s.sBase && s.sBase < sp.off+uint64(sp.n) {
				s.requeueLocked(sp)
				s.rescueAt = s.sBase
				s.cond.Broadcast()
				return
			}
		}
	}
}

func (s *session) finishLocked() {
	if s.dead {
		return
	}
	s.dead = true
	s.err = io.EOF
	subs := append([]*subflow(nil), s.subs...) // killLocked edits s.subs in place
	s.cond.Broadcast()
	close(s.done)
	go func() {
		// let the final ACK drain before closing carriers
		time.Sleep(200 * time.Millisecond)
		for _, sf := range subs {
			sf.conn.Close()
		}
	}()
}

// ---------------------------------------------------------------- stats

// SubflowStat is a point-in-time view of one subflow.
type SubflowStat struct {
	ID       uint32
	Path     string
	SRTT     time.Duration
	RateBps  float64
	Inflight uint64
	Active   bool
}

func (s *session) subflowStats() []SubflowStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SubflowStat, 0, len(s.subs))
	for _, sf := range s.subs {
		out = append(out, SubflowStat{ID: sf.id, Path: sf.path, SRTT: sf.srtt, RateBps: sf.rate,
			Inflight: sf.inflight(), Active: s.active == sf})
	}
	return out
}

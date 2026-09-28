package msess

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ErrUnsupported: no candidate exit speaks msess (legacy exit) — callers
// fall back to the pre-1.3.10 dispatch.
var ErrUnsupported = errors.New("msess: exit does not support sessions")

// ErrBusy: every candidate exit is at its session limit — callers fall back
// to a plain relay stream for this connection.
var ErrBusy = errors.New("msess: exit at session capacity")

// DialError: the exit answered but could not reach the target.
type DialError struct{ Msg string }

func (e *DialError) Error() string { return "msess: exit dial failed: " + e.Msg }

// Selector switching policy (rendr-derived).
var (
	SwitchBand     = 0.25                 // candidate must beat active by this fraction...
	SwitchFloor    = 5 * time.Millisecond // ...and by this much
	SwitchDwell    = 3 * time.Second      // sustained this long
	SwitchCooldown = 15 * time.Second     // between quality switches
	RetireGrace    = 2 * time.Second      // old carrier kept this long after a switch
	JoinStagger    = time.Second          // failover: start the next candidate if none attached yet
	MaxSubflows    = 6
)

// DialConfig describes one user connection.
type DialConfig struct {
	Mode   Mode
	Target string
	Paths  []Path // candidates; may end at different exits (selector)
	Prober *Prober
	// Grace: how long the connection survives with no working path to its
	// exit before it is closed (0 = OrphanBudget). The exit uses the same
	// value (sent in the handshake).
	Grace time.Duration
	// NoProbeWait: dial the first ranked path at once instead of waiting
	// (≤ 800 ms) for RTT samples — for a pinned route (direct mode), whose
	// candidates are only the carriers of one path.
	NoProbeWait bool
	Logf        func(string, ...any)
}

// Conn is the app-facing end of a session (net.Conn + CloseWrite).
type Conn struct {
	s  *session
	pm *pathMgr
}

func (c *Conn) Read(p []byte) (int, error)         { return c.s.Read(p) }
func (c *Conn) Write(p []byte) (int, error)        { return c.s.Write(p) }
func (c *Conn) Close() error                       { return c.s.Close() }
func (c *Conn) CloseWrite() error                  { return c.s.CloseWrite() }
func (c *Conn) LocalAddr() net.Addr                { return sessAddr("msess-local") }
func (c *Conn) RemoteAddr() net.Addr               { return sessAddr(c.pm.cfg.Target) }
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.s.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.s.SetWriteDeadline(t) }
func (c *Conn) SetDeadline(t time.Time) error {
	c.s.SetReadDeadline(t)
	return c.s.SetWriteDeadline(t)
}

// Subflows reports the live carriers (diagnostics / tests).
func (c *Conn) Subflows() []SubflowStat { return c.s.subflowStats() }

// Exit is the exit node this session is bound to.
func (c *Conn) Exit() string { return c.pm.exit }

// Done is closed when the session has fully ended.
func (c *Conn) Done() <-chan struct{} { return c.s.done }

type sessAddr string

func (a sessAddr) Network() string { return "msess" }
func (a sessAddr) String() string  { return string(a) }

// Dial opens a stream (TCP) session to the best exit among cfg.Paths.
func Dial(ctx context.Context, cfg DialConfig) (*Conn, error) { return dial(ctx, cfg, false) }

// DialPacket opens a datagram (UDP) session: each Write is one datagram to
// cfg.Target, each Read one datagram back. The exit keeps a single UDP
// socket for the flow while the carrying paths change underneath.
func DialPacket(ctx context.Context, cfg DialConfig) (*Conn, error) { return dial(ctx, cfg, true) }

func dial(ctx context.Context, cfg DialConfig, packet bool) (*Conn, error) {
	if len(cfg.Paths) == 0 {
		return nil, errors.New("msess: no paths")
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Prober == nil {
		cfg.Prober = NewProber(cfg.Logf)
	}
	pr := cfg.Prober
	pr.Track(cfg.Paths)
	ranked := pr.Rank(cfg.Paths)
	if _, ok := pr.Score(ranked[0].Name); !ok && !cfg.NoProbeWait {
		pr.ProbeNow(ctx, cfg.Paths, 800*time.Millisecond)
		ranked = pr.Rank(cfg.Paths)
	}
	var sid [16]byte
	rand.Read(sid[:])

	// exit groups in order of their best path
	var exits []string
	seen := map[string]bool{}
	for _, p := range ranked {
		if !seen[p.Exit] {
			seen[p.Exit] = true
			exits = append(exits, p.Exit)
		}
	}
	unsupported, busy, tried := 0, 0, 0
	var lastErr, unsupErr error
	for _, exit := range exits {
		var group []Path
		for _, p := range ranked {
			if p.Exit == exit {
				group = append(group, p)
			}
		}
		for _, p := range group {
			tried++
			grace := ClampGrace(cfg.Grace)
			kind := byte(kindOpen)
			if packet {
				kind = kindOpenPacket
			}
			conn, br, err := handshake(ctx, p, hello{kind: kind, sid: sid, sub: 1, mode: cfg.Mode,
				grace: uint16(grace / time.Second), target: cfg.Target})
			if err != nil {
				var de *DialError
				if errors.As(err, &de) {
					return nil, err // the exit can't reach the target; other paths won't help
				}
				if errors.Is(err, ErrUnsupported) {
					unsupported++
					unsupErr = err
				}
				if errors.Is(err, ErrBusy) {
					busy++
				}
				pr.Fail(p.Name)
				lastErr = err
				continue
			}
			s := newSession(sid, cfg.Mode, false, cfg.Logf)
			s.mu.Lock()
			s.orphan = grace
			s.packet = packet
			s.lastDgram = time.Now()
			s.mu.Unlock()
			pm := &pathMgr{s: s, cfg: cfg, exit: exit, paths: group, backoff: map[string]time.Time{},
				fails: map[string]int{}, dialing: map[string]bool{}, lastSwitch: time.Now()}
			s.nextSub = 1
			s.onEvent = pm.onEvent
			sf := s.attach(conn, br, 1, p.Name)
			if sf == nil {
				return nil, ErrAborted
			}
			pm.mu.Lock()
			pm.started = true
			pm.mu.Unlock()
			return &Conn{s: s, pm: pm}, nil
		}
	}
	if tried > 0 && unsupported == tried {
		return nil, unsupErr // wraps ErrUnsupported and says why
	}
	if tried > 0 && unsupported+busy == tried {
		return nil, ErrBusy
	}
	return nil, fmt.Errorf("msess: no usable path: %w", lastErr)
}

// handshake dials p and performs HELLO / HELLO_ACK.
func handshake(ctx context.Context, p Path, h hello) (net.Conn, *bufio.Reader, error) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	c, err := p.open(dctx, h)
	cancel()
	if err != nil {
		return nil, nil, err
	}
	c.SetDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReaderSize(c, 32<<10)
	a, err := readHelloAck(br)
	if err != nil {
		c.Close()
		if (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) && !p.Capable {
			// the far end closed on our HELLO: a pre-1.3.10 exit treating
			// the subflow address as a (failed) TCP target
			return nil, nil, fmt.Errorf("%w (%s)", ErrUnsupported, p.Name)
		}
		return nil, nil, err
	}
	switch a.status {
	case stOK:
	case stDialFailed:
		c.Close()
		return nil, nil, &DialError{Msg: a.msg}
	case stUnknown:
		c.Close()
		return nil, nil, fmt.Errorf("%w (%s)", errUnknownSession, a.msg)
	case stRefused:
		c.Close()
		if a.msg == refusedCapacity {
			return nil, nil, fmt.Errorf("%w (%s)", ErrBusy, p.Name)
		}
		return nil, nil, fmt.Errorf("msess: %s refused: %s", p.Name, a.msg)
	default:
		c.Close()
		return nil, nil, fmt.Errorf("msess: %s refused (%d): %s", p.Name, a.status, a.msg)
	}
	c.SetDeadline(time.Time{})
	return c, br, nil
}

// pathMgr keeps a client session supplied with carriers.
type pathMgr struct {
	mu      sync.Mutex // guards the fields below (never held with s.mu... see onEvent)
	s       *session
	cfg     DialConfig
	exit    string
	paths   []Path
	started bool

	backoff    map[string]time.Time
	fails      map[string]int
	dialing    map[string]bool
	lastEval   time.Time
	lastJoin   time.Time
	lastSwitch time.Time
	betterPath string
	betterFrom time.Time
}

// onEvent runs under s.mu: only bookkeeping and goroutine spawns here.
func (pm *pathMgr) onEvent(ev string, sf *subflow) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if !pm.started {
		return
	}
	now := time.Now()
	switch ev {
	case "down":
		if sf.retiring {
			return
		}
		pm.fails[sf.path]++
		d := time.Second << min(pm.fails[sf.path], 5)
		pm.backoff[sf.path] = now.Add(d)
		pm.cfg.Prober.Fail(sf.path)
		pm.lastEval = time.Time{} // react on the next tick
	case "tick":
		if now.Sub(pm.lastEval) < 200*time.Millisecond {
			return
		}
		pm.lastEval = now
		pm.evalLocked(now)
	}
}

func (pm *pathMgr) livePathsLocked() map[string]*subflow {
	m := map[string]*subflow{}
	for _, sf := range pm.s.subs {
		if !sf.dead && !sf.retiring {
			m[sf.path] = sf
		}
	}
	return m
}

func (pm *pathMgr) evalLocked(now time.Time) {
	s := pm.s
	if s.dead || s.closed && s.finAcked {
		return
	}
	live := pm.livePathsLocked()
	ranked := pm.cfg.Prober.Rank(pm.paths)
	usable := func(p Path) bool {
		return live[p.Name] == nil && !pm.dialing[p.Name] && !now.Before(pm.backoff[p.Name])
	}
	if s.mode == ModeBond {
		n := len(live) + len(pm.dialing)
		for _, p := range ranked {
			if n >= MaxSubflows {
				break
			}
			if usable(p) && !pm.cfg.Prober.Failed(p.Name) {
				pm.startJoinLocked(p, joinSpare)
				n++
			}
		}
		// nothing alive and every path marked failed: still keep trying
		if len(live) == 0 && len(pm.dialing) == 0 {
			for _, p := range ranked {
				if usable(p) {
					pm.startJoinLocked(p, joinSpare)
					break
				}
			}
		}
		return
	}
	// selector
	if s.active == nil || s.active.dead {
		for _, sf := range s.subs {
			if !sf.dead && !sf.retiring {
				s.setActiveLocked(sf)
				return
			}
		}
		// Race rejoins: a candidate can be stalled too (a join into it then
		// hangs for the whole handshake timeout), so if nothing attached
		// within JoinStagger, start the next one as well; the first to
		// attach becomes active, later ones retire.
		if len(pm.dialing) > 0 && now.Sub(pm.lastJoin) < JoinStagger {
			return
		}
		for _, p := range ranked {
			if usable(p) {
				pm.startJoinLocked(p, joinFailover)
				pm.lastJoin = now
				return
			}
		}
		if len(pm.dialing) == 0 {
			// all in backoff: take the one whose backoff ends first
			var pick *Path
			for i := range ranked {
				p := ranked[i]
				if live[p.Name] == nil && !pm.dialing[p.Name] &&
					(pick == nil || pm.backoff[p.Name].Before(pm.backoff[pick.Name])) {
					pick = &p
				}
			}
			if pick != nil && pm.backoff[pick.Name].Sub(now) < 500*time.Millisecond {
				pm.startJoinLocked(*pick, joinFailover)
				pm.lastJoin = now
			}
		}
		return
	}
	// quality: move to a clearly better path after dwell, respecting cooldown
	cur := s.active.path
	curRTT, ok := pm.cfg.Prober.Score(cur)
	if !ok {
		curRTT = s.active.srtt
	}
	if curRTT == 0 || len(pm.dialing) > 0 {
		return
	}
	var best *Path
	var bestRTT time.Duration
	for i := range ranked {
		p := ranked[i]
		if p.Name == cur {
			continue
		}
		if rtt, ok := pm.cfg.Prober.Score(p.Name); ok && !now.Before(pm.backoff[p.Name]) {
			best, bestRTT = &p, rtt
			break
		}
	}
	if best == nil || float64(bestRTT) >= float64(curRTT)*(1-SwitchBand) || curRTT-bestRTT < SwitchFloor {
		pm.betterPath = ""
		return
	}
	if pm.betterPath != best.Name {
		pm.betterPath, pm.betterFrom = best.Name, now
		return
	}
	if now.Sub(pm.betterFrom) >= SwitchDwell && now.Sub(pm.lastSwitch) >= SwitchCooldown {
		pm.cfg.Logf("[msess] %s: quality switch %s (%s) → %s (%s)", s.sidShort(), cur,
			curRTT.Round(time.Millisecond), best.Name, bestRTT.Round(time.Millisecond))
		pm.lastSwitch = now
		pm.betterPath = ""
		pm.startJoinLocked(*best, joinSwitch)
	}
}

// Join kinds.
const (
	joinSpare    = iota // bond: one more carrier
	joinFailover        // selector: becomes active unless a racing join already did
	joinSwitch          // selector quality switch: becomes active
)

// startJoinLocked dials a join subflow on p in the background.
func (pm *pathMgr) startJoinLocked(p Path, kind int) {
	pm.dialing[p.Name] = true
	s := pm.s
	s.nextSub++
	id := s.nextSub
	rx := s.rRead
	sid := s.id
	mode := s.mode
	go func() {
		conn, br, err := handshake(context.Background(), p, hello{kind: kindJoin, sid: sid, sub: id, mode: mode, rxNext: rx})
		pm.mu.Lock()
		delete(pm.dialing, p.Name)
		if err != nil {
			pm.fails[p.Name]++
			pm.backoff[p.Name] = time.Now().Add(time.Second << min(pm.fails[p.Name], 5))
			pm.mu.Unlock()
			pm.cfg.Prober.Fail(p.Name)
			pm.cfg.Logf("[msess] %s: join %s failed: %v", s.sidShort(), p.Name, err)
			if errors.Is(err, errUnknownSession) {
				s.abort(err)
			}
			return
		}
		pm.fails[p.Name] = 0
		pm.mu.Unlock()
		sf := s.attach(conn, br, id, p.Name)
		if sf == nil {
			return
		}
		s.mu.Lock()
		if s.mode == ModeSelector {
			// attach() already made sf active when nothing was: that is a win too
			if kind == joinSwitch || s.active == nil || s.active.dead || s.active == sf {
				s.setActiveLocked(sf)
			} else {
				sf.retiring = true // lost the failover race: no penalty for its path
				go s.kill(sf, errors.New("lost join race"))
			}
		}
		s.mu.Unlock()
	}()
}

var errUnknownSession = errors.New("msess: exit lost the session")

// setActiveLocked makes sf the selector data carrier and retires the old
// one: its unacked data is resent on sf (the receiver drops duplicates).
func (s *session) setActiveLocked(sf *subflow) {
	old := s.active
	s.active = sf
	sf.queueCtrl(fRole, []byte{1})
	if old != nil && old != sf && !old.dead {
		for _, sp := range old.infl {
			s.requeueLocked(sp)
		}
		old.infl = nil
		old.retiring = true
		go func() {
			time.Sleep(RetireGrace)
			s.kill(old, errors.New("retired"))
		}()
	}
	s.cond.Broadcast()
}

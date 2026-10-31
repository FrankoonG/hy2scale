package msess

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sort"
	"sync"
	"time"
)

// Path is one route from the ingress to an exit.
type Path struct {
	Name string // unique per route/carrier, e.g. "x@0", "a/x"
	Exit string // terminal node identity
	// Dial opens a raw StreamMsess relay stream along this route.
	Dial func(ctx context.Context) (net.Conn, error)
	// Open, when set, opens a subflow whose relay request already carries
	// the HELLO (relay.MsessAddr): the exit reads it without the round trip
	// a separate write after the stream is established costs.
	Open func(ctx context.Context, hello []byte) (net.Conn, error)
	// Capable: the exit is known (advertised version) to speak msess, so a
	// stream closed during the handshake is a path failure — not a sign of
	// a legacy exit (it happens while a hop or the exit reconnects).
	Capable bool
}

// open dials p and sends h on it.
func (p Path) open(ctx context.Context, h hello) (net.Conn, error) {
	b, err := encodeHello(h)
	if err != nil {
		return nil, err
	}
	if p.Open != nil {
		return p.Open(ctx, b)
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.Write(b); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Prober keeps a live RTT estimate per path using one long-lived probe
// stream each. Shared by every session of an ingress node.
type Prober struct {
	mu     sync.Mutex
	paths  map[string]*probeState
	logf   func(string, ...any)
	closed bool
}

type probeState struct {
	p       Path
	srtt    time.Duration
	sampled time.Time // last good sample
	fails   int
	lastUse time.Time
	running bool
}

var (
	ProbeEvery = 2 * time.Second
	ProbeFresh = 10 * time.Second // older samples don't count
	ProbeIdle  = 5 * time.Minute  // stop probing paths nobody used for this long
)

func NewProber(logf func(string, ...any)) *Prober {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Prober{paths: map[string]*probeState{}, logf: logf}
}

// Track registers paths (refreshing their dialers) and starts probing.
func (pr *Prober) Track(paths []Path) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.closed {
		return
	}
	now := time.Now()
	for _, p := range paths {
		st := pr.paths[p.Name]
		if st == nil {
			st = &probeState{}
			pr.paths[p.Name] = st
		}
		st.p = p
		st.lastUse = now
		if !st.running {
			st.running = true
			go pr.loop(st)
		}
	}
}

// PathScore is one probed path (diagnostics).
type PathScore struct {
	Name   string  `json:"name"`
	SRTTMs float64 `json:"srtt_ms"`
	Fails  int     `json:"fails"`
	AgeS   float64 `json:"age_s"` // since the last good sample
}

// Snapshot lists every tracked path (diagnostics).
func (pr *Prober) Snapshot() []PathScore {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	out := make([]PathScore, 0, len(pr.paths))
	for name, st := range pr.paths {
		ps := PathScore{Name: name, SRTTMs: float64(st.srtt.Microseconds()) / 1000, Fails: st.fails, AgeS: -1}
		if !st.sampled.IsZero() {
			ps.AgeS = time.Since(st.sampled).Round(100 * time.Millisecond).Seconds()
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Score returns a fresh RTT estimate for a path.
func (pr *Prober) Score(name string) (time.Duration, bool) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	st := pr.paths[name]
	if st == nil || st.fails > 0 || st.srtt == 0 || time.Since(st.sampled) > ProbeFresh {
		return 0, false
	}
	return st.srtt, true
}

// Failed reports whether the path's latest probe attempts failed.
func (pr *Prober) Failed(name string) bool {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	st := pr.paths[name]
	return st != nil && st.fails > 0
}

// Fail records an out-of-band failure (a session subflow died on it).
func (pr *Prober) Fail(name string) {
	pr.mu.Lock()
	if st := pr.paths[name]; st != nil {
		st.fails++
		st.srtt = 0
	}
	pr.mu.Unlock()
}

// Rank orders paths: fresh scores ascending, then unknown (input order),
// then failed.
func (pr *Prober) Rank(paths []Path) []Path {
	type r struct {
		p     Path
		class int
		rtt   time.Duration
		idx   int
	}
	rs := make([]r, len(paths))
	for i, p := range paths {
		rs[i] = r{p: p, class: 1, idx: i}
		if rtt, ok := pr.Score(p.Name); ok {
			rs[i].class, rs[i].rtt = 0, rtt
		} else if pr.Failed(p.Name) {
			rs[i].class = 2
		}
	}
	sort.SliceStable(rs, func(a, b int) bool {
		if rs[a].class != rs[b].class {
			return rs[a].class < rs[b].class
		}
		if rs[a].class == 0 {
			return rs[a].rtt < rs[b].rtt
		}
		return rs[a].idx < rs[b].idx
	})
	out := make([]Path, len(rs))
	for i := range rs {
		out[i] = rs[i].p
	}
	return out
}

// ProbeNow measures every path without a fresh score once, concurrently,
// waiting at most wait (returns early when all answered).
func (pr *Prober) ProbeNow(ctx context.Context, paths []Path, wait time.Duration) {
	var wg sync.WaitGroup
	for _, p := range paths {
		if _, ok := pr.Score(p.Name); ok {
			continue
		}
		wg.Add(1)
		go func(p Path) {
			defer wg.Done()
			c, _, rtt, err := probeHandshake(ctx, p, wait)
			if err != nil {
				pr.record(p.Name, 0, err)
				return
			}
			c.Close()
			if rtt < DeadMin {
				pr.record(p.Name, rtt, nil)
			} else {
				pr.markAlive(p.Name) // a stalled stream open is no RTT sample
			}
		}(p)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
	case <-ctx.Done():
	}
}

// markAlive clears a path's failure without an RTT sample.
func (pr *Prober) markAlive(name string) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if st := pr.paths[name]; st != nil {
		st.fails = 0
	}
}

func (pr *Prober) record(name string, rtt time.Duration, err error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	st := pr.paths[name]
	if st == nil {
		return
	}
	if err != nil {
		st.fails++
		st.srtt = 0
		return
	}
	st.fails = 0
	if st.srtt == 0 || time.Since(st.sampled) > ProbeFresh {
		st.srtt = rtt
	} else {
		st.srtt = (7*st.srtt + 3*rtt) / 10
	}
	st.sampled = time.Now()
}

func probeHandshake(ctx context.Context, p Path, timeout time.Duration) (net.Conn, *bufio.Reader, time.Duration, error) {
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t0 := time.Now()
	c, err := p.open(dctx, hello{kind: kindProbe})
	if err != nil {
		return nil, nil, 0, err
	}
	c.SetDeadline(time.Now().Add(timeout))
	br := bufio.NewReaderSize(c, 256)
	a, err := readHelloAck(br)
	if err != nil {
		c.Close()
		return nil, nil, 0, err
	}
	if a.status != stOK {
		c.Close()
		return nil, nil, 0, errors.New("probe refused")
	}
	c.SetDeadline(time.Time{})
	return c, br, time.Since(t0), nil
}

func (pr *Prober) loop(st *probeState) {
	backoff := time.Second
	for {
		pr.mu.Lock()
		p := st.p
		stop := pr.closed || time.Since(st.lastUse) > ProbeIdle
		if stop {
			st.running = false
			delete(pr.paths, p.Name)
		}
		pr.mu.Unlock()
		if stop {
			return
		}
		c, br, _, err := probeHandshake(context.Background(), p, 10*time.Second)
		if err != nil {
			pr.record(p.Name, 0, err)
			// short cap: a path that is back must not stay "failed" for long
			time.Sleep(backoff)
			if backoff < 4*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		// The handshake time includes opening the relay stream, which on a
		// stalled QUIC connection can take many seconds: the RTT comes from
		// PING/PONG only (the first one is sent at once).
		pr.markAlive(p.Name)
		pr.pingLoop(st, p.Name, c, br)
		c.Close()
	}
}

// pingLoop PINGs over an established probe stream until it fails or the
// path goes idle.
func (pr *Prober) pingLoop(st *probeState, name string, c net.Conn, br *bufio.Reader) {
	buf := make([]byte, 64)
	var seq uint32
	for first := true; ; first = false {
		if !first {
			time.Sleep(ProbeEvery)
		}
		pr.mu.Lock()
		idle := pr.closed || time.Since(st.lastUse) > ProbeIdle
		pr.mu.Unlock()
		if idle {
			return
		}
		seq++
		var p [12]byte
		binary.BigEndian.PutUint32(p[:4], seq)
		t0 := time.Now()
		binary.BigEndian.PutUint64(p[4:], uint64(t0.UnixNano()))
		c.SetDeadline(t0.Add(DeadMin))
		if _, err := c.Write(append(appendHdr(nil, fPing, seq, 12), p[:]...)); err != nil {
			pr.record(name, 0, err)
			return
		}
		f, err := readFrame(br, buf)
		if err != nil || f.typ != fPong {
			if err == nil {
				err = errBadFrame
			}
			pr.record(name, 0, err)
			return
		}
		pr.record(name, time.Since(t0), nil)
	}
}

func (pr *Prober) Close() {
	pr.mu.Lock()
	pr.closed = true
	pr.mu.Unlock()
}

// Count returns the number of tracked paths (diagnostics).
func (pr *Prober) Count() int {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	return len(pr.paths)
}

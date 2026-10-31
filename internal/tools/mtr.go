package tools

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MTROptions of one mtr run: Count rounds, each probing every TTL up to the
// destination (MaxHops until it answers) with ICMP echo over a raw socket.
type MTROptions struct {
	Host     string
	Count    int
	Interval time.Duration
	MaxHops  int
	Family   string
	DNS      bool // reverse-resolve hop addresses
}

// MTRHandler serves GET /api/tools/mtr.
func MTRHandler(w http.ResponseWriter, r *http.Request) {
	o, err := mtrOptions(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	emit := ndjson(w)
	if err := MTR(r.Context(), o, emit); err != nil && r.Context().Err() == nil {
		emit(errEvent(err))
	}
}

func mtrOptions(r *http.Request) (MTROptions, error) {
	q := r.URL.Query()
	var o MTROptions
	var err error
	if o.Host, err = checkHost(q.Get("host")); err != nil {
		return o, err
	}
	if o.Count, err = intParam(q, "count", 10, 1, 1000); err != nil {
		return o, err
	}
	iv, err := intParam(q, "interval_ms", 1000, 200, 60000)
	if err != nil {
		return o, err
	}
	o.Interval = time.Duration(iv) * time.Millisecond
	if o.MaxHops, err = intParam(q, "max_hops", 30, 1, 64); err != nil {
		return o, err
	}
	if o.Family, err = checkFamily(q.Get("family")); err != nil {
		return o, err
	}
	o.DNS = boolParam(q, "dns")
	return o, nil
}

// hopStat: one TTL's results so far.
type hopStat struct {
	ip              string
	sent, recv      int
	last, best, max float64
	mean, m2        float64 // Welford
}

func (h *hopStat) add(rtt float64) {
	h.recv++
	h.last = rtt
	if h.recv == 1 || rtt < h.best {
		h.best = rtt
	}
	if rtt > h.max {
		h.max = rtt
	}
	d := rtt - h.mean
	h.mean += d / float64(h.recv)
	h.m2 += d * (rtt - h.mean)
}

func (h *hopStat) event(ttl int, name string) Event {
	e := Event{"ttl": ttl, "ip": h.ip, "sent": h.sent, "recv": h.recv, "loss": 0.0,
		"last_ms": round3(h.last), "avg_ms": round3(h.mean), "best_ms": round3(h.best),
		"worst_ms": round3(h.max), "stdev_ms": 0.0}
	if h.sent > 0 {
		e["loss"] = round3(float64(h.sent-h.recv) * 100 / float64(h.sent))
	}
	if h.recv > 1 {
		e["stdev_ms"] = round3(math.Sqrt(h.m2 / float64(h.recv)))
	}
	if name != "" {
		e["name"] = name
	}
	return e
}

type mtrProbe struct {
	ttl  int
	sent time.Time
	done bool
}

// MTR runs o, streaming start, one round line per round and done. Hops are
// listed up to the destination once it answered, else up to one past the
// last hop that did.
func MTR(ctx context.Context, o MTROptions, emit Emit) error {
	dst, err := resolve(ctx, o.Host, o.Family)
	if err != nil {
		return err
	}
	s, err := openICMP(dst.IP.To4() == nil, true)
	if err != nil {
		return fmt.Errorf("raw ICMP socket: %s", sockReason(err))
	}
	defer s.Close()
	in := make(chan icmpIn, 256)
	go func() {
		buf := make([]byte, 65536)
		for {
			p, err := s.recv(buf)
			if err != nil {
				close(in)
				return
			}
			select {
			case in <- p:
			default: // a burst nobody waits for
			}
		}
	}()
	if err := emit(Event{"type": "start", "host": o.Host, "ip": dst.String()}); err != nil {
		return nil
	}
	hops := make([]hopStat, o.MaxHops+1) // by TTL
	destTTL, lastSeen := 0, 0
	payload := make([]byte, 36)
	wait := max(o.Interval, time.Second)
	for round := 1; round <= o.Count; round++ {
		start := time.Now()
		limit := o.MaxHops
		if destTTL > 0 {
			limit = destTTL
		}
		probes := map[int]*mtrProbe{}
		for ttl := 1; ttl <= limit; ttl++ {
			seq := (round%1024)<<6 | (ttl - 1)
			p := &mtrProbe{ttl: ttl, sent: time.Now()}
			if err := s.send(dst, seq, payload, ttl); err != nil && !isTransient(err) {
				return fmt.Errorf("send: %s", sockReason(err))
			}
			probes[seq] = p
		}
		timer := time.NewTimer(wait)
	collect:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
				break collect
			case p, ok := <-in:
				if !ok {
					timer.Stop()
					return fmt.Errorf("raw ICMP socket closed")
				}
				pr := probes[p.seq]
				if pr == nil || pr.done {
					continue
				}
				if p.echo && !p.from.Equal(dst.IP) || !p.echo && !p.dst.Equal(dst.IP) {
					continue
				}
				pr.done = true
				h := &hops[pr.ttl]
				h.ip = p.from.String()
				h.add(ms(p.at.Sub(pr.sent)))
				lastSeen = max(lastSeen, pr.ttl)
				if p.echo || p.from.Equal(dst.IP) {
					if destTTL == 0 || pr.ttl < destTTL {
						destTTL = pr.ttl
					}
				}
				if destTTL > 0 && allDone(probes, destTTL) {
					timer.Stop()
					break collect
				}
			}
		}
		show := min(o.MaxHops, lastSeen+1)
		if destTTL > 0 {
			show = destTTL
		}
		for _, p := range probes {
			if p.ttl <= show {
				hops[p.ttl].sent++
			}
		}
		// a probe answered past the destination (first round) is not a hop
		for ttl := show + 1; ttl <= o.MaxHops; ttl++ {
			hops[ttl] = hopStat{}
		}
		list := make([]Event, 0, show)
		for ttl := 1; ttl <= show; ttl++ {
			name := ""
			if o.DNS && hops[ttl].ip != "" {
				name = rdns.name(hops[ttl].ip)
			}
			list = append(list, hops[ttl].event(ttl, name))
		}
		if emit(Event{"type": "round", "round": round, "hops": list}) != nil {
			return nil
		}
		if round < o.Count && !sleepCtx(ctx, o.Interval-time.Since(start)) {
			return nil
		}
	}
	emit(Event{"type": "done"})
	return nil
}

func allDone(probes map[int]*mtrProbe, upto int) bool {
	for _, p := range probes {
		if p.ttl <= upto && !p.done {
			return false
		}
	}
	return true
}

// rdns caches reverse lookups; a name not known yet is looked up in the
// background and shows from a later round on.
var rdns = &rdnsCache{m: map[string]rdnsEntry{}}

type rdnsCache struct {
	mu sync.Mutex
	m  map[string]rdnsEntry
}

type rdnsEntry struct {
	name    string
	expires time.Time
	pending bool
}

func (c *rdnsCache) name(ip string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[ip]
	if ok && (e.pending || time.Now().Before(e.expires)) {
		return e.name
	}
	if len(c.m) > 4096 {
		c.m = map[string]rdnsEntry{}
	}
	c.m[ip] = rdnsEntry{name: e.name, pending: true}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		names, err := net.DefaultResolver.LookupAddr(ctx, ip)
		name := ""
		if err == nil && len(names) > 0 {
			name = strings.TrimSuffix(names[0], ".")
		}
		c.mu.Lock()
		c.m[ip] = rdnsEntry{name: name, expires: time.Now().Add(10 * time.Minute)}
		c.mu.Unlock()
	}()
	return e.name
}

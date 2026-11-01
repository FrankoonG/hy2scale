package app

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Per-user rate limits (UserConfig.RateUp / RateDown, bytes per second,
// 0 = unlimited). One token bucket per user and direction, shared by all of
// the user's connections on every protocol. Streams wait for tokens (the
// sender slows down); datagrams on a shared listener are dropped instead.

// rateBurstMin: the bucket holds at least this many bytes, so one packet or
// read always fits; above that it holds a fifth of a second of traffic.
const rateBurstMin = 16 << 10

type userRate struct {
	mu       sync.Mutex
	up, down *rate.Limiter // nil = unlimited
	changed  chan struct{} // closed when the limits change: sleepers re-check
}

func newBucket(bps int64) *rate.Limiter {
	if bps <= 0 {
		return nil
	}
	b := int(bps / 5)
	if b < rateBurstMin {
		b = rateBurstMin
	}
	return rate.NewLimiter(rate.Limit(bps), b)
}

// syncUserRates follows the config: buckets for limited users, none for the
// rest. Called with the user index rebuilt.
func (a *App) syncUserRates(users []UserConfig) {
	a.ratesMu.Lock()
	defer a.ratesMu.Unlock()
	if a.rates == nil {
		a.rates = make(map[string]*userRate)
	}
	keep := make(map[string]bool, len(users))
	for i := range users {
		u := &users[i]
		if u.RateUp <= 0 && u.RateDown <= 0 {
			continue
		}
		keep[u.Username] = true
		r := a.rates[u.Username]
		if r == nil {
			r = &userRate{changed: make(chan struct{})}
			a.rates[u.Username] = r
		}
		r.set(u.RateUp, u.RateDown)
	}
	for name, r := range a.rates {
		if !keep[name] {
			r.set(0, 0) // wake connections waiting on the old limit
			delete(a.rates, name)
		}
	}
}

func (r *userRate) set(up, down int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if same(r.up, up) && same(r.down, down) {
		return
	}
	r.up, r.down = newBucket(up), newBucket(down)
	close(r.changed)
	r.changed = make(chan struct{})
}

func same(l *rate.Limiter, bps int64) bool {
	if l == nil {
		return bps <= 0
	}
	return int64(l.Limit()) == bps
}

// userRate returns the user's buckets, nil when the user has no limit.
func (a *App) userRate(user string) *userRate {
	if user == "" {
		return nil
	}
	a.ratesMu.RLock()
	defer a.ratesMu.RUnlock()
	return a.rates[user]
}

func (r *userRate) bucket(up bool) (*rate.Limiter, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if up {
		return r.up, r.changed
	}
	return r.down, r.changed
}

// wait blocks until n bytes in this direction are within the limit. A
// limit change ends the wait; later bytes count against the new limit.
func (r *userRate) wait(up bool, n int) {
	if r == nil || n <= 0 {
		return
	}
	l, changed := r.bucket(up)
	if l == nil {
		return
	}
	d := reserve(l, n)
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	select {
	case <-t.C:
	case <-changed:
		t.Stop()
	}
}

// reserve takes n tokens (in bursts no larger than the bucket) and returns
// how long until they are all available.
func reserve(l *rate.Limiter, n int) time.Duration {
	now := time.Now()
	b := l.Burst()
	var d time.Duration
	for n > 0 {
		k := n
		if k > b {
			k = b
		}
		res := l.ReserveN(now, k)
		if !res.OK() {
			return 0
		}
		d = res.DelayFrom(now)
		n -= k
	}
	return d
}

// allow reports whether a datagram of n bytes is within the limit now
// (policing: a datagram over the limit is dropped, not delayed).
func (r *userRate) allow(up bool, n int) bool {
	if r == nil {
		return true
	}
	l, _ := r.bucket(up)
	return l == nil || l.AllowN(time.Now(), min(n, l.Burst()))
}

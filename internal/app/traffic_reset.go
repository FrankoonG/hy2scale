package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"
)

// Periodic traffic reset: a user's TrafficUsed goes back to 0 at 00:00
// (node local time) on each boundary of its period.

const (
	TrafficResetDaily   = "daily"
	TrafficResetWeekly  = "weekly"
	TrafficResetMonthly = "monthly"
)

// ValidateTrafficReset checks a period and its day and returns the day as
// stored (0 for never and daily).
func ValidateTrafficReset(period string, day int) (int, error) {
	switch period {
	case "", TrafficResetDaily:
		return 0, nil
	case TrafficResetWeekly:
		if day < 1 || day > 7 {
			return 0, fmt.Errorf("traffic_reset_day must be 1 (Monday) .. 7 (Sunday) for weekly")
		}
		return day, nil
	case TrafficResetMonthly:
		if day < 1 || day > 31 {
			return 0, fmt.Errorf("traffic_reset_day must be 1..31 for monthly")
		}
		return day, nil
	}
	return 0, fmt.Errorf("traffic_reset must be \"\", daily, weekly or monthly")
}

func monthBoundary(y int, m time.Month, day int, loc *time.Location) time.Time {
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
	if day > last {
		day = last
	}
	return time.Date(y, m, day, 0, 0, 0, 0, loc)
}

// TrafficResetLast is the latest reset boundary at or before t, in t's
// location; zero for never or an invalid setting.
func TrafficResetLast(period string, day int, t time.Time) time.Time {
	if _, err := ValidateTrafficReset(period, day); err != nil || period == "" {
		return time.Time{}
	}
	loc := t.Location()
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	switch period {
	case TrafficResetDaily:
		return midnight
	case TrafficResetWeekly:
		wd := int(t.Weekday())
		if wd == 0 {
			wd = 7
		}
		return midnight.AddDate(0, 0, -((wd - day + 7) % 7))
	default: // monthly
		b := monthBoundary(t.Year(), t.Month(), day, loc)
		if b.After(t) {
			p := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -1, 0)
			b = monthBoundary(p.Year(), p.Month(), day, loc)
		}
		return b
	}
}

// TrafficResetNext is the first reset boundary strictly after t, in t's
// location; zero for never or an invalid setting.
func TrafficResetNext(period string, day int, t time.Time) time.Time {
	last := TrafficResetLast(period, day, t)
	if last.IsZero() {
		return last
	}
	loc := t.Location()
	switch period {
	case TrafficResetDaily:
		return time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, loc)
	case TrafficResetWeekly:
		return time.Date(last.Year(), last.Month(), last.Day()+7, 0, 0, 0, 0, loc)
	default:
		n := time.Date(last.Year(), last.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
		return monthBoundary(n.Year(), n.Month(), day, loc)
	}
}

// TrafficResetDue: a reset boundary lies in (since, now] (boundaries are
// taken in now's location).
func TrafficResetDue(period string, day int, since, now time.Time) bool {
	b := TrafficResetLast(period, day, now)
	return !b.IsZero() && b.After(since)
}

// trafficClockOffset shifts the clock of the traffic reset scheduler. Only
// for tests (HY2SCALE_TRAFFIC_RESET_CLOCK_OFFSET, a Go duration), read once.
var trafficClockOffset = func() time.Duration {
	v := os.Getenv("HY2SCALE_TRAFFIC_RESET_CLOCK_OFFSET")
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("[traffic] ignoring HY2SCALE_TRAFFIC_RESET_CLOCK_OFFSET=%q: %v", v, err)
		return 0
	}
	log.Printf("[traffic] traffic reset clock shifted by %s (test setting)", d)
	return d
}()

// TrafficNow is the clock of traffic resets (node local time).
func TrafficNow() time.Time { return time.Now().Add(trafficClockOffset).Local() }

func parseRFC3339(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// resetSince: the later of the last reset and the time the period took
// effect; false when neither is known.
func (u *UserConfig) resetSince() (time.Time, bool) {
	at, ok1 := parseRFC3339(u.TrafficResetAt)
	from, ok2 := parseRFC3339(u.TrafficResetFrom)
	switch {
	case ok1 && ok2:
		if from.After(at) {
			return from, true
		}
		return at, true
	case ok1:
		return at, true
	case ok2:
		return from, true
	}
	return time.Time{}, false
}

// TrafficResetNextOf: the user's next reset moment ("" without a period).
func TrafficResetNextOf(u *UserConfig, now time.Time) string {
	n := TrafficResetNext(u.TrafficReset, u.TrafficResetDay, now)
	if n.IsZero() {
		return ""
	}
	return n.Format(time.RFC3339)
}

// ApplyTrafficResets resets the users whose period boundary has passed
// since their last reset (once, however many boundaries passed — e.g. after
// downtime), live counters included, and persists. A user with a period but
// no anchor yet is anchored at now without a reset. Returns the usernames
// reset.
func (a *App) ApplyTrafficResets(now time.Time) []string {
	need := false
	for _, u := range a.store.Get().Users {
		if u.TrafficReset == "" {
			continue
		}
		since, ok := u.resetSince()
		if !ok || TrafficResetDue(u.TrafficReset, u.TrafficResetDay, since, now) {
			need = true
			break
		}
	}
	if !need {
		return nil
	}
	stamp := now.Format(time.RFC3339)
	var reset []string
	a.usersMu.RLock()
	err := a.store.Update(func(c *Config) {
		for i := range c.Users {
			u := &c.Users[i]
			if u.TrafficReset == "" {
				continue
			}
			since, ok := u.resetSince()
			if !ok {
				u.TrafficResetFrom = stamp
				continue
			}
			if !TrafficResetDue(u.TrafficReset, u.TrafficResetDay, since, now) {
				continue
			}
			if live, ok := a.userIndex[u.Username]; ok {
				atomic.StoreInt64(&live.TrafficUsed, 0)
			}
			atomic.StoreInt64(&u.TrafficUsed, 0)
			u.TrafficResetAt = stamp
			reset = append(reset, u.Username)
		}
	})
	a.usersMu.RUnlock()
	if err != nil {
		log.Printf("[traffic] periodic reset not persisted: %v", err)
	}
	if len(reset) > 0 {
		a.rebuildUserIndex()
		log.Printf("[traffic] periodic reset: %v", reset)
	}
	return reset
}

// runTrafficResets checks the reset periods at start (catching up after
// downtime) and then every minute.
func (a *App) runTrafficResets(ctx context.Context) {
	a.ApplyTrafficResets(TrafficNow())
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.ApplyTrafficResets(TrafficNow())
		}
	}
}

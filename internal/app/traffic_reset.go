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
// (node local time; UTC for a shared user) every N days, weeks or months
// counted from a start date.

const (
	TrafficResetDaily   = "daily"
	TrafficResetWeekly  = "weekly"
	TrafficResetMonthly = "monthly"
)

// TrafficResetMaxEvery bounds the period count.
const TrafficResetMaxEvery = 1000

// legacyAnchor: the start date standing for an old weekday / day of month
// setting (2024-01-01 is a Monday; January has 31 days).
func legacyAnchor(period string, day int) string {
	if (period == TrafficResetWeekly && day >= 1 && day <= 7) || (period == TrafficResetMonthly && day >= 1 && day <= 31) {
		return fmt.Sprintf("2024-01-%02d", day)
	}
	return "2024-01-01"
}

// ResetAnchor: the start date of u's period ("" without one).
func (u *UserConfig) ResetAnchor() string {
	if u.TrafficReset == "" {
		return ""
	}
	if u.TrafficResetAnchor != "" {
		return u.TrafficResetAnchor
	}
	return legacyAnchor(u.TrafficReset, u.TrafficResetDay)
}

// ResetEvery: how many units make u's period (at least 1).
func (u *UserConfig) ResetEvery() int {
	if u.TrafficResetEvery < 1 {
		return 1
	}
	return u.TrafficResetEvery
}

// NormalizeTrafficReset checks u's period and fills what follows from it:
// the unit (day/week/month accepted for daily/weekly/monthly), the count
// (default 1), the start date (default today; an old weekday / day of
// month setting gives its own) and the old-style day (for older holders of
// a shared user).
func NormalizeTrafficReset(u *UserConfig, today time.Time) error {
	switch u.TrafficReset {
	case "":
		u.TrafficResetEvery, u.TrafficResetAnchor, u.TrafficResetDay = 0, "", 0
		return nil
	case "day":
		u.TrafficReset = TrafficResetDaily
	case "week":
		u.TrafficReset = TrafficResetWeekly
	case "month":
		u.TrafficReset = TrafficResetMonthly
	case TrafficResetDaily, TrafficResetWeekly, TrafficResetMonthly:
	default:
		return fmt.Errorf("traffic_reset must be \"\", daily, weekly or monthly")
	}
	if u.TrafficResetEvery < 0 || u.TrafficResetEvery > TrafficResetMaxEvery {
		return fmt.Errorf("traffic_reset_every must be 1..%d", TrafficResetMaxEvery)
	}
	if u.TrafficResetEvery == 0 {
		u.TrafficResetEvery = 1
	}
	if u.TrafficResetAnchor == "" {
		switch {
		case u.TrafficReset == TrafficResetWeekly && u.TrafficResetDay != 0:
			if u.TrafficResetDay < 1 || u.TrafficResetDay > 7 {
				return fmt.Errorf("traffic_reset_day must be 1 (Monday) .. 7 (Sunday) for weekly")
			}
			u.TrafficResetAnchor = legacyAnchor(u.TrafficReset, u.TrafficResetDay)
		case u.TrafficReset == TrafficResetMonthly && u.TrafficResetDay != 0:
			if u.TrafficResetDay < 1 || u.TrafficResetDay > 31 {
				return fmt.Errorf("traffic_reset_day must be 1..31 for monthly")
			}
			u.TrafficResetAnchor = legacyAnchor(u.TrafficReset, u.TrafficResetDay)
		default:
			u.TrafficResetAnchor = today.Format("2006-01-02")
		}
	}
	if a, err := time.Parse("2006-01-02", u.TrafficResetAnchor); err != nil || a.Year() < 2000 || a.Year() > 2200 {
		return fmt.Errorf("traffic_reset_anchor must be a date (YYYY-MM-DD)")
	}
	u.TrafficResetDay = dayOfAnchor(u)
	return nil
}

// dayOfAnchor: the old-style day of u's start date (weekday / day of
// month; 0 for daily or none).
func dayOfAnchor(u *UserConfig) int {
	a, err := time.Parse("2006-01-02", u.ResetAnchor())
	if err != nil {
		return 0
	}
	switch u.TrafficReset {
	case TrafficResetWeekly:
		if wd := int(a.Weekday()); wd != 0 {
			return wd
		}
		return 7
	case TrafficResetMonthly:
		return a.Day()
	}
	return 0
}

// ResetSettingChanged: the period of a differs from b's.
func ResetSettingChanged(a, b *UserConfig) bool {
	return a.TrafficReset != b.TrafficReset || a.ResetEvery() != b.ResetEvery() || a.ResetAnchor() != b.ResetAnchor()
}

func monthBoundary(y int, m time.Month, day int, loc *time.Location) time.Time {
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
	if day > last {
		day = last
	}
	return time.Date(y, m, day, 0, 0, 0, 0, loc)
}

// civilDays: the day number of t's date.
func civilDays(t time.Time) int64 {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Unix() / 86400
}

// resetBoundary: the k-th boundary (k >= 0) of u's period in loc.
func resetBoundary(u *UserConfig, a time.Time, k int, loc *time.Location) time.Time {
	n := k * u.ResetEvery()
	switch u.TrafficReset {
	case TrafficResetDaily:
		return time.Date(a.Year(), a.Month(), a.Day()+n, 0, 0, 0, 0, loc)
	case TrafficResetWeekly:
		return time.Date(a.Year(), a.Month(), a.Day()+7*n, 0, 0, 0, 0, loc)
	default:
		return monthBoundary(a.Year(), a.Month()+time.Month(n), a.Day(), loc)
	}
}

// resetIndex: the index of the latest boundary at or before t (-1 before
// the start date); ok false for no period.
func resetIndex(u *UserConfig, t time.Time) (time.Time, int, bool) {
	if u.TrafficReset == "" {
		return time.Time{}, 0, false
	}
	a, err := time.Parse("2006-01-02", u.ResetAnchor())
	if err != nil {
		return time.Time{}, 0, false
	}
	loc := t.Location()
	if resetBoundary(u, a, 0, loc).After(t) {
		return a, -1, true
	}
	var k int
	switch u.TrafficReset {
	case TrafficResetDaily:
		k = int((civilDays(t) - civilDays(a)) / int64(u.ResetEvery()))
	case TrafficResetWeekly:
		k = int((civilDays(t) - civilDays(a)) / int64(7*u.ResetEvery()))
	default:
		k = ((t.Year()*12 + int(t.Month())) - (a.Year()*12 + int(a.Month()))) / u.ResetEvery()
	}
	for k > 0 && resetBoundary(u, a, k, loc).After(t) {
		k--
	}
	for !resetBoundary(u, a, k+1, loc).After(t) {
		k++
	}
	return a, k, true
}

// TrafficResetLast is u's latest reset boundary at or before t, in t's
// location; zero for none (no period, or before its start date).
func TrafficResetLast(u *UserConfig, t time.Time) time.Time {
	a, k, ok := resetIndex(u, t)
	if !ok || k < 0 {
		return time.Time{}
	}
	return resetBoundary(u, a, k, t.Location())
}

// TrafficResetNext is u's first reset boundary strictly after t, in t's
// location; zero for no period.
func TrafficResetNext(u *UserConfig, t time.Time) time.Time {
	a, k, ok := resetIndex(u, t)
	if !ok {
		return time.Time{}
	}
	return resetBoundary(u, a, k+1, t.Location())
}

// TrafficResetDue: a boundary of u's period lies in (since, now]
// (boundaries taken in now's location).
func TrafficResetDue(u *UserConfig, since, now time.Time) bool {
	b := TrafficResetLast(u, now)
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
	n := TrafficResetNext(u, now)
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
	a.applySharedEpochs()
	for _, u := range a.store.Get().Users {
		if u.TrafficReset == "" || u.Shared != nil {
			continue
		}
		since, ok := u.resetSince()
		if !ok || TrafficResetDue(&u, since, now) {
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
			if u.TrafficReset == "" || u.Shared != nil {
				continue
			}
			since, ok := u.resetSince()
			if !ok {
				u.TrafficResetFrom = stamp
				continue
			}
			if !TrafficResetDue(u, since, now) {
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

// ExtendedExpiry: u's expiry date moved on by one reset period (one month
// without a period), counted from today (UTC, as expiry is checked) when it
// has passed; "" when u never expires.
func ExtendedExpiry(u *UserConfig, now time.Time) string {
	if u.ExpiryDate == "" {
		return ""
	}
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	base, err := time.Parse("2006-01-02", u.ExpiryDate)
	if err != nil || !base.After(today) {
		base = today
	}
	n := u.ResetEvery()
	switch u.TrafficReset {
	case TrafficResetDaily:
		base = base.AddDate(0, 0, n)
	case TrafficResetWeekly:
		base = base.AddDate(0, 0, 7*n)
	case TrafficResetMonthly:
		base = monthBoundary(base.Year(), base.Month()+time.Month(n), base.Day(), time.UTC)
	default:
		base = monthBoundary(base.Year(), base.Month()+1, base.Day(), time.UTC)
	}
	return base.Format("2006-01-02")
}

package geoip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/sockmark"
)

// Automatic updates ("auto" mode): the databases are downloaded from the
// official publishers, DB-IP Lite first (city + asn, monthly) and IPLocate
// as the backup (country + asn, daily, no city). Checked shortly after the
// start, then every 24 h; conditional requests skip unchanged files.

// Modes.
const (
	ModeManual = "manual"
	ModeAuto   = "auto"
)

// Sources: the download URLs. {month} in the DB-IP ones is the UTC month
// (YYYY-MM); when that file is not there yet, the previous month's is used.
type Sources struct {
	DBIPCity        string `json:"dbip_city"`
	DBIPASN         string `json:"dbip_asn"`
	IPLocateCountry string `json:"iplocate_country"`
	IPLocateASN     string `json:"iplocate_asn"`
}

// DefaultSources are the official download locations.
var DefaultSources = Sources{
	DBIPCity:        "https://download.db-ip.com/free/dbip-city-lite-{month}.mmdb.gz",
	DBIPASN:         "https://download.db-ip.com/free/dbip-asn-lite-{month}.mmdb.gz",
	IPLocateCountry: "https://github.com/iplocate/ip-address-databases/raw/main/ip-to-country/ip-to-country.mmdb",
	IPLocateASN:     "https://github.com/iplocate/ip-address-databases/raw/main/ip-to-asn/ip-to-asn.mmdb",
}

// SourcesFromEnv is DefaultSources with HY2SCALE_GEOIP_SOURCES applied (for
// tests only): a JSON object with any of the Sources keys, or the same as
// "key=url,key=url". Keys left out keep their default.
func SourcesFromEnv() Sources {
	src := DefaultSources
	v := strings.TrimSpace(os.Getenv("HY2SCALE_GEOIP_SOURCES"))
	if v == "" {
		return src
	}
	if strings.HasPrefix(v, "{") {
		if err := json.Unmarshal([]byte(v), &src); err != nil {
			log.Printf("[geoip] HY2SCALE_GEOIP_SOURCES: %v", err)
			return DefaultSources
		}
	} else {
		for _, kv := range strings.Split(v, ",") {
			k, u, _ := strings.Cut(strings.TrimSpace(kv), "=")
			switch strings.TrimSpace(k) {
			case "dbip_city":
				src.DBIPCity = u
			case "dbip_asn":
				src.DBIPASN = u
			case "iplocate_country":
				src.IPLocateCountry = u
			case "iplocate_asn":
				src.IPLocateASN = u
			}
		}
	}
	log.Printf("[geoip] sources overridden: %+v", src)
	return src
}

// AutoStatus is the "auto" block of GET /api/geoip/status. Times are RFC
// 3339 UTC, "" = never / not scheduled.
type AutoStatus struct {
	State      string `json:"state"`  // idle | checking | error
	Source     string `json:"source"` // dbip | iplocate | "" (active source)
	LastCheck  string `json:"last_check"`
	LastUpdate string `json:"last_update"`
	NextCheck  string `json:"next_check"`
	Error      string `json:"error"`
}

// UserAgent of the downloads.
var UserAgent = "hy2scale"

// Timing of the checks.
const (
	FileTimeout   = 10 * time.Minute // one download
	CheckInterval = 24 * time.Hour
	RetryInterval = time.Hour // after a check where no source worked
)

type autoState struct {
	mu      sync.Mutex
	mode    string
	gen     uint64 // bumped on every mode change
	running bool
	cancel  context.CancelFunc
	timer   *time.Timer
	next    time.Time

	sources Sources
	client  *http.Client
	now     func() time.Time
}

func (a *autoState) init() {
	a.mode = ModeManual
	a.sources = DefaultSources
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyFromEnvironment
	t.DialContext = sockmark.Dialer(net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	a.client = &http.Client{Transport: t}
	a.now = time.Now
}

// SetSources replaces the download URLs.
func (s *Store) SetSources(src Sources) {
	s.auto.mu.Lock()
	s.auto.sources = src
	s.auto.mu.Unlock()
}

// SetClock replaces the clock (tests: the month of the DB-IP files).
func (s *Store) SetClock(now func() time.Time) {
	s.auto.mu.Lock()
	s.auto.now = now
	s.auto.mu.Unlock()
}

// Mode is manual or auto.
func (s *Store) Mode() string {
	s.auto.mu.Lock()
	defer s.auto.mu.Unlock()
	return s.auto.mode
}

// BootDelay: when the first check runs after the start (30–60 s).
func BootDelay() time.Duration {
	return 30*time.Second + rand.N(30*time.Second)
}

// EnableAuto turns automatic updates on; the first check runs after delay
// (0 = now, already "checking" when this returns).
func (s *Store) EnableAuto(delay time.Duration) {
	a := &s.auto
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mode != ModeAuto {
		a.mode = ModeAuto
		a.gen++
	}
	switch {
	case a.running:
		// a check cancelled by a switch to manual may still be ending: it
		// starts a new one when it sees the generation changed
	case delay <= 0:
		s.startLocked()
	case a.timer == nil:
		s.scheduleLocked(delay)
	}
}

// DisableAuto turns automatic updates off: no more checks, a running one
// is cancelled; the files stay.
func (s *Store) DisableAuto() {
	a := &s.auto
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = ModeManual
	a.gen++
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	a.next = time.Time{}
	if a.cancel != nil {
		a.cancel()
	}
}

// TriggerCheck starts a check now (auto mode only); false when one is
// already running or the mode is manual.
func (s *Store) TriggerCheck() bool {
	a := &s.auto
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mode != ModeAuto || a.running {
		return false
	}
	s.startLocked()
	return true
}

// CheckNow runs one check and waits for it (whatever the mode; tests).
func (s *Store) CheckNow(ctx context.Context) AutoStatus {
	a := &s.auto
	a.mu.Lock()
	for a.running {
		a.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		a.mu.Lock()
	}
	a.running = true
	a.mu.Unlock()
	s.check(ctx)
	a.mu.Lock()
	a.running = false
	a.mu.Unlock()
	_, st := s.autoStatus()
	return st
}

func (s *Store) scheduleLocked(d time.Duration) {
	a := &s.auto
	if a.timer != nil {
		a.timer.Stop()
	}
	gen := a.gen
	a.next = a.now().Add(d)
	a.timer = time.AfterFunc(d, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.gen != gen || a.mode != ModeAuto || a.running {
			return
		}
		a.timer = nil
		s.startLocked()
	})
}

// startLocked runs a check in the background; s.auto.mu is held.
func (s *Store) startLocked() {
	a := &s.auto
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	a.next = time.Time{}
	a.running = true
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	gen := a.gen
	go func() {
		s.check(ctx)
		cancel()
		a.mu.Lock()
		defer a.mu.Unlock()
		a.running, a.cancel = false, nil
		if a.mode != ModeAuto {
			return
		}
		if a.gen != gen { // turned off and on again while this one ran
			s.startLocked()
			return
		}
		s.scheduleLocked(nextInterval(s.autoMeta().Failed))
	}()
}

// nextInterval: 24 h ± 30 min, or 1 h ± 10 min after a failed check.
func nextInterval(failed bool) time.Duration {
	if failed {
		return RetryInterval - 10*time.Minute + rand.N(20*time.Minute)
	}
	return CheckInterval - 30*time.Minute + rand.N(time.Hour)
}

func (s *Store) autoStatus() (string, AutoStatus) {
	a := &s.auto
	a.mu.Lock()
	mode, running, next := a.mode, a.running, a.next
	a.mu.Unlock()
	m := s.autoMeta()
	st := AutoStatus{State: "idle", Source: m.Source, LastCheck: m.LastCheck, LastUpdate: m.LastUpdate, Error: m.Error}
	switch {
	case running:
		st.State = "checking"
	case m.Failed:
		st.State = "error"
	}
	if mode == ModeAuto && !running && !next.IsZero() {
		st.NextCheck = next.UTC().Format(time.RFC3339)
	}
	return mode, st
}

// check: the primary (DB-IP: city, asn); when any part of it fails, the
// backup (IPLocate: country, asn), keeping whatever city database there is.
func (s *Store) check(ctx context.Context) {
	a := &s.auto
	a.mu.Lock()
	src, now := a.sources, a.now().UTC()
	a.mu.Unlock()
	log.Printf("[geoip] checking for database updates")
	updated := false
	var errs []string
	perr := func() error {
		for _, f := range []struct{ kind, url string }{{KindCity, src.DBIPCity}, {KindASN, src.DBIPASN}} {
			ch, err := s.fetchMonthly(ctx, f.url, f.kind, SourceDBIP, now)
			updated = updated || ch
			if err != nil {
				return fmt.Errorf("%s: %w", f.kind, err)
			}
		}
		return nil
	}()
	active, failed := "", false
	if perr == nil {
		active = SourceDBIP
		if s.loaded(KindCountry) {
			// the city database answers country questions
			if err := s.Remove(KindCountry); err == nil {
				updated = true
			}
		}
	} else if ctx.Err() == nil {
		errs = append(errs, "dbip "+perr.Error())
		log.Printf("[geoip] DB-IP failed (%v); trying IPLocate", perr)
		ok := 0
		for _, f := range []struct{ kind, url string }{{KindCountry, src.IPLocateCountry}, {KindASN, src.IPLocateASN}} {
			ch, err := s.fetch(ctx, f.url, f.kind, SourceIPLocate)
			updated = updated || ch
			if err != nil {
				errs = append(errs, fmt.Sprintf("iplocate %s: %v", f.kind, err))
				continue
			}
			ok++
		}
		if ok > 0 {
			active = SourceIPLocate
		}
		failed = ok < 2
	}
	if ctx.Err() != nil {
		log.Printf("[geoip] check cancelled")
		return
	}
	s.updateAutoMeta(func(m *autoMeta) {
		ts := s.auto.now().UTC().Format(time.RFC3339)
		m.LastCheck = ts
		if updated {
			m.LastUpdate = ts
		}
		if active != "" {
			m.Source = active
		}
		m.Error, m.Failed = strings.Join(errs, "; "), failed
	})
	log.Printf("[geoip] check done: source %q, updated %v, errors %q", active, updated, strings.Join(errs, "; "))
}

var errNotFound = errors.New("HTTP 404")

// fetchMonthly: url with {month} = this month, else the previous one.
func (s *Store) fetchMonthly(ctx context.Context, url, kind, source string, now time.Time) (bool, error) {
	if !strings.Contains(url, "{month}") {
		return s.fetch(ctx, url, kind, source)
	}
	cur := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	ch, err := s.fetch(ctx, strings.ReplaceAll(url, "{month}", cur.Format("2006-01")), kind, source)
	if errors.Is(err, errNotFound) {
		prev := cur.AddDate(0, -1, 0).Format("2006-01")
		return s.fetch(ctx, strings.ReplaceAll(url, "{month}", prev), kind, source)
	}
	return ch, err
}

// fetch downloads url as the database of kind unless it is unchanged
// since the last download (conditional request); true = replaced.
func (s *Store) fetch(ctx context.Context, url, kind, source string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, FileTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if fm := s.fileMeta(kind); fm != nil && fm.Source == source && fm.URL == url && s.loaded(kind) {
		if fm.ETag != "" {
			req.Header.Set("If-None-Match", fm.ETag)
		}
		if fm.LastModified != "" {
			req.Header.Set("If-Modified-Since", fm.LastModified)
		}
	}
	s.auto.mu.Lock()
	client := s.auto.client
	s.auto.mu.Unlock()
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return false, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, errNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	fm := fileMeta{Source: source, URL: url, ETag: resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"), Fetched: s.auto.now().UTC().Format(time.RFC3339)}
	if _, err := s.installStream(&capped{r: resp.Body, n: MaxUpload}, kind, fm); err != nil {
		return false, err
	}
	return true, nil
}

// capped reads at most n bytes, then fails with ErrTooLarge.
type capped struct {
	r io.Reader
	n int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.n <= 0 {
		var one [1]byte
		if k, _ := c.r.Read(one[:]); k > 0 {
			return 0, ErrTooLarge
		}
		return c.r.Read(p)
	}
	if int64(len(p)) > c.n {
		p = p[:c.n]
	}
	k, err := c.r.Read(p)
	c.n -= int64(k)
	return k, err
}

package storage

// Hourly totals of the audit records, so the summary of a long time range
// needs no scan of the records: per hour the flows and bytes, and the
// biggest users, sources, destinations and application protocols. One file
// per UTC day in <audit dir>/rollup/; the days being written stay in memory
// and are saved once a minute. Existing records are summed up once in the
// background (a marker file says that is done); until then summaries scan.

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
)

const (
	rollupKeep    = 50     // keys kept per hour and kind when an hour is saved
	rollupLiveCap = 20_000 // distinct keys per hour and kind in memory
	rollupMarker  = "complete"
)

// tally: flows, up and down bytes of a key, and an address it was seen
// with (to place it on the map). Stored as [f, u, d] or [f, u, d, ip].
type tally struct {
	F, U, D int64
	IP      string
}

func (t tally) MarshalJSON() ([]byte, error) {
	if t.IP == "" {
		return json.Marshal([3]int64{t.F, t.U, t.D})
	}
	return json.Marshal([]any{t.F, t.U, t.D, t.IP})
}

func (t *tally) UnmarshalJSON(b []byte) error {
	var a []json.RawMessage
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if len(a) < 3 {
		return errors.New("tally: 3 numbers expected")
	}
	for i, p := range []*int64{&t.F, &t.U, &t.D} {
		if err := json.Unmarshal(a[i], p); err != nil {
			return err
		}
	}
	if len(a) > 3 {
		json.Unmarshal(a[3], &t.IP)
	}
	return nil
}

type hourAgg struct {
	Flows int64            `json:"f"`
	Up    int64            `json:"u"`
	Down  int64            `json:"d"`
	Users map[string]tally `json:"us,omitempty"`
	Srcs  map[string]tally `json:"sr,omitempty"`
	Dsts  map[string]tally `json:"ds,omitempty"`
	Apps  map[string]tally `json:"ap,omitempty"`
}

// day: hour start (unix ms) → totals.
type day map[int64]*hourAgg

// Rollup keeps the hourly totals.
type Rollup struct {
	dir string

	mu    sync.Mutex
	days  map[string]day // days in memory (being written)
	dirty map[string]bool
	long  map[string][2]int64 // flows with interim records: totals so far
	ready bool                // existing records summed up
	live  bool                // records written now are counted (see startLive)
}

func newRollup(auditDir string) *Rollup {
	r := &Rollup{dir: filepath.Join(auditDir, "rollup"), days: map[string]day{}, dirty: map[string]bool{},
		long: map[string][2]int64{}}
	_, err := os.Stat(filepath.Join(r.dir, rollupMarker))
	r.ready = err == nil
	r.live = r.ready
	if !r.ready {
		// totals of an unfinished backfill: the backfill does them all again
		os.RemoveAll(r.dir)
	}
	return r
}

// Ready reports whether the totals cover every stored record.
func (r *Rollup) Ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready
}

func dayKey(ms int64) string { return time.UnixMilli(ms).UTC().Format("20060102") }

func hourOf(ms int64) int64 { return ms - ms%3_600_000 }

// srcKey: the peer a flow came through, else the client's own address
// (outside a VPN tunnel when known).
func srcKey(rec *audit.Record) string {
	if rec.SrcNode != "" {
		return rec.SrcNode
	}
	if rec.SrcPeer != "" {
		return hostOf(rec.SrcPeer)
	}
	return hostOf(rec.Src)
}

// ipOf: the IP of host:port or of a bare address ("" = a name).
func ipOf(hp string) string {
	h := hostOf(hp)
	if ip := net.ParseIP(h); ip != nil {
		return ip.String()
	}
	return ""
}

// clientIP: the client's own address.
func clientIP(rec *audit.Record) string {
	if rec.SrcPeer != "" {
		return ipOf(rec.SrcPeer)
	}
	return ipOf(rec.Src)
}

// dstIP: the destination's address.
func dstIP(rec *audit.Record) string {
	if rec.DstIP != "" {
		return ipOf(rec.DstIP)
	}
	return ipOf(rec.Dst)
}

func publicIP(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate()
}

// betterIP: keep a public address over a private one.
func betterIP(cur, ip string) string {
	if ip == "" || cur != "" && (publicIP(cur) || !publicIP(ip)) {
		return cur
	}
	return ip
}

func dstKey(rec *audit.Record) string {
	if rec.DstHost != "" {
		return rec.DstHost
	}
	return hostOf(rec.Dst)
}

// AppKey: the application protocol of a record, else its transport.
func AppKey(rec *audit.Record) string {
	if rec.App != "" {
		return rec.App
	}
	return rec.Proto
}

func bump(m *map[string]tally, k string, flows, up, down int64, ip string, limit int) {
	if k == "" {
		return
	}
	if *m == nil {
		*m = map[string]tally{}
	}
	t, ok := (*m)[k]
	if !ok && len(*m) >= limit {
		return
	}
	t.F += flows
	t.U += up
	t.D += down
	t.IP = betterIP(t.IP, ip)
	(*m)[k] = t
}

// addTo counts rec into days (long: the interim totals of open flows).
func addTo(days map[string]day, long map[string][2]int64, rec *audit.Record) string {
	flows, up, down := int64(1), rec.Up, rec.Down
	isLong := rec.Partial || rec.End-rec.Start >= audit.InterimEvery.Milliseconds()
	if isLong {
		if prev, seen := long[rec.ID]; seen {
			flows, up, down = 0, rec.Up-prev[0], rec.Down-prev[1]
			if up < 0 {
				up = 0
			}
			if down < 0 {
				down = 0
			}
		}
		if rec.Partial {
			long[rec.ID] = [2]int64{rec.Up, rec.Down}
		} else {
			delete(long, rec.ID)
		}
	}
	k := dayKey(rec.End)
	d := days[k]
	if d == nil {
		d = day{}
		days[k] = d
	}
	h := hourOf(rec.End)
	a := d[h]
	if a == nil {
		a = &hourAgg{}
		d[h] = a
	}
	a.Flows += flows
	a.Up += up
	a.Down += down
	bump(&a.Users, rec.User, flows, up, down, clientIP(rec), rollupLiveCap)
	bump(&a.Srcs, srcKey(rec), flows, up, down, clientIP(rec), rollupLiveCap)
	bump(&a.Dsts, dstKey(rec), flows, up, down, dstIP(rec), rollupLiveCap)
	bump(&a.Apps, AppKey(rec), flows, up, down, "", rollupLiveCap)
	return k
}

// add counts a record just written.
func (r *Rollup) add(rec *audit.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.live {
		return // stored before the backfill's snapshot: the backfill counts it
	}
	if k := dayKey(rec.End); r.days[k] == nil {
		if d := r.readDay(k); d != nil { // saved earlier (before a restart)
			r.days[k] = d
		}
	}
	r.dirty[addTo(r.days, r.long, rec)] = true
	if len(r.long) > 200_000 { // flows never ended (crash): forget
		r.long = map[string][2]int64{}
	}
}

func trim(m map[string]tally, n int) map[string]tally {
	if len(m) <= n {
		return m
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := m[keys[i]], m[keys[j]]
		if a.U+a.D != b.U+b.D {
			return a.U+a.D > b.U+b.D
		}
		return keys[i] < keys[j]
	})
	out := make(map[string]tally, n)
	for _, k := range keys[:n] {
		out[k] = m[k]
	}
	return out
}

func (r *Rollup) path(k string) string { return filepath.Join(r.dir, k+".json") }

func (r *Rollup) readDay(k string) day {
	b, err := os.ReadFile(r.path(k))
	if err != nil {
		return nil
	}
	var d day
	if json.Unmarshal(b, &d) != nil {
		return nil
	}
	return d
}

func (r *Rollup) writeDay(k string, d day) error {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	out := day{}
	for h, a := range d {
		c := *a
		c.Users, c.Srcs, c.Dsts, c.Apps = trim(a.Users, rollupKeep), trim(a.Srcs, rollupKeep), trim(a.Dsts, rollupKeep), trim(a.Apps, rollupKeep)
		out[h] = &c
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	tmp := r.path(k) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path(k))
}

// save writes the changed days and lets go of days no longer written.
func (r *Rollup) save() {
	r.mu.Lock()
	defer r.mu.Unlock()
	today := dayKey(time.Now().UnixMilli())
	yesterday := dayKey(time.Now().Add(-24 * time.Hour).UnixMilli())
	for k := range r.dirty {
		if r.writeDay(k, r.days[k]) == nil {
			delete(r.dirty, k)
		}
	}
	for k := range r.days {
		if k != today && k != yesterday && !r.dirty[k] {
			delete(r.days, k)
		}
	}
}

// startLive counts every record written from now on; the records stored
// so far (segs, with their sizes now) are the backfill's. Called with
// writes held off.
func (r *Rollup) startLive() {
	r.mu.Lock()
	r.live = true
	r.mu.Unlock()
}

// backfill sums up the records of segs (each up to the size it had when
// counting live began), oldest first.
func (r *Rollup) backfill(seg *Segments, segs []SegInfo, stop <-chan struct{}) error {
	days, long := map[string]day{}, map[string][2]int64{}
	for _, si := range segs {
		f, err := os.Open(seg.Path(si.Name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(io.LimitReader(f, si.Size))
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case <-stop:
				f.Close()
				return errors.New("stopped")
			default:
			}
			var rec audit.Record
			if json.Unmarshal(sc.Bytes(), &rec) != nil {
				continue
			}
			addTo(days, long, &rec)
		}
		f.Close()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(segs) == 0 && len(r.days) == 0 {
		r.ready = true // nothing stored: no files (done again at the next start)
		return nil
	}
	for k, d := range days {
		if live := r.days[k]; live != nil {
			for h, a := range d {
				if la := live[h]; la != nil {
					merge(la, a, rollupLiveCap)
				} else {
					live[h] = a
				}
			}
			r.dirty[k] = true
			continue
		}
		if err := r.writeDay(k, d); err != nil {
			return err
		}
	}
	for k := range r.dirty {
		if r.writeDay(k, r.days[k]) == nil {
			delete(r.dirty, k)
		}
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.dir, rollupMarker), []byte("1\n"), 0o644); err != nil {
		return err
	}
	r.ready = true
	return nil
}

func merge(dst, src *hourAgg, limit int) {
	dst.Flows += src.Flows
	dst.Up += src.Up
	dst.Down += src.Down
	for _, p := range []struct {
		d *map[string]tally
		s map[string]tally
	}{
		{&dst.Users, src.Users}, {&dst.Srcs, src.Srcs}, {&dst.Dsts, src.Dsts}, {&dst.Apps, src.Apps}} {
		for k, t := range p.s {
			bump(p.d, k, t.F, t.U, t.D, t.IP, limit)
		}
	}
}

// prune drops the days that end before oldest (unix ms; 0 = none left).
func (r *Rollup) prune(oldest int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ents, _ := os.ReadDir(r.dir)
	for _, e := range ents {
		k, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || len(k) != 8 {
			continue
		}
		t, err := time.Parse("20060102", k)
		if err != nil {
			continue
		}
		if oldest == 0 || t.Add(24*time.Hour).UnixMilli() <= oldest {
			os.Remove(r.path(k))
			delete(r.days, k)
			delete(r.dirty, k)
		}
	}
}

// wipe drops every total (the records are gone).
func (r *Rollup) wipe() {
	r.mu.Lock()
	r.days, r.dirty, r.long = map[string]day{}, map[string]bool{}, map[string][2]int64{}
	r.mu.Unlock()
	r.prune(0)
}

// sum adds up the hours that overlap [from, to] (0 = open).
func (r *Rollup) sum(from, to int64) (StatsResult, int64) {
	var res StatsResult
	r.mu.Lock()
	mem := map[string]day{}
	for k, d := range r.days {
		c := day{}
		for h, a := range d {
			cp := &hourAgg{}
			merge(cp, a, rollupLiveCap)
			c[h] = cp
		}
		mem[k] = c
	}
	r.mu.Unlock()

	keys := map[string]bool{}
	for k := range mem {
		keys[k] = true
	}
	ents, _ := os.ReadDir(r.dir)
	for _, e := range ents {
		if k, ok := strings.CutSuffix(e.Name(), ".json"); ok && len(k) == 8 {
			keys[k] = true
		}
	}
	total := &hourAgg{}
	oldest := int64(0)
	for k := range keys {
		t, err := time.Parse("20060102", k)
		if err != nil {
			continue
		}
		if (from > 0 && t.Add(24*time.Hour).UnixMilli() <= from) || (to > 0 && t.UnixMilli() > to) {
			continue
		}
		d := mem[k]
		if d == nil {
			d = r.readDay(k)
		}
		for h, a := range d {
			if (from > 0 && h+3_600_000 <= from) || (to > 0 && h > to) {
				continue
			}
			merge(total, a, 1<<30)
			if oldest == 0 || h < oldest {
				oldest = h
			}
		}
	}
	res.Flows, res.Up, res.Down = total.Flows, total.Up, total.Down
	res.TopUsers, res.TopSrc, res.TopDst, res.TopApps = topOf(total.Users), topOf(total.Srcs), topOf(total.Dsts), topOf(total.Apps)
	return res, oldest
}

func topOf(m map[string]tally) []Stat {
	sm := make(map[string]*Stat, len(m))
	for k, t := range m {
		sm[k] = &Stat{Key: k, Flows: t.F, Up: t.U, Down: t.D, Bytes: t.U + t.D, IP: t.IP}
	}
	return top(sm, 10)
}

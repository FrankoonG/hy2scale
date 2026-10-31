package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
)

// Queue length and page sizes.
const (
	AuditQueueLen     = 16384
	QueryDefaultLimit = 100
	QueryMaxLimit     = 1000
)

// Bounds of one audit request (variables so tests can lower them).
var (
	QueryMaxScan     = 200_000
	QueryMaxTime     = 2 * time.Second
	StatsMaxScan     = 500_000
	StatsMaxTime     = 3 * time.Second
	ExportMaxRecords = 100_000
	ExportMaxScan    = 2_000_000
	ExportMaxTime    = 60 * time.Second
)

// AuditStore keeps audit records: an asynchronous writer behind a bounded
// queue (full = the record is dropped and counted, a flow never waits) and
// newest-first queries over the segments.
type AuditStore struct {
	seg     *Segments
	ch      chan *audit.Record
	dropped atomic.Int64
	written atomic.Int64
	on      atomic.Bool
	flushMu sync.Mutex

	wmu     sync.Mutex // a write vs. Wipe
	wipedAt int64      // records that ended by then are gone (under wmu)
}

// NewAuditStore uses dir (<data>/audit).
func NewAuditStore(dir string) *AuditStore {
	return &AuditStore{seg: OpenSegments(dir), ch: make(chan *audit.Record, AuditQueueLen)}
}

// Segments: the files.
func (s *AuditStore) Segments() *Segments { return s.seg }

// Dropped: records lost to a full queue since start.
func (s *AuditStore) Dropped() int64 { return s.dropped.Load() }

// Written: records written since start.
func (s *AuditStore) Written() int64 { return s.written.Load() }

// SetEnabled turns recording on or off (core/audit follows).
func (s *AuditStore) SetEnabled(on bool) {
	s.on.Store(on)
	audit.SetEnabled(on)
}

// Enabled reports whether records are kept.
func (s *AuditStore) Enabled() bool { return s.on.Load() }

// Submit queues a record; it never blocks.
func (s *AuditStore) Submit(r *audit.Record) {
	if !s.on.Load() {
		return
	}
	select {
	case s.ch <- r:
	default:
		s.dropped.Add(1)
	}
}

// Run writes queued records until ctx ends, and once a minute emits
// interim records of long flows; prune (retention) runs at start and with
// the same cadence. The store receives records once installed as the sink
// (audit.SetSink(s.Submit)).
func (s *AuditStore) Run(ctx context.Context, prune func()) {
	if prune != nil {
		prune()
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	flush := time.NewTicker(time.Second)
	defer flush.Stop()
	dirty := false
	for {
		select {
		case <-ctx.Done():
			// flows cut by the shutdown would leave no record: record them
			// as they stand
			audit.Interim(time.Now(), 0)
			s.drain()
			s.seg.Close()
			return
		case r := <-s.ch:
			s.write(r)
			dirty = true
			// write what is queued, then make it visible
			for n := 0; n < 4096; n++ {
				select {
				case r = <-s.ch:
					s.write(r)
					continue
				default:
				}
				break
			}
			if len(s.ch) == 0 {
				s.seg.Flush()
				dirty = false
			}
		case <-flush.C:
			if dirty {
				s.seg.Flush()
				dirty = false
			}
		case now := <-tick.C:
			audit.Interim(now, audit.InterimEvery)
			if prune != nil {
				prune()
			}
		}
	}
}

func (s *AuditStore) drain() {
	for {
		select {
		case r := <-s.ch:
			s.write(r)
		default:
			s.seg.Flush()
			return
		}
	}
}

// Sync writes everything queued now (tests, wipe).
func (s *AuditStore) Sync() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.drain()
}

func (s *AuditStore) write(r *audit.Record) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if r.End <= s.wipedAt {
		return
	}
	if s.seg.Append(b, time.UnixMilli(r.End)) == nil {
		s.written.Add(1)
	}
}

// Wipe deletes every stored record, also those still queued.
func (s *AuditStore) Wipe() error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.wipedAt = time.Now().UnixMilli()
	for {
		select {
		case <-s.ch:
			continue
		default:
		}
		break
	}
	return s.seg.Wipe()
}

// WriteNow appends a record synchronously (tests and tools).
func (s *AuditStore) WriteNow(r *audit.Record) {
	s.write(r)
	s.seg.Flush()
}

// ---------------------------------------------------------------- query

// Query selects records. Zero values are "any".
type Query struct {
	From, To int64 // unix ms; a record matches when it overlaps [From, To]
	User     string
	Src      string // substring of src / src_node
	Dst      string // substring of dst / dst_host / dst_ip
	Inbound  string
	Exit     string
	Proto    string
	Result   string
	Q        string // substring of any text field
	Limit    int
	Cursor   string
}

// QueryResult is one page.
type QueryResult struct {
	Records    []audit.Record `json:"records"`
	NextCursor string         `json:"next_cursor"`
	Scanned    int            `json:"scanned"`
	Truncated  bool           `json:"truncated"`
}

type cursor struct {
	Seg string `json:"s"`
	Off int64  `json:"o"`
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ErrBadCursor: the cursor is not one this store issued.
var ErrBadCursor = errors.New("bad cursor")

func decodeCursor(s string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || !validSegName(c.Seg) || c.Off < 0 {
		return c, ErrBadCursor
	}
	return c, nil
}

// matcher is a compiled query.
type matcher struct {
	q                       Query
	exact                   [][]byte // raw-line prefilters for exact fields
	srcL, dstL, qL          string
	srcNeed, dstNeed, qNeed []byte
	lower                   []byte
}

func compile(q Query) *matcher {
	m := &matcher{q: q}
	for _, kv := range [][2]string{{"user", q.User}, {"inbound", q.Inbound}, {"exit", q.Exit},
		{"proto", q.Proto}, {"result", q.Result}} {
		if kv[1] != "" {
			v, _ := json.Marshal(kv[1])
			m.exact = append(m.exact, []byte(`"`+kv[0]+`":`+string(v)))
		}
	}
	m.srcL, m.dstL, m.qL = strings.ToLower(q.Src), strings.ToLower(q.Dst), strings.ToLower(q.Q)
	need := func(s string) []byte {
		if s == "" {
			return nil
		}
		// only a plain substring survives JSON escaping unchanged
		v, _ := json.Marshal(s)
		if string(v[1:len(v)-1]) != s {
			return nil
		}
		return []byte(s)
	}
	m.srcNeed, m.dstNeed, m.qNeed = need(m.srcL), need(m.dstL), need(m.qL)
	return m
}

func lowerASCII(dst, src []byte) []byte {
	dst = dst[:0]
	for _, c := range src {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst = append(dst, c)
	}
	return dst
}

// intField reads a top-level integer field from a record line written by
// this store (no parsing of the whole line).
func intField(line []byte, key string) (int64, bool) {
	k := []byte(`"` + key + `":`)
	i := bytes.Index(line, k)
	if i < 0 {
		return 0, false
	}
	j := i + len(k)
	var n int64
	neg := false
	if j < len(line) && line[j] == '-' {
		neg = true
		j++
	}
	start := j
	for j < len(line) && line[j] >= '0' && line[j] <= '9' {
		n = n*10 + int64(line[j]-'0')
		j++
	}
	if j == start {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

// match checks a raw line; the record is decoded only when the cheap checks
// pass.
func (m *matcher) match(line []byte) (audit.Record, bool) {
	var r audit.Record
	q := m.q
	if q.From > 0 || q.To > 0 {
		st, ok1 := intField(line, "start")
		en, ok2 := intField(line, "end")
		if !ok1 || !ok2 || (q.From > 0 && en < q.From) || (q.To > 0 && st > q.To) {
			return r, false
		}
	}
	for _, e := range m.exact {
		if !bytes.Contains(line, e) {
			return r, false
		}
	}
	if m.srcNeed != nil || m.dstNeed != nil || m.qNeed != nil {
		m.lower = lowerASCII(m.lower, line)
		for _, n := range [][]byte{m.srcNeed, m.dstNeed, m.qNeed} {
			if n != nil && !bytes.Contains(m.lower, n) {
				return r, false
			}
		}
	}
	if json.Unmarshal(line, &r) != nil {
		return r, false
	}
	if (q.User != "" && r.User != q.User) || (q.Inbound != "" && r.Inbound != q.Inbound) ||
		(q.Exit != "" && r.Exit != q.Exit) || (q.Proto != "" && r.Proto != q.Proto) ||
		(q.Result != "" && r.Result != q.Result) {
		return r, false
	}
	if m.srcL != "" && !containsAny(m.srcL, r.Src, r.SrcNode) {
		return r, false
	}
	if m.dstL != "" && !containsAny(m.dstL, r.Dst, r.DstHost, r.DstIP) {
		return r, false
	}
	if m.qL != "" && !containsAny(m.qL, r.ID, r.Proto, r.Inbound, r.User, r.Src, r.SrcNode, r.Dst, r.DstHost,
		r.DstIP, r.Rule, r.Exit, r.ExitPath, r.ExitMode, r.EgressIface, r.EgressSrc, r.Result, r.Error) {
		return r, false
	}
	return r, true
}

func containsAny(needle string, fields ...string) bool {
	for _, f := range fields {
		if f != "" && strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

// scan walks records newest first from the cursor (or the newest record),
// calling fn for each match until fn returns false or a bound is hit. It
// returns the number scanned, whether a bound stopped it, and the cursor of
// the next record not yet scanned ("" = none left).
func (s *AuditStore) scan(ctx context.Context, q Query, maxScan int, maxTime time.Duration,
	fn func(r audit.Record) bool) (scanned int, truncated bool, next string, err error) {
	m := compile(q)
	segs := s.seg.List()
	start := len(segs) - 1
	startOff := int64(-1)
	if q.Cursor != "" {
		c, cerr := decodeCursor(q.Cursor)
		if cerr != nil {
			return 0, false, "", cerr
		}
		start = -1
		for i := len(segs) - 1; i >= 0; i-- {
			if segs[i].Name == c.Seg {
				start, startOff = i, c.Off
				break
			}
			if segCounter(segs[i].Name) < segCounter(c.Seg) {
				start = i // the cursor's segment was pruned: continue below it
				break
			}
		}
		if start < 0 {
			return 0, false, "", nil
		}
	}
	deadline := time.Now().Add(maxTime)
	for i := start; i >= 0; i-- {
		si := segs[i]
		off := int64(-1)
		if i == start {
			off = startOff
		}
		// a segment last written before From holds nothing that ends later
		if q.From > 0 && si.MTime.UnixMilli() < q.From {
			return scanned, false, "", nil
		}
		rl, oerr := openRev(s.seg.Path(si.Name), off)
		if oerr != nil {
			continue // pruned meanwhile
		}
		for {
			line, loff, ok := rl.next()
			if !ok {
				break
			}
			if scanned >= maxScan || (scanned&1023 == 0 && (time.Now().After(deadline) || ctx.Err() != nil)) {
				rl.close()
				return scanned, true, encodeCursor(cursor{si.Name, loff + int64(len(line)) + 1}), nil
			}
			scanned++
			r, ok := m.match(line)
			if !ok {
				continue
			}
			if !fn(r) {
				// the next page starts with this record
				rl.close()
				return scanned, false, encodeCursor(cursor{si.Name, loff + int64(len(line)) + 1}), nil
			}
		}
		rl.close()
	}
	return scanned, false, "", nil
}

// Query returns one page of matching records, newest first.
func (s *AuditStore) Query(ctx context.Context, q Query) (QueryResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = QueryDefaultLimit
	}
	if limit > QueryMaxLimit {
		limit = QueryMaxLimit
	}
	res := QueryResult{Records: []audit.Record{}}
	scanned, trunc, next, err := s.scan(ctx, q, QueryMaxScan, QueryMaxTime, func(r audit.Record) bool {
		if len(res.Records) == limit {
			return false // r is the first record of the next page
		}
		res.Records = append(res.Records, r)
		return true
	})
	if err != nil {
		return res, err
	}
	res.Scanned, res.Truncated, res.NextCursor = scanned, trunc, next
	return res, nil
}

// ---------------------------------------------------------------- stats

// Stat is one ranked entry.
type Stat struct {
	Key   string `json:"key"`
	Flows int64  `json:"flows"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Bytes int64  `json:"bytes"`
}

// StatsResult: aggregates over a time range.
type StatsResult struct {
	Flows     int64  `json:"flows"`
	Up        int64  `json:"up"`
	Down      int64  `json:"down"`
	TopUsers  []Stat `json:"top_users"`
	TopSrc    []Stat `json:"top_sources"`
	TopDst    []Stat `json:"top_destinations"`
	Scanned   int    `json:"scanned"`
	Truncated bool   `json:"truncated"`
}

// Stats aggregates the records matching q. A flow with interim records is
// counted once, with its newest (largest) totals.
func (s *AuditStore) Stats(ctx context.Context, q Query) (StatsResult, error) {
	q.Cursor = ""
	var res StatsResult
	users, srcs, dsts := map[string]*Stat{}, map[string]*Stat{}, map[string]*Stat{}
	seen := map[string]bool{} // flows that may have more than one record
	long := audit.InterimEvery.Milliseconds()
	add := func(m map[string]*Stat, k string, r *audit.Record) {
		if k == "" {
			return
		}
		st := m[k]
		if st == nil {
			if len(m) >= 100_000 {
				return
			}
			st = &Stat{Key: k}
			m[k] = st
		}
		st.Flows++
		st.Up += r.Up
		st.Down += r.Down
		st.Bytes += r.Up + r.Down
	}
	scanned, trunc, _, err := s.scan(ctx, q, StatsMaxScan, StatsMaxTime, func(r audit.Record) bool {
		if r.Partial || r.End-r.Start >= long {
			if seen[r.ID] {
				return true
			}
			seen[r.ID] = true
		}
		res.Flows++
		res.Up += r.Up
		res.Down += r.Down
		add(users, r.User, &r)
		src := r.SrcNode
		if src == "" {
			src = hostOf(r.Src)
		}
		add(srcs, src, &r)
		dst := r.DstHost
		if dst == "" {
			dst = hostOf(r.Dst)
		}
		add(dsts, dst, &r)
		return true
	})
	if err != nil {
		return res, err
	}
	res.Scanned, res.Truncated = scanned, trunc
	res.TopUsers, res.TopSrc, res.TopDst = top(users, 10), top(srcs, 10), top(dsts, 10)
	return res, nil
}

func hostOf(hp string) string {
	if h, _, err := net.SplitHostPort(hp); err == nil {
		return h
	}
	return hp
}

func top(m map[string]*Stat, n int) []Stat {
	out := make([]Stat, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// ---------------------------------------------------------------- export

// CSVColumns: the export columns, in order.
var CSVColumns = []string{"id", "start", "end", "partial", "proto", "inbound", "user", "src", "src_node",
	"dst", "dst_host", "dst_ip", "rule", "exit", "exit_path", "exit_mode", "egress_iface", "egress_src",
	"up", "down", "result", "error"}

func csvRow(r *audit.Record) []string {
	i := func(v int64) string { return strconv.FormatInt(v, 10) }
	p := ""
	if r.Partial {
		p = "true"
	}
	return []string{r.ID, i(r.Start), i(r.End), p, r.Proto, r.Inbound, r.User, r.Src, r.SrcNode,
		r.Dst, r.DstHost, r.DstIP, r.Rule, r.Exit, r.ExitPath, r.ExitMode, r.EgressIface, r.EgressSrc,
		i(r.Up), i(r.Down), r.Result, r.Error}
}

// ExportResult says how an export ended.
type ExportResult struct {
	Records   int
	Scanned   int
	Truncated bool // a bound (records, scan, time) ended it early
}

// Export streams the records matching q (newest first) as "csv" or "jsonl".
func (s *AuditStore) Export(ctx context.Context, w io.Writer, q Query, format string) (ExportResult, error) {
	var res ExportResult
	var cw *csv.Writer
	if format == "csv" {
		cw = csv.NewWriter(w)
		cw.Write(CSVColumns)
	}
	enc := json.NewEncoder(w)
	capped := false
	var werr error
	scanned, trunc, _, err := s.scan(ctx, q, ExportMaxScan, ExportMaxTime, func(r audit.Record) bool {
		if res.Records >= ExportMaxRecords {
			capped = true
			return false
		}
		if cw != nil {
			werr = cw.Write(csvRow(&r))
			if res.Records%1000 == 999 {
				cw.Flush()
			}
		} else {
			werr = enc.Encode(&r)
		}
		if werr != nil {
			return false
		}
		res.Records++
		return true
	})
	if cw != nil {
		cw.Flush()
	}
	if err == nil {
		err = werr
	}
	res.Scanned, res.Truncated = scanned, trunc || capped
	return res, err
}

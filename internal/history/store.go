// Package history records per-peer time-bucketed metrics for the
// PathInfoExpand UI. Three precisions are kept in parallel:
//
//	1m  — one bucket per wall-clock minute, last 60 (= 1 hour back)
//	1h  — one bucket per wall-clock hour,   last 60 (= 2.5 days back)
//	1d  — one bucket per wall-clock day,    last 60 (= 60 days back)
//
// Each Record() call updates the current bucket for all three precisions
// concurrently. When the current sample's bucket timestamp differs from
// the previously-held one for a precision, the previous bucket is
// committed to the ring and a new accumulator starts. Rings are capped
// at MaxBuckets per precision; older entries slide off.
//
// Persistence is a single JSON file written atomically (tmp+rename)
// every persistInterval and on Close(). On Load() the file is restored;
// missing or malformed file is non-fatal (fresh start).
package history

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// MaxBuckets caps each precision's ring. 60 matches the front-end
// status-bar visual cap.
const MaxBuckets = 60

// Bucket is the read-only public view of one time bucket.
type Bucket struct {
	Ts        int64   `json:"ts"`        // unix seconds, aligned to precision
	LatencyMs int     `json:"latencyMs"` // average of online samples; -1 if no online sample
	OnlinePct float64 `json:"onlinePct"` // 0..1 fraction of samples with latency>=0
	TxPeakBps int64   `json:"txPeak"`    // peak observed tx rate in this bucket
	RxPeakBps int64   `json:"rxPeak"`    // peak observed rx rate in this bucket
	Samples   int     `json:"samples"`   // raw samples contributing
}

// acc is the running accumulator for one precision's "current" bucket.
type acc struct {
	Ts         int64 `json:"ts"`
	LatencySum int64 `json:"ls"`
	LatencyN   int   `json:"ln"`
	Online     int   `json:"on"`
	Total      int   `json:"tot"`
	TxPeak     int64 `json:"tx"`
	RxPeak     int64 `json:"rx"`
}

func (a acc) freeze() Bucket {
	out := Bucket{Ts: a.Ts, Samples: a.Total, TxPeakBps: a.TxPeak, RxPeakBps: a.RxPeak, LatencyMs: -1}
	if a.LatencyN > 0 {
		out.LatencyMs = int(a.LatencySum / int64(a.LatencyN))
	}
	if a.Total > 0 {
		out.OnlinePct = float64(a.Online) / float64(a.Total)
	}
	return out
}

// peerHist holds the three precision rings plus their live accumulators.
type peerHist struct {
	M1 []Bucket `json:"m1"`
	H1 []Bucket `json:"h1"`
	D1 []Bucket `json:"d1"`

	CurM acc `json:"curM"`
	CurH acc `json:"curH"`
	CurD acc `json:"curD"`
}

// Sample is one observation pushed in per peer per tick.
type Sample struct {
	Peer      string
	At        time.Time
	LatencyMs int   // <0 means offline
	TxBps     int64 // current tx rate (bps)
	RxBps     int64 // current rx rate (bps)
}

// Snapshot is the API-shaped read-out for one peer.
type Snapshot struct {
	M1 []Bucket `json:"m1"`
	H1 []Bucket `json:"h1"`
	D1 []Bucket `json:"d1"`
}

// Store is the singleton recorder. Safe for concurrent Record and Snapshot.
type Store struct {
	mu              sync.Mutex
	peers           map[string]*peerHist
	path            string
	persistInterval time.Duration
	lastPersist     time.Time
}

// New returns a Store backed by `path` (atomic JSON snapshot). If path
// is empty, the store is in-memory only.
func New(path string) *Store {
	return &Store{
		peers:           make(map[string]*peerHist),
		path:            path,
		persistInterval: 60 * time.Second,
	}
}

func alignMinute(t time.Time) int64 { return t.Unix() / 60 * 60 }
func alignHour(t time.Time) int64   { return t.Unix() / 3600 * 3600 }
func alignDay(t time.Time) int64    { return t.Unix() / 86400 * 86400 }

// commit pushes `a` into ring and trims, returns the new ring.
func commit(ring []Bucket, a acc) []Bucket {
	if a.Total == 0 {
		return ring
	}
	ring = append(ring, a.freeze())
	if len(ring) > MaxBuckets {
		drop := len(ring) - MaxBuckets
		ring = append(ring[:0], ring[drop:]...)
	}
	return ring
}

// updateAcc folds one sample into an accumulator. Returns updated acc.
func updateAcc(a acc, s Sample, ts int64) acc {
	a.Ts = ts
	a.Total++
	if s.LatencyMs >= 0 {
		a.LatencySum += int64(s.LatencyMs)
		a.LatencyN++
		a.Online++
	}
	if s.TxBps > a.TxPeak {
		a.TxPeak = s.TxBps
	}
	if s.RxBps > a.RxPeak {
		a.RxPeak = s.RxBps
	}
	return a
}

// Record folds samples into the bucket rings. Safe for concurrent use.
func (s *Store) Record(samples []Sample) {
	if len(samples) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sm := range samples {
		if sm.Peer == "" {
			continue
		}
		ph, ok := s.peers[sm.Peer]
		if !ok {
			ph = &peerHist{}
			s.peers[sm.Peer] = ph
		}
		mTs := alignMinute(sm.At)
		hTs := alignHour(sm.At)
		dTs := alignDay(sm.At)
		// Roll forward each precision if bucket boundary crossed.
		if ph.CurM.Ts != 0 && ph.CurM.Ts != mTs {
			ph.M1 = commit(ph.M1, ph.CurM)
			ph.CurM = acc{}
		}
		if ph.CurH.Ts != 0 && ph.CurH.Ts != hTs {
			ph.H1 = commit(ph.H1, ph.CurH)
			ph.CurH = acc{}
		}
		if ph.CurD.Ts != 0 && ph.CurD.Ts != dTs {
			ph.D1 = commit(ph.D1, ph.CurD)
			ph.CurD = acc{}
		}
		ph.CurM = updateAcc(ph.CurM, sm, mTs)
		ph.CurH = updateAcc(ph.CurH, sm, hTs)
		ph.CurD = updateAcc(ph.CurD, sm, dTs)
	}
}

// flushCurrent commits in-flight accumulators as their newest bucket so
// the snapshot includes "right now". Caller holds s.mu.
func flushCurrent(ph *peerHist) Snapshot {
	out := Snapshot{
		M1: append([]Bucket(nil), ph.M1...),
		H1: append([]Bucket(nil), ph.H1...),
		D1: append([]Bucket(nil), ph.D1...),
	}
	if ph.CurM.Total > 0 {
		out.M1 = commit(out.M1, ph.CurM)
	}
	if ph.CurH.Total > 0 {
		out.H1 = commit(out.H1, ph.CurH)
	}
	if ph.CurD.Total > 0 {
		out.D1 = commit(out.D1, ph.CurD)
	}
	return out
}

// Snapshot returns a defensive copy for one peer; empty if unknown.
func (s *Store) Snapshot(peer string) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	ph, ok := s.peers[peer]
	if !ok {
		return Snapshot{}
	}
	return flushCurrent(ph)
}

// SnapshotAll returns the full read-only map for the API response.
func (s *Store) SnapshotAll() map[string]Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Snapshot, len(s.peers))
	for k, ph := range s.peers {
		out[k] = flushCurrent(ph)
	}
	return out
}

// Forget removes data for peers no longer present. names is the
// authoritative live set; anything outside it is dropped.
func (s *Store) Forget(keep map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.peers {
		if _, ok := keep[name]; !ok {
			delete(s.peers, name)
		}
	}
}

// gcOld discards buckets older than the retention horizon. Called after
// each Record so old entries fall off even if a peer stops receiving
// samples for a long time.
func (s *Store) gcOld(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mFloor := now.Add(-time.Hour).Unix()        // keep 1h of 1m
	hFloor := now.Add(-60 * time.Hour).Unix()   // keep 60h of 1h
	dFloor := now.Add(-60 * 24 * time.Hour).Unix() // keep 60d of 1d
	for _, ph := range s.peers {
		ph.M1 = trimOld(ph.M1, mFloor)
		ph.H1 = trimOld(ph.H1, hFloor)
		ph.D1 = trimOld(ph.D1, dFloor)
	}
}

func trimOld(ring []Bucket, floor int64) []Bucket {
	if len(ring) == 0 {
		return ring
	}
	// Sorted oldest→newest; binary-search-style trim.
	cut := sort.Search(len(ring), func(i int) bool { return ring[i].Ts >= floor })
	if cut == 0 {
		return ring
	}
	return append(ring[:0], ring[cut:]...)
}

// Persist writes the current state atomically. No-op if path is empty.
func (s *Store) Persist() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	data, err := json.Marshal(s.peers)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Load restores from disk. Missing file is non-fatal.
func (s *Store) Load() error {
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var peers map[string]*peerHist
	if err := json.Unmarshal(data, &peers); err != nil {
		// Treat corrupt file as cold start rather than aborting the process.
		return nil
	}
	s.mu.Lock()
	s.peers = peers
	if s.peers == nil {
		s.peers = make(map[string]*peerHist)
	}
	s.mu.Unlock()
	return nil
}

// MaybePersist saves if the persistInterval has elapsed since the last
// successful save. Cheap to call every tick.
func (s *Store) MaybePersist(now time.Time) {
	s.mu.Lock()
	due := now.Sub(s.lastPersist) >= s.persistInterval
	s.mu.Unlock()
	if !due {
		return
	}
	if err := s.Persist(); err == nil {
		s.mu.Lock()
		s.lastPersist = now
		s.mu.Unlock()
	}
}

// GC is the convenience wrapper exposed to the caller's tick loop.
func (s *Store) GC(now time.Time) { s.gcOld(now) }

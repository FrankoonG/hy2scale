package storage

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/hy2scale/core/logbuf"
)

// LogStore keeps log lines on disk (<data>/logs) behind the in-memory ring:
// every line the ring keeps is queued here and appended by a writer
// goroutine, so a slow disk never holds up logging (a full queue drops).
type LogStore struct {
	seg     *Segments
	ch      chan logbuf.Entry
	dropped atomic.Int64

	firstMu sync.Mutex
	first   map[string]uint64 // segment → its first seq

	wmu   sync.Mutex // a write vs. Wipe
	wiped uint64     // lines up to this seq are gone (under wmu)
}

// NewLogStore uses dir (<data>/logs).
func NewLogStore(dir string) *LogStore {
	return &LogStore{seg: OpenSegments(dir), ch: make(chan logbuf.Entry, 8192), first: map[string]uint64{}}
}

// Segments: the files.
func (s *LogStore) Segments() *Segments { return s.seg }

// Dropped: lines lost to a full queue since start.
func (s *LogStore) Dropped() int64 { return s.dropped.Load() }

// Sink is the logbuf sink: it queues e and never blocks.
func (s *LogStore) Sink(e logbuf.Entry) {
	select {
	case s.ch <- e:
	default:
		s.dropped.Add(1)
	}
}

// LastSeq is the sequence number of the newest line on disk (0 = none).
func (s *LogStore) LastSeq() uint64 {
	segs := s.seg.List()
	for i := len(segs) - 1; i >= 0; i-- {
		rl, err := openRev(s.seg.Path(segs[i].Name), -1)
		if err != nil {
			continue
		}
		line, _, ok := rl.next()
		rl.close()
		if !ok {
			continue
		}
		var e logbuf.Entry
		if json.Unmarshal(line, &e) == nil {
			return e.Seq
		}
	}
	return 0
}

// Run writes queued lines until ctx ends; prune runs at start and about
// once a minute.
func (s *LogStore) Run(ctx context.Context, prune func()) {
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
			s.drainShutdown()
			s.seg.Close()
			return
		case e := <-s.ch:
			s.write(e)
			dirty = true
		case <-flush.C:
			if dirty {
				s.seg.Flush()
				dirty = false
			}
		case <-tick.C:
			if prune != nil {
				prune()
			}
		}
	}
}

// drainShutdown keeps writing the lines logged while the node shuts down,
// until it has been quiet for a moment (at most 2 s).
func (s *LogStore) drainShutdown() {
	end := time.After(2 * time.Second)
	for {
		select {
		case e := <-s.ch:
			s.write(e)
		case <-time.After(300 * time.Millisecond):
			s.seg.Flush()
			return
		case <-end:
			s.Sync()
			return
		}
	}
}

// Sync writes everything queued now.
func (s *LogStore) Sync() {
	for {
		select {
		case e := <-s.ch:
			s.write(e)
		default:
			s.seg.Flush()
			return
		}
	}
}

func (s *LogStore) write(e logbuf.Entry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if e.Seq <= s.wiped {
		return
	}
	s.seg.Append(b, time.UnixMilli(e.TimeMs))
}

// Wipe deletes the stored lines up to seq (the newest one logged), also
// those still queued.
func (s *LogStore) Wipe(seq uint64) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if seq > s.wiped {
		s.wiped = seq
	}
	for {
		select {
		case <-s.ch:
			continue
		default:
		}
		break
	}
	s.firstMu.Lock()
	s.first = map[string]uint64{}
	s.firstMu.Unlock()
	return s.seg.Wipe()
}

func (s *LogStore) firstSeq(name string) (uint64, bool) {
	s.firstMu.Lock()
	v, ok := s.first[name]
	s.firstMu.Unlock()
	if ok {
		return v, true
	}
	line := firstLine(s.seg.Path(name))
	var e logbuf.Entry
	if line == nil || json.Unmarshal(line, &e) != nil {
		return 0, false
	}
	s.firstMu.Lock()
	if len(s.first) > 4096 {
		s.first = map[string]uint64{}
	}
	s.first[name] = e.Seq
	s.firstMu.Unlock()
	return e.Seq, true
}

// Before returns up to limit stored lines with Seq < before, oldest first,
// and whether older lines exist.
func (s *LogStore) Before(before uint64, limit int) ([]logbuf.Entry, bool) {
	if limit <= 0 {
		limit = 500
	}
	segs := s.seg.List()
	var out []logbuf.Entry // newest first while collecting
	more := false
	for i := len(segs) - 1; i >= 0 && !more; i-- {
		if fs, ok := s.firstSeq(segs[i].Name); ok && fs >= before {
			continue
		}
		rl, err := openRev(s.seg.Path(segs[i].Name), -1)
		if err != nil {
			continue
		}
		for {
			line, _, ok := rl.next()
			if !ok {
				break
			}
			var e logbuf.Entry
			if json.Unmarshal(line, &e) != nil || e.Seq >= before {
				continue
			}
			if len(out) == limit {
				more = true
				break
			}
			out = append(out, e)
		}
		rl.close()
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if out == nil {
		out = []logbuf.Entry{}
	}
	return out, more
}

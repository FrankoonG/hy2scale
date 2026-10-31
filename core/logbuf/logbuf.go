// Package logbuf keeps the most recent process log lines in memory for the
// web UI. It is an io.Writer meant to sit beside stderr behind log.SetOutput.
package logbuf

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Entry is one log line.
type Entry struct {
	Seq    uint64 `json:"seq"`
	TimeMs int64  `json:"ts"`    // unix milliseconds
	Level  string `json:"level"` // info | warn | error
	Source string `json:"src"`   // leading "[tag]" of the message, if any
	Msg    string `json:"msg"`
}

const (
	maxLine    = 4096
	maxPartial = 64 << 10
)

// Buffer is a bounded ring of entries; safe for concurrent use.
//
// Keeping can be switched off (SetEnabled): nothing is kept then. Until the
// first SetEnabled call the buffer keeps lines (start-up, before the
// configuration is read); switching off clears them.
type Buffer struct {
	mu      sync.Mutex
	ring    []Entry
	start   int // index of the oldest entry
	n       int
	seq     uint64
	partial []byte
	state   atomic.Int32 // stateBoot | stateOn | stateOff
	sink    func(Entry)  // also given every kept entry (under mu; must not block)
}

const (
	stateBoot int32 = iota
	stateOn
	stateOff
)

// New returns a buffer that keeps the last capacity lines.
func New(capacity int) *Buffer {
	if capacity < 1 {
		capacity = 1
	}
	return &Buffer{ring: make([]Entry, capacity)}
}

// Default is the process-wide buffer.
var Default = New(5000)

// SetEnabled switches keeping on or off. Off drops what is kept; on after
// start-up hands the lines kept so far to the sink.
func (b *Buffer) SetEnabled(on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	prev := b.state.Load()
	if !on {
		b.state.Store(stateOff)
		b.start, b.n, b.partial = 0, 0, nil
		for i := range b.ring {
			b.ring[i] = Entry{}
		}
		return
	}
	b.state.Store(stateOn)
	if prev == stateBoot && b.sink != nil {
		for i := 0; i < b.n; i++ {
			b.sink(b.ring[(b.start+i)%len(b.ring)])
		}
	}
}

// Enabled reports whether lines are kept (true during start-up).
func (b *Buffer) Enabled() bool { return b.state.Load() != stateOff }

// SetSink installs fn to receive every entry kept from now on (nil = none).
// fn runs under the buffer's lock and must not block.
func (b *Buffer) SetSink(fn func(Entry)) {
	b.mu.Lock()
	b.sink = fn
	b.mu.Unlock()
}

// SetSeq continues numbering after seq (the last entry kept on disk), so
// sequence numbers stay unique across restarts. During start-up the
// entries kept so far are renumbered to follow it; later it only moves the
// counter forward.
func (b *Buffer) SetSeq(seq uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Load() != stateBoot {
		if seq > b.seq {
			b.seq = seq
		}
		return
	}
	for i := 0; i < b.n; i++ {
		b.ring[(b.start+i)%len(b.ring)].Seq += seq
	}
	b.seq += seq
}

// Clear drops every kept entry (numbering continues).
func (b *Buffer) Clear() {
	b.mu.Lock()
	b.start, b.n, b.partial = 0, 0, nil
	for i := range b.ring {
		b.ring[i] = Entry{}
	}
	b.mu.Unlock()
}

// Last is the sequence number of the newest entry.
func (b *Buffer) Last() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// Oldest is the sequence number of the oldest kept entry (0 = none).
func (b *Buffer) Oldest() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.n == 0 {
		return 0
	}
	return b.ring[b.start].Seq
}

// Before returns up to limit entries with Seq < before, oldest first (the
// newest ones below before).
func (b *Buffer) Before(before uint64, limit int) []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Entry
	for i := b.n - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		e := b.ring[(b.start+i)%len(b.ring)]
		if e.Seq < before {
			out = append(out, e)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Write splits p into lines; an unterminated tail waits for the next write.
func (b *Buffer) Write(p []byte) (int, error) {
	if b.state.Load() == stateOff {
		return len(p), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	data := p
	if len(b.partial) > 0 {
		data = append(b.partial, p...)
		b.partial = nil
	}
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		b.add(string(data[:i]))
		data = data[i+1:]
	}
	if len(data) > 0 {
		if len(data) > maxPartial {
			b.add(string(data))
		} else {
			b.partial = append([]byte(nil), data...)
		}
	}
	return len(p), nil
}

var (
	stdPrefix = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})(\.\d+)? `)
	tagPrefix = regexp.MustCompile(`^\[([^\]\s]{1,40})\]\s*`)
	slogLevel = regexp.MustCompile(`\blevel=(DEBUG|INFO|WARN|ERROR)\b`)
)

func (b *Buffer) add(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	ts := time.Now()
	if m := stdPrefix.FindStringSubmatch(line); m != nil {
		if t, err := time.ParseInLocation("2006/01/02 15:04:05", m[1], time.Local); err == nil {
			ts = t
		}
		line = line[len(m[0]):]
	}
	if len(line) > maxLine {
		line = line[:maxLine] + "…"
	}
	src := ""
	if m := tagPrefix.FindStringSubmatch(line); m != nil {
		src = m[1]
	}
	b.seq++
	e := Entry{Seq: b.seq, TimeMs: ts.UnixMilli(), Level: levelOf(line), Source: src, Msg: line}
	idx := (b.start + b.n) % len(b.ring)
	if b.n == len(b.ring) {
		b.start = (b.start + 1) % len(b.ring)
	} else {
		b.n++
	}
	b.ring[idx] = e
	if b.sink != nil && b.state.Load() == stateOn {
		b.sink(e)
	}
}

func levelOf(line string) string {
	if m := slogLevel.FindStringSubmatch(line); m != nil {
		switch m[1] {
		case "ERROR":
			return "error"
		case "WARN":
			return "warn"
		}
		return "info"
	}
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "panic"), strings.Contains(l, "fatal"),
		strings.Contains(l, "error"), strings.Contains(l, "failed"), strings.Contains(l, " fail"):
		return "error"
	case strings.Contains(l, "warn"), strings.Contains(l, "timeout"), strings.Contains(l, "timed out"),
		strings.Contains(l, "retry"), strings.Contains(l, "disconnected"), strings.Contains(l, "refused"):
		return "warn"
	}
	return "info"
}

// Since returns up to limit entries with Seq > after, oldest first, plus the
// newest sequence number and whether entries after `after` were already
// overwritten.
func (b *Buffer) Since(after uint64, limit int) (out []Entry, last uint64, gap bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	last = b.seq
	if b.n == 0 {
		return []Entry{}, last, false
	}
	oldest := b.ring[b.start].Seq
	gap = after > 0 && after+1 < oldest
	// Skip to the first entry newer than after.
	skip := 0
	if after >= oldest {
		skip = int(after - oldest + 1)
	}
	avail := b.n - skip
	if avail <= 0 {
		return []Entry{}, last, gap
	}
	// With a limit, return the newest `limit` of what is available.
	if limit > 0 && avail > limit {
		skip += avail - limit
		avail = limit
		gap = true
	}
	out = make([]Entry, avail)
	for i := 0; i < avail; i++ {
		out[i] = b.ring[(b.start+skip+i)%len(b.ring)]
	}
	return out, last, gap
}

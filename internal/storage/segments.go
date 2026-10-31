// Package storage keeps this node's logs and audit records on disk:
// append-only JSON-lines segment files per kind, rotated by day and size and
// pruned by age and total size (Settings → Storage).
package storage

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const segExt = ".jsonl"

// Segments is a directory of append-only segment files named
// "YYYYMMDD-NNNNNN.jsonl" (local date of the first write, then a counter
// that grows across days), so names sort oldest first.
type Segments struct {
	dir string

	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	name   string // active segment
	day    string // its date
	size   int64  // its size, buffered bytes included
	next   int    // next counter
	segMax int64
}

// SegInfo describes one segment file.
type SegInfo struct {
	Name  string
	Size  int64
	MTime time.Time
}

// OpenSegments uses dir (created on first write).
func OpenSegments(dir string) *Segments {
	s := &Segments{dir: dir, segMax: 16 << 20}
	for _, si := range s.list() {
		if n := segCounter(si.Name); n >= s.next {
			s.next = n + 1
		}
	}
	return s
}

// Dir is the directory.
func (s *Segments) Dir() string { return s.dir }

// SetSegmentMax sets the size at which the active segment is rotated.
func (s *Segments) SetSegmentMax(n int64) {
	s.mu.Lock()
	s.segMax = n
	s.mu.Unlock()
}

// SegmentMaxFor picks a rotation size for a total budget: about an eighth of
// it, so pruning the oldest segment frees little at a time.
func SegmentMaxFor(total int64) int64 {
	n := total / 8
	if n < 64<<10 {
		n = 64 << 10
	}
	if n > 64<<20 {
		n = 64 << 20
	}
	return n
}

func segCounter(name string) int {
	base := strings.TrimSuffix(name, segExt)
	_, c, ok := strings.Cut(base, "-")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(c)
	if err != nil {
		return -1
	}
	return n
}

func validSegName(name string) bool {
	if !strings.HasSuffix(name, segExt) || len(name) < len("20060102-0")+len(segExt) {
		return false
	}
	return segCounter(name) >= 0
}

func (s *Segments) list() []SegInfo {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []SegInfo
	for _, e := range ents {
		if e.IsDir() || !validSegName(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, SegInfo{Name: e.Name(), Size: fi.Size(), MTime: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool {
		ci, cj := segCounter(out[i].Name), segCounter(out[j].Name)
		if ci != cj {
			return ci < cj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// List returns the segments, oldest first. Data still buffered for the
// active segment is flushed first so readers see whole lines.
func (s *Segments) List() []SegInfo {
	s.mu.Lock()
	if s.w != nil {
		s.w.Flush()
	}
	s.mu.Unlock()
	return s.list()
}

// Path of a segment.
func (s *Segments) Path(name string) string { return filepath.Join(s.dir, name) }

// Append writes one line (a newline is added) stamped at t.
func (s *Segments) Append(line []byte, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := t.Format("20060102")
	if s.f != nil && (day != s.day || s.size >= s.segMax) {
		s.closeLocked()
	}
	if s.f == nil {
		if err := os.MkdirAll(s.dir, 0o755); err != nil {
			return err
		}
		name := fmt.Sprintf("%s-%06d%s", day, s.next, segExt)
		f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		s.next++
		fi, _ := f.Stat()
		s.f, s.w, s.name, s.day, s.size = f, bufio.NewWriterSize(f, 64<<10), name, day, 0
		if fi != nil {
			s.size = fi.Size()
		}
	}
	if _, err := s.w.Write(line); err != nil {
		s.closeLocked()
		return err
	}
	s.w.WriteByte('\n')
	s.size += int64(len(line)) + 1
	return nil
}

// Flush writes buffered lines to the active segment.
func (s *Segments) Flush() {
	s.mu.Lock()
	if s.w != nil {
		s.w.Flush()
	}
	s.mu.Unlock()
}

func (s *Segments) closeLocked() {
	if s.w != nil {
		s.w.Flush()
	}
	if s.f != nil {
		s.f.Close()
	}
	s.f, s.w, s.name, s.day, s.size = nil, nil, "", "", 0
}

// Close flushes and closes the active segment.
func (s *Segments) Close() {
	s.mu.Lock()
	s.closeLocked()
	s.mu.Unlock()
}

// Used is the total size on disk.
func (s *Segments) Used() int64 {
	var n int64
	for _, si := range s.List() {
		n += si.Size
	}
	return n
}

// Prune deletes segments last written more than days ago, then the oldest
// ones while the total exceeds maxBytes. It returns how many it removed.
func (s *Segments) Prune(days int, maxBytes int64, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		s.w.Flush()
	}
	segs := s.list()
	var total int64
	for _, si := range segs {
		total += si.Size
	}
	cut := now.Add(-time.Duration(days) * 24 * time.Hour)
	removed := 0
	for _, si := range segs {
		old := days > 0 && si.MTime.Before(cut)
		over := maxBytes > 0 && total > maxBytes
		if !old && !over {
			break
		}
		if si.Name == s.name {
			s.closeLocked()
		}
		if err := os.Remove(filepath.Join(s.dir, si.Name)); err == nil || errors.Is(err, os.ErrNotExist) {
			total -= si.Size
			removed++
		}
	}
	return removed
}

// Wipe deletes every segment.
func (s *Segments) Wipe() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
	var first error
	for _, si := range s.list() {
		if err := os.Remove(filepath.Join(s.dir, si.Name)); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

// ---------------------------------------------------------------- reading

// revLines reads a file's lines backwards from an offset.
type revLines struct {
	f      *os.File
	bufOff int64  // file offset of buf[0]
	buf    []byte // file[bufOff : end] not yet returned
}

const revChunk = 64 << 10

// openRev starts reading name backwards from end (-1 = the end of the
// file, ignoring an unterminated last line).
func openRev(path string, end int64) (*revLines, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	trimTail := end < 0
	if end < 0 || end > fi.Size() {
		end = fi.Size()
	}
	r := &revLines{f: f, bufOff: end}
	if trimTail && end > 0 {
		// drop a line still being written
		if err := r.fill(); err != nil {
			f.Close()
			return nil, err
		}
		for len(r.buf) > 0 && r.buf[len(r.buf)-1] != '\n' {
			i := lastNL(r.buf)
			if i < 0 {
				if r.bufOff == 0 {
					r.buf = r.buf[:0]
					break
				}
				if err := r.fill(); err != nil {
					f.Close()
					return nil, err
				}
				continue
			}
			r.buf = r.buf[:i+1]
		}
	}
	return r, nil
}

func lastNL(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}

// fill prepends the previous chunk of the file to buf.
func (r *revLines) fill() error {
	if r.bufOff == 0 {
		return io.EOF
	}
	n := int64(revChunk)
	if n > r.bufOff {
		n = r.bufOff
	}
	chunk := make([]byte, n, int(n)+len(r.buf))
	if _, err := r.f.ReadAt(chunk, r.bufOff-n); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	r.buf = append(chunk, r.buf...)
	r.bufOff -= n
	return nil
}

// next returns the previous line (without its newline) and its offset.
func (r *revLines) next() ([]byte, int64, bool) {
	for {
		b := r.buf
		// strip the terminator of the line being returned
		end := len(b)
		if end > 0 && b[end-1] == '\n' {
			end--
		}
		if end == 0 && r.bufOff == 0 {
			r.buf = b[:0]
			return nil, 0, false
		}
		i := lastNL(b[:end])
		if i < 0 && r.bufOff > 0 {
			if err := r.fill(); err != nil {
				return nil, 0, false
			}
			continue
		}
		line := b[i+1 : end]
		off := r.bufOff + int64(i+1)
		r.buf = b[:i+1]
		if len(line) == 0 {
			continue // blank line
		}
		return line, off, true
	}
}

func (r *revLines) close() { r.f.Close() }

// firstLine reads the first line of a segment.
func firstLine(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 8<<10)
	line, err := br.ReadSlice('\n')
	if err != nil {
		return nil
	}
	return append([]byte(nil), line[:len(line)-1]...)
}

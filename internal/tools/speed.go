package tools

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/hy2scale/core/proto"
)

// Speed test between nodes over proto.StreamSpeed:
//
//	client → node: "HSP1", dir ('D' node sends, 'U' node receives), seconds
//	node → client: "HSP1"
//	down: zeros until the time is up, then the node closes
//	up:   the node discards what arrives and reports every 500 ms
//	      ('P' progress, 'F' final; elapsed µs and bytes, uint64 each)
const (
	speedMagic = "HSP1"
	// MaxSpeedSeconds caps a test, whatever the client asks.
	MaxSpeedSeconds = 30
	speedTick       = 500 * time.Millisecond
	speedChunk      = 32 << 10
)

// UserAgent of URL speed tests (the api package sets the version).
var UserAgent = "hy2scale"

// ErrSpeedUnsupported: the node answers the mesh but not speed tests.
var ErrSpeedUnsupported = errors.New("target does not support speed test")

// ServeSpeed serves one StreamSpeed stream (registered with the node).
func ServeSpeed(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	var h [6]byte
	if _, err := io.ReadFull(c, h[:]); err != nil || string(h[:4]) != speedMagic || h[4] != 'D' && h[4] != 'U' {
		return
	}
	secs := min(max(int(h[5]), 1), MaxSpeedSeconds)
	dur := time.Duration(secs) * time.Second
	start := time.Now()
	end := start.Add(dur)
	c.SetDeadline(end.Add(5 * time.Second))
	if _, err := c.Write([]byte(speedMagic)); err != nil {
		return
	}
	var n int64
	if h[4] == 'D' {
		buf := make([]byte, speedChunk)
		c.SetWriteDeadline(end)
		for time.Now().Before(end) {
			k, err := c.Write(buf)
			n += int64(k)
			if err != nil {
				break
			}
		}
	} else {
		n = speedSink(c, start, end)
	}
	log.Printf("[tools] speed test (%c, %ds) from %s: %d bytes", h[4], secs, c.RemoteAddr(), n)
}

// speedSink discards until end, reporting progress; returns the bytes.
func speedSink(c net.Conn, start, end time.Time) int64 {
	var got atomic.Int64
	go func() {
		buf := make([]byte, speedChunk)
		for {
			k, err := c.Read(buf)
			got.Add(int64(k))
			if err != nil {
				return
			}
		}
	}()
	report := func(kind byte, at time.Time) error {
		var f [17]byte
		f[0] = kind
		binary.BigEndian.PutUint64(f[1:], uint64(at.Sub(start)/time.Microsecond))
		binary.BigEndian.PutUint64(f[9:], uint64(got.Load()))
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := c.Write(f[:])
		return err
	}
	t := time.NewTicker(speedTick)
	defer t.Stop()
	fin := time.NewTimer(time.Until(end))
	defer fin.Stop()
	for {
		select {
		case now := <-t.C:
			if now.Before(end) && report('P', now) != nil {
				return got.Load()
			}
		case <-fin.C:
			report('F', end)
			return got.Load()
		}
	}
}

// StreamDialer opens a relay stream (addr) on the node under test.
type StreamDialer func(ctx context.Context, addr string) (net.Conn, error)

// ChainDialer opens a relay stream on the node at the end of chain.
type ChainDialer func(ctx context.Context, chain []string, addr string) (net.Conn, error)

// SpeedHandler serves GET /api/tools/speed (dial reaches mode=node targets).
func SpeedHandler(dial ChainDialer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		secs, err := intParam(q, "seconds", 10, 1, MaxSpeedSeconds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var run func(Emit) error
		switch q.Get("mode") {
		case "node":
			chain, err := checkChain(q.Get("chain"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			dir := q.Get("dir")
			if dir == "" {
				dir = "down"
			}
			if dir != "down" && dir != "up" {
				http.Error(w, "dir must be down or up", http.StatusBadRequest)
				return
			}
			run = func(emit Emit) error {
				if emit(Event{"type": "start", "mode": "node", "target": strings.Join(chain, "/"), "dir": dir}) != nil {
					return nil
				}
				d := func(ctx context.Context, addr string) (net.Conn, error) { return dial(ctx, chain, addr) }
				return SpeedNode(r.Context(), d, dir, secs, emit)
			}
		case "url":
			u, err := checkURL(q.Get("url"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			run = func(emit Emit) error {
				if emit(Event{"type": "start", "mode": "url", "target": u, "dir": "down"}) != nil {
					return nil
				}
				return SpeedURL(r.Context(), u, secs, emit)
			}
		default:
			http.Error(w, "mode must be node or url", http.StatusBadRequest)
			return
		}
		emit := ndjson(w)
		if err := run(emit); err != nil && r.Context().Err() == nil {
			emit(errEvent(err))
		}
	}
}

func checkChain(s string) ([]string, error) {
	s = strings.Trim(s, "/")
	if s == "" {
		return nil, fmt.Errorf("chain required")
	}
	if len(s) > 1024 {
		return nil, fmt.Errorf("chain too long")
	}
	parts := strings.Split(s, "/")
	if len(parts) > 16 {
		return nil, fmt.Errorf("chain too long")
	}
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("invalid chain")
		}
	}
	return parts, nil
}

func checkURL(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("url required")
	}
	if len(s) > 2048 {
		return "", fmt.Errorf("url too long")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be http(s)://host/...")
	}
	return u.String(), nil
}

// meter turns a byte count into progress / done lines. mbps is the average
// since the start, cur_mbps the rate over the last report interval.
type meter struct {
	emit      Emit
	lastAt    time.Duration
	lastBytes int64
}

func mbps(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return round3(float64(bytes) * 8 / d.Seconds() / 1e6)
}

func (m *meter) progress(elapsed time.Duration, bytes int64) error {
	cur := mbps(bytes-m.lastBytes, elapsed-m.lastAt)
	m.lastAt, m.lastBytes = elapsed, bytes
	return m.emit(Event{"type": "progress", "elapsed_ms": elapsed.Milliseconds(), "bytes": bytes,
		"mbps": mbps(bytes, elapsed), "cur_mbps": cur})
}

func (m *meter) done(elapsed time.Duration, bytes int64) error {
	return m.emit(Event{"type": "done", "bytes": bytes, "seconds": round3(elapsed.Seconds()), "mbps": mbps(bytes, elapsed)})
}

// SpeedNode measures throughput to (down) or from (up) the node dial
// reaches, for secs seconds.
func SpeedNode(ctx context.Context, dial StreamDialer, dir string, secs int, emit Emit) error {
	secs = min(max(secs, 1), MaxSpeedSeconds)
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, err := dial(dctx, proto.StreamSpeed)
	cancel()
	if err != nil {
		if ctx.Err() == nil && reachable(ctx, dial) {
			return ErrSpeedUnsupported
		}
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	d := byte('D')
	if dir == "up" {
		d = 'U'
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	var ack [4]byte
	_, err = c.Write([]byte{speedMagic[0], speedMagic[1], speedMagic[2], speedMagic[3], d, byte(secs)})
	if err == nil {
		_, err = io.ReadFull(c, ack[:])
	}
	if err != nil || string(ack[:]) != speedMagic {
		c.Close()
		if ctx.Err() == nil && reachable(ctx, dial) {
			return ErrSpeedUnsupported
		}
		if err == nil || err == io.EOF {
			err = fmt.Errorf("target closed the stream")
		}
		return err
	}
	c.SetDeadline(time.Now().Add(time.Duration(secs)*time.Second + 10*time.Second))
	m := &meter{emit: emit}
	if d == 'D' {
		return speedDown(ctx, c, secs, m)
	}
	return speedUp(ctx, c, m)
}

// reachable: the node answers StreamPing over the same path.
func reachable(ctx context.Context, dial StreamDialer) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := dial(ctx, proto.StreamPing)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	var b [1]byte
	_, err = io.ReadFull(c, b[:])
	return err == nil && b[0] == 1
}

func speedDown(ctx context.Context, c net.Conn, secs int, m *meter) error {
	start := time.Now()
	var got atomic.Int64
	var last atomic.Int64 // ns since start of the last byte
	errc := make(chan error, 1)
	go func() {
		buf := make([]byte, speedChunk)
		for {
			k, err := c.Read(buf)
			if k > 0 {
				got.Add(int64(k))
				last.Store(int64(time.Since(start)))
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	t := time.NewTicker(speedTick)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if m.progress(time.Since(start), got.Load()) != nil {
				return nil
			}
		case err := <-errc:
			if ctx.Err() != nil {
				return nil
			}
			el := time.Duration(last.Load())
			// the node ends the stream when its time is up; a break well
			// before that is a failure
			if err != io.EOF && el < time.Duration(secs)*time.Second-time.Second {
				return fmt.Errorf("stream broke after %.1fs: %v", el.Seconds(), err)
			}
			if el <= 0 {
				el = time.Since(start)
			}
			m.done(el, got.Load())
			return nil
		}
	}
}

func speedUp(ctx context.Context, c net.Conn, m *meter) error {
	go func() {
		buf := make([]byte, speedChunk)
		for {
			if _, err := c.Write(buf); err != nil {
				return
			}
		}
	}()
	var f [17]byte
	for {
		if _, err := io.ReadFull(c, f[:]); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("stream broke: %v", err)
		}
		el := time.Duration(binary.BigEndian.Uint64(f[1:])) * time.Microsecond
		n := int64(binary.BigEndian.Uint64(f[9:]))
		if f[0] == 'F' {
			c.Close() // stops the writer
			m.done(el, n)
			return nil
		}
		if m.progress(el, n) != nil {
			return nil
		}
	}
}

// SpeedURL downloads u for at most secs seconds (from the response
// headers on), counting body bytes.
func SpeedURL(ctx context.Context, u string, secs int, emit Emit) error {
	secs = min(max(secs, 1), MaxSpeedSeconds)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true // count wire bytes
	tr.ResponseHeaderTimeout = 15 * time.Second
	defer tr.CloseIdleConnections()
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	start := time.Now()
	limit := time.Duration(secs) * time.Second
	var up atomic.Bool
	end := time.AfterFunc(limit, func() { up.Store(true); cancel() })
	defer end.Stop()
	m := &meter{emit: emit}
	var got int64
	next := speedTick
	buf := make([]byte, speedChunk)
	for {
		k, err := resp.Body.Read(buf)
		got += int64(k)
		el := time.Since(start)
		if err != nil {
			switch {
			case parent.Err() != nil:
				return nil // the client went away
			case err != io.EOF && !up.Load():
				return fmt.Errorf("download broke after %.1fs: %v", el.Seconds(), err)
			}
			m.done(min(el, limit), got)
			return nil
		}
		if el >= next {
			if m.progress(el, got) != nil {
				return nil
			}
			next = el.Truncate(speedTick) + speedTick
		}
	}
}

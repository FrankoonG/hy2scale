package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"syscall"
	"time"
)

// PingOptions of one ping run. Probes go out one after another: each waits
// up to Timeout for its reply, the next leaves Interval after the previous.
type PingOptions struct {
	Host     string
	Count    int
	Interval time.Duration
	Timeout  time.Duration
	Size     int    // ICMP payload bytes
	Proto    string // "icmp" or "tcp" (TCP connect time)
	Port     int    // tcp
	Family   string // "auto", "4", "6"
}

// PingHandler serves GET /api/tools/ping.
func PingHandler(w http.ResponseWriter, r *http.Request) {
	o, err := pingOptions(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	emit := ndjson(w)
	if err := Ping(r.Context(), o, emit); err != nil && r.Context().Err() == nil {
		emit(errEvent(err))
	}
}

func pingOptions(r *http.Request) (PingOptions, error) {
	q := r.URL.Query()
	var o PingOptions
	var err error
	if o.Host, err = checkHost(q.Get("host")); err != nil {
		return o, err
	}
	if o.Count, err = intParam(q, "count", 4, 1, 1000); err != nil {
		return o, err
	}
	iv, err := intParam(q, "interval_ms", 1000, 200, 60000)
	if err != nil {
		return o, err
	}
	to, err := intParam(q, "timeout_ms", 2000, 100, 10000)
	if err != nil {
		return o, err
	}
	o.Interval, o.Timeout = time.Duration(iv)*time.Millisecond, time.Duration(to)*time.Millisecond
	if o.Family, err = checkFamily(q.Get("family")); err != nil {
		return o, err
	}
	switch o.Proto = q.Get("proto"); o.Proto {
	case "", "icmp":
		o.Proto = "icmp"
		if o.Size, err = intParam(q, "size", 56, 0, 1472); err != nil {
			return o, err
		}
	case "tcp":
		if q.Get("port") == "" {
			return o, fmt.Errorf("port required for tcp")
		}
		if o.Port, err = intParam(q, "port", 0, 1, 65535); err != nil {
			return o, err
		}
	default:
		return o, fmt.Errorf("proto must be icmp or tcp")
	}
	return o, nil
}

// pingStats accumulates round-trip times.
type pingStats struct {
	sent, recv          int
	min, max, sum, sum2 float64
}

func (s *pingStats) add(rtt float64) {
	if s.recv == 0 || rtt < s.min {
		s.min = rtt
	}
	if rtt > s.max {
		s.max = rtt
	}
	s.recv++
	s.sum += rtt
	s.sum2 += rtt * rtt
}

func (s *pingStats) done() Event {
	e := Event{"type": "done", "sent": s.sent, "recv": s.recv, "loss": 0.0,
		"min_ms": 0.0, "avg_ms": 0.0, "max_ms": 0.0, "mdev_ms": 0.0}
	if s.sent > 0 {
		e["loss"] = round3(float64(s.sent-s.recv) * 100 / float64(s.sent))
	}
	if s.recv > 0 {
		avg := s.sum / float64(s.recv)
		e["min_ms"], e["avg_ms"], e["max_ms"] = round3(s.min), round3(avg), round3(s.max)
		e["mdev_ms"] = round3(math.Sqrt(math.Max(0, s.sum2/float64(s.recv)-avg*avg)))
	}
	return e
}

// Ping runs o, streaming start / reply / timeout lines and a final done.
// A returned error is fatal (nothing more was sent).
func Ping(ctx context.Context, o PingOptions, emit Emit) error {
	dst, err := resolve(ctx, o.Host, o.Family)
	if err != nil {
		return err
	}
	var probe func(seq int) (Event, error)
	if o.Proto == "tcp" {
		probe = tcpProbe(ctx, dst, o)
	} else {
		s, err := openICMP(dst.IP.To4() == nil, false)
		if err != nil {
			return fmt.Errorf("ICMP socket: %s", sockReason(err))
		}
		defer s.Close()
		stop := context.AfterFunc(ctx, func() { s.c.SetReadDeadline(time.Now()) })
		defer stop()
		probe = icmpProbe(s, dst, o)
	}
	if err := emit(Event{"type": "start", "host": o.Host, "ip": dst.String(), "proto": o.Proto}); err != nil {
		return nil
	}
	var st pingStats
	for seq := 1; seq <= o.Count; seq++ {
		t0 := time.Now()
		st.sent++
		e, err := probe(seq)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if rtt, ok := e["rtt_ms"].(float64); ok {
			st.add(rtt)
		}
		if emit(e) != nil {
			return nil
		}
		if seq < o.Count && !sleepCtx(ctx, o.Interval-time.Since(t0)) {
			return nil
		}
	}
	emit(st.done())
	return nil
}

func icmpProbe(s *icmpSock, dst net.IPAddr, o PingOptions) func(int) (Event, error) {
	payload := make([]byte, o.Size)
	for i := range payload {
		payload[i] = byte(i)
	}
	buf := make([]byte, 65536)
	return func(seq int) (Event, error) {
		t0 := time.Now()
		if err := s.send(dst, seq, payload, 0); err != nil {
			if isTransient(err) {
				return Event{"type": "timeout", "seq": seq, "error": sockReason(err)}, nil
			}
			return nil, fmt.Errorf("send: %s", sockReason(err))
		}
		s.c.SetReadDeadline(t0.Add(o.Timeout))
		for {
			in, err := s.recv(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					return Event{"type": "timeout", "seq": seq}, nil
				}
				return nil, fmt.Errorf("receive: %s", sockReason(err))
			}
			if !in.echo || in.seq != seq&0xffff || !in.from.Equal(dst.IP) {
				continue
			}
			e := Event{"type": "reply", "seq": seq, "ip": in.from.String(), "rtt_ms": ms(in.at.Sub(t0)), "bytes": in.n}
			if in.ttl > 0 {
				e["ttl"] = in.ttl
			}
			return e, nil
		}
	}
}

func tcpProbe(ctx context.Context, dst net.IPAddr, o PingOptions) func(int) (Event, error) {
	addr := net.JoinHostPort(dst.String(), strconv.Itoa(o.Port))
	return func(seq int) (Event, error) {
		d := net.Dialer{Timeout: o.Timeout}
		t0 := time.Now()
		c, err := d.DialContext(ctx, "tcp", addr)
		rtt := time.Since(t0)
		if err != nil {
			e := Event{"type": "timeout", "seq": seq}
			var ne net.Error
			if !(errors.As(err, &ne) && ne.Timeout()) {
				e["error"] = sockReason(err)
			}
			return e, nil
		}
		c.Close()
		return Event{"type": "reply", "seq": seq, "ip": dst.String(), "rtt_ms": ms(rtt)}, nil
	}
}

// isTransient: a send error that loses this probe only (no route yet, a
// full buffer), not the run.
func isTransient(err error) bool {
	for _, e := range []error{syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ENOBUFS, syscall.EHOSTDOWN} {
		if errors.Is(err, e) {
			return true
		}
	}
	var se *os.SyscallError
	return errors.As(err, &se) && errors.Is(se.Err, syscall.EAGAIN)
}

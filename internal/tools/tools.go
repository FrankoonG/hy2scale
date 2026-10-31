// Package tools: network tests run from this node (ping, mtr, speed) and a
// terminal on it, served under /api/tools by internal/api.
//
// The streaming endpoints answer application/x-ndjson, one JSON object per
// line, flushed per line; a client stops a test by dropping the request.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Capability of one tool on this node; Reason says why it is unavailable.
type Capability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// PingCapability: ping works over TCP everywhere; ICMP needs a socket.
type PingCapability struct {
	Capability
	ICMP bool `json:"icmp"`
}

// Capabilities answers GET /api/tools/info.
type Capabilities struct {
	Ping     PingCapability `json:"ping"`
	MTR      Capability     `json:"mtr"`
	Speed    Capability     `json:"speed"`
	Terminal Capability     `json:"terminal"`
}

// Probe checks what this node can run now.
func Probe() Capabilities {
	var c Capabilities
	c.Ping.Available = true
	if s, err := openICMP(false, false); err == nil {
		s.Close()
		c.Ping.ICMP = true
	} else if s, err6 := openICMP(true, false); err6 == nil {
		s.Close()
		c.Ping.ICMP = true
	} else {
		c.Ping.Reason = "ICMP unavailable (" + sockReason(err) + "); TCP only"
	}
	if s, err := openICMP(false, true); err == nil {
		s.Close()
		c.MTR.Available = true
	} else if s, err6 := openICMP(true, true); err6 == nil {
		s.Close()
		c.MTR.Available = true
	} else {
		c.MTR.Reason = "raw ICMP socket unavailable (" + sockReason(err) + ")"
	}
	c.Speed.Available = true
	c.Terminal.Available, c.Terminal.Reason = terminalAvailable()
	return c
}

// InfoHandler serves GET /api/tools/info.
func InfoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Probe())
}

// Event is one NDJSON line.
type Event = map[string]any

// Emit sends one line; an error means the client is gone.
type Emit func(Event) error

// ndjson starts a streaming answer.
func ndjson(w http.ResponseWriter) Emit {
	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson")
	h.Set("Cache-Control", "no-cache, no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	return func(e Event) error {
		if err := enc.Encode(e); err != nil {
			return err
		}
		return rc.Flush()
	}
}

func errEvent(err error) Event { return Event{"type": "error", "error": err.Error()} }

// intParam reads q[name] (def when absent) within [lo, hi].
func intParam(q url.Values, name string, def, lo, hi int) (int, error) {
	s := q.Get(name)
	if s == "" {
		return def, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%s must be %d..%d", name, lo, hi)
	}
	return v, nil
}

func boolParam(q url.Values, name string) bool {
	switch strings.ToLower(q.Get(name)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// checkHost validates a host name or IP literal ("[v6]" accepted).
func checkHost(h string) (string, error) {
	h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(h), "["), "]")
	if h == "" {
		return "", fmt.Errorf("host required")
	}
	if len(h) > 253 {
		return "", fmt.Errorf("host too long")
	}
	for _, c := range h {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == '_' || c == ':' || c == '%'
		if !ok {
			return "", fmt.Errorf("invalid host")
		}
	}
	return h, nil
}

func checkFamily(f string) (string, error) {
	switch f {
	case "", "auto":
		return "auto", nil
	case "4", "6":
		return f, nil
	}
	return "", fmt.Errorf("family must be auto, 4 or 6")
}

// resolve picks the address of host to test: an IP literal as is, else the
// first of the family asked for (auto: IPv4 first).
func resolve(ctx context.Context, host, family string) (net.IPAddr, error) {
	ip, zone, _ := strings.Cut(host, "%")
	if a := net.ParseIP(ip); a != nil {
		v4 := a.To4() != nil
		if family == "4" && !v4 || family == "6" && v4 {
			return net.IPAddr{}, fmt.Errorf("%s is not an IPv%s address", host, family)
		}
		return net.IPAddr{IP: a, Zone: zone}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return net.IPAddr{}, fmt.Errorf("resolve %s: %v", host, unwrapDNS(err))
	}
	var v4, v6 []net.IPAddr
	for _, a := range addrs {
		if a.IP.To4() != nil {
			v4 = append(v4, a)
		} else {
			v6 = append(v6, a)
		}
	}
	switch {
	case family != "6" && len(v4) > 0:
		return v4[0], nil
	case family != "4" && len(v6) > 0:
		return v6[0], nil
	}
	return net.IPAddr{}, fmt.Errorf("resolve %s: no IPv%s address", host, map[string]string{"4": "4", "6": "6", "auto": "4/6"}[family])
}

func unwrapDNS(err error) error {
	if de, ok := err.(*net.DNSError); ok {
		if de.IsNotFound {
			return fmt.Errorf("no such host")
		}
		return fmt.Errorf("%s", de.Err)
	}
	return err
}

// ms: a duration in milliseconds, 3 decimals.
func ms(d time.Duration) float64 { return round3(float64(d) / float64(time.Millisecond)) }

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

// sleepCtx waits d unless ctx ends first (false).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

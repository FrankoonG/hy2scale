package app

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// adaptiveDialer implements Happy Eyeballs-style multi-path dialing.
// When exit_via ends with "*" (e.g., "C*"), it finds all available paths
// to the target node and races them, picking the fastest successful connection.
type adaptiveDialer struct {
	app *App
}

// pathScore tracks quality metrics for a specific path to a target.
type pathScore struct {
	path      string // e.g., "C" or "B/C"
	successes atomic.Int64
	failures  atomic.Int64
	totalRTT  atomic.Int64 // cumulative dial RTT in ms
	lastFail  atomic.Int64 // unix ms of last failure
}

func (ps *pathScore) score() float64 {
	s := ps.successes.Load()
	f := ps.failures.Load()
	total := s + f
	if total == 0 {
		return 50 // unknown = neutral score
	}
	successRate := float64(s) / float64(total) * 100
	// Penalize recent failures (within last 30s)
	now := time.Now().UnixMilli()
	if lf := ps.lastFail.Load(); lf > 0 && now-lf < 30000 {
		successRate *= 0.5 // halve score if failed recently
	}
	return successRate
}

func (ps *pathScore) avgRTT() time.Duration {
	s := ps.successes.Load()
	if s == 0 {
		return time.Hour // unknown = high
	}
	return time.Duration(ps.totalRTT.Load()/s) * time.Millisecond
}

var (
	adaptiveScores   sync.Map // "target:path" → *pathScore
	adaptivePathsMu  sync.Mutex
	adaptivePathsAge sync.Map // "target" → int64 (unix ms of last path discovery)
)

func getPathScore(target, path string) *pathScore {
	key := target + ":" + path
	v, _ := adaptiveScores.LoadOrStore(key, &pathScore{path: path})
	return v.(*pathScore)
}

// dialAdaptive finds all paths to the target node and races them.
func (a *App) dialAdaptive(ctx context.Context, target, addr string) (net.Conn, error) {
	paths := a.findPathsTo(target)
	if len(paths) == 0 {
		return nil, fmt.Errorf("adaptive: no paths to %s", target)
	}

	debugLog("[adaptive] %s: %d paths available: %v", target, len(paths), paths)

	// Sort paths by score (best first)
	sortPathsByScore(target, paths)

	// Happy Eyeballs: race all paths with staggered start
	// Best path starts immediately, others start after delays
	type result struct {
		conn net.Conn
		path string
		dur  time.Duration
		err  error
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	results := make(chan result, len(paths))
	var started int32

	for i, path := range paths {
		stagger := time.Duration(i) * 300 * time.Millisecond // 0ms, 300ms, 600ms...
		go func(p string, delay time.Duration) {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}
			// Skip if we already have a winner
			if atomic.LoadInt32(&started) > 0 && delay > 0 {
				// Check if a result came in during our stagger wait
				// Still try — Happy Eyeballs allows parallel attempts
			}

			start := time.Now()
			conn, err := a.dialPath(ctx, p, addr)
			dur := time.Since(start)

			ps := getPathScore(target, p)
			if err != nil {
				ps.failures.Add(1)
				ps.lastFail.Store(time.Now().UnixMilli())
				results <- result{nil, p, dur, err}
			} else {
				ps.successes.Add(1)
				ps.totalRTT.Add(dur.Milliseconds())
				atomic.StoreInt32(&started, 1)
				results <- result{conn, p, dur, nil}
			}
		}(path, stagger)
	}

	// Collect results: return first success, close others
	var firstConn net.Conn
	var firstPath string
	var lastErr error
	pending := len(paths)

	for pending > 0 {
		select {
		case r := <-results:
			pending--
			if r.err != nil {
				lastErr = r.err
				debugLog("[adaptive] %s path %s failed (%v): %v", target, r.path, r.dur, r.err)
				continue
			}
			if firstConn == nil {
				firstConn = r.conn
				firstPath = r.path
				debugLog("[adaptive] %s winner: path %s (%v)", target, r.path, r.dur)
				cancel() // cancel remaining attempts
			} else {
				// Close duplicate winners
				r.conn.Close()
			}
		case <-ctx.Done():
			if firstConn != nil {
				return firstConn, nil
			}
			return nil, fmt.Errorf("adaptive: all paths to %s failed (last: %v)", target, lastErr)
		}
	}

	if firstConn != nil {
		_ = firstPath
		return firstConn, nil
	}
	return nil, fmt.Errorf("adaptive: all %d paths to %s failed", len(paths), target)
}

// dialPath dials through a specific path string (e.g., "C" or "B/C").
func (a *App) dialPath(ctx context.Context, path, addr string) (net.Conn, error) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if len(parts) == 1 {
		return a.node.DialTCP(ctx, parts[0], addr)
	}
	return a.node.DialVia(ctx, parts, addr)
}

// findPathsTo discovers all available paths to a target node.
// Returns paths like ["C", "B/C", "D/E/C"] sorted shortest first.
func (a *App) findPathsTo(target string) []string {
	// Cache path discovery for 10 seconds
	if ts, ok := adaptivePathsAge.Load(target); ok {
		if time.Now().UnixMilli()-ts.(int64) < 10000 {
			// Return cached paths
			var cached []string
			adaptiveScores.Range(func(k, v any) bool {
				key := k.(string)
				if strings.HasPrefix(key, target+":") {
					cached = append(cached, strings.TrimPrefix(key, target+":"))
				}
				return true
			})
			if len(cached) > 0 {
				return cached
			}
		}
	}

	adaptivePathsMu.Lock()
	defer adaptivePathsMu.Unlock()

	var paths []string
	peers := a.node.Peers()

	// Direct path: check if target is a directly connected peer
	for _, p := range peers {
		if p.Name == target {
			paths = append(paths, target)
			break
		}
	}

	// Via intermediate peers: check their nested peers
	for _, p := range peers {
		if p.Name == target || p.Native {
			continue
		}
		// Query this peer's peers
		subPeers, err := a.node.PeersOf(p.Name)
		if err != nil {
			continue
		}
		for _, sp := range subPeers {
			if sp.Name == target {
				paths = append(paths, p.Name+"/"+target)
				break
			}
			// 3-hop: check sub-sub-peers (rare but possible)
			// Skip for now — 2 hops covers most cases
		}
	}

	adaptivePathsAge.Store(target, time.Now().UnixMilli())

	// Ensure all paths have score entries
	for _, p := range paths {
		getPathScore(target, p)
	}

	return paths
}

// sortPathsByScore sorts paths by their quality score (best first).
func sortPathsByScore(target string, paths []string) {
	// Simple insertion sort (usually <5 paths)
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0; j-- {
			sA := getPathScore(target, paths[j]).score()
			sB := getPathScore(target, paths[j-1]).score()
			if sA > sB {
				paths[j], paths[j-1] = paths[j-1], paths[j]
			}
		}
	}
}

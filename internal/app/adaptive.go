package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// adaptiveDialer implements Happy Eyeballs-style multi-path dialing.
// When exit_via ends with "*" (e.g., "C*"), it finds all available paths
// to the target node and races them, picking the fastest successful connection.

// pathScore tracks quality metrics using exponential moving average.
// Recent results have much higher weight than old ones.
type pathScore struct {
	mu        sync.Mutex
	path      string
	emaRTT    float64 // EMA of successful dial RTT in ms (lower = better)
	emaOK     float64 // EMA of success rate (0.0-1.0, higher = better)
	samples   int     // total samples seen
	lastOK    int64   // unix ms of last success
	lastFail  int64   // unix ms of last failure
}

const emaAlpha = 0.4 // Higher = more weight on recent samples (0.4 = last ~3 samples dominate)

func (ps *pathScore) recordSuccess(rtt time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ms := float64(rtt.Milliseconds())
	if ps.samples == 0 {
		ps.emaRTT = ms
		ps.emaOK = 1.0
	} else {
		ps.emaRTT = emaAlpha*ms + (1-emaAlpha)*ps.emaRTT
		ps.emaOK = emaAlpha*1.0 + (1-emaAlpha)*ps.emaOK
	}
	ps.samples++
	ps.lastOK = time.Now().UnixMilli()
}

func (ps *pathScore) recordFailure() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.samples == 0 {
		ps.emaOK = 0.0
		ps.emaRTT = 99999
	} else {
		ps.emaOK = emaAlpha*0.0 + (1-emaAlpha)*ps.emaOK
		ps.emaRTT = emaAlpha*99999 + (1-emaAlpha)*ps.emaRTT
	}
	ps.samples++
	ps.lastFail = time.Now().UnixMilli()
}

// score returns a composite quality score (higher = better).
// Considers: success rate (70%) + RTT (30%).
func (ps *pathScore) score() float64 {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.samples == 0 {
		return 50 // unknown = neutral
	}
	// Success rate component (0-70)
	okScore := ps.emaOK * 70
	// RTT component (0-30): 0ms=30, 500ms=15, 2000ms+=0
	rttScore := 30.0
	if ps.emaRTT > 0 {
		rttScore = 30.0 * (1.0 - min(ps.emaRTT/2000.0, 1.0))
	}
	return okScore + rttScore
}

func (ps *pathScore) isHealthy() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.emaOK > 0.3 // consider healthy if >30% success rate
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

	debugLog("[adaptive] %s: %d paths: %v", target, len(paths), paths)

	// Sort paths by score (best first)
	sortPathsByScore(target, paths)

	// Dynamic stagger: if best path has much higher score, others wait longer
	bestScore := getPathScore(target, paths[0]).score()

	type result struct {
		conn net.Conn
		path string
		dur  time.Duration
		err  error
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	results := make(chan result, len(paths))
	var won int32

	for i, path := range paths {
		// Stagger: best path starts immediately
		// Others get stagger based on score gap
		var stagger time.Duration
		if i > 0 {
			ps := getPathScore(target, path)
			gap := bestScore - ps.score()
			if gap < 5 {
				stagger = 100 * time.Millisecond // scores very close: race almost together
			} else if gap < 20 {
				stagger = 200 * time.Millisecond
			} else {
				stagger = 400 * time.Millisecond // big gap: give best path more time
			}
			stagger *= time.Duration(i) // multiply by position
		}

		go func(p string, delay time.Duration) {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}

			start := time.Now()
			conn, err := a.dialPath(ctx, p, addr)
			dur := time.Since(start)

			ps := getPathScore(target, p)
			if err != nil {
				ps.recordFailure()
				if atomic.LoadInt32(&won) == 0 {
					debugLog("[adaptive] %s path %s failed (%v): %v", target, p, dur, err)
				}
				results <- result{nil, p, dur, err}
			} else {
				ps.recordSuccess(dur)
				atomic.StoreInt32(&won, 1)
				results <- result{conn, p, dur, nil}
			}
		}(path, stagger)
	}

	// Collect results: return first success
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
				continue
			}
			if firstConn == nil {
				firstConn = r.conn
				firstPath = r.path
				debugLog("[adaptive] %s → %s (%v, score=%.0f)", target, r.path, r.dur, getPathScore(target, r.path).score())
				cancel() // cancel remaining
			} else {
				r.conn.Close() // close duplicate
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

// dialPath dials through a specific path string.
func (a *App) dialPath(ctx context.Context, path, addr string) (net.Conn, error) {
	return a.dialPathIdx(ctx, path, addr, -1)
}

// dialPathIdx dials with a specific QUIC client index (for bond path pinning).
func (a *App) dialPathIdx(ctx context.Context, path, addr string, clientIdx int) (net.Conn, error) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if len(parts) == 1 {
		return a.node.DialTCPIdx(ctx, parts[0], addr, clientIdx)
	}
	return a.node.DialVia(ctx, parts, addr)
}

// isPathAuthorised returns true when every qualified prefix along `parts`,
// from the direct peer up to but NOT including the final segment (which is
// the exit itself), has `nested=true` in cfg.Peers. This is the iron-rule-2
// predicate: a single nested=false at any intermediate hop makes the path
// unusable, mirroring the UI's filterChildrenByNestedConfig recursion.
// A single-segment path (direct peer target) is always authorised — nested
// gates sub-exits, not the direct peer itself.
func isPathAuthorised(parts []string, cfg Config) bool {
	for i := 1; i < len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		pc, ok := cfg.Peers[prefix]
		if !ok || !pc.Nested {
			return false
		}
	}
	return true
}

// findPathsTo discovers all available paths to a target node.
func (a *App) findPathsTo(target string) []string {
	// Cache path discovery for 5 seconds
	if ts, ok := adaptivePathsAge.Load(target); ok {
		if time.Now().UnixMilli()-ts.(int64) < 5000 {
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
	cfg := a.store.Get()
	seen := make(map[string]bool) // avoid duplicate paths

	// Direct path — multiply by number of QUIC connections to target
	for _, p := range peers {
		if p.Name == target {
			k := a.node.PeerConnCount(target)
			if k < 1 {
				k = 1
			}
			for i := 0; i < k; i++ {
				paths = append(paths, target)
			}
			seen[target] = true
			break
		}
	}

	// Via intermediate peers (depth 1): check direct peers' sub-peers.
	// Rule 2: intermediate must have nested=true on our side.
	for _, p := range peers {
		if p.Name == target || p.Native {
			continue
		}
		if !isPathAuthorised([]string{p.Name, target}, cfg) {
			continue
		}
		subPeers, err := a.node.PeersOf(p.Name)
		if err != nil {
			continue
		}
		for _, sp := range subPeers {
			if sp.Name == target {
				pathStr := p.Name + "/" + target
				if !seen[pathStr] {
					seen[pathStr] = true
					k := a.node.PeerConnCount(p.Name)
					if k < 1 {
						k = 1
					}
					for i := 0; i < k; i++ {
						paths = append(paths, pathStr)
					}
				}
			}
		}
	}

	// Via intermediate peers (depth 2+): BFS over cached peer lists.
	// Rule 2: every intermediate hop along the resulting path needs
	// nested=true on our side. Rule 1: the descent drops any sub-peer
	// whose name already appears on the current chain (no path-local
	// cycles). Seed the queue only with direct peers whose nested is
	// enabled — otherwise nothing beneath them can legitimately count.
	type bfsItem struct {
		chain []string // peer names from hub
	}
	bfsQueue := []bfsItem{}
	for _, p := range peers {
		if p.Name == target || p.Native {
			continue
		}
		if pc, ok := cfg.Peers[p.Name]; !ok || !pc.Nested {
			continue
		}
		bfsQueue = append(bfsQueue, bfsItem{chain: []string{p.Name}})
	}
	for len(bfsQueue) > 0 {
		item := bfsQueue[0]
		bfsQueue = bfsQueue[1:]
		if len(item.chain) > 4 {
			continue
		}
		lastPeer := item.chain[len(item.chain)-1]
		subPeers, ok := a.node.PeersOfCached(lastPeer)
		if !ok {
			continue
		}
		for _, sp := range subPeers {
			if sp.Name == target {
				// Final admissibility: every ancestor prefix along the
				// full path must be nested-authorised on our side.
				fullPath := append([]string{}, item.chain...)
				fullPath = append(fullPath, target)
				if !isPathAuthorised(fullPath, cfg) {
					continue
				}
				pathStr := strings.Join(fullPath, "/")
				if !seen[pathStr] {
					seen[pathStr] = true
					k := a.node.PeerConnCount(item.chain[0])
					if k < 1 {
						k = 1
					}
					for i := 0; i < k; i++ {
						paths = append(paths, pathStr)
					}
				}
			} else if !sp.Native && len(item.chain) < 4 {
				// Rule 1: sub-peer name must not appear anywhere on the
				// chain (including the current node) — nor match our
				// local node_id / display name (self identity). Rule 2:
				// to descend further, the new qualified prefix must
				// also be nested=true on our side — otherwise paths
				// below it are not authorised and we don't need the
				// BFS state.
				cycle := sp.Name == a.node.Name() || sp.Name == cfg.NodeID
				for _, c := range item.chain {
					if c == sp.Name {
						cycle = true
						break
					}
				}
				if cycle {
					continue
				}
				newChain := make([]string, len(item.chain)+1)
				copy(newChain, item.chain)
				newChain[len(item.chain)] = sp.Name
				if !isPathAuthorised(append(append([]string{}, newChain...), "_sentinel"), cfg) {
					// isPathAuthorised checks every prefix up to but not
					// including the last segment. Using a sentinel final
					// token makes it check every prefix of newChain itself.
					continue
				}
				bfsQueue = append(bfsQueue, bfsItem{chain: newChain})
			}
		}
	}

	log.Printf("[findPathsTo] %s: found %d paths: %v (cache keys: %v)", target, len(paths), paths, func() []string {
		var keys []string
		for _, p := range peers {
			if _, ok := a.node.PeersOfCached(p.Name); ok {
				keys = append(keys, p.Name)
			}
		}
		return keys
	}())
	adaptivePathsAge.Store(target, time.Now().UnixMilli())
	for _, p := range paths {
		getPathScore(target, p)
	}
	return paths
}

// sortPathsByScore sorts paths best-first.
func sortPathsByScore(target string, paths []string) {
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

// dialLoadBalance distributes connections across all healthy paths (round-robin).
// Each new TCP connection goes to the next healthy path, maximizing aggregate throughput.
func (a *App) dialLoadBalance(ctx context.Context, target, addr string) (net.Conn, error) {
	paths := a.findPathsTo(target)
	if len(paths) == 0 {
		return nil, fmt.Errorf("loadbalance: no paths to %s", target)
	}

	// Filter to healthy paths only
	var healthy []string
	for _, p := range paths {
		ps := getPathScore(target, p)
		if ps.isHealthy() || ps.samples == 0 {
			healthy = append(healthy, p)
		}
	}
	if len(healthy) == 0 {
		healthy = paths // fallback: try all if none healthy
	}

	// Round-robin selection
	idx := lbCounter.Add(1)
	selected := healthy[int(idx)%len(healthy)]

	debugLog("[loadbalance] %s: %d healthy paths, selected %s (rr #%d)", target, len(healthy), selected, idx)

	conn, err := a.dialPath(ctx, selected, addr)
	if err != nil {
		ps := getPathScore(target, selected)
		ps.recordFailure()
		// Fallback: try adaptive (race all paths)
		debugLog("[loadbalance] %s path %s failed, falling back to adaptive", target, selected)
		return a.dialAdaptive(ctx, target, addr)
	}

	ps := getPathScore(target, selected)
	ps.recordSuccess(0) // RTT not measured in round-robin mode
	return conn, nil
}

var lbCounter atomic.Int64

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

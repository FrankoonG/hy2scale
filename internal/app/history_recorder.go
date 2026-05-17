package app

import (
	"context"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/internal/history"
)

// runHistoryRecorder ticks every 5 s, walks every known qualified path
// (cfg.Peers keys plus the unique paths in cfg.Users / cfg.Proxies
// ExitPaths), aggregates each path's per-tick metrics, and folds the
// observation into the bucket store. Once per minute it triggers an
// atomic snapshot to dataDir/history.json so the rings survive process
// restart. Old data beyond the retention horizon is pruned each tick.
//
// Metrics per path (qualified, no self prefix):
//
//   latencyMs — sum of per-hop latencies along the chain. Hop 1 uses
//               the local node's own ping of the direct peer; hops 2+
//               read PeersOfCached(prevHop) and find the next hop by
//               name, picking up *that* hop's reported view of its own
//               next hop's latency. A missing hop means we don't have
//               cache yet → online = false for the bucket.
//   online    — every hop on the chain has latencyMs ≥ 0. One broken
//               segment anywhere = 0% online for the bucket.
//   txPeak    — per-path rate from Node.PathRates() (already keyed by
//               qualified path; for a 1-hop path falls back to the
//               direct peer's rate so single-hop entries match what the
//               old per-peer recorder used to capture).
//   rxPeak    — same.
//
// Deeper than 2 hops past self: hop ≥ 3 needs cache keyed by qualified
// path (parent/child), which the relay only keeps 1-level. Those buckets
// are marked offline rather than fabricating data — operators can read
// it as "we don't measure this segment yet". The API layer's
// subPeersCache covers deeper paths but is intentionally not used here
// to keep the recorder self-contained.
func (a *App) runHistoryRecorder(ctx context.Context) {
	if a.hist == nil {
		return
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	flush := time.NewTicker(60 * time.Second)
	defer flush.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = a.hist.Persist()
			return
		case now := <-tick.C:
			a.recordOnce(now)
		case now := <-flush.C:
			a.hist.GC(now)
			a.hist.MaybePersist(now)
		}
	}
}

// recordOnce gathers a Sample for every known qualified path and
// pushes the batch into the history store.
func (a *App) recordOnce(now time.Time) {
	cfg := a.store.Get()
	rates := a.node.PeerRates()
	pathRates := a.node.PathRates()

	paths := a.collectPaths(cfg)
	if len(paths) == 0 {
		return
	}
	samples := make([]history.Sample, 0, len(paths))
	keep := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		hops := strings.Split(p, "/")
		latMs, online := a.pathLatencyAndOnline(hops)
		var tx, rx int64
		if pr, ok := pathRates[p]; ok {
			tx = int64(pr.TxRate)
			rx = int64(pr.RxRate)
		} else if len(hops) == 1 {
			// 1-hop paths share the direct peer's rate.
			if r, ok := rates[hops[0]]; ok {
				tx = int64(r.TxRate)
				rx = int64(r.RxRate)
			}
		}
		samples = append(samples, history.Sample{
			Peer:      p,
			At:        now,
			LatencyMs: latMs,
			TxBps:     tx,
			RxBps:     rx,
		})
		keep[p] = struct{}{}
		if !online {
			// Online% comes from per-bucket counts: a sample with
			// negative latency is counted as offline, which is exactly
			// what we want for a partially-broken chain.
		}
	}
	a.hist.Record(samples)
	a.hist.Forget(keep)
}

// collectPaths returns the de-duplicated set of qualified paths the
// operator cares about: every cfg.Peers key (those are already
// qualified) plus the self-stripped form of every ExitPaths entry on
// users and proxies. The local node id and the legacy name field are
// recognised as "self" prefixes.
//
// Topology-visible deep paths (the slash-joined keys the api server's
// walkAndCache writes into relay.peersOfCache when it walks each direct
// peer's nested tree) are unioned in too. Operators who only toggle
// nested on the direct peer — leaving the deeper qualified-path keys
// unset in cfg.Peers — used to see the graph render those deep paths
// (AUB/tz-cm-temp/cn-shandong is the canonical example) but every
// PathInfoExpand history bar came back empty, because the recorder's
// roots came exclusively from cfg.Peers. Pulling from peersOfCache as
// well closes that gap without forcing operators to click every
// generated sub-path. Filter back below: the path is only recorded if
// it descends from a cfg.Peers root whose own .Nested is true, so we
// don't record sub-trees of disabled peers.
func (a *App) collectPaths(cfg Config) []string {
	selfPrefix := cfg.NodeID + "/"
	legacySelf := cfg.Name + "/"
	set := make(map[string]struct{})
	for k := range cfg.Peers {
		if k == "" {
			continue
		}
		set[k] = struct{}{}
	}
	// Union in topology-visible deep paths whose root direct-peer has
	// .Nested = true in cfg.Peers. This catches paths the api server's
	// deep walk discovered (subPeersCache → relay.peersOfCache) but the
	// operator never explicitly toggled.
	//
	// peersOfCache is keyed by the *parent* qualified path; the entry
	// value is the list of *direct children*. The leaf qualified path
	// for each child is therefore parentKey + "/" + child.Name, which
	// is what the recorder + history rings index by. Iterating keys
	// alone only yields parents (and parent==direct-peer for level-1
	// entries, which cfg.Peers already covers), so the bug-fix value
	// shows up only after we expand to leaves.
	//
	// Two filters keep this bounded:
	//   1. Rule 1 (docs/nested-rules.md) — no node may appear twice on
	//      the same path. Walks the leaf's components and drops it if
	//      any duplicate is detected. Without this, the api server's
	//      deep walk can hand us cyclic keys (production AUB showed
	//      e.g. tz-cm-temp/cn-shandong/tz-cm-temp from a remote node
	//      reporting its own ancestors back). Recording those would
	//      pollute the rings with paths the routing layer would
	//      refuse anyway.
	//   2. Depth cap — operator-meaningful paths are 2-5 hops in
	//      practice; deeper chains add little signal and lots of
	//      rings. We cap at the standard maxDepth (default 8) so a
	//      single direct peer can't explode the record store.
	const maxLeafDepth = 8
	for _, parent := range a.node.PeersOfCacheKeys() {
		if parent == "" {
			continue
		}
		parts := strings.Split(parent, "/")
		root := parts[0]
		// Only enroll deep paths that descend from a nested-enabled
		// root the operator has already authorised. Otherwise we'd
		// record paths from disabled / unauthorised peers too.
		if pc, ok := cfg.Peers[root]; !ok || !pc.Nested {
			continue
		}
		if len(parts) >= maxLeafDepth {
			continue
		}
		// Build the ancestor set once per parent for the cycle check,
		// AND skip the parent entirely if it is itself cyclic — without
		// this guard, peersOfCache parents like
		//   2400/cn-shandong/cn-xinchang/2400
		// (cn-shandong/cn-xinchang re-entering 2400) would still produce
		// non-trivially-cyclic leaves whose immediate child happens not
		// to collide with the immediate ancestor set. Rejecting cyclic
		// parents up front is the cheaper invariant: a non-cyclic
		// ancestor map paired with the dup-child check is then sufficient
		// to keep every enrolled leaf rule-1 clean.
		ancestors := make(map[string]struct{}, len(parts)+1)
		ancestors[cfg.NodeID] = struct{}{}
		if cfg.Name != "" {
			ancestors[cfg.Name] = struct{}{}
		}
		cyclicParent := false
		for _, h := range parts {
			if _, dup := ancestors[h]; dup {
				cyclicParent = true
				break
			}
			ancestors[h] = struct{}{}
		}
		if cyclicParent {
			continue
		}
		children, hasCache := a.node.PeersOfCached(parent)
		if !hasCache {
			continue
		}
		for _, c := range children {
			if c.Name == "" {
				continue
			}
			if _, dup := ancestors[c.Name]; dup {
				continue // Rule 1: would form a cycle
			}
			leaf := parent + "/" + c.Name
			set[leaf] = struct{}{}
		}
	}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if cfg.NodeID != "" && strings.HasPrefix(p, selfPrefix) {
			p = p[len(selfPrefix):]
		} else if cfg.Name != "" && strings.HasPrefix(p, legacySelf) {
			p = p[len(legacySelf):]
		}
		if p == "" {
			return
		}
		set[p] = struct{}{}
	}
	for _, u := range cfg.Users {
		for _, p := range u.ExitPaths {
			add(p)
		}
		if u.ExitVia != "" {
			add(u.ExitVia)
		}
	}
	for _, prx := range cfg.Proxies {
		for _, p := range prx.ExitPaths {
			add(p)
		}
		if prx.ExitVia != "" {
			add(prx.ExitVia)
		}
	}
	for _, r := range cfg.Rules {
		for _, p := range r.ExitPaths {
			add(p)
		}
		if r.ExitVia != "" {
			add(r.ExitVia)
		}
	}
	if cfg.WireGuard != nil {
		for _, wp := range cfg.WireGuard.Peers {
			for _, p := range wp.ExitPaths {
				add(p)
			}
			if wp.ExitVia != "" {
				add(wp.ExitVia)
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// pathLatencyAndOnline walks a qualified path and returns cumulative
// latency in ms plus a boolean indicating every hop reported a valid
// latency. Returns (-1, false) the moment a hop can't be resolved or
// reports offline.
func (a *App) pathLatencyAndOnline(hops []string) (int, bool) {
	if len(hops) == 0 {
		return -1, false
	}
	// Hop 1: local view of the direct peer.
	first := hops[0]
	cum := a.node.GetLatency(first)
	if cum < 0 {
		return -1, false
	}
	// Hops 2+: each ascends via the previous hop's cached peer list.
	for i := 1; i < len(hops); i++ {
		parent := hops[i-1]
		subs, ok := a.node.PeersOfCached(parent)
		if !ok {
			return -1, false
		}
		target := hops[i]
		var found bool
		for _, s := range subs {
			if s.Name == target {
				if s.LatencyMs < 0 {
					return -1, false
				}
				cum += s.LatencyMs
				found = true
				break
			}
		}
		if !found {
			return -1, false
		}
	}
	return cum, true
}

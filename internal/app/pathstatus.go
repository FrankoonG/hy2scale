package app

import (
	"sort"
	"strings"
)

// PathStatus says whether an exit path can be dialed now and, if not, which
// hop stops it. The UI colours exit paths from it alone (iron rule 2: a red
// hop is exactly a hop routing refuses), instead of judging reachability
// itself.
type PathStatus struct {
	Path string `json:"path"`
	OK   bool   `json:"ok"`
	// Hop indexes the path's segments as written (a leading self label
	// counts); meaningful only when !OK.
	Hop int `json:"hop"`
	// Reason when !OK: "loop", "nested", "disabled" or "offline".
	Reason string `json:"reason,omitempty"`
}

// PathStatus evaluates exitVia with the rules exitChain applies when it is
// dialed, plus the current reachability of every hop.
func (a *App) PathStatus(exitVia string) PathStatus {
	st := PathStatus{Path: exitVia, OK: true}
	parts := splitPath(exitVia)
	cfg := a.store.Get()
	off := 0
	if len(parts) > 0 && (parts[0] == a.node.Name() || parts[0] == cfg.NodeID) {
		parts, off = parts[1:], 1
	}
	fail := func(i int, reason string) PathStatus {
		st.OK, st.Hop, st.Reason = false, i+off, reason
		return st
	}
	seen := map[string]bool{a.node.Name(): true, cfg.NodeID: true}
	for i, h := range parts {
		if seen[h] {
			return fail(i, "loop")
		}
		seen[h] = true
	}
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		if i > 0 {
			if pc, ok := cfg.Peers[strings.Join(parts[:i], "/")]; !ok || !pc.Nested {
				return fail(i, "nested")
			}
		}
		if a.isExitPathDisabled(prefix) {
			return fail(i, "disabled")
		}
		if i == 0 {
			if a.node.HasPeer(parts[0]) {
				continue
			}
			// a lone non-direct exit is dialed through any intermediate
			// (dialExit → findPathsTo)
			if len(parts) == 1 && len(a.findPathsTo(parts[0])) > 0 {
				return st
			}
			return fail(0, "offline")
		}
		kids, _ := a.node.PeersOfCached(strings.Join(parts[:i], "/"))
		online := false
		for _, k := range kids {
			if k.Name == parts[i] {
				online = k.LatencyMs >= 0
				break
			}
		}
		if !online {
			return fail(i, "offline")
		}
	}
	return st
}

// ConfiguredExitPaths lists every exit path the configuration refers to.
func (a *App) ConfiguredExitPaths() []string {
	cfg := a.store.Get()
	set := map[string]bool{}
	add := func(via string, paths []string) {
		if via != "" {
			set[via] = true
		}
		for _, p := range paths {
			if p != "" {
				set[p] = true
			}
		}
	}
	for _, u := range cfg.Users {
		add(u.ExitVia, u.ExitPaths)
	}
	for _, r := range cfg.Rules {
		add(r.ExitVia, r.ExitPaths)
	}
	for _, p := range cfg.Proxies {
		add(p.ExitVia, p.ExitPaths)
	}
	if cfg.IKEv2 != nil {
		add(cfg.IKEv2.DefaultExit, cfg.IKEv2.DefaultExitPaths)
	}
	if cfg.WireGuard != nil {
		for _, p := range cfg.WireGuard.Peers {
			add(p.ExitVia, p.ExitPaths)
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// PathStatuses evaluates every configured exit path.
func (a *App) PathStatuses() []PathStatus {
	paths := a.ConfiguredExitPaths()
	out := make([]PathStatus, 0, len(paths))
	for _, p := range paths {
		out = append(out, a.PathStatus(p))
	}
	return out
}

package node

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/hy2scale/core/proto"
)

// Nested discovery: the peers behind the direct peers, learned from what
// each direct peer publishes in its list_peers reply (docs/nested-rules.md).
//
//   - Rule 1: no node twice on a path; self (node id) is always an ancestor.
//   - Rule 2: a path's children are fetched, cached and published only when
//     every hop down to it is nested-enabled.
//   - Rule 3: sub-peers of X come only from the direct peer whose cache
//     holds X — never by querying past a hop.

// TreePeer is one node of the nested peer tree, as carried in list_peers
// replies and cached per qualified path ("a/b/c").
type TreePeer struct {
	Name       string `json:"name"`
	ExitNode   bool   `json:"exit_node"`
	Direction  string `json:"direction"`
	Via        string `json:"via"`
	LatencyMs  int    `json:"latency_ms"`
	Nested     bool   `json:"nested"`
	Disabled   bool   `json:"disabled,omitempty"`
	Native     bool   `json:"native,omitempty"`
	Version    string `json:"version,omitempty"`
	TunCapable bool   `json:"tun_capable,omitempty"`
	// Conns: connections the parent holds to this node (several addresses)
	Conns int `json:"conns,omitempty"`
	// Punched (inbound children of self): who set up a live hole-punched
	// connection with this peer.
	Punched []string `json:"punched,omitempty"`
	TxRate  uint64   `json:"tx_rate"`
	RxRate  uint64   `json:"rx_rate"`
	// MaxRate is the configured peak of the link (bytes/sec), the larger of
	// the dialer's max tx/rx; 0 = unknown.
	MaxRate uint64 `json:"max_rate,omitempty"`
	// Egress: the node's egress (public address per family, null family =
	// not provided); null = unknown (older node).
	Egress json.RawMessage `json:"egress"`
	// EgressProbed: Egress was found by the viewing node itself (the node
	// does not report it); set for display only, never sent to peers.
	EgressProbed bool `json:"egress_probed,omitempty"`
	// Connected: false for a peer the parent has configured but is offline.
	// Old peers don't send it; absent is read as connected.
	Connected bool `json:"connected"`
	// Restricted: reached through node access by user (traffic only: no
	// management or punching), with everything below it.
	Restricted bool `json:"restricted,omitempty"`
	// Attrs: the node's attributes, carried unchanged.
	Attrs        proto.Attrs `json:"attrs,omitempty"`
	RestrictTags bool        `json:"rt,omitempty"`
	Children     []TreePeer  `json:"children,omitempty"`
}

// MarkRestricted marks a subtree as reached through node access by user.
func MarkRestricted(children []TreePeer) {
	for i := range children {
		children[i].Restricted = true
		MarkRestricted(children[i].Children)
	}
}

// PathConfig is the local setting for one (qualified) peer path.
type PathConfig struct {
	Nested   bool
	Disabled bool
}

// ConfiguredPeer is an outbound peer from the local configuration.
type ConfiguredPeer struct {
	Name    string
	MaxRate uint64 // bytes/sec, 0 = unknown
}

// TreeConfig is what the tree needs from the local configuration.
type TreeConfig struct {
	SelfID string
	// Paths: settings keyed by peer path ("a", "a/b", …).
	Paths map[string]PathConfig
	// Peers: configured outbound peers, published as offline while not
	// connected.
	Peers []ConfiguredPeer
}

func (c *TreeConfig) nested(path string) bool {
	p, ok := c.Paths[path]
	return ok && p.Nested
}

func (c *TreeConfig) disabled(path string) bool {
	p, ok := c.Paths[path]
	return ok && p.Disabled
}

// Authorised: every hop down to and including path is nested-enabled — the
// paths whose children are fetched and shown (rule 2).
func (c *TreeConfig) Authorised(path string) bool {
	parts := strings.Split(path, "/")
	for i := 1; i <= len(parts); i++ {
		if !c.nested(strings.Join(parts[:i], "/")) {
			return false
		}
	}
	return true
}

// TreeLimits are the hard caps of nested discovery.
type TreeLimits struct {
	MaxDepth         int
	MaxResponseNodes int
	MaxCacheEntries  int
	MaxResponseBytes int
	MaxFetchFanOut   int
}

// Clamp fills zero fields with the defaults and clamps each to its range.
func (l TreeLimits) Clamp() TreeLimits {
	clamp := func(v, def, max int) int {
		if v <= 0 {
			return def
		}
		if v > max {
			return max
		}
		return v
	}
	l.MaxDepth = clamp(l.MaxDepth, 5, 10)
	l.MaxResponseNodes = clamp(l.MaxResponseNodes, 1024, 65536)
	l.MaxCacheEntries = clamp(l.MaxCacheEntries, 5000, 100000)
	l.MaxResponseBytes = clamp(l.MaxResponseBytes, 1<<20, 16<<20)
	l.MaxFetchFanOut = clamp(l.MaxFetchFanOut, 8, 64)
	return l
}

// Tree keeps the nested peer tree of a node: it answers list_peers with the
// direct peers plus the authorised cached subtrees, and refreshes its cache
// from the direct peers' replies.
type Tree struct {
	n      *Node
	config func() TreeConfig

	mu     sync.RWMutex
	cache  map[string][]TreePeer // qualified path → flat children
	kick   chan struct{}
	limits atomic.Pointer[TreeLimits]
}

// NewTree serves list_peers for n from now on; Run keeps the cache fresh.
func NewTree(n *Node, config func() TreeConfig) *Tree {
	t := &Tree{n: n, config: config, cache: map[string][]TreePeer{}, kick: make(chan struct{}, 1)}
	t.SetLimits(TreeLimits{})
	n.SetListPeersFunc(t.listPeers)
	return t
}

// SetLimits installs new limits (clamped); they apply from the next use.
func (t *Tree) SetLimits(l TreeLimits) {
	l = l.Clamp()
	t.limits.Store(&l)
}

func (t *Tree) Limits() TreeLimits { return *t.limits.Load() }

// Kick asks for an immediate refresh.
func (t *Tree) Kick() {
	select {
	case t.kick <- struct{}{}:
	default:
	}
}

// Cached returns the flat children cached for a qualified path.
func (t *Tree) Cached(path string) []TreePeer {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cache[path]
}

// listPeers is the list_peers reply: the direct peers, each nested one with
// the subtree cached for it (rule 2), and configured outbound peers that
// are offline. Disabled peers are not published.
func (t *Tree) listPeers() []byte {
	peers := t.n.Peers()
	cfg := t.config()
	type peerWithChildren struct {
		PeerInfo
		Connected bool       `json:"connected"`
		MaxRate   uint64     `json:"max_rate,omitempty"`
		Children  []TreePeer `json:"children,omitempty"`
	}
	maxRate := make(map[string]uint64, len(cfg.Peers))
	for _, p := range cfg.Peers {
		maxRate[p.Name] = p.MaxRate
	}
	live := make(map[string]bool, len(peers))
	result := make([]peerWithChildren, 0, len(peers))
	for _, p := range peers {
		live[p.Name] = true
		if cfg.disabled(p.Name) {
			continue
		}
		pc := peerWithChildren{PeerInfo: p, Connected: true, MaxRate: maxRate[p.Name]}
		if cfg.nested(p.Name) {
			count := 0
			pc.Children = t.assemble(&cfg, p.Name, 0, &count)
		}
		result = append(result, pc)
	}
	for _, p := range cfg.Peers {
		if live[p.Name] || cfg.disabled(p.Name) {
			continue
		}
		result = append(result, peerWithChildren{
			PeerInfo: PeerInfo{Name: p.Name, Direction: "outbound"},
			MaxRate:  p.MaxRate,
		})
	}
	data, _ := json.Marshal(result)
	return data
}

// assemble builds the subtree under prefix from the cache, descending only
// into nested-enabled paths and skipping disabled ones. Latencies stay
// one-hop: the receiver adds its own distance. count is shared by siblings
// so one branch can't take the whole budget.
func (t *Tree) assemble(cfg *TreeConfig, prefix string, depth int, count *int) []TreePeer {
	lim := t.Limits()
	if depth >= lim.MaxDepth || *count >= lim.MaxResponseNodes {
		return nil
	}
	base := t.Cached(prefix)
	if len(base) == 0 {
		return nil
	}
	result := make([]TreePeer, 0, len(base))
	for _, c := range base {
		if *count >= lim.MaxResponseNodes {
			break
		}
		qkey := prefix + "/" + c.Name
		if cfg.disabled(qkey) {
			continue
		}
		*count++
		c.Children = nil
		if cfg.nested(qkey) {
			c.Children = t.assemble(cfg, qkey, depth+1, count)
		}
		result = append(result, c)
	}
	return result
}

// cacheTree stores every qualified path of a reply subtree flat, dropping
// ancestors (rule 1) and stopping at the depth and cache-size caps.
func (t *Tree) cacheTree(prefix string, children []TreePeer, ancestors map[string]bool, depth int, cache map[string][]TreePeer) {
	lim := t.Limits()
	if depth >= lim.MaxDepth || len(children) == 0 || len(cache) >= lim.MaxCacheEntries {
		return
	}
	flat := make([]TreePeer, 0, len(children))
	for _, c := range children {
		if ancestors[c.Name] {
			continue
		}
		c.Children = nil
		flat = append(flat, c)
	}
	if len(flat) == 0 {
		return
	}
	cache[prefix] = flat
	for _, c := range children {
		if ancestors[c.Name] || len(c.Children) == 0 {
			continue
		}
		ancestors[c.Name] = true
		t.cacheTree(prefix+"/"+c.Name, c.Children, ancestors, depth+1, cache)
		delete(ancestors, c.Name)
	}
}

// Run refreshes the cache every 7 s (or on Kick) until ctx ends.
func (t *Tree) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second): // offset from the prober
	}
	tk := time.NewTicker(7 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		case <-t.kick:
		}
		t.refresh()
	}
}

func (t *Tree) refresh() {
	cfg := t.config()
	native := t.n.NativeMap()
	// Rule 3: only direct peers are asked; deeper paths come embedded in
	// their replies.
	var names []string
	for _, name := range t.n.ConnectedPeerNames() {
		if !native[name] && !strings.Contains(name, "/") && cfg.nested(name) {
			names = append(names, name)
		}
	}
	type fetched struct {
		name     string
		children []TreePeer
	}
	ch := make(chan fetched, len(names))
	sem := make(chan struct{}, t.Limits().MaxFetchFanOut)
	for _, name := range names {
		sem <- struct{}{}
		go func(name string) {
			defer func() { <-sem }()
			ch <- fetched{name, t.fetch(name, cfg.SelfID)}
		}(name)
	}
	cache := make(map[string][]TreePeer)
	for range names {
		select {
		case r := <-ch:
			if r.children != nil {
				t.cacheTree(r.name, r.children, map[string]bool{cfg.SelfID: true, r.name: true}, 0, cache)
			}
		case <-time.After(6 * time.Second):
		}
	}
	t.mu.Lock()
	t.cache = cache
	t.mu.Unlock()
	t.feedPeersOf(&cfg, cache)
}

// feedPeersOf hands the authorised paths to the node's peersOf cache (path
// discovery, routing, history) under the qualified path and, for indirect
// peers, the bare name. Each bare name is expanded once per round so
// cycles can't multiply paths.
func (t *Tree) feedPeersOf(cfg *TreeConfig, cache map[string][]TreePeer) {
	visited := make(map[string]bool)
	var walk func(path string, children []TreePeer)
	walk = func(path string, children []TreePeer) {
		if !cfg.Authorised(path) {
			return
		}
		bare := path[strings.LastIndexByte(path, '/')+1:]
		if visited[bare] {
			return
		}
		visited[bare] = true
		var infos []PeerInfo
		for _, c := range children {
			// An offline hop is latency -1 for the path checks.
			lat := c.LatencyMs
			if !c.Connected {
				lat = -1
			}
			infos = append(infos, PeerInfo{
				Name:         c.Name,
				ExitNode:     c.ExitNode,
				Native:       c.Native,
				Version:      c.Version,
				TunCapable:   c.TunCapable,
				LatencyMs:    lat,
				Conns:        c.Conns,
				Egress:       c.Egress,
				Restricted:   c.Restricted,
				RestrictTags: c.RestrictTags,
				Attrs:        c.Attrs,
			})
		}
		if len(infos) > 0 {
			t.n.SetPeersOfCache(path, infos)
			// Direct peers' bare entries belong to the latency prober.
			if strings.Contains(path, "/") && !t.n.HasPeer(bare) {
				t.n.SetPeersOfCache(bare, infos)
			}
		}
		for _, c := range children {
			if visited[c.Name] {
				continue
			}
			if len(c.Children) > 0 {
				walk(path+"/"+c.Name, c.Children)
			} else if cached := cache[c.Name]; len(cached) > 0 {
				walk(path+"/"+c.Name, cached)
			}
		}
	}
	for name, children := range cache {
		walk(name, children)
	}
	// A node reached through several parents is written under one of them
	// per round; paths not written for an hour have left the topology.
	t.n.ExpirePeersOfCache(time.Hour)
}

// fetch asks direct peer name for its list_peers reply. Latencies become
// cumulative from self; the first grandchild level drops self and name
// (rule 1), deeper levels are left to cacheTree.
func (t *Tree) fetch(name, selfID string) []TreePeer {
	data, err := t.n.PeersOfRaw(name)
	if err != nil {
		return nil
	}
	if max := t.Limits().MaxResponseBytes; len(data) > max {
		log.Printf("[topology] %s listPeers payload %d B > %d cap — dropping", name, len(data), max)
		return nil
	}
	type remotePeer struct {
		TreePeer
		// Absent (older peers) = connected.
		Connected *bool `json:"connected,omitempty"`
	}
	var remote []remotePeer
	if err := json.Unmarshal(data, &remote); err != nil {
		return nil
	}
	parentLat := t.n.GetLatency(name)
	// Everything a gateway reached by node access shows is restricted too.
	viaAccess := t.n.IsRestricted(name)
	children := make([]TreePeer, 0, len(remote))
	for _, rp := range remote {
		c := rp.TreePeer
		c.Via = name
		c.Nested, c.Disabled, c.Punched, c.TxRate, c.RxRate, c.EgressProbed = false, false, nil, 0, 0, false
		if c.LatencyMs > 0 && parentLat > 0 {
			c.LatencyMs += parentLat
		}
		c.Connected = rp.Connected == nil || *rp.Connected
		if len(c.Children) > 0 {
			kept := make([]TreePeer, 0, len(c.Children))
			for _, gc := range c.Children {
				if gc.Name != selfID && gc.Name != name && gc.Name != c.Name {
					kept = append(kept, gc)
				}
			}
			c.Children = kept
		}
		if c.Egress == nil && c.Attrs != nil {
			c.Egress = c.Attrs[proto.AttrEgress]
		}
		c.Restricted = c.Restricted || viaAccess
		if c.Restricted {
			MarkRestricted(c.Children)
		}
		children = append(children, c)
	}
	return children
}

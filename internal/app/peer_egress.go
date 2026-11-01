package app

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/exit"
	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/core/proto"
	"github.com/FrankoonG/hy2scale/internal/pubip"
	"github.com/FrankoonG/hy2scale/internal/publicip"
)

// Probed egress: for a node that does not report its egress (releases
// before 1.4.0, or one behind a parent that does not carry it), this node
// finds it itself — the detection servers of the public-address settings
// are asked through that node as the exit, like user traffic leaving
// there, and the address they see is the node's egress. Only nodes routing
// could use as an exit are probed: the same predicate as the exit dial
// (docs/nested-rules.md: no loop, every hop before the node nested-enabled,
// none disabled). A node that reports is never probed, and its report
// replaces a probed value. Results are kept per node name in
// <data>/peer-egress.json.

const (
	peerEgressRound     = 15 * time.Second
	peerEgressRefresh   = 6 * time.Hour
	peerEgressRetryMin  = time.Minute
	peerEgressRetryMax  = time.Hour
	peerEgressReconnect = time.Minute      // a reconnect re-probes at most this often
	peerEgressGrace     = 90 * time.Second // a deeper node's parent may still be fetching its report
	peerEgressTimeout   = 45 * time.Second
	peerEgressKeep      = 30 * 24 * time.Hour
	peerEgressMax       = 512
	peerEgressParallel  = 2
	peerEgressMaxDepth  = 5
)

// peerEgress is one node's probe state; the exported fields are persisted.
type peerEgress struct {
	Egress  publicip.SelfEgress `json:"egress"`
	Path    string              `json:"path"`    // the path it was probed through
	Checked int64               `json:"checked"` // last successful probe (unix)
	Seen    int64               `json:"seen"`    // last time it was a probe candidate (unix)

	fails   int
	tried   time.Time
	next    time.Time
	since   time.Time // candidate (online, not reporting) since
	running bool
}

type peerEgressProber struct {
	a    *App
	file string
	ctx  context.Context
	kick chan struct{}
	sem  chan struct{}

	mu    sync.Mutex
	m     map[string]*peerEgress
	sig   string // the servers of the current probes
	dirty bool
}

func newPeerEgressProber(a *App) *peerEgressProber {
	pe := &peerEgressProber{a: a, ctx: context.Background(), kick: make(chan struct{}, 1),
		sem: make(chan struct{}, peerEgressParallel), m: map[string]*peerEgress{}}
	if a.dataDir != "" {
		pe.file = filepath.Join(a.dataDir, "peer-egress.json")
		pe.load()
	}
	return pe
}

func (pe *peerEgressProber) load() {
	b, err := os.ReadFile(pe.file)
	if err != nil {
		return
	}
	var m map[string]*peerEgress
	if json.Unmarshal(b, &m) != nil {
		return
	}
	for name, e := range m {
		if e == nil || e.Checked == 0 {
			continue
		}
		e.tried = time.Unix(e.Checked, 0)
		e.next = e.tried.Add(peerEgressRefresh)
		pe.m[name] = e
	}
}

func (pe *peerEgressProber) save() {
	pe.mu.Lock()
	if !pe.dirty || pe.file == "" {
		pe.mu.Unlock()
		return
	}
	out := make(map[string]*peerEgress, len(pe.m))
	for name, e := range pe.m {
		if e.Checked > 0 {
			c := *e
			out[name] = &c
		}
	}
	pe.dirty = false
	pe.mu.Unlock()
	b, _ := json.Marshal(out)
	tmp := pe.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err == nil {
		err = os.Rename(tmp, pe.file)
		if err != nil {
			log.Printf("[peer-egress] save: %v", err)
		}
	}
}

// Kick re-probes every probed node now (new detection settings).
func (pe *peerEgressProber) Kick() {
	pe.mu.Lock()
	for _, e := range pe.m {
		e.next, e.fails = time.Time{}, 0
	}
	pe.mu.Unlock()
	select {
	case pe.kick <- struct{}{}:
	default:
	}
}

func (pe *peerEgressProber) run(ctx context.Context) {
	pe.ctx = ctx
	t := time.NewTicker(peerEgressRound)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			pe.save()
			return
		case <-t.C:
		case <-pe.kick:
		}
		pe.round()
		pe.save()
	}
}

// egressTarget: how a node is probed.
type egressTarget struct {
	path  string
	depth int // hops (1 = direct peer)
}

func (pe *peerEgressProber) round() {
	cands, reported := pe.a.peerEgressTargets()
	servers := pe.a.ifaces.pub.Servers()
	sig := strings.Join(servers, "\n")
	now := time.Now()
	type job struct{ name, path string }
	var due []job

	pe.mu.Lock()
	if pe.sig != "" && sig != pe.sig {
		for _, e := range pe.m {
			e.next, e.fails = time.Time{}, 0
		}
	}
	pe.sig = sig
	for name := range reported {
		if _, ok := pe.m[name]; ok {
			delete(pe.m, name)
			pe.dirty = true
		}
	}
	for name, e := range pe.m {
		if _, ok := cands[name]; !ok {
			e.since = time.Time{}
			if !e.running && e.Checked == 0 {
				delete(pe.m, name)
			} else if time.Since(time.Unix(e.Seen, 0)) > peerEgressKeep {
				delete(pe.m, name)
				pe.dirty = true
			}
		}
	}
	names := make([]string, 0, len(cands))
	for name := range cands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := cands[name]
		e := pe.m[name]
		if e == nil {
			if len(pe.m) >= peerEgressMax {
				continue
			}
			e = &peerEgress{}
			pe.m[name] = e
		}
		if e.since.IsZero() { // (re)connected: probe again soon (a failing one keeps its backoff)
			e.since = now
			if r := e.tried.Add(peerEgressReconnect); e.fails == 0 && r.Before(e.next) {
				e.next = r
			}
		}
		e.Seen = now.Unix()
		if c.depth > 1 && now.Sub(e.since) < peerEgressGrace {
			continue
		}
		if e.running || now.Before(e.next) {
			continue
		}
		due = append(due, job{name, c.path})
	}
	pe.mu.Unlock()

	for _, j := range due {
		select {
		case pe.sem <- struct{}{}:
		default:
			return // the rest next round
		}
		pe.mu.Lock()
		if e := pe.m[j.name]; e != nil {
			e.running = true
		}
		pe.mu.Unlock()
		go func(name, path string) {
			defer func() { <-pe.sem }()
			pe.probe(name, path, servers)
		}(j.name, j.path)
	}
}

var errPathNotUsable = errors.New("path not usable as exit")

// probe finds the egress of node name through path, per family.
func (pe *peerEgressProber) probe(name, path string, servers []string) {
	ctx, cancel := context.WithTimeout(pe.ctx, peerEgressTimeout)
	defer cancel()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !pe.a.exitPathUsable(path) {
			return nil, errPathNotUsable
		}
		if strings.HasPrefix(network, "udp") {
			return pe.a.exit.Dial(ctx, exit.Route{Exit: path}, "udp", addr)
		}
		return pe.a.exit.DialPath(ctx, path, addr)
	}
	var res [2]pubip.Result
	var wg sync.WaitGroup
	for i, fam := range []int{4, 6} {
		wg.Add(1)
		go func(i, fam int) {
			defer wg.Done()
			resolve := func(ctx context.Context, network, host string) ([]netip.Addr, error) {
				ss, err := pe.a.ResolveViaExit(ctx, host, path)
				if err != nil {
					return nil, err
				}
				var out []netip.Addr
				for _, s := range ss {
					if ip, err := netip.ParseAddr(s); err == nil && ip.Unmap().Is4() == (fam == 4) {
						out = append(out, ip.Unmap())
					}
				}
				if len(out) == 0 {
					return nil, errors.New("no address of this family for " + host)
				}
				return out, nil
			}
			res[i] = pubip.Detect(ctx, fam, dial, resolve, servers)
		}(i, fam)
	}
	wg.Wait()

	var eg publicip.SelfEgress
	if res[0].IP.IsValid() {
		eg.IPv4 = &publicip.EgressPublic{IP: res[0].IP.String(), OK: true}
	}
	if res[1].IP.IsValid() {
		eg.IPv6 = &publicip.EgressPublic{IP: res[1].IP.String(), OK: true}
	}
	now := time.Now()
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.m[name]
	if e == nil { // it reports by now
		return
	}
	e.running, e.tried = false, now
	if eg.IPv4 == nil && eg.IPv6 == nil {
		e.fails++
		backoff := peerEgressRetryMax
		if e.fails <= 6 {
			backoff = min(peerEgressRetryMin<<(e.fails-1), peerEgressRetryMax)
		}
		e.next = now.Add(backoff)
		if e.fails == 1 || e.fails%10 == 0 {
			log.Printf("[peer-egress] %s via %s: not found (%d): %v", name, path, e.fails, res[0].Err)
		}
		return
	}
	if old, _ := json.Marshal(e.Egress); string(old) != string(mustJSON(eg)) || e.Path != path {
		log.Printf("[peer-egress] %s via %s: %s", name, path, mustJSON(eg))
	}
	e.Egress, e.Path, e.Checked, e.fails = eg, path, now.Unix(), 0
	e.next = now.Add(peerEgressRefresh)
	pe.dirty = true
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// exitPathUsable: path (hops after this node) can carry user traffic to
// its last node — no loop through self or a repeated node, every hop
// before the last nested-enabled, none disabled (the exit dial's checks).
func (a *App) exitPathUsable(path string) bool {
	parts := splitPath(path)
	if len(parts) == 0 {
		return false
	}
	cfg := a.store.Get()
	seen := map[string]bool{cfg.NodeID: true, a.node.Name(): true}
	for _, p := range parts {
		if seen[p] {
			return false
		}
		seen[p] = true
	}
	return a.exit.Authorised(parts) && !a.exit.PathDisabled(path)
}

// peerEgressTargets: the nodes to probe (name → shortest usable path) and
// the nodes that report their egress. Direct peers report when they
// declare it (1.4.0+); a deeper node when its parent's list carries it.
// Deeper nodes are those the topology shows (only below nested-enabled
// hops, rule 2; from the direct peer's cache, rule 3; no loops, rule 1).
func (a *App) peerEgressTargets() (map[string]egressTarget, map[string]bool) {
	cfg := a.store.Get()
	h := exitHost{a}
	cands := map[string]egressTarget{}
	reported := map[string]bool{}
	direct := append(a.node.Peers(), a.node.PeersAlt()...)
	isDirect := map[string]bool{}
	for _, p := range direct {
		isDirect[p.Name] = true
		if p.AttrsCapable || p.EgressCapable {
			reported[p.Name] = true
			continue
		}
		if p.Incompatible || p.Unsupported || p.Conflict || !a.exitPathUsable(p.Name) {
			continue
		}
		cands[p.Name] = egressTarget{path: p.Name, depth: 1}
	}
	var walk func(prefix string, children []relay.TreePeer, anc map[string]bool, depth int)
	walk = func(prefix string, children []relay.TreePeer, anc map[string]bool, depth int) {
		for _, c := range children {
			if anc[c.Name] {
				continue
			}
			path := prefix + "/" + c.Name
			eg := c.Egress
			if (eg == nil || string(eg) == "null") && c.Attrs != nil {
				eg = c.Attrs[proto.AttrEgress]
			}
			switch {
			case eg != nil && string(eg) != "null":
				reported[c.Name] = true
			case isDirect[c.Name]:
			case c.Connected && !c.Disabled && a.exitPathUsable(path):
				if old, ok := cands[c.Name]; !ok || old.depth > depth {
					cands[c.Name] = egressTarget{path: path, depth: depth}
				}
			}
			if depth < peerEgressMaxDepth && h.Nested(path) {
				kids := c.Children
				if len(kids) == 0 {
					kids = a.tree.Cached(path)
				}
				anc[c.Name] = true
				walk(path, kids, anc, depth+1)
				delete(anc, c.Name)
			}
		}
	}
	done := map[string]bool{}
	for _, p := range direct {
		if done[p.Name] || !h.Nested(p.Name) || p.Native || p.Incompatible || p.Unsupported || p.Conflict {
			continue
		}
		done[p.Name] = true
		anc := map[string]bool{cfg.NodeID: true, a.node.Name(): true, p.Name: true}
		walk(p.Name, a.tree.Cached(p.Name), anc, 2)
	}
	for name := range reported {
		delete(cands, name)
	}
	return cands, reported
}

// ProbedEgress: the probed egress of node name (JSON, as the egress
// attribute), if any.
func (a *App) ProbedEgress(name string) (json.RawMessage, bool) {
	pe := a.peerEgress
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.m[name]
	if e == nil || e.Checked == 0 {
		return nil, false
	}
	return mustJSON(e.Egress), true
}

// PeerEgressView is one node of GET /api/peer-egress.
type PeerEgressView struct {
	Name    string               `json:"name"`
	Path    string               `json:"path,omitempty"`
	Egress  *publicip.SelfEgress `json:"egress"`
	Checked int64                `json:"checked,omitempty"`
	Next    int64                `json:"next,omitempty"`
	Fails   int                  `json:"fails,omitempty"`
	Probing bool                 `json:"probing,omitempty"`
}

// ProbedEgresses: the probe state of every node not reporting its egress.
func (a *App) ProbedEgresses() []PeerEgressView {
	pe := a.peerEgress
	pe.mu.Lock()
	defer pe.mu.Unlock()
	out := make([]PeerEgressView, 0, len(pe.m))
	for name, e := range pe.m {
		v := PeerEgressView{Name: name, Path: e.Path, Checked: e.Checked, Fails: e.fails, Probing: e.running}
		if e.Checked > 0 {
			eg := e.Egress
			v.Egress = &eg
		}
		if !e.next.IsZero() {
			v.Next = e.next.Unix()
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

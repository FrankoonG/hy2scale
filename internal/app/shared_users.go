package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Shared users: a user held by several nodes, all equal. Every holder may
// edit it (its exit stays per node), share it on and delete it everywhere.
// Holders keep each other in step over the mesh, signed with the user's
// key; only the first share to a node needs that node's trust. Each field
// carries a hybrid-logical-clock stamp (the later write wins), traffic is
// counted per installation and summed against the limit, and periods reset
// on UTC boundaries every holder computes alike. The sync itself lives in
// internal/api/shared_users.go. See docs/v1.4.4-shared-users.md.

// Stamp orders writes: T is a hybrid logical clock (ms*1000 + counter), I
// the writing installation (ties).
type Stamp struct {
	T int64  `yaml:"t" json:"t"`
	I string `yaml:"i,omitempty" json:"i,omitempty"`
}

// After: s was written later than o.
func (s Stamp) After(o Stamp) bool {
	if s.T != o.T {
		return s.T > o.T
	}
	return s.I > o.I
}

// Ms: the wall-clock milliseconds of s.
func (s Stamp) Ms() int64 { return s.T / 1000 }

// UsageEntry: one installation's traffic in the period starting at E (unix
// ms, 0 = since the start).
type UsageEntry struct {
	E int64 `yaml:"e" json:"e"`
	B int64 `yaml:"b" json:"b"`
}

// SharedMember: a holder of a shared user, keyed by installation id.
type SharedMember struct {
	NodeID string `yaml:"node_id,omitempty" json:"node_id,omitempty"`
	Name   string `yaml:"name" json:"name"`
	Left   bool   `yaml:"left,omitempty" json:"left,omitempty"`
	S      Stamp  `yaml:"s" json:"s"`
}

// SharedState: the share record kept with a shared user.
type SharedState struct {
	GID string `yaml:"gid"`
	Key string `yaml:"key"`
	// Stamps: the last write of each synced field (sharedFields, reset_at).
	Stamps map[string]Stamp `yaml:"stamps,omitempty"`
	// ResetAt: the last manual traffic reset (unix ms).
	ResetAt int64 `yaml:"reset_at,omitempty"`
	// Usage: the other installations' traffic (this one's is TrafficUsed).
	Usage map[string]UsageEntry `yaml:"usage,omitempty"`
	// Epoch: the period this node's TrafficUsed is counted in.
	Epoch   int64                   `yaml:"epoch,omitempty"`
	Members map[string]SharedMember `yaml:"members"`
}

func (s *SharedState) clone() *SharedState {
	if s == nil {
		return nil
	}
	c := *s
	c.Stamps = make(map[string]Stamp, len(s.Stamps))
	for k, v := range s.Stamps {
		c.Stamps[k] = v
	}
	c.Usage = make(map[string]UsageEntry, len(s.Usage))
	for k, v := range s.Usage {
		c.Usage[k] = v
	}
	c.Members = make(map[string]SharedMember, len(s.Members))
	for k, v := range s.Members {
		c.Members[k] = v
	}
	return &c
}

// SharedTomb: a shared user deleted everywhere, kept until every member
// still holding it (Pending) has been told.
type SharedTomb struct {
	GID      string                  `yaml:"gid"`
	Key      string                  `yaml:"key"`
	Username string                  `yaml:"username,omitempty"`
	Pending  map[string]SharedMember `yaml:"pending"`
	At       string                  `yaml:"at,omitempty"`
}

// sharedFields: the user fields kept in step (JSON names); the exit and the
// counters stay per node.
var sharedFields = []string{"username", "password", "proxy_passwords", "proxy_disabled", "enabled",
	"traffic_limit", "rate_up", "rate_down", "expiry_date", "node_access", "node_access_mode",
	"deny_internal", "traffic_reset", "traffic_reset_day"}

// fieldResetAt: the stamp key of the manual traffic reset.
const fieldResetAt = "reset_at"

// SharedRecord: a shared user as holders exchange it.
type SharedRecord struct {
	GID     string                     `json:"gid"`
	Fields  map[string]json.RawMessage `json:"fields"`
	Stamps  map[string]Stamp           `json:"stamps"`
	ResetAt int64                      `json:"reset_at"`
	Usage   map[string]UsageEntry      `json:"usage"`
	Members map[string]SharedMember    `json:"members"`
}

var jsonNull = json.RawMessage("null")

func userFields(u UserConfig) map[string]json.RawMessage {
	b, _ := json.Marshal(u)
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	out := make(map[string]json.RawMessage, len(sharedFields))
	for _, f := range sharedFields {
		if v, ok := m[f]; ok {
			out[f] = v
		} else {
			out[f] = jsonNull
		}
	}
	return out
}

// setUserFields writes vals (synced fields only) into u.
func setUserFields(u *UserConfig, vals map[string]json.RawMessage) error {
	b, _ := json.Marshal(*u)
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	for f, v := range vals {
		if string(v) == "null" || len(v) == 0 {
			delete(m, f)
		} else {
			m[f] = v
		}
	}
	b, _ = json.Marshal(m)
	var nu UserConfig
	if err := json.Unmarshal(b, &nu); err != nil {
		return err
	}
	nu.TrafficResetFrom, nu.Shared = u.TrafficResetFrom, u.Shared
	*u = nu
	return nil
}

func isSharedField(f string) bool {
	for _, s := range sharedFields {
		if s == f {
			return true
		}
	}
	return f == fieldResetAt
}

// hlcNow: a fresh stamp of this installation.
func (a *App) hlcNow() Stamp {
	a.hlcMu.Lock()
	defer a.hlcMu.Unlock()
	pt := time.Now().UnixMilli() * 1000
	if pt > a.hlcLast {
		a.hlcLast = pt
	} else {
		a.hlcLast++
	}
	return Stamp{T: a.hlcLast, I: a.node.InstallationID()}
}

// hlcSeen moves the clock past a stamp received (one far ahead of this
// node's clock moves it at most a minute).
func (a *App) hlcSeen(t int64) {
	a.hlcMu.Lock()
	defer a.hlcMu.Unlock()
	if lim := (time.Now().UnixMilli() + 60_000) * 1000; t > lim {
		t = lim
	}
	if t > a.hlcLast {
		a.hlcLast = t
	}
}

// SelfIID: this installation's id (the member key).
func (a *App) SelfIID() string { return a.node.InstallationID() }

// selfMember: this node's member entry (nodeID: the config's NodeID — the
// store may be locked by the caller).
func (a *App) selfMember(nodeID string) SharedMember {
	return SharedMember{NodeID: nodeID, Name: a.node.Name()}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func randomUserID() string { return randomHex(4) }

// sharedEpoch: the start (unix ms) of the user's current traffic period: the
// later of its last manual reset and its last UTC period boundary (one that
// lies after the period was set).
func sharedEpoch(u *UserConfig, now time.Time) int64 {
	st := u.Shared
	e := st.ResetAt
	if u.TrafficReset != "" {
		b := TrafficResetLast(u.TrafficReset, u.TrafficResetDay, now.UTC())
		set := st.Stamps["traffic_reset"].Ms()
		if d := st.Stamps["traffic_reset_day"].Ms(); d > set {
			set = d
		}
		if !b.IsZero() && b.UnixMilli() >= set && b.UnixMilli() > e {
			e = b.UnixMilli()
		}
	}
	return e
}

// SharedNextReset: the next UTC boundary of a shared user's period ("" for
// none).
func SharedNextReset(u *UserConfig, now time.Time) string {
	n := TrafficResetNext(u.TrafficReset, u.TrafficResetDay, now.UTC())
	if n.IsZero() {
		return ""
	}
	return n.Format(time.RFC3339)
}

// remoteUsed: the other installations' traffic in u's current period.
func remoteUsed(u *UserConfig, self string) int64 {
	var sum int64
	for iid, e := range u.Shared.Usage {
		if iid != self && e.E == u.Shared.Epoch {
			sum += e.B
		}
	}
	return sum
}

// SharedUsage: per installation, the traffic of u in its current period
// (this node's live counter included).
func (a *App) SharedUsage(u UserConfig) map[string]int64 {
	out := map[string]int64{}
	if u.Shared == nil {
		return out
	}
	self := a.SelfIID()
	for iid, e := range u.Shared.Usage {
		if iid != self && e.E == u.Shared.Epoch {
			out[iid] = e.B
		}
	}
	used := u.TrafficUsed
	if live, ok := a.liveUser(u.Username); ok {
		used = live
	}
	out[self] = used
	return out
}

func (a *App) liveUser(username string) (int64, bool) {
	a.usersMu.RLock()
	defer a.usersMu.RUnlock()
	if u, ok := a.userIndex[username]; ok {
		return atomic.LoadInt64(&u.TrafficUsed), true
	}
	return 0, false
}

// sharedChanged reports a change of a shared user to the sync (set by the
// api server).
func (a *App) sharedChanged(gid string) {
	if f := a.SharedNotify; f != nil && gid != "" {
		f(gid)
	}
}

// --- local operations ---

// MakeShared turns a local user into a shared one (it stays so when it is
// already). The sharer is its first member.
func (a *App) MakeShared(id string) (UserConfig, error) {
	var out UserConfig
	found := false
	err := a.store.Update(func(c *Config) {
		for i := range c.Users {
			u := &c.Users[i]
			if u.ID != id {
				continue
			}
			found = true
			if u.Shared == nil {
				now := a.hlcNow()
				st := &SharedState{GID: randomHex(16), Key: randomHex(32), Stamps: map[string]Stamp{},
					Usage: map[string]UsageEntry{}, Members: map[string]SharedMember{}}
				for _, f := range sharedFields {
					st.Stamps[f] = now
				}
				st.Stamps[fieldResetAt] = now
				m := a.selfMember(c.NodeID)
				m.S = now
				st.Members[a.SelfIID()] = m
				u.Shared = st
				st.Epoch = sharedEpoch(u, TrafficNow())
			}
			out = *u
			return
		}
	})
	if err == nil && !found {
		err = fmt.Errorf("user not found")
	}
	if err == nil {
		a.rebuildUserIndex()
	}
	return out, err
}

// SharedUserByGID: the shared user gid held here.
func (a *App) SharedUserByGID(gid string) (UserConfig, bool) {
	for _, u := range a.store.Get().Users {
		if u.Shared != nil && u.Shared.GID == gid {
			return u, true
		}
	}
	return UserConfig{}, false
}

// SharedRecordOf: the record of a shared user held here, with this node's
// own usage and member entry current.
func (a *App) SharedRecordOf(u UserConfig) SharedRecord {
	st := u.Shared
	rec := SharedRecord{GID: st.GID, Fields: userFields(u), Stamps: map[string]Stamp{}, ResetAt: st.ResetAt,
		Usage: map[string]UsageEntry{}, Members: map[string]SharedMember{}}
	for k, v := range st.Stamps {
		rec.Stamps[k] = v
	}
	for k, v := range st.Usage {
		rec.Usage[k] = v
	}
	for k, v := range st.Members {
		rec.Members[k] = v
	}
	used := u.TrafficUsed
	if live, ok := a.liveUser(u.Username); ok {
		used = live
	}
	rec.Usage[a.SelfIID()] = UsageEntry{E: st.Epoch, B: used}
	return rec
}

// sharedStamp stamps the synced fields of cur that differ from old (a
// local edit); reset: a manual traffic reset.
func (a *App) sharedStamp(old, cur *UserConfig, reset bool) {
	if cur.Shared == nil {
		return
	}
	st := cur.Shared.clone()
	of, nf := userFields(*old), userFields(*cur)
	var now Stamp
	stamp := func(f string) {
		if now.T == 0 {
			now = a.hlcNow()
		}
		st.Stamps[f] = now
	}
	for _, f := range sharedFields {
		if string(of[f]) != string(nf[f]) {
			stamp(f)
		}
	}
	if reset {
		st.ResetAt = TrafficNow().UnixMilli()
		stamp(fieldResetAt)
	}
	cur.Shared = st
}

// MergeShared merges a record received from a member into the shared user
// gid. state: "ok" (merged; out is the merged record), "absent" (not held
// here) or "deleted" (deleted everywhere: a tomb is kept). conflict names a
// field not applied (a username taken here by another user). news: a field
// or the member list changed (worth telling the other members; traffic
// counts alone are not).
func (a *App) MergeShared(rec SharedRecord) (out SharedRecord, state, conflict string, news bool, err error) {
	cfg := a.store.Get()
	for _, t := range cfg.SharedTombs {
		if t.GID == rec.GID {
			return out, "deleted", "", false, nil
		}
	}
	if _, ok := a.SharedUserByGID(rec.GID); !ok {
		return out, "absent", "", false, nil
	}
	var kick string
	var merged UserConfig
	changed := false
	a.usersMu.RLock()
	err = a.store.Update(func(c *Config) {
		for i := range c.Users {
			u := &c.Users[i]
			if u.Shared == nil || u.Shared.GID != rec.GID {
				continue
			}
			old := *u
			nu := *u
			nu.Shared = u.Shared.clone()
			ch, nw, cf := a.mergeInto(c, i, &nu, rec)
			conflict, news = cf, nw
			if ch {
				changed = true
				nu.TrafficUsed = atomic.LoadInt64(&u.TrafficUsed)
				if live, ok := a.userIndex[u.Username]; ok {
					nu.TrafficUsed = atomic.LoadInt64(&live.TrafficUsed)
				}
				if credsChanged(&old, &nu) {
					kick = old.Username
				}
				*u = nu
			}
			merged = *u
			return
		}
	})
	a.usersMu.RUnlock()
	if err != nil {
		return out, "", conflict, news, err
	}
	if changed {
		a.rebuildUserIndex()
		a.applySharedEpochs()
		merged, _ = a.SharedUserByGID(rec.GID)
	}
	if kick != "" {
		a.Sessions.KickUser(kick)
	}
	return a.SharedRecordOf(merged), "ok", conflict, news, nil
}

func credsChanged(old, nu *UserConfig) bool {
	return old.Password != nu.Password || fmt.Sprint(old.ProxyPasswords) != fmt.Sprint(nu.ProxyPasswords) ||
		fmt.Sprint(old.ProxyDisabled) != fmt.Sprint(nu.ProxyDisabled) || old.Username != nu.Username ||
		(old.Enabled && !nu.Enabled)
}

// mergeInto merges rec into nu (users[idx] of c, its Shared already a
// copy). Returns whether anything changed, whether a field or a member did,
// and a field left out.
func (a *App) mergeInto(c *Config, idx int, nu *UserConfig, rec SharedRecord) (bool, bool, string) {
	st := nu.Shared
	self := a.SelfIID()
	changed := false
	conflict := ""
	vals := map[string]json.RawMessage{}
	for f, s := range rec.Stamps {
		a.hlcSeen(s.T)
		if !isSharedField(f) || !s.After(st.Stamps[f]) {
			continue
		}
		if f == fieldResetAt {
			st.ResetAt = rec.ResetAt
			st.Stamps[f] = s
			changed = true
			continue
		}
		v, ok := rec.Fields[f]
		if !ok {
			continue
		}
		if f == "username" {
			var name string
			json.Unmarshal(v, &name)
			if name == "" || usernameTaken(c, idx, name) {
				conflict = "username"
				continue
			}
		}
		if f == "password" {
			var pw string
			json.Unmarshal(v, &pw)
			if pw == "" || (c.Server != nil && c.Server.Password != "" && c.Server.Password == pw) {
				conflict = "password"
				continue
			}
		}
		vals[f] = v
		st.Stamps[f] = s
		changed = true
	}
	news := changed
	if len(vals) > 0 {
		if err := setUserFields(nu, vals); err != nil {
			log.Printf("[shared] %s: %v", st.GID, err)
		}
	}
	for iid, e := range rec.Usage {
		if iid == self {
			continue
		}
		cur, ok := st.Usage[iid]
		if !ok || e.E > cur.E || (e.E == cur.E && e.B > cur.B) {
			st.Usage[iid] = e
			changed = true
		}
	}
	for iid, m := range rec.Members {
		a.hlcSeen(m.S.T)
		cur, ok := st.Members[iid]
		if !ok || m.S.After(cur.S) {
			st.Members[iid] = m
			changed, news = true, true
		}
	}
	// this node holds it: its own entry says so, under its current name
	me, ok := st.Members[self]
	sm := a.selfMember(c.NodeID)
	if !ok || me.Left || me.Name != sm.Name || me.NodeID != sm.NodeID {
		sm.S = a.hlcNow()
		st.Members[self] = sm
		changed, news = true, true
	}
	return changed, news, conflict
}

func usernameTaken(c *Config, idx int, name string) bool {
	for j, o := range c.Users {
		if j != idx && o.Username == name {
			return true
		}
	}
	return false
}

// JoinResult: the outcome of a first share on the receiving node.
type JoinResult struct {
	State  string       `json:"state"` // ok | conflict
	Error  string       `json:"error,omitempty"`
	Member SharedMember `json:"member"`
	IID    string       `json:"iid"`
	Record SharedRecord `json:"record"`
}

// JoinShared adds a shared user received in a first share (or merges it when
// already held). exitNode: the node its exit starts from ("" = direct).
func (a *App) JoinShared(rec SharedRecord, key, exitNode string) (JoinResult, error) {
	self := a.SelfIID()
	res := JoinResult{IID: self, Member: a.selfMember(a.store.Get().NodeID)}
	if _, ok := a.SharedUserByGID(rec.GID); ok {
		out, _, cf, _, err := a.MergeShared(rec)
		if err != nil {
			return res, err
		}
		res.State, res.Record = "ok", out
		if cf != "" {
			res.Error = cf + " not applied here (taken)"
		}
		return res, nil
	}
	var nu UserConfig
	if err := setUserFields(&nu, rec.Fields); err != nil {
		return res, err
	}
	if nu.Username == "" || nu.Password == "" {
		return res, fmt.Errorf("username and password required")
	}
	if _, err := ValidateTrafficReset(nu.TrafficReset, nu.TrafficResetDay); err != nil {
		return res, err
	}
	nu.ExitVia, nu.ExitPaths, nu.ExitMode = a.initialExit(exitNode)
	nu.ExitPolicy = ""
	nu.TrafficUsed = 0
	st := &SharedState{GID: rec.GID, Key: key, Stamps: map[string]Stamp{}, ResetAt: rec.ResetAt,
		Usage: map[string]UsageEntry{}, Members: map[string]SharedMember{}}
	for k, v := range rec.Stamps {
		if isSharedField(k) {
			st.Stamps[k] = v
			a.hlcSeen(v.T)
		}
	}
	for k, v := range rec.Usage {
		if k != self {
			st.Usage[k] = v
		}
	}
	for k, v := range rec.Members {
		st.Members[k] = v
		a.hlcSeen(v.S.T)
	}
	m := res.Member
	m.S = a.hlcNow()
	st.Members[self] = m
	nu.Shared = st
	st.Epoch = sharedEpoch(&nu, TrafficNow())
	if st.Epoch > 0 {
		nu.TrafficResetAt = time.UnixMilli(st.Epoch).UTC().Format(time.RFC3339)
	}
	err := a.store.Update(func(c *Config) {
		for _, u := range c.Users {
			if u.Username == nu.Username {
				res.State, res.Error = "conflict", fmt.Sprintf("username %s is already used on this node", nu.Username)
				return
			}
		}
		if c.Server != nil && c.Server.Password != "" && c.Server.Password == nu.Password {
			res.State, res.Error = "conflict", "user password collides with this node's hy2 server password"
			return
		}
		nu.ID = randomUserID()
		c.Users = append(c.Users, nu)
		out := c.SharedTombs[:0]
		for _, t := range c.SharedTombs {
			if t.GID != rec.GID {
				out = append(out, t)
			}
		}
		c.SharedTombs = nilIfEmpty(out)
		res.State = "ok"
	})
	if err != nil || res.State != "ok" {
		return res, err
	}
	a.rebuildUserIndex()
	log.Printf("[shared] user %s joined (shared user %s)", nu.Username, rec.GID)
	u, _ := a.SharedUserByGID(rec.GID)
	res.Record = a.SharedRecordOf(u)
	return res, nil
}

func nilIfEmpty(t []SharedTomb) []SharedTomb {
	if len(t) == 0 {
		return nil
	}
	return t
}

// initialExit: the exit of a user received in a first share — every path
// to exitNode this node knows, fastest first, in quality mode; exitNode
// itself (no path yet) when none; direct for "" or this node.
func (a *App) initialExit(exitNode string) (string, []string, string) {
	if exitNode == "" || exitNode == a.node.Name() || exitNode == a.store.Get().NodeID {
		return "", nil, ""
	}
	paths := a.PathsTo(exitNode)
	if len(paths) == 0 {
		return exitNode, []string{exitNode}, ""
	}
	mode := ""
	if len(paths) > 1 {
		mode = "quality"
	}
	return paths[0], paths, mode
}

// ReachPath: a path to a node this node can reach (Lat: summed latency in
// ms; an inbound first hop without a measurement counts 10 s).
type ReachPath struct {
	Hops    []string
	Lat     int
	Version string
}

// Path: the hops joined.
func (r ReachPath) Path() string { return strings.Join(r.Hops, "/") }

// Reachable: every path this node can reach a node by — through connected
// peers (never node access by user), nested-enabled hops, no node twice and
// never this node — fastest first.
func (a *App) Reachable() []ReachPath {
	return a.reach("")
}

// PathsTo: the paths to the node target (Reachable), fastest first.
func (a *App) PathsTo(target string) []string {
	self, selfID := a.node.Name(), a.store.Get().NodeID
	if target == "" || target == self || target == selfID {
		return nil
	}
	var paths []string
	for _, r := range a.reach(target) {
		if r.Hops[len(r.Hops)-1] == target {
			paths = append(paths, r.Path())
		}
	}
	return paths
}

// reach walks the paths; stop: a node not walked past ("" = none).
func (a *App) reach(stop string) []ReachPath {
	self, selfID := a.node.Name(), a.store.Get().NodeID
	var out, level []ReachPath
	for _, p := range a.node.Peers() {
		if p.Invisible || p.Restricted || p.Name == self || p.Name == selfID {
			continue
		}
		// an inbound peer has no latency of its own: known, but last
		lat := a.node.GetLatency(p.Name)
		if lat < 0 {
			lat = p.LatencyMs
		}
		if lat < 0 {
			lat = 10_000
		}
		level = append(level, ReachPath{Hops: []string{p.Name}, Lat: lat, Version: p.Version})
	}
	const maxDepth = 6
	for depth := 1; depth <= maxDepth && len(level) > 0; depth++ {
		var next []ReachPath
		for _, c := range level {
			path := c.Path()
			if a.exit.PathDisabled(path) {
				continue
			}
			out = append(out, c)
			last := c.Hops[len(c.Hops)-1]
			if last == stop {
				continue
			}
			kids, ok := a.node.PeersOfCached(path)
			if !ok {
				continue
			}
			for _, k := range kids {
				if k.Name == "" || k.Name == self || k.Name == selfID || k.LatencyMs < 0 {
					continue
				}
				dup := false
				for _, h := range c.Hops {
					if h == k.Name {
						dup = true
					}
				}
				hops := append(append([]string(nil), c.Hops...), k.Name)
				if dup || !a.exit.Authorised(hops) {
					continue
				}
				next = append(next, ReachPath{Hops: hops, Lat: c.Lat + k.LatencyMs, Version: k.Version})
			}
		}
		level = next
	}
	// a native (plain hysteria) node relays nothing: only as the last hop
	keep := out[:0]
	for _, r := range out {
		ok := true
		for i := 0; i < len(r.Hops)-1; i++ {
			if p, found := a.exit.NodeInfoAt(r.Hops[:i+1]); found && p.Native {
				ok = false
			}
		}
		if ok {
			keep = append(keep, r)
		}
	}
	out = keep
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Lat != out[j].Lat {
			return out[i].Lat < out[j].Lat
		}
		return len(out[i].Hops) < len(out[j].Hops)
	})
	return out
}

// UnshareIfAlone turns a shared user without other members back into a
// local one (a first share that failed).
func (a *App) UnshareIfAlone(id string) {
	changed := false
	a.store.Update(func(c *Config) {
		for i := range c.Users {
			u := &c.Users[i]
			if u.ID != id || u.Shared == nil {
				continue
			}
			for iid, m := range u.Shared.Members {
				if iid != a.SelfIID() && !m.Left {
					return
				}
			}
			u.Shared = nil
			changed = true
			return
		}
	})
	if changed {
		a.rebuildUserIndex()
	}
}

// ApplySharedAttr announces whether this node accepts shared users (the
// node attribute other nodes list share candidates by).
func (a *App) ApplySharedAttr() {
	if a.store.Get().AcceptSharedUsers {
		a.node.SetAttr(AttrSharedUsers, json.RawMessage("true"))
	} else {
		a.node.SetAttr(AttrSharedUsers, nil)
	}
}

// AttrSharedUsers: the node attribute of AcceptSharedUsers.
const AttrSharedUsers = "shared_users"

// AddSharedMember records a node that joined through this node.
func (a *App) AddSharedMember(gid, iid string, m SharedMember) error {
	return a.updateShared(gid, func(st *SharedState) bool {
		m.Left = false
		m.S = a.hlcNow()
		st.Members[iid] = m
		return true
	})
}

// MarkMemberLeft: the member iid no longer holds gid.
func (a *App) MarkMemberLeft(gid, iid string) {
	a.updateShared(gid, func(st *SharedState) bool {
		m, ok := st.Members[iid]
		if !ok || m.Left || iid == a.SelfIID() {
			return false
		}
		m.Left = true
		m.S = a.hlcNow()
		st.Members[iid] = m
		return true
	})
}

func (a *App) updateShared(gid string, fn func(st *SharedState) bool) error {
	changed := false
	err := a.store.Update(func(c *Config) {
		for i := range c.Users {
			u := &c.Users[i]
			if u.Shared == nil || u.Shared.GID != gid {
				continue
			}
			st := u.Shared.clone()
			if fn(st) {
				u.Shared = st
				changed = true
			}
			return
		}
	})
	if changed {
		a.rebuildUserIndex()
		a.sharedChanged(gid)
	}
	return err
}

// LeaveShared deletes the shared user id here only and returns the record
// to tell the other members with (this node marked as left) and its key.
func (a *App) LeaveShared(id string) (rec SharedRecord, key string, err error) {
	var cur UserConfig
	for _, u := range a.store.Get().Users {
		if u.ID == id && u.Shared != nil {
			cur = u
		}
	}
	if cur.Shared == nil {
		return rec, "", fmt.Errorf("not a shared user")
	}
	// the record is read before the store is locked (lock order: users
	// index, then store)
	rec, key = a.SharedRecordOf(cur), cur.Shared.Key
	m := rec.Members[a.SelfIID()]
	m.Left, m.S = true, a.hlcNow()
	rec.Members[a.SelfIID()] = m
	delete(rec.Usage, a.SelfIID())
	removed := false
	err = a.store.Update(func(c *Config) {
		for i, u := range c.Users {
			if u.ID == id && u.Shared != nil && u.Shared.GID == rec.GID {
				c.Users = append(c.Users[:i], c.Users[i+1:]...)
				removed = true
				return
			}
		}
	})
	if err == nil && !removed {
		err = fmt.Errorf("not a shared user")
	}
	if err == nil {
		a.rebuildUserIndex()
		a.Sessions.KickUser(cur.Username)
		log.Printf("[shared] left shared user %s (%s)", cur.Username, rec.GID)
	}
	return
}

// DeleteSharedEverywhere deletes a shared user here and keeps a tomb for
// the other members still to tell. exceptIID: a member that already knows
// (the one that told this node).
func (a *App) DeleteSharedEverywhere(gid, exceptIID string) (state string, err error) {
	var name string
	state = "absent"
	err = a.store.Update(func(c *Config) {
		for _, t := range c.SharedTombs {
			if t.GID == gid {
				state = "deleted"
				return
			}
		}
		for i, u := range c.Users {
			if u.Shared == nil || u.Shared.GID != gid {
				continue
			}
			tomb := SharedTomb{GID: gid, Key: u.Shared.Key, Username: u.Username, Pending: map[string]SharedMember{},
				At: time.Now().UTC().Format(time.RFC3339)}
			for iid, m := range u.Shared.Members {
				if iid != a.SelfIID() && iid != exceptIID && !m.Left {
					tomb.Pending[iid] = m
				}
			}
			if len(tomb.Pending) > 0 {
				c.SharedTombs = append(c.SharedTombs, tomb)
			}
			name = u.Username
			c.Users = append(c.Users[:i], c.Users[i+1:]...)
			state = "deleted"
			return
		}
	})
	if err == nil && name != "" {
		a.rebuildUserIndex()
		a.Sessions.KickUser(name)
		log.Printf("[shared] shared user %s (%s) deleted everywhere", name, gid)
	}
	return
}

// SharedTombs: the deletions still to deliver.
func (a *App) SharedTombs() []SharedTomb { return a.store.Get().SharedTombs }

// TombDone: member iid no longer holds the user of tomb gid.
func (a *App) TombDone(gid, iid string) {
	a.store.Update(func(c *Config) {
		out := c.SharedTombs[:0]
		for _, t := range c.SharedTombs {
			if t.GID == gid {
				p := make(map[string]SharedMember, len(t.Pending))
				for k, v := range t.Pending {
					if k != iid {
						p[k] = v
					}
				}
				t.Pending = p
				if len(p) == 0 {
					continue
				}
			}
			out = append(out, t)
		}
		c.SharedTombs = nilIfEmpty(out)
	})
}

// applySharedEpochs starts a new period for the shared users whose period
// boundary or manual reset has passed: this node's counter restarts and the
// other installations' counts of older periods are dropped.
func (a *App) applySharedEpochs() {
	now := TrafficNow()
	need := false
	cfg := a.store.Get()
	for i := range cfg.Users {
		if u := &cfg.Users[i]; u.Shared != nil && sharedEpoch(u, now) > u.Shared.Epoch {
			need = true
		}
	}
	if !need {
		return
	}
	var reset []string
	a.usersMu.RLock()
	a.store.Update(func(c *Config) {
		for i := range c.Users {
			u := &c.Users[i]
			if u.Shared == nil {
				continue
			}
			e := sharedEpoch(u, now)
			if e <= u.Shared.Epoch {
				continue
			}
			st := u.Shared.clone()
			st.Epoch = e
			for k, v := range st.Usage {
				if v.E < e {
					delete(st.Usage, k)
				}
			}
			if live, ok := a.userIndex[u.Username]; ok {
				atomic.StoreInt64(&live.TrafficUsed, 0)
			}
			atomic.StoreInt64(&u.TrafficUsed, 0)
			u.TrafficResetAt = time.UnixMilli(e).UTC().Format(time.RFC3339)
			u.Shared = st
			reset = append(reset, u.Username)
		}
	})
	a.usersMu.RUnlock()
	if len(reset) > 0 {
		a.rebuildUserIndex()
		log.Printf("[shared] new traffic period: %v", reset)
	}
}

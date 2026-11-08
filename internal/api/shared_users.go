// Add-on users: shared users — a user held by several nodes, all equal
// (internal/app/shared_users.go). Holders keep each other in step over the
// mesh with requests signed by the user's key; the first share to a node
// needs that node's relay trust or a session there. Nothing travels over
// node access by user: no push leaves through such a connection and the
// endpoints below refuse requests that arrive through one.
// See docs/v1.4.4-shared-users.md.

package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
)

const (
	sharedCallTimeout = 10 * time.Second
	sharedUsageEvery  = 30 * time.Second // own traffic counts, when changed
	sharedFullEvery   = time.Minute      // anti-entropy: every member, whole record
	sharedTombEvery   = 30 * time.Second
	sharedClockSkew   = 10 * time.Minute // signed requests older or newer are refused
	sharedPathTries   = 3
)

// memberStatus: how a member was last reached from here.
type memberStatus struct {
	State    string `json:"state"` // synced | pending | error | unreachable
	Error    string `json:"error,omitempty"`
	SyncedAt string `json:"synced_at,omitempty"`
	Path     string `json:"path,omitempty"`
}

type sharedFlight struct {
	again bool
	done  chan struct{}
}

// shareSync keeps the members of the shared users held here in step.
type shareSync struct {
	s         *Server
	mu        sync.Mutex
	status    map[string]map[string]*memberStatus // gid → iid → status
	flights   map[string]*sharedFlight            // gid + "\x00" + iid
	lastUsage map[string]int64                    // gid → own count last pushed
}

var (
	shareSyncsMu sync.Mutex
	shareSyncs   = map[*Server]*shareSync{}
)

func (s *Server) shares() *shareSync {
	shareSyncsMu.Lock()
	defer shareSyncsMu.Unlock()
	ss := shareSyncs[s]
	if ss == nil {
		ss = &shareSync{s: s, status: map[string]map[string]*memberStatus{},
			flights: map[string]*sharedFlight{}, lastUsage: map[string]int64{}}
		shareSyncs[s] = ss
	}
	return ss
}

// startShareSync runs the sync for the life of the process.
func (s *Server) startShareSync() {
	ss := s.shares()
	s.app.SharedNotify = func(gid string) { go ss.pushAll(gid) }
	s.app.ApplySharedAttr()
	go func() {
		time.Sleep(10 * time.Second) // let the peers connect first
		seen := map[string]bool{}
		var lastFull, lastUsage, lastTomb time.Time
		for {
			now := map[string]bool{}
			fresh := false
			for _, p := range s.app.Node().Peers() {
				now[p.Name] = true
				if !seen[p.Name] {
					fresh = true
				}
			}
			seen = now
			if fresh || time.Since(lastFull) >= sharedFullEvery {
				ss.roundAll()
				lastFull, lastUsage = time.Now(), time.Now()
			} else if time.Since(lastUsage) >= sharedUsageEvery {
				ss.roundUsage()
				lastUsage = time.Now()
			}
			if fresh || time.Since(lastTomb) >= sharedTombEvery {
				ss.roundTombs()
				lastTomb = time.Now()
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

func (ss *shareSync) sharedUsers() []app.UserConfig {
	var out []app.UserConfig
	for _, u := range ss.s.app.Store().Get().Users {
		if u.Shared != nil {
			out = append(out, u)
		}
	}
	return out
}

func (ss *shareSync) roundAll() {
	for _, u := range ss.sharedUsers() {
		ss.pushAll(u.Shared.GID)
	}
}

// roundUsage pushes the users whose own traffic count moved.
func (ss *shareSync) roundUsage() {
	for _, u := range ss.sharedUsers() {
		used := ss.s.app.SharedUsage(u)[ss.s.app.SelfIID()]
		ss.mu.Lock()
		moved := ss.lastUsage[u.Shared.GID] != used
		ss.mu.Unlock()
		if moved {
			ss.pushAll(u.Shared.GID)
		}
	}
}

// pushAll syncs gid with every other member.
func (ss *shareSync) pushAll(gid string) {
	u, ok := ss.s.app.SharedUserByGID(gid)
	if !ok {
		return
	}
	ss.mu.Lock()
	ss.lastUsage[gid] = ss.s.app.SharedUsage(u)[ss.s.app.SelfIID()]
	ss.mu.Unlock()
	for iid, m := range u.Shared.Members {
		if iid != ss.s.app.SelfIID() && !m.Left {
			ss.start(gid, iid)
		}
	}
}

// start syncs (gid, member) in the background; pushes to one member never
// overlap (a change during a push is pushed again after it).
func (ss *shareSync) start(gid, iid string) <-chan struct{} {
	key := gid + "\x00" + iid
	ss.mu.Lock()
	if f := ss.flights[key]; f != nil {
		f.again = true
		ss.mu.Unlock()
		return f.done
	}
	f := &sharedFlight{done: make(chan struct{})}
	ss.flights[key] = f
	ss.mu.Unlock()
	go func() {
		for {
			ss.syncMember(gid, iid)
			ss.mu.Lock()
			if f.again {
				f.again = false
				ss.mu.Unlock()
				continue
			}
			delete(ss.flights, key)
			ss.mu.Unlock()
			close(f.done)
			return
		}
	}()
	return f.done
}

func (ss *shareSync) set(gid, iid string, fn func(st *memberStatus)) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	m := ss.status[gid]
	if m == nil {
		m = map[string]*memberStatus{}
		ss.status[gid] = m
	}
	st := m[iid]
	if st == nil {
		st = &memberStatus{State: "pending"}
		m[iid] = st
	}
	fn(st)
}

func (ss *shareSync) statusOf(gid, iid string) memberStatus {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if st := ss.status[gid][iid]; st != nil {
		return *st
	}
	return memberStatus{State: "pending"}
}

// --- the wire ---

// sharedMsg is the body of /api/shared-users/sync and /delete.
type sharedMsg struct {
	GID    string            `json:"gid"`
	To     string            `json:"to"`   // the member's installation id
	From   string            `json:"from"` // the sender's
	Record *app.SharedRecord `json:"record,omitempty"`
}

// sharedReply: the answer to a sync or a delete.
type sharedReply struct {
	State  string            `json:"state"` // ok | deleted | absent | wrong_node
	Record *app.SharedRecord `json:"record,omitempty"`
	Error  string            `json:"error,omitempty"`
}

func sharedSig(key, kind, ts string, body []byte) string {
	k, _ := hex.DecodeString(key)
	m := hmac.New(sha256.New, k)
	m.Write([]byte(kind + "\n" + ts + "\n"))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func sharedSigned(key, kind string, h http.Header, body []byte) bool {
	ts := h.Get("X-Shared-TS")
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if d := time.Since(time.UnixMilli(ms)); d > sharedClockSkew || d < -sharedClockSkew {
		return false
	}
	want := sharedSig(key, kind, ts, body)
	return hmac.Equal([]byte(want), []byte(h.Get("X-Shared-Sig")))
}

func signHeaders(key, kind string, body []byte) map[string]string {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	return map[string]string{"X-Shared-TS": ts, "X-Shared-Sig": sharedSig(key, kind, ts, body)}
}

// relayCall sends one request to the management API of the node at the end
// of chain and returns its status, headers and body.
func (s *Server) relayCall(chain []string, method, path string, body []byte, hdr map[string]string) (int, http.Header, []byte, error) {
	chain = append([]string(nil), chain...)
	s.mu.RLock()
	for i, name := range chain {
		if newID, ok := s.oldNodeIDs[name]; ok {
			chain[i] = newID
		}
	}
	s.mu.RUnlock()
	if len(chain) == 0 {
		return 0, nil, nil, fmt.Errorf("empty chain")
	}
	if s.app.Node().IsInvisible(chain[0]) {
		return 0, nil, nil, fmt.Errorf("not over node access by user")
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedCallTimeout)
	defer cancel()
	conn, _, err := s.dialRemoteAPI(ctx, chain)
	if err != nil {
		return 0, nil, nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, "http://127.0.0.1:5565/scale"+path, rd)
	req.Host = "127.0.0.1:5565"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req.Close = true
	target := strings.Join(chain, "/")
	if err := req.Write(conn); err != nil {
		return 0, nil, nil, fmt.Errorf("write to %s: %v", target, err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("no answer from %s: %v", target, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, resp.Header, b, nil
}

// callMember sends a signed message to the member iid (named name), trying
// the fastest few paths to it. Returns the verified reply.
func (s *Server) callMember(key, kind, name string, msg sharedMsg) (sharedReply, string, error) {
	body, _ := json.Marshal(msg)
	paths := s.app.PathsTo(name)
	if len(paths) == 0 {
		return sharedReply{}, "", errUnreachable
	}
	if len(paths) > sharedPathTries {
		paths = paths[:sharedPathTries]
	}
	var lastErr error
	for _, p := range paths {
		code, h, resp, err := s.relayCall(strings.Split(p, "/"), "POST", "/api/shared-users/"+kind, body, signHeaders(key, kind, body))
		if err != nil {
			lastErr = err
			continue
		}
		var rep sharedReply
		switch {
		case code == 404 || code == 405 || code == 302:
			return rep, p, fmt.Errorf("%s does not support shared users (older version)", name)
		case code != 200 && code != 409:
			return rep, p, fmt.Errorf("%s answered %d: %s", name, code, remoteMsg(resp))
		}
		if err := json.Unmarshal(resp, &rep); err != nil {
			return rep, p, fmt.Errorf("%s: %v", name, err)
		}
		// absent and wrong_node come from a node without the key
		if rep.State != "absent" && rep.State != "wrong_node" && !sharedSigned(key, "reply", h, resp) {
			return rep, p, fmt.Errorf("%s: reply not signed with the user's key", name)
		}
		return rep, p, nil
	}
	return sharedReply{}, "", lastErr
}

var errUnreachable = fmt.Errorf("no path to this node")

func remoteMsg(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(b))
}

// syncMember exchanges the whole record of gid with one member: the member
// merges it and answers with its merged record, merged here in turn.
func (ss *shareSync) syncMember(gid, iid string) {
	a := ss.s.app
	u, ok := a.SharedUserByGID(gid)
	if !ok {
		return
	}
	m, ok := u.Shared.Members[iid]
	if !ok || m.Left {
		return
	}
	rec := a.SharedRecordOf(u)
	rep, path, err := ss.s.callMember(u.Shared.Key, "sync", m.Name, sharedMsg{GID: gid, To: iid, From: a.SelfIID(), Record: &rec})
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		state := "error"
		if err == errUnreachable {
			state = "unreachable"
		}
		changed := false
		ss.set(gid, iid, func(st *memberStatus) {
			changed = st.Error != err.Error()
			st.State, st.Error, st.Path = state, err.Error(), path
		})
		if changed {
			log.Printf("[shared] sync of %s with %s: %v", u.Username, m.Name, err)
		}
		return
	}
	switch rep.State {
	case "ok":
		if rep.Record != nil && rep.Record.GID == gid {
			if _, _, _, news, err := a.MergeShared(*rep.Record); err == nil && news {
				go ss.pushOthers(gid, iid)
			}
		}
		ss.set(gid, iid, func(st *memberStatus) { st.State, st.Error, st.SyncedAt, st.Path = "synced", "", now, path })
	case "deleted":
		// deleted everywhere while this node did not hear of it
		a.DeleteSharedEverywhere(gid, iid)
		ss.forget(gid)
	case "absent":
		a.MarkMemberLeft(gid, iid)
		ss.drop(gid, iid)
	case "wrong_node":
		ss.set(gid, iid, func(st *memberStatus) {
			st.State, st.Error, st.Path = "error", "another node answers to "+m.Name, path
		})
	default:
		ss.set(gid, iid, func(st *memberStatus) { st.State, st.Error = "error", rep.Error })
	}
}

// pushOthers syncs gid with every member but except.
func (ss *shareSync) pushOthers(gid, except string) {
	u, ok := ss.s.app.SharedUserByGID(gid)
	if !ok {
		return
	}
	for iid, m := range u.Shared.Members {
		if iid != ss.s.app.SelfIID() && iid != except && !m.Left {
			ss.start(gid, iid)
		}
	}
}

func (ss *shareSync) drop(gid, iid string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.status[gid], iid)
}

func (ss *shareSync) forget(gid string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.status, gid)
	delete(ss.lastUsage, gid)
}

// roundTombs tells the members still holding a user deleted everywhere.
func (ss *shareSync) roundTombs() {
	a := ss.s.app
	for _, t := range a.SharedTombs() {
		for iid, m := range t.Pending {
			rep, path, err := ss.s.callMember(t.Key, "delete", m.Name, sharedMsg{GID: t.GID, To: iid, From: a.SelfIID()})
			if err != nil {
				log.Printf("[shared] deletion of %s for %s (%s): %v", t.Username, m.Name, path, err)
				continue
			}
			if rep.State == "deleted" || rep.State == "absent" {
				a.TombDone(t.GID, iid)
			}
		}
	}
}

// tellLeft tells the members that this node left gid (best effort: one that
// is not told finds out on its next sync).
func (s *Server) tellLeft(rec app.SharedRecord, key string) {
	self := s.app.SelfIID()
	for iid, m := range rec.Members {
		if iid == self || m.Left {
			continue
		}
		go s.callMember(key, "sync", m.Name, sharedMsg{GID: rec.GID, To: iid, From: self, Record: &rec})
	}
}

// --- receiving side: /api/shared-users/{join,sync,delete} ---

// sharedPushRoutes: the endpoints other holders call over the relay. They
// are outside authMiddleware: the gate and the signature are their access
// rules.
func (s *Server) sharedPushRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/shared-users/join", s.sharedGate(s.sharedJoin))
	m.HandleFunc("POST /api/shared-users/sync", s.sharedGate(s.sharedSync))
	m.HandleFunc("POST /api/shared-users/delete", s.sharedGate(s.sharedDelete))
}

// sharedGate admits only requests delivered over the relay by a node (the
// system password): never the public listener, a user or node access by
// user.
func (s *Server) sharedGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.Context().Value(relayCtxKey{})
		if raw == nil { // public listener: not even discoverable
			http.NotFound(w, r)
			return
		}
		if authID, _ := raw.(string); authID != "system" {
			sharedRefuse(w, 403, "shared users are not accepted over this connection")
			return
		}
		next(w, r)
	}
}

func sharedRefuse(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeSigned answers v signed with key ("" = unsigned).
func writeSigned(w http.ResponseWriter, key string, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	if key != "" {
		for k, val := range signHeaders(key, "reply", b) {
			w.Header().Set(k, val)
		}
	}
	w.WriteHeader(code)
	w.Write(b)
}

// sharedKey: the key of gid held here (as a user or a tomb).
func (s *Server) sharedKey(gid string) (key string, tomb bool) {
	if u, ok := s.app.SharedUserByGID(gid); ok {
		return u.Shared.Key, false
	}
	for _, t := range s.app.SharedTombs() {
		if t.GID == gid {
			return t.Key, true
		}
	}
	return "", false
}

// readShared reads and authenticates a signed message.
func (s *Server) readShared(w http.ResponseWriter, r *http.Request, kind string) (sharedMsg, string, bool, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var msg sharedMsg
	if err == nil {
		err = json.Unmarshal(body, &msg)
	}
	if err != nil || msg.GID == "" {
		sharedRefuse(w, 400, "bad request")
		return msg, "", false, false
	}
	if msg.To != s.app.SelfIID() {
		writeSigned(w, "", 409, sharedReply{State: "wrong_node"})
		return msg, "", false, false
	}
	key, tomb := s.sharedKey(msg.GID)
	if key == "" {
		writeSigned(w, "", 200, sharedReply{State: "absent"})
		return msg, "", false, false
	}
	if !sharedSigned(key, kind, r.Header, body) {
		sharedRefuse(w, 401, "bad signature")
		return msg, "", false, false
	}
	return msg, key, tomb, true
}

func (s *Server) sharedSync(w http.ResponseWriter, r *http.Request) {
	msg, key, tomb, ok := s.readShared(w, r, "sync")
	if !ok {
		return
	}
	if tomb {
		s.app.TombDone(msg.GID, msg.From) // the sender deletes it on this answer
		writeSigned(w, key, 200, sharedReply{State: "deleted"})
		return
	}
	if msg.Record == nil || msg.Record.GID != msg.GID {
		sharedRefuse(w, 400, "record required")
		return
	}
	out, state, conflict, news, err := s.app.MergeShared(*msg.Record)
	if err != nil {
		sharedRefuse(w, 500, err.Error())
		return
	}
	if state != "ok" {
		writeSigned(w, key, 200, sharedReply{State: state})
		return
	}
	if news {
		go s.shares().pushOthers(msg.GID, msg.From)
	}
	rep := sharedReply{State: "ok", Record: &out}
	if conflict != "" {
		rep.Error = conflict + " not applied here (taken)"
	}
	writeSigned(w, key, 200, rep)
}

func (s *Server) sharedDelete(w http.ResponseWriter, r *http.Request) {
	msg, key, tomb, ok := s.readShared(w, r, "delete")
	if !ok {
		return
	}
	if tomb {
		s.app.TombDone(msg.GID, msg.From)
	} else if _, err := s.app.DeleteSharedEverywhere(msg.GID, msg.From); err != nil {
		sharedRefuse(w, 500, err.Error())
		return
	}
	s.shares().forget(msg.GID)
	go s.shares().roundTombs()
	writeSigned(w, key, 200, sharedReply{State: "deleted"})
}

// joinMsg is the body of /api/shared-users/join (a first share).
type joinMsg struct {
	Record   app.SharedRecord `json:"record"`
	Key      string           `json:"key"`
	ExitNode string           `json:"exit_node,omitempty"`
}

// sharedJoin: a first share. The node must accept shared users, and the
// sharer must hold relay trust here or a session of this node.
func (s *Server) sharedJoin(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	if !cfg.AcceptSharedUsers {
		sharedRefuse(w, 403, "this node does not accept shared users")
		return
	}
	if !cfg.RelayAdminPassthrough && !s.validSession(r) {
		sharedRefuse(w, 403, "needs relay trust or a session on this node")
		return
	}
	var msg joinMsg
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&msg); err != nil || msg.Record.GID == "" {
		sharedRefuse(w, 400, "bad request")
		return
	}
	if k, err := hex.DecodeString(msg.Key); err != nil || len(k) != 32 {
		sharedRefuse(w, 400, "bad key")
		return
	}
	if u, ok := s.app.SharedUserByGID(msg.Record.GID); ok && u.Shared.Key != msg.Key {
		sharedRefuse(w, 409, "a different shared user with this id is held here")
		return
	}
	res, err := s.app.JoinShared(msg.Record, msg.Key, msg.ExitNode)
	if err != nil {
		sharedRefuse(w, 400, err.Error())
		return
	}
	if res.State != "ok" {
		sharedRefuse(w, 409, res.Error)
		return
	}
	writeJSON(w, res)
}

// validSession: the request carries a live session token of this node.
func (s *Server) validSession(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if len(h) <= 7 || h[:7] != "Bearer " {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	exp, ok := s.sessions[h[7:]]
	return ok && time.Now().Before(exp)
}

// --- management: share, leave, delete everywhere ---

// sharedView: a shared user's members and traffic as GET /api/users shows
// them.
type sharedView struct {
	Members   []sharedMemberView `json:"members"`
	TotalUsed int64              `json:"total_used"`
	// PeriodStart (RFC3339): the start of the current traffic period
	// ("" = since the start).
	PeriodStart string `json:"period_start,omitempty"`
}

type sharedMemberView struct {
	Name     string `json:"name"`
	NodeID   string `json:"node_id,omitempty"`
	Self     bool   `json:"self,omitempty"`
	Used     int64  `json:"used"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
	SyncedAt string `json:"synced_at,omitempty"`
}

func (s *Server) sharedViewOf(u app.UserConfig) *sharedView {
	if u.Shared == nil {
		return nil
	}
	self := s.app.SelfIID()
	usage := s.app.SharedUsage(u)
	ss := s.shares()
	v := &sharedView{Members: []sharedMemberView{}}
	if u.Shared.Epoch > 0 {
		v.PeriodStart = time.UnixMilli(u.Shared.Epoch).UTC().Format(time.RFC3339)
	}
	for _, b := range usage {
		v.TotalUsed += b
	}
	for iid, m := range u.Shared.Members {
		if m.Left {
			continue
		}
		mv := sharedMemberView{Name: m.Name, NodeID: m.NodeID, Used: usage[iid]}
		if iid == self {
			mv.Self, mv.State = true, "synced"
		} else {
			st := ss.statusOf(u.Shared.GID, iid)
			mv.State, mv.Error, mv.SyncedAt = st.State, st.Error, st.SyncedAt
		}
		v.Members = append(v.Members, mv)
	}
	sortMembers(v.Members)
	return v
}

func sortMembers(m []sharedMemberView) {
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && memberLess(m[j], m[j-1]); j-- {
			m[j], m[j-1] = m[j-1], m[j]
		}
	}
}

func memberLess(a, b sharedMemberView) bool {
	if a.Self != b.Self {
		return a.Self
	}
	return a.Name < b.Name
}

// exitNodeOf: the node a shared user's exit starts from on a node it is
// shared to — the last hop of its first exit ("" = direct: no exit, or a
// policy).
func exitNodeOf(u app.UserConfig) string {
	if u.ExitPolicy != "" {
		return ""
	}
	p := u.ExitVia
	if len(u.ExitPaths) > 0 {
		p = u.ExitPaths[0]
	}
	if p == "" {
		return ""
	}
	hops := strings.Split(p, "/")
	return hops[len(hops)-1]
}

// shareUser: POST /api/users/{id}/share {chain, token} — a first share of
// the user to the node at the end of chain. token: a session of that node
// the browser holds (not needed when it trusts this node's relay).
func (s *Server) shareUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Chain []string `json:"chain"`
		Token string   `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Chain) == 0 {
		http.Error(w, "chain required", 400)
		return
	}
	cfg := s.app.Store().Get()
	seen := map[string]bool{cfg.NodeID: true, s.app.Node().Name(): true}
	for _, h := range body.Chain {
		if h == "" || seen[h] {
			http.Error(w, "invalid chain", 400)
			return
		}
		seen[h] = true
	}
	if s.app.Node().IsInvisible(body.Chain[0]) {
		http.Error(w, "cannot share over node access by user", 400)
		return
	}
	before, ok := s.userByID(id)
	if !ok {
		http.Error(w, "user not found", 404)
		return
	}
	u, err := s.app.MakeShared(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	failed := func(code int, msg string) {
		if before.Shared == nil {
			s.app.UnshareIfAlone(id)
		}
		sharedRefuse(w, code, msg)
	}
	jb, _ := json.Marshal(joinMsg{Record: s.app.SharedRecordOf(u), Key: u.Shared.Key, ExitNode: exitNodeOf(u)})
	hdr := map[string]string{}
	if body.Token != "" {
		hdr["Authorization"] = "Bearer " + body.Token
	}
	target := body.Chain[len(body.Chain)-1]
	code, _, resp, err := s.relayCall(body.Chain, "POST", "/api/shared-users/join", jb, hdr)
	switch {
	case err != nil:
		failed(502, err.Error())
		return
	case code == 404 || code == 405 || code == 302:
		failed(409, target+" does not support shared users (older version)")
		return
	case code != 200:
		failed(409, target+": "+remoteMsg(resp))
		return
	}
	var res app.JoinResult
	if err := json.Unmarshal(resp, &res); err != nil || res.IID == "" {
		failed(502, target+": bad answer")
		return
	}
	if res.IID == s.app.SelfIID() {
		failed(400, "that is this node")
		return
	}
	if err := s.app.AddSharedMember(u.Shared.GID, res.IID, res.Member); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.app.MergeShared(res.Record)
	ss := s.shares()
	ss.set(u.Shared.GID, res.IID, func(st *memberStatus) {
		st.State, st.Error, st.SyncedAt, st.Path = "synced", "", time.Now().UTC().Format(time.RFC3339), strings.Join(body.Chain, "/")
	})
	log.Printf("[shared] user %s shared to %s", u.Username, target)
	go ss.pushOthers(u.Shared.GID, res.IID)
	writeJSON(w, map[string]string{"status": "ok"})
}

// deleteUserEverywhere: POST /api/users/{id}/delete-everywhere.
func (s *Server) deleteUserEverywhere(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByID(r.PathValue("id"))
	if !ok {
		http.Error(w, "user not found", 404)
		return
	}
	if u.Shared == nil {
		http.Error(w, "not a shared user", 400)
		return
	}
	if _, err := s.app.DeleteSharedEverywhere(u.Shared.GID, ""); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.shares().forget(u.Shared.GID)
	go s.shares().roundTombs()
	writeJSON(w, map[string]string{"status": "ok"})
}

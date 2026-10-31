// Add-on users: shared users — a user owned by this node (the manager) and
// pushed to other nodes over the relay mesh, or received from a manager.
// See docs/v1.4.0-shared-users.md.

package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
)

const (
	sharePushTimeout = 10 * time.Second
	shareRetryEvery  = 30 * time.Second
	shareMaxTargets  = 64
)

// shareStatus: one target of a user shared out, as last seen here.
type shareStatus struct {
	Target      string `json:"target"`
	State       string `json:"state"` // synced | pending | error | conflict
	Rev         int64  `json:"rev"`   // revision the target holds (0 = none yet)
	Error       string `json:"error,omitempty"`
	SyncedAt    string `json:"synced_at,omitempty"`
	TrafficUsed int64  `json:"traffic_used"`
}

type shareFlight struct {
	again bool
	done  chan struct{}
}

// shareSync keeps the targets of shared-out users in step: every change is
// pushed at once, failures are retried every shareRetryEvery. Pushes and
// deletions of one (user, target) never overlap.
type shareSync struct {
	s       *Server
	mu      sync.Mutex
	status  map[string]map[string]*shareStatus // user id → target → status
	flights map[string]*shareFlight            // user id + "\x00" + target
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
		ss = &shareSync{s: s, status: map[string]map[string]*shareStatus{}, flights: map[string]*shareFlight{}}
		shareSyncs[s] = ss
	}
	return ss
}

// startShareSync runs the retry loop for the life of the process.
func (s *Server) startShareSync() {
	ss := s.shares()
	go func() {
		time.Sleep(10 * time.Second) // let the peers connect first
		seen := map[string]bool{}
		last := time.Time{}
		for {
			// a retry round every shareRetryEvery, and at once when a
			// direct peer (re)connects
			now := map[string]bool{}
			fresh := false
			for _, p := range s.app.Node().Peers() {
				now[p.Name] = true
				if !seen[p.Name] {
					fresh = true
				}
			}
			seen = now
			if fresh || time.Since(last) >= shareRetryEvery {
				ss.round()
				last = time.Now()
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

// round starts every (user, target) not known to be in step.
func (ss *shareSync) round() {
	cfg := ss.s.app.Store().Get()
	for _, u := range cfg.Users {
		for _, t := range u.ShareTargets {
			ss.mu.Lock()
			st := ss.status[u.ID][t]
			stale := st == nil || st.State != "synced" || st.Rev < u.ShareRev
			ss.mu.Unlock()
			if stale {
				ss.start(u.ID, t)
			}
		}
	}
	for _, d := range cfg.SharedDeletes {
		ss.start(d.UserID, d.Target)
	}
}

// start syncs (id, target) in the background; the channel closes once the
// latest state has been pushed (or failed).
func (ss *shareSync) start(id, target string) <-chan struct{} {
	key := id + "\x00" + target
	ss.mu.Lock()
	if f := ss.flights[key]; f != nil {
		f.again = true
		ss.mu.Unlock()
		return f.done
	}
	f := &shareFlight{done: make(chan struct{})}
	ss.flights[key] = f
	ss.mu.Unlock()
	go func() {
		for {
			ss.syncOne(id, target)
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

func (ss *shareSync) set(id, target string, fn func(st *shareStatus)) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	m := ss.status[id]
	if m == nil {
		m = map[string]*shareStatus{}
		ss.status[id] = m
	}
	st := m[target]
	if st == nil {
		st = &shareStatus{Target: target, State: "pending"}
		m[target] = st
	}
	fn(st)
}

func (ss *shareSync) drop(id, target string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if m := ss.status[id]; m != nil {
		delete(m, target)
		if len(m) == 0 {
			delete(ss.status, id)
		}
	}
}

// syncOne brings one target in step with the current config: an upsert
// while it is a target of the user, a deletion while one is queued.
func (ss *shareSync) syncOne(id, target string) {
	cfg := ss.s.app.Store().Get()
	var user *app.UserConfig
	for i := range cfg.Users {
		if cfg.Users[i].ID == id && cfg.Users[i].SharedFrom == nil {
			user = &cfg.Users[i]
		}
	}
	if user != nil && contains(user.ShareTargets, target) {
		ss.pushUser(cfg, *user, target)
		return
	}
	for _, d := range cfg.SharedDeletes {
		if d.UserID == id && d.Target == target {
			ss.deleteUser(cfg, d)
			return
		}
	}
	ss.drop(id, target)
}

func contains(l []string, v string) bool {
	for _, e := range l {
		if e == v {
			return true
		}
	}
	return false
}

func sharedPath(managerID, userID string) string {
	return "/api/shared-users/" + url.PathEscape(managerID) + "/" + url.PathEscape(userID)
}

// sharePush is the body of PUT /api/shared-users/{manager}/{id}.
type sharePush struct {
	ManagerName string         `json:"manager_name"`
	Rev         int64          `json:"rev"`
	ResetRev    int64          `json:"reset_rev"`
	User        app.UserConfig `json:"user"`
}

func (ss *shareSync) pushUser(cfg app.Config, u app.UserConfig, target string) {
	body := sharePush{ManagerName: ss.s.app.Node().Name(), Rev: u.ShareRev, ResetRev: u.ShareResetRev, User: app.SharedPayload(u)}
	code, resp, err := ss.s.relayAPI(target, "PUT", sharedPath(cfg.NodeID, u.ID), body)
	var res app.SharedApply
	if err == nil && code == 200 {
		err = json.Unmarshal(resp, &res)
	}
	now := time.Now().Format(time.RFC3339)
	ss.set(u.ID, target, func(st *shareStatus) {
		switch {
		case err != nil:
			st.State, st.Error = "error", err.Error()
		case code == 200 && res.State == "synced":
			st.State, st.Error, st.Rev, st.SyncedAt, st.TrafficUsed = "synced", "", res.Rev, now, res.TrafficUsed
		case code == 200 && res.State == "stale":
			st.State, st.Rev = "error", res.Rev
			st.Error = fmt.Sprintf("target holds a newer revision (%d)", res.Rev)
		case code == 409:
			st.State, st.Error = "conflict", remoteMsg(resp)
			if json.Unmarshal(resp, &res) == nil && res.Error != "" {
				st.Error = res.Error
			}
		default:
			st.State, st.Error = "error", shareRemoteError(code, resp)
		}
	})
	if err != nil || code != 200 || res.State != "synced" {
		log.Printf("[shared] push of %s to %s: %d %v %s", u.Username, target, code, err, strings.TrimSpace(string(resp)))
	}
}

func (ss *shareSync) deleteUser(cfg app.Config, d app.SharedDelete) {
	code, resp, err := ss.s.relayAPI(d.Target, "DELETE", sharedPath(cfg.NodeID, d.UserID), nil)
	if err == nil && code == 200 {
		ss.s.app.DoneSharedDelete(d)
		ss.drop(d.UserID, d.Target)
		return
	}
	msg := shareRemoteError(code, resp)
	if err != nil {
		msg = err.Error()
	}
	ss.set(d.UserID, d.Target, func(st *shareStatus) { st.State, st.Error = "error", "delete: "+msg })
	log.Printf("[shared] delete of %s on %s: %s", d.UserID, d.Target, msg)
}

func remoteMsg(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(b))
}

// shareRemoteError explains a refused push.
func shareRemoteError(code int, body []byte) string {
	switch code {
	case 401, 403:
		return "target does not trust relay management: " + remoteMsg(body)
	case 404, 302, 405:
		return "target does not support shared users (older version)"
	}
	return fmt.Sprintf("target answered %d: %s", code, remoteMsg(body))
}

// relayAPI sends one request to the management API of the node at the end
// of the relay chain target ("a/b") and returns its status and body.
func (s *Server) relayAPI(target, method, path string, body any) (int, []byte, error) {
	chain := strings.Split(target, "/")
	s.mu.RLock()
	for i, name := range chain {
		if newID, ok := s.oldNodeIDs[name]; ok {
			chain[i] = newID
		}
	}
	s.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), sharePushTimeout)
	defer cancel()
	conn, _, err := s.dialRemoteAPI(ctx, chain)
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://127.0.0.1:5565/scale"+path, rd)
	req.Host = "127.0.0.1:5565"
	req.Header.Set("Content-Type", "application/json")
	req.Close = true
	if err := req.Write(conn); err != nil {
		return 0, nil, fmt.Errorf("write to %s: %v", target, err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return 0, nil, fmt.Errorf("no answer from %s: %v", target, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, b, nil
}

// shareChanged pushes a change of a user shared out to all its targets.
func (s *Server) shareChanged(id string, manualReset bool) {
	s.shareSyncTargets(id, s.app.BumpShareRev(id, manualReset))
}

func (s *Server) shareSyncTargets(id string, targets []string) {
	ss := s.shares()
	for _, t := range targets {
		ss.start(id, t)
	}
}

// --- manager side: GET / PUT /api/users/{id}/share ---

type shareView struct {
	UserID         string        `json:"user_id"`
	Rev            int64         `json:"rev"`
	Targets        []shareStatus `json:"targets"`
	PendingDeletes []string      `json:"pending_deletes"`
}

func (s *Server) shareView(u app.UserConfig) shareView {
	ss := s.shares()
	v := shareView{UserID: u.ID, Rev: u.ShareRev, Targets: []shareStatus{}, PendingDeletes: []string{}}
	ss.mu.Lock()
	for _, t := range u.ShareTargets {
		st := shareStatus{Target: t, State: "pending"}
		if cur := ss.status[u.ID][t]; cur != nil {
			st = *cur
		}
		if st.State == "synced" && st.Rev < u.ShareRev {
			st.State = "pending"
		}
		v.Targets = append(v.Targets, st)
	}
	ss.mu.Unlock()
	for _, d := range s.app.Store().Get().SharedDeletes {
		if d.UserID == u.ID {
			v.PendingDeletes = append(v.PendingDeletes, d.Target)
		}
	}
	return v
}

// refreshShare reads each synced target's own view of the user (traffic
// counted there), in parallel.
func (s *Server) refreshShare(u app.UserConfig) {
	cfg := s.app.Store().Get()
	ss := s.shares()
	var wg sync.WaitGroup
	for _, t := range u.ShareTargets {
		wg.Add(1)
		go func(t string) {
			defer wg.Done()
			code, resp, err := s.relayAPI(t, "GET", sharedPath(cfg.NodeID, u.ID), nil)
			if err != nil || code != 200 {
				return
			}
			var got struct {
				Exists      bool  `json:"exists"`
				Rev         int64 `json:"rev"`
				TrafficUsed int64 `json:"traffic_used"`
			}
			if json.Unmarshal(resp, &got) != nil {
				return
			}
			gone := false
			ss.set(u.ID, t, func(st *shareStatus) {
				if !got.Exists {
					if st.State == "synced" {
						st.State, st.Rev, st.Error = "pending", 0, "missing on target"
						gone = true
					}
					return
				}
				st.Rev, st.TrafficUsed = got.Rev, got.TrafficUsed
			})
			if gone {
				ss.start(u.ID, t)
			}
		}(t)
	}
	wg.Wait()
}

func (s *Server) getUserShare(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByID(r.PathValue("id"))
	if !ok {
		http.Error(w, "user not found", 404)
		return
	}
	if r.URL.Query().Get("refresh") != "0" {
		s.refreshShare(u)
	}
	writeJSON(w, s.shareView(u))
}

func (s *Server) putUserShare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Targets []string `json:"targets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	u, ok := s.userByID(id)
	if !ok {
		http.Error(w, "user not found", 404)
		return
	}
	if refuseShared(w, u) {
		return
	}
	cfg := s.app.Store().Get()
	var targets []string
	for _, t := range body.Targets {
		n, err := app.NormalizeShareTarget(t, cfg.NodeID, s.app.Node().Name())
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if !contains(targets, n) {
			targets = append(targets, n)
		}
	}
	if len(targets) > shareMaxTargets {
		http.Error(w, fmt.Sprintf("at most %d targets", shareMaxTargets), 400)
		return
	}
	removed, err := s.app.SetShareTargets(id, targets)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// push now and answer with the outcome (pushes still running after the
	// wait go on in the background)
	ss := s.shares()
	var dones []<-chan struct{}
	for _, t := range append(targets, removed...) {
		dones = append(dones, ss.start(id, t))
	}
	deadline := time.After(sharePushTimeout + 2*time.Second)
wait:
	for _, d := range dones {
		select {
		case <-d:
		case <-deadline:
			break wait
		}
	}
	u, _ = s.userByID(id)
	writeJSON(w, s.shareView(u))
}

// --- receiving side: /api/shared-users/{manager}/{id} ---

// sharedPushRoutes: the endpoints a manager calls over the relay. They are
// outside authMiddleware: the gate below is their only access rule.
func (s *Server) sharedPushRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/shared-users/{manager}/{id}", s.sharedGate(s.getSharedUser))
	m.HandleFunc("PUT /api/shared-users/{manager}/{id}", s.sharedGate(s.putSharedUser))
	m.HandleFunc("DELETE /api/shared-users/{manager}/{id}", s.sharedGate(s.deleteSharedUser))
}

// sharedGate admits only what relay trust admits: a request delivered over
// the relay by a node (system password) or a full node-access user, on a
// node with relay trust (relay_admin_passthrough) on.
func (s *Server) sharedGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.Context().Value(relayCtxKey{})
		if raw == nil { // public listener: not even discoverable
			http.NotFound(w, r)
			return
		}
		authID, _ := raw.(string)
		if !trustedRelay(authID) {
			shareRefuse(w, "shared users are accepted only from a trusted node")
			return
		}
		if !s.app.Store().Get().RelayAdminPassthrough {
			shareRefuse(w, "relay trust (relay_admin_passthrough) is off on this node")
			return
		}
		next(w, r)
	}
}

func shareRefuse(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(403)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) getSharedUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.app.FindSharedUser(r.PathValue("manager"), r.PathValue("id"))
	if !ok {
		writeJSON(w, map[string]any{"exists": false})
		return
	}
	writeJSON(w, map[string]any{
		"exists": true, "id": u.ID, "username": u.Username, "enabled": u.Enabled,
		"rev": u.SharedFrom.Rev, "traffic_used": u.TrafficUsed,
		"exit_via": u.ExitVia, "exit_paths": u.ExitPaths, "exit_mode": u.ExitMode,
	})
}

func (s *Server) putSharedUser(w http.ResponseWriter, r *http.Request) {
	var p sharePush
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if p.Rev <= 0 || p.User.Username == "" || p.User.Password == "" {
		http.Error(w, "rev, username and password required", 400)
		return
	}
	if _, err := app.ValidateTrafficReset(p.User.TrafficReset, p.User.TrafficResetDay); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	res, err := s.app.ApplySharedUser(r.PathValue("manager"), p.ManagerName, r.PathValue("id"), p.Rev, p.ResetRev, p.User)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if res.State == "conflict" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(res)
		return
	}
	writeJSON(w, res)
}

func (s *Server) deleteSharedUser(w http.ResponseWriter, r *http.Request) {
	found, err := s.app.RemoveSharedUser(r.PathValue("manager"), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"deleted": found})
}

// --- receiving side: local edits of a shared user ---

// sharedProtected: the fields of a shared user only its manager changes.
var sharedProtected = []string{"username", "password", "proxy_passwords", "proxy_disabled", "traffic_limit",
	"expiry_date", "enabled", "node_access", "node_access_mode", "deny_internal", "traffic_reset", "traffic_reset_day"}

func jsonZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case float64:
		return x == 0
	case bool:
		return !x
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// updateSharedUserExit: a PUT on a shared user may change only its exit
// settings; any other field sent with a different value is refused.
func (s *Server) updateSharedUserExit(w http.ResponseWriter, cur, u app.UserConfig, raw []byte) {
	var sent map[string]any
	if err := json.Unmarshal(raw, &sent); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var have map[string]any
	b, _ := json.Marshal(cur)
	json.Unmarshal(b, &have)
	for _, k := range sharedProtected {
		v, ok := sent[k]
		if !ok {
			continue
		}
		if !(jsonZero(v) && jsonZero(have[k])) && !reflect.DeepEqual(v, have[k]) {
			http.Error(w, "shared user: managed by "+cur.SharedFrom.ManagerName()+" (only exit settings can be changed here)", 403)
			return
		}
	}
	if len(u.ExitPaths) > 0 {
		u.ExitVia = u.ExitPaths[0]
	} else if u.ExitVia != "" {
		u.ExitPaths = []string{u.ExitVia}
	}
	mode, err := app.SanitizeExitMode(u.ExitPaths, u.ExitMode)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	u.ExitMode = mode
	merged := cur
	merged.ExitVia, merged.ExitPaths, merged.ExitMode = u.ExitVia, u.ExitPaths, u.ExitMode
	if err := s.app.UpdateUser(cur.ID, merged); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

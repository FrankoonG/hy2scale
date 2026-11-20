// Add-on users: users, their devices and sessions.

package api

import (
	"time"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/FrankoonG/hy2scale/internal/app"
)

// userRoutes: the routes of the users add-on.
func (s *Server) userRoutes(m *http.ServeMux) {
	// Sessions (active connections)
	m.HandleFunc("GET /api/sessions", s.getSessions)
	m.HandleFunc("DELETE /api/sessions/{id}", s.kickSession)
	// Users
	m.HandleFunc("GET /api/users", s.getUsers)
	m.HandleFunc("POST /api/users", s.addUserAPI)
	m.HandleFunc("PUT /api/users/{id}", s.updateUserAPI)
	m.HandleFunc("DELETE /api/users/{id}", s.removeUserAPI)
	m.HandleFunc("PUT /api/users/{id}/toggle", s.toggleUserAPI)
	m.HandleFunc("GET /api/users/conflicts", s.getUserConflicts)
	m.HandleFunc("PUT /api/users/{id}/reset-traffic", s.resetUserTrafficAPI)
	m.HandleFunc("POST /api/users/{id}/extend", s.extendUserAPI)
	// Shared users (shared_users.go): a first share to another node, and
	// deletion on every holder (DELETE above: this node only).
	m.HandleFunc("POST /api/users/{id}/share", s.shareUser)
	m.HandleFunc("POST /api/users/{id}/delete-everywhere", s.deleteUserEverywhere)
	s.startShareSync()
}

// userView: a user as GET /api/users shows it.
type userView struct {
	app.UserConfig
	// TrafficResetNext (RFC3339): the next periodic reset, computed.
	TrafficResetNext string `json:"traffic_reset_next,omitempty"`
	// Shared: set for a shared user (traffic_used is then its total).
	Shared *sharedView `json:"shared,omitempty"`
}

func (s *Server) userByID(id string) (app.UserConfig, bool) {
	for _, u := range s.app.Store().Get().Users {
		if u.ID == id {
			return u, true
		}
	}
	return app.UserConfig{}, false
}

// validateTrafficReset normalizes the reset period of u (false: answered
// 400/409). A shared user's period runs in UTC; one of several units needs
// every holder to understand it.
func validateTrafficReset(w http.ResponseWriter, u *app.UserConfig, old *app.UserConfig, self string) bool {
	today := app.TrafficNow()
	if old != nil && old.Shared != nil {
		today = today.UTC()
	}
	if err := app.NormalizeTrafficReset(u, today); err != nil {
		http.Error(w, err.Error(), 400)
		return false
	}
	if old != nil && old.Shared != nil && u.ResetEvery() > 1 && app.ResetSettingChanged(u, old) {
		if lack := app.SharedLacksKinds(*old, self); len(lack) > 0 {
			http.Error(w, "nodes holding this shared user are too old for this reset period: "+strings.Join(lack, ", "), 409)
			return false
		}
	}
	return true
}

func (s *Server) getSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"devices": s.app.Devices(),
		"total":   s.app.Sessions.Count(),
	})
}

func (s *Server) kickSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.app.Sessions.Kick(id) {
		writeJSON(w, map[string]string{"status": "ok"})
	} else {
		http.Error(w, "session not found", 404)
	}
}

func (s *Server) getUsers(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	now := app.TrafficNow()
	users := make([]userView, 0, len(cfg.Users))
	for i := range cfg.Users {
		v := userView{UserConfig: cfg.Users[i], TrafficResetNext: app.TrafficResetNextOf(&cfg.Users[i], now)}
		v.ExitPolicy = cfg.Users[i].SinglePolicy() // for clients of the old model
		if v.TrafficReset != "" {
			v.TrafficResetEvery, v.TrafficResetAnchor = v.ResetEvery(), v.ResetAnchor()
		}
		if v.Shared = s.sharedViewOf(cfg.Users[i]); v.Shared != nil {
			v.TrafficUsed = v.Shared.TotalUsed
			v.TrafficResetNext = app.SharedNextReset(&cfg.Users[i], now)
		}
		users = append(users, v)
	}
	writeJSON(w, users)
}

func (s *Server) getUserConflicts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.app.GetPasswordConflicts())
}

func (s *Server) addUserAPI(w http.ResponseWriter, r *http.Request) {
	var u app.UserConfig
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(u.ExitPaths) > 0 {
		u.ExitVia = u.ExitPaths[0]
	} else if u.ExitVia != "" {
		u.ExitPaths = []string{u.ExitVia}
	}
	if u.Username == "" {
		http.Error(w, "username required", 400)
		return
	}
	if err := s.app.CheckExitPolicy(u.ExitPolicy); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if !s.normalizeKind(w, &u, nil) {
		return
	}
	if !validateTrafficReset(w, &u, nil, "") {
		return
	}
	// server-managed
	u.TrafficResetAt, u.TrafficResetFrom = "", ""
	u.Shared = nil
	if sanitized, err := app.SanitizeExitMode(u.ExitPaths, u.ExitMode); err != nil {
		http.Error(w, err.Error(), 400)
		return
	} else {
		u.ExitMode = sanitized
	}
	if u.ID == "" {
		b := make([]byte, 4)
		rand.Read(b)
		u.ID = hex.EncodeToString(b)
	}
	if err := s.app.AddUser(u); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) updateUserAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var u app.UserConfig
	if err := json.Unmarshal(raw, &u); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	u.ID = id
	if err := s.app.CheckExitPolicy(u.ExitPolicy); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(u.ExitPaths) > 0 {
		u.ExitVia = u.ExitPaths[0]
	} else if u.ExitVia != "" {
		u.ExitPaths = []string{u.ExitVia}
	}
	old, ok := s.userByID(id)
	if !ok {
		http.Error(w, "user not found", 404)
		return
	}
	// a partial body (an import, an older client) keeps what it leaves out
	var keys map[string]json.RawMessage
	json.Unmarshal(raw, &keys)
	if _, ok := keys["sub"]; !ok {
		u.Sub = old.Sub
	}
	if _, ok := keys["exit_kind"]; !ok && u.ExitPolicy == "" && !u.NodeAccess {
		u.ExitKind = old.ExitKind
	}
	if _, ok := keys["rows"]; !ok {
		u.Rows = old.Rows
	}
	if _, ok := keys["row_policy"]; !ok {
		u.RowPolicy = old.RowPolicy
	}
	if _, ok := keys["proxy_disabled"]; !ok {
		u.ProxyDisabled = old.ProxyDisabled
	}
	if _, ok := keys["enabled"]; !ok {
		u.Enabled = old.Enabled
	}
	if _, ok := keys["traffic_reset"]; !ok {
		u.TrafficReset, u.TrafficResetEvery, u.TrafficResetAnchor, u.TrafficResetDay =
			old.TrafficReset, old.TrafficResetEvery, old.TrafficResetAnchor, old.TrafficResetDay
	}
	if !validateTrafficReset(w, &u, &old, s.app.SelfIID()) {
		return
	}
	// a client of the old model clearing the user's policy set: the exit group
	if v, ok := keys["exit_policy"]; ok && string(v) == `""` && old.SinglePolicy() != "" && u.ExitKind == app.KindPolicy {
		u.ExitKind = app.KindGroup
	}
	if !s.normalizeKind(w, &u, &old) {
		return
	}
	if sanitized, err := app.SanitizeExitMode(u.ExitPaths, u.ExitMode); err != nil {
		http.Error(w, err.Error(), 400)
		return
	} else {
		u.ExitMode = sanitized
	}
	if err := s.app.UpdateUser(id, u); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) removeUserAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if cur, ok := s.userByID(id); ok && cur.Shared != nil {
		// a shared user: this node leaves, the others keep it
		rec, key, err := s.app.LeaveShared(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.shares().forget(rec.GID)
		s.tellLeft(rec, key)
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	if err := s.app.RemoveUser(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) toggleUserAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.ToggleUser(id, body.Enabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// extendUserAPI moves the user's expiry date on by one reset period (from
// today when passed); answers the new date.
func (s *Server) extendUserAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, ok := s.userByID(id)
	if !ok {
		http.Error(w, "user not found", 404)
		return
	}
	d := app.ExtendedExpiry(&u, time.Now())
	if d == "" {
		http.Error(w, "the user never expires", 400)
		return
	}
	u.ExpiryDate = d
	if err := s.app.UpdateUser(id, u); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"expiry_date": d})
}

func (s *Server) resetUserTrafficAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.app.ResetUserTraffic(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// normalizeKind checks and completes the exit kind of a user being saved
// (old: the stored user, nil when new): the kind's fields, its rows, their
// policy sets here, a usable username, and — for a shared user — members
// that understand exit kinds.
func (s *Server) normalizeKind(w http.ResponseWriter, u *app.UserConfig, old *app.UserConfig) bool {
	for _, c := range u.Username {
		if c < 0x20 || c == 0x7f {
			http.Error(w, "username: no control characters", 400)
			return false
		}
	}
	// a client of the old model setting a policy set on the whole user
	// (every row goes there), or clearing it (back to the exit group)
	if u.ExitPolicy != "" && (old == nil || u.ExitPolicy != old.SinglePolicy()) {
		u.ExitKind = app.KindPolicy
		if len(u.Rows) == 0 {
			u.Rows = []app.UserRow{{ID: "r1", Password: u.Password}}
		}
		u.RowPolicy = map[string]string{}
		for _, r := range u.Rows {
			u.RowPolicy[r.ID] = u.ExitPolicy
		}
	}
	if u.ExitKind == "" && u.NodeAccess {
		u.ExitKind = app.KindAccess // a client of the old model turning node access on
	}
	if u.ExitKind == "" {
		u.ExitKind = app.KindGroup
		if old != nil {
			u.ExitKind = old.Kind()
		}
	}
	u.ExitPolicy = ""
	if u.Sub != nil {
		if u.ExitKind == app.KindAccess {
			u.Sub = nil // node access has no subscription
		} else {
			if err := app.ValidateSubName(u.Sub.Name); err != nil {
				http.Error(w, err.Error(), 400)
				return false
			}
			if on := u.Sub.Enabled; on != nil {
				u.Sub.Token = "" // never the client's: kept while on, else a new one
				if *on && old != nil && old.Sub != nil {
					u.Sub.Token = old.Sub.Token
				}
				if *on && u.Sub.Token == "" {
					u.Sub.Token = app.NewSubToken()
				}
			}
			u.Sub.Enabled = nil
			if *u.Sub == (app.UserSub{}) {
				u.Sub = nil
			}
		}
	}
	switch u.ExitKind {
	case app.KindGroup, app.KindAccess:
		if u.Password == "" {
			http.Error(w, "password required", 400)
			return false
		}
	case app.KindPolicy:
		if len(u.Rows) == 0 {
			http.Error(w, "at least one password row required", 400)
			return false
		}
		seen := map[string]bool{}
		ids := map[string]bool{}
		for i := range u.Rows {
			r := &u.Rows[i]
			if r.Password == "" {
				http.Error(w, "every row needs a password", 400)
				return false
			}
			if seen[r.Password] {
				http.Error(w, "two rows share a password", 400)
				return false
			}
			seen[r.Password] = true
			if r.ID == "" || ids[r.ID] {
				b := make([]byte, 3)
				rand.Read(b)
				r.ID = "r" + hex.EncodeToString(b)
			}
			ids[r.ID] = true
		}
		rp := map[string]string{}
		for id, pol := range u.RowPolicy {
			if !ids[id] || pol == "" {
				continue
			}
			if err := s.app.CheckExitPolicy(pol); err != nil {
				http.Error(w, err.Error(), 400)
				return false
			}
			rp[id] = pol
		}
		u.RowPolicy = rp
	default:
		http.Error(w, "exit_kind: group, policy or access", 400)
		return false
	}
	// node access is the access kind (mirrored for older holders)
	u.NodeAccess = u.ExitKind == app.KindAccess
	if !u.NodeAccess {
		u.NodeAccessMode = ""
	}
	if cur := s.app.Store().Get(); cur.Server != nil && cur.Server.Password != "" {
		for _, c := range u.Creds() {
			if c.Password == cur.Server.Password {
				http.Error(w, "user password collides with the hy2 server password — pick a different password", 400)
				return false
			}
		}
	}
	// a shared user leaves exit groups only when every member understands it
	if old != nil && old.Shared != nil && (u.ExitKind != app.KindGroup || len(u.Rows) > 1) {
		if lack := app.SharedLacksKinds(*old, s.app.SelfIID()); len(lack) > 0 {
			http.Error(w, "nodes holding this shared user are too old for this exit mode: "+strings.Join(lack, ", "), 409)
			return false
		}
	}
	return true
}

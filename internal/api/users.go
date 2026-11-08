// Add-on users: users, their devices and sessions.

package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

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

// validateTrafficReset normalizes the reset period of u (false: answered 400).
func validateTrafficReset(w http.ResponseWriter, u *app.UserConfig) bool {
	day, err := app.ValidateTrafficReset(u.TrafficReset, u.TrafficResetDay)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return false
	}
	u.TrafficResetDay = day
	return true
}

func (s *Server) getSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"devices": s.app.Sessions.List(),
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
	if u.Username == "" || u.Password == "" {
		http.Error(w, "username and password required", 400)
		return
	}
	if err := s.app.CheckExitPolicy(u.ExitPolicy); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if !validateTrafficReset(w, &u) {
		return
	}
	// server-managed
	u.TrafficResetAt, u.TrafficResetFrom = "", ""
	u.Shared = nil
	// Block the user/server-password collision from the user-side too
	// (the inverse check lives in updateNode).
	if cur := s.app.Store().Get(); cur.Server != nil && cur.Server.Password != "" && cur.Server.Password == u.Password {
		http.Error(w, "user password collides with the hy2 server password — pick a different password", 400)
		return
	}
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
	if !validateTrafficReset(w, &u) {
		return
	}
	if err := s.app.CheckExitPolicy(u.ExitPolicy); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(u.ExitPaths) > 0 {
		u.ExitVia = u.ExitPaths[0]
	} else if u.ExitVia != "" {
		u.ExitPaths = []string{u.ExitVia}
	}
	if u.Password != "" {
		if cur := s.app.Store().Get(); cur.Server != nil && cur.Server.Password != "" && cur.Server.Password == u.Password {
			http.Error(w, "user password collides with the hy2 server password — pick a different password", 400)
			return
		}
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

func (s *Server) resetUserTrafficAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.app.ResetUserTraffic(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

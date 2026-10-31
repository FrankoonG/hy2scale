// Add-on web: sign-in, sessions of the web UI, account.

package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

// webRoutes: the routes of the web add-on (account).
func (s *Server) webRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/settings/web", s.getWebSettings)
	m.HandleFunc("PUT /api/settings/web", s.updateWebSettings)
	m.HandleFunc("GET /api/me", s.getMe)
	m.HandleFunc("PUT /api/settings/password", s.changePassword)
	m.HandleFunc("POST /api/logout", s.logout)
}

// logout ends the session of the request's bearer token.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
		s.mu.Lock()
		delete(s.sessions, h[7:])
		s.mu.Unlock()
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Client sends SHA-256(password) hex; compare against stored hash
	s.mu.RLock()
	ok := subtle.ConstantTimeCompare([]byte(body.Username), []byte(s.username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.passHash)) == 1
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "invalid credentials", 401)
		return
	}
	token := s.generateToken()
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(s.sessionTimeout())
	s.mu.Unlock()
	// Check if password is still the default (admin)
	forceChange := body.Password == sha256Hex("admin")
	writeJSON(w, map[string]any{"token": token, "force_password_change": forceChange})
}

// relayPassthroughToken issues a session token to a caller that has
// already authenticated with our system password on the relay-API
// stream — but only when RelayAdminPassthrough is on. This lets a
// trusted upstream peer (the operator's hub) open this node's web UI
// without re-entering the local web password.
//
// SECURITY — this endpoint mints a token without checking a password,
// so a public-listener request MUST never be able to reach a success
// path. Three independent gates enforce that:
//
//  1. Public-listener path: the public TCP server (srv) has no
//     ConnContext hook, so r.Context().Value(relayCtxKey{}) is nil.
//     We bail with 404 — same response shape any unmapped path gets,
//     so the endpoint isn't even discoverable from the open internet.
//  2. Relay-listener path with non-system auth (e.g. a user-level
//     `user:<name>` relay caller): rejected with 403.
//  3. Operator hasn't opted in to RelayAdminPassthrough on THIS node:
//     rejected with 403, regardless of who's calling.
//
// Only when all three pass do we mint and return a session token.
func (s *Server) relayPassthroughToken(w http.ResponseWriter, r *http.Request) {
	// Gate 1: the relay-context value MUST be present. Absence proves
	// the request came in on the public listener, never the apiBridge,
	// so the caller never authenticated with our system password and
	// must not be able to mint a token here. 404 instead of 401 to
	// hide the endpoint from external scans.
	rawCtx := r.Context().Value(relayCtxKey{})
	if rawCtx == nil {
		http.NotFound(w, r)
		return
	}
	// Gate 2: relay-routed, but must be system-level (peer system
	// password). User-level relay traffic doesn't grant admin even
	// with passthrough on.
	authID, _ := rawCtx.(string)
	if !trustedRelay(authID) {
		http.Error(w, "forbidden", 403)
		return
	}
	// Gate 3: operator must have explicitly enabled passthrough.
	if !s.app.Store().Get().RelayAdminPassthrough {
		http.Error(w, "passthrough disabled", 403)
		return
	}
	token := s.generateToken()
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(s.sessionTimeout())
	s.mu.Unlock()
	writeJSON(w, map[string]any{"token": token})
}

func (s *Server) sessionTimeout() time.Duration {
	cfg := s.app.Store().Get()
	if cfg.SessionTimeoutH > 0 {
		return time.Duration(cfg.SessionTimeoutH) * time.Hour
	}
	return 12 * time.Hour // default 12h
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RelayAdminPassthrough lets requests delivered through the
		// peer-to-peer relay bridge skip the local token check — but
		// ONLY when the underlying QUIC connection authenticated with
		// the system password (auth ID "system"). User-level relay
		// traffic (auth ID "user:<name>") falls through to the normal
		// auth path, which 401s the API or SPA-redirects to /login —
		// so a regular VPN user can never escalate to admin via
		// `_relay_api_:0` even with the toggle on.
		if trustedRelay(relayAuthID(r.Context())) && s.app.Store().Get().RelayAdminPassthrough {
			next.ServeHTTP(w, r)
			return
		}
		// Collect every token the request might carry, then accept the
		// first one we recognize. The Authorization header, the
		// `?token=` query (used by SSE / EventSource which can't set
		// headers), and the `hy2_session` cookie (used by new tabs
		// that can't pre-populate sessionStorage) are all valid carriers
		// of the same secret. They're listed in parallel rather than
		// "first non-empty wins" because a remote-proxy tab carries
		// THREE different tokens at once: its own remote-node Bearer
		// (for the eventual remote authMiddleware), and its hub-origin
		// cookie (for THIS hub's /remote/ gate). The hub gate must
		// succeed on the cookie even when an unrecognized Bearer is
		// also present.
		candidates := make([]string, 0, 3)
		if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
			candidates = append(candidates, h[7:])
		}
		if q := r.URL.Query().Get("token"); q != "" {
			candidates = append(candidates, q)
		}
		if c, err := r.Cookie("hy2_session"); err == nil && c.Value != "" {
			candidates = append(candidates, c.Value)
		}
		var matched string
		s.mu.RLock()
		for _, t := range candidates {
			if expiry, ok := s.sessions[t]; ok && time.Now().Before(expiry) {
				matched = t
				break
			}
		}
		s.mu.RUnlock()
		if matched == "" {
			http.Error(w, "unauthorized", 401)
			return
		}
		// Refresh session on activity
		s.mu.Lock()
		s.sessions[matched] = time.Now().Add(s.sessionTimeout())
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// getMe returns the signed-in web user (shown in the top bar user menu).
func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	u := s.username
	s.mu.RUnlock()
	writeJSON(w, map[string]string{"username": u})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewUsername     string `json:"new_username"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// All password fields are SHA-256 hex from client
	s.mu.Lock()
	defer s.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(body.CurrentPassword), []byte(s.passHash)) != 1 {
		http.Error(w, "current password incorrect", 403)
		return
	}
	if body.NewUsername != "" {
		s.username = body.NewUsername
	}
	if body.NewPassword != "" {
		s.passHash = body.NewPassword
	}
	// Persist credentials to config (stored as SHA-256 hex)
	s.app.UpdateWebCredentials(s.username, s.passHash)
	s.sessions = make(map[string]time.Time)
	writeJSON(w, map[string]string{"status": "ok"})
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

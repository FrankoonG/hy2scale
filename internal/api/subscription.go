// Subscriptions: one user's links for subscription converters
// (GET /sub/{id}/{token}, no login: the token is the secret), and their
// settings on the user.

package api

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/FrankoonG/hy2scale/internal/app"
)

const subPathTries = 3

func (s *Server) subscriptionRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/users/{id}/subscription", s.getUserSub)
	m.HandleFunc("POST /api/users/{id}/subscription/token", s.resetUserSubToken)
}

// webPort: the port of the web listener.
func (s *Server) webPort() string {
	_, p, err := net.SplitHostPort(s.addr)
	if err != nil {
		return ""
	}
	return p
}

// userSubView: a user's subscription as the UI shows it.
type userSubView struct {
	Enabled    bool            `json:"enabled"`
	Name       string          `json:"name"` // the name format in effect
	LAN        bool            `json:"lan"`
	Path       string          `json:"path"` // "<base>/sub/<id>/<token>"; "" when off
	WebPort    string          `json:"web_port"`
	PublicHost string          `json:"public_host"`
	LANAddrs   []string        `json:"lan_addrs"`
	Shared     bool            `json:"shared"`
	HTTP       bool            `json:"http"` // also served over plain http (Settings → Web)
	Entries    []app.ProxyLink `json:"entries"`
}

func (s *Server) userSubView(u app.UserConfig) userSubView {
	v := userSubView{Name: app.SubNameDefault, LAN: u.Sub == nil || !u.Sub.NoLAN,
		WebPort: s.webPort(), PublicHost: s.app.PublicHost(), LANAddrs: lanStrings(), Shared: u.Shared != nil,
		Entries: []app.ProxyLink{}}
	if sc := s.app.Store().Get().Subscription; sc != nil {
		v.HTTP = sc.HTTP
	}
	if u.Sub != nil && u.Sub.Name != "" {
		v.Name = u.Sub.Name
	}
	if u.Sub != nil && u.Sub.Token != "" {
		v.Enabled = true
		v.Path = s.basePath + "/sub/" + s.app.SubID(&u) + "/" + u.Sub.Token
		v.Entries = append(v.Entries, s.app.SubLinks(u)...)
	}
	return v
}

// subUser: the user of the request; refuses node access (no subscription).
func (s *Server) subUser(w http.ResponseWriter, r *http.Request) (app.UserConfig, bool) {
	u, ok := s.userByID(r.PathValue("id"))
	if !ok {
		http.Error(w, "user not found", 404)
		return u, false
	}
	if u.Kind() == app.KindAccess {
		http.Error(w, "node access users have no subscription", 400)
		return u, false
	}
	return u, true
}

func (s *Server) getUserSub(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.subUser(w, r); ok {
		writeJSON(w, s.userSubView(u))
	}
}

// saveUserSub stores u's subscription through the user update (a shared
// user's goes to every holder).
func (s *Server) saveUserSub(w http.ResponseWriter, u app.UserConfig) {
	if err := s.app.UpdateUser(u.ID, u); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	nu, _ := s.userByID(u.ID)
	writeJSON(w, s.userSubView(nu))
}

func (s *Server) resetUserSubToken(w http.ResponseWriter, r *http.Request) {
	u, ok := s.subUser(w, r)
	if !ok {
		return
	}
	if u.Sub == nil || u.Sub.Token == "" {
		http.Error(w, "subscription is off", 400)
		return
	}
	sub := *u.Sub
	sub.Token = app.NewSubToken()
	u.Sub = &sub
	s.saveUserSub(w, u)
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// subHeaders: the response headers passed on from another node.
var subHeaders = []string{"Content-Type", "Content-Disposition", "Subscription-Userinfo", "Profile-Update-Interval", "Cache-Control"}

// serveSub answers GET /sub/{id}/{token}: a subscription of a user held
// here (id: this installation, or a shared user's gid), or one of another
// node fetched through the mesh (id: that node's installation). Anything
// else — and a subscription turned off — answers 404.
func (s *Server) serveSub(w http.ResponseWriter, r *http.Request) {
	id, token := r.PathValue("id"), r.PathValue("token")
	format := r.URL.Query().Get("format")
	raw := r.Context().Value(relayCtxKey{})
	viaRelay := raw != nil
	if u, ok := s.app.SubUser(id, token); ok {
		if viaRelay {
			// passed on by a mesh node (not a user's node access)
			sc := s.app.Store().Get().Subscription
			if authID, _ := raw.(string); authID != "system" || (sc != nil && sc.NoRelay) {
				http.NotFound(w, r)
				return
			}
		}
		body, ctype := app.SubscriptionBody(s.app.SubLinks(u), format)
		h := w.Header()
		h.Set("Content-Type", ctype)
		h.Set("Cache-Control", "no-store")
		h.Set("Profile-Update-Interval", "12")
		h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(u.Username))
		h.Set("Subscription-Userinfo", s.app.SubUserInfo(u))
		w.Write(body)
		return
	}
	// another node's: only from this node's own listener, never passed on twice
	if viaRelay || len(id) < 16 || len(id) > 64 {
		http.NotFound(w, r)
		return
	}
	paths := s.app.PathsToIID(id)
	if len(paths) > subPathTries {
		paths = paths[:subPathTries]
	}
	q := ""
	if format != "" {
		q = "?format=" + url.QueryEscape(format)
	}
	for _, p := range paths {
		code, hdr, body, err := s.relayCall(strings.Split(p, "/"), "GET", "/sub/"+url.PathEscape(id)+"/"+url.PathEscape(token)+q, nil, nil)
		if err != nil {
			continue
		}
		if code != 200 {
			break // the node answered: wrong token, off, or older than subscriptions
		}
		for _, k := range subHeaders {
			if v := hdr.Get(k); v != "" {
				w.Header().Set(k, v)
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
		return
	}
	http.NotFound(w, r)
}

// subPlainHTTP lets subscriptions answer over plain http when allowed
// (converters that cannot check the web certificate); everything else
// still goes to https.
func (s *Server) subPlainHTTP(root, next http.Handler) http.Handler {
	prefix := s.basePath + "/sub/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil && strings.HasPrefix(r.URL.Path, prefix) {
			if sc := s.app.Store().Get().Subscription; sc != nil && sc.HTTP {
				root.ServeHTTP(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

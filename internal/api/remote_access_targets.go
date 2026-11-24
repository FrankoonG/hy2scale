package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
)

// Remote access targets: the list of addresses other nodes managing this one
// reach from here. It lives on this node, so every manager sees the same
// list; opening one is up to each manager (remote_access.go).

func (s *Server) remoteAccessTargetRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/remote-access/targets", s.listRATargets)
	m.HandleFunc("POST /api/remote-access/targets", s.addRATarget)
	m.HandleFunc("DELETE /api/remote-access/targets/{id}", s.deleteRATarget)
}

func (s *Server) listRATargets(w http.ResponseWriter, r *http.Request) {
	out := s.app.Store().Get().RemoteAccessTargets
	if out == nil {
		out = []app.RemoteAccessTarget{}
	}
	writeJSON(w, out)
}

// normRATarget checks a target and puts it in its stored form.
func normRATarget(t *app.RemoteAccessTarget) error {
	t.Target = strings.TrimSpace(t.Target)
	switch t.Mode {
	case "web":
		u, _, err := raWebURL(t.Target)
		if err != nil {
			return err
		}
		t.Target, t.TCP, t.UDP = u.String(), true, false
	case "port":
		if _, err := raHostPort(t.Target); err != nil {
			return err
		}
		if !t.TCP && !t.UDP {
			return errors.New("protocol: tcp, udp or both")
		}
	default:
		return errors.New("mode: web or port")
	}
	return nil
}

// raWebURL: a web target as a URL and the address dialled for it.
func raWebURL(target string) (*url.URL, string, error) {
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, "", errors.New("web address: http(s)://host[:port][/path]")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return u, net.JoinHostPort(u.Hostname(), port), nil
}

// raHostPort checks a host:port target.
func raHostPort(target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if p, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || p < 1 || p > 65535 {
		return "", errors.New("target: host:port")
	}
	return target, nil
}

func (s *Server) addRATarget(w http.ResponseWriter, r *http.Request) {
	var t app.RemoteAccessTarget
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := normRATarget(&t); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	t.ID = s.generateToken()[:12]
	t.Created = time.Now().UnixMilli()
	dup := false
	s.app.Store().Update(func(c *app.Config) {
		for _, x := range c.RemoteAccessTargets {
			if x.Mode == t.Mode && x.Target == t.Target && x.TCP == t.TCP && x.UDP == t.UDP {
				dup = true
				return
			}
		}
		c.RemoteAccessTargets = append(c.RemoteAccessTargets, t)
	})
	if dup {
		http.Error(w, "already in the list", 409)
		return
	}
	writeJSON(w, t)
}

func (s *Server) deleteRATarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found := false
	s.app.Store().Update(func(c *app.Config) {
		out := c.RemoteAccessTargets[:0:0]
		for _, x := range c.RemoteAccessTargets {
			if x.ID == id {
				found = true
				continue
			}
			out = append(out, x)
		}
		c.RemoteAccessTargets = out
	})
	if !found {
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

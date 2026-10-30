// Core: routing rules.

package api

import (
	"encoding/json"
	"net/http"

	"github.com/FrankoonG/hy2scale/internal/app"
)

func (s *Server) getRules(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	rules := cfg.Rules
	if rules == nil {
		rules = []app.RoutingRule{}
	}
	writeJSON(w, map[string]any{
		"available": true,
		"capture":   s.app.RuleCaptureMode(),
		"rules":     rules,
	})
}

func (s *Server) addRule(w http.ResponseWriter, r *http.Request) {
	var rule app.RoutingRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(rule.ExitPaths) > 0 {
		rule.ExitVia = rule.ExitPaths[0]
	} else if rule.ExitVia != "" {
		rule.ExitPaths = []string{rule.ExitVia}
	}
	if rule.ID == "" || rule.ExitVia == "" || len(rule.Targets) == 0 {
		http.Error(w, "id, exit_via, and targets required", 400)
		return
	}
	if rule.Type != "ip" && rule.Type != "domain" {
		http.Error(w, "type must be 'ip' or 'domain'", 400)
		return
	}
	if sanitized, err := app.SanitizeExitMode(rule.ExitPaths, rule.ExitMode); err != nil {
		http.Error(w, err.Error(), 400)
		return
	} else {
		rule.ExitMode = sanitized
	}
	s.app.AddRule(rule)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var rule app.RoutingRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	rule.ID = id
	if len(rule.ExitPaths) > 0 {
		rule.ExitVia = rule.ExitPaths[0]
	} else if rule.ExitVia != "" {
		rule.ExitPaths = []string{rule.ExitVia}
	}
	if sanitized, err := app.SanitizeExitMode(rule.ExitPaths, rule.ExitMode); err != nil {
		http.Error(w, err.Error(), 400)
		return
	} else {
		rule.ExitMode = sanitized
	}
	s.app.UpdateRule(id, rule)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.app.DeleteRule(id)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) toggleRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.app.ToggleRule(id, body.Enabled)
	writeJSON(w, map[string]string{"status": "ok"})
}

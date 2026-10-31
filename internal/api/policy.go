// Core: condition sets, policy sets, the direct entry (docs/v1.4.0-policy.md §7).

package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/FrankoonG/hy2scale/core/policy"
	"github.com/FrankoonG/hy2scale/internal/app"
)

// policyRoutes: the policy CRUD, settings, notice and test routes. The
// system-capture routes (capabilities / interfaces / status) are
// registered by policy_system.go.
func (s *Server) policyRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/policy", s.getPolicy)
	m.HandleFunc("PUT /api/policy/settings", s.putPolicySettings)
	m.HandleFunc("POST /api/policy/notice/dismiss", s.dismissPolicyNotice)
	m.HandleFunc("POST /api/policy/conditions", s.putConditionSet)
	m.HandleFunc("PUT /api/policy/conditions/{id}", s.putConditionSet)
	m.HandleFunc("DELETE /api/policy/conditions/{id}", s.deleteConditionSet)
	m.HandleFunc("POST /api/policy/policies", s.putPolicySet)
	m.HandleFunc("PUT /api/policy/policies/{id}", s.putPolicySet)
	m.HandleFunc("DELETE /api/policy/policies/{id}", s.deletePolicySet)
	m.HandleFunc("POST /api/policy/test", s.testPolicy)
}

type condSetView struct {
	policy.ConditionSet
	Refs []policy.Ref `json:"refs"`
}

type policySetView struct {
	policy.PolicySet
	Refs     []policy.Ref `json:"refs"`
	Warnings []string     `json:"warnings,omitempty"`
}

type policyView struct {
	MaxDepth       int             `json:"max_depth"`
	Direct         string          `json:"direct"`
	System         policy.System   `json:"system"`
	Conditions     []condSetView   `json:"conditions"`
	Policies       []policySetView `json:"policies"`
	MigratedNotice bool            `json:"migrated_notice"`
}

func refsOrEmpty(r []policy.Ref) []policy.Ref {
	if r == nil {
		return []policy.Ref{}
	}
	return r
}

func (s *Server) policyView() policyView {
	cfg := s.app.Store().Get()
	p := s.app.PolicyConfigCopy()
	v := policyView{MaxDepth: p.EffectiveMaxDepth(), Direct: p.Direct, System: p.System,
		Conditions: []condSetView{}, Policies: []policySetView{}, MigratedNotice: p.MigratedNotice}
	for _, cs := range p.Conditions {
		v.Conditions = append(v.Conditions, condSetView{cs, refsOrEmpty(p.InternalRefs(cs.ID))})
	}
	for _, ps := range p.Policies {
		v.Policies = append(v.Policies, policySetView{PolicySet: ps, Refs: refsOrEmpty(s.app.PolicyRefs(&cfg, ps.ID)),
			Warnings: policy.PolicyCycles(p, ps.ID)})
	}
	return v
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.policyView()) }

// writePolicyErr answers an error of a policy change: validation 400
// {error, path}, referenced 409 {error, refs}, missing 404.
func writePolicyErr(w http.ResponseWriter, err error) {
	var ve *policy.Error
	var re *app.PolicyRefError
	switch {
	case errors.As(err, &ve):
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": ve.Msg, "path": ve.Path})
	case errors.As(err, &re):
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "referenced", "refs": refsOrEmpty(re.Refs)})
	case errors.Is(err, app.ErrPolicyNotFound):
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"error": "not found"})
	default:
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func decodePolicyBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(v); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error(), "path": ""})
		return false
	}
	return true
}

func (s *Server) putPolicySettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MaxDepth *int            `json:"max_depth"`
		Direct   *string         `json:"direct"`
		System   json.RawMessage `json:"system"`
	}
	if !decodePolicyBody(w, r, &body) {
		return
	}
	set := app.PolicySettings{MaxDepth: body.MaxDepth, Direct: body.Direct}
	if len(body.System) > 0 && string(body.System) != "null" {
		// decoded over the current settings: a partial object keeps the
		// fields it leaves out
		sys := s.app.PolicyConfigCopy().System
		if err := json.Unmarshal(body.System, &sys); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "invalid system: " + err.Error(), "path": "system"})
			return
		}
		set.System = &sys
	}
	if err := s.app.UpdatePolicySettings(set); err != nil {
		writePolicyErr(w, err)
		return
	}
	writeJSON(w, s.policyView())
}

func (s *Server) dismissPolicyNotice(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DismissPolicyNotice(); err != nil {
		writePolicyErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) putConditionSet(w http.ResponseWriter, r *http.Request) {
	var cs policy.ConditionSet
	if !decodePolicyBody(w, r, &cs) {
		return
	}
	out, err := s.app.PutConditionSet(r.PathValue("id"), cs)
	if err != nil {
		writePolicyErr(w, err)
		return
	}
	p := s.app.PolicyConfigCopy()
	writeJSON(w, condSetView{out, refsOrEmpty(p.InternalRefs(out.ID))})
}

func (s *Server) putPolicySet(w http.ResponseWriter, r *http.Request) {
	var ps policy.PolicySet
	if !decodePolicyBody(w, r, &ps) {
		return
	}
	out, warnings, err := s.app.PutPolicySet(r.PathValue("id"), ps)
	if err != nil {
		writePolicyErr(w, err)
		return
	}
	cfg := s.app.Store().Get()
	writeJSON(w, policySetView{PolicySet: out, Refs: refsOrEmpty(s.app.PolicyRefs(&cfg, out.ID)), Warnings: warnings})
}

func (s *Server) deleteConditionSet(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteConditionSet(r.PathValue("id")); err != nil {
		writePolicyErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) deletePolicySet(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeletePolicySet(r.PathValue("id")); err != nil {
		writePolicyErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) testPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Start  string               `json:"start"`
		Source app.PolicyTestSource `json:"source"`
		Dest   app.PolicyTestDest   `json:"dest"`
	}
	if !decodePolicyBody(w, r, &body) {
		return
	}
	d, err := s.app.PolicyTest(r.Context(), body.Start, body.Source, body.Dest)
	if err != nil {
		writePolicyErr(w, err)
		return
	}
	chain := d.Chain
	if chain == nil {
		chain = []policy.Hop{}
	}
	fields := d.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	writeJSON(w, map[string]any{"chain": chain, "terminal": d.Terminal, "reason": d.Reason,
		"policy": d.Policy, "fields": fields})
}

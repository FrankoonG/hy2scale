package policy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
)

// Error is a validation error at Path (relative to the object validated,
// e.g. "rules[2].when.items[0].values[1]").
type Error struct {
	Path string `json:"path"`
	Msg  string `json:"error"`
}

func (e *Error) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

func errAt(path, format string, args ...any) *Error {
	return &Error{Path: path, Msg: fmt.Sprintf(format, args...)}
}

// NewID returns a fresh id with prefix ("cs_", "ps_", "r").
func NewID(prefix string) string {
	var b [5]byte
	rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// FillRuleIDs gives rules without an id (nested ones included) a fresh one.
func FillRuleIDs(s *Set) {
	s.WalkRules(func(r *Rule, _ string) {
		if strings.TrimSpace(r.ID) == "" {
			r.ID = NewID("r")
		}
	})
}
func (c *Config) condSet(id string) *ConditionSet {
	for i := range c.Conditions {
		if c.Conditions[i].ID == id {
			return &c.Conditions[i]
		}
	}
	return nil
}

func (c *Config) policySet(id string) *PolicySet {
	for i := range c.Policies {
		if c.Policies[i].ID == id {
			return &c.Policies[i]
		}
	}
	return nil
}

// ConditionSetByID returns the condition set id (nil = none).
func (c *Config) ConditionSetByID(id string) *ConditionSet { return c.condSet(id) }

// PolicySetByID returns the policy set id (nil = none).
func (c *Config) PolicySetByID(id string) *PolicySet { return c.policySet(id) }

// checkCond validates a condition tree at path against cfg (refs exist).
func checkCond(cfg *Config, c *Condition, path string) *Error {
	if c == nil {
		return errAt(path, "missing condition")
	}
	switch c.Type {
	case CondAll, CondAny:
		if len(c.Items) == 0 {
			return errAt(path+".items", "empty group")
		}
		for i := range c.Items {
			if e := checkCond(cfg, &c.Items[i], fmt.Sprintf("%s.items[%d]", path, i)); e != nil {
				return e
			}
		}
	case CondNot:
		if c.Item == nil {
			return errAt(path+".item", "missing condition")
		}
		return checkCond(cfg, c.Item, path+".item")
	case CondRef:
		if c.Ref == "" {
			return errAt(path+".ref", "missing condition set")
		}
		if cfg.condSet(c.Ref) == nil {
			return errAt(path+".ref", "unknown condition set %q", c.Ref)
		}
	case CondMatch:
		if !validField(c.Field) {
			return errAt(path+".field", "unknown field %q", c.Field)
		}
		if len(c.Values) == 0 {
			return errAt(path+".values", "no values")
		}
		for i, v := range c.Values {
			if err := checkValue(c.Field, v); err != nil {
				return errAt(fmt.Sprintf("%s.values[%d]", path, i), "%v", err)
			}
		}
	default:
		return errAt(path+".type", "unknown condition type %q", c.Type)
	}
	return nil
}

// refPaths lists every ref in a condition tree with its path.
func refPaths(c *Condition, path string, out *[][2]string) {
	if c == nil {
		return
	}
	switch c.Type {
	case CondAll, CondAny:
		for i := range c.Items {
			refPaths(&c.Items[i], fmt.Sprintf("%s.items[%d]", path, i), out)
		}
	case CondNot:
		refPaths(c.Item, path+".item", out)
	case CondRef:
		*out = append(*out, [2]string{c.Ref, path + ".ref"})
	}
}

// condRefs: the condition sets s references directly (with paths).
func condRefs(s *Set) [][2]string {
	var refs [][2]string
	s.WalkRules(func(r *Rule, path string) { refPaths(r.When, path+".when", &refs) })
	return refs
}

// reachesCond reports whether condition set target is reachable from
// from (from itself included) along refs.
func (c *Config) reachesCond(from, target string, seen map[string]bool) bool {
	if from == target {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	cs := c.condSet(from)
	if cs == nil {
		return false
	}
	for _, r := range condRefs(cs) {
		if c.reachesCond(r[0], target, seen) {
			return true
		}
	}
	return false
}

// checkSet validates s of kind k as it stands in cfg: name, rule ids
// (unique across nested flows), conditions, flow depth and the ends the
// kind allows.
func checkSet(cfg *Config, s *Set, k SetKind) *Error {
	if strings.TrimSpace(s.Name) == "" {
		return errAt("name", "name required")
	}
	ids := map[string]bool{}
	if e := checkRules(cfg, s.Rules, "rules", k, 0, ids); e != nil {
		return e
	}
	if s.Default != nil {
		if e := checkEnd(cfg, s.Default, "default", k); e != nil {
			return e
		}
	}
	return nil
}

func checkRules(cfg *Config, rules []Rule, path string, k SetKind, depth int, ids map[string]bool) *Error {
	for i := range rules {
		r := &rules[i]
		p := fmt.Sprintf("%s[%d]", path, i)
		if strings.TrimSpace(r.ID) == "" {
			return errAt(p+".id", "missing id")
		}
		if ids[r.ID] {
			return errAt(p+".id", "duplicate rule id %q", r.ID)
		}
		ids[r.ID] = true
		if r.When != nil {
			if e := checkCond(cfg, r.When, p+".when"); e != nil {
				return e
			}
		}
		t := &r.Then
		if t.Type != TermFlow {
			if e := checkEnd(cfg, t, p+".then", k); e != nil {
				return e
			}
			continue
		}
		if depth+1 > MaxFlowDepth {
			return errAt(p+".then", "flows nested deeper than %d", MaxFlowDepth)
		}
		if e := checkRules(cfg, t.Rules, p+".then.rules", k, depth+1, ids); e != nil {
			return e
		}
		if t.Default != nil {
			if e := checkEnd(cfg, t.Default, p+".then.default", k); e != nil {
				return e
			}
		}
	}
	return nil
}

// checkEnd validates an end allowed for kind k.
func checkEnd(cfg *Config, t *Terminal, path string, k SetKind) *Error {
	if k == KindCondition {
		if t.Type != TermResult {
			return errAt(path+".type", "a condition set ends in result, not %q", t.Type)
		}
		return nil
	}
	switch t.Type {
	case TermDeny, TermDefault:
	case TermExit:
		if len(t.Paths) == 0 {
			return errAt(path+".paths", "no exit paths")
		}
		for i, p := range t.Paths {
			if strings.Trim(strings.TrimSpace(p), "/") == "" {
				return errAt(fmt.Sprintf("%s.paths[%d]", path, i), "empty path")
			}
		}
		switch t.Mode {
		case "", "quality", "aggregate":
		default:
			return errAt(path+".mode", "unknown mode %q", t.Mode)
		}
	case TermIface:
		if strings.TrimSpace(t.Iface) == "" {
			return errAt(path+".iface", "interface required")
		}
		if t.Src != "" {
			if _, err := netip.ParseAddr(t.Src); err != nil {
				return errAt(path+".src", "invalid address %q", t.Src)
			}
		}
	case TermCall:
		if t.Policy == "" {
			return errAt(path+".policy", "missing policy set")
		}
		if cfg.policySet(t.Policy) == nil {
			return errAt(path+".policy", "unknown policy set %q", t.Policy)
		}
	default:
		return errAt(path+".type", "unknown terminal %q", t.Type)
	}
	return nil
}

// CheckConditionSet validates cs as it stands in cfg (cfg already holds
// cs, so references to and from it resolve), refusing reference cycles
// through cs.
func CheckConditionSet(cfg *Config, cs *ConditionSet) error {
	if e := checkSet(cfg, cs, KindCondition); e != nil {
		return e
	}
	for _, r := range condRefs(cs) {
		if cfg.reachesCond(r[0], cs.ID, map[string]bool{}) {
			return errAt(r[1], "condition set cycle")
		}
	}
	return nil
}

// CheckPolicySet validates ps as it stands in cfg. Call cycles through ps
// are allowed (the depth limit ends them) and reported as warnings.
func CheckPolicySet(cfg *Config, ps *PolicySet) (warnings []string, err error) {
	if e := checkSet(cfg, ps, KindPolicy); e != nil {
		return nil, e
	}
	return PolicyCycles(cfg, ps.ID), nil
}

// calls: the policy sets s calls (with the call's path).
func calls(s *Set) [][2]string {
	var out [][2]string
	s.WalkEnds(func(t *Terminal, path string) {
		if t.Type == TermCall {
			out = append(out, [2]string{t.Policy, path})
		}
	})
	return out
}

// PolicyCycles describes the call cycles through policy set id.
func PolicyCycles(cfg *Config, id string) []string {
	ps := cfg.policySet(id)
	if ps == nil {
		return nil
	}
	var out []string
	for _, cl := range calls(ps) {
		if path := cfg.callPath(cl[0], id, map[string]bool{}); path != nil {
			names := []string{ps.Name}
			for _, p := range path {
				n := p
				if s := cfg.policySet(p); s != nil {
					n = s.Name
				}
				names = append(names, n)
			}
			out = append(out, fmt.Sprintf("%s: call cycle %s", cl[1], strings.Join(names, " > ")))
		}
	}
	return out
}

// callPath: the call chain from → … → target, nil when target is not
// reachable.
func (c *Config) callPath(from, target string, seen map[string]bool) []string {
	if from == target {
		return []string{from}
	}
	if seen[from] {
		return nil
	}
	seen[from] = true
	ps := c.policySet(from)
	if ps == nil {
		return nil
	}
	for _, cl := range calls(ps) {
		if p := c.callPath(cl[0], target, seen); p != nil {
			return append([]string{from}, p...)
		}
	}
	return nil
}

// CheckSettings validates max_depth, direct and system.
func CheckSettings(cfg *Config) error {
	if cfg.MaxDepth != 0 && (cfg.MaxDepth < MinMaxDepth || cfg.MaxDepth > MaxMaxDepth) {
		return errAt("max_depth", "must be %d-%d", MinMaxDepth, MaxMaxDepth)
	}
	if cfg.Direct != "" && cfg.policySet(cfg.Direct) == nil {
		return errAt("direct", "unknown policy set %q", cfg.Direct)
	}
	for _, s := range []struct {
		path, v string
	}{{"system.local.on_stop", cfg.System.Local.OnStop}, {"system.forward.on_stop", cfg.System.Forward.OnStop}} {
		if s.v != "" && s.v != OnStopOpen && s.v != OnStopBlock {
			return errAt(s.path, "must be open or block")
		}
	}
	for i, n := range cfg.System.Forward.Interfaces {
		if strings.TrimSpace(n) == "" {
			return errAt(fmt.Sprintf("system.forward.interfaces[%d]", i), "empty interface")
		}
	}
	for i, s := range cfg.System.Forward.Sources {
		if _, err := netip.ParsePrefix(strings.TrimSpace(s)); err != nil {
			if _, err := netip.ParseAddr(strings.TrimSpace(s)); err != nil {
				return errAt(fmt.Sprintf("system.forward.sources[%d]", i), "invalid CIDR %q", s)
			}
		}
	}
	return nil
}

// Validate checks the whole block (paths prefixed conditions[i] /
// policies[i]); duplicate set ids are refused.
func (c *Config) Validate() error {
	if err := CheckSettings(c); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range c.Conditions {
		cs := &c.Conditions[i]
		if cs.ID == "" || seen[cs.ID] {
			return errAt(fmt.Sprintf("conditions[%d].id", i), "missing or duplicate id")
		}
		seen[cs.ID] = true
		if err := CheckConditionSet(c, cs); err != nil {
			e := err.(*Error)
			return errAt(fmt.Sprintf("conditions[%d].%s", i, e.Path), "%s", e.Msg)
		}
	}
	for i := range c.Policies {
		ps := &c.Policies[i]
		if ps.ID == "" || seen[ps.ID] {
			return errAt(fmt.Sprintf("policies[%d].id", i), "missing or duplicate id")
		}
		seen[ps.ID] = true
		if _, err := CheckPolicySet(c, ps); err != nil {
			e := err.(*Error)
			return errAt(fmt.Sprintf("policies[%d].%s", i, e.Path), "%s", e.Msg)
		}
	}
	return nil
}

// Normalize fills defaults a loaded block may lack (nil slices, on_stop).
func (c *Config) Normalize() {
	if c.Conditions == nil {
		c.Conditions = []ConditionSet{}
	}
	if c.Policies == nil {
		c.Policies = []PolicySet{}
	}
	if c.System.Local.OnStop == "" {
		c.System.Local.OnStop = OnStopOpen
	}
	if c.System.Forward.OnStop == "" {
		c.System.Forward.OnStop = OnStopOpen
	}
	if c.System.Forward.Interfaces == nil {
		c.System.Forward.Interfaces = []string{}
	}
	if c.System.Forward.Sources == nil {
		c.System.Forward.Sources = []string{}
	}
	for i := range c.Conditions {
		if c.Conditions[i].Rules == nil {
			c.Conditions[i].Rules = []Rule{}
		}
	}
	for i := range c.Policies {
		if c.Policies[i].Rules == nil {
			c.Policies[i].Rules = []Rule{}
		}
	}
}

// Ref is one place a set is referenced (GET /api/policy refs).
type Ref struct {
	Kind string `json:"kind"` // user|proxy|wireguard|vpn|policy|condition|direct
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InternalRefs: the references to set id from within the block (policy
// rules calling it, condition sets / policy rules referencing it, the
// direct entry).
func (c *Config) InternalRefs(id string) []Ref {
	var out []Ref
	if c.Direct == id && id != "" {
		out = append(out, Ref{Kind: "direct", ID: id, Name: ""})
	}
	uses := func(s *Set) bool {
		for _, r := range condRefs(s) {
			if r[0] == id {
				return true
			}
		}
		for _, cl := range calls(s) {
			if cl[0] == id {
				return true
			}
		}
		return false
	}
	for i := range c.Conditions {
		if cs := &c.Conditions[i]; cs.ID != id && uses(cs) {
			out = append(out, Ref{Kind: "condition", ID: cs.ID, Name: cs.Name})
		}
	}
	for i := range c.Policies {
		if ps := &c.Policies[i]; ps.ID != id && uses(ps) {
			out = append(out, Ref{Kind: "policy", ID: ps.ID, Name: ps.Name})
		}
	}
	return out
}

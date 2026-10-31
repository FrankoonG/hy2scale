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

// FillRuleIDs gives rules without an id a fresh one.
func FillRuleIDs[T any](rules []T, id func(*T) *string) {
	for i := range rules {
		if p := id(&rules[i]); strings.TrimSpace(*p) == "" {
			*p = NewID("r")
		}
	}
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

// condRefs: the condition sets cs references directly.
func condRefs(cs *ConditionSet) []string {
	var refs [][2]string
	for i, r := range cs.Rules {
		refPaths(r.When, fmt.Sprintf("rules[%d].when", i), &refs)
	}
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r[0]
	}
	return out
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
		if c.reachesCond(r, target, seen) {
			return true
		}
	}
	return false
}

func checkRuleIDs(ids []string) *Error {
	seen := map[string]bool{}
	for i, id := range ids {
		if strings.TrimSpace(id) == "" {
			return errAt(fmt.Sprintf("rules[%d].id", i), "missing id")
		}
		if seen[id] {
			return errAt(fmt.Sprintf("rules[%d].id", i), "duplicate rule id %q", id)
		}
		seen[id] = true
	}
	return nil
}

// CheckConditionSet validates cs as it stands in cfg (cfg already holds
// cs, so references to and from it resolve): name, rule ids, conditions,
// and no reference cycle through cs.
func CheckConditionSet(cfg *Config, cs *ConditionSet) error {
	if strings.TrimSpace(cs.Name) == "" {
		return errAt("name", "name required")
	}
	ids := make([]string, len(cs.Rules))
	for i, r := range cs.Rules {
		ids[i] = r.ID
	}
	if e := checkRuleIDs(ids); e != nil {
		return e
	}
	for i, r := range cs.Rules {
		if r.When == nil {
			continue
		}
		if e := checkCond(cfg, r.When, fmt.Sprintf("rules[%d].when", i)); e != nil {
			return e
		}
	}
	var refs [][2]string
	for i, r := range cs.Rules {
		refPaths(r.When, fmt.Sprintf("rules[%d].when", i), &refs)
	}
	for _, r := range refs {
		if cfg.reachesCond(r[0], cs.ID, map[string]bool{}) {
			return errAt(r[1], "condition set cycle")
		}
	}
	return nil
}

func checkTerminal(cfg *Config, t *Terminal, path string) *Error {
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

// CheckPolicySet validates ps as it stands in cfg. Call cycles through ps
// are allowed (the depth limit ends them) and reported as warnings.
func CheckPolicySet(cfg *Config, ps *PolicySet) (warnings []string, err error) {
	if strings.TrimSpace(ps.Name) == "" {
		return nil, errAt("name", "name required")
	}
	ids := make([]string, len(ps.Rules))
	for i, r := range ps.Rules {
		ids[i] = r.ID
	}
	if e := checkRuleIDs(ids); e != nil {
		return nil, e
	}
	for i := range ps.Rules {
		r := &ps.Rules[i]
		if r.When != nil {
			if e := checkCond(cfg, r.When, fmt.Sprintf("rules[%d].when", i)); e != nil {
				return nil, e
			}
		}
		if e := checkTerminal(cfg, &r.Then, fmt.Sprintf("rules[%d].then", i)); e != nil {
			return nil, e
		}
	}
	return PolicyCycles(cfg, ps.ID), nil
}

// PolicyCycles describes the call cycles through policy set id.
func PolicyCycles(cfg *Config, id string) []string {
	ps := cfg.policySet(id)
	if ps == nil {
		return nil
	}
	var out []string
	for i, r := range ps.Rules {
		if r.Then.Type != TermCall {
			continue
		}
		if path := cfg.callPath(r.Then.Policy, id, map[string]bool{}); path != nil {
			names := []string{ps.Name}
			for _, p := range path {
				n := p
				if s := cfg.policySet(p); s != nil {
					n = s.Name
				}
				names = append(names, n)
			}
			out = append(out, fmt.Sprintf("rules[%d]: call cycle %s", i, strings.Join(names, " > ")))
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
	for _, r := range ps.Rules {
		if r.Then.Type == TermCall {
			if p := c.callPath(r.Then.Policy, target, seen); p != nil {
				return append([]string{from}, p...)
			}
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
			c.Conditions[i].Rules = []CondRule{}
		}
	}
	for i := range c.Policies {
		if c.Policies[i].Rules == nil {
			c.Policies[i].Rules = []PolicyRule{}
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
	usesRef := func(when *Condition) bool {
		var refs [][2]string
		refPaths(when, "", &refs)
		for _, r := range refs {
			if r[0] == id {
				return true
			}
		}
		return false
	}
	for _, cs := range c.Conditions {
		if cs.ID == id {
			continue
		}
		for _, r := range cs.Rules {
			if usesRef(r.When) {
				out = append(out, Ref{Kind: "condition", ID: cs.ID, Name: cs.Name})
				break
			}
		}
	}
	for _, ps := range c.Policies {
		if ps.ID == id {
			continue
		}
		for _, r := range ps.Rules {
			if usesRef(r.When) || (r.Then.Type == TermCall && r.Then.Policy == id) {
				out = append(out, Ref{Kind: "policy", ID: ps.ID, Name: ps.Name})
				break
			}
		}
	}
	return out
}

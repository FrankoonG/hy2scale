package app

import (
	"errors"
	"fmt"
	"log"
	"net/netip"
	"sort"
	"sync/atomic"

	"github.com/FrankoonG/hy2scale/core/policy"
)

// Policy: condition sets, policy sets and the direct entry (core/policy,
// docs/v1.4.0-policy.md). The config's `policy:` block is compiled into
// a.pol on every change; route.go evaluates flows against it.

// PolicyConfig is the `policy:` block (core/policy).
type PolicyConfig = policy.Config

// RoutingRule is a pre-policy routing rule (read for migration only).
type RoutingRule = policy.LegacyRule

// Typed API errors.
var (
	ErrPolicyNotFound = errors.New("not found")
)

// PolicyRefError: a set still referenced (DELETE → 409).
type PolicyRefError struct{ Refs []policy.Ref }

func (e *PolicyRefError) Error() string { return "set is referenced" }

// MigratePolicy turns the legacy top-level rules into the policy block,
// once (a config with a policy block is migrated): the rules become the
// policy set "规则（迁移）", made the direct entry, and the one-time notice
// is set. Reports whether c changed.
func MigratePolicy(c *Config) bool {
	if c.Policy != nil {
		c.Policy.Normalize()
		if len(c.Rules) > 0 {
			c.Rules = nil
			return true
		}
		return false
	}
	p := policy.NewConfig()
	if len(c.Rules) > 0 {
		if ps := policy.MigrateRules(c.Rules); ps != nil {
			p.Policies = append(p.Policies, *ps)
			p.Direct = ps.ID
		}
		p.MigratedNotice = true
		log.Printf("[policy] migrated %d legacy rules into policy set %q (direct entry)", len(c.Rules), policy.MigratedName)
	}
	c.Policy = p
	c.Rules = nil
	return true
}

// initPolicy migrates the config if needed and compiles it.
func (a *App) initPolicy() {
	cfg := a.store.Get()
	if cfg.Policy == nil || len(cfg.Rules) > 0 {
		if err := a.store.Update(func(c *Config) { MigratePolicy(c) }); err != nil {
			log.Printf("[policy] migration not persisted: %v", err)
		}
	}
	a.reloadPolicy()
}

// reloadPolicy compiles the current policy block.
func (a *App) reloadPolicy() {
	p := a.store.Get().Policy
	if p != nil {
		if err := p.Validate(); err != nil {
			log.Printf("[policy] config: %v", err)
		}
	}
	a.pol.Load(p)
	for _, fn := range policyListeners {
		fn()
	}
}

// policyListeners run after every policy change (the capture backend
// re-reads its settings). Registered at init time only.
var policyListeners []func()

// OnPolicyChange registers fn to run after every policy change.
func OnPolicyChange(fn func()) { policyListeners = append(policyListeners, fn) }

// PolicyEngine is the compiled policy (for the capture backend).
func (a *App) PolicyEngine() *policy.Engine { return a.pol }

// PolicyConfigCopy is a deep copy of the policy block.
func (a *App) PolicyConfigCopy() *policy.Config {
	return clonePolicy(a.store.Get().Policy)
}

func clonePolicy(p *policy.Config) *policy.Config {
	if p == nil {
		return policy.NewConfig()
	}
	c := *p
	c.Conditions = append([]policy.ConditionSet(nil), p.Conditions...)
	c.Policies = append([]policy.PolicySet(nil), p.Policies...)
	c.System.Forward.Interfaces = append([]string{}, p.System.Forward.Interfaces...)
	c.System.Forward.Sources = append([]string{}, p.System.Forward.Sources...)
	c.Normalize()
	return &c
}

// updatePolicy applies fn to a copy of the policy block; fn's error aborts
// the change. The engine is reloaded after a change.
func (a *App) updatePolicy(fn func(p *policy.Config) error) error {
	var ferr error
	err := a.store.Update(func(c *Config) {
		p := clonePolicy(c.Policy)
		if ferr = fn(p); ferr == nil {
			c.Policy = p
		}
	})
	if ferr != nil {
		return ferr
	}
	if err != nil {
		return err
	}
	a.reloadPolicy()
	return nil
}

// PolicySettings is the body of PUT /api/policy/settings (nil = keep).
type PolicySettings struct {
	MaxDepth *int           `json:"max_depth"`
	Direct   *string        `json:"direct"`
	System   *policy.System `json:"system"`
}

// UpdatePolicySettings applies a partial settings change.
func (a *App) UpdatePolicySettings(s PolicySettings) error {
	return a.updatePolicy(func(p *policy.Config) error {
		if s.MaxDepth != nil {
			p.MaxDepth = *s.MaxDepth
		}
		if s.Direct != nil {
			p.Direct = *s.Direct
		}
		if s.System != nil {
			p.System = *s.System
		}
		p.Normalize()
		return policy.CheckSettings(p)
	})
}

// DismissPolicyNotice clears the migration notice.
func (a *App) DismissPolicyNotice() error {
	return a.updatePolicy(func(p *policy.Config) error { p.MigratedNotice = false; return nil })
}

// PutConditionSet creates (id "") or replaces a condition set.
func (a *App) PutConditionSet(id string, cs policy.ConditionSet) (policy.ConditionSet, error) {
	if cs.Rules == nil {
		cs.Rules = []policy.Rule{}
	}
	policy.FillRuleIDs(&cs)
	err := a.updatePolicy(func(p *policy.Config) error {
		if id == "" {
			cs.ID = policy.NewID("cs_")
			p.Conditions = append(p.Conditions, cs)
		} else {
			cur := p.ConditionSetByID(id)
			if cur == nil {
				return ErrPolicyNotFound
			}
			cs.ID = id
			*cur = cs
		}
		return policy.CheckConditionSet(p, p.ConditionSetByID(cs.ID))
	})
	return cs, err
}

// PutPolicySet creates (id "") or replaces a policy set; warnings describe
// call cycles through it.
func (a *App) PutPolicySet(id string, ps policy.PolicySet) (policy.PolicySet, []string, error) {
	if ps.Rules == nil {
		ps.Rules = []policy.Rule{}
	}
	policy.FillRuleIDs(&ps)
	var modeErr error
	ps.WalkEnds(func(t *policy.Terminal, path string) {
		if t.Type != policy.TermExit || modeErr != nil {
			return
		}
		if m, err := SanitizeExitMode(t.Paths, t.Mode); err != nil {
			modeErr = &policy.Error{Path: path + ".mode", Msg: err.Error()}
		} else {
			t.Mode = m
		}
	})
	if modeErr != nil {
		return ps, nil, modeErr
	}
	var warnings []string
	err := a.updatePolicy(func(p *policy.Config) error {
		if id == "" {
			ps.ID = policy.NewID("ps_")
			p.Policies = append(p.Policies, ps)
		} else {
			cur := p.PolicySetByID(id)
			if cur == nil {
				return ErrPolicyNotFound
			}
			ps.ID = id
			*cur = ps
		}
		w, err := policy.CheckPolicySet(p, p.PolicySetByID(ps.ID))
		warnings = w
		return err
	})
	return ps, warnings, err
}

// DeleteConditionSet removes an unreferenced condition set.
func (a *App) DeleteConditionSet(id string) error {
	return a.updatePolicy(func(p *policy.Config) error {
		for i := range p.Conditions {
			if p.Conditions[i].ID == id {
				if refs := p.InternalRefs(id); len(refs) > 0 {
					return &PolicyRefError{Refs: refs}
				}
				p.Conditions = append(p.Conditions[:i], p.Conditions[i+1:]...)
				return nil
			}
		}
		return ErrPolicyNotFound
	})
}

// DeletePolicySet removes an unreferenced policy set.
func (a *App) DeletePolicySet(id string) error {
	cfg := a.store.Get()
	if refs := a.PolicyRefs(&cfg, id); len(refs) > 0 {
		return &PolicyRefError{Refs: refs}
	}
	return a.updatePolicy(func(p *policy.Config) error {
		for i := range p.Policies {
			if p.Policies[i].ID == id {
				if refs := p.InternalRefs(id); len(refs) > 0 {
					return &PolicyRefError{Refs: refs}
				}
				p.Policies = append(p.Policies[:i], p.Policies[i+1:]...)
				return nil
			}
		}
		return ErrPolicyNotFound
	})
}

// PolicyRefs: every reference to set id — users, proxies, WireGuard peers,
// VPN defaults, and the block's own (policy, condition, direct).
func (a *App) PolicyRefs(cfg *Config, id string) []policy.Ref {
	var out []policy.Ref
	for _, u := range cfg.Users {
		if u.ExitPolicy == id {
			out = append(out, policy.Ref{Kind: "user", ID: u.ID, Name: u.Username})
		}
	}
	for _, p := range cfg.Proxies {
		if p.ExitPolicy == id {
			out = append(out, policy.Ref{Kind: "proxy", ID: p.ID, Name: p.ID})
		}
	}
	if cfg.WireGuard != nil {
		for _, p := range cfg.WireGuard.Peers {
			if p.ExitPolicy == id {
				out = append(out, policy.Ref{Kind: "wireguard", ID: p.Name, Name: p.Name})
			}
		}
	}
	if cfg.IKEv2 != nil && cfg.IKEv2.DefaultExitPolicy == id {
		out = append(out, policy.Ref{Kind: "vpn", ID: "ikev2", Name: "IKEv2"})
	}
	if cfg.Policy != nil {
		out = append(out, cfg.Policy.InternalRefs(id)...)
	}
	return out
}

// CheckExitPolicy: "" or an existing policy set (users, proxies, peers,
// VPN defaults reference it).
func (a *App) CheckExitPolicy(id string) error {
	if id == "" {
		return nil
	}
	if p := a.store.Get().Policy; p != nil && p.PolicySetByID(id) != nil {
		return nil
	}
	return fmt.Errorf("unknown policy set %q", id)
}

// policyExitPaths: the exit paths policy terminals name (path status,
// history recorder).
func policyExitPaths(cfg *Config) []string {
	if cfg.Policy == nil {
		return nil
	}
	var out []string
	for i := range cfg.Policy.Policies {
		cfg.Policy.Policies[i].WalkEnds(func(t *policy.Terminal, _ string) {
			if t.Type == policy.TermExit {
				out = append(out, t.Paths...)
			}
		})
	}
	sort.Strings(out)
	return out
}

// GeoIP and DNS-record hooks of policy conditions (set by the API layer,
// which owns the GeoIP store, and by the DNS proxy).
var (
	policyGeoIP   atomic.Pointer[func(netip.Addr) (string, uint)]
	policyDomains atomic.Pointer[func(netip.Addr) string]
)

// SetPolicyGeoIP installs the dst_geo / dst_asn lookup.
func SetPolicyGeoIP(fn func(netip.Addr) (country string, asn uint)) { policyGeoIP.Store(&fn) }

// SetPolicyDomainLookup installs the DNS-record lookup (dst_domain of an
// IP destination): the name an address was handed out for, "" = none.
func SetPolicyDomainLookup(fn func(netip.Addr) string) { policyDomains.Store(&fn) }

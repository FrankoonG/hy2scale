package app

import "github.com/FrankoonG/hy2scale/core/rules"

// RoutingRule is a routing rule (core/rules).
type RoutingRule = rules.Rule

// rulesHost: the rule engine's view of this node (core/rules.Host).
type rulesHost struct{ a *App }

func (h rulesHost) Rules() []rules.Rule { return h.a.store.Get().Rules }

func (h rulesHost) MeshAddrs() (string, []string) {
	cfg := h.a.store.Get()
	var listen string
	if cfg.Server != nil {
		listen = stripAddrBind(cfg.Server.Listen)
	}
	var peers []string
	for _, cl := range cfg.Clients {
		for _, ad := range cl.AllAddrs() {
			if !IsPunchAddr(ad) {
				peers = append(peers, stripAddrBind(ad))
			}
		}
	}
	return listen, peers
}

// RuleCaptureMode: how this host's traffic is captured (rules.Engine).
func (a *App) RuleCaptureMode() string { return a.rules.CaptureMode() }

// ApplyRule enables a routing rule.
func (a *App) ApplyRule(r RoutingRule) { a.rules.Apply(r) }

// RemoveRule disables a routing rule.
func (a *App) RemoveRule(id string) { a.rules.Remove(id) }

// CRUD operations for rules

func (a *App) AddRule(r RoutingRule) {
	a.store.Update(func(c *Config) {
		c.Rules = append(c.Rules, r)
	})
	if r.Enabled {
		a.ApplyRule(r)
	}
}

func (a *App) UpdateRule(id string, r RoutingRule) {
	a.RemoveRule(id)
	a.store.Update(func(c *Config) {
		for i, existing := range c.Rules {
			if existing.ID == id {
				c.Rules[i] = r
				return
			}
		}
	})
	if r.Enabled {
		a.ApplyRule(r)
	}
}

func (a *App) DeleteRule(id string) {
	a.RemoveRule(id)
	a.store.Update(func(c *Config) {
		for i, r := range c.Rules {
			if r.ID == id {
				c.Rules = append(c.Rules[:i], c.Rules[i+1:]...)
				return
			}
		}
	})
}

func (a *App) ToggleRule(id string, enabled bool) {
	var rule RoutingRule
	a.store.Update(func(c *Config) {
		for i, r := range c.Rules {
			if r.ID == id {
				c.Rules[i].Enabled = enabled
				rule = c.Rules[i]
				return
			}
		}
	})
	if enabled {
		a.ApplyRule(rule)
	} else {
		a.RemoveRule(id)
	}
}

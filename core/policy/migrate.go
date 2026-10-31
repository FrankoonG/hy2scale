package policy

import (
	"net"
	"sort"
	"strings"
)

// LegacyRule is a pre-policy routing rule (the old top-level `rules:`).
type LegacyRule struct {
	ID        string   `yaml:"id" json:"id"`
	Name      string   `yaml:"name" json:"name"`
	Type      string   `yaml:"type" json:"type"` // "ip" | "domain"
	Targets   []string `yaml:"targets" json:"targets"`
	ExitVia   string   `yaml:"exit_via" json:"exit_via"`
	ExitPaths []string `yaml:"exit_paths,omitempty" json:"exit_paths,omitempty"`
	ExitMode  string   `yaml:"exit_mode,omitempty" json:"exit_mode,omitempty"`
	Enabled   bool     `yaml:"enabled" json:"enabled"`
	Priority  int      `yaml:"priority,omitempty" json:"priority,omitempty"`
}

// MigratedName is the name of the policy set made from legacy rules.
const MigratedName = "规则（迁移）"

// specificity is the old engine's measure of how narrowly a target
// matches: prefix bits for IP targets (an address = full length, a range =
// the common prefix of its ends), the domain length for domain targets;
// -1 for an unusable target.
func specificity(typ, target string) int {
	target = strings.TrimSpace(target)
	if typ == "domain" {
		d := NormalizeDomain(strings.TrimPrefix(target, "*."))
		if d == "" {
			return -1
		}
		return len(d)
	}
	if strings.Contains(target, "/") {
		_, cidr, err := net.ParseCIDR(target)
		if err != nil {
			return -1
		}
		ones, _ := cidr.Mask.Size()
		return ones
	}
	if lo, hi, ok := strings.Cut(target, "-"); ok {
		a, b := norm4(net.ParseIP(strings.TrimSpace(lo))), norm4(net.ParseIP(strings.TrimSpace(hi)))
		if a == nil || b == nil || len(a) != len(b) {
			return -1
		}
		return commonPrefixBits(a, b)
	}
	ip := norm4(net.ParseIP(target))
	if ip == nil {
		return -1
	}
	return len(ip) * 8
}

func norm4(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

func commonPrefixBits(a, b []byte) int {
	n := 0
	for i := range a {
		x := a[i] ^ b[i]
		if x == 0 {
			n += 8
			continue
		}
		for x&0x80 == 0 {
			n++
			x <<= 1
		}
		break
	}
	return n
}

// ruleSpecificity: the most specific usable target of r.
func ruleSpecificity(r LegacyRule) int {
	best := -1
	for _, t := range r.Targets {
		if s := specificity(r.Type, t); s > best {
			best = s
		}
	}
	return best
}

// MigrateRules turns legacy rules into one policy set (nil when there are
// none): ordered by priority (descending), domain rules before IP rules
// (the old engine let a name match win over an address match), then the
// old engine's target specificity (descending), then rule id. Each rule
// becomes `when: match dst_ip|dst_domain targets`, `then: exit` with its
// paths and mode; enabled and name are kept.
func MigrateRules(old []LegacyRule) *PolicySet {
	if len(old) == 0 {
		return nil
	}
	rs := append([]LegacyRule(nil), old...)
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if (a.Type == "domain") != (b.Type == "domain") {
			return a.Type == "domain"
		}
		if sa, sb := ruleSpecificity(a), ruleSpecificity(b); sa != sb {
			return sa > sb
		}
		return a.ID < b.ID
	})
	ps := &PolicySet{ID: NewID("ps_"), Name: MigratedName, Rules: []PolicyRule{}}
	seen := map[string]bool{}
	for _, r := range rs {
		field := FieldDstIP
		if r.Type == "domain" {
			field = FieldDstDomain
		} else if r.Type != "ip" {
			continue
		}
		var vals []string
		for _, t := range r.Targets {
			t = strings.TrimSpace(t)
			if t == "" || checkValue(field, t) != nil {
				continue
			}
			if field == FieldDstDomain {
				t = strings.ToLower(strings.TrimSuffix(t, "."))
			}
			vals = append(vals, t)
		}
		if len(vals) == 0 {
			continue
		}
		paths := r.ExitPaths
		if len(paths) == 0 && r.ExitVia != "" {
			paths = []string{r.ExitVia}
		}
		if len(paths) == 0 {
			continue
		}
		id := r.ID
		if id == "" || seen[id] {
			id = NewID("r")
		}
		seen[id] = true
		name := r.Name
		if name == "" {
			name = r.ID
		}
		mode := r.ExitMode
		ps.Rules = append(ps.Rules, PolicyRule{
			ID: id, Name: name, Enabled: r.Enabled,
			When: &Condition{Type: CondMatch, Field: field, Values: vals},
			Then: Terminal{Type: TermExit, Paths: append([]string(nil), paths...), Mode: mode},
		})
	}
	return ps
}

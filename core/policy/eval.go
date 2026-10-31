package policy

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
)

// Program is a compiled Config, safe for concurrent evaluation.
type Program struct {
	maxDepth int
	direct   string
	conds    map[string]*cset
	pols     map[string]*pset
}

type cnode struct {
	typ   string
	items []*cnode
	item  *cnode
	ref   string
	leaf  *leaf
}

type crule struct {
	enabled bool
	when    *cnode
	result  bool
}

type cset struct {
	id, name string
	rules    []crule
}

type prule struct {
	id, name string
	enabled  bool
	when     *cnode
	then     Terminal
}

type pset struct {
	id, name string
	rules    []prule
}

// Compile builds the program of cfg (nil = empty). Invalid values are
// skipped (validation rejects them on save); a leaf left without values
// never matches.
func Compile(cfg *Config) *Program {
	p := &Program{maxDepth: cfg.EffectiveMaxDepth(), conds: map[string]*cset{}, pols: map[string]*pset{}}
	if cfg == nil {
		return p
	}
	p.direct = cfg.Direct
	for _, cs := range cfg.Conditions {
		c := &cset{id: cs.ID, name: cs.Name}
		for _, r := range cs.Rules {
			c.rules = append(c.rules, crule{enabled: r.Enabled, when: compileCond(r.When), result: r.Result})
		}
		p.conds[cs.ID] = c
	}
	for _, ps := range cfg.Policies {
		s := &pset{id: ps.ID, name: ps.Name}
		for _, r := range ps.Rules {
			s.rules = append(s.rules, prule{id: r.ID, name: r.Name, enabled: r.Enabled, when: compileCond(r.When), then: r.Then})
		}
		p.pols[ps.ID] = s
	}
	return p
}

func compileCond(c *Condition) *cnode {
	if c == nil {
		return nil
	}
	n := &cnode{typ: c.Type, ref: c.Ref}
	switch c.Type {
	case CondAll, CondAny:
		for i := range c.Items {
			n.items = append(n.items, compileCond(&c.Items[i]))
		}
	case CondNot:
		n.item = compileCond(c.Item)
	case CondMatch:
		l := &leaf{field: c.Field}
		for _, v := range c.Values {
			compileValue(l, v)
		}
		n.leaf = l
	}
	return n
}

// MaxDepth is the effective call depth limit.
func (p *Program) MaxDepth() int { return p.maxDepth }

// Direct is the direct entry ("" = none).
func (p *Program) Direct() string { return p.direct }

// PolicyName is the name of a policy set ("" = unknown).
func (p *Program) PolicyName(id string) string {
	if s := p.pols[id]; s != nil {
		return s.name
	}
	return ""
}

// HasPolicy reports whether a policy set exists.
func (p *Program) HasPolicy(id string) bool { return p.pols[id] != nil }

// maxRefDepth bounds nested condition-set references (cycles are refused
// on save; this is the backstop).
const maxRefDepth = 64

type evaluator struct {
	p    *Program
	ctx  context.Context
	c    *Conn
	memo map[string]bool
}

// Evaluate decides where c goes, starting at s.
func (p *Program) Evaluate(ctx context.Context, c *Conn, s Start) Decision {
	if s.Exit != nil {
		return Decision{Terminal: *s.Exit, Reason: ReasonExit}
	}
	id := s.Policy
	if id == "" {
		if p.direct == "" || p.pols[p.direct] == nil {
			return Decision{Terminal: Terminal{Type: TermDefault}, Reason: ReasonDirect}
		}
		id = p.direct
	}
	ev := &evaluator{p: p, ctx: ctx, c: c}
	var d Decision
	for depth := 1; ; depth++ {
		if depth > p.maxDepth {
			d.Terminal = Terminal{Type: TermDeny, Reject: true}
			d.Reason = ReasonDepth
			return ev.finish(d)
		}
		ps := p.pols[id]
		if ps == nil {
			d.Terminal = Terminal{Type: TermDefault}
			d.Policy = id
			d.Reason = ReasonMissing
			return ev.finish(d)
		}
		var hit *prule
		for i := range ps.rules {
			r := &ps.rules[i]
			if !r.enabled {
				continue
			}
			if r.when == nil || ev.cond(r.when, 0) {
				hit = r
				break
			}
		}
		d.Policy = id
		if hit == nil {
			d.Chain = append(d.Chain, Hop{Policy: id, PolicyName: ps.name, Depth: depth})
			d.Terminal = Terminal{Type: TermDefault}
			d.Reason = ReasonDefault
			return ev.finish(d)
		}
		d.Chain = append(d.Chain, Hop{Policy: id, PolicyName: ps.name, Rule: hit.id, RuleName: hit.name, Depth: depth})
		if hit.then.Type == TermCall {
			id = hit.then.Policy
			continue
		}
		d.Terminal = hit.then
		d.Reason = ReasonRule
		return ev.finish(d)
	}
}

func (ev *evaluator) finish(d Decision) Decision {
	if u := ev.c.lazy.used; len(u) > 0 {
		d.Fields = make(map[string]string, len(u))
		for k, v := range u {
			d.Fields[k] = v
		}
	}
	return d
}

func (ev *evaluator) cond(n *cnode, depth int) bool {
	if n == nil {
		return true
	}
	switch n.typ {
	case CondAll:
		for _, it := range n.items {
			if !ev.cond(it, depth) {
				return false
			}
		}
		return true
	case CondAny:
		for _, it := range n.items {
			if ev.cond(it, depth) {
				return true
			}
		}
		return false
	case CondNot:
		return !ev.cond(n.item, depth)
	case CondRef:
		return ev.condSet(n.ref, depth+1)
	case CondMatch:
		return ev.leaf(n.leaf)
	}
	return false
}

// condSet evaluates a condition set: first enabled matching rule's result,
// else false. Memoised per evaluation.
func (ev *evaluator) condSet(id string, depth int) bool {
	if depth > maxRefDepth {
		return false
	}
	if v, ok := ev.memo[id]; ok {
		return v
	}
	cs := ev.p.conds[id]
	res := false
	if cs != nil {
		for _, r := range cs.rules {
			if !r.enabled {
				continue
			}
			if r.when == nil || ev.cond(r.when, depth) {
				res = r.result
				break
			}
		}
	}
	if ev.memo == nil {
		ev.memo = map[string]bool{}
	}
	ev.memo[id] = res
	return res
}

func (ev *evaluator) note(field, v string) {
	if v == "" {
		return
	}
	if ev.c.lazy.used == nil {
		ev.c.lazy.used = map[string]string{}
	}
	ev.c.lazy.used[field] = v
}

func (ev *evaluator) str(field, v string, l *leaf) bool {
	if v == "" {
		return false
	}
	ev.note(field, v)
	return l.strs[v]
}

func (ev *evaluator) leaf(l *leaf) bool {
	if l == nil {
		return false
	}
	c := ev.c
	switch l.field {
	case FieldSrcKind:
		return ev.str(l.field, strings.ToLower(c.SrcKind), l)
	case FieldUser:
		return ev.str(l.field, c.User, l)
	case FieldSrcNode:
		return ev.str(l.field, c.SrcNode, l)
	case FieldInIface:
		return ev.str(l.field, c.InIface, l)
	case FieldSrcMAC:
		return ev.str(l.field, NormalizeMAC(c.SrcMAC), l)
	case FieldProto:
		return ev.str(l.field, strings.ToLower(c.Proto), l)
	case FieldSrcIP:
		if c.SrcIP.IsValid() {
			ev.note(l.field, c.SrcIP.Unmap().String())
		}
		return matchIP(l.ips, c.SrcIP)
	case FieldDstIP:
		ip := ev.dstIP()
		if ip.IsValid() {
			ev.note(l.field, ip.String())
		}
		return matchIP(l.ips, ip)
	case FieldSrcPort:
		if c.SrcPort != 0 {
			ev.note(l.field, strconv.Itoa(int(c.SrcPort)))
		}
		return matchPort(l.ports, c.SrcPort, c.SrcPort != 0)
	case FieldDstPort:
		if c.DstPort != 0 {
			ev.note(l.field, strconv.Itoa(int(c.DstPort)))
		}
		return matchPort(l.ports, c.DstPort, c.DstPort != 0)
	case FieldDstDomain:
		name := ev.domain()
		if name == "" {
			return false
		}
		ev.note(l.field, name)
		for _, p := range l.domains {
			if p.match(name) {
				return true
			}
		}
		return false
	case FieldDstGeo:
		country, _ := ev.geo()
		if country == "" {
			return false
		}
		ev.note(l.field, country)
		return l.strs[country]
	case FieldDstASN:
		_, asn := ev.geo()
		if asn == 0 {
			return false
		}
		ev.note(l.field, strconv.FormatUint(uint64(asn), 10))
		return l.asns[asn]
	}
	return false
}

// dstIP: the destination address, resolving a requested name on demand.
func (ev *evaluator) dstIP() netip.Addr {
	c := ev.c
	if c.DstIP.IsValid() {
		return c.DstIP.Unmap()
	}
	if c.lazy.ipDone {
		return c.lazy.ip
	}
	c.lazy.ipDone = true
	if c.DstHost == "" {
		return netip.Addr{}
	}
	if a, err := netip.ParseAddr(strings.Trim(c.DstHost, "[]")); err == nil {
		c.lazy.ip = a.Unmap().WithZone("")
		return c.lazy.ip
	}
	if c.Resolve != nil {
		if a, err := c.Resolve(ev.ctx, c.DstHost); err == nil && a.IsValid() {
			c.lazy.ip = a.Unmap().WithZone("")
		}
	}
	return c.lazy.ip
}

// domain: the requested name, else the sniffed name, else a DNS record,
// else (once) sniffing.
func (ev *evaluator) domain() string {
	c := ev.c
	if c.DstHost != "" {
		if _, err := netip.ParseAddr(strings.Trim(c.DstHost, "[]")); err != nil {
			return NormalizeDomain(c.DstHost)
		}
	}
	if c.Sniffed != "" {
		return NormalizeDomain(c.Sniffed)
	}
	if !c.lazy.domainDone {
		c.lazy.domainDone = true
		if c.DomainOf != nil {
			if ip := ev.dstIP(); ip.IsValid() {
				c.lazy.domain = NormalizeDomain(c.DomainOf(ip))
			}
		}
	}
	if c.lazy.domain != "" {
		return c.lazy.domain
	}
	if c.Sniff != nil && !c.lazy.sniffDone {
		c.lazy.sniffDone = true
		c.Sniffed = NormalizeDomain(c.Sniff(ev.ctx))
		return c.Sniffed
	}
	return ""
}

func (ev *evaluator) geo() (string, uint) {
	c := ev.c
	if c.lazy.geoDone {
		return c.lazy.country, c.lazy.asn
	}
	c.lazy.geoDone = true
	if c.GeoIP == nil {
		return "", 0
	}
	ip := ev.dstIP()
	if !ip.IsValid() {
		return "", 0
	}
	country, asn := c.GeoIP(ip)
	c.lazy.country, c.lazy.asn = strings.ToUpper(country), asn
	return c.lazy.country, c.lazy.asn
}

// Engine holds the current program (hot-swapped on every config change).
type Engine struct{ p atomic.Pointer[Program] }

// NewEngine returns an engine with an empty program.
func NewEngine() *Engine {
	e := &Engine{}
	e.p.Store(Compile(nil))
	return e
}

// Load compiles cfg and makes it current.
func (e *Engine) Load(cfg *Config) { e.p.Store(Compile(cfg)) }

// Program is the current program.
func (e *Engine) Program() *Program { return e.p.Load() }

// Evaluate evaluates c on the current program.
func (e *Engine) Evaluate(ctx context.Context, c *Conn, s Start) Decision {
	return e.p.Load().Evaluate(ctx, c, s)
}

// ChainText renders a chain as "name/rule > name/rule" (rule name, else
// id; a default end as "default").
func ChainText(chain []Hop) string {
	var b strings.Builder
	for i, h := range chain {
		if i > 0 {
			b.WriteString(" > ")
		}
		n := h.PolicyName
		if n == "" {
			n = h.Policy
		}
		b.WriteString(n)
		b.WriteByte('/')
		switch {
		case h.Rule == "":
			b.WriteString("default")
		case h.RuleName != "":
			b.WriteString(h.RuleName)
		default:
			b.WriteString(h.Rule)
		}
	}
	return b.String()
}

// Package policy is hy2scale's rule model: condition sets (named boolean
// predicates over a connection), policy sets (ordered rules whose first
// match picks a terminal) and the direct entry (the policy set that flows
// without an exit of their own enter). It is platform independent; the
// application implements Runtime (terminal execution) and the capture
// backend (core/policy/capture) feeds intercepted system flows into it.
// Spec: docs/v1.4.0-policy.md.
package policy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// Source kinds (Conn.SrcKind).
const (
	KindLocal     = "local"   // this host's own outgoing traffic (capture layer 1)
	KindForward   = "forward" // traffic forwarded through this host (capture layer 2)
	KindSOCKS5    = "socks5"
	KindHTTP      = "http"
	KindSS        = "ss"
	KindTrojan    = "trojan"
	KindHy2       = "hy2"
	KindIKEv2     = "ikev2"
	KindL2TP      = "l2tp"
	KindOpenVPN   = "openvpn"
	KindWireGuard = "wireguard"
	KindAccess    = "access" // node-access users
	KindRelay     = "relay"  // other nodes leaving through this one
)

// Kinds lists every source kind.
var Kinds = []string{KindLocal, KindForward, KindSOCKS5, KindHTTP, KindSS, KindTrojan, KindHy2, KindIKEv2,
	KindL2TP, KindOpenVPN, KindWireGuard, KindAccess, KindRelay}

// Condition fields (Condition.Field).
const (
	FieldSrcKind   = "src_kind"
	FieldUser      = "user"
	FieldSrcNode   = "src_node"
	FieldSrcIP     = "src_ip"
	FieldSrcPort   = "src_port"
	FieldInIface   = "in_iface"
	FieldSrcMAC    = "src_mac"
	FieldProto     = "proto"
	FieldDstIP     = "dst_ip"
	FieldDstPort   = "dst_port"
	FieldDstDomain = "dst_domain"
	FieldDstGeo    = "dst_geo"
	FieldDstASN    = "dst_asn"
)

// Fields lists every condition field.
var Fields = []string{FieldSrcKind, FieldUser, FieldSrcNode, FieldSrcIP, FieldSrcPort, FieldInIface,
	FieldSrcMAC, FieldProto, FieldDstIP, FieldDstPort, FieldDstDomain, FieldDstGeo, FieldDstASN}

// Conn is the one argument every set sees (spec §2.1). Fields a source
// does not have stay zero; a condition on a missing field is false.
// Evaluation is single-goroutine per Conn; the lazy fields are computed at
// most once and cached on it.
type Conn struct {
	SrcKind string     // one of the Kind* constants
	User    string     // user (internal id); WireGuard: the peer name
	SrcNode string     // relay: the node the flow comes from (name)
	SrcIP   netip.Addr // source address
	SrcPort uint16
	InIface string // inbound interface (capture layer 2)
	SrcMAC  string // source MAC (capture layer 2, neighbour table)
	Proto   string // "tcp" | "udp"
	DstIP   netip.Addr
	DstPort uint16
	// DstHost: the host name the client asked for ("" = it asked for DstIP).
	// When set, dst_domain is this name and DstIP is resolved on demand.
	DstHost string
	// Sniffed: a host name already recovered from the client's first bytes
	// (TLS SNI / HTTP Host) by the caller; "" = not sniffed (yet).
	Sniffed string

	// Hooks; nil = unavailable.

	// DomainOf names an IP from DNS records (the DNS proxy's answers).
	DomainOf func(ip netip.Addr) string
	// GeoIP: country (ISO alpha-2, "" unknown) and ASN (0 unknown) of ip.
	// The runtime fills it when nil.
	GeoIP func(ip netip.Addr) (country string, asn uint)
	// Sniff reads the client's first bytes and returns the host name they
	// carry ("" = none). Called at most once, only when a condition needs
	// dst_domain and neither DstHost, Sniffed nor DomainOf names the
	// destination. The implementation keeps the bytes it read and forwards
	// them to the dialled connection itself.
	Sniff func(ctx context.Context) string
	// Resolve: dst_ip for a DstHost destination. The runtime fills it
	// when nil.
	Resolve func(ctx context.Context, host string) (netip.Addr, error)

	lazy lazyFields
}

type lazyFields struct {
	domainDone bool
	domain     string
	ipDone     bool
	ip         netip.Addr // resolved DstHost
	geoDone    bool
	country    string
	asn        uint
	sniffDone  bool
	used       map[string]string // field → value seen, for /api/policy/test
}

// Start is where evaluation begins.
type Start struct {
	// Policy: the policy set to enter; "" = direct (the direct entry when
	// one is set, else the default terminal).
	Policy string
	// Exit: a fixed exit group (the source's own exit); no policy is
	// evaluated. Takes precedence over Policy.
	Exit *Terminal
}

// Direct is the start of flows without an exit selection.
var Direct = Start{}

// AtPolicy starts in the policy set id.
func AtPolicy(id string) Start { return Start{Policy: id} }

// ViaExit is a fixed exit group start.
func ViaExit(paths []string, mode string) Start {
	return Start{Exit: &Terminal{Type: TermExit, Paths: paths, Mode: mode}}
}

// Hop is one rule (or default end) the evaluation passed through.
type Hop struct {
	Policy     string `json:"policy"`
	PolicyName string `json:"policy_name"`
	Rule       string `json:"rule"`      // the deciding rule's id; "" = a default end
	RuleName   string `json:"rule_name"` // its name ("" = none)
	// Path: 1-based rule positions from the set down to the deciding rule
	// ([2, 1] = rule 1 of the flow in rule 2). For a default end: the
	// positions of the flow whose default it is ([] = the set's).
	Path      []int `json:"path"`
	Otherwise bool  `json:"otherwise,omitempty"` // a default end decided
	Depth     int   `json:"depth"`               // 1 = first policy set entered
}

// Decision is the outcome of evaluating a Conn.
type Decision struct {
	Terminal Terminal // never TermCall
	Chain    []Hop
	// Policy: the policy set that gave the terminal ("" = none).
	Policy string
	// Reason: why the terminal was chosen — ReasonRule, ReasonDefault
	// (a set's default end), ReasonDirect (no direct entry), ReasonExit
	// (a fixed exit group), ReasonDepth (call depth exceeded),
	// ReasonMissing (a set no longer exists).
	Reason string
	// Fields: the connection fields evaluation used and their values.
	Fields map[string]string
}

// Decision reasons.
const (
	ReasonRule    = "rule"
	ReasonDefault = "default"
	ReasonDirect  = "direct"
	ReasonExit    = "exit"
	ReasonDepth   = "depth"
	ReasonMissing = "missing"
)

// Runtime evaluates a connection and executes its terminal: exit → the
// exit engine; iface → the interface/egress binding (Linux only); default
// → the system default route; deny → a *DenyError (callers answer it:
// reject = RST / refusal reply, drop = silent close; UDP: drop).
// The returned conn is a connected stream (TCP) or datagram flow (UDP).
type Runtime interface {
	DialTCP(ctx context.Context, c *Conn, s Start) (net.Conn, Decision, error)
	DialUDP(ctx context.Context, c *Conn, s Start) (net.Conn, Decision, error)
}

// DenyError: the decision was deny.
type DenyError struct {
	Reject bool   // true: actively refuse (RST / unreachable / refusal reply); false: drop silently
	Reason string // Decision.Reason
}

func (e *DenyError) Error() string {
	if e.Reason == ReasonDepth {
		return "denied by policy (depth limit)"
	}
	if e.Reject {
		return "rejected by policy"
	}
	return "dropped by policy"
}

// AsDeny reports whether err is (or wraps) a policy deny.
func AsDeny(err error) (*DenyError, bool) {
	var d *DenyError
	if errors.As(err, &d) {
		return d, true
	}
	return nil, false
}

// ErrIfaceUnavailable: an iface terminal on a platform without interface
// binding.
var ErrIfaceUnavailable = errors.New("interface terminal unavailable on this platform")

// ---- model (spec §2.2–§2.4, §4) ----

// Condition types.
const (
	CondAll   = "all"
	CondAny   = "any"
	CondNot   = "not"
	CondRef   = "ref"
	CondMatch = "match"
)

// Condition is a recursive condition node.
type Condition struct {
	Type   string      `yaml:"type" json:"type"`
	Items  []Condition `yaml:"items,omitempty" json:"items,omitempty"`   // all / any
	Item   *Condition  `yaml:"item,omitempty" json:"item,omitempty"`     // not
	Ref    string      `yaml:"ref,omitempty" json:"ref,omitempty"`       // ref: a condition set id
	Field  string      `yaml:"field,omitempty" json:"field,omitempty"`   // match
	Values []string    `yaml:"values,omitempty" json:"values,omitempty"` // match (any of)
}

// Action types. A rule's then is an end (the set kind decides which ends
// are allowed) or a nested flow; a set's default is an end.
const (
	// policy-set ends
	TermDeny    = "deny"
	TermExit    = "exit"
	TermIface   = "iface"
	TermCall    = "call"
	TermDefault = "default"
	// condition-set end
	TermResult = "result"
	// TermFlow: a nested flow (rules + optional default), evaluated like a
	// set. When none of its rules match and it has no default, evaluation
	// continues with the rule after the one that entered it.
	TermFlow = "flow"
)

// Terminal is a rule's then, a flow's default, or a decision's terminal.
type Terminal struct {
	Type   string   `yaml:"type" json:"type"`
	Reject bool     `yaml:"reject,omitempty" json:"reject,omitempty"` // deny
	Paths  []string `yaml:"paths,omitempty" json:"paths,omitempty"`   // exit
	Mode   string   `yaml:"mode,omitempty" json:"mode,omitempty"`     // exit: ""|quality|aggregate
	Iface  string   `yaml:"iface,omitempty" json:"iface,omitempty"`   // iface
	Src    string   `yaml:"src,omitempty" json:"src,omitempty"`       // iface: source address (optional)
	Policy string   `yaml:"policy,omitempty" json:"policy,omitempty"` // call
	Result bool     `yaml:"result,omitempty" json:"result,omitempty"` // result
	// flow
	Rules   []Rule    `yaml:"rules,omitempty" json:"rules,omitempty"`
	Default *Terminal `yaml:"default,omitempty" json:"default,omitempty"` // nil = continue outside
}

// Rule: when its condition holds, then decides (an end, or a nested flow).
type Rule struct {
	ID      string     `yaml:"id" json:"id"`
	Name    string     `yaml:"name,omitempty" json:"name,omitempty"`
	Enabled bool       `yaml:"enabled" json:"enabled"`
	When    *Condition `yaml:"when,omitempty" json:"when,omitempty"` // nil = always
	Then    Terminal   `yaml:"then" json:"then"`
}

// Set is a condition set or a policy set; both are the same flow: the
// first enabled rule whose When holds decides, else the default. They
// differ only in their ends: a condition set returns true/false (default
// false), a policy set ends in a terminal (default: the default route).
type Set struct {
	ID      string    `yaml:"id" json:"id"`
	Name    string    `yaml:"name" json:"name"`
	Rules   []Rule    `yaml:"rules" json:"rules"`
	Default *Terminal `yaml:"default,omitempty" json:"default,omitempty"` // nil = the built-in default
}

// Set kinds (validation of the ends).
type SetKind int

const (
	KindCondition SetKind = iota
	KindPolicy
)

// BuiltinDefault is a set's end when Default is nil.
func BuiltinDefault(k SetKind) Terminal {
	if k == KindCondition {
		return Terminal{Type: TermResult, Result: false}
	}
	return Terminal{Type: TermDefault}
}

// Aliases kept for readability at call sites.
type (
	ConditionSet = Set
	PolicySet    = Set
	CondRule     = Rule
	PolicyRule   = Rule
)

// MaxFlowDepth bounds nested flows inside one set.
const MaxFlowDepth = 8

// WalkEnds calls fn with every end of s (rule thens that are not flows,
// flow defaults, the set default) and its path.
func (s *Set) WalkEnds(fn func(t *Terminal, path string)) {
	walkRuleEnds(s.Rules, "rules", fn)
	if s.Default != nil {
		fn(s.Default, "default")
	}
}

func walkRuleEnds(rules []Rule, path string, fn func(t *Terminal, path string)) {
	for i := range rules {
		p := fmt.Sprintf("%s[%d].then", path, i)
		t := &rules[i].Then
		if t.Type != TermFlow {
			fn(t, p)
			continue
		}
		walkRuleEnds(t.Rules, p+".rules", fn)
		if t.Default != nil {
			fn(t.Default, p+".default")
		}
	}
}

// WalkRules calls fn with every rule of s, nested ones included.
func (s *Set) WalkRules(fn func(r *Rule, path string)) { walkRules(s.Rules, "rules", fn) }

func walkRules(rules []Rule, path string, fn func(r *Rule, path string)) {
	for i := range rules {
		p := fmt.Sprintf("%s[%d]", path, i)
		fn(&rules[i], p)
		if rules[i].Then.Type == TermFlow {
			walkRules(rules[i].Then.Rules, p+".then.rules", fn)
		}
	}
}

// Stop behaviours of a capture layer.
const (
	OnStopOpen  = "open"
	OnStopBlock = "block"
)

// LocalCapture: capture layer 1 (this host's own outgoing traffic).
type LocalCapture struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	DNS     bool   `yaml:"dns" json:"dns"`
	OnStop  string `yaml:"on_stop" json:"on_stop"` // open | block
}

// ForwardCapture: capture layer 2 (traffic forwarded through this host).
type ForwardCapture struct {
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	Interfaces []string `yaml:"interfaces" json:"interfaces"`
	Sources    []string `yaml:"sources" json:"sources"` // CIDRs; empty = any
	DNS        bool     `yaml:"dns" json:"dns"`
	OnStop     string   `yaml:"on_stop" json:"on_stop"`
}

// System: system traffic capture (spec §3).
type System struct {
	Local   LocalCapture   `yaml:"local" json:"local"`
	Forward ForwardCapture `yaml:"forward" json:"forward"`
	// LocalNets: destinations in the subnets of this host's interfaces are
	// never captured, by either layer; the system routes them as before
	// (Docker drops captured flows to its containers, which come back in
	// on lo). nil = on.
	LocalNets *bool `yaml:"local_nets,omitempty" json:"local_nets,omitempty"`
}

// LocalNetsDirect: the local-subnet bypass is on.
func (s System) LocalNetsDirect() bool { return s.LocalNets == nil || *s.LocalNets }

// Depth limits.
const (
	DefaultMaxDepth = 16
	MinMaxDepth     = 2
	MaxMaxDepth     = 64
)

// Config is the `policy:` block of the node configuration.
type Config struct {
	MaxDepth   int            `yaml:"max_depth" json:"max_depth"`
	Direct     string         `yaml:"direct" json:"direct"` // a policy set id; "" = none
	System     System         `yaml:"system" json:"system"`
	Conditions []ConditionSet `yaml:"conditions" json:"conditions"`
	Policies   []PolicySet    `yaml:"policies" json:"policies"`
	// MigratedNotice: show the one-time migration notice.
	MigratedNotice bool `yaml:"migrated_notice,omitempty" json:"migrated_notice"`
}

// DefaultSystem: both layers off, DNS proxy on, open on stop.
func DefaultSystem() System {
	return System{
		Local:   LocalCapture{DNS: true, OnStop: OnStopOpen},
		Forward: ForwardCapture{DNS: true, OnStop: OnStopOpen, Interfaces: []string{}, Sources: []string{}},
	}
}

// NewConfig is an empty policy block with defaults.
func NewConfig() *Config {
	return &Config{MaxDepth: DefaultMaxDepth, System: DefaultSystem(),
		Conditions: []ConditionSet{}, Policies: []PolicySet{}}
}

// EffectiveMaxDepth clamps MaxDepth (0 = default).
func (c *Config) EffectiveMaxDepth() int {
	if c == nil || c.MaxDepth == 0 {
		return DefaultMaxDepth
	}
	if c.MaxDepth < MinMaxDepth {
		return MinMaxDepth
	}
	if c.MaxDepth > MaxMaxDepth {
		return MaxMaxDepth
	}
	return c.MaxDepth
}

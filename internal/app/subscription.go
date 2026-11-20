package app

// Subscriptions: one user's links as a list for subscription converters
// (SublinkPro, Sub-Store, subconverter, ...), at GET /sub/<id>/<token>
// (api/subscription.go). A shared user's is the same on every holder: id
// is the shared user's gid and the list holds every holder's entrance
// (each holder publishes its own with its member entry).

import (
	"regexp"
	"unicode/utf8"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// UserSub: a user's subscription (shared between holders). It lists the
// protocols the user may use.
type UserSub struct {
	Token string `yaml:"token" json:"token"`
	// Name: the entries' name template (SubNamePlaceholders); "" =
	// SubNameDefault.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// NoLAN leaves the LAN-address entries out.
	NoLAN bool `yaml:"no_lan,omitempty" json:"no_lan,omitempty"`
	// Enabled: on a user save only — turns the subscription on (a token
	// made when there is none) or off (the token dropped, the settings
	// kept). Never stored.
	Enabled *bool `yaml:"-" json:"enabled,omitempty"`
}

// SubNameDefault: the entries' name template when none is set.
const SubNameDefault = "{user} {node} {policy} {proto} {lan}"

// SubNamePlaceholders: what a name template may hold, as {name}.
var SubNamePlaceholders = []string{"user", "node", "policy", "proto", "lan", "host"}

// SubNameMax bounds a name template (characters).
const SubNameMax = 128

// ValidateSubName checks a name template.
func ValidateSubName(t string) error {
	if utf8.RuneCountInString(t) > SubNameMax {
		return fmt.Errorf("name format: at most %d characters", SubNameMax)
	}
	for _, c := range t {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("name format: no control characters")
		}
	}
	return nil
}

var (
	emptyPairs = regexp.MustCompile(`\(\s*\)|\[\s*\]|（\s*）|【\s*】|<\s*>`)
	spaces     = regexp.MustCompile(`\s+`)
)

// FormatLinkName fills a name template: unknown {words} stay as written,
// brackets left empty and repeated blanks go.
func FormatLinkName(tmpl string, vals map[string]string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = SubNameDefault
	}
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		if tmpl[i] == '{' {
			if j := strings.IndexByte(tmpl[i:], '}'); j > 0 {
				if v, ok := vals[tmpl[i+1:i+j]]; ok {
					b.WriteString(v)
					i += j + 1
					continue
				}
			}
		}
		b.WriteByte(tmpl[i])
		i++
	}
	out := b.String()
	for {
		n := emptyPairs.ReplaceAllString(out, "")
		if n == out {
			break
		}
		out = n
	}
	return strings.TrimSpace(spaces.ReplaceAllString(out, " "))
}

// SubscriptionConfig: the node's settings for subscriptions served here.
type SubscriptionConfig struct {
	// NoRelay refuses requests other nodes pass on.
	NoRelay bool `yaml:"no_relay,omitempty" json:"no_relay"`
	// HTTP also answers subscriptions over plain http on the web port.
	HTTP bool `yaml:"http,omitempty" json:"http"`
}

// NewSubToken: a fresh subscription token.
func NewSubToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// AttrIID: the node attribute carrying the installation id, so nodes
// further away can be found by it.
const AttrIID = "iid"

// ApplyIIDAttr announces the installation id.
func (a *App) ApplyIIDAttr() {
	if iid := a.SelfIID(); iid != "" {
		b, _ := json.Marshal(iid)
		a.node.SetAttr(AttrIID, b)
	}
}

// PathsToIID: the paths to the node with installation id iid (Reachable),
// fastest first.
func (a *App) PathsToIID(iid string) []string {
	if iid == "" || iid == a.SelfIID() {
		return nil
	}
	want, _ := json.Marshal(iid)
	var out []string
	for _, r := range a.reach("") {
		if string(r.IID) == string(want) {
			out = append(out, r.Path())
		}
	}
	return out
}

// SubID: the first part of u's subscription path: its gid when shared,
// else this installation's id.
func (a *App) SubID(u *UserConfig) string {
	if u.Shared != nil {
		return u.Shared.GID
	}
	return a.SelfIID()
}

// SubUser: the user whose subscription id/token is, held here.
func (a *App) SubUser(id, token string) (UserConfig, bool) {
	if token == "" {
		return UserConfig{}, false
	}
	self := a.SelfIID()
	for _, u := range a.store.Get().Users {
		if u.Sub == nil || u.Sub.Token == "" || u.Kind() == KindAccess {
			continue
		}
		if (u.Shared != nil && u.Shared.GID == id) || (u.Shared == nil && id == self) {
			if subtle.ConstantTimeCompare([]byte(token), []byte(u.Sub.Token)) == 1 {
				return u, true
			}
		}
	}
	return UserConfig{}, false
}

// SubLinks: u's subscription: this node's entrance, and every other
// holder's for a shared user, by node name.
func (a *App) SubLinks(u UserConfig) []ProxyLink {
	ents := []Entrance{a.LocalEntrance(&u, "")}
	if st := u.Shared; st != nil {
		self := a.SelfIID()
		for iid, m := range st.Members {
			if iid != self && !m.Left && m.Ent != nil {
				ents = append(ents, *m.Ent)
			}
		}
	}
	sort.SliceStable(ents, func(i, j int) bool { return ents[i].Node < ents[j].Node })
	opt := LinkOptions{LAN: u.Sub == nil || !u.Sub.NoLAN, NodeName: true, NameFormat: SubNameDefault}
	if u.Sub != nil && u.Sub.Name != "" {
		opt.NameFormat = u.Sub.Name
	}
	var out []ProxyLink
	for _, e := range ents {
		out = append(out, EntranceLinks(e, &u, opt)...)
	}
	// names alike (a format leaving something out): told apart by number
	seen := map[string]int{}
	for _, l := range out {
		seen[l.Name]++
	}
	n := map[string]int{}
	for i := range out {
		if nm := out[i].Name; seen[nm] > 1 {
			n[nm]++
			out[i].Rename(fmt.Sprintf("%s %d", nm, n[nm]))
		}
	}
	return out
}

// SubscriptionBody: the subscription in format "" (base64 of the links, one
// per line) or "clash" (a proxies-only YAML), with its content type.
func SubscriptionBody(links []ProxyLink, format string) ([]byte, string) {
	if format == "clash" {
		var list []*yaml.Node
		for _, l := range links {
			m := &yaml.Node{Kind: yaml.MappingNode}
			for _, e := range l.clash {
				var v yaml.Node
				v.Encode(e.v)
				m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: e.k}, &v)
			}
			list = append(list, m)
		}
		doc := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "proxies"},
			{Kind: yaml.SequenceNode, Content: list},
		}}
		if len(list) == 0 {
			doc.Content[1] = &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		}
		b, _ := yaml.Marshal(doc)
		return b, "text/yaml; charset=utf-8"
	}
	var lines []string
	for _, l := range links {
		lines = append(lines, l.URL)
	}
	return []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))), "text/plain; charset=utf-8"
}

// SubUserInfo: the subscription-userinfo header of u (a shared user's
// traffic is every holder's).
func (a *App) SubUserInfo(u UserConfig) string {
	s := fmt.Sprintf("upload=0; download=%d; total=%d", a.userUsed(u), u.TrafficLimit)
	if u.ExpiryDate != "" {
		if t, err := time.ParseInLocation("2006-01-02", u.ExpiryDate, time.Local); err == nil {
			s += fmt.Sprintf("; expire=%d", t.Unix())
		}
	}
	return s
}

// userUsed: the traffic u used in its period (live, shared holders included).
func (a *App) userUsed(u UserConfig) int64 {
	a.usersMu.RLock()
	defer a.usersMu.RUnlock()
	if x, ok := a.userIndex[u.Username]; ok {
		return atomic.LoadInt64(&x.TrafficUsed) + atomic.LoadInt64(&x.RemoteUsed)
	}
	return u.TrafficUsed
}

// RefreshSharedEntrances publishes this node's entrance of each shared user
// with a subscription (its member entry) when it changed; returns the gids
// synced.
func (a *App) RefreshSharedEntrances() []string {
	self := a.SelfIID()
	cfg := a.store.Get()
	want := map[string]Entrance{}
	for i := range cfg.Users {
		u := &cfg.Users[i]
		if u.Shared == nil || u.Sub == nil || u.Sub.Token == "" {
			continue
		}
		want[u.Shared.GID] = a.LocalEntrance(u, "")
	}
	if len(want) == 0 {
		return nil
	}
	var changed []string
	a.store.Update(func(c *Config) {
		for i := range c.Users {
			st := c.Users[i].Shared
			if st == nil {
				continue
			}
			e, ok := want[st.GID]
			if !ok {
				continue
			}
			m, ok := st.Members[self]
			if !ok {
				continue
			}
			old, _ := json.Marshal(m.Ent)
			now, _ := json.Marshal(&e)
			if string(old) == string(now) {
				continue
			}
			m.Ent = &e
			m.S = a.hlcNow()
			st.Members[self] = m
			changed = append(changed, st.GID)
		}
	})
	for _, gid := range changed {
		a.sharedChanged(gid)
	}
	return changed
}

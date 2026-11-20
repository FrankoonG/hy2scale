package app

// Links: the share links of a user's proxies, on the public host every link
// of this node carries and on its LAN addresses.

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// SubProtocols: the protocols links are made for, in order.
var SubProtocols = []string{"hy2", "ss", "trojan", "socks5", "http"}

// --- the public host ---

// PublicHost: the host links of this node carry: the setting, else the
// detected public IPv4, else IPv6 ("" = none known).
func (a *App) PublicHost() string {
	if h := strings.TrimSpace(a.store.Get().PublicHost); h != "" {
		return h
	}
	return a.DetectedPublicHost()
}

// DetectedPublicHost: the detected public address (IPv4 first).
func (a *App) DetectedPublicHost() string {
	if a.ifaces == nil || a.ifaces.pub == nil {
		return ""
	}
	eg := a.SelfEgress()
	if eg.IPv4 != nil && eg.IPv4.OK && eg.IPv4.IP != "" {
		return eg.IPv4.IP
	}
	if eg.IPv6 != nil && eg.IPv6.OK && eg.IPv6.IP != "" {
		return eg.IPv6.IP
	}
	return ""
}

// virtualIfacePrefixes: interfaces that are not a LAN (containers, tunnels,
// overlays).
var virtualIfacePrefixes = []string{"lo", "docker", "br-", "veth", "virbr", "tun", "tap", "utun", "wg",
	"tailscale", "zt", "cni", "flannel", "cali", "vxlan", "kube", "hy2", "ppp", "ipsec", "gre", "sit", "ip6tnl"}

// VirtualIface: name is a container, tunnel or overlay interface.
func VirtualIface(name string) bool {
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// LANAddrs: the private IPv4 addresses of this host's LAN interfaces (none
// in a container's bridge network: its addresses are not the LAN's).
func LANAddrs() []netip.Addr {
	if !onLAN() {
		return nil
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || VirtualIface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, ad := range addrs {
			pfx, err := netip.ParsePrefix(ad.String())
			if err != nil {
				continue
			}
			ip := pfx.Addr().Unmap()
			if ip.Is4() && ip.IsPrivate() {
				out = append(out, ip)
			}
		}
	}
	return out
}

// --- links ---

// ProxyLink: one share link of a user on one protocol and host.
type ProxyLink struct {
	Proto string `json:"proto"`
	User  string `json:"user"`
	Name  string `json:"name"`
	Host  string `json:"host"`
	LAN   bool   `json:"lan,omitempty"`
	Label string `json:"label,omitempty"` // the row's policy set (policy kind)
	URL   string `json:"url"`
	clash []kv
}

type kv struct {
	k string
	v any
}

// Rename gives the link another name (its fragment and Clash name).
func (l *ProxyLink) Rename(name string) {
	if i := strings.LastIndexByte(l.URL, '#'); i >= 0 {
		l.URL = l.URL[:i]
	}
	l.URL += "#" + uriEscape(name)
	ck := make([]kv, len(l.clash))
	copy(ck, l.clash)
	for i := range ck {
		if ck[i].k == "name" {
			ck[i].v = name
		}
	}
	l.clash, l.Name = ck, name
}

// certTrust: how clients should check a server certificate.
type certTrust struct {
	sni      string // a name the certificate covers
	insecure bool   // not verifiable against the system roots
	pin      string // sha256 of the certificate (when insecure)
	names    []string
}

// sniFor: the name to present when connecting to host.
func (t certTrust) sniFor(host string) string {
	for _, n := range t.names {
		if strings.EqualFold(n, host) || strings.HasPrefix(n, "*.") && strings.HasSuffix(strings.ToLower(host), strings.ToLower(n[1:])) {
			return host
		}
	}
	return t.sni
}

func certTrustOf(path string) certTrust {
	data, err := os.ReadFile(path)
	if err != nil {
		return certTrust{insecure: true}
	}
	var certs []*x509.Certificate
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		if b.Type == "CERTIFICATE" {
			if c, err := x509.ParseCertificate(b.Bytes); err == nil {
				certs = append(certs, c)
			}
		}
	}
	if len(certs) == 0 {
		return certTrust{insecure: true}
	}
	leaf := certs[0]
	t := certTrust{names: leaf.DNSNames}
	if len(leaf.DNSNames) > 0 {
		t.sni = leaf.DNSNames[0]
	} else if leaf.Subject.CommonName != "" && net.ParseIP(leaf.Subject.CommonName) == nil {
		t.sni = leaf.Subject.CommonName
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Intermediates: inter, CurrentTime: time.Now()}); err != nil {
		sum := sha256.Sum256(leaf.Raw)
		t.insecure, t.pin = true, hex.EncodeToString(sum[:])
	}
	return t
}

// Endpoint: a protocol a node serves, as a link needs it.
type Endpoint struct {
	Proto  string `json:"proto"`
	Port   int    `json:"port"`
	Ports  string `json:"ports,omitempty"`  // hy2 port hopping: every port of the listen spec
	Method string `json:"method,omitempty"` // ss
	TLS    bool   `json:"tls,omitempty"`    // https proxy
	// the server certificate (hy2, trojan, https)
	SNI      string   `json:"sni,omitempty"`
	Names    []string `json:"names,omitempty"`
	Insecure bool     `json:"insecure,omitempty"`
	Pin      string   `json:"pin,omitempty"`
}

func (ep *Endpoint) setTrust(t certTrust) {
	ep.SNI, ep.Names, ep.Insecure, ep.Pin = t.sni, t.names, t.insecure, t.pin
}

// sniFor: the name to present when connecting to host.
func (ep Endpoint) sniFor(host string) string {
	return certTrust{sni: ep.SNI, names: ep.Names}.sniFor(host)
}

// Entrance: how clients reach a node — its public host, LAN addresses,
// the protocols it serves and (for one user) the policy set of each row
// there. Shared users' holders publish theirs (shared_users.go).
type Entrance struct {
	Node string     `json:"node"`
	Host string     `json:"host,omitempty"`
	LAN  []string   `json:"lan,omitempty"`
	Eps  []Endpoint `json:"eps,omitempty"`
	// Rows: a policy-kind user's rows usable here → the policy set's name.
	Rows map[string]string `json:"rows,omitempty"`
}

// endpoints: the protocols this node serves now, in SubProtocols order.
func (a *App) endpoints() []Endpoint {
	cfg := a.store.Get()
	by := map[string]Endpoint{}
	listenPort := func(listen string) int {
		_, p, err := net.SplitHostPort(stripAddrBind(listen))
		if err != nil {
			return 0
		}
		n, _ := strconv.Atoi(p)
		return n
	}
	if s := cfg.Server; s != nil && s.Listen != "" {
		if _, ports, err := net.SplitHostPort(stripAddrBind(s.Listen)); err == nil {
			ep := Endpoint{Proto: "hy2"}
			if strings.ContainsAny(ports, ",-") {
				ep.Port, _ = strconv.Atoi(hopPrimaryPort(ports))
				ep.Ports = ports
			} else {
				ep.Port, _ = strconv.Atoi(ports)
			}
			ep.setTrust(certTrustOf(s.TLSCert))
			if ep.Port > 0 {
				by["hy2"] = ep
			}
		}
	}
	if s := cfg.SS; s != nil && s.Enabled {
		m := s.Method
		if m == "" {
			m = "aes-256-gcm"
		}
		if p := listenPort(s.Listen); p > 0 {
			by["ss"] = Endpoint{Proto: "ss", Port: p, Method: m}
		}
	}
	if s := cfg.Trojan; s != nil && s.Enabled {
		id := s.CertID
		if id == "" {
			id = "default"
		}
		if p := listenPort(s.Listen); p > 0 {
			ep := Endpoint{Proto: "trojan", Port: p}
			ep.setTrust(certTrustOf(a.tls.CertPath(id)))
			by["trojan"] = ep
		}
	}
	for _, px := range cfg.Proxies {
		if !px.Enabled || (px.Protocol != "socks5" && px.Protocol != "http") {
			continue
		}
		if _, dup := by[px.Protocol]; dup {
			continue
		}
		p := listenPort(px.Listen)
		if p == 0 {
			continue
		}
		ep := Endpoint{Proto: px.Protocol, Port: p}
		if px.Protocol == "http" && px.TLSCert != "" {
			ep.TLS = true
			ep.setTrust(certTrustOf(a.tls.CertPath(px.TLSCert)))
		}
		by[px.Protocol] = ep
	}
	var out []Endpoint
	for _, p := range SubProtocols {
		if ep, ok := by[p]; ok {
			out = append(out, ep)
		}
	}
	return out
}

// LocalEntrance: this node's entrance for user u (fallback: the host used
// when no public host is known).
func (a *App) LocalEntrance(u *UserConfig, fallback string) Entrance {
	e := Entrance{Node: a.node.Name(), Host: a.PublicHost(), Eps: a.endpoints()}
	if e.Host == "" {
		e.Host = fallback
	}
	for _, ip := range LANAddrs() {
		if s := ip.String(); s != e.Host {
			e.LAN = append(e.LAN, s)
		}
	}
	if u != nil && u.Kind() == KindPolicy {
		e.Rows = map[string]string{}
		pol := a.store.Get().Policy
		for _, r := range u.Rows {
			id := u.RowPolicy[r.ID]
			if id == "" || pol == nil {
				continue
			}
			if ps := pol.PolicySetByID(id); ps != nil {
				name := ps.Name
				if name == "" {
					name = id
				}
				e.Rows[r.ID] = name
			}
		}
	}
	return e
}

// uriEscape escapes all but the unreserved characters (RFC 3986).
func uriEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hostPort(host string, port int) string {
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

// link builds the link of user (password pw) on endpoint ep at host.
func link(ep Endpoint, host, user, pw, name string) (string, []kv) {
	hp := hostPort(host, ep.Port)
	frag := "#" + uriEscape(name)
	server := strings.Trim(host, "[]")
	switch ep.Proto {
	case "hy2":
		var q []string
		ck := []kv{{"name", name}, {"type", "hysteria2"}, {"server", server}, {"port", ep.Port}, {"password", pw}}
		if ep.Ports != "" {
			q = append(q, "mport="+uriEscape(ep.Ports))
			ck = append(ck, kv{"ports", ep.Ports})
		}
		if sni := ep.sniFor(server); sni != "" {
			q = append(q, "sni="+uriEscape(sni))
			ck = append(ck, kv{"sni", sni})
		}
		if ep.Insecure {
			q = append(q, "insecure=1")
			ck = append(ck, kv{"skip-cert-verify", true})
			if ep.Pin != "" {
				q = append(q, "pinSHA256="+ep.Pin)
				ck = append(ck, kv{"fingerprint", ep.Pin})
			}
		}
		u := "hysteria2://" + uriEscape(pw) + "@" + hp + "/"
		if len(q) > 0 {
			u += "?" + strings.Join(q, "&")
		}
		return u + frag, ck
	case "ss":
		ui := base64.RawURLEncoding.EncodeToString([]byte(ep.Method + ":" + pw))
		return "ss://" + ui + "@" + hp + frag,
			[]kv{{"name", name}, {"type", "ss"}, {"server", server}, {"port", ep.Port}, {"cipher", ep.Method}, {"password", pw}, {"udp", true}}
	case "trojan":
		q := []string{"security=tls"}
		ck := []kv{{"name", name}, {"type", "trojan"}, {"server", server}, {"port", ep.Port}, {"password", pw}, {"udp", true}}
		if sni := ep.sniFor(server); sni != "" {
			q = append(q, "sni="+uriEscape(sni))
			ck = append(ck, kv{"sni", sni})
		}
		if ep.Insecure {
			q = append(q, "allowInsecure=1")
			ck = append(ck, kv{"skip-cert-verify", true})
		}
		return "trojan://" + uriEscape(pw) + "@" + hp + "?" + strings.Join(q, "&") + frag, ck
	case "socks5":
		return "socks5://" + uriEscape(user) + ":" + uriEscape(pw) + "@" + hp + frag,
			[]kv{{"name", name}, {"type", "socks5"}, {"server", server}, {"port", ep.Port}, {"username", user}, {"password", pw}, {"udp", true}}
	case "http":
		ck := []kv{{"name", name}, {"type", "http"}, {"server", server}, {"port", ep.Port}, {"username", user}, {"password", pw}}
		scheme := "http"
		var q []string
		if ep.TLS {
			scheme = "https"
			ck = append(ck, kv{"tls", true})
			if sni := ep.sniFor(server); sni != "" {
				q = append(q, "sni="+uriEscape(sni))
				ck = append(ck, kv{"sni", sni})
			}
			if ep.Insecure {
				q = append(q, "skip-cert-verify=true")
				ck = append(ck, kv{"skip-cert-verify", true})
			}
		}
		u := scheme + "://" + uriEscape(user) + ":" + uriEscape(pw) + "@" + hp
		if len(q) > 0 {
			u += "/?" + strings.Join(q, "&")
		}
		return u + frag, ck
	}
	return "", nil
}

// LinkOptions: what EntranceLinks builds.
type LinkOptions struct {
	Protocols []string // nil = every protocol served
	LAN       bool     // also the LAN addresses
	NodeName  bool     // names carry the node's name (a subscription)
	// NameFormat: the name template (subscription.go); "" = the user, the
	// row's set, the protocol and the LAN mark.
	NameFormat string
	Fallback  string   // the host used when no public host is known ("" = none)
}

// EntranceLinks: the links of user u at entrance e: each of its passwords
// usable there (a policy-kind user's rows set there, else the main
// password) on each protocol, public host first, then the LAN addresses.
// Names: "<user> [<node>] [<policy set>] <proto> [LAN <ip>]".
func EntranceLinks(e Entrance, u *UserConfig, opt LinkOptions) []ProxyLink {
	protos := opt.Protocols
	if protos == nil {
		protos = SubProtocols
	}
	type cred struct{ pw, label string }
	var creds []cred
	if u.Kind() == KindPolicy {
		seen := map[string]int{}
		for _, r := range u.Rows {
			if name, ok := e.Rows[r.ID]; ok {
				seen[name]++
			}
		}
		for i, r := range u.Rows {
			if name, ok := e.Rows[r.ID]; ok && r.Password != "" {
				if seen[name] > 1 { // rows going to the same set: told apart by number
					name += fmt.Sprintf(" #%d", i+1)
				}
				creds = append(creds, cred{r.Password, name})
			}
		}
	} else if u.Password != "" {
		creds = append(creds, cred{pw: u.Password})
	}
	type hostE struct{ host, lan string }
	var hosts []hostE
	if e.Host != "" {
		hosts = append(hosts, hostE{host: e.Host})
	}
	if opt.LAN {
		for _, ip := range e.LAN {
			label := "LAN"
			if len(e.LAN) > 1 {
				label = "LAN " + ip
			}
			hosts = append(hosts, hostE{host: ip, lan: label})
		}
	}
	var out []ProxyLink
	for _, c := range creds {
		for _, h := range hosts {
			for _, p := range protos {
				var ep *Endpoint
				for i := range e.Eps {
					if e.Eps[i].Proto == p {
						ep = &e.Eps[i]
					}
				}
				if ep == nil || !u.Serves(p) {
					continue
				}
				node := ""
				if opt.NodeName {
					node = e.Node
				}
				tmpl := opt.NameFormat
				if tmpl == "" {
					tmpl = "{user} {policy} {proto} {lan}"
				}
				name := FormatLinkName(tmpl, map[string]string{"user": u.Username, "node": node,
					"policy": c.label, "proto": p, "lan": h.lan, "host": h.host})
				url, ck := link(*ep, h.host, u.Username, c.pw, name)
				out = append(out, ProxyLink{Proto: p, User: u.Username, Name: name, Host: h.host, LAN: h.lan != "", Label: c.label, URL: url, clash: ck})
			}
		}
	}
	return out
}

// UserLinks: the links of user u on this node.
func (a *App) UserLinks(u *UserConfig, opt LinkOptions) []ProxyLink {
	return EntranceLinks(a.LocalEntrance(u, opt.Fallback), u, opt)
}

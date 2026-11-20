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
	URL   string `json:"url"`
	clash []kv
}

type kv struct {
	k string
	v any
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

// subEndpoint: a protocol this node serves, as a link needs it.
type subEndpoint struct {
	proto  string
	port   int
	ports  string // hy2 port hopping: every port of the listen spec
	method string // ss
	tls    *certTrust
}

// subEndpoints: the protocols this node serves now, by key.
func (a *App) subEndpoints() map[string]subEndpoint {
	cfg := a.store.Get()
	out := map[string]subEndpoint{}
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
			ep := subEndpoint{proto: "hy2"}
			if strings.ContainsAny(ports, ",-") {
				ep.port, _ = strconv.Atoi(hopPrimaryPort(ports))
				ep.ports = ports
			} else {
				ep.port, _ = strconv.Atoi(ports)
			}
			t := certTrustOf(s.TLSCert)
			ep.tls = &t
			if ep.port > 0 {
				out["hy2"] = ep
			}
		}
	}
	if s := cfg.SS; s != nil && s.Enabled {
		m := s.Method
		if m == "" {
			m = "aes-256-gcm"
		}
		if p := listenPort(s.Listen); p > 0 {
			out["ss"] = subEndpoint{proto: "ss", port: p, method: m}
		}
	}
	if s := cfg.Trojan; s != nil && s.Enabled {
		id := s.CertID
		if id == "" {
			id = "default"
		}
		if p := listenPort(s.Listen); p > 0 {
			t := certTrustOf(a.tls.CertPath(id))
			out["trojan"] = subEndpoint{proto: "trojan", port: p, tls: &t}
		}
	}
	for _, px := range cfg.Proxies {
		if !px.Enabled || (px.Protocol != "socks5" && px.Protocol != "http") {
			continue
		}
		if _, dup := out[px.Protocol]; dup {
			continue
		}
		p := listenPort(px.Listen)
		if p == 0 {
			continue
		}
		ep := subEndpoint{proto: px.Protocol, port: p}
		if px.Protocol == "http" && px.TLSCert != "" {
			t := certTrustOf(a.tls.CertPath(px.TLSCert))
			ep.tls = &t
		}
		out[px.Protocol] = ep
	}
	return out
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

// link builds the link of user u on endpoint ep at host.
func link(ep subEndpoint, host string, u *UserConfig, name string) (string, []kv) {
	user := u.Username
	pw := u.EffectivePassword(ep.proto)
	hp := hostPort(host, ep.port)
	frag := "#" + uriEscape(name)
	server := strings.Trim(host, "[]")
	switch ep.proto {
	case "hy2":
		var q []string
		ck := []kv{{"name", name}, {"type", "hysteria2"}, {"server", server}, {"port", ep.port}, {"password", pw}}
		if ep.ports != "" {
			q = append(q, "mport="+uriEscape(ep.ports))
			ck = append(ck, kv{"ports", ep.ports})
		}
		if t := ep.tls; t != nil {
			if sni := t.sniFor(server); sni != "" {
				q = append(q, "sni="+uriEscape(sni))
				ck = append(ck, kv{"sni", sni})
			}
			if t.insecure {
				q = append(q, "insecure=1")
				ck = append(ck, kv{"skip-cert-verify", true})
				if t.pin != "" {
					q = append(q, "pinSHA256="+t.pin)
					ck = append(ck, kv{"fingerprint", t.pin})
				}
			}
		}
		u := "hysteria2://" + uriEscape(pw) + "@" + hp + "/"
		if len(q) > 0 {
			u += "?" + strings.Join(q, "&")
		}
		return u + frag, ck
	case "ss":
		ui := base64.RawURLEncoding.EncodeToString([]byte(ep.method + ":" + pw))
		return "ss://" + ui + "@" + hp + frag,
			[]kv{{"name", name}, {"type", "ss"}, {"server", server}, {"port", ep.port}, {"cipher", ep.method}, {"password", pw}, {"udp", true}}
	case "trojan":
		q := []string{"security=tls"}
		ck := []kv{{"name", name}, {"type", "trojan"}, {"server", server}, {"port", ep.port}, {"password", pw}, {"udp", true}}
		if t := ep.tls; t != nil {
			if sni := t.sniFor(server); sni != "" {
				q = append(q, "sni="+uriEscape(sni))
				ck = append(ck, kv{"sni", sni})
			}
			if t.insecure {
				q = append(q, "allowInsecure=1")
				ck = append(ck, kv{"skip-cert-verify", true})
			}
		}
		return "trojan://" + uriEscape(pw) + "@" + hp + "?" + strings.Join(q, "&") + frag, ck
	case "socks5":
		return "socks5://" + uriEscape(user) + ":" + uriEscape(pw) + "@" + hp + frag,
			[]kv{{"name", name}, {"type", "socks5"}, {"server", server}, {"port", ep.port}, {"username", user}, {"password", pw}, {"udp", true}}
	case "http":
		ck := []kv{{"name", name}, {"type", "http"}, {"server", server}, {"port", ep.port}, {"username", user}, {"password", pw}}
		scheme := "http"
		var q []string
		if t := ep.tls; t != nil {
			scheme = "https"
			ck = append(ck, kv{"tls", true})
			if sni := t.sniFor(server); sni != "" {
				q = append(q, "sni="+uriEscape(sni))
				ck = append(ck, kv{"sni", sni})
			}
			if t.insecure {
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

// LinkOptions: what UserLinks builds.
type LinkOptions struct {
	Protocols []string // nil = every protocol served
	LAN       bool     // also the LAN addresses
	NodeName  bool     // names start with the node's name
	Fallback  string   // the host used when no public host is known ("" = none)
}

// UserLinks: the links of user u, public host first, then the LAN
// addresses (each protocol in SubProtocols order).
func (a *App) UserLinks(u *UserConfig, opt LinkOptions) []ProxyLink {
	eps := a.subEndpoints()
	protos := opt.Protocols
	if protos == nil {
		protos = SubProtocols
	}
	type hostE struct {
		host string
		lan  string // "" public; else the LAN label
	}
	var hosts []hostE
	pub := a.PublicHost()
	if pub == "" {
		pub = opt.Fallback
	}
	if pub != "" {
		hosts = append(hosts, hostE{host: pub})
	}
	if opt.LAN {
		lans := LANAddrs()
		for _, ip := range lans {
			if ip.String() == pub {
				continue
			}
			label := "LAN"
			if len(lans) > 1 {
				label = "LAN " + ip.String()
			}
			hosts = append(hosts, hostE{host: ip.String(), lan: label})
		}
	}
	prefix := u.Username
	if opt.NodeName {
		prefix = a.node.Name() + " " + u.Username
	}
	var out []ProxyLink
	for _, h := range hosts {
		for _, p := range protos {
			ep, ok := eps[p]
			if !ok || !u.IsProxyEnabled(p) {
				continue
			}
			name := prefix + " " + p
			if h.lan != "" {
				name += " " + h.lan
			}
			url, ck := link(ep, h.host, u, name)
			out = append(out, ProxyLink{Proto: p, User: u.Username, Name: name, Host: h.host, LAN: h.lan != "", URL: url, clash: ck})
		}
	}
	return out
}


package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/core/sockmark"
	"github.com/FrankoonG/hy2scale/internal/vpnmux"
	"github.com/xen0bit/veepin/dataplane"
	"github.com/xen0bit/veepin/ikev2"
	"github.com/xen0bit/veepin/l2tp"
	"github.com/xen0bit/veepin/openvpn"
)

// IKEv2, L2TP/IPsec and OpenVPN servers, all userspace (veepin, vendored in
// third_party/veepin): no kernel IPsec, PPP, TUN or iptables, and no
// privileges beyond binding their UDP ports. IKEv2 and L2TP share UDP
// 500/4500 through vpnmux.

// IKEv2Config holds the IKEv2 server configuration.
type IKEv2Config struct {
	Enabled           bool     `yaml:"enabled" json:"enabled"`
	Mode              string   `yaml:"mode" json:"mode"`                                                 // "mschapv2" or "psk"
	Pool              string   `yaml:"pool" json:"pool"`                                                 // e.g. "10.10.10.1/24"
	Pool6             string   `yaml:"pool6,omitempty" json:"pool6"`                                     // IPv6 pool, e.g. "fd00:10:10::1/64"; "" = default
	CertID            string   `yaml:"cert_id" json:"cert_id"`                                           // CA cert ID (mschapv2): the server cert is signed from it
	PSK               string   `yaml:"psk" json:"psk"`                                                   // pre-shared key (psk mode)
	LocalID           string   `yaml:"local_id" json:"local_id"`                                         // server identity, default = node ID
	RemoteID          string   `yaml:"remote_id" json:"remote_id"`                                       // unused since 1.4.0
	PSKUserMode       bool     `yaml:"psk_user_mode" json:"psk_user_mode"`                               // PSK: the IKE identity names the user
	DefaultExit       string   `yaml:"default_exit" json:"default_exit"`                                 // exit_via when user mode off
	DefaultExitPaths  []string `yaml:"default_exit_paths,omitempty" json:"default_exit_paths,omitempty"` // quality / aggregate candidates
	DefaultExitMode   string   `yaml:"default_exit_mode,omitempty" json:"default_exit_mode,omitempty"`   // ""|"quality"|"aggregate"
	DefaultExitPolicy string   `yaml:"default_exit_policy,omitempty" json:"default_exit_policy"`         // a policy set id (overrides default_exit*)
	DNS               string   `yaml:"dns" json:"dns"`                                                   // unused: the node DNS setting is pushed
	ProxyPort         int      `yaml:"proxy_port" json:"proxy_port"`                                     // unused since 1.4.0
	MTU               int      `yaml:"mtu" json:"mtu"`                                                   // tunnel MTU, default 1400
}

// L2TPConfig holds the L2TP/IPsec server configuration.
type L2TPConfig struct {
	Listen    string `yaml:"listen" json:"listen"` // unused since 1.4.0 (L2TP rides inside IPsec)
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Pool      string `yaml:"pool" json:"pool"`             // e.g. "192.168.25.1/24"
	PSK       string `yaml:"psk" json:"psk"`               // IPsec pre-shared key
	ProxyPort int    `yaml:"proxy_port" json:"proxy_port"` // unused since 1.4.0
	MTU       int    `yaml:"mtu" json:"mtu"`               // PPP MTU, default 1280
}

// OpenVPNConfig holds the OpenVPN server configuration.
type OpenVPNConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Port    int    `yaml:"port" json:"port"`             // UDP, default 1194
	Pool    string `yaml:"pool" json:"pool"`             // e.g. "10.8.0.1/24"
	Pool6   string `yaml:"pool6,omitempty" json:"pool6"` // IPv6 prefix, e.g. "fd00:8::1/64"; "" = default
	CertID  string `yaml:"cert_id" json:"cert_id"`       // CA cert ID: the server cert is signed from it
	MTU     int    `yaml:"mtu" json:"mtu"`               // tunnel MTU, default 1400

	ListenIP string   `yaml:"listen_ip,omitempty" json:"listen_ip"` // bind address; "" = all
	DNS      []string `yaml:"dns,omitempty" json:"dns"`             // pushed DNS servers; empty = the node's VPN DNS
	// Control channel protection: "" (none), "tls-crypt" or "tls-auth", with
	// an OpenVPN static key (generated when empty) that profiles carry.
	Control   string `yaml:"control,omitempty" json:"control"`
	StaticKey string `yaml:"static_key,omitempty" json:"-"`
	Auth      string `yaml:"auth,omitempty" json:"auth"` // tls-auth HMAC digest: SHA1 (default) | SHA256
	// Shape pads downstream packets so inner handshakes keep no size pattern.
	Shape bool `yaml:"shape,omitempty" json:"shape"`

	// Client profile.
	FullTunnel   *bool    `yaml:"full_tunnel,omitempty" json:"full_tunnel"`     // all traffic into the tunnel (default true)
	TunnelIPv6   *bool    `yaml:"tunnel_ipv6,omitempty" json:"tunnel_ipv6"`     // IPv6 into the tunnel too (default true)
	Routes       []string `yaml:"routes,omitempty" json:"routes"`               // split tunnel: networks through the tunnel
	ProfileExtra string   `yaml:"profile_extra,omitempty" json:"profile_extra"` // lines appended to profiles

	// RotateKey (API only) replaces the static key.
	RotateKey bool `yaml:"-" json:"rotate_key,omitempty"`
}

// OpenVPN control channel modes.
const (
	OpenVPNTLSCrypt = "tls-crypt"
	OpenVPNTLSAuth  = "tls-auth"
)

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// NewOpenVPNStaticKey is a fresh OpenVPN static key ("OpenVPN Static key V1":
// 256 random bytes as 16 lines of 32 hex digits).
func NewOpenVPNStaticKey() string {
	var raw [256]byte
	rand.Read(raw[:])
	h := hex.EncodeToString(raw[:])
	var b strings.Builder
	b.WriteString("-----BEGIN OpenVPN Static key V1-----\n")
	for i := 0; i < len(h); i += 32 {
		b.WriteString(h[i : i+32])
		b.WriteByte('\n')
	}
	b.WriteString("-----END OpenVPN Static key V1-----\n")
	return b.String()
}

// Check validates the settings an operator enters (pools are checked when
// the server starts).
func (c *OpenVPNConfig) Check() error {
	if c.Port < 0 || c.Port > 65535 {
		return errors.New("invalid port")
	}
	if c.ListenIP != "" {
		if _, err := netip.ParseAddr(c.ListenIP); err != nil {
			return fmt.Errorf("invalid listen address %q", c.ListenIP)
		}
	}
	for _, d := range c.DNS {
		if _, err := netip.ParseAddr(strings.TrimSpace(d)); err != nil {
			return fmt.Errorf("invalid DNS server %q", d)
		}
	}
	switch c.Control {
	case "", OpenVPNTLSCrypt, OpenVPNTLSAuth:
	default:
		return fmt.Errorf("unknown control channel protection %q", c.Control)
	}
	switch strings.ToUpper(c.Auth) {
	case "", "SHA1", "SHA256":
	default:
		return fmt.Errorf("unsupported auth digest %q (SHA1, SHA256)", c.Auth)
	}
	for _, r := range c.Routes {
		if _, err := netip.ParsePrefix(strings.TrimSpace(r)); err != nil {
			return fmt.Errorf("invalid route %q", r)
		}
	}
	if strings.ContainsAny(c.ProfileExtra, "<>") {
		return errors.New("extra profile lines cannot contain inline blocks")
	}
	return nil
}

// Default IPv6 pools. Every client gets an IPv4 and an IPv6 address; which
// destinations it reaches depends on what its exit can reach.
const (
	DefaultIKEv2Pool6   = "fd00:10:10::1/64"
	DefaultOpenVPNPool6 = "fd00:8::1/64"
)

const (
	ikePort  = 500
	nattPort = 4500
)

type vpnServer interface {
	ListenAndServe() error
	Close() error
}

type vpnInstance struct {
	net *vpnNet
	srv vpnServer
}

// vpnMuxFor returns the shared IKE/NAT-T sockets. Caller holds a.vpnMu.
func (a *App) vpnMuxFor() *vpnmux.Mux {
	if a.vpnMux == nil {
		a.vpnMux = vpnmux.New(ikePort, nattPort)
	}
	return a.vpnMux
}

// startVPN replaces the running server for proto. build constructs the
// server over the stack it is given; it must not start serving.
func (a *App) startVPN(proto string, gateways []netip.Addr, mtu int,
	resolve func(n *vpnNet, srcIP string) (vpnRoute, bool),
	build func(n *vpnNet) (vpnServer, error)) error {
	a.vpnMu.Lock()
	defer a.vpnMu.Unlock()
	a.stopVPNLocked(proto)

	n, err := newVPNNet(a.appCtx, proto, gateways, mtu)
	if err != nil {
		return vpnErr(proto, err)
	}
	a.installVPNForwarders(n, func(ip string) (vpnRoute, bool) { return resolve(n, ip) })
	srv, err := build(n)
	if err != nil {
		n.close()
		return vpnErr(proto, err)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		// Returned at once: it never served (bind failure and the like).
		srv.Close()
		n.close()
		if err == nil {
			err = errors.New("server exited")
		}
		return vpnErr(proto, err)
	case <-time.After(300 * time.Millisecond):
	}
	if a.vpn == nil {
		a.vpn = map[string]*vpnInstance{}
	}
	a.vpn[proto] = &vpnInstance{net: n, srv: srv}
	return nil
}

func (a *App) stopVPNLocked(proto string) {
	inst := a.vpn[proto]
	if inst == nil {
		return
	}
	delete(a.vpn, proto)
	inst.srv.Close()
	inst.net.close()
	log.Printf("[%s] stopped", proto)
}

func (a *App) stopVPN(proto string) {
	a.vpnMu.Lock()
	a.stopVPNLocked(proto)
	a.vpnMu.Unlock()
}

// VPNClientCount is the number of authenticated clients of proto.
func (a *App) VPNClientCount(proto string) int {
	a.vpnMu.Lock()
	defer a.vpnMu.Unlock()
	if inst := a.vpn[proto]; inst != nil {
		return inst.net.clientCount()
	}
	return 0
}

// VPNRunning reports whether proto's server is up.
func (a *App) VPNRunning(proto string) bool {
	a.vpnMu.Lock()
	defer a.vpnMu.Unlock()
	return a.vpn[proto] != nil
}

// vpnUser is the active user name may log in as on proto, or nil.
func (a *App) vpnUser(name, proto string) *UserConfig {
	a.usersMu.RLock()
	u, ok := a.userIndex[name]
	a.usersMu.RUnlock()
	if !ok || !u.Enabled || !u.IsProxyEnabled(proto) {
		return nil
	}
	if u.ExpiryDate != "" {
		if t, err := time.Parse("2006-01-02", u.ExpiryDate); err == nil && time.Now().After(t) {
			return nil
		}
	}
	if u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit {
		return nil
	}
	return u
}

// vpnPassword serves the per-attempt credential lookups (MS-CHAPv2 needs the
// plaintext password).
func (a *App) vpnPassword(proto string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if u := a.vpnUser(name, proto); u != nil {
			return u.Password, true
		}
		return "", false
	}
}

// userRoute is the route of an authenticated user: their own exit, or direct
// when the account is gone.
func (a *App) userRoute(name, proto string) vpnRoute {
	r := vpnRoute{user: name}
	if u := a.vpnUser(name, proto); u != nil {
		r.userID, r.policy = u.ID, u.ExitPolicy
		r.exitVia, r.exitPaths, r.exitMode = u.ExitVia, u.ExitPaths, u.ExitMode
	}
	return r
}

// vpnPool turns the configured "gateway/prefix" form into the network CIDR
// the servers take (they hand out the network's first host as gateway).
func vpnPool(pool, def string) (cidr string, gateway netip.Addr, err error) {
	if pool == "" {
		pool = def
	}
	p, err := netip.ParsePrefix(pool)
	if err != nil {
		return "", netip.Addr{}, err
	}
	if !p.Addr().Is4() || p.Bits() > 30 {
		return "", netip.Addr{}, fmt.Errorf("pool %q: need an IPv4 prefix of /30 or larger", pool)
	}
	p = p.Masked()
	return p.String(), p.Addr().Next(), nil
}

// vpnPool6 is vpnPool for the IPv6 pool: "gateway/prefix" in, the network
// CIDR and its first host (the server's tunnel address) out.
func vpnPool6(pool, def string) (cidr string, gateway netip.Addr, err error) {
	pool = strings.TrimSpace(pool)
	if pool == "" {
		pool = def
	}
	p, err := netip.ParsePrefix(pool)
	if err != nil {
		return "", netip.Addr{}, err
	}
	if !p.Addr().Is6() || p.Addr().Is4In6() || p.Bits() > 120 {
		return "", netip.Addr{}, fmt.Errorf("pool6 %q: need an IPv6 prefix of /120 or larger", pool)
	}
	p = p.Masked()
	return p.String(), p.Addr().Next(), nil
}

// vpnDNS is the node DNS setting as the address list pushed to clients.
func (a *App) vpnDNS() []net.IP {
	dns := a.store.Get().DNS
	if dns == "" {
		dns = "8.8.8.8,1.1.1.1"
	}
	var out []net.IP
	for _, f := range strings.FieldsFunc(dns, func(r rune) bool { return r == ',' || r == ' ' }) {
		if h, _, err := net.SplitHostPort(f); err == nil {
			f = h
		}
		if ip := net.ParseIP(f); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

// vpnServerCert signs (on every start) a server certificate for cn from the
// CA certID and writes cert + key under the data dir.
func (a *App) vpnServerCert(proto, certID, cn string) (certFile, keyFile string, err error) {
	ca, err := a.tls.Get(certID)
	if err != nil {
		return "", "", fmt.Errorf("CA cert %q not found", certID)
	}
	if !ca.IsCA {
		return "", "", fmt.Errorf("cert %q is not a CA certificate", certID)
	}
	tmp := "__" + proto + "_auto__"
	if err := a.tls.SignWithCA(certID, tmp, proto+" auto", cn, 3650); err != nil {
		return "", "", fmt.Errorf("sign server cert: %w", err)
	}
	defer a.tls.Delete(tmp)
	certPEM, err := os.ReadFile(a.tls.CertPath(tmp))
	if err != nil {
		return "", "", err
	}
	keyPEM, err := os.ReadFile(a.tls.KeyPath(tmp))
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(a.dataDir, "vpn")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	certFile, keyFile = filepath.Join(dir, proto+"-server.crt"), filepath.Join(dir, proto+"-server.key")
	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		return "", "", err
	}
	return certFile, keyFile, nil
}

// vpnLogger routes a server's log to the process log: warnings always,
// progress only in debug mode.
func vpnLogger(proto string) *slog.Logger {
	level := slog.LevelWarn
	if debugMode() {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(vpnLogWriter{"[" + proto + "] "}, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

type vpnLogWriter struct{ prefix string }

func (w vpnLogWriter) Write(p []byte) (int, error) {
	log.Print(w.prefix + strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// ---- IKEv2 ----

func (c *IKEv2Config) mtuVal() int {
	if c.MTU <= 0 {
		return 1400
	}
	return c.MTU
}

// StartIKEv2 starts the IKEv2 server.
func (a *App) StartIKEv2(cfg IKEv2Config) error {
	if !cfg.Enabled {
		a.stopVPN("ikev2")
		return nil
	}
	cidr, gw, err := vpnPool(cfg.Pool, "10.10.10.1/24")
	if err != nil {
		return vpnErr("ikev2", err)
	}
	cidr6, gw6, err := vpnPool6(cfg.Pool6, DefaultIKEv2Pool6)
	if err != nil {
		return vpnErr("ikev2", err)
	}
	localID := cfg.LocalID
	if localID == "" {
		localID = a.store.Get().NodeID
	}
	sc := ikev2.ServerConfig{
		LocalID: localID,
		Pool:    cidr,
		Pool6:   cidr6,
		DNS:     a.vpnDNS(),
		Logger:  vpnLogger("ikev2"),
	}
	switch cfg.Mode {
	case "psk":
		if cfg.PSK == "" {
			return vpnErr("ikev2", errors.New("psk mode needs a PSK"))
		}
		sc.PSK = cfg.PSK
	case "mschapv2":
		certFile, keyFile, err := a.vpnServerCert("ikev2", cfg.CertID, localID)
		if err != nil {
			return vpnErr("ikev2", err)
		}
		sc.CertFile, sc.KeyFile = certFile, keyFile
		sc.EAPLookup = a.vpnPassword("ikev2")
	default:
		return vpnErr("ikev2", fmt.Errorf("unknown mode %q", cfg.Mode))
	}
	if cfg.RemoteID != "" && cfg.RemoteID != "%any" {
		log.Printf("[ikev2] remote_id %q is ignored: any client identity is accepted", cfg.RemoteID)
	}
	resolve := func(n *vpnNet, ip string) (vpnRoute, bool) {
		id, ok := n.clientUser(ip)
		if !ok {
			return vpnRoute{}, false
		}
		c := a.store.Get().IKEv2
		if c != nil && c.Mode == "psk" && !c.PSKUserMode {
			return vpnRoute{user: "__psk__", policy: c.DefaultExitPolicy, exitVia: c.DefaultExit, exitPaths: c.DefaultExitPaths, exitMode: c.DefaultExitMode}, true
		}
		return a.userRoute(id, "ikev2"), true
	}
	err = a.startVPN("ikev2", []netip.Addr{gw, gw6}, cfg.mtuVal(), resolve, func(n *vpnNet) (vpnServer, error) {
		sc.Device = n.device()
		sc.ListenPacket = a.vpnMuxFor().Listen(vpnmux.IKEv2)
		sc.OnSession = n.session
		return ikev2.NewServer(sc)
	})
	if err == nil {
		log.Printf("[ikev2] userspace server on udp/%d+%d (%s, pool %s %s, id %s)", ikePort, nattPort, cfg.Mode, cidr, cidr6, localID)
	}
	return err
}

// StopIKEv2 stops the IKEv2 server.
func (a *App) StopIKEv2() { a.stopVPN("ikev2") }

// RestartIKEv2 applies the current IKEv2 config.
func (a *App) RestartIKEv2() error {
	cfg := a.store.Get().IKEv2
	if cfg == nil || !cfg.Enabled {
		a.StopIKEv2()
		return nil
	}
	return a.StartIKEv2(*cfg)
}

// ---- L2TP/IPsec ----

func (c *L2TPConfig) mtuVal() int {
	if c.MTU <= 0 {
		return 1280
	}
	return c.MTU
}

// StartL2TP starts the L2TP/IPsec server.
func (a *App) StartL2TP(cfg L2TPConfig) error {
	if !cfg.Enabled {
		a.stopVPN("l2tp")
		return nil
	}
	if cfg.PSK == "" {
		return vpnErr("l2tp", errors.New("a PSK is required"))
	}
	cidr, gw, err := vpnPool(cfg.Pool, "192.168.25.1/24")
	if err != nil {
		return vpnErr("l2tp", err)
	}
	resolve := func(n *vpnNet, ip string) (vpnRoute, bool) {
		user, ok := n.clientUser(ip)
		if !ok {
			return vpnRoute{}, false
		}
		return a.userRoute(user, "l2tp"), true
	}
	err = a.startVPN("l2tp", []netip.Addr{gw}, cfg.mtuVal(), resolve, func(n *vpnNet) (vpnServer, error) {
		return l2tp.NewServer(l2tp.ServerConfig{
			Port:         ikePort,
			PSK:          cfg.PSK,
			UserLookup:   a.vpnPassword("l2tp"),
			Pool:         cidr,
			DNS:          a.vpnDNS(),
			Device:       n.device(),
			ListenPacket: a.vpnMuxFor().Listen(vpnmux.IKEv1),
			OnSession:    n.session,
			Logger:       vpnLogger("l2tp"),
		})
	})
	if err == nil {
		log.Printf("[l2tp] userspace L2TP/IPsec server on udp/%d+%d (pool %s)", ikePort, nattPort, cidr)
	}
	return err
}

// StopL2TP stops the L2TP/IPsec server.
func (a *App) StopL2TP() { a.stopVPN("l2tp") }

// RestartL2TP applies the current L2TP config.
func (a *App) RestartL2TP() error {
	cfg := a.store.Get().L2TP
	if cfg == nil || !cfg.Enabled {
		a.StopL2TP()
		return nil
	}
	return a.StartL2TP(*cfg)
}

// ---- OpenVPN ----

func (c *OpenVPNConfig) port() int {
	if c.Port <= 0 {
		return 1194
	}
	return c.Port
}

func (c *OpenVPNConfig) mtuVal() int {
	if c.MTU <= 0 {
		return 1400
	}
	return c.MTU
}

// StartOpenVPN starts the OpenVPN server (UDP, user/password auth).
func (a *App) StartOpenVPN(cfg OpenVPNConfig) error {
	if !cfg.Enabled {
		a.stopVPN("openvpn")
		return nil
	}
	cidr, gw, err := vpnPool(cfg.Pool, "10.8.0.1/24")
	if err != nil {
		return vpnErr("openvpn", err)
	}
	cidr6, _, err := vpnPool6(cfg.Pool6, DefaultOpenVPNPool6)
	if err != nil {
		return vpnErr("openvpn", err)
	}
	// A client's v6 address is derived from its v4 one (10.8.0.2 → <pool6>::2),
	// and so is the server's.
	gw6, err := openVPNAddr6(cidr, cidr6, gw)
	if err != nil {
		return vpnErr("openvpn", err)
	}
	certFile, keyFile, err := a.vpnServerCert("openvpn", cfg.CertID, a.store.Get().NodeID)
	if err != nil {
		return vpnErr("openvpn", err)
	}
	caPEM, err := os.ReadFile(a.tls.CertPath(cfg.CertID))
	if err != nil {
		return vpnErr("openvpn", err)
	}
	certPEM, _ := os.ReadFile(certFile)
	keyPEM, _ := os.ReadFile(keyFile)
	check := a.vpnPassword("openvpn")
	resolve := func(n *vpnNet, ip string) (vpnRoute, bool) {
		user, ok := n.clientUser(ip)
		if !ok {
			return vpnRoute{}, false
		}
		return a.userRoute(user, "openvpn"), true
	}
	dns := a.vpnDNS()
	if len(cfg.DNS) > 0 {
		dns = nil
		for _, d := range cfg.DNS {
			if ip := net.ParseIP(strings.TrimSpace(d)); ip != nil {
				dns = append(dns, ip)
			}
		}
	}
	var tlsCrypt, tlsAuth []byte
	switch cfg.Control {
	case OpenVPNTLSCrypt:
		tlsCrypt = []byte(cfg.StaticKey)
	case OpenVPNTLSAuth:
		tlsAuth = []byte(cfg.StaticKey)
	}
	shape := 0
	if cfg.Shape {
		shape = dataplane.DefaultShapeBytes
	}
	bindIP := net.ParseIP(cfg.ListenIP)
	err = a.startVPN("openvpn", []netip.Addr{gw, gw6}, cfg.mtuVal(), resolve, func(n *vpnNet) (vpnServer, error) {
		return openvpn.NewServer(openvpn.ServerConfig{
			CA:         caPEM,
			Cert:       certPEM,
			Key:        keyPEM,
			ListenPort: cfg.port(),
			TLSCrypt:   tlsCrypt,
			TLSAuth:    tlsAuth,
			Auth:       cfg.Auth,
			// profiles use key-direction 1
			KeyDirection: 1,
			Shape:        shape,
			ListenPacket: func(port int) (*dataplane.PacketConn, error) {
				c, err := sockmark.ListenUDP("udp", &net.UDPAddr{IP: bindIP, Port: port})
				if err != nil {
					return nil, err
				}
				return dataplane.NewPacketConn(c), nil
			},
			Pool:   cidr,
			Pool6:  cidr6,
			DNS:    dns,
			MTU:    cfg.mtuVal(),
			Device: n.device(),
			VerifyUser: func(user, pass string) bool {
				pw, ok := check(user)
				return ok && pw == pass
			},
			OnSession: n.session,
			Logger:    vpnLogger("openvpn"),
		})
	})
	if err == nil {
		log.Printf("[openvpn] userspace server on udp/%d (pool %s %s)", cfg.port(), cidr, cidr6)
	}
	return err
}

// openVPNAddr6 maps ip in the v4 pool to its address in the v6 prefix, the
// way the OpenVPN server derives a client's IPv6 address.
func openVPNAddr6(cidr4, cidr6 string, ip netip.Addr) (netip.Addr, error) {
	p4, err := netip.ParsePrefix(cidr4)
	if err != nil {
		return netip.Addr{}, err
	}
	p6, err := netip.ParsePrefix(cidr6)
	if err != nil {
		return netip.Addr{}, err
	}
	if host := 128 - p6.Bits(); host < 32-p4.Bits() {
		return netip.Addr{}, fmt.Errorf("pool6 %s is too small for pool %s", cidr6, cidr4)
	}
	b4, i4 := p4.Masked().Addr().As4(), ip.As4()
	off := (uint32(i4[0])<<24 | uint32(i4[1])<<16 | uint32(i4[2])<<8 | uint32(i4[3])) -
		(uint32(b4[0])<<24 | uint32(b4[1])<<16 | uint32(b4[2])<<8 | uint32(b4[3]))
	b6 := p6.Masked().Addr().As16()
	carry := uint64(off)
	for i := 15; i >= 0 && carry > 0; i-- {
		carry += uint64(b6[i])
		b6[i] = byte(carry)
		carry >>= 8
	}
	return netip.AddrFrom16(b6), nil
}

// StopOpenVPN stops the OpenVPN server.
func (a *App) StopOpenVPN() { a.stopVPN("openvpn") }

// RestartOpenVPN applies the current OpenVPN config.
func (a *App) RestartOpenVPN() error {
	cfg := a.store.Get().OpenVPN
	if cfg == nil || !cfg.Enabled {
		a.StopOpenVPN()
		return nil
	}
	return a.StartOpenVPN(*cfg)
}

// OpenVPNProfile is a client profile for endpoint (the address clients use to
// reach this node): username/password auth, the CA inline.
func (a *App) OpenVPNProfile(endpoint string) (string, error) {
	cfg := a.store.Get().OpenVPN
	if cfg == nil || cfg.CertID == "" {
		return "", errors.New("openvpn is not configured")
	}
	endpoint = strings.TrimSpace(endpoint)
	// OpenVPN takes an IPv6 literal bare ("remote 2001:db8::1 1194")
	if strings.HasPrefix(endpoint, "[") && strings.HasSuffix(endpoint, "]") {
		endpoint = endpoint[1 : len(endpoint)-1]
	}
	if endpoint == "" || strings.ContainsAny(endpoint, " \t\r\n<>[]") {
		return "", errors.New("endpoint must be a host name or address")
	}
	ca, err := os.ReadFile(a.tls.CertPath(cfg.CertID))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "client\ndev tun\nproto udp\nremote %s %d\n", endpoint, cfg.port())
	b.WriteString("nobind\npersist-key\npersist-tun\nresolv-retry infinite\n")
	// the server pushes tun-mtu only to clients that apply it (2.6+); older
	// ones take it from here
	fmt.Fprintf(&b, "tun-mtu %d\n", cfg.mtuVal())
	// both families into the tunnel: the server pushes an IPv6 address and
	// gateway, and IPv6 left outside the tunnel would leak on dual-stack clients
	b.WriteString("remote-cert-tls server\nauth-user-pass\n")
	ipv6 := boolOr(cfg.TunnelIPv6, true)
	if boolOr(cfg.FullTunnel, true) {
		if ipv6 {
			b.WriteString("redirect-gateway def1 ipv6\n")
		} else {
			b.WriteString("redirect-gateway def1\n")
		}
	} else {
		// split tunnel: only these networks go through the tunnel
		for _, r := range cfg.Routes {
			p, err := netip.ParsePrefix(strings.TrimSpace(r))
			if err != nil {
				continue
			}
			if p.Addr().Is4() {
				m := net.CIDRMask(p.Bits(), 32)
				fmt.Fprintf(&b, "route %s %s\n", p.Masked().Addr(), net.IP(m).String())
			} else if ipv6 {
				fmt.Fprintf(&b, "route-ipv6 %s\n", p.Masked())
			}
		}
	}
	b.WriteString("data-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305\n")
	if cfg.Control == OpenVPNTLSAuth && cfg.Auth != "" {
		fmt.Fprintf(&b, "auth %s\n", strings.ToUpper(cfg.Auth))
	}
	if extra := strings.TrimSpace(cfg.ProfileExtra); extra != "" {
		b.WriteString(strings.ReplaceAll(extra, "\r", ""))
		b.WriteByte('\n')
	}
	b.WriteString("verb 3\n<ca>\n")
	b.WriteString(strings.TrimSpace(string(ca)))
	b.WriteString("\n</ca>\n")
	switch cfg.Control {
	case OpenVPNTLSCrypt:
		b.WriteString("<tls-crypt>\n" + strings.TrimSpace(cfg.StaticKey) + "\n</tls-crypt>\n")
	case OpenVPNTLSAuth:
		b.WriteString("key-direction 1\n<tls-auth>\n" + strings.TrimSpace(cfg.StaticKey) + "\n</tls-auth>\n")
	}
	return b.String(), nil
}

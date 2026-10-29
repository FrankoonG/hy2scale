// Package l2tp is the public entry point for L2TP/IPsec: an IKEv1-keyed IPsec
// transport SA (RFC 2409) carrying an L2TP tunnel (RFC 2661) and a PPP session
// over a userspace TUN. It is the classic native-OS remote-access VPN — the mode
// Windows, macOS, iOS and Android ship, and the stock xl2tpd/strongSwan stack
// speaks.
//
// Like every protocol here, Dial installs no addresses, routes or DNS: it
// returns the negotiated Result for the caller to apply. Importing this package
// registers "l2tp" with the client registry:
//
//	import _ "github.com/xen0bit/veepin/l2tp"
//	sess, res, err := client.Dial(ctx, "l2tp", opts)
//
// The exchange machinery lives in internal/ikev1 (IKE), internal/l2tp (L2TP) and
// internal/ppp (PPP); this package wires them to a real TUN and the registry.
package l2tp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/xen0bit/veepin/client"
	"github.com/xen0bit/veepin/dataplane"
	"github.com/xen0bit/veepin/internal/ikev1"
	engine "github.com/xen0bit/veepin/internal/l2tp"
	"github.com/xen0bit/veepin/internal/ppp"
	"github.com/xen0bit/veepin/internal/vlog"
)

func init() { client.Register("l2tp", parseOptions) }

// Option names for client.Dial.
const (
	OptServer   = "server"
	OptPort     = "port"
	OptPSK      = "psk"
	OptUser     = "user"
	OptPassword = "password"
	OptDNS      = "dns"
	OptTUNName  = "tun"
)

// defaultPort is the IKE port Main Mode starts on. After the NAT-T float both
// IKE and UDP-encapsulated ESP move to nattPort, which RFC 3948 fixes — so only
// the IKE port is configurable.
const (
	defaultPort = 500
	nattPort    = 4500
)

const dialTimeout = 30 * time.Second

// Config holds the parameters for one L2TP/IPsec tunnel.
type Config struct {
	Server   string // server host or IP (required)
	Port     int    // combined IKE/ESP port (default 500)
	PSK      string // IPsec pre-shared key (required)
	Username string // MS-CHAPv2 username (required)
	Password string // MS-CHAPv2 password (required)
	DNS      []net.IP
	TUNName  string
	Logger   *slog.Logger
}

func (c *Config) validate() error {
	switch {
	case c.Server == "":
		return fmt.Errorf("l2tp: server is required")
	case c.PSK == "":
		return fmt.Errorf("l2tp: psk is required")
	case c.Username == "":
		return fmt.Errorf("l2tp: user is required")
	case c.Password == "":
		return fmt.Errorf("l2tp: password is required")
	}
	return nil
}

func parseOptions(opts map[string]string) (client.Dialer, error) {
	cfg := &Config{
		Server:   opts[OptServer],
		PSK:      opts[OptPSK],
		Username: opts[OptUser],
		Password: opts[OptPassword],
		DNS:      parseIPList(opts[OptDNS]),
		TUNName:  opts[OptTUNName],
	}
	if v := opts[OptPort]; v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("l2tp: invalid port %q", v)
		}
		cfg.Port = p
	}
	return dialer{cfg}, cfg.validate()
}

type dialer struct{ cfg *Config }

func (d dialer) Dial(ctx context.Context) (client.Session, client.Result, error) {
	return Dial(ctx, *d.cfg)
}

// Dial establishes the IPsec SA, L2TP session and PPP link, brings up a TUN, and
// returns the assigned inner addressing for the caller to apply.
func Dial(ctx context.Context, cfg Config) (client.Session, client.Result, error) {
	if err := cfg.validate(); err != nil {
		return nil, client.Result{}, err
	}
	logger := vlog.From(cfg.Logger)

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}
	raddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(cfg.Server, strconv.Itoa(port)))
	if err != nil {
		return nil, client.Result{}, fmt.Errorf("l2tp: resolve %s: %w", cfg.Server, err)
	}
	// One unconnected socket carries Main Mode to the IKE port and, after the
	// NAT-T float, both IKE and ESP to the NAT-T port. localIP is discovered
	// before binding it, since the socket itself is wildcard-bound.
	localIP, err := outboundIP(raddr)
	if err != nil {
		return nil, client.Result{}, err
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: localIP})
	if err != nil {
		return nil, client.Result{}, fmt.Errorf("l2tp: bind: %w", err)
	}
	tun, err := dataplane.OpenTUN(cfg.TUNName)
	if err != nil {
		conn.Close()
		return nil, client.Result{}, fmt.Errorf("l2tp: open TUN: %w", err)
	}

	c := engine.NewClient(conn, tun, engine.ClientConfig{
		ServerIP: raddr.IP,
		IKEPort:  port,
		NATTPort: nattPort,
		LocalIP:  localIP,
		PSK:      []byte(cfg.PSK),
		Username: cfg.Username,
		Password: cfg.Password,
		DNS:      cfg.DNS,
		Logger:   logger,
	})

	hctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	nc, err := c.Handshake(hctx)
	if err != nil {
		tun.Close()
		// Two credentials in sequence, and either can be the wrong one: the
		// IPsec PSK authenticates phase 1, and MS-CHAPv2 authenticates the user
		// inside the tunnel it builds. Both are the caller's to fix, so both
		// map onto client.ErrAuth rather than only the one that happens to be
		// checked first.
		err = client.WrapAuth(fmt.Errorf("l2tp: %w", err), ikev1.ErrAuth, ppp.ErrAuth)
		return nil, client.Result{}, err
	}
	logger.Printf("l2tp: tunnel up on %s, address %s", tun.Name(), nc.AssignedIP)

	res := client.Result{
		TUNName:    tun.Name(),
		AssignedIP: nc.AssignedIP,
		Netmask:    nc.Netmask,
		// Gateway is the server's outer IP: the router pins a host route to it so
		// the ESP/IKE carrier does not recurse into the tunnel.
		Gateway: raddr.IP,
		DNS:     nc.DNS,
		MTU:     client.DefaultTunnelMTU,
	}
	return &session{engine: c, tun: tun}, res, nil
}

// session is a running L2TP/IPsec tunnel implementing client.Session.
type session struct {
	engine *engine.Client
	tun    *dataplane.TUN
}

// Probe implements client.Prober.
//
// client/liveness.go names L2TP as a protocol that can black-hole silently, and
// it is the case with two layers to go quiet: the ESP transport SA underneath
// and the L2TP control connection inside it. A HELLO and its acknowledgement
// cross both, so one probe covers both.
//
// This end has always answered a peer's HELLO. It had never sent one, so an
// idle tunnel queued nothing, the control channel's retransmit timer had
// nothing to retransmit, and the session stayed up reporting health it had not
// checked.
func (s *session) Probe(ctx context.Context) error { return s.engine.Probe(ctx) }

func (s *session) Wait(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- s.engine.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = s.Close()
		return ctx.Err()
	}
}

func (s *session) Close() error {
	err := s.engine.Close()
	s.tun.Close()
	return err
}

// outboundIP reports the local address the kernel would source traffic to raddr
// from. IKE puts it in the ID payload and the phase-2 traffic selectors, so it
// has to be the real interface address rather than the wildcard the socket binds.
// The dial is connectionless — it consults the routing table without any packet
// leaving the host.
func outboundIP(raddr *net.UDPAddr) (net.IP, error) {
	probe, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("l2tp: route to %s: %w", raddr.IP, err)
	}
	defer probe.Close()
	la, ok := probe.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("l2tp: cannot determine the local address for %s", raddr.IP)
	}
	return la.IP, nil
}

// parseIPList parses a comma/space-separated list of IPs, dropping anything
// unparseable.
func parseIPList(list string) []net.IP {
	fields := strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	var out []net.IP
	for _, s := range fields {
		if ip := net.ParseIP(s); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

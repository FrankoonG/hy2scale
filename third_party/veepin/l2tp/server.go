package l2tp

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"

	"github.com/xen0bit/veepin/client"
	"github.com/xen0bit/veepin/dataplane"
	engine "github.com/xen0bit/veepin/internal/l2tp"
	"github.com/xen0bit/veepin/internal/userdb"
	"github.com/xen0bit/veepin/internal/vlog"
)

func init() {
	client.RegisterServer("l2tp", parseServerOptions)
	client.RegisterServerOpts("l2tp", []client.OptSpec{
		{Key: OptServerListen, Kind: client.OptStr, Default: "0.0.0.0", Help: "local IP to bind the IKE/ESP sockets on (default 0.0.0.0)"},
		{Key: OptServerPublic, Kind: client.OptStr, Help: "server's public IP as clients reach it (IKE identity and traffic selector); required when -listen is the wildcard"},
		{Key: OptServerPort, Kind: client.OptInt, Default: "500", Help: "UDP port to listen on (default 500)"},
		{Key: OptServerPSK, Kind: client.OptStr, Required: true, Secret: true, Generate: "psk", Help: "IPsec pre-shared key"},
		{Key: OptServerPool, Kind: client.OptCIDR, Default: "10.20.0.0/24", Help: "internal address pool handed to clients (default 10.20.0.0/24)"},
		{Key: OptServerDNS, Kind: client.OptCommaList, Help: "comma-separated DNS servers assigned to clients"},
		{Key: OptServerUser, Kind: client.OptStr, Help: "MS-CHAPv2 username to accept; the one-user shorthand for users-file, and one of the two is required"},
		{Key: OptServerPassword, Flag: "pass", Kind: client.OptStr, Secret: true, Help: "the password for user"},
		{Key: OptServerUsers, Kind: client.OptFilePath, Secret: true, Help: "path to a file of username:secret lines, for more than one user; MS-CHAPv2 derives its response from the password, so the secret must be the password itself"},
		client.TUNOpt(OptServerTUN),
		client.ShapeOpt(OptServerShape, "downstream"),
	})
}

// Server option keys for client.NewServer("l2tp", opts).
const (
	OptServerListen   = "listen"
	OptServerPublic   = "public"
	OptServerPort     = "port"
	OptServerPSK      = "psk"
	OptServerUser     = "user"
	OptServerPassword = "password"
	OptServerUsers    = "users-file"
	OptServerPool     = "pool"
	OptServerDNS      = "dns"
	OptServerTUN      = "tun"
	OptServerShape    = "shape" // per-flow downstream shaping budget in bytes (0 = off)
)

const defaultPool = "10.20.0.0/24"

// ServerConfig configures an L2TP/IPsec responder and its userspace data path.
type ServerConfig struct {
	// ListenIP is the local IP to bind the IKE/ESP sockets on (default 0.0.0.0).
	ListenIP string
	// PublicIP is the server's address as clients reach it, used as the IKE
	// identity and phase-2 traffic selector. It defaults to ListenIP when that is
	// concrete, and must be set when listening on the wildcard.
	PublicIP string
	// Port is the combined IKE/ESP port (default 500).
	Port int
	// PSK authenticates the IPsec SA (required).
	PSK string
	// Users maps a username to its password for MS-CHAPv2 (at least one required).
	Users map[string]string
	// Pool is the internal address pool handed to clients, CIDR (default
	// 10.20.0.0/24). Its first host is the server's tunnel address.
	Pool string
	// DNS servers assigned to clients over IPCP.
	DNS []net.IP
	// TUNName is the desired TUN interface name; empty lets the kernel pick.
	TUNName string

	// Shape enables downstream traffic shaping: how much padded output each
	// inner flow is given before shaping stops for that flow, so it bounds what
	// shaping costs. A flow gets Shape/MTU padded packets whatever sizes it
	// carries. Zero, the default, disables it.
	//
	// It hides the size pattern of an inner TLS handshake, which otherwise shows
	// through as the size of the ESP packet carrying it (see dataplane/shape.go).
	// Clients need no support for it — the padding is RFC 1661 §5.1 PPP padding,
	// which every conforming peer trims by the inner IP header — so a stock
	// Windows or macOS L2TP/IPsec client benefits unmodified.
	// dataplane.DefaultShapeBytes is a reasonable value.
	Shape int

	// Device, if set, replaces the kernel TUN. (hy2scale P1)
	Device dataplane.Device
	// ListenPacket, if set, opens the IKE (Port) and NAT-T sockets. (hy2scale P2)
	ListenPacket dataplane.ListenFunc
	// UserLookup, if set, replaces Users. (hy2scale P3)
	UserLookup func(username string) (string, bool)
	// OnSession reports a client's address coming up / going away. (hy2scale P3)
	OnSession func(up bool, user string, addr netip.Addr)

	Logger *slog.Logger
}

// Server is a running L2TP/IPsec responder. It owns the TUN and the UDP socket
// but, like the other protocols, configures no host networking — Gateway and
// Network report what the caller needs to do that.
type Server struct {
	eng     *engine.Server
	tun     dataplane.Device
	pool    *dataplane.AddrPool
	gateway net.IP
}

// NewServer opens the TUN, creates the address pool, and binds the socket. It
// does not start serving until ListenAndServe.
func NewServer(cfg ServerConfig) (*Server, error) {
	switch {
	case cfg.PSK == "":
		return nil, fmt.Errorf("l2tp: PSK is required")
	case len(cfg.Users) == 0 && cfg.UserLookup == nil:
		return nil, fmt.Errorf("l2tp: at least one user is required")
	}
	logger := vlog.From(cfg.Logger)
	poolCIDR := cfg.Pool
	if poolCIDR == "" {
		poolCIDR = defaultPool
	}
	pool, gateway, err := dataplane.NewAddrPool(poolCIDR)
	if err != nil {
		return nil, fmt.Errorf("l2tp: address pool: %w", err)
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}
	listenIP := net.ParseIP(cfg.ListenIP)
	if listenIP == nil {
		listenIP = net.IPv4zero
	}
	// Two sockets: Main Mode arrives on the IKE port, and everything after the
	// NAT-T float — IKE behind the non-ESP marker, and UDP-encapsulated ESP —
	// arrives on 4500.
	listen := cfg.ListenPacket
	if listen == nil {
		listen = func(p int) (*dataplane.PacketConn, error) {
			c, lerr := net.ListenUDP("udp", &net.UDPAddr{IP: listenIP, Port: p})
			if lerr != nil {
				return nil, lerr
			}
			return dataplane.NewPacketConn(c), nil
		}
	}
	ikeConn, err := listen(port)
	if err != nil {
		return nil, fmt.Errorf("l2tp: bind %s:%d: %w", cfg.ListenIP, port, err)
	}
	nattConn, err := listen(nattPort)
	if err != nil {
		ikeConn.Close()
		return nil, fmt.Errorf("l2tp: bind %s:%d: %w", cfg.ListenIP, nattPort, err)
	}
	var tun dataplane.Device = cfg.Device
	if tun == nil {
		t, terr := dataplane.OpenTUN(cfg.TUNName)
		if terr != nil {
			ikeConn.Close()
			nattConn.Close()
			return nil, fmt.Errorf("l2tp: open TUN: %w", terr)
		}
		tun = t
	}

	eng := engine.NewServer(ikeConn, nattConn, tun, engine.ServerConfig{
		PSK:        []byte(cfg.PSK),
		Users:      cfg.Users,
		UserLookup: cfg.UserLookup,
		OnSession:  cfg.OnSession,
		PublicIP:   net.ParseIP(cfg.PublicIP),
		Pool:       pool,
		Gateway:    gateway,
		DNS:        cfg.DNS,
		Shape:      cfg.Shape,
		Logger:     logger,
	})
	return &Server{eng: eng, tun: tun, pool: pool, gateway: gateway}, nil
}

// ListenAndServe serves clients until Close. It blocks.
func (s *Server) ListenAndServe() error { return s.eng.Serve() }

// Close stops the server and releases the TUN and socket.
func (s *Server) Close() error {
	err := s.eng.Close()
	s.tun.Close()
	return err
}

// Abandon implements client.AbandonableServer. It closes the TUN directly, so
// an abandoned listener's packet pump unparks and its descriptor is released
// even though Close never returned. See client.AbandonableServer for why this
// is not simply Close.
//
// The TUN is set in NewServer and never reassigned, so this reads it without
// the lock Close takes -- deliberately, because a wedged Close may be holding
// that lock, and waiting on it here would reproduce the very stall this is the
// escape from.
func (s *Server) Abandon() { s.tun.Close() }

// Server implements client.AbandonableServer, so the supervisor can take its
// descriptors back when Close overruns. Asserted here because the interface is
// found by type assertion at the one call site: without this, a renamed or
// re-signatured Abandon compiles fine and the assertion silently starts failing,
// which reads as the leak coming back.
var _ client.AbandonableServer = (*Server)(nil)

// TUNName is the interface the data path is bound to.
func (s *Server) TUNName() string { return s.tun.Name() }

// Gateway is the server's own tunnel-side address (the pool's first host).
func (s *Server) Gateway() net.IP { return s.gateway }

// Network is the tunnel subnet, for routing and NAT rules.
func (s *Server) Network() *net.IPNet { return s.pool.Network() }

func parseServerOptions(opts map[string]string) (client.Server, error) {
	cfg := ServerConfig{
		ListenIP: opts[OptServerListen],
		PublicIP: opts[OptServerPublic],
		PSK:      opts[OptServerPSK],
		Pool:     opts[OptServerPool],
		DNS:      parseIPList(opts[OptServerDNS]),
		TUNName:  opts[OptServerTUN],
		Logger:   slog.New(vlog.NewTextHandler(os.Stdout, slog.LevelInfo)),
	}
	if cfg.ListenIP == "" {
		cfg.ListenIP = "0.0.0.0"
	}
	if v := opts[OptServerShape]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("l2tp: invalid %s %q", OptServerShape, v)
		}
		cfg.Shape = n
	}
	if v := opts[OptServerPort]; v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("l2tp: invalid port %q", v)
		}
		cfg.Port = p
	}
	users, uerr := userdb.Resolve(userdb.NeedsPlaintext, opts[OptServerUsers], opts[OptServerUser], opts[OptServerPassword])
	if uerr != nil {
		return nil, fmt.Errorf("l2tp: %w", uerr)
	}
	cfg.Users = users
	return NewServer(cfg)
}

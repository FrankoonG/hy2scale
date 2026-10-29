package openvpn

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/xen0bit/veepin/client"
	iovpn "github.com/xen0bit/veepin/internal/openvpn"
	"github.com/xen0bit/veepin/internal/pqpolicy"
	"github.com/xen0bit/veepin/internal/vlog"
)

// ServerConfig configures an OpenVPN responder: certificates, the address pool
// it hands out, and the control-channel protection it expects. It is the
// engine's own type under its public name; see internal/openvpn.
type ServerConfig = iovpn.ServerConfig

// Server is a running OpenVPN responder: one UDP socket serving many clients, a
// TUN device, an address pool, and the shared data-path pump. It owns the TUN but
// does not configure host networking — Gateway and Network report what a caller
// needs to do that itself. The engine is internal/openvpn's; this is its public
// name.
type Server struct{ *iovpn.Server }

// Server implements client.AbandonableServer and client.DualStackServer, both
// discovered by type assertion at their one call site, so a method lost in the
// embedding would compile fine and silently stop being found. Asserted here on
// the type the registry hands out, which is the one that matters.
var (
	_ client.AbandonableServer = (*Server)(nil)
	_ client.DualStackServer   = (*Server)(nil)
)

// NewServer builds a server from cfg: it parses the certificates and pool and
// opens the TUN device. It does not bind the socket until ListenAndServe. Opening
// a TUN device requires CAP_NET_ADMIN.
func NewServer(cfg ServerConfig) (*Server, error) {
	srv, err := iovpn.NewServer(cfg)
	if err != nil {
		return nil, err
	}
	return &Server{srv}, nil
}

// Server option keys for client.NewServer("openvpn", opts).
const (
	OptServerCA         = "ca"
	OptServerCert       = "cert"
	OptServerKey        = "key"
	OptServerListenIP   = "listen"
	OptServerListenPort = "port"
	OptServerPool       = "pool"
	OptServerPool6      = "pool6"
	OptServerDNS        = "dns"
	OptServerTUN        = "tun"
	// Control-channel protection, matching the client options of the same name.
	OptServerTLSAuth      = "tls-auth"
	OptServerTLSCrypt     = "tls-crypt"
	OptServerAuth         = "auth"
	OptServerKeyDirection = "key-direction"
	// OptServerShape is the per-flow downstream shaping budget in bytes (0 = off).
	OptServerShape = "shape"
)

func init() {
	client.RegisterServer("openvpn", parseServerOptions)
	client.RegisterServerOpts("openvpn", []client.OptSpec{
		{Key: OptServerCA, Kind: client.OptFilePath, Required: true, Generate: "x509-chain", Help: "path to the CA certificate PEM"},
		{Key: OptServerCert, Kind: client.OptFilePath, Required: true, Generate: "x509-chain", Help: "path to the server certificate PEM"},
		{Key: OptServerKey, Kind: client.OptFilePath, Required: true, Secret: true, Generate: "x509-chain", Help: "path to the server private key PEM"},
		{Key: OptServerTLSAuth, Kind: client.OptFilePath, Secret: true, Help: "static key adding an HMAC to every control packet"},
		{Key: OptServerTLSCrypt, Kind: client.OptFilePath, Secret: true, Help: "static key encrypting and authenticating every control packet"},
		{Key: OptServerAuth, Kind: client.OptStr, Help: "HMAC digest for -tls-auth: SHA1 (default) or SHA256"},
		{Key: OptServerKeyDirection, Kind: client.OptInt, Default: "-1", Help: "the client's --key-direction for -tls-auth: 0, 1, or -1 for a bidirectional key"},
		{Key: OptServerListenIP, Kind: client.OptStr, Default: "0.0.0.0", Help: "local IP to bind the UDP socket on (default 0.0.0.0)"},
		{Key: OptServerListenPort, Kind: client.OptInt, Default: "1194", Help: "UDP port to listen on (default 1194)"},
		{Key: OptServerPool, Kind: client.OptCIDR, Default: "10.8.0.0/24", Help: "internal address pool handed to clients (default 10.8.0.0/24)"},
		{Key: OptServerPool6, Kind: client.OptCIDR, Help: "IPv6 prefix for a dual-stack tunnel, e.g. fd00:8::/64 (empty leaves the tunnel IPv4-only)"},
		{Key: OptServerDNS, Kind: client.OptCommaList, Help: "comma-separated DNS servers pushed to clients"},
		client.TUNOpt(OptServerTUN),
		client.ShapeOpt(OptServerShape, "downstream"),
	})
}

// parseServerOptions builds an OpenVPN responder from string options, reading the
// CA/cert/key from the paths given.
func parseServerOptions(opts map[string]string) (client.Server, error) {
	cfg := ServerConfig{
		ListenIP: opts[OptServerListenIP],
		Pool:     opts[OptServerPool],
		Pool6:    opts[OptServerPool6],
		TUNName:  opts[OptServerTUN],
		Logger:   slog.New(vlog.NewTextHandler(os.Stdout, slog.LevelInfo)),

		PostQuantumOnly: pqpolicy.Requested(opts),
	}
	var err error
	if cfg.CA, err = readFileOpt(opts[OptServerCA]); err != nil {
		return nil, fmt.Errorf("openvpn: ca: %w", err)
	}
	if cfg.Cert, err = readFileOpt(opts[OptServerCert]); err != nil {
		return nil, fmt.Errorf("openvpn: cert: %w", err)
	}
	if cfg.Key, err = readFileOpt(opts[OptServerKey]); err != nil {
		return nil, fmt.Errorf("openvpn: key: %w", err)
	}
	if v := opts[OptServerListenPort]; v != "" {
		p, perr := parsePort(v)
		if perr != nil {
			return nil, perr
		}
		cfg.ListenPort = p
	}
	// Both wrappings are optional, so an absent option is not an error — unlike
	// the CA/cert/key above, which readFileOpt treats as required. The CLI always
	// puts a key in the map, empty when the flag was not given.
	if v := opts[OptServerTLSAuth]; v != "" {
		if cfg.TLSAuth, err = os.ReadFile(v); err != nil {
			return nil, fmt.Errorf("openvpn: tls-auth: %w", err)
		}
	}
	if v := opts[OptServerTLSCrypt]; v != "" {
		if cfg.TLSCrypt, err = os.ReadFile(v); err != nil {
			return nil, fmt.Errorf("openvpn: tls-crypt: %w", err)
		}
	}
	cfg.Auth = opts[OptServerAuth]
	cfg.KeyDirection = -1
	if v := opts[OptServerKeyDirection]; v != "" {
		d, derr := strconv.Atoi(v)
		if derr != nil || d < -1 || d > 1 {
			return nil, fmt.Errorf("openvpn: invalid %s %q", OptServerKeyDirection, v)
		}
		cfg.KeyDirection = d
	}
	if v := opts[OptServerShape]; v != "" {
		n, nerr := strconv.Atoi(v)
		if nerr != nil || n < 0 {
			return nil, fmt.Errorf("openvpn: invalid %s %q", OptServerShape, v)
		}
		cfg.Shape = n
	}
	cfg.DNS = append(cfg.DNS, splitCommaIPs(opts[OptServerDNS])...)
	return NewServer(cfg)
}

func readFileOpt(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("required")
	}
	return os.ReadFile(path)
}

func parsePort(v string) (int, error) {
	var p int
	if _, err := fmt.Sscanf(v, "%d", &p); err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("openvpn: invalid port %q", v)
	}
	return p, nil
}

func splitCommaIPs(list string) []net.IP {
	var out []net.IP
	for s := range strings.SplitSeq(list, ",") {
		if s = strings.TrimSpace(s); s != "" {
			if ip := net.ParseIP(s); ip != nil {
				out = append(out, ip)
			}
		}
	}
	return out
}

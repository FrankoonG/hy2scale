// Package openvpn is the public entry point to this module's OpenVPN
// implementation: a UDP client that speaks OpenVPN's TLS control channel and
// AES-256-GCM data channel, over a userspace TUN.
//
// Like every protocol here, Dial installs no addresses, routes or DNS. It
// returns the negotiated client.Result and the caller applies it — the veepin
// command hands it to dataplane's router, and the NetworkManager plugin to NM.
//
// Importing this package registers "openvpn" with the client registry:
//
//	import _ "github.com/xen0bit/veepin/openvpn"
//
//	sess, res, err := client.Dial(ctx, "openvpn", opts)
//
// The implementation lives in internal/openvpn: the client session and the
// server engine there, and beneath them the packet codec, reliability layer,
// TLS control channel, key exchange, and data-channel crypto. This package is
// the supported surface -- .ovpn profiles, options, validation -- and hands the
// engine what it needs to dial or serve.
//
// # Scope
//
// Both roles. The client is an initiator over UDP transport with TLS certificate
// authentication and P_DATA_V2 (a server-assigned peer-id). It negotiates the
// AES-256-GCM data cipher by default and also speaks the older AES-256-CBC data
// channel (encrypt-then-MAC with an --auth HMAC). The control channel can be
// plain, --tls-auth (an HMAC over every control packet), or --tls-crypt
// (authenticated encryption of every control packet); the static key is read
// from the config or a flag. It does not implement compression or the older
// net30 topology's assumptions, and a profile it cannot speak fails at dial
// rather than silently misbehaving.
package openvpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/xen0bit/veepin/client"
	iovpn "github.com/xen0bit/veepin/internal/openvpn"
	"github.com/xen0bit/veepin/internal/pqpolicy"
	"github.com/xen0bit/veepin/internal/vlog"
)

func init() { client.Register("openvpn", parseOptions) }

// parseOptions turns string-keyed options into a Dialer: it loads the .ovpn file
// if given, layers the individual options over it, and validates. It is what the
// registry calls for client.Dial(ctx, "openvpn", opts).
func parseOptions(opts map[string]string) (client.Dialer, error) {
	cfg := &Config{KeyDirection: -1}
	if path := opts[OptConfig]; path != "" {
		loaded, err := ParseConfigFile(path)
		if err != nil {
			return nil, err
		}
		cfg = loaded
	}
	if err := cfg.applyOverrides(opts); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	// After the overrides, deliberately. A -config <file> replaces cfg
	// wholesale a few lines up, so setting this any earlier would let loading
	// an .ovpn profile silently drop the guarantee the pq- name promised.
	cfg.PostQuantumOnly = pqpolicy.Requested(opts)
	return dialer{cfg}, nil
}

// dialer adapts a Config to client.Dialer.
type dialer struct{ cfg *Config }

func (d dialer) Dial(ctx context.Context) (client.Session, client.Result, error) {
	return Dial(ctx, *d.cfg)
}

// Dial connects to the OpenVPN server, runs the handshake, key exchange and
// config pull, opens the TUN, and starts the data path. It returns a running
// session and the Result the caller must apply. On error nothing is left
// running.
func Dial(ctx context.Context, cfg Config) (client.Session, client.Result, error) {
	if err := cfg.validate(); err != nil {
		return nil, client.Result{}, fmt.Errorf("openvpn: %w", err)
	}
	endpoint, err := net.ResolveUDPAddr("udp", net.JoinHostPort(cfg.Remote, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, client.Result{}, fmt.Errorf("openvpn: resolve %s: %w", cfg.Remote, err)
	}
	sess, res, err := iovpn.Dial(ctx, iovpn.ClientConfig{
		Endpoint:     endpoint,
		CA:           cfg.CA,
		Cert:         cfg.Cert,
		Key:          cfg.Key,
		Cipher:       cfg.Cipher,
		Auth:         cfg.Auth,
		TLSAuth:      cfg.TLSAuth,
		TLSCrypt:     cfg.TLSCrypt,
		KeyDirection: cfg.KeyDirection,
		Username:     cfg.Username,
		Password:     cfg.Password,
		TUNName:      cfg.TUNName,
		Shape:        cfg.Shape,
		Logger:       vlog.From(cfg.Logger),
		Peer: iovpn.PeerPolicy{
			X509Name:           cfg.VerifyX509Name,
			X509NameType:       cfg.VerifyX509Type,
			RequireServerUsage: cfg.RemoteCertTLSServer,
			CRL:                cfg.CRL,
		},
		MinTLS13:        cfg.MinTLS13,
		PostQuantumOnly: cfg.PostQuantumOnly,
	})
	var authErr *iovpn.AuthError
	if errors.As(err, &authErr) {
		return nil, client.Result{}, fmt.Errorf("openvpn: %w: %w", client.ErrAuth, authErr.Err)
	}
	return sess, res, err
}

package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
)

// WireGuardConfig stored in config.yaml.
type WireGuardConfig struct {
	Enabled    bool            `yaml:"enabled" json:"enabled"`
	ListenPort int             `yaml:"listen_port" json:"listen_port"`
	PrivateKey string          `yaml:"private_key" json:"private_key"`
	Address    string          `yaml:"address" json:"address"`
	DNS        string          `yaml:"dns,omitempty" json:"dns,omitempty"`
	MTU        int             `yaml:"mtu,omitempty" json:"mtu,omitempty"`
	Peers      []WireGuardPeer `yaml:"peers" json:"peers"`
}

// WireGuardPeer is a peer entry.
type WireGuardPeer struct {
	Name       string `yaml:"name" json:"name"`
	PublicKey  string `yaml:"public_key" json:"public_key"`
	PrivateKey string `yaml:"private_key" json:"private_key"`
	AllowedIPs string `yaml:"allowed_ips" json:"allowed_ips"`
	Keepalive  int    `yaml:"keepalive" json:"keepalive"`
	ExitVia    string `yaml:"exit_via,omitempty" json:"exit_via"`
}

type wgInstance struct {
	dev    *device.Device
	cancel context.CancelFunc
}

var (
	wgMu      sync.Mutex
	wgRunning *wgInstance
)

// GenerateWireGuardKey returns (privateKey, publicKey) base64.
func GenerateWireGuardKey() (string, string) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return "", ""
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, _ := curve25519.X25519(priv[:], curve25519.Basepoint)
	return base64.StdEncoding.EncodeToString(priv[:]),
		base64.StdEncoding.EncodeToString(pub)
}

// PublicKeyFromPrivate derives public key from base64 private key.
func PublicKeyFromPrivate(privB64 string) (string, error) {
	priv, err := base64.StdEncoding.DecodeString(privB64)
	if err != nil || len(priv) != 32 {
		return "", fmt.Errorf("invalid private key")
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// StartWireGuard starts userspace WireGuard with full traffic forwarding.
func (a *App) StartWireGuard(cfg WireGuardConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 51820
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1420
	}

	gateway, _, err := parseWGAddress(cfg.Address)
	if err != nil {
		return fmt.Errorf("wireguard: %w", err)
	}

	privBytes, err := base64.StdEncoding.DecodeString(cfg.PrivateKey)
	if err != nil || len(privBytes) != 32 {
		return fmt.Errorf("wireguard: invalid private key")
	}

	localAddr, _ := netip.ParseAddr(gateway)

	// Create netstack TUN with SOCKS5 proxy on virtual interface
	tunDev, err := createWGNetstack(
		[]netip.Addr{localAddr},
		parseDNSAddrs(cfg.DNS),
		cfg.MTU, a, cfg,
	)
	if err != nil {
		return fmt.Errorf("wireguard: create tun: %w", err)
	}

	// Create WireGuard device with error logging
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "[wg] "))

	ipc := fmt.Sprintf("private_key=%s\nlisten_port=%d\n",
		hex.EncodeToString(privBytes), cfg.ListenPort)
	for _, peer := range cfg.Peers {
		pubBytes, perr := base64.StdEncoding.DecodeString(peer.PublicKey)
		if perr != nil || len(pubBytes) != 32 {
			log.Printf("[wireguard] skip peer %s: invalid public key", peer.Name)
			continue
		}
		ipc += fmt.Sprintf("public_key=%s\n", hex.EncodeToString(pubBytes))
		for _, aip := range strings.Split(peer.AllowedIPs, ",") {
			aip = strings.TrimSpace(aip)
			if aip != "" {
				ipc += fmt.Sprintf("allowed_ip=%s\n", aip)
			}
		}
		if peer.Keepalive > 0 {
			ipc += fmt.Sprintf("persistent_keepalive_interval=%d\n", peer.Keepalive)
		}
	}

	if err := dev.IpcSet(ipc); err != nil {
		dev.Close()
		return fmt.Errorf("wireguard: ipc set: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("wireguard: device up: %w", err)
	}

	ctx, cancel := context.WithCancel(a.appCtx)

	wgMu.Lock()
	if wgRunning != nil {
		wgRunning.dev.Close()
		wgRunning.cancel()
	}
	wgRunning = &wgInstance{dev: dev, cancel: cancel}
	wgMu.Unlock()

	_ = ctx
	log.Printf("[wireguard] listening on :%d, address %s", cfg.ListenPort, cfg.Address)
	return nil
}

// findPeerExitVia finds the exit_via for a WG peer by their tunnel IP.
func findPeerExitVia(cfg WireGuardConfig, srcIP string) string {
	for _, p := range cfg.Peers {
		for _, aip := range strings.Split(p.AllowedIPs, ",") {
			aip = strings.TrimSpace(aip)
			if aip == "" {
				continue
			}
			// Check if srcIP matches the allowed IP (strip prefix length)
			ip := strings.SplitN(aip, "/", 2)[0]
			if ip == srcIP {
				return p.ExitVia
			}
		}
	}
	return ""
}

// StopWireGuard stops the running WireGuard instance.
func StopWireGuard() {
	wgMu.Lock()
	defer wgMu.Unlock()
	if wgRunning != nil {
		wgRunning.dev.Close()
		wgRunning.cancel()
		wgRunning = nil
		log.Printf("[wireguard] stopped")
	}
}

// GenerateWireGuardClientConfig generates a .conf file for a WireGuard peer.
func GenerateWireGuardClientConfig(serverCfg WireGuardConfig, peer WireGuardPeer, endpoint, dns string) string {
	serverPub, _ := PublicKeyFromPrivate(serverCfg.PrivateKey)
	var b strings.Builder
	b.WriteString("[Interface]\n")
	b.WriteString(fmt.Sprintf("PrivateKey = %s\n", peer.PrivateKey))
	b.WriteString(fmt.Sprintf("Address = %s\n", peer.AllowedIPs))
	if dns != "" {
		b.WriteString(fmt.Sprintf("DNS = %s\n", dns))
	}
	if serverCfg.MTU > 0 {
		b.WriteString(fmt.Sprintf("MTU = %d\n", serverCfg.MTU))
	}
	b.WriteString("\n[Peer]\n")
	b.WriteString(fmt.Sprintf("PublicKey = %s\n", serverPub))
	if endpoint != "" {
		b.WriteString(fmt.Sprintf("Endpoint = %s:%d\n", endpoint, serverCfg.ListenPort))
	}
	// Route WG subnet through tunnel; use SOCKS5 proxy for internet
	gateway, _, _ := parseWGAddress(serverCfg.Address)
	subnet := serverCfg.Address // e.g. 10.0.0.1/24
	if idx := strings.Index(subnet, "/"); idx > 0 {
		// Convert to network: 10.0.0.0/24
		base := net.ParseIP(gateway).To4()
		if base != nil {
			mask := net.CIDRMask(24, 32)
			network := net.IP(make([]byte, 4))
			for i := range base {
				network[i] = base[i] & mask[i]
			}
			subnet = fmt.Sprintf("%s/24", network)
		}
	}
	b.WriteString(fmt.Sprintf("AllowedIPs = %s\n", subnet))
	b.WriteString(fmt.Sprintf("# SOCKS5 proxy: %s:1080 (set as system proxy for internet access)\n", gateway))
	if peer.Keepalive > 0 {
		b.WriteString(fmt.Sprintf("PersistentKeepalive = %d\n", peer.Keepalive))
	}
	return b.String()
}

func parseWGAddress(addr string) (gateway, prefix string, err error) {
	parts := strings.SplitN(addr, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected CIDR (e.g. 10.0.0.1/24)")
	}
	if net.ParseIP(parts[0]) == nil {
		return "", "", fmt.Errorf("invalid IP: %s", parts[0])
	}
	return parts[0], parts[1], nil
}

func parseDNSAddrs(dns string) []netip.Addr {
	if dns == "" {
		return nil
	}
	var addrs []netip.Addr
	for _, s := range strings.Split(dns, ",") {
		s = strings.TrimSpace(s)
		if a, err := netip.ParseAddr(s); err == nil {
			addrs = append(addrs, a)
		}
	}
	return addrs
}

// WireGuardRunning returns true if WireGuard is active.
func WireGuardRunning() bool {
	wgMu.Lock()
	defer wgMu.Unlock()
	return wgRunning != nil
}


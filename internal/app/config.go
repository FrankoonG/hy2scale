package app

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// PasswordOnlyProxies lists proxy types that authenticate by password alone
// (no username). These need per-proxy password support and conflict detection.
// To add a new proxy: add its key here AND in web/app/src/config/proxyRegistry.ts
var PasswordOnlyProxies = []string{"hy2", "ss"}

// UserConfig defines a client user with auth and exit routing.
type UserConfig struct {
	ID             string            `yaml:"id" json:"id"`
	Username       string            `yaml:"username" json:"username"`
	Password       string            `yaml:"password" json:"password"`
	ProxyPasswords map[string]string `yaml:"proxy_passwords,omitempty" json:"proxy_passwords,omitempty"` // proxy-specific overrides (e.g. "hy2", "ss")
	ExitVia        string            `yaml:"exit_via" json:"exit_via"`
	ExitPaths      []string          `yaml:"exit_paths,omitempty" json:"exit_paths,omitempty"`
	ExitMode       string            `yaml:"exit_mode,omitempty" json:"exit_mode,omitempty"` // ""|"quality"|"aggregate"
	TrafficLimit   int64             `yaml:"traffic_limit" json:"traffic_limit"`             // bytes, 0=unlimited
	TrafficUsed    int64             `yaml:"traffic_used" json:"traffic_used"`
	ExpiryDate     string            `yaml:"expiry_date,omitempty" json:"expiry_date"`
	Enabled        bool              `yaml:"enabled" json:"enabled"`
}

// EffectivePassword returns the proxy-specific password if set, else the main password.
func (u *UserConfig) EffectivePassword(proxy string) string {
	if u.ProxyPasswords != nil {
		if p, ok := u.ProxyPasswords[proxy]; ok && p != "" {
			return p
		}
	}
	return u.Password
}

// ProxyConfig defines a protocol listener.
type ProxyConfig struct {
	ID       string `yaml:"id" json:"id"`
	Protocol string `yaml:"protocol" json:"protocol"` // "socks5"
	Listen   string `yaml:"listen" json:"listen"`
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	ExitVia   string   `yaml:"exit_via,omitempty" json:"exit_via,omitempty"`
	ExitPaths []string `yaml:"exit_paths,omitempty" json:"exit_paths,omitempty"`
	ExitMode  string   `yaml:"exit_mode,omitempty" json:"exit_mode,omitempty"`
}

// RoutingRule defines a traffic routing rule (IP or domain based).
type RoutingRule struct {
	ID       string   `yaml:"id" json:"id"`
	Name     string   `yaml:"name" json:"name"`
	Type     string   `yaml:"type" json:"type"`                           // "ip" or "domain"
	Targets  []string `yaml:"targets" json:"targets"`                     // IPs/CIDRs/ranges or domains
	ExitVia   string   `yaml:"exit_via" json:"exit_via"`
	ExitPaths []string `yaml:"exit_paths,omitempty" json:"exit_paths,omitempty"`
	ExitMode  string   `yaml:"exit_mode,omitempty" json:"exit_mode,omitempty"` // ""|"quality"|"aggregate"
	Enabled   bool     `yaml:"enabled" json:"enabled"`
}

// TunModeConfig controls TUN-based IP packet forwarding for the rules engine.
// When enabled, raw IP packets are forwarded through the relay instead of
// TCP/UDP proxy, preserving end-to-end connections (required for protocols
// like Moonlight ENC-RTSP that bind encryption to TCP sessions).
type TunModeConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Mode    string `yaml:"mode" json:"mode"` // "mixed" (routable=TUN, others=proxy) or "full" (all TUN)
}

// ConfigStore manages dynamic configuration with persistence.
type ConfigStore struct {
	mu   sync.RWMutex
	cfg  Config
	path string
}

func NewConfigStore(cfg Config, persistPath string) *ConfigStore {
	return &ConfigStore{cfg: cfg, path: persistPath}
}

func (s *ConfigStore) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *ConfigStore) Update(fn func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
	return s.persist()
}

func (s *ConfigStore) persist() error {
	if s.path == "" {
		return nil
	}
	data, err := yaml.Marshal(&s.cfg)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// loadOrCreateNodeID reads or generates a persistent node ID.
func loadOrCreateNodeID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "node-id")
	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", err
	}
	return id, os.WriteFile(path, []byte(id), 0644)
}

// LoadOrInitConfig loads persisted config from dataDir/config.yaml.
// If it doesn't exist, creates a fresh default config.
func LoadOrInitConfig(dataDir string) (Config, error) {
	os.MkdirAll(dataDir, 0755)

	nodeID, err := loadOrCreateNodeID(dataDir)
	if err != nil {
		return Config{}, err
	}

	persistPath := filepath.Join(dataDir, "config.yaml")
	data, err := os.ReadFile(persistPath)
	if err == nil {
		// Load existing persisted config
		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, err
		}
		if cfg.NodeID == "" {
			cfg.NodeID = nodeID
		}
		if cfg.Name == "" {
			cfg.Name = cfg.NodeID
		}
		if cfg.Peers == nil {
			cfg.Peers = make(map[string]PeerConfig)
		}
		return cfg, nil
	}

	// Fresh start — generate random password and default TLS cert
	pwBytes := make([]byte, 12)
	rand.Read(pwBytes)
	password := hex.EncodeToString(pwBytes)

	// Generate default TLS certificate
	tlsStore := NewTLSStore(dataDir)
	tlsStore.Generate("default", "Default", []string{nodeID}, 3650)

	cfg := Config{
		NodeID:   nodeID,
		Name:     nodeID,
		ExitNode: true,
		Server: &ServerConfig{
			Listen:  "0.0.0.0:5565",
			Password: password,
			TLSCert: filepath.Join(dataDir, "tls", "default.crt"),
			TLSKey:  filepath.Join(dataDir, "tls", "default.key"),
		},
		Peers: make(map[string]PeerConfig),
	}
	return cfg, nil
}

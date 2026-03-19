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

// ProxyConfig defines a protocol listener that exits through the mesh.
type ProxyConfig struct {
	ID       string `yaml:"id" json:"id"`
	Protocol string `yaml:"protocol" json:"protocol"` // "socks5"
	Listen   string `yaml:"listen" json:"listen"`
	ExitVia  string `yaml:"exit_via" json:"exit_via"`
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

	// Fresh start — empty config with just the node ID
	cfg := Config{
		NodeID:   nodeID,
		Name:     nodeID,
		ExitNode: true,
		Peers:    make(map[string]PeerConfig),
	}
	return cfg, nil
}

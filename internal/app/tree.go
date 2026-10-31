package app

import (
	"os"
	"strconv"
	"strings"

	relay "github.com/FrankoonG/hy2scale/core/node"
)

// treeConfig: the nested-discovery view of the configuration.
func (a *App) treeConfig() relay.TreeConfig {
	cfg := a.store.Get()
	tc := relay.TreeConfig{
		SelfID: cfg.NodeID,
		Paths:  make(map[string]relay.PathConfig, len(cfg.Peers)),
		Peers:  make([]relay.ConfiguredPeer, 0, len(cfg.Clients)),
	}
	for k, p := range cfg.Peers {
		tc.Paths[k] = relay.PathConfig{Nested: p.Nested, Disabled: p.Disabled}
	}
	for _, cl := range cfg.Clients {
		tc.Peers = append(tc.Peers, relay.ConfiguredPeer{Name: cl.Name, MaxRate: uint64(max(cl.MaxTx, cl.MaxRx, 0))})
	}
	return tc
}

// NestedLimits: the nested-discovery limits configured in cfg. The
// HY2SCALE_MAX_NESTED_DEPTH environment variable overrides the depth.
func NestedLimits(cfg Config) relay.TreeLimits {
	l := relay.TreeLimits{
		MaxDepth:         cfg.MaxNestedDepth,
		MaxResponseNodes: cfg.MaxResponseNodes,
		MaxCacheEntries:  cfg.MaxCacheEntries,
		MaxResponseBytes: cfg.MaxResponseBytes,
		MaxFetchFanOut:   cfg.MaxFetchFanOut,
	}
	if e := strings.TrimSpace(os.Getenv("HY2SCALE_MAX_NESTED_DEPTH")); e != "" {
		if n, err := strconv.Atoi(e); err == nil && n >= 1 && n <= 10 {
			l.MaxDepth = n
		}
	}
	return l.Clamp()
}

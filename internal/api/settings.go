// Settings, by owner: the node (core: DNS, resolver, path-loss grace,
// nested limits, punching, management through the mesh), the web add-on
// (listen address, base path, HTTPS, sessions) and the public-address
// add-on.
// GET / PUT /api/settings/ui read and write all of them.

package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
)

func (s *Server) getUISettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	out := s.nodeSettings(cfg)
	for k, v := range s.webSettings(cfg) {
		out[k] = v
	}
	out["public_ip"] = publicIPSettings(cfg)
	writeJSON(w, out)
}

// webSettings: the web add-on's settings.
func (s *Server) webSettings(cfg app.Config) map[string]any {
	sessionH := cfg.SessionTimeoutH
	if sessionH == 0 {
		sessionH = 12
	}
	return map[string]any{
		"listen":    s.addr,
		"base_path": s.basePath,
		// https is always on
		"force_https":       true,
		"https_cert_id":     cfg.HTTPSCertID,
		"https_active":      true,
		"session_timeout_h": sessionH,
	}
}

// nodeSettings: the core's settings.
func (s *Server) nodeSettings(cfg app.Config) map[string]any {
	dns := cfg.DNS
	if dns == "" {
		dns = "8.8.8.8,1.1.1.1"
	}
	lim := s.app.Tree().Limits() // current runtime values (post-clamp + env override)
	// DNS resolver: raw stored values, so "unset = default" survives a save.
	dr := cfg.DNSResolver
	return map[string]any{
		"dns": dns,
		// quality/aggregate: seconds a connection survives with no path (effective value)
		"path_loss_grace_s": int(s.app.PathLossGrace() / time.Second),
		// Nested-discovery hard limits — hot-reloadable, no restart.
		"max_nested_depth":   lim.MaxDepth,
		"max_response_nodes": lim.MaxResponseNodes,
		"max_cache_entries":  lim.MaxCacheEntries,
		"max_response_bytes": lim.MaxResponseBytes,
		"max_fetch_fan_out":  lim.MaxFetchFanOut,
		"accept_punch":       cfg.AcceptPunch,
		// Off by default: management requests arriving through the mesh
		// still need a token.
		"relay_admin_passthrough": cfg.RelayAdminPassthrough,
		// The resolver's upstreams are `dns`; Enabled is the effective value
		// (unset = on).
		"dns_resolver_enabled":          dr.IsEnabled(),
		"dns_resolver_cache_ttl":        dr.CacheTTL,
		"dns_resolver_negative_ttl":     dr.NegativeTTL,
		"dns_resolver_cache_size":       dr.CacheSize,
		"dns_resolver_query_timeout_ms": dr.QueryTimeoutMs,
	}
}

func (s *Server) getNodeSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.nodeSettings(s.app.Store().Get()))
}

func (s *Server) getWebSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.webSettings(s.app.Store().Get()))
}

func (s *Server) getPublicIPSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, publicIPSettings(s.app.Store().Get()))
}

// settingsBody: every settings field (nil = unchanged).
type settingsBody struct {
	Listen                *string `json:"listen"`
	BasePath              *string `json:"base_path"`
	DNS                   *string `json:"dns"`
	ForceHTTPS            *bool   `json:"force_https"`
	HTTPSCertID           *string `json:"https_cert_id"`
	SessionTimeoutH       *int    `json:"session_timeout_h"`
	PathLossGraceS        *int    `json:"path_loss_grace_s"`
	MaxNestedDepth        *int    `json:"max_nested_depth"`
	MaxResponseNodes      *int    `json:"max_response_nodes"`
	MaxCacheEntries       *int    `json:"max_cache_entries"`
	MaxResponseBytes      *int    `json:"max_response_bytes"`
	MaxFetchFanOut        *int    `json:"max_fetch_fan_out"`
	RelayAdminPassthrough *bool   `json:"relay_admin_passthrough"`
	AcceptPunch           *bool   `json:"accept_punch"`
	// DNS resolver (relay-routed) — see app.DNSResolverConfig docs.
	// No separate upstream knob: the resolver uses the top-level
	// `dns` field above, same as VPN-client DNS push does.
	DNSResolverEnabled        *bool               `json:"dns_resolver_enabled"`
	DNSResolverCacheTTL       *int                `json:"dns_resolver_cache_ttl"`
	DNSResolverNegativeTTL    *int                `json:"dns_resolver_negative_ttl"`
	DNSResolverCacheSize      *int                `json:"dns_resolver_cache_size"`
	DNSResolverQueryTimeoutMs *int                `json:"dns_resolver_query_timeout_ms"`
	PublicIP                  *app.PublicIPConfig `json:"public_ip"`
}

// The owner of a settings write.
const (
	ownNode = 1 << iota
	ownWeb
	ownPublicIP
)

func (s *Server) updateUISettings(w http.ResponseWriter, r *http.Request) {
	s.putSettings(w, r, ownNode|ownWeb|ownPublicIP)
}

func (s *Server) updateNodeSettings(w http.ResponseWriter, r *http.Request) {
	s.putSettings(w, r, ownNode)
}

func (s *Server) updateWebSettings(w http.ResponseWriter, r *http.Request) {
	s.putSettings(w, r, ownWeb)
}

func (s *Server) updatePublicIPSettings(w http.ResponseWriter, r *http.Request) {
	var pc app.PublicIPConfig
	if err := json.NewDecoder(r.Body).Decode(&pc); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.app.Store().Update(func(c *app.Config) { applyPublicIPSettings(c, &pc) })
	writeJSON(w, map[string]string{"status": "ok", "note": "applied live"})
}

// putSettings applies the fields of the owners in own; others are ignored.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, own int) {
	var body settingsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if own&ownWeb == 0 {
		body.Listen, body.BasePath, body.ForceHTTPS, body.HTTPSCertID = nil, nil, nil, nil
		body.SessionTimeoutH = nil
	}
	if own&ownPublicIP == 0 {
		body.PublicIP = nil
	}
	if own&ownNode == 0 {
		body.DNS, body.PathLossGraceS, body.AcceptPunch, body.RelayAdminPassthrough = nil, nil, nil, nil
		body.MaxNestedDepth, body.MaxResponseNodes, body.MaxCacheEntries, body.MaxResponseBytes, body.MaxFetchFanOut = nil, nil, nil, nil, nil
		body.DNSResolverEnabled, body.DNSResolverCacheTTL, body.DNSResolverNegativeTTL = nil, nil, nil
		body.DNSResolverCacheSize, body.DNSResolverQueryTimeoutMs = nil, nil
	}
	nestedTouched := body.MaxNestedDepth != nil || body.MaxResponseNodes != nil ||
		body.MaxCacheEntries != nil || body.MaxResponseBytes != nil || body.MaxFetchFanOut != nil
	s.app.Store().Update(func(c *app.Config) {
		if body.Listen != nil {
			c.UIListen = *body.Listen
		}
		if body.BasePath != nil {
			c.UIBasePath = *body.BasePath
		}
		if body.DNS != nil {
			c.DNS = *body.DNS
		}
		// force_https is not settable: https is always on.
		if body.HTTPSCertID != nil {
			c.HTTPSCertID = *body.HTTPSCertID
		}
		if body.SessionTimeoutH != nil {
			c.SessionTimeoutH = *body.SessionTimeoutH
		}
		if body.PathLossGraceS != nil {
			// read per new session: effective immediately, no restart
			c.PathLossGraceS = *body.PathLossGraceS
		}
		if body.MaxNestedDepth != nil {
			c.MaxNestedDepth = *body.MaxNestedDepth
		}
		if body.MaxResponseNodes != nil {
			c.MaxResponseNodes = *body.MaxResponseNodes
		}
		if body.MaxCacheEntries != nil {
			c.MaxCacheEntries = *body.MaxCacheEntries
		}
		if body.MaxResponseBytes != nil {
			c.MaxResponseBytes = *body.MaxResponseBytes
		}
		if body.MaxFetchFanOut != nil {
			c.MaxFetchFanOut = *body.MaxFetchFanOut
		}
		if body.RelayAdminPassthrough != nil {
			c.RelayAdminPassthrough = *body.RelayAdminPassthrough
		}
		if body.AcceptPunch != nil {
			c.AcceptPunch = *body.AcceptPunch
		}
		if body.DNSResolverEnabled != nil {
			// Store as *bool so a future GET keeps round-tripping the
			// explicit choice; never collapses back to "unset → default".
			v := *body.DNSResolverEnabled
			c.DNSResolver.Enabled = &v
		}
		if body.DNSResolverCacheTTL != nil {
			c.DNSResolver.CacheTTL = *body.DNSResolverCacheTTL
		}
		if body.DNSResolverNegativeTTL != nil {
			c.DNSResolver.NegativeTTL = *body.DNSResolverNegativeTTL
		}
		if body.DNSResolverCacheSize != nil {
			c.DNSResolver.CacheSize = *body.DNSResolverCacheSize
		}
		if body.DNSResolverQueryTimeoutMs != nil {
			c.DNSResolver.QueryTimeoutMs = *body.DNSResolverQueryTimeoutMs
		}
		if body.PublicIP != nil {
			applyPublicIPSettings(c, body.PublicIP)
		}
	})
	// Hot-reload resolver snapshot on any DNS-resolver field touch +
	// purge cache so the new config takes effect on next call.
	// `body.DNS` is included because the resolver now reads its
	// upstream list from cfg.DNS — a DNS field save invalidates
	// cached `<exit>/<name>` entries that may have answered via the
	// old upstream.
	resolverTouched := body.DNSResolverEnabled != nil ||
		body.DNSResolverCacheTTL != nil || body.DNSResolverNegativeTTL != nil ||
		body.DNSResolverCacheSize != nil || body.DNSResolverQueryTimeoutMs != nil ||
		body.DNS != nil
	if resolverTouched {
		app.ApplyDNSResolver(s.app.Store().Get().DNSResolver)
	}
	// Nested limits apply from the next refresh, without a restart.
	if nestedTouched {
		s.app.Tree().SetLimits(app.NestedLimits(s.app.Store().Get()))
	}
	// Only listen/base_path actually need a restart — they're consumed at
	// http.ListenAndServe binding time. DNS is read on every ikev2/l2tp
	// handshake (a.store.Get().DNS), so DNS changes affect new VPN
	// connections immediately. force_https / https_cert_id are read per
	// connection (webTLS), so they apply live too.
	listenChanged := body.Listen != nil || body.BasePath != nil
	note := "applied live"
	if listenChanged {
		note = "listen / base_path require restart to take effect"
	}
	writeJSON(w, map[string]string{"status": "ok", "note": note})
}

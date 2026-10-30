// Settings: network, DNS and nested limits (core), listen address and base path (web).

package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
)

func (s *Server) getUISettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	dns := cfg.DNS
	if dns == "" {
		dns = "8.8.8.8,1.1.1.1"
	}
	sessionH := cfg.SessionTimeoutH
	if sessionH == 0 {
		sessionH = 12
	}
	lim := s.app.Tree().Limits() // current runtime values (post-clamp + env override)
	// DNS resolver: surface raw stored values so the UI's "unset = inherit
	// default" semantic survives a save (an operator who never touched the
	// section keeps zeros in config, sees defaults rendered as placeholder).
	dr := cfg.DNSResolver
	writeJSON(w, map[string]any{
		"listen":        s.addr,
		"base_path":     s.basePath,
		"dns":           dns,
		"force_https":   cfg.ForceHTTPS,
		"https_cert_id": cfg.HTTPSCertID,
		// plain http is being redirected right now (force on + usable cert)
		"https_active":      s.web != nil && s.web.forced(),
		"session_timeout_h": sessionH,
		// quality/aggregate: seconds a connection survives with no path (effective value)
		"path_loss_grace_s": int(s.app.PathLossGrace() / time.Second),
		// Nested-discovery hard limits — hot-reloadable, no restart.
		"max_nested_depth":   lim.MaxDepth,
		"max_response_nodes": lim.MaxResponseNodes,
		"max_cache_entries":  lim.MaxCacheEntries,
		"max_response_bytes": lim.MaxResponseBytes,
		"max_fetch_fan_out":  lim.MaxFetchFanOut,
		// Off by default: relay-delivered admin requests still need a token.
		"relay_admin_passthrough": cfg.RelayAdminPassthrough,
		"accept_punch":            cfg.AcceptPunch,
		// DNS resolver (relay-routed). Defaults baked into app.dnsResolverDefaults.
		// Upstream resolver list reuses the top-level `dns` field above —
		// no separate knob. Enabled exposes the *effective* value: nil
		// pointer in config defaults to true so fresh installs and
		// 1.3.3 → 1.3.4 upgrades both ship with the resolver on.
		"dns_resolver_enabled":          dr.IsEnabled(),
		"dns_resolver_cache_ttl":        dr.CacheTTL,
		"dns_resolver_negative_ttl":     dr.NegativeTTL,
		"dns_resolver_cache_size":       dr.CacheSize,
		"dns_resolver_query_timeout_ms": dr.QueryTimeoutMs,
		"public_ip":                     publicIPSettings(cfg),
	})
}

func (s *Server) updateUISettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
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
		if body.ForceHTTPS != nil {
			c.ForceHTTPS = *body.ForceHTTPS
		}
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

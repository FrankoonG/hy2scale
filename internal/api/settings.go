// Settings, by owner: the node (core: DNS, resolver, path-loss grace,
// nested limits, punching, management through the mesh), the web add-on
// (listen address, base path, HTTPS, sessions) and the public-address
// add-on.
// GET / PUT /api/settings/ui read and write all of them.

package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
	"github.com/FrankoonG/hy2scale/internal/mdns"
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
		// subscriptions served here (subscription.go)
		"sub_no_relay": cfg.Subscription != nil && cfg.Subscription.NoRelay,
		"sub_http":     cfg.Subscription != nil && cfg.Subscription.HTTP,
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
	mdnsClaimed, mdnsState := s.app.MDNSStatus()
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
		"accept_shared_users": cfg.AcceptSharedUsers,
		// automatic update (autoupdate.go)
		"auto_update_enabled":    cfg.AutoUpdate.Enabled,
		"auto_update_time":       cfg.AutoUpdate.UpdateTime(),
		"auto_update_from_peers": cfg.AutoUpdate.FromPeers,
		"auto_update_supported":  canSelfUpgrade(),
		"auto_update_last":       s.autoUpdateLast(),
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
		// the host links of this node carry, and the LAN name (subscription.go, mdns.go)
		"public_host":          cfg.PublicHost,
		"public_host_detected": s.app.DetectedPublicHost(),
		"mdns_enabled":         cfg.MDNS == nil || !cfg.MDNS.Disabled,
		"mdns_name":            mdnsName(cfg),
		"mdns_label":           s.app.MDNSLabel(),
		"mdns_claimed":         mdnsClaimed,
		"mdns_state":           mdnsState,
	}
}

func mdnsName(cfg app.Config) string {
	if cfg.MDNS == nil {
		return ""
	}
	return cfg.MDNS.Name
}

// validPublicHost: a host name or an IP address (no scheme, port or path).
func validPublicHost(h string) bool {
	if h == "" {
		return true
	}
	if net.ParseIP(strings.Trim(h, "[]")) != nil {
		return true
	}
	if len(h) > 253 {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return !strings.HasPrefix(h, ".") && !strings.HasPrefix(h, "-")
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
	AcceptSharedUsers     *bool   `json:"accept_shared_users"`
	AutoUpdateEnabled     *bool   `json:"auto_update_enabled"`
	AutoUpdateTime        *string `json:"auto_update_time"`
	AutoUpdateFromPeers   *bool   `json:"auto_update_from_peers"`
	// DNS resolver (relay-routed) — see app.DNSResolverConfig docs.
	// No separate upstream knob: the resolver uses the top-level
	// `dns` field above, same as VPN-client DNS push does.
	DNSResolverEnabled        *bool               `json:"dns_resolver_enabled"`
	DNSResolverCacheTTL       *int                `json:"dns_resolver_cache_ttl"`
	DNSResolverNegativeTTL    *int                `json:"dns_resolver_negative_ttl"`
	DNSResolverCacheSize      *int                `json:"dns_resolver_cache_size"`
	DNSResolverQueryTimeoutMs *int                `json:"dns_resolver_query_timeout_ms"`
	PublicIP                  *app.PublicIPConfig `json:"public_ip"`
	PublicHost                *string             `json:"public_host"`
	SubNoRelay                *bool               `json:"sub_no_relay"`
	SubHTTP                   *bool               `json:"sub_http"`
	MDNSEnabled               *bool               `json:"mdns_enabled"`
	MDNSName                  *string             `json:"mdns_name"`
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
	s.app.PublicIPSettingsChanged()
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
		body.SubNoRelay, body.SubHTTP = nil, nil
	}
	if own&ownPublicIP == 0 {
		body.PublicIP = nil
	}
	if own&ownNode == 0 {
		body.DNS, body.PathLossGraceS, body.AcceptPunch, body.RelayAdminPassthrough = nil, nil, nil, nil
		body.AcceptSharedUsers = nil
		body.AutoUpdateEnabled, body.AutoUpdateTime, body.AutoUpdateFromPeers = nil, nil, nil
		body.MaxNestedDepth, body.MaxResponseNodes, body.MaxCacheEntries, body.MaxResponseBytes, body.MaxFetchFanOut = nil, nil, nil, nil, nil
		body.DNSResolverEnabled, body.DNSResolverCacheTTL, body.DNSResolverNegativeTTL = nil, nil, nil
		body.DNSResolverCacheSize, body.DNSResolverQueryTimeoutMs = nil, nil
		body.PublicHost, body.MDNSEnabled, body.MDNSName = nil, nil, nil
	}
	if body.PublicHost != nil {
		h := strings.TrimSpace(*body.PublicHost)
		if !validPublicHost(h) {
			http.Error(w, "public host: a host name or an IP address, without a port", 400)
			return
		}
		body.PublicHost = &h
	}
	if body.MDNSName != nil {
		n := strings.TrimSpace(*body.MDNSName)
		if n != "" && mdns.SanitizeLabel(n) != strings.ToLower(n) {
			http.Error(w, "LAN name: letters, digits and '-' only", 400)
			return
		}
		body.MDNSName = &n
	}
	if body.AutoUpdateTime != nil {
		if _, err := time.Parse("15:04", *body.AutoUpdateTime); err != nil || len(*body.AutoUpdateTime) != 5 {
			http.Error(w, "auto_update_time must be HH:MM", 400)
			return
		}
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
		if body.AcceptSharedUsers != nil {
			c.AcceptSharedUsers = *body.AcceptSharedUsers
		}
		if body.AutoUpdateEnabled != nil {
			c.AutoUpdate.Enabled = *body.AutoUpdateEnabled
		}
		if body.AutoUpdateTime != nil {
			c.AutoUpdate.Time = *body.AutoUpdateTime
		}
		if body.AutoUpdateFromPeers != nil {
			c.AutoUpdate.FromPeers = *body.AutoUpdateFromPeers
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
		if body.PublicHost != nil {
			c.PublicHost = *body.PublicHost
		}
		if body.SubNoRelay != nil || body.SubHTTP != nil {
			if c.Subscription == nil {
				c.Subscription = &app.SubscriptionConfig{}
			}
			if body.SubNoRelay != nil {
				c.Subscription.NoRelay = *body.SubNoRelay
			}
			if body.SubHTTP != nil {
				c.Subscription.HTTP = *body.SubHTTP
			}
		}
		if body.MDNSEnabled != nil || body.MDNSName != nil {
			if c.MDNS == nil {
				c.MDNS = &app.MDNSConfig{}
			}
			if body.MDNSEnabled != nil {
				c.MDNS.Disabled = !*body.MDNSEnabled
			}
			if body.MDNSName != nil {
				c.MDNS.Name = *body.MDNSName
			}
		}
		if body.PublicIP != nil {
			applyPublicIPSettings(c, body.PublicIP)
		}
	})
	if body.PublicIP != nil {
		s.app.PublicIPSettingsChanged()
	}
	if body.AcceptSharedUsers != nil {
		s.app.ApplySharedAttr()
	}
	if body.MDNSEnabled != nil || body.MDNSName != nil {
		go s.app.ApplyMDNS()
	}
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

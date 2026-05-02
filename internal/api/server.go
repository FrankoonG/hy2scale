package api

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"compress/gzip"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
	"github.com/FrankoonG/hy2scale/internal/relay"
	"github.com/FrankoonG/hy2scale/internal/web"
	qrcode "github.com/skip2/go-qrcode"
)

type topoSubPeer struct {
	Name       string         `json:"name"`
	ExitNode   bool           `json:"exit_node"`
	Direction  string         `json:"direction"`
	Via        string         `json:"via"`
	LatencyMs  int            `json:"latency_ms"`
	Nested     bool           `json:"nested"`
	Disabled   bool           `json:"disabled,omitempty"`
	Native     bool           `json:"native,omitempty"`
	Version    string         `json:"version,omitempty"`
	TunCapable bool           `json:"tun_capable,omitempty"`
	TxRate     uint64         `json:"tx_rate"`
	RxRate     uint64         `json:"rx_rate"`
	// MaxRate is the configured peak the link can sustain (bytes/sec),
	// derived from `max(ClientEntry.MaxTx, ClientEntry.MaxRx)` of the
	// dialer side. The graph uses it as the reference for edge
	// thickness — a known-bandwidth link draws at its calibrated width
	// immediately on first load instead of having to observe traffic
	// to populate a peak. 0 = unknown → falls back to observed peak
	// on the frontend.
	MaxRate    uint64         `json:"max_rate,omitempty"`
	// Connected mirrors the direct-peer Connected flag for nested
	// sub-peers. A sub-peer that the parent has configured but is
	// currently offline is propagated with Connected=false so the
	// graph stays visually complete (red edge, but the node is still
	// drawn). Old peers that don't send this field default to
	// Connected=true on the consumer side for backward compat.
	Connected  bool           `json:"connected"`
	Children   []topoSubPeer  `json:"children,omitempty"`
}

type Server struct {
	app      *app.App
	addr     string
	basePath string

	mu         sync.RWMutex
	username   string
	passHash   string // SHA-256 hex of password
	sessions   map[string]time.Time
	oldNodeIDs map[string]string

	// Cached nested sub-peers (updated asynchronously)
	subPeersMu    sync.RWMutex
	subPeersCache map[string][]topoSubPeer
	subPeersKick  chan struct{} // signal immediate refresh

	// Graph-layout SSE subscribers. Each entry is a buffered channel
	// receiving full-snapshot updates; slow consumers drop messages
	// rather than blocking the PUT handler. Protected by layoutMu.
	layoutMu     sync.Mutex
	layoutSubs   map[uint64]chan map[string]app.GraphLayoutPos
	layoutSubSeq uint64

	// Build ID derived from the embedded index.html hash. Frontend polls
	// /api/build-id and reloads on mismatch so a server rebuild doesn't
	// leave long-lived tabs running stale code.
	buildID string

	// Online-update singleton. Shared across all sessions so that two
	// browser tabs see the same download progress instead of triggering
	// duplicate downloads. See internal/api/upgrade.go.
	upgrade *upgradeJob
}

// validAddrSpec checks "host:portspec" where portspec can be "5565", "1000,2000", "20000-30000"
func validAddrSpec(addr string) bool {
	idx := strings.LastIndex(addr, ":")
	if idx <= 0 || idx == len(addr)-1 {
		return false
	}
	portSpec := addr[idx+1:]
	for _, part := range strings.Split(portSpec, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			rng := strings.SplitN(part, "-", 2)
			a, err1 := strconv.Atoi(strings.TrimSpace(rng[0]))
			b, err2 := strconv.Atoi(strings.TrimSpace(rng[1]))
			if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
				return false
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 || n > 65535 {
				return false
			}
		}
	}
	return true
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func NewServer(a *app.App, addr, basePath string) *Server {
	bp := strings.TrimRight(basePath, "/")
	if bp != "" && !strings.HasPrefix(bp, "/") {
		bp = "/" + bp
	}
	cfg := a.GetConfig()
	username := cfg.WebUsername
	passHash := cfg.WebPassword // stored as SHA-256 hex
	if username == "" {
		username = "admin"
	}
	if passHash == "" {
		passHash = sha256Hex("admin")
	}
	// Install runtime nested limits from cfg + env override; subsequent
	// updateUISettings calls re-apply on every change for hot-reload.
	applyNestedLimits(cfg)
	// Same pattern for the DNS resolver — install at startup so the
	// next ResolveViaExit call sees the operator-configured snapshot.
	app.ApplyDNSResolver(cfg.DNSResolver)
	return &Server{
		app:        a,
		addr:       addr,
		basePath:   bp,
		username:   username,
		passHash:   passHash,
		oldNodeIDs:    make(map[string]string),
		subPeersCache: make(map[string][]topoSubPeer),
		subPeersKick:  make(chan struct{}, 1),
		layoutSubs:    make(map[uint64]chan map[string]app.GraphLayoutPos),
		sessions: make(map[string]time.Time),
		upgrade:  newUpgradeJob(),
	}
}

func (s *Server) Start(ctx context.Context) error {
	apiMux := http.NewServeMux()

	// Login (no auth)
	apiMux.HandleFunc("POST /api/login", s.login)
	// Internal peer list (no auth, used for reverse nested discovery)
	apiMux.HandleFunc("GET /api/internal/peers", s.internalPeers)
	// Build-id probe (no auth) — a long-lived SPA tab polls this to detect
	// server rebuild/redeploy and reload itself. Cheap and stateless.
	apiMux.HandleFunc("GET /api/build-id", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		writeJSON(w, map[string]string{"build_id": s.buildID})
	})

	// Authed API routes
	authed := http.NewServeMux()
	authed.HandleFunc("GET /api/node", s.getNode)
	authed.HandleFunc("GET /api/stats", s.getStats)
	authed.HandleFunc("PUT /api/node", s.updateNode)
	authed.HandleFunc("GET /api/peers", s.getPeers)
	authed.HandleFunc("GET /api/topology", s.getTopology)
	authed.HandleFunc("GET /api/graph-layout", s.getGraphLayout)
	authed.HandleFunc("PUT /api/graph-layout", s.setGraphLayout)
	authed.HandleFunc("GET /api/graph-layout/stream", s.streamGraphLayout)
	authed.HandleFunc("GET /api/peers/{name}/peers", s.getNestedPeers)
	authed.HandleFunc("PUT /api/peers/{name}/nested", s.setNested)
	authed.HandleFunc("PUT /api/peers/{name}/disable", s.setPeerDisabled)
	authed.HandleFunc("GET /api/clients", s.getClients)
	authed.HandleFunc("GET /api/clients/{name}", s.getClient)
	authed.HandleFunc("POST /api/clients", s.addClient)
	authed.HandleFunc("PUT /api/clients/{name}", s.updateClient)
	authed.HandleFunc("PUT /api/clients/{name}/disable", s.disableClient)
	authed.HandleFunc("DELETE /api/clients/{name}", s.removeClient)
	authed.HandleFunc("GET /api/proxies", s.getProxies)
	authed.HandleFunc("POST /api/proxies", s.addProxy)
	authed.HandleFunc("PUT /api/proxies/{id}", s.updateProxy)
	authed.HandleFunc("DELETE /api/proxies/{id}", s.removeProxy)
	// SS config
	authed.HandleFunc("GET /api/ss", s.getSSConfig)
	authed.HandleFunc("PUT /api/ss", s.updateSSConfig)

	// L2TP config
	authed.HandleFunc("GET /api/l2tp", s.getL2TPConfig)
	authed.HandleFunc("PUT /api/l2tp", s.updateL2TPConfig)

	// IKEv2 config
	authed.HandleFunc("GET /api/ikev2", s.getIKEv2Config)
	authed.HandleFunc("PUT /api/ikev2", s.updateIKEv2Config)

	// WireGuard
	authed.HandleFunc("GET /api/wireguard", s.getWireGuardConfig)
	authed.HandleFunc("PUT /api/wireguard", s.updateWireGuardConfig)
	authed.HandleFunc("POST /api/wireguard/generate-key", s.generateWGKey)
	authed.HandleFunc("POST /api/wireguard/peers", s.addWGPeer)
	authed.HandleFunc("PUT /api/wireguard/peers/{name}", s.updateWGPeer)
	authed.HandleFunc("DELETE /api/wireguard/peers/{name}", s.removeWGPeer)
	authed.HandleFunc("GET /api/wireguard/peers/{name}/config", s.downloadWGPeerConfig)
	authed.HandleFunc("GET /api/wireguard/qr", s.wireGuardQR)

	// Rules
	authed.HandleFunc("GET /api/rules", s.getRules)
	authed.HandleFunc("POST /api/rules", s.addRule)
	authed.HandleFunc("PUT /api/rules/{id}", s.updateRule)
	authed.HandleFunc("DELETE /api/rules/{id}", s.deleteRule)
	authed.HandleFunc("PUT /api/rules/{id}/toggle", s.toggleRule)

	// Port check
	authed.HandleFunc("POST /api/check-ports", s.checkPorts)

	// DNS resolver diagnostic — sanity-check that ResolveViaExit returns
	// the expected IP through the configured upstream/exit. Used by ops
	// to verify the relay-routed DNS is healthy without needing to add
	// a domain rule and watch logs.
	//   GET /api/diag/resolve?name=example.com&exit_via=us
	authed.HandleFunc("GET /api/diag/resolve", s.diagResolve)

	// Sessions (active connections)
	authed.HandleFunc("GET /api/sessions", s.getSessions)
	authed.HandleFunc("DELETE /api/sessions/{id}", s.kickSession)

	// Users
	authed.HandleFunc("GET /api/users", s.getUsers)
	authed.HandleFunc("POST /api/users", s.addUserAPI)
	authed.HandleFunc("PUT /api/users/{id}", s.updateUserAPI)
	authed.HandleFunc("DELETE /api/users/{id}", s.removeUserAPI)
	authed.HandleFunc("PUT /api/users/{id}/toggle", s.toggleUserAPI)
	authed.HandleFunc("GET /api/users/conflicts", s.getUserConflicts)
	authed.HandleFunc("PUT /api/users/{id}/reset-traffic", s.resetUserTrafficAPI)

	authed.HandleFunc("PUT /api/settings/password", s.changePassword)

	// Backup / Restore
	authed.HandleFunc("GET /api/backup", s.downloadBackup)
	authed.HandleFunc("POST /api/restore", s.uploadRestore)

	// Upgrade
	authed.HandleFunc("GET /api/system/arch", s.getSystemArch)
	authed.HandleFunc("GET /api/build-info", s.getBuildInfo)
	authed.HandleFunc("POST /api/upgrade", s.uploadUpgrade)
	// Online-update endpoints. /check polls GitHub for the latest release;
	// /download starts a server-side download whose progress every session
	// observes via /events (SSE); /apply swaps in the downloaded binary.
	authed.HandleFunc("GET /api/upgrade/check", s.checkUpdate)
	authed.HandleFunc("POST /api/upgrade/download", s.startDownload)
	authed.HandleFunc("GET /api/upgrade/status", s.getUpgradeStatus)
	authed.HandleFunc("GET /api/upgrade/events", s.streamUpgradeEvents)
	authed.HandleFunc("POST /api/upgrade/apply", s.applyDownloadedUpgrade)
	authed.HandleFunc("GET /api/settings/ui", s.getUISettings)
	authed.HandleFunc("PUT /api/settings/ui", s.updateUISettings)

	// TLS
	authed.HandleFunc("GET /api/tls", s.listCerts)
	authed.HandleFunc("POST /api/tls/import", s.importCert)
	authed.HandleFunc("POST /api/tls/import-path", s.importCertFromPath)
	authed.HandleFunc("POST /api/tls/generate", s.generateCert)
	authed.HandleFunc("POST /api/tls/sign", s.signCertWithCA)
	authed.HandleFunc("GET /api/tls/{id}/pem", s.getCertPEM)
	authed.HandleFunc("DELETE /api/tls/{id}", s.deleteCert)

	apiMux.Handle("/api/", s.authMiddleware(authed))

	// Static files with SPA fallback — inject basePath into index.html.
	// A build-id derived from the sha256 of the raw index.html is also
	// embedded on the page and exposed at /api/build-id so long-lived
	// SPA sessions detect when they're running stale code after a
	// server rebuild/redeploy and can reload themselves.
	staticFS, _ := fs.Sub(web.Static, "static")
	rawIndex, _ := fs.ReadFile(staticFS, "index.html")
	buildSum := sha256.Sum256(rawIndex)
	buildID := hex.EncodeToString(buildSum[:8])
	s.buildID = buildID
	injected := `<script>window.__BASE__="` + s.basePath + `";window.__BUILD_ID__="` + buildID + `";</script>`
	indexHTML := strings.Replace(string(rawIndex), "<head>", "<head>"+injected, 1)
	indexBytes := []byte(indexHTML)

	// Known frontend routes that should serve index.html
	frontendRoutes := map[string]bool{
		"":         true,
		"login":    true,
		"nodes":    true,
		"proxies":  true,
		"users":    true,
		"tls":      true,
		"rules":    true,
		"settings": true,
	}

	// Remote node proxy — no local auth, remote handles its own
	// Block if accessed through proxy (prevent proxy chaining)
	apiMux.HandleFunc("/remote/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hy2scale-Proxy") == "true" {
			http.Error(w, "proxy chaining not allowed", 403)
			return
		}
		s.remoteProxy(w, r)
	})

	apiMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")

		if path != "" {
			if data, err := fs.ReadFile(staticFS, path); err == nil {
				switch {
				case strings.HasSuffix(path, ".css"):
					w.Header().Set("Content-Type", "text/css")
				case strings.HasSuffix(path, ".js"):
					w.Header().Set("Content-Type", "application/javascript")
				case strings.HasSuffix(path, ".html"):
					w.Header().Set("Content-Type", "text/html")
				case strings.HasSuffix(path, ".svg"):
					w.Header().Set("Content-Type", "image/svg+xml")
				case strings.HasSuffix(path, ".png"):
					w.Header().Set("Content-Type", "image/png")
				case strings.HasSuffix(path, ".json"):
					w.Header().Set("Content-Type", "application/json")
				}
				w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				w.Write(data)
				return
			}
		}

		if frontendRoutes[path] {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Write(indexBytes)
			return
		}

		http.Redirect(w, r, s.basePath+"/login", http.StatusFound)
	})

	// Root mux — strip base path
	root := http.NewServeMux()
	if s.basePath != "" {
		root.Handle(s.basePath+"/", http.StripPrefix(s.basePath, apiMux))
		// Redirect bare path to path with trailing slash
		root.HandleFunc(s.basePath, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, s.basePath+"/", http.StatusMovedPermanently)
		})
	} else {
		root.Handle("/", apiMux)
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: root}
	go func() { <-ctx.Done(); srv.Close() }()
	if s.basePath != "" {
		log.Printf("API/UI on %s%s", s.addr, s.basePath)
	} else {
		log.Printf("API/UI on %s", s.addr)
	}

	// Relay incoming remote-proxy streams (_relay_api_) to the same HTTP
	// handler by bridging the stream into an in-process listener. The
	// bridge's http.Server is separate from the public TCP one so its
	// handler chain can tag each request with the relay-delivered marker
	// — authMiddleware then applies RelayAdminPassthrough only to those.
	apiBridge := newStreamListener()
	s.app.Node().SetAPIHandler(func(stream net.Conn) {
		apiBridge.push(stream)
	})
	relaySrv := &http.Server{
		Handler:     root,
		ConnContext: relayConnContext,
	}
	go func() { <-ctx.Done(); relaySrv.Close() }()
	go relaySrv.Serve(apiBridge)

	return srv.Serve(ln)
}

// relayCtxKey carries the hyserver authID of the QUIC connection that
// originated a request delivered through apiBridge ("" for non-relay TCP
// requests). Unexported struct type so external packages can neither synthesize
// the key nor inject a forged value via context.WithValue. The api server's
// relaySrv populates it via ConnContext from the *app.RelayAuthConn wrapping
// each pushed stream; authMiddleware reads it back to gate the
// RelayAdminPassthrough bypass on authID == "system".
type relayCtxKey struct{}

// relayAuthCarrier is the small interface satisfied by *app.RelayAuthConn —
// duck-typed here so this package doesn't have to import app/ for what is
// purely a metadata read.
type relayAuthCarrier interface {
	RelayAuthID() string
}

func relayConnContext(ctx context.Context, c net.Conn) context.Context {
	if rc, ok := c.(relayAuthCarrier); ok {
		return context.WithValue(ctx, relayCtxKey{}, rc.RelayAuthID())
	}
	return ctx
}

// relayAuthID returns the hyserver authID for the current request, or ""
// if the request didn't arrive via apiBridge (or arrived without an
// auth-tagged conn). authMiddleware uses this to decide whether to apply
// RelayAdminPassthrough.
func relayAuthID(ctx context.Context) string {
	v, _ := ctx.Value(relayCtxKey{}).(string)
	return v
}

// streamListener is a net.Listener backed by a channel; each Accept returns a
// net.Conn that was fed via push(). Used so relay-delivered API streams can
// be served by the same *http.Server without a separate TCP socket.
type streamListener struct {
	ch     chan net.Conn
	closed chan struct{}
}

func newStreamListener() *streamListener {
	return &streamListener{ch: make(chan net.Conn, 16), closed: make(chan struct{})}
}

func (l *streamListener) push(c net.Conn) {
	select {
	case l.ch <- c:
	case <-l.closed:
		c.Close()
	default:
		// channel full — drop
		c.Close()
	}
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, fmt.Errorf("listener closed")
	}
}

func (l *streamListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *streamListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4zero, Port: 0}
}

// --- Auth ---

func (s *Server) generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Client sends SHA-256(password) hex; compare against stored hash
	s.mu.RLock()
	ok := subtle.ConstantTimeCompare([]byte(body.Username), []byte(s.username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.passHash)) == 1
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "invalid credentials", 401)
		return
	}
	token := s.generateToken()
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(s.sessionTimeout())
	s.mu.Unlock()
	// Check if password is still the default (admin)
	forceChange := body.Password == sha256Hex("admin")
	writeJSON(w, map[string]any{"token": token, "force_password_change": forceChange})
}

func (s *Server) sessionTimeout() time.Duration {
	cfg := s.app.Store().Get()
	if cfg.SessionTimeoutH > 0 {
		return time.Duration(cfg.SessionTimeoutH) * time.Hour
	}
	return 12 * time.Hour // default 12h
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RelayAdminPassthrough lets requests delivered through the
		// peer-to-peer relay bridge skip the local token check — but
		// ONLY when the underlying QUIC connection authenticated with
		// the system password (auth ID "system"). User-level relay
		// traffic (auth ID "user:<name>") falls through to the normal
		// auth path, which 401s the API or SPA-redirects to /login —
		// so a regular VPN user can never escalate to admin via
		// `_relay_api_:0` even with the toggle on.
		if relayAuthID(r.Context()) == "system" && s.app.Store().Get().RelayAdminPassthrough {
			next.ServeHTTP(w, r)
			return
		}
		token := r.Header.Get("Authorization")
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}
		// EventSource and similar SSE clients can't set Authorization
		// headers, so accept an equivalent ?token= query parameter as a
		// fallback. Only the exact token value leaks into the URL, which
		// is the same secret as the Bearer header.
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		s.mu.RLock()
		expiry, ok := s.sessions[token]
		s.mu.RUnlock()
		if !ok || time.Now().After(expiry) {
			http.Error(w, "unauthorized", 401)
			return
		}
		// Refresh session on activity
		s.mu.Lock()
		s.sessions[token] = time.Now().Add(s.sessionTimeout())
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewUsername     string `json:"new_username"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// All password fields are SHA-256 hex from client
	s.mu.Lock()
	defer s.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(body.CurrentPassword), []byte(s.passHash)) != 1 {
		http.Error(w, "current password incorrect", 403)
		return
	}
	if body.NewUsername != "" {
		s.username = body.NewUsername
	}
	if body.NewPassword != "" {
		s.passHash = body.NewPassword
	}
	// Persist credentials to config (stored as SHA-256 hex)
	s.app.UpdateWebCredentials(s.username, s.passHash)
	s.sessions = make(map[string]time.Time)
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- UI Settings (port, base path) ---

func (s *Server) getUISettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	dns := cfg.DNS
	if dns == "" { dns = "8.8.8.8,1.1.1.1" }
	sessionH := cfg.SessionTimeoutH
	if sessionH == 0 { sessionH = 12 }
	lim := getNestedLimits() // current runtime values (post-clamp + env override)
	// DNS resolver: surface raw stored values so the UI's "unset = inherit
	// default" semantic survives a save (an operator who never touched the
	// section keeps zeros in config, sees defaults rendered as placeholder).
	dr := cfg.DNSResolver
	writeJSON(w, map[string]any{
		"listen":             s.addr,
		"base_path":          s.basePath,
		"dns":                dns,
		"force_https":        cfg.ForceHTTPS,
		"https_cert_id":      cfg.HTTPSCertID,
		"session_timeout_h":  sessionH,
		// Nested-discovery hard limits — hot-reloadable, no restart.
		"max_nested_depth":   lim.MaxDepth,
		"max_response_nodes": lim.MaxResponseNodes,
		"max_cache_entries":  lim.MaxCacheEntries,
		"max_response_bytes": lim.MaxResponseBytes,
		"max_fetch_fan_out":  lim.MaxFetchFanOut,
		// Off by default: relay-delivered admin requests still need a token.
		"relay_admin_passthrough": cfg.RelayAdminPassthrough,
		// DNS resolver (relay-routed). Defaults baked into app.dnsResolverDefaults.
		"dns_resolver_enabled":          dr.Enabled,
		"dns_resolver_upstream":         dr.Upstream,
		"dns_resolver_cache_ttl":        dr.CacheTTL,
		"dns_resolver_negative_ttl":     dr.NegativeTTL,
		"dns_resolver_cache_size":       dr.CacheSize,
		"dns_resolver_query_timeout_ms": dr.QueryTimeoutMs,
	})
}

func (s *Server) updateUISettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Listen           *string `json:"listen"`
		BasePath         *string `json:"base_path"`
		DNS              *string `json:"dns"`
		ForceHTTPS       *bool   `json:"force_https"`
		HTTPSCertID      *string `json:"https_cert_id"`
		SessionTimeoutH  *int    `json:"session_timeout_h"`
		MaxNestedDepth   *int    `json:"max_nested_depth"`
		MaxResponseNodes *int    `json:"max_response_nodes"`
		MaxCacheEntries  *int    `json:"max_cache_entries"`
		MaxResponseBytes *int    `json:"max_response_bytes"`
		MaxFetchFanOut   *int    `json:"max_fetch_fan_out"`
		RelayAdminPassthrough *bool `json:"relay_admin_passthrough"`
		// DNS resolver (relay-routed) — see app.DNSResolverConfig docs.
		DNSResolverEnabled        *bool   `json:"dns_resolver_enabled"`
		DNSResolverUpstream       *string `json:"dns_resolver_upstream"`
		DNSResolverCacheTTL       *int    `json:"dns_resolver_cache_ttl"`
		DNSResolverNegativeTTL    *int    `json:"dns_resolver_negative_ttl"`
		DNSResolverCacheSize      *int    `json:"dns_resolver_cache_size"`
		DNSResolverQueryTimeoutMs *int    `json:"dns_resolver_query_timeout_ms"`
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
		if body.DNSResolverEnabled != nil {
			c.DNSResolver.Enabled = *body.DNSResolverEnabled
		}
		if body.DNSResolverUpstream != nil {
			c.DNSResolver.Upstream = *body.DNSResolverUpstream
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
	})
	// Hot-reload resolver snapshot on any DNS-resolver field touch +
	// purge cache so the new upstream takes effect on next call.
	resolverTouched := body.DNSResolverEnabled != nil || body.DNSResolverUpstream != nil ||
		body.DNSResolverCacheTTL != nil || body.DNSResolverNegativeTTL != nil ||
		body.DNSResolverCacheSize != nil || body.DNSResolverQueryTimeoutMs != nil
	if resolverTouched {
		app.ApplyDNSResolver(s.app.Store().Get().DNSResolver)
	}
	// Hot-reload the runtime atomic pointer if any nested limit changed —
	// next walkAndCache / assembleDeepTree / fetchSubPeersViaStream call
	// picks up the new clamped values without a restart.
	if nestedTouched {
		applyNestedLimits(s.app.Store().Get())
	}
	// Only listen/base_path actually need a restart — they're consumed at
	// http.ListenAndServe binding time. DNS is read on every ikev2/l2tp
	// handshake (a.store.Get().DNS), so DNS changes affect new VPN
	// connections immediately. force_https / https_cert_id / session
	// timeout are restart-required (TLS bound at startup).
	listenChanged := body.Listen != nil || body.BasePath != nil ||
		body.ForceHTTPS != nil || body.HTTPSCertID != nil
	note := "applied live"
	if listenChanged {
		note = "listen / base_path / TLS settings require restart to take effect"
	}
	writeJSON(w, map[string]string{"status": "ok", "note": note})
}

// diagResolve runs one ResolveViaExit and reports the outcome. Lets
// the operator confirm the relay-routed DNS path is wired correctly:
// upstream reachable, exit dial-able, response parsing OK. Read-only.
func (s *Server) diagResolve(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	exitVia := r.URL.Query().Get("exit_via")
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ips, err := s.app.ResolveViaExit(ctx, name, exitVia)
	resp := map[string]any{
		"name":     name,
		"exit_via": exitVia,
		"ips":      ips,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

// --- Node ---

func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	st := s.app.Node().GetStats()
	writeJSON(w, map[string]any{
		"tx_bytes": st.TxBytes,
		"rx_bytes": st.RxBytes,
		"tx_rate":  st.TxRate,
		"rx_rate":  st.RxRate,
		"conns":    st.Conns,
		"exit_clients": st.ExitClients,
	})
}

// Version is the application version. Update this on each release.
const Version = "1.3.4"

func init() {
	app.AppVersion = Version
	relay.NodeVersion = Version
	capOK, _ := app.CheckCapability()
	relay.NodeTunCapable = capOK
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	capOK, _ := app.CheckCapability()
	limited := !capOK
	writeJSON(w, map[string]any{
		"node_id":       cfg.NodeID,
		"name":          cfg.Name,
		"exit_node":     cfg.ExitNode,
		"server":        cfg.Server,
		"version":       Version,
		"limited":       limited,
		"compat":        app.IsCompatMode(),
		"hy2_user_auth": cfg.Hy2UserAuth,
		"active_paths":  s.app.AllActivePaths(),
	})
}

func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID      *string          `json:"node_id"`
		Name        *string          `json:"name"`
		ExitNode    *bool            `json:"exit_node"`
		Server      *app.ServerConfig `json:"server"`
		Hy2UserAuth *bool            `json:"hy2_user_auth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Validate server password
	if body.Server != nil && body.Server.Listen != "" && len(body.Server.Password) < 6 {
		http.Error(w, "server password must be at least 6 characters", 400)
		return
	}
	// The hy2 relay password is broadcast to every other peer via the
	// clients list — sharing it with a SOCKS5/HTTP user account would
	// give that user full peer-level relay access. Reject the collision
	// at save time on both directions.
	if body.Server != nil && body.Server.Password != "" {
		curUsers := s.app.Store().Get().Users
		for _, u := range curUsers {
			if u.Password == body.Server.Password {
				http.Error(w, fmt.Sprintf("server password collides with user %q — pick a different password", u.Username), 400)
				return
			}
		}
	}
	oldID := s.app.Store().Get().NodeID

	s.app.Store().Update(func(c *app.Config) {
		if body.NodeID != nil && *body.NodeID != "" {
			c.NodeID = *body.NodeID
			c.Name = *body.NodeID
		}
		if body.Name != nil {
			c.Name = *body.Name
		}
		if body.ExitNode != nil {
			c.ExitNode = *body.ExitNode
		}
		if body.Server != nil {
			if body.Server.Listen == "" && body.Server.Password == "" {
				c.Server = nil
			} else {
				c.Server = body.Server
			}
		}
		if body.Hy2UserAuth != nil {
			c.Hy2UserAuth = *body.Hy2UserAuth
		}
	})

	cfg := s.app.Store().Get()
	oldName := s.app.Node().Name()
	s.app.Node().SetName(cfg.Name)
	s.app.Node().SetNodeID(cfg.NodeID)
	s.app.Node().SetExit(cfg.ExitNode)

	needReconnect := false
	if body.NodeID != nil {
		s.app.PersistNodeID(cfg.NodeID)
		if oldID != cfg.NodeID {
			s.mu.Lock()
			s.oldNodeIDs[oldID] = cfg.NodeID
			s.mu.Unlock()
			needReconnect = true
		}
	}
	// Reconnect if name or ID changed so peers see the new identity
	if cfg.Name != oldName {
		needReconnect = true
	}
	// Hot-restart server if server config or identity changed
	if needReconnect || body.Server != nil {
		go func() {
			if needReconnect {
				s.app.ReconnectAll()
			}
			s.app.RestartServer()
		}()
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- Peers ---

func (s *Server) getPeers(w http.ResponseWriter, r *http.Request) {
	peers := s.app.Node().Peers()
	cfg := s.app.Store().Get()
	type peerResp struct {
		Name      string `json:"name"`
		ExitNode  bool   `json:"exit_node"`
		Direction string `json:"direction"`
		Nested    bool   `json:"nested"`
	}
	result := make([]peerResp, 0, len(peers))
	for _, p := range peers {
		nested := false
		if pc, ok := cfg.Peers[p.Name]; ok {
			nested = pc.Nested
		}
		result = append(result, peerResp{
			Name:      p.Name,
			ExitNode:  p.ExitNode,
			Direction: p.Direction,
			Nested:    nested,
		})
	}
	writeJSON(w, result)
}

// internalPeers returns relay.PeerInfo list (no auth, for reverse nested discovery).
// internalPeers returns peers with cached sub-peer children (no auth, for reverse nested discovery).
func (s *Server) internalPeers(w http.ResponseWriter, r *http.Request) {
	peers := s.app.Node().Peers()
	type peerWithChildren struct {
		relay.PeerInfo
		Children []topoSubPeer `json:"children,omitempty"`
	}
	result := make([]peerWithChildren, 0, len(peers))
	for _, p := range peers {
		pc := peerWithChildren{PeerInfo: p}
		pc.Children = s.getCachedSubPeers(p.Name)
		result = append(result, pc)
	}
	writeJSON(w, result)
}

// getTopology returns a tree structure: direct peers with their nested sub-peers.
// getGraphLayout returns the persisted topology-graph coordinates. Shape:
// { "positions": { "<nodeKey>": { "x": number, "y": number } } }. An empty
// map means "auto-layout" on the client side.
func (s *Server) getGraphLayout(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	pos := cfg.GraphLayout
	if pos == nil {
		pos = map[string]app.GraphLayoutPos{}
	}
	writeJSON(w, map[string]interface{}{"positions": pos})
}

// setGraphLayout persists a full snapshot of the graph layout. Callers
// always send the complete map — partial updates would race with other
// concurrent sessions (last-writer-wins is easier to reason about and
// matches how the UI treats its local state today).
func (s *Server) setGraphLayout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Positions map[string]app.GraphLayoutPos `json:"positions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.SetGraphLayout(body.Positions); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Fan out to every active SSE subscriber so other sessions pick up
	// the change with ~no delay. Always broadcast the committed config
	// snapshot (not the raw request body) so every listener sees the
	// same authoritative map, including any defensive copies performed
	// by SetGraphLayout.
	committed := s.app.Store().Get().GraphLayout
	if committed == nil {
		committed = map[string]app.GraphLayoutPos{}
	}
	s.broadcastGraphLayout(committed)
	writeJSON(w, map[string]string{"status": "ok"})
}

// broadcastGraphLayout sends the snapshot to every registered SSE
// subscriber. Slow consumers with a full buffer simply drop the update —
// each SSE frame carries the complete layout so a missed frame self-
// heals on the next one. Never blocks the PUT path.
func (s *Server) broadcastGraphLayout(positions map[string]app.GraphLayoutPos) {
	s.layoutMu.Lock()
	subs := make([]chan map[string]app.GraphLayoutPos, 0, len(s.layoutSubs))
	for _, ch := range s.layoutSubs {
		subs = append(subs, ch)
	}
	s.layoutMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- positions:
		default:
		}
	}
}

// streamGraphLayout pushes layout snapshots to the client as SSE events.
// The first event after connect carries the current state so the client
// doesn't have to also call GET on mount; subsequent events fire only
// when SetGraphLayout runs (from any session).
func (s *Server) streamGraphLayout(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable any buffering proxies that would defeat SSE.
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan map[string]app.GraphLayoutPos, 4)
	s.layoutMu.Lock()
	s.layoutSubSeq++
	id := s.layoutSubSeq
	s.layoutSubs[id] = ch
	s.layoutMu.Unlock()
	defer func() {
		s.layoutMu.Lock()
		delete(s.layoutSubs, id)
		s.layoutMu.Unlock()
	}()

	writeEvent := func(pos map[string]app.GraphLayoutPos) bool {
		payload, err := json.Marshal(map[string]interface{}{"positions": pos})
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Initial snapshot so the client renders something before the first
	// SetGraphLayout fires.
	initial := s.app.Store().Get().GraphLayout
	if initial == nil {
		initial = map[string]app.GraphLayoutPos{}
	}
	if !writeEvent(initial) {
		return
	}

	// Periodic keepalive so idle connections don't hit HTTP client or
	// proxy timeouts. Nothing needs to be parsed from these, they just
	// refresh the socket.
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case pos, ok := <-ch:
			if !ok {
				return
			}
			if !writeEvent(pos) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) getTopology(w http.ResponseWriter, r *http.Request) {
	peers := s.app.Node().Peers()
	// Alt-slot peers carry the OTHER direction in a mutually-dialed
	// (bidirectional) deployment — see relay.Node.peersAlt for the slot
	// contract. We merge them into the same maps below so both directions
	// surface in the topology JSON: top-level for outbound side, inbound
	// child of self for inbound side. The frontend then renders parallel
	// rails for the mutual link (NodesGraphView's `hasOpposite` branch).
	peersAlt := s.app.Node().PeersAlt()
	allPeers := make([]relay.PeerInfo, 0, len(peers)+len(peersAlt))
	allPeers = append(allPeers, peers...)
	allPeers = append(allPeers, peersAlt...)
	cfg := s.app.Store().Get()

	// Build client lookup for addr info
	clientMap := make(map[string]app.ClientEntry)
	for _, cl := range cfg.Clients {
		clientMap[cl.Name] = cl
	}
	connected := make(map[string]bool)
	nativeMap := make(map[string]bool)
	versionMap := make(map[string]string)
	incompatMap := make(map[string]bool)
	conflictMap := make(map[string]bool)
	unsupportedMap := make(map[string]bool)
	for _, p := range allPeers {
		connected[p.Name] = true
		if p.Native {
			nativeMap[p.Name] = true
		}
		if p.Version != "" {
			versionMap[p.Name] = p.Version
		}
		if p.Incompatible {
			incompatMap[p.Name] = true
		}
		if p.Conflict {
			conflictMap[p.Name] = true
		}
		if p.Unsupported {
			unsupportedMap[p.Name] = true
		}
	}

	type treeNode struct {
		Name         string            `json:"name"`
		Addr         string            `json:"addr,omitempty"`
		Addrs        []string          `json:"addrs,omitempty"`
		IPStatuses   []relay.IPStatus  `json:"ip_statuses,omitempty"`
		ExitNode     bool              `json:"exit_node"`
		Direction    string            `json:"direction"`
		Connected    bool              `json:"connected"`
		Disabled     bool              `json:"disabled"`
		Nested       bool              `json:"nested"`
		Native       bool              `json:"native,omitempty"`
		Version      string            `json:"version,omitempty"`
		Incompatible bool              `json:"incompatible,omitempty"`
		Conflict     bool              `json:"conflict,omitempty"`
		Unsupported  bool              `json:"unsupported,omitempty"`
		TunCapable   bool              `json:"tun_capable,omitempty"`
		LatencyMs    int               `json:"latency_ms"`
		TxRate       uint64            `json:"tx_rate"`
		RxRate       uint64            `json:"rx_rate"`
		// MaxRate (bytes/sec) — same role as topoSubPeer.MaxRate,
		// see comment there. Populated from the local cfg.Clients
		// entry's max(MaxTx, MaxRx).
		MaxRate      uint64            `json:"max_rate,omitempty"`
		IsSelf       bool              `json:"is_self,omitempty"`
		Children     []topoSubPeer     `json:"children,omitempty"`
	}

	disabledMap := make(map[string]bool)
	// maxRateByName: configured bandwidth per direct outbound peer.
	// max(MaxTx, MaxRx) so a duplex-capped link is represented by the
	// higher-of-two rates (the one that's actually a saturating cap
	// for the dominant traffic direction in most deployments).
	maxRateByName := make(map[string]uint64)
	for _, cl := range cfg.Clients {
		if cl.Disabled {
			disabledMap[cl.Name] = true
		}
		var m int
		if cl.MaxTx > m {
			m = cl.MaxTx
		}
		if cl.MaxRx > m {
			m = cl.MaxRx
		}
		if m > 0 {
			maxRateByName[cl.Name] = uint64(m)
		}
	}

	nameSet := make(map[string]bool)
	for _, p := range allPeers {
		nameSet[p.Name] = true
	}
	for _, cl := range cfg.Clients {
		nameSet[cl.Name] = true
	}
	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	sort.Strings(names)

	// Read latencies from background prober (instant, non-blocking)
	latencyCache := make(map[string]int)
	for _, name := range names {
		latencyCache[name] = s.app.Node().GetLatency(name)
	}

	// Collect inbound peers as self's children
	selfServer := ""
	selfDisabled := cfg.Server == nil || cfg.Server.Listen == ""
	if cfg.Server != nil {
		selfServer = cfg.Server.Listen
	}
	selfChildren := make([]topoSubPeer, 0)
	inboundNames := make(map[string]bool)
	for _, p := range allPeers {
		if p.Direction == "inbound" {
			inboundNames[p.Name] = true
			child := topoSubPeer{
				Name:       p.Name,
				ExitNode:   p.ExitNode,
				Direction:  "inbound",
				Via:        cfg.NodeID,
				LatencyMs:  latencyCache[p.Name],
				Version:    p.Version,
				TunCapable: p.TunCapable,
				// Inbound peers in the live `peers` slice are connected
				// by definition — `p` was added when the inbound stream
				// registered. The new `connected` field on topoSubPeer
				// has no `omitempty`, so we MUST set it to true here or
				// the row defaults to false → list shows "offline" for
				// every active inbound peer.
				Connected: true,
			}
			if pc, ok := cfg.Peers[p.Name]; ok {
				if pc.Nested {
					child.Nested = true // explicitly enabled
				}
				if pc.Disabled {
					child.Disabled = true
				}
			}
			selfChildren = append(selfChildren, child)
		}
	}
	peerRates := s.app.Node().PeerRates()
	pathRates := s.app.Node().PathRates()

	// Add per-peer traffic and sub-peers for inbound children
	for i, c := range selfChildren {
		if pr, ok := peerRates[c.Name]; ok {
			selfChildren[i].TxRate = pr.TxRate
			selfChildren[i].RxRate = pr.RxRate
		}
		if c.Nested {
			ancestors := selfAncestors(cfg, c.Name)
			children := filterAncestorPaths(s.getCachedSubPeers(c.Name), ancestors)
			// parentCumLat=latencyCache[c.Name] is self→c. The cache
			// holds the cumulative self→c→child for direct children, so
			// the depth=0 branch in filterChildrenByNestedConfig leaves
			// them alone; deeper levels (raw one-hop) get +parentCumLat.
			selfChildren[i].Children = s.filterChildrenByNestedConfig(children, c.Name, cfg, ancestors, pathRates, latencyCache[c.Name], 0)
		}
	}
	sort.Slice(selfChildren, func(i, j int) bool { return selfChildren[i].Name < selfChildren[j].Name })

	result := make([]treeNode, 0, len(names)+1)
	result = append(result, treeNode{
		Name:       cfg.NodeID,
		Addr:       selfServer,
		Direction:  "local",
		Connected:  true,
		Disabled:   selfDisabled,
		IsSelf:     true,
		LatencyMs:  0,
		TunCapable: relay.NodeTunCapable,
		Children:   selfChildren,
	})

	for _, name := range names {
		_, configuredOutbound := clientMap[name]
		// Skip inbound peers from the top-level list — they're nested
		// under self and shouldn't render twice — UNLESS this peer is
		// ALSO in our cfg.Clients (we configured it as outbound). In
		// that case the relationship is genuinely bidirectional: we
		// dial them AND they dial us. Emit both: this top-level entry
		// (outbound, from our config) plus the inbound child under
		// self (kept in selfChildren above). The frontend's addEdge
		// will produce two opposite-direction edges and the graph
		// renders them as parallel rails.
		if inboundNames[name] && !configuredOutbound {
			continue
		}
		tn := treeNode{Name: name, Connected: connected[name] && !disabledMap[name], Disabled: disabledMap[name]}
		if cl, ok := clientMap[name]; ok {
			tn.Addr = cl.PrimaryAddr()
			addrs := cl.AllAddrs()
			if len(addrs) > 1 {
				ipStatuses := s.app.Node().PeerIPStatuses(name)
				addrLats := s.app.Node().GetAddrLatencies(name)
				primaryAddr := cl.PrimaryAddr()
				// Ensure primary addr is included
				hasPrimary := false
				for _, s := range ipStatuses {
					if s.Addr == primaryAddr {
						hasPrimary = true
						break
					}
				}
				if !hasPrimary {
					primaryLat := latencyCache[name]
					ipStatuses = append([]relay.IPStatus{{Addr: primaryAddr, Status: "online", LatencyMs: primaryLat}}, ipStatuses...)
				}
				// Enrich with per-addr latency
				for i := range ipStatuses {
					if ms, ok := addrLats[ipStatuses[i].Addr]; ok {
						ipStatuses[i].LatencyMs = ms
					}
				}
				tn.IPStatuses = ipStatuses
				tn.Addrs = addrs
			}
			tn.Direction = "outbound"
		}
		// Pull the OUTBOUND-direction peer info for this name, since this
		// is the top-level outbound entry. In the bidirectional case the
		// outbound side may live in either primary or alt — search both.
		// If only an inbound entry is available, use it as the source of
		// metadata (ExitNode, TunCapable, etc.) but keep direction logic
		// gated by configuredOutbound below.
		for _, p := range allPeers {
			if p.Name == name {
				tn.ExitNode = p.ExitNode
				if !configuredOutbound {
					tn.Direction = p.Direction
				}
				tn.TunCapable = p.TunCapable
				if p.Direction == "outbound" {
					// Found the matching outbound entry — prefer this and
					// stop. (Continuing would overwrite with inbound
					// metadata in mutual-dial topologies.)
					break
				}
			}
		}
		tn.Native = nativeMap[name]
		tn.Version = versionMap[name]
		tn.Incompatible = incompatMap[name]
		tn.Conflict = conflictMap[name]
		tn.Unsupported = unsupportedMap[name]
		// Nested flag is always whatever the config says — config is the
		// only authority. Transient compatibility / native-binding /
		// conflict states are surfaced in their own fields (Incompatible,
		// Native, Conflict) so the UI can present a "nested but currently
		// blocked" visual rather than silently flipping the user's
		// configured nested off during a network blip.
		if pc, ok := cfg.Peers[name]; ok && pc.Nested {
			tn.Nested = true
		}
		if tn.Connected && tn.Direction == "outbound" {
			tn.LatencyMs = latencyCache[name]
		} else if !tn.Connected {
			tn.LatencyMs = -1
		}
		if pr, ok := peerRates[name]; ok {
			tn.TxRate = pr.TxRate
			tn.RxRate = pr.RxRate
		}
		if mr, ok := maxRateByName[name]; ok {
			tn.MaxRate = mr
		}

		// Load sub-peers from background cache only (never blocks).
		// Hide children when the peer's wire-protocol generation is
		// newer than ours — we may not be able to parse the frames it
		// would use to expose those sub-peers, and we don't want to
		// surface them in the UI as if they were reachable.
		if tn.Nested && tn.Connected && !tn.Native && !tn.Incompatible && !tn.Conflict && !tn.Unsupported {
			ancestors := selfAncestors(cfg, name)
			children := filterAncestorPaths(s.getCachedSubPeers(name), ancestors)
			tn.Children = s.filterChildrenByNestedConfig(children, name, cfg, ancestors, pathRates, latencyCache[name], 0)
		}
		// Also load children for inbound peers nested under self
		// (they can have outbound connections visible if nested is enabled)
		result = append(result, tn)
	}
	writeJSON(w, result)
}


func (s *Server) getCachedSubPeers(name string) []topoSubPeer {
	s.subPeersMu.RLock()
	defer s.subPeersMu.RUnlock()
	return s.subPeersCache[name]
}

// nestedLimits holds the runtime values of the 5 nested-discovery hard
// caps. Sourced from cfg fields (with built-in defaults as fallback) and
// clamped to sane ranges by clampNestedLimits. The atomic pointer makes
// the limits hot-reloadable: updateUISettings calls applyNestedLimits to
// install a new snapshot, and every read site (walkAndCache,
// assembleDeepTree, fetchSubPeersViaStream, SubPeersUpdater) picks it up
// on the next tick without a restart.
type nestedLimits struct {
	MaxDepth         int
	MaxResponseNodes int
	MaxCacheEntries  int
	MaxResponseBytes int
	MaxFetchFanOut   int
}

var currentNestedLimits atomic.Pointer[nestedLimits]

// clampNestedLimits applies built-in defaults for any zero / unset field
// and clamps each value to a sane range. Defaults match the original
// v1.3.2 constants so behaviour is identical when cfg has the fields
// blank.
func clampNestedLimits(in nestedLimits) nestedLimits {
	if in.MaxDepth <= 0 {
		in.MaxDepth = 5
	}
	if in.MaxDepth > 10 {
		in.MaxDepth = 10
	}
	if in.MaxResponseNodes <= 0 {
		in.MaxResponseNodes = 1024
	}
	if in.MaxResponseNodes > 65536 {
		in.MaxResponseNodes = 65536
	}
	if in.MaxCacheEntries <= 0 {
		in.MaxCacheEntries = 5000
	}
	if in.MaxCacheEntries > 100000 {
		in.MaxCacheEntries = 100000
	}
	if in.MaxResponseBytes <= 0 {
		in.MaxResponseBytes = 1 << 20 // 1 MiB
	}
	if in.MaxResponseBytes > 16<<20 {
		in.MaxResponseBytes = 16 << 20
	}
	if in.MaxFetchFanOut <= 0 {
		in.MaxFetchFanOut = 8
	}
	if in.MaxFetchFanOut > 64 {
		in.MaxFetchFanOut = 64
	}
	return in
}

// applyNestedLimits reads the cfg fields, clamps, and atomically installs
// the resulting snapshot. Called once at server start and again on every
// successful updateUISettings PUT. Reads on hot paths use getNestedLimits.
func applyNestedLimits(cfg app.Config) {
	limits := clampNestedLimits(nestedLimits{
		MaxDepth:         cfg.MaxNestedDepth,
		MaxResponseNodes: cfg.MaxResponseNodes,
		MaxCacheEntries:  cfg.MaxCacheEntries,
		MaxResponseBytes: cfg.MaxResponseBytes,
		MaxFetchFanOut:   cfg.MaxFetchFanOut,
	})
	// Env override on max-depth kept as a startup-only escape hatch — if
	// the operator deliberately set HY2SCALE_MAX_NESTED_DEPTH the env
	// value wins over the cfg field. Reads on every applyNestedLimits
	// call so re-setting via env+restart keeps working.
	if e := strings.TrimSpace(os.Getenv("HY2SCALE_MAX_NESTED_DEPTH")); e != "" {
		if n, err := strconv.Atoi(e); err == nil && n >= 1 && n <= 10 {
			limits.MaxDepth = n
		}
	}
	currentNestedLimits.Store(&limits)
}

// getNestedLimits returns the current snapshot. Cheap: one atomic load,
// no allocation. Always non-nil after applyNestedLimits has run at least
// once at server start.
func getNestedLimits() nestedLimits {
	if p := currentNestedLimits.Load(); p != nil {
		return *p
	}
	return clampNestedLimits(nestedLimits{}) // pure-defaults fallback
}

// (limits live in nestedLimits / currentNestedLimits / getNestedLimits above)

// assembleDeepTree walks the local subPeersCache to build a multi-depth
// tree rooted at `prefix`. The result is suitable for embedding into a
// peerWithChildren.Children field — Children at every level reflect
// what we have cached for that qualified path, gated by rule 2.
//
// Recursion stops at any of:
//   - depth >= maxNestedDepth (defensive cap)
//   - cache miss at the qualified key (we have nothing to publish)
//   - cfg.Peers[qkey].Nested == false (rule 2 — caller didn't authorise
//     us to propagate descendants of qkey)
//   - cfg.Peers[qkey].Disabled == true (caller explicitly disabled this
//     peer — don't propagate at all)
//   - emitted node count >= maxResponseNodes (defensive payload cap)
//
// Rule 1 (cycle drop) is already enforced when the cache was populated
// (walkAndCache pre-seeds ancestors); we don't repeat the check here.
//
// Counter is passed by pointer so siblings share quota — preventing one
// fan-out branch from pre-empting all the budget.
func (s *Server) assembleDeepTree(cfg app.Config, prefix string, depth int, count *int) []topoSubPeer {
	lim := getNestedLimits()
	if depth >= lim.MaxDepth || *count >= lim.MaxResponseNodes {
		return nil
	}
	s.subPeersMu.RLock()
	base, ok := s.subPeersCache[prefix]
	s.subPeersMu.RUnlock()
	if !ok || len(base) == 0 {
		return nil
	}
	result := make([]topoSubPeer, 0, len(base))
	for _, c := range base {
		if *count >= lim.MaxResponseNodes {
			break
		}
		qkey := prefix + "/" + c.Name
		if pc, hasPC := cfg.Peers[qkey]; hasPC && pc.Disabled {
			continue // user disabled — drop entirely
		}
		out := c // value copy; mutate the local
		// Latency stays as-is here: this function feeds the
		// `_relay_list_peers_` response sent to other nodes, and the
		// inter-node convention is that LatencyMs is one-hop measured
		// by THIS node toward each child — fetchSubPeersViaStream on
		// the receiving side adds its own self→prefix to convert.
		// Mutating to cumulative here would double-count for downstream
		// consumers. Cumulative-from-self adjustment for OUR own UI is
		// done in filterChildrenByNestedConfig.
		*count++
		if pc, hasPC := cfg.Peers[qkey]; hasPC && pc.Nested {
			out.Children = s.assembleDeepTree(cfg, qkey, depth+1, count)
		} else {
			out.Children = nil
		}
		result = append(result, out)
	}
	return result
}

// walkAndCache populates newCache at every qualified-path key discovered
// in the response tree. It applies rule 1 (drop any descendant whose
// name is already an ancestor — self-id, self-name, and any peer name
// already on the path are seeded into the ancestors set by the caller)
// at every level, and stops recursing at maxNestedDepth.
//
// Each map entry is stored flat: the slice elements have Children=nil.
// Depth comes from chained qualified keys (cache[A], cache[A/B],
// cache[A/B/C], …) — the storage shape mirrors the qualified-key
// addressing the topology API and adaptive routing already use.
func walkAndCache(prefix string, children []topoSubPeer, ancestors map[string]bool, depth int, newCache map[string][]topoSubPeer) {
	lim := getNestedLimits()
	if depth >= lim.MaxDepth || len(children) == 0 {
		return
	}
	if len(newCache) >= lim.MaxCacheEntries {
		// Defensive cache-size cap — refuse to add more keys this
		// round. Already-populated entries stay valid; we just stop
		// expanding deeper. Rule 1 + maxNestedDepth + rule 2 should
		// keep this from binding under sane configs; if it ever does
		// the operator is the one paying with truncation visibility.
		return
	}
	flat := make([]topoSubPeer, 0, len(children))
	for _, c := range children {
		if ancestors[c.Name] {
			continue
		}
		cflat := c
		cflat.Children = nil
		flat = append(flat, cflat)
	}
	if len(flat) == 0 {
		return
	}
	newCache[prefix] = flat
	for _, c := range children {
		if ancestors[c.Name] || len(c.Children) == 0 {
			continue
		}
		ancestors[c.Name] = true
		walkAndCache(prefix+"/"+c.Name, c.Children, ancestors, depth+1, newCache)
		delete(ancestors, c.Name)
	}
}

// StartSubPeersUpdater runs in background, periodically refreshes nested peer data.
// Uses relay streamListPeers for both outbound and inbound peers (same mechanism).
func (s *Server) StartSubPeersUpdater(ctx context.Context) {
	// Register the rich listPeers handler used by remote nodes' SubPeersUpdater.
	//
	// Per docs/nested-rules.md, the response:
	//   - Rule 2: for each direct peer P, only embed sub-peers if we have
	//     explicitly authorised P as nested on OUR side. Non-nested peers
	//     ship with no Children.
	//   - Rule 3: what we embed is OUR flat cache of P's direct peers,
	//     strictly one level deep. We never walk into grandchildren — the
	//     caller learns deeper paths by chaining their own fetches through
	//     their direct peer, never by bypassing a hop.
	// Together these prevent the exponential feedback loop that made
	// AUB↔CN peers saturate the link (docs/nested-discovery-explosion.md):
	// the payload never exceeds O(direct peers × one-hop descendants),
	// and cycles like A → B → A collapse the second A via rule 1 at the
	// decoder side.
	s.app.Node().SetListPeersFunc(func() []byte {
		peers := s.app.Node().Peers()
		cfg := s.app.Store().Get()
		type peerWithChildren struct {
			relay.PeerInfo
			// Connected lets a remote consumer distinguish "configured
			// here but currently offline" from "fully alive". Disabled
			// peers are NOT propagated at all — see filter below — so
			// the consumer never sees `Disabled=true` for our sub-peers.
			Connected bool          `json:"connected"`
			// MaxRate (bytes/sec) — see topoSubPeer.MaxRate comment.
			// Surfaced so a remote graph (drawing US over a multi-hop
			// chain) can size the edge by our local configured cap
			// before any traffic actually flows.
			MaxRate   uint64        `json:"max_rate,omitempty"`
			Children  []topoSubPeer `json:"children,omitempty"`
		}

		// Per-direct-peer configured bandwidth lookup, identical to
		// the topology-API map but local to this closure. Keyed by
		// peer name (= cfg.Clients[i].Name).
		maxRateByName := make(map[string]uint64)
		for _, cl := range cfg.Clients {
			var m int
			if cl.MaxTx > m {
				m = cl.MaxTx
			}
			if cl.MaxRx > m {
				m = cl.MaxRx
			}
			if m > 0 {
				maxRateByName[cl.Name] = uint64(m)
			}
		}

		// Live (currently-connected) direct peers. Skip any that we
		// have explicitly disabled in our peers config — the user's
		// preference is that disabled peers should NOT propagate.
		liveByName := make(map[string]bool, len(peers))
		result := make([]peerWithChildren, 0, len(peers))
		for _, p := range peers {
			liveByName[p.Name] = true
			if pcfg, ok := cfg.Peers[p.Name]; ok && pcfg.Disabled {
				continue
			}
			pc := peerWithChildren{PeerInfo: p, Connected: true, MaxRate: maxRateByName[p.Name]}
			if pcfg, ok := cfg.Peers[p.Name]; ok && pcfg.Nested {
				// v1.3.2: walk own cache for qualified paths prefixed
				// with p.Name, building a multi-depth tree per the
				// rule-2 authorisation chain. Stops at maxNestedDepth
				// or maxResponseNodes (both defensive backstops; rule 1
				// + rule 2 already shape the tree under sane config).
				count := 0
				pc.Children = s.assembleDeepTree(cfg, p.Name, 0, &count)
			}
			result = append(result, pc)
		}

		// Configured-but-offline outbound peers. These appear in
		// cfg.Clients but are missing from the live `peers` slice
		// because Node.peers gets the entry deleted on disconnect.
		// Propagate them with Connected=false so the consumer's
		// graph stays visually complete (red edge to offline node,
		// node still drawn). Skip disabled.
		for _, cl := range cfg.Clients {
			if liveByName[cl.Name] {
				continue
			}
			if pcfg, ok := cfg.Peers[cl.Name]; ok && pcfg.Disabled {
				continue
			}
			result = append(result, peerWithChildren{
				PeerInfo:  relay.PeerInfo{Name: cl.Name, Direction: "outbound"},
				Connected: false,
				MaxRate:   maxRateByName[cl.Name],
			})
		}

		data, _ := json.Marshal(result)
		return data
	})

	time.Sleep(2 * time.Second) // offset from prober
	t := time.NewTicker(7 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.subPeersKick:
		}

		newCache := make(map[string][]topoSubPeer)
		connectedPeers := s.app.Node().ConnectedPeerNames()
		nativeMap := s.app.Node().NativeMap()
		cfg := s.app.Store().Get()

		// Per docs/nested-rules.md rule 3, we only query DIRECT peers.
		// The response already carries one level of flat cached grandchildren
		// per direct peer that the remote has authorised — that's our source
		// of qualified-path data. Deeper paths extend through chained hops:
		// the remote's own SubPeersUpdater populated its cache from its own
		// direct peers the same way, so what it embeds is exactly "B's
		// current view of its B/C level", never an arbitrarily-deep tree.
		var fetchNames []string
		for _, name := range connectedPeers {
			if nativeMap[name] {
				continue
			}
			pc, ok := cfg.Peers[name]
			if !ok || !pc.Nested {
				continue
			}
			// Only direct (unqualified) peer names here.
			if strings.Contains(name, "/") {
				continue
			}
			fetchNames = append(fetchNames, name)
		}

		type fetchResult struct {
			name     string
			children []topoSubPeer
		}
		resultCh := make(chan fetchResult, len(fetchNames))
		// Bounded fan-out: at most cfg-driven concurrent stream
		// fetches, regardless of how many direct peers are
		// nested-authorised. Keeps goroutine + socket pressure
		// predictable on a heavy cluster.
		sem := make(chan struct{}, getNestedLimits().MaxFetchFanOut)
		for _, n := range fetchNames {
			sem <- struct{}{}
			go func(name string) {
				defer func() { <-sem }()
				resultCh <- fetchResult{name, s.fetchSubPeersViaStream(name)}
			}(n)
		}
		for range fetchNames {
			select {
			case r := <-resultCh:
				if r.children == nil {
					break
				}
				// v1.3.2: walk the response tree to whatever depth the
				// remote sent (clamped by maxNestedDepth). Each visited
				// path becomes a flat cache entry keyed by the qualified
				// path string — the same addressing topology /
				// findPathsTo / etc. already use. Rule 1 (cycle drop)
				// is applied at every level via the ancestors set
				// pre-seeded with self identity and the direct-peer
				// prefix. Rule 3 is preserved: every grandchild we
				// learn about is one whose direct parent (the peer
				// queried) chose to publish it in their listPeers
				// response — we never bypass to query the target.
				ancestors := map[string]bool{
					cfg.NodeID: true,
					cfg.Name:   true,
					r.name:     true,
				}
				walkAndCache(r.name, r.children, ancestors, 0, newCache)
			case <-time.After(6 * time.Second):
			}
		}

		// Detect nested peer renames by comparing old and new caches.
		// If a parent had child "X" and now has child "Y" at the same position,
		// rename all config references from X to Y.
		s.subPeersMu.RLock()
		oldCache := s.subPeersCache
		s.subPeersMu.RUnlock()
		for parentName, newChildren := range newCache {
			oldChildren, ok := oldCache[parentName]
			if !ok || len(oldChildren) == 0 {
				continue
			}
			// Log for debugging
			if len(oldChildren) != len(newChildren) {
				log.Printf("[topology] %s children changed: %d → %d", parentName, len(oldChildren), len(newChildren))
			}
			// Build old name set
			oldNames := make(map[string]bool)
			for _, c := range oldChildren {
				oldNames[c.Name] = true
			}
			// Build new name set
			newNames := make(map[string]bool)
			for _, c := range newChildren {
				newNames[c.Name] = true
			}
			// Find names that disappeared and appeared
			for _, c := range newChildren {
				if oldNames[c.Name] {
					continue // name unchanged
				}
				// New name appeared. Check if an old name disappeared (rename).
				for _, oc := range oldChildren {
					if newNames[oc.Name] {
						continue // old name still exists
					}
					// oc.Name disappeared, c.Name appeared → likely rename
					log.Printf("[topology] nested peer rename detected: %s → %s (via %s)", oc.Name, c.Name, parentName)
					s.app.Store().Update(func(cfg *app.Config) {
						rename := func(s string) string {
							return strings.ReplaceAll(s, oc.Name, c.Name)
						}
						newPeers := make(map[string]app.PeerConfig)
						for k, v := range cfg.Peers {
							newPeers[rename(k)] = v
						}
						cfg.Peers = newPeers
						for i := range cfg.Proxies {
							cfg.Proxies[i].ExitVia = rename(cfg.Proxies[i].ExitVia)
							for j := range cfg.Proxies[i].ExitPaths {
								cfg.Proxies[i].ExitPaths[j] = rename(cfg.Proxies[i].ExitPaths[j])
							}
						}
						for i := range cfg.Users {
							cfg.Users[i].ExitVia = rename(cfg.Users[i].ExitVia)
							for j := range cfg.Users[i].ExitPaths {
								cfg.Users[i].ExitPaths[j] = rename(cfg.Users[i].ExitPaths[j])
							}
						}
					})
					break // only one rename per new name
				}
			}
		}

		s.subPeersMu.Lock()
		s.subPeersCache = newCache
		s.subPeersMu.Unlock()

		// Feed sub-peer data into relay's peersOfCache for path discovery.
		// Cache at BOTH qualified path (e.g. "cn-xinchang/2400" — unambiguous
		// for multi-hop TUN capability lookup) AND at bare last-segment name
		// (backward-compat for adaptive BFS).
		//
		// Cycle-safety: `visitedBare` tracks bare peer NAMES we've already
		// expanded. A cyclic reference (e.g. A→B→A) would otherwise generate
		// unbounded qualified paths because the fallback lookup via newCache
		// keeps finding more children under the same node name — previously
		// this caused demo-au to run away to 7.5GB RSS in ~30s.
		visitedBare := make(map[string]bool)
		var walkAndCache func(qualifiedPath string, children []topoSubPeer)
		walkAndCache = func(qualifiedPath string, children []topoSubPeer) {
			parts := strings.Split(qualifiedPath, "/")
			bare := parts[len(parts)-1]
			if visitedBare[bare] {
				return
			}
			visitedBare[bare] = true

			var infos []relay.PeerInfo
			for _, c := range children {
				infos = append(infos, relay.PeerInfo{
					Name:       c.Name,
					ExitNode:   c.ExitNode,
					Native:     c.Native,
					Version:    c.Version,
					TunCapable: c.TunCapable,
				})
			}
			if len(infos) > 0 {
				s.app.Node().SetPeersOfCache(qualifiedPath, infos)
				if strings.Contains(qualifiedPath, "/") {
					if _, exists := s.app.Node().PeersOfCached(bare); !exists {
						s.app.Node().SetPeersOfCache(bare, infos)
					}
				}
			}
			for _, c := range children {
				if visitedBare[c.Name] {
					continue
				}
				if len(c.Children) > 0 {
					walkAndCache(qualifiedPath+"/"+c.Name, c.Children)
				} else if cached, ok := newCache[c.Name]; ok && len(cached) > 0 {
					walkAndCache(qualifiedPath+"/"+c.Name, cached)
				}
			}
		}
		for peerName, children := range newCache {
			walkAndCache(peerName, children)
		}
	}
}

// fetchSubPeersViaStream queries a peer's topology via relay streamListPeers.
// Preserves the one-level-of-grandchildren structure the remote embeds per
// rule 3 in docs/nested-rules.md. The caller (StartSubPeersUpdater) walks
// the result once to populate subPeersCache flat at every qualified key it
// observes, and hands the tree to walkAndCache for the relay-side
// peersOfCache population.
//
// No multi-hop query variant exists. Per rule 3 we never bypass a direct
// peer with PeersOfVia to inspect a descendant — the descendant's info
// must come from the direct peer's own cache as embedded in its listPeers
// reply, or not at all.
func (s *Server) fetchSubPeersViaStream(peerName string) []topoSubPeer {
	data, err := s.app.Node().PeersOfRaw(peerName)
	if err != nil {
		return nil
	}
	if cap := getNestedLimits().MaxResponseBytes; len(data) > cap {
		log.Printf("[topology] %s listPeers payload %d B > %d cap — dropping", peerName, len(data), cap)
		return nil
	}
	type peerWithChildren struct {
		Name       string        `json:"name"`
		ExitNode   bool          `json:"exit_node"`
		Direction  string        `json:"direction"`
		Native     bool          `json:"native"`
		LatencyMs  int           `json:"latency_ms"`
		Version    string        `json:"version,omitempty"`
		TunCapable bool          `json:"tun_capable,omitempty"`
		// Pointer so we can detect "field absent" vs "field=false".
		// Older peers (1.3.0/early 1.3.1) don't send `connected` at
		// all — we treat absent as live (true) so the legacy
		// behavior is preserved unchanged. New peers send true/false
		// explicitly per the offline-propagation contract.
		Connected *bool         `json:"connected,omitempty"`
		// MaxRate, bytes/sec; absent on old peers. Used by the graph
		// to size the edge thickness directly from configured
		// bandwidth, even before any traffic flows.
		MaxRate   uint64        `json:"max_rate,omitempty"`
		Children  []topoSubPeer `json:"children,omitempty"`
	}
	var remotePeers []peerWithChildren
	if err := json.Unmarshal(data, &remotePeers); err != nil {
		return nil
	}
	parentLatency := s.app.Node().GetLatency(peerName)
	cfg := s.app.Store().Get()
	selfNames := map[string]bool{cfg.NodeID: true, cfg.Name: true, peerName: true}
	children := make([]topoSubPeer, 0, len(remotePeers))
	for _, rp := range remotePeers {
		childLatency := rp.LatencyMs
		if childLatency > 0 && parentLatency > 0 {
			childLatency += parentLatency
		}
		// Rule 1 at the FIRST grandchild level: drop any grandchild whose
		// name already appears in {local self, direct peer}. Guards
		// against paths like AUB/cn-xinchang/au-kbv looping back to us.
		// v1.3.2: deeper levels of the response tree are kept intact —
		// the consumer (walkAndCache in StartSubPeersUpdater) walks them
		// and applies rule 1 / depth-cap at every level via the
		// ancestors set. We no longer strip beyond the first grandchild
		// here; depth comes back through chained hops naturally and the
		// per-walk cycle check inside walkAndCache prevents the
		// pathological self-amplification rc14 was guarding against.
		var embedded []topoSubPeer
		if len(rp.Children) > 0 {
			embedded = make([]topoSubPeer, 0, len(rp.Children))
			for _, gc := range rp.Children {
				if selfNames[gc.Name] || gc.Name == rp.Name {
					continue
				}
				embedded = append(embedded, gc) // keep gc.Children intact
			}
		}
		// Connected: absent → treat as connected (legacy peers).
		// Present → use the explicit value.
		connected := true
		if rp.Connected != nil {
			connected = *rp.Connected
		}
		child := topoSubPeer{
			Name:       rp.Name,
			ExitNode:   rp.ExitNode,
			Direction:  rp.Direction,
			Via:        peerName,
			LatencyMs:  childLatency,
			Native:     rp.Native,
			Version:    rp.Version,
			TunCapable: rp.TunCapable,
			Connected:  connected,
			MaxRate:    rp.MaxRate,
			Children:   embedded,
		}
		children = append(children, child)
	}
	return children
}

// truncateAndFilter limits tree depth, removes self-ID, and detects cycles.
func truncateAndFilter(children []topoSubPeer, parentLatency int, selfID string, maxDepth int) []topoSubPeer {
	if maxDepth <= 0 || len(children) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	return doTruncate(children, parentLatency, selfID, maxDepth, seen)
}

func doTruncate(children []topoSubPeer, parentLatency int, selfID string, depth int, seen map[string]bool) []topoSubPeer {
	if depth <= 0 || len(children) == 0 {
		return nil
	}
	var result []topoSubPeer
	for _, c := range children {
		if c.Name == selfID || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		if c.LatencyMs > 0 && parentLatency > 0 {
			c.LatencyMs += parentLatency
		}
		c.Children = doTruncate(c.Children, parentLatency, selfID, depth-1, seen)
		result = append(result, c)
		delete(seen, c.Name) // allow same name in different branches
	}
	return result
}

// filterChildrenByNestedConfig strips deeper children unless local nested config allows them.
// Uses path-qualified keys (parent/child) so same-name peers have independent nested state.
// When c.Nested is enabled but c.Children is empty, pulls the flat child list
// from subPeersCache[qualifiedKey] — StartSubPeersUpdater populates one cache
// entry per user-declared qualified path via explicit PeersOfVia queries.
//
// Rule 1 (including self identity): a descendant whose name matches any
// ancestor on the path — OR matches our local node_id / name — is dropped.
// `ancestors` is mutated on descent and restored on backtrack so identical
// names on parallel sibling branches remain legal.
//
// Path-qualified traffic rates are attached per node so the Nodes UI can
// attribute relayed bytes to the specific nested descendant they're destined
// for, not only to the first-hop direct peer that physically carries them.
func (s *Server) filterChildrenByNestedConfig(children []topoSubPeer, parentName string, cfg app.Config, ancestors map[string]bool, pathRates map[string]relay.PeerTraffic, parentCumLat int, depth int) []topoSubPeer {
	if len(children) == 0 {
		return children
	}
	// Defensive depth cap mirroring the cache-fill side (walkAndCache).
	// Under normal operation this never binds — walkAndCache already
	// stops populating qkeys past maxNestedDepth, so the cache lookups
	// below come up empty and the recursion ends naturally. This branch
	// only fires if cache state contradicts maxNestedDepth (e.g., env
	// var was lowered after cache was filled, or in-memory drift).
	if depth >= getNestedLimits().MaxDepth {
		stripped := make([]topoSubPeer, len(children))
		for i, c := range children {
			c.Children = nil
			stripped[i] = c
		}
		return stripped
	}
	result := make([]topoSubPeer, 0, len(children))
	for _, c := range children {
		if ancestors[c.Name] {
			continue
		}
		qualifiedKey := parentName + "/" + c.Name
		pc, hasPC := cfg.Peers[qualifiedKey]
		c.Nested = hasPC && pc.Nested
		c.Disabled = hasPC && pc.Disabled
		if pr, ok := pathRates[qualifiedKey]; ok {
			c.TxRate = pr.TxRate
			c.RxRate = pr.RxRate
		}
		// Latency cumulative-from-self adjustment.
		//
		// At depth=0 the input slice came from getCachedSubPeers(parent)
		// which is subPeersCache[parent] — fetchSubPeersViaStream already
		// pre-added self→parent to each direct child's LatencyMs there,
		// so values are already cumulative. Don't double-add.
		//
		// At depth>=1 the input slice was either pulled from
		// subPeersCache[parent/grandparent/...] inside the previous
		// recursion's `c.Children = s.getCachedSubPeers(qualifiedKey)`
		// branch — which holds RAW one-hop values — or it survived in
		// c.Children from a remote response that walkAndCache flattened
		// without adjustment. Either way we have to fold parentCumLat in
		// here so the list view (and graph segment math) sees a true
		// cumulative figure for every depth, not just the second hop.
		if depth >= 1 && c.LatencyMs > 0 && parentCumLat > 0 {
			c.LatencyMs += parentCumLat
		}
		childCumLat := c.LatencyMs
		if c.Nested {
			if len(c.Children) == 0 {
				// Pull from the per-qualified-path cache populated by
				// StartSubPeersUpdater + walkAndCache. v1.3.2 fills
				// every depth ≤ maxNestedDepth, so this lookup may
				// return data even at qualified keys ≥3 deep.
				c.Children = s.getCachedSubPeers(qualifiedKey)
			}
			if len(c.Children) > 0 {
				ancestors[c.Name] = true
				c.Children = s.filterChildrenByNestedConfig(c.Children, qualifiedKey, cfg, ancestors, pathRates, childCumLat, depth+1)
				delete(ancestors, c.Name)
			}
		} else {
			c.Children = nil
		}
		result = append(result, c)
	}
	return result
}

// selfAncestors returns the baseline ancestor set used for Rule 1 filtering.
// Always contains the local node's ID and name so neither can ever appear as
// a sub-peer at any depth.
func selfAncestors(cfg app.Config, extra ...string) map[string]bool {
	a := map[string]bool{cfg.NodeID: true, cfg.Name: true}
	for _, e := range extra {
		if e != "" {
			a[e] = true
		}
	}
	return a
}

// filterAncestorPaths recursively removes any descendant whose name already
// appears in the ancestor path (including self). This prevents loops like
// "AUB/tz-cm-temp/2400/tz-cm-temp" where a descendant's peer list legitimately
// includes the upstream node, since routing through a node already in the path
// would just bounce the traffic back.
// `ancestors` is mutated during recursion (add on descent, remove on backtrack)
// so the same name IS allowed in parallel sibling branches.
func filterAncestorPaths(children []topoSubPeer, ancestors map[string]bool) []topoSubPeer {
	if len(children) == 0 {
		return children
	}
	filtered := make([]topoSubPeer, 0, len(children))
	for _, c := range children {
		if ancestors[c.Name] {
			continue
		}
		ancestors[c.Name] = true
		c.Children = filterAncestorPaths(c.Children, ancestors)
		delete(ancestors, c.Name)
		filtered = append(filtered, c)
	}
	return filtered
}

// addLatencyOffset recursively adds an offset to all latency values in a sub-peer tree.
func addLatencyOffset(children []topoSubPeer, offset int) []topoSubPeer {
	if offset <= 0 || len(children) == 0 {
		return children
	}
	result := make([]topoSubPeer, len(children))
	copy(result, children)
	for i := range result {
		if result[i].LatencyMs > 0 {
			result[i].LatencyMs += offset
		}
		result[i].Children = addLatencyOffset(result[i].Children, offset)
	}
	return result
}

// loadSubPeers loads nested peers. path is the chain from local to the peer being queried.
func (s *Server) loadSubPeers(path []string, parentLatency int, latencyCache map[string]int, cfg app.Config, depth int) []topoSubPeer {
	if depth > 8 {
		return nil
	}
	target := path[len(path)-1]
	var subPeers []relay.PeerInfo
	var err error
	if len(path) == 1 {
		// Direct peer: query directly
		subPeers, err = s.app.Node().PeersOf(target)
	} else {
		// Multi-hop: query via chain
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		subPeers, err = s.app.Node().PeersOfVia(ctx, path)
	}
	if err != nil {
		return nil
	}
	cfg2 := s.app.Store().Get()
	myID := cfg2.NodeID
	myName := cfg2.Name
	peerName := path[len(path)-1]
	children := make([]topoSubPeer, 0, len(subPeers))
	for _, sp := range subPeers {
		// Cycle detection: skip self (by ID or display name)
		if sp.Name == myID || sp.Name == myName {
			continue
		}
		inPath := false
		for _, p := range path {
			if p == sp.Name {
				inPath = true
				break
			}
		}
		if inPath {
			continue
		}
		// Cumulative latency: parent's latency + remote-reported latency to this child
		childLatency := sp.LatencyMs
		if childLatency > 0 && parentLatency > 0 {
			childLatency += parentLatency
		}
		child := topoSubPeer{
			Name:      sp.Name,
			ExitNode:  sp.ExitNode,
			Direction: sp.Direction,
			Via:       peerName,
			LatencyMs: childLatency,
			// Default to connected=true — this code path is for peers
			// the source actively reported (relay PeerInfo from PeersOf).
			// Offline propagation flows through fetchSubPeersViaStream's
			// dedicated handling above, not this branch.
			Connected: true,
		}
		qualifiedKey := peerName + "/" + sp.Name
		if sp.Native {
			child.Native = true
		} else if pc, ok := cfg.Peers[qualifiedKey]; ok && pc.Nested {
			child.Nested = true // explicitly enabled
		}
		if child.Nested && !child.Native {
			childPath := append(append([]string{}, path...), sp.Name)
			child.Children = s.loadSubPeers(childPath, childLatency, latencyCache, cfg, depth+1)
		}
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
	return children
}

func (s *Server) getNestedPeers(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	peers, err := s.app.Node().PeersOf(name)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, peers)
}

func (s *Server) setPeerDisabled(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.SetPeerDisabled(name, body.Disabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) setNested(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.SetNested(name, body.Enabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Signal immediate cache refresh (non-blocking)
	select {
	case s.subPeersKick <- struct{}{}:
	default:
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- Clients ---

func (s *Server) getClients(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	connected := make(map[string]bool)
	for _, p := range s.app.Node().Peers() {
		connected[p.Name] = true
	}
	type clientResp struct {
		app.ClientEntry
		Connected bool `json:"connected"`
	}
	result := make([]clientResp, 0, len(cfg.Clients))
	for _, cl := range cfg.Clients {
		result = append(result, clientResp{
			ClientEntry: cl,
			Connected:   connected[cl.Name] && !cl.Disabled,
		})
	}
	writeJSON(w, result)
}

func (s *Server) getClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.app.Store().Get()
	for _, cl := range cfg.Clients {
		if cl.Name == name || cl.Addr == name {
			writeJSON(w, cl)
			return
		}
	}
	http.Error(w, "not found", 404)
}

func (s *Server) updateClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var cl app.ClientEntry
	if err := json.NewDecoder(r.Body).Decode(&cl); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Sync Addrs ↔ Addr
	if len(cl.Addrs) > 0 {
		cl.Addr = cl.Addrs[0]
	} else if cl.Addr != "" {
		cl.Addrs = []string{cl.Addr}
	}
	if cl.Addr == "" || cl.Password == "" {
		http.Error(w, "addr and password required", 400)
		return
	}
	for _, a := range cl.AllAddrs() {
		if !validAddrSpec(a) {
			http.Error(w, fmt.Sprintf("invalid address format %q", a), 400)
			return
		}
	}
	// Look up by URL path name, update with body (may include rename)
	oldName := name
	if cl.Name == "" {
		cl.Name = name
	}
	if err := s.app.UpdateClientByAddr(oldName, cl); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) addClient(w http.ResponseWriter, r *http.Request) {
	var cl app.ClientEntry
	if err := json.NewDecoder(r.Body).Decode(&cl); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Sync Addrs ↔ Addr for backward compat
	if len(cl.Addrs) > 0 {
		cl.Addr = cl.Addrs[0]
	} else if cl.Addr != "" {
		cl.Addrs = []string{cl.Addr}
	}
	if cl.Name == "" || cl.Addr == "" || cl.Password == "" {
		http.Error(w, "name, addr, password required", 400)
		return
	}
	for _, a := range cl.AllAddrs() {
		if !validAddrSpec(a) {
			http.Error(w, fmt.Sprintf("invalid address format %q", a), 400)
			return
		}
	}
	if err := s.app.AddClient(cl); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) disableClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.SetClientDisabled(name, body.Disabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) removeClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.app.RemoveClient(name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- Proxies ---

func (s *Server) getProxies(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	// A nil slice JSON-marshals to null, which the UI's useQuery
	// default value doesn't replace (the `= []` shorthand only
	// matches undefined). Always return a concrete empty array.
	list := cfg.Proxies
	if list == nil {
		list = []app.ProxyConfig{}
	}
	writeJSON(w, list)
}

func (s *Server) addProxy(w http.ResponseWriter, r *http.Request) {
	var pc app.ProxyConfig
	if err := json.NewDecoder(r.Body).Decode(&pc); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(pc.ExitPaths) > 0 {
		pc.ExitVia = pc.ExitPaths[0]
	} else if pc.ExitVia != "" {
		pc.ExitPaths = []string{pc.ExitVia}
	}
	if pc.ID == "" || pc.Listen == "" {
		http.Error(w, "id, listen required", 400)
		return
	}
	if pc.Protocol == "" {
		pc.Protocol = "socks5"
	}
	if err := s.app.AddProxy(pc); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) updateProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var pc app.ProxyConfig
	if err := json.NewDecoder(r.Body).Decode(&pc); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	pc.ID = id
	if len(pc.ExitPaths) > 0 {
		pc.ExitVia = pc.ExitPaths[0]
	} else if pc.ExitVia != "" {
		pc.ExitPaths = []string{pc.ExitVia}
	}
	if pc.Protocol == "" {
		pc.Protocol = "socks5"
	}
	// Reject empty listen on update too — symmetric with addProxy. Without
	// this check the operator could clear the field, save, and the proxy
	// would silently persist as enabled-with-no-listen, refusing to bind
	// on next restart. The frontend defaults to a sane "0.0.0.0:<port>"
	// per protocol; this guard catches direct-API callers and edge cases
	// where an empty string slips through.
	if pc.Listen == "" {
		http.Error(w, "listen required", 400)
		return
	}
	if err := s.app.UpdateProxy(pc); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) removeProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.app.RemoveProxy(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- SS ---

func (s *Server) getSSConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	if cfg.SS == nil {
		writeJSON(w, map[string]any{"listen": "", "enabled": false, "method": "aes-256-gcm"})
		return
	}
	writeJSON(w, cfg.SS)
}

func (s *Server) updateSSConfig(w http.ResponseWriter, r *http.Request) {
	var ss app.SSConfig
	if err := json.NewDecoder(r.Body).Decode(&ss); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.app.Store().Update(func(c *app.Config) {
		c.SS = &ss
	})
	go s.app.RestartSS()
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- L2TP ---

func (s *Server) getL2TPConfig(w http.ResponseWriter, r *http.Request) {
	capOK, capReason := app.CheckL2TPCapability()
	capable := capOK
	hostNet := app.CheckHostNetwork()
	cfg := s.app.Store().Get()
	result := map[string]any{
		"capable":      capable,
		"host_network": hostNet,
		// Surface the precise reason CheckL2TPCapability turned us down
		// (cgroup blocking /dev/ppp, l2tp_ppp module missing, NET_ADMIN
		// not granted, …). The UI used to fall back to a hard-coded
		// "needs --cap-add NET_ADMIN and --network host" message which
		// was wrong when both were already granted; with reason exposed
		// the banner can name the actual gap.
		"reason": capReason,
	}
	if cfg.L2TP != nil {
		result["listen"] = cfg.L2TP.Listen
		result["enabled"] = cfg.L2TP.Enabled
		result["pool"] = cfg.L2TP.Pool
		result["psk"] = cfg.L2TP.PSK
		result["proxy_port"] = cfg.L2TP.ProxyPort
		result["mtu"] = cfg.L2TP.MTU
	} else {
		result["listen"] = "1701"
		result["enabled"] = false
		result["pool"] = "192.168.25.1/24"
		result["psk"] = ""
		result["proxy_port"] = 12345
		result["mtu"] = 1280
	}
	writeJSON(w, result)
}

func (s *Server) updateL2TPConfig(w http.ResponseWriter, r *http.Request) {
	if ok, reason := app.CheckL2TPCapability(); !ok {
		http.Error(w, "L2TP unavailable: "+reason, 403)
		return
	}
	var l2tp app.L2TPConfig
	if err := json.NewDecoder(r.Body).Decode(&l2tp); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.app.Store().Update(func(c *app.Config) {
		c.L2TP = &l2tp
	})
	// Hot reload L2TP
	go func() {
		if err := s.app.RestartL2TP(); err != nil {
			log.Printf("[l2tp] hot reload failed: %v", err)
		}
	}()
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- IKEv2 ---

func (s *Server) getIKEv2Config(w http.ResponseWriter, r *http.Request) {
	capable := app.CheckIKEv2Capability()
	hostNet := app.CheckHostNetwork()
	// Pick up whatever reason CheckCapability returned for transparency —
	// IKEv2's own capability test is just a bool, but the underlying gate
	// (NET_ADMIN, xfrm) shares CheckCapability with L2TP and that one
	// reports a useful message.
	_, capReason := app.CheckCapability()
	cfg := s.app.Store().Get()
	result := map[string]any{
		"capable":      capable,
		"host_network": hostNet,
		"reason":       capReason,
	}
	nodeID := cfg.NodeID
	if cfg.IKEv2 != nil {
		result["enabled"] = cfg.IKEv2.Enabled
		result["mode"] = cfg.IKEv2.Mode
		result["pool"] = cfg.IKEv2.Pool
		result["cert_id"] = cfg.IKEv2.CertID
		result["psk"] = cfg.IKEv2.PSK
		result["local_id"] = cfg.IKEv2.LocalID
		result["remote_id"] = cfg.IKEv2.RemoteID
		result["default_exit"] = cfg.IKEv2.DefaultExit
		result["proxy_port"] = cfg.IKEv2.ProxyPort
		result["mtu"] = cfg.IKEv2.MTU
	} else {
		result["enabled"] = false
		result["mode"] = "mschapv2"
		result["pool"] = "192.168.26.1/24"
		result["cert_id"] = ""
		result["psk"] = ""
		result["local_id"] = nodeID
		result["remote_id"] = ""
		result["default_exit"] = ""
		result["proxy_port"] = 12350
		result["mtu"] = 1400
	}
	writeJSON(w, result)
}

func (s *Server) updateIKEv2Config(w http.ResponseWriter, r *http.Request) {
	if ok, reason := app.CheckCapability(); !ok {
		http.Error(w, "IKEv2 unavailable: "+reason, 403)
		return
	}
	var ikev2 app.IKEv2Config
	if err := json.NewDecoder(r.Body).Decode(&ikev2); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if ikev2.Mode != "mschapv2" && ikev2.Mode != "psk" {
		http.Error(w, "mode must be 'mschapv2' or 'psk'", 400)
		return
	}
	s.app.Store().Update(func(c *app.Config) {
		c.IKEv2 = &ikev2
	})
	// Hot reload IKEv2
	go func() {
		if err := s.app.RestartIKEv2(); err != nil {
			log.Printf("[ikev2] hot reload failed: %v", err)
		}
	}()
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- WireGuard ---

func (s *Server) getWireGuardConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	result := map[string]any{
		"running":   app.WireGuardRunning(),
		"connected": app.WireGuardConnectedCount(),
	}
	if cfg.WireGuard != nil {
		result["enabled"] = cfg.WireGuard.Enabled
		result["listen_port"] = cfg.WireGuard.ListenPort
		result["private_key"] = cfg.WireGuard.PrivateKey
		result["address"] = cfg.WireGuard.Address
		result["dns"] = cfg.WireGuard.DNS
		result["mtu"] = cfg.WireGuard.MTU
		result["peers"] = cfg.WireGuard.Peers
		if cfg.WireGuard.PrivateKey != "" {
			pub, _ := app.PublicKeyFromPrivate(cfg.WireGuard.PrivateKey)
			result["public_key"] = pub
		}
	} else {
		result["enabled"] = false
		result["listen_port"] = 51820
		result["private_key"] = ""
		result["address"] = "10.0.0.1/24"
		result["dns"] = ""
		result["mtu"] = 1420
		result["peers"] = []app.WireGuardPeer{}
		result["public_key"] = ""
	}
	writeJSON(w, result)
}

func (s *Server) updateWireGuardConfig(w http.ResponseWriter, r *http.Request) {
	var wg app.WireGuardConfig
	if err := json.NewDecoder(r.Body).Decode(&wg); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Preserve existing peers when updating server config
	cfg := s.app.Store().Get()
	if cfg.WireGuard != nil && len(wg.Peers) == 0 {
		wg.Peers = cfg.WireGuard.Peers
	}
	s.app.Store().Update(func(c *app.Config) {
		c.WireGuard = &wg
	})
	// Restart WireGuard
	app.StopWireGuard()
	if wg.Enabled {
		if err := s.app.StartWireGuard(wg); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) generateWGKey(w http.ResponseWriter, r *http.Request) {
	priv, pub := app.GenerateWireGuardKey()
	writeJSON(w, map[string]string{"private_key": priv, "public_key": pub})
}

func (s *Server) addWGPeer(w http.ResponseWriter, r *http.Request) {
	var peer app.WireGuardPeer
	if err := json.NewDecoder(r.Body).Decode(&peer); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(peer.ExitPaths) > 0 {
		peer.ExitVia = peer.ExitPaths[0]
	} else if peer.ExitVia != "" {
		peer.ExitPaths = []string{peer.ExitVia}
	}
	if peer.Name == "" || peer.PublicKey == "" || peer.AllowedIPs == "" {
		http.Error(w, "name, public_key, allowed_ips required", 400)
		return
	}
	s.app.Store().Update(func(c *app.Config) {
		if c.WireGuard == nil {
			c.WireGuard = &app.WireGuardConfig{}
		}
		c.WireGuard.Peers = append(c.WireGuard.Peers, peer)
	})
	// Hot-add peer without restarting device
	if err := app.WGAddPeer(peer); err != nil {
		log.Printf("[wireguard] hot-add peer %s: %v (will apply on next restart)", peer.Name, err)
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) updateWGPeer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var updated app.WireGuardPeer
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(updated.ExitPaths) > 0 {
		updated.ExitVia = updated.ExitPaths[0]
	} else if updated.ExitVia != "" {
		updated.ExitPaths = []string{updated.ExitVia}
	}
	var oldPub string
	var wgFieldsChanged bool
	s.app.Store().Update(func(c *app.Config) {
		if c.WireGuard == nil {
			return
		}
		for i, p := range c.WireGuard.Peers {
			if p.Name == name {
				oldPub = p.PublicKey
				wgFieldsChanged = p.PublicKey != updated.PublicKey ||
					p.AllowedIPs != updated.AllowedIPs ||
					p.Keepalive != updated.Keepalive
				c.WireGuard.Peers[i] = updated
				break
			}
		}
	})
	// Hot-update: if public key changed, remove old + add new; otherwise just update
	if wgFieldsChanged {
		if oldPub != updated.PublicKey && oldPub != "" {
			app.WGRemovePeer(oldPub)
			app.WGAddPeer(updated)
		} else {
			app.WGUpdatePeer(updated)
		}
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) removeWGPeer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var removedPub string
	s.app.Store().Update(func(c *app.Config) {
		if c.WireGuard == nil {
			return
		}
		peers := make([]app.WireGuardPeer, 0, len(c.WireGuard.Peers))
		for _, p := range c.WireGuard.Peers {
			if p.Name == name {
				removedPub = p.PublicKey
			} else {
				peers = append(peers, p)
			}
		}
		c.WireGuard.Peers = peers
	})
	// Hot-remove peer without restarting device
	if removedPub != "" {
		app.WGRemovePeer(removedPub)
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) downloadWGPeerConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.app.Store().Get()
	if cfg.WireGuard == nil {
		http.Error(w, "wireguard not configured", 404)
		return
	}
	var peer *app.WireGuardPeer
	for _, p := range cfg.WireGuard.Peers {
		if p.Name == name {
			peer = &p
			break
		}
	}
	if peer == nil {
		http.Error(w, "peer not found", 404)
		return
	}
	endpoint := r.URL.Query().Get("endpoint")
	dns := cfg.DNS
	if dns == "" {
		dns = "1.1.1.1, 8.8.8.8"
	}
	conf := app.GenerateWireGuardClientConfig(*cfg.WireGuard, *peer, endpoint, dns)
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.conf", name))
	w.Write([]byte(conf))
}

func (s *Server) wireGuardQR(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" {
		http.Error(w, "missing text", 400)
		return
	}
	png, err := qrcode.Encode(text, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}

// --- Sessions ---

func (s *Server) checkPorts(w http.ResponseWriter, r *http.Request) {
	var req []app.PortConflict
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	conflicts := app.CheckPorts(req)
	writeJSON(w, map[string]any{"conflicts": conflicts})
}

func (s *Server) getSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"devices": s.app.Sessions.List(),
		"total":   s.app.Sessions.Count(),
	})
}

func (s *Server) kickSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.app.Sessions.Kick(id) {
		writeJSON(w, map[string]string{"status": "ok"})
	} else {
		http.Error(w, "session not found", 404)
	}
}

// --- Users ---

func (s *Server) getUsers(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	users := cfg.Users
	if users == nil {
		users = []app.UserConfig{}
	}
	writeJSON(w, users)
}

func (s *Server) getUserConflicts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.app.GetPasswordConflicts())
}

func (s *Server) addUserAPI(w http.ResponseWriter, r *http.Request) {
	var u app.UserConfig
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(u.ExitPaths) > 0 {
		u.ExitVia = u.ExitPaths[0]
	} else if u.ExitVia != "" {
		u.ExitPaths = []string{u.ExitVia}
	}
	if u.Username == "" || u.Password == "" {
		http.Error(w, "username and password required", 400)
		return
	}
	// Block the user/server-password collision from the user-side too
	// (the inverse check lives in updateNode).
	if cur := s.app.Store().Get(); cur.Server != nil && cur.Server.Password != "" && cur.Server.Password == u.Password {
		http.Error(w, "user password collides with the hy2 server password — pick a different password", 400)
		return
	}
	if sanitized, err := app.SanitizeExitMode(u.ExitPaths, u.ExitMode); err != nil {
		http.Error(w, err.Error(), 400)
		return
	} else {
		u.ExitMode = sanitized
	}
	if u.ID == "" {
		b := make([]byte, 4)
		rand.Read(b)
		u.ID = hex.EncodeToString(b)
	}
	if err := s.app.AddUser(u); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) updateUserAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var u app.UserConfig
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	u.ID = id
	if len(u.ExitPaths) > 0 {
		u.ExitVia = u.ExitPaths[0]
	} else if u.ExitVia != "" {
		u.ExitPaths = []string{u.ExitVia}
	}
	if u.Password != "" {
		if cur := s.app.Store().Get(); cur.Server != nil && cur.Server.Password != "" && cur.Server.Password == u.Password {
			http.Error(w, "user password collides with the hy2 server password — pick a different password", 400)
			return
		}
	}
	if sanitized, err := app.SanitizeExitMode(u.ExitPaths, u.ExitMode); err != nil {
		http.Error(w, err.Error(), 400)
		return
	} else {
		u.ExitMode = sanitized
	}
	if err := s.app.UpdateUser(id, u); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) removeUserAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.app.RemoveUser(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) toggleUserAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.ToggleUser(id, body.Enabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) resetUserTrafficAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.app.ResetUserTraffic(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- Rules ---

func (s *Server) getRules(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	// Annotate each rule with its live TUN status: true only when UseTun is
	// requested AND the exit peer is TUN-capable AND the target is routable
	// (i.e. the rule is really running on the TUN path, not the proxy fallback).
	type ruleWithTun struct {
		app.RoutingRule
		TunActive bool `json:"tun_active,omitempty"`
	}
	rules := make([]ruleWithTun, len(cfg.Rules))
	for i, rr := range cfg.Rules {
		rules[i] = ruleWithTun{RoutingRule: rr}
		rules[i].TunActive = s.app.RuleUsesTun(rr)
	}
	result := map[string]any{
		"available": app.RuleEngineAvailable(),
		"rules":     rules,
	}
	writeJSON(w, result)
}

func (s *Server) addRule(w http.ResponseWriter, r *http.Request) {
	var rule app.RoutingRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(rule.ExitPaths) > 0 {
		rule.ExitVia = rule.ExitPaths[0]
	} else if rule.ExitVia != "" {
		rule.ExitPaths = []string{rule.ExitVia}
	}
	if rule.ID == "" || rule.ExitVia == "" || len(rule.Targets) == 0 {
		http.Error(w, "id, exit_via, and targets required", 400)
		return
	}
	if rule.Type != "ip" && rule.Type != "domain" {
		http.Error(w, "type must be 'ip' or 'domain'", 400)
		return
	}
	if sanitized, err := app.SanitizeExitMode(rule.ExitPaths, rule.ExitMode); err != nil {
		http.Error(w, err.Error(), 400)
		return
	} else {
		rule.ExitMode = sanitized
	}
	s.app.AddRule(rule)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var rule app.RoutingRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	rule.ID = id
	if len(rule.ExitPaths) > 0 {
		rule.ExitVia = rule.ExitPaths[0]
	} else if rule.ExitVia != "" {
		rule.ExitPaths = []string{rule.ExitVia}
	}
	if sanitized, err := app.SanitizeExitMode(rule.ExitPaths, rule.ExitMode); err != nil {
		http.Error(w, err.Error(), 400)
		return
	} else {
		rule.ExitMode = sanitized
	}
	s.app.UpdateRule(id, rule)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.app.DeleteRule(id)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) toggleRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.app.ToggleRule(id, body.Enabled)
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- TLS ---

func (s *Server) listCerts(w http.ResponseWriter, r *http.Request) {
	store := s.app.TLS()
	if store == nil {
		writeJSON(w, []any{})
		return
	}
	certs, err := store.List()
	if err != nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, certs)
}

func (s *Server) importCert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.ID == "" || body.Cert == "" {
		http.Error(w, "id and cert required", 400)
		return
	}
	if body.Name == "" {
		body.Name = body.ID
	}
	if err := s.app.TLS().Import(body.ID, body.Name, body.Cert, body.Key); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) getCertPEM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	certPEM, _ := s.app.TLS().GetPEM(id)
	keyData, _ := os.ReadFile(s.app.TLS().KeyPath(id))
	writeJSON(w, map[string]string{"cert": certPEM, "key": string(keyData)})
}

func (s *Server) importCertFromPath(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		CertPath string `json:"cert_path"`
		KeyPath  string `json:"key_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.ID == "" || body.CertPath == "" {
		http.Error(w, "id and cert_path required", 400)
		return
	}
	if body.Name == "" {
		body.Name = body.ID
	}
	certData, err := os.ReadFile(body.CertPath)
	if err != nil {
		http.Error(w, "cannot read cert file: "+err.Error(), 400)
		return
	}
	var keyData []byte
	if body.KeyPath != "" {
		keyData, err = os.ReadFile(body.KeyPath)
		if err != nil {
			http.Error(w, "cannot read key file: "+err.Error(), 400)
			return
		}
	}
	if err := s.app.TLS().Import(body.ID, body.Name, string(certData), string(keyData)); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) generateCert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Domains []string `json:"domains"`
		Days    int      `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.ID == "" || len(body.Domains) == 0 {
		http.Error(w, "id and domains required", 400)
		return
	}
	if body.Name == "" {
		body.Name = body.ID
	}
	if body.Days <= 0 {
		body.Days = 365
	}
	if err := s.app.TLS().Generate(body.ID, body.Name, body.Domains, body.Days); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) signCertWithCA(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CAID string `json:"ca_id"`
		ID   string `json:"id"`
		Name string `json:"name"`
		CN   string `json:"cn"`
		Days int    `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.CAID == "" || body.ID == "" || body.CN == "" {
		http.Error(w, "ca_id, id, and cn required", 400)
		return
	}
	if body.Name == "" {
		body.Name = body.CN
	}
	if body.Days <= 0 {
		body.Days = 7300
	}
	if err := s.app.TLS().SignWithCA(body.CAID, body.ID, body.Name, body.CN, body.Days); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) deleteCert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.app.TLS().Delete(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// remoteProxy tunnels HTTP requests to a remote node's web UI through the relay chain.
// Path: /remote/{peer1}/{peer2}/.../{remaining_path}
// The last contiguous known-peer segment forms the chain; the rest is the proxied path.
// remoteProxy tunnels HTTP to a remote node's web UI.
// URL: /remote/{peer1}/{peer2}/.../{remaining}
// Maps to remote: /scale/{remaining} (auto-prepends remote's base path)
// Rewrites __BASE__ in HTML so JS API calls route back through the proxy.
func (s *Server) remoteProxy(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, "/remote/")
	if raw == "" || raw == "/" {
		http.Error(w, "usage: /remote/{peer}/{path}", 400)
		return
	}

	// Split into chain + remaining. Chain = contiguous peer names (no dots, no slashes in names).
	// First segment with a dot or known as a file extension ends the chain.
	segments := strings.Split(strings.TrimSuffix(raw, "/"), "/")
	// Known path prefixes that mark the end of the peer chain.
	// The frontend builds links like "/scale/remote/{peer}/scale/..." so
	// "scale" must be recognized as a base-path sentinel, not a peer name.
	notPeer := map[string]bool{
		"api": true, "login": true, "nodes": true, "proxies": true,
		"tls": true, "settings": true, "remote": true,
		"scale": true, "users": true, "rules": true, "assets": true,
	}
	chainLen := 0
	for _, seg := range segments {
		if seg == "" || strings.Contains(seg, ".") || notPeer[seg] {
			break
		}
		chainLen++
	}
	if chainLen == 0 {
		http.Error(w, "missing peer chain", 400)
		return
	}

	chain := segments[:chainLen]
	// Resolve old node IDs to current ones
	s.mu.RLock()
	for i, name := range chain {
		if newID, ok := s.oldNodeIDs[name]; ok {
			chain[i] = newID
		}
	}
	s.mu.RUnlock()
	// Frontend emits /scale/remote/{chain}/scale/{remaining}. The literal
	// "scale" sentinel after the chain is the remote's base-path marker;
	// strip it so we don't end up with /scale/scale/... after prepending.
	rest := segments[chainLen:]
	if len(rest) > 0 && rest[0] == "scale" {
		rest = rest[1:]
	}
	remaining := strings.Join(rest, "/")
	// The remote's actual path: prepend their base path (/scale)
	// /remote/vm/ → remote /scale/
	// /remote/vm/scale/login → remote /scale/login
	// /remote/vm/scale/api/node → remote /scale/api/node
	remotePath := "/scale/" + remaining

	// Proxy base: what the remote's __BASE__ should point to
	proxyBase := s.basePath + "/remote/" + strings.Join(chain, "/")

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var conn net.Conn
	var err error
	if len(chain) == 1 {
		conn, err = s.app.Node().DialTCP(ctx, chain[0], relay.StreamAPI)
	} else {
		conn, err = s.app.Node().DialVia(ctx, chain, relay.StreamAPI)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("tunnel to %s failed: %v", strings.Join(chain, "/"), err), 502)
		return
	}
	defer conn.Close()

	// Construct proxied request. Use a fresh header so we don't forward the
	// local API token (remote node authenticates independently) or Hop-by-Hop
	// headers that confuse it. Force HTTP/1.1 with Connection: close so the
	// remote sends a complete response-with-body and closes cleanly.
	outReq, _ := http.NewRequest(r.Method, "http://127.0.0.1:5565"+remotePath, r.Body)
	outReq.URL.RawQuery = r.URL.RawQuery
	outReq.Proto = "HTTP/1.1"
	outReq.ProtoMajor = 1
	outReq.ProtoMinor = 1
	outReq.Host = "127.0.0.1:5565"
	// Copy request headers. Forward Authorization so the client's remote
	// session token (stored in localStorage after the user logs into the
	// remote UI) reaches the target — the local node's token is never
	// used here because the frontend scopes tokens per __BASE__. Drop
	// hop-by-hop headers that can corrupt the proxied request.
	for k, vv := range r.Header {
		lk := strings.ToLower(k)
		if lk == "connection" || lk == "proxy-connection" ||
			lk == "keep-alive" || lk == "te" || lk == "trailers" || lk == "transfer-encoding" {
			continue
		}
		outReq.Header[k] = vv
	}
	outReq.Header.Set("X-Hy2scale-Proxy", "true")
	outReq.Close = true // force Connection: close in serialization
	outReq.ContentLength = r.ContentLength

	if err := outReq.Write(conn); err != nil {
		http.Error(w, "write to remote failed: "+err.Error(), 502)
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), outReq)
	if err != nil {
		http.Error(w, "bad response from remote: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	// Rewrite __BASE__ in HTML responses so JS API calls route through proxy
	ct := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(ct, "text/html")

	for k, vv := range resp.Header {
		lk := strings.ToLower(k)
		// Drop hop-by-hop headers and framing headers. Content-Length and
		// Transfer-Encoding from upstream are invalidated because:
		//   (a) we rewrite HTML bodies (length changes), and
		//   (b) we serve on a different HTTP/1.1 connection with its own
		//       framing that Go's net/http decides based on what we write.
		// Keeping the upstream Content-Length causes Go to reject Write()
		// calls that exceed it with "wrote more than the declared
		// Content-Length" and send zero body bytes to the client.
		if lk == "content-length" || lk == "transfer-encoding" ||
			lk == "connection" || lk == "keep-alive" ||
			lk == "proxy-connection" || lk == "trailer" || lk == "te" {
			continue
		}
		// Rewrite Location header for redirects
		if lk == "location" {
			for i, v := range vv {
				// Rewrite /scale/... → /scale/remote/{chain}/...
				if strings.HasPrefix(v, "/scale/") {
					vv[i] = proxyBase + "/" + strings.TrimPrefix(v, "/scale/")
				}
			}
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if isHTML {
		body, _ := io.ReadAll(resp.Body)
		// Rewrite __BASE__ to proxy path and inject __PROXY__
		html := strings.Replace(string(body),
			"window.__BASE__=\"/scale\"",
			"window.__BASE__=\""+proxyBase+"\";window.__PROXY__=true",
			1)
		w.Write([]byte(html))
	} else {
		io.Copy(w, resp.Body)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// --- Backup / Restore ---

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	dataDir := s.app.DataDir()
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", "attachment; filename=hy2scale-backup.tar")
	tw := tar.NewWriter(w)
	defer tw.Close()
	filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dataDir, path)
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			io.Copy(tw, f)
		}
		return nil
	})
}

func (s *Server) uploadRestore(w http.ResponseWriter, r *http.Request) {
	dataDir := s.app.DataDir()

	// Read tar from request body (limit 50MB)
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	tr := tar.NewReader(r.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "invalid tar: "+err.Error(), 400)
			return
		}
		target := filepath.Join(dataDir, filepath.Clean(hdr.Name))
		// Security: ensure target is within dataDir
		if !strings.HasPrefix(target, filepath.Clean(dataDir)+string(os.PathSeparator)) && target != filepath.Clean(dataDir) {
			continue
		}
		if hdr.Typeflag == tar.TypeDir {
			os.MkdirAll(target, 0755)
		} else {
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.Create(target)
			if err != nil {
				continue
			}
			io.Copy(f, tr)
			f.Close()
		}
	}
	log.Printf("[backup] config restored from upload, restarting...")
	writeJSON(w, map[string]string{"status": "ok"})

	// Restart the process after a short delay
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0) // container/systemd will restart the process
	}()
}

func isRunningInDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	// Fallback: check cgroup for docker/containerd
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		if strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "/lxc/") {
			return true
		}
	}
	return false
}

func (s *Server) getSystemArch(w http.ResponseWriter, r *http.Request) {
	self, _ := os.Executable()
	writeJSON(w, map[string]any{
		"os":        runtime.GOOS,
		"arch":      runtime.GOARCH,
		"version":   Version,
		"binary":    filepath.Base(self),
		"in_docker": isRunningInDocker(),
	})
}

// getBuildInfo returns the project's license metadata and the full
// dependency list (Go module graph compiled in + the significant
// native bundles that ship in the Docker image). Exposed to the web
// UI so users can see exactly what third-party code is running and
// under which licence, without having to inspect the binary.
func (s *Server) getBuildInfo(w http.ResponseWriter, r *http.Request) {
	type dep struct {
		Path    string `json:"path"`
		Version string `json:"version"`
	}
	type native struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		License string `json:"license"`
		Source  string `json:"source"`
	}
	goDeps := []dep{}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, m := range info.Deps {
			// Skip replaced / anonymous — keep the resolved identity.
			resolved := m
			if m.Replace != nil {
				resolved = m.Replace
			}
			if resolved.Path == "" || resolved.Version == "" {
				continue
			}
			goDeps = append(goDeps, dep{Path: resolved.Path, Version: resolved.Version})
		}
	}
	sort.Slice(goDeps, func(i, j int) bool { return goDeps[i].Path < goDeps[j].Path })
	// Native components bundled into the Docker image but not tracked
	// by the Go module graph. Kept short and hand-maintained — these
	// are the pieces whose licences directly constrain the umbrella
	// licence of this project.
	natives := []native{
		{Name: "strongSwan", Version: "5.8.4", License: "GPL-2.0-or-later", Source: "https://www.strongswan.org/"},
		{Name: "iptables (nf_tables backend)", Version: "distro-packaged", License: "GPL-2.0-or-later", Source: "https://www.netfilter.org/projects/iptables/"},
		{Name: "xl2tpd", Version: "distro-packaged", License: "GPL-2.0-or-later", Source: "https://www.xelerance.com/software/xl2tpd/"},
	}
	writeJSON(w, map[string]any{
		"version":     Version,
		"license":     "GPL-3.0-or-later",
		"repository":  "https://github.com/FrankoonG/hy2scale",
		"go_deps":     goDeps,
		"natives":     natives,
	})
}

func (s *Server) uploadUpgrade(w http.ResponseWriter, r *http.Request) {
	if !isRunningInDocker() {
		http.Error(w, "upgrade via web is only available in Docker deployments", 403)
		return
	}
	// Limit to 200MB
	r.Body = http.MaxBytesReader(w, r.Body, 200<<20)

	tmp, err := os.CreateTemp("", "hy2scale-upload-*.tar.gz")
	if err != nil {
		http.Error(w, "temp file: "+err.Error(), 500)
		return
	}
	if _, err := io.Copy(tmp, r.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		http.Error(w, "save: "+err.Error(), 500)
		return
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	if err := applyTarball(tmp.Name()); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	log.Printf("[upgrade] binary upgraded from upload, restarting...")
	writeJSON(w, map[string]string{"status": "ok", "message": "upgrade successful, restarting"})

	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

// applyTarball extracts the matching platform binary from a gzipped tar
// archive at path and atomically swaps it into place, leaving the previous
// binary as <self>.bak. Caller is responsible for triggering os.Exit so
// the process restarts on the new binary. Used by both the manual upload
// path (uploadUpgrade) and the online-update path
// (applyDownloadedUpgrade) so the two stay in lock-step.
func applyTarball(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("invalid gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	expectedArch := runtime.GOOS + "-" + runtime.GOARCH
	expectedBin := "hy2scale"

	var tmpPath string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(hdr.Name)
		isMatch := name == expectedBin ||
			name == expectedBin+"-"+expectedArch ||
			name == expectedBin+"_"+expectedArch
		if !isMatch {
			continue
		}
		tmp, err := os.CreateTemp("", "hy2scale-upgrade-*")
		if err != nil {
			return fmt.Errorf("temp file: %w", err)
		}
		if _, err := io.Copy(tmp, tr); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return fmt.Errorf("extract: %w", err)
		}
		tmp.Close()
		os.Chmod(tmp.Name(), 0755)
		tmpPath = tmp.Name()
		break
	}
	if tmpPath == "" {
		return fmt.Errorf("no matching binary for %s found in archive", expectedArch)
	}

	// Smoke test — non-fatal because some builds may not support --version.
	out, _ := exec.Command(tmpPath, "--version").CombinedOutput()
	_ = out

	self, err := os.Executable()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("cannot find self: %w", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	bakPath := self + ".bak"
	os.Remove(bakPath)
	if err := os.Rename(self, bakPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("backup old binary: %w", err)
	}
	if err := os.Rename(tmpPath, self); err != nil {
		os.Rename(bakPath, self)
		return fmt.Errorf("install new binary: %w", err)
	}
	os.Chmod(self, 0755)
	return nil
}

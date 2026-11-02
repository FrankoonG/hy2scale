package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/internal/app"
	"github.com/FrankoonG/hy2scale/internal/web"
)

type Server struct {
	app      *app.App
	addr     string
	basePath string

	mu         sync.RWMutex
	username   string
	passHash   string // SHA-256 hex of password
	sessions   map[string]time.Time
	oldNodeIDs map[string]string

	// Graph-canvas SSE subscribers. Each entry is a buffered channel
	// receiving full-snapshot updates; slow consumers drop messages
	// rather than blocking the write handlers. Protected by layoutMu.
	layoutMu     sync.Mutex
	layoutSubs   map[uint64]chan []app.GraphCanvas
	layoutSubSeq uint64

	// Build ID derived from the embedded index.html hash. Frontend polls
	// /api/build-id and reloads on mismatch so a server rebuild doesn't
	// leave long-lived tabs running stale code.
	buildID string

	// Online-update singleton. Shared across all sessions so that two
	// browser tabs see the same download progress instead of triggering
	// duplicate downloads. See internal/api/upgrade.go.
	upgrade *upgradeJob

	// Web UI TLS (https_cert_id / force_https), read live. Set in Start.
	web *webTLS

	// restart asks main to shut down and come back (SetRestart).
	restart func()
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
	// Same pattern for the DNS resolver — install at startup so the
	// next ResolveViaExit call sees the operator-configured snapshot.
	app.ApplyDNSResolver(cfg.DNSResolver)
	return &Server{
		app:        a,
		addr:       addr,
		basePath:   bp,
		username:   username,
		passHash:   passHash,
		oldNodeIDs: make(map[string]string),
		layoutSubs: make(map[uint64]chan []app.GraphCanvas),
		sessions:   make(map[string]time.Time),
		upgrade:    newUpgradeJob(),
	}
}

func (s *Server) Start(ctx context.Context) error {
	apiMux := http.NewServeMux()

	// Login (no auth)
	apiMux.HandleFunc("POST /api/login", s.login)
	// Relay-passthrough token mint: a peer that already proved itself
	// with our system password (authID == "system" on the relay-API
	// stream) can ask for a session token here, bypassing the local
	// web password — but only when RelayAdminPassthrough is enabled.
	// Used by RemoteConnectModal so that the hub's "open remote UI"
	// flow on a passthrough-trusted peer doesn't prompt for that
	// peer's web password just to mint the token the new tab needs.
	apiMux.HandleFunc("POST /api/relay-passthrough-token", s.relayPassthroughToken)
	// Shared-user pushes from a manager node: relay trust only (own gate).
	s.sharedPushRoutes(apiMux)
	// Internal peer list (no auth, used for reverse nested discovery)
	apiMux.HandleFunc("GET /api/internal/peers", s.internalPeers)
	// Build-id probe (no auth) — a long-lived SPA tab polls this to detect
	// server rebuild/redeploy and reload itself. Cheap and stateless.
	apiMux.HandleFunc("GET /api/build-id", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		writeJSON(w, map[string]string{"build_id": s.buildID})
	})

	// Authed API routes: the core's, then each add-on's.
	authed := http.NewServeMux()
	s.coreRoutes(authed)
	s.userRoutes(authed)
	s.proxyRoutes(authed)
	s.endpointRoutes(authed)
	s.webRoutes(authed)
	s.publicIPRoutes(authed)
	s.geoipRoutes(authed)
	s.policySystemRoutes(authed)
	s.shellRoutes(authed)
	s.toolsRoutes(authed)
	s.storageRoutes(authed)

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
		"":           true,
		"login":      true,
		"overview":   true,
		"nodes":      true,
		"proxies":    true,
		"users":      true,
		"tls":        true,
		"rules":      true,
		"interfaces": true,
		"endpoints":  true,
		"logs":       true,
		"settings":   true,
		"tools":      true,
		"audit":      true,
	}

	// Remote node proxy — no local auth, remote handles its own
	// Block if accessed through proxy (prevent proxy chaining)
	// /remote/ is the relay-tunneling proxy from this hub to a managed
	// peer. It MUST be hub-authenticated: an unauthenticated /remote/
	// would let any external HTTP caller use this hub as a free
	// admin tunnel into every passthrough-trusted peer (the remote's
	// authMiddleware bypasses on system-relay + passthrough, so an
	// anonymous /remote/<peer>/scale/api/node would otherwise return
	// the peer's full server config including its system password).
	apiMux.Handle("/remote/", s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hy2scale-Proxy") == "true" {
			http.Error(w, "proxy chaining not allowed", 403)
			return
		}
		s.remoteProxy(w, r)
	})))

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
				if strings.HasPrefix(path, "assets/geo/") {
					// Map data (megabytes, fixed per build): cached, gzipped.
					w.Header().Set("Cache-Control", "public, max-age=86400")
					w.Header().Add("Vary", "Accept-Encoding")
					if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
						w.Header().Set("Content-Encoding", "gzip")
						w.Write(gzipStatic(path, data))
						return
					}
					w.Write(data)
					return
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
	s.web = &webTLS{store: s.app.TLS(), conf: s.app.Store().Get}
	srv := &http.Server{Handler: s.web.redirect(root)}
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
	// Wrap API streams arriving via the s2c-ctrl dial-request path
	// (inbound-only peer) with a RelayAuthConn carrying the peer's
	// effective auth ID. nodeOutbound.TCP already does the equivalent
	// for the direct outbound-peer path. Without this hook the api
	// server's relayConnContext sees a plain conn on the inbound-only
	// branch, relayCtxKey stays nil, and relayPassthroughToken's Gate 1
	// fires 404 — which is exactly the
	// "remote-connect from inbound peer redirects to /login" symptom.
	s.app.Node().SetAPIStreamWrap(func(stream net.Conn, authID string) net.Conn {
		return &app.RelayAuthConn{Conn: stream, AuthID: authID}
	})
	relaySrv := &http.Server{
		Handler:     root,
		ConnContext: relayConnContext,
	}
	go func() { <-ctx.Done(); relaySrv.Close() }()
	go relaySrv.Serve(apiBridge)

	return srv.Serve(newSniffListener(ln, s.web))
}

// --- Auth ---

// --- UI Settings (port, base path) ---

// --- Node ---

// Version is the application version. Update this on each release.
const Version = "1.4.2"

func init() {
	app.AppVersion = Version
	relay.NodeVersion = Version
}

// --- Peers ---

// --- Clients ---

// --- Proxies ---

// --- SS ---

// --- L2TP / IKEv2 / OpenVPN (userspace servers, always available) ---

// --- WireGuard ---

// --- Sessions ---

// --- Users ---

// --- Rules ---

// --- TLS ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// --- Backup / Restore ---

var gzipCache sync.Map // static path → gzipped bytes

// gzipStatic: the gzip encoding of an embedded static file, made once.
func gzipStatic(path string, data []byte) []byte {
	if v, ok := gzipCache.Load(path); ok {
		return v.([]byte)
	}
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	zw.Write(data)
	zw.Close()
	gzipCache.Store(path, b.Bytes())
	return b.Bytes()
}

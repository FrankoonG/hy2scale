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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"compress/gzip"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/proto"
	"github.com/FrankoonG/hy2scale/internal/app"
	"github.com/FrankoonG/hy2scale/core/logbuf"
	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/internal/selfupdate"
	"github.com/FrankoonG/hy2scale/internal/web"
	qrcode "github.com/skip2/go-qrcode"
)

// topoSubPeer: a node of the nested peer tree (core/node).
type topoSubPeer = relay.TreePeer

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

// SetRestart installs the process restart used by POST /api/system/restart.
func (s *Server) SetRestart(fn func()) { s.restart = fn }

// restartProcess replies, then restarts the node (same path as a stop, so
// counters and history are flushed first).
func (s *Server) restartProcess(w http.ResponseWriter, r *http.Request) {
	if s.restart == nil {
		http.Error(w, "restart not available", http.StatusNotImplemented)
		return
	}
	log.Printf("[api] restart requested from the web UI")
	writeJSON(w, map[string]string{"status": "ok"})
	go func() {
		time.Sleep(300 * time.Millisecond) // let the reply go out
		s.restart()
	}()
}

// normalizeClient validates a node entry: manual addresses and/or punch
// routes. An entry with punch routes only is keyed by its first punch route.
func normalizeClient(cl *app.ClientEntry) error {
	if p := cl.Punch; p != nil {
		if len(p.Signal) == 0 && len(p.Reflectors) == 0 && len(p.Binds) == 0 {
			cl.Punch = nil
		} else {
			if len(p.Signal) == 0 {
				return fmt.Errorf("punch needs at least one signalling path")
			}
			for _, path := range p.Signal {
				hops := strings.Split(path, "/")
				if hops[len(hops)-1] != cl.Name {
					return fmt.Errorf("signalling path %q must end at %q", path, cl.Name)
				}
				for _, h := range hops {
					if h == "" {
						return fmt.Errorf("invalid signalling path %q", path)
					}
				}
			}
			if len(p.Reflectors) == 0 {
				return fmt.Errorf("punch needs at least one reflector")
			}
			for _, r := range p.Reflectors {
				if r == "" || strings.Contains(r, "/") || r == cl.Name {
					return fmt.Errorf("invalid reflector %q", r)
				}
			}
			if !app.ValidPunchFamily(p.Family) {
				return fmt.Errorf("invalid punch family %q", p.Family)
			}
			for _, b := range p.Binds {
				if b == "" {
					continue
				}
				_, _, src, err := app.SplitAddrBind("x:1#" + b)
				if err != nil {
					return err
				}
				if src != "" && (strings.Contains(src, ":") != (p.Family == app.PunchFamilyIPv6)) {
					return fmt.Errorf("punch source %s does not match family %s", src, punchFamilyName(p.Family))
				}
			}
		}
	}
	manual := cl.Addrs
	if len(manual) == 0 && cl.Addr != "" && !app.IsPunchAddr(cl.Addr) {
		manual = []string{cl.Addr}
	}
	for _, a := range manual {
		if !validAddrSpec(a) || app.IsPunchAddr(a) {
			return fmt.Errorf("invalid address format %q", a)
		}
	}
	switch {
	case len(manual) > 0:
		cl.Addr, cl.Addrs = manual[0], manual
		if cl.Password == "" {
			return fmt.Errorf("password required")
		}
	case cl.Punch != nil:
		// punch only: the token is the password; the key is the first route
		cl.Addrs = nil
		if !app.IsPunchAddr(cl.Addr) {
			cl.Addr = cl.AllAddrs()[0]
		}
	default:
		return fmt.Errorf("an address or a punch route is required")
	}
	return nil
}

// validAddrSpec checks "host:portspec" where portspec can be "5565", "1000,2000", "20000-30000".
// An IPv6 host must be bracketed ("[2001:db8::1]:5565"); a bare IPv6
// address is ambiguous with the port and is refused.
func validAddrSpec(addr string) bool {
	if i := strings.LastIndexByte(addr, '#'); i >= 0 {
		if _, _, _, err := app.SplitAddrBind(addr); err != nil {
			return false
		}
		addr = addr[:i]
	}
	// SplitHostPort refuses unbracketed IPv6 ("too many colons") and
	// unbalanced brackets; the port part may be a list / ranges.
	host, portSpec, err := net.SplitHostPort(addr)
	if err != nil || host == "" || portSpec == "" {
		return false
	}
	if strings.ContainsAny(host, "[]/# ") {
		return false
	}
	if strings.Contains(host, ":") || strings.HasPrefix(addr, "[") {
		// bracketed: must be an IPv6 address (with an optional zone)
		h, _, _ := strings.Cut(host, "%")
		if net.ParseIP(h) == nil {
			return false
		}
	}
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

func punchFamilyName(f string) string {
	if f == "" {
		return app.PunchFamilyIPv4
	}
	return f
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
		layoutSubs:    make(map[uint64]chan []app.GraphCanvas),
		sessions: make(map[string]time.Time),
		upgrade:  newUpgradeJob(),
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
	authed.HandleFunc("GET /api/me", s.getMe)
	authed.HandleFunc("GET /api/stats", s.getStats)
	authed.HandleFunc("PUT /api/node", s.updateNode)
	authed.HandleFunc("GET /api/peers", s.getPeers)
	authed.HandleFunc("GET /api/topology", s.getTopology)
	authed.HandleFunc("GET /api/paths/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.app.PathStatuses()) })
	authed.HandleFunc("GET /api/graph-canvases", s.getGraphCanvases)
	authed.HandleFunc("GET /api/graph-canvases/stream", s.streamGraphCanvases)
	authed.HandleFunc("PUT /api/graph-canvases/{id}", s.putGraphCanvas)
	authed.HandleFunc("DELETE /api/graph-canvases/{id}", s.deleteGraphCanvas)
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
	authed.HandleFunc("GET /api/punch/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.app.PunchStatuses()) })
	authed.HandleFunc("GET /api/system/interfaces", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.app.BindableInterfaces()) })
	s.registerInterfaceRoutes(authed)
	authed.HandleFunc("GET /api/openvpn", s.getOpenVPNConfig)
	authed.HandleFunc("PUT /api/openvpn", s.updateOpenVPNConfig)
	authed.HandleFunc("GET /api/openvpn/profile", s.downloadOpenVPNProfile)

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

	// Per-peer relay diagnostics — three axes of state we wish we'd had
	// during the cn-xinchang +82ms drift investigation. None of these
	// touch peer state; all are read-only counters / rings on the live
	// peer struct.
	//   GET /api/diag/peer-health   QUIC-tunnel proxy: ping ok/fail,
	//                                consecutive failCount, tx/rx bytes
	//   GET /api/diag/peer-rebinds  per-peer count of [via] rebind-probe
	//                                arrivals — chronic per-stream
	//                                disruption signal
	//   GET /api/diag/peer-latency  rolling time series of recent
	//                                latency_ms samples (~10 min ring),
	//                                so an operator can pinpoint when
	//                                a peer's RTT started creeping up
	authed.HandleFunc("GET /api/diag/peer-health", s.diagPeerHealth)
	authed.HandleFunc("GET /api/diag/peer-rebinds", s.diagPeerRebinds)
	authed.HandleFunc("GET /api/diag/peer-latency", s.diagPeerLatency)
	authed.HandleFunc("GET /api/diag/peer-history", s.diagPeerHistory)
	//   GET /api/diag/runtime       goroutine / heap / relay-object counts,
	//                                the first thing to read when a node's
	//                                memory keeps growing
	authed.HandleFunc("GET /api/diag/runtime", s.diagRuntime)
	authed.HandleFunc("GET /api/logs", s.handleLogs)

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
	authed.HandleFunc("POST /api/system/restart", s.restartProcess)
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
// trustedRelay: a relay-delivered API request whose connection is trusted
// for management — a peer node (system password) or a node-access user in
// full mode (the gateway tags its streams "access:<user>").
func trustedRelay(authID string) bool {
	return authID == "system" || strings.HasPrefix(authID, "access:")
}

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

// relayPassthroughToken issues a session token to a caller that has
// already authenticated with our system password on the relay-API
// stream — but only when RelayAdminPassthrough is on. This lets a
// trusted upstream peer (the operator's hub) open this node's web UI
// without re-entering the local web password.
//
// SECURITY — this endpoint mints a token without checking a password,
// so a public-listener request MUST never be able to reach a success
// path. Three independent gates enforce that:
//
//  1. Public-listener path: the public TCP server (srv) has no
//     ConnContext hook, so r.Context().Value(relayCtxKey{}) is nil.
//     We bail with 404 — same response shape any unmapped path gets,
//     so the endpoint isn't even discoverable from the open internet.
//  2. Relay-listener path with non-system auth (e.g. a user-level
//     `user:<name>` relay caller): rejected with 403.
//  3. Operator hasn't opted in to RelayAdminPassthrough on THIS node:
//     rejected with 403, regardless of who's calling.
//
// Only when all three pass do we mint and return a session token.
func (s *Server) relayPassthroughToken(w http.ResponseWriter, r *http.Request) {
	// Gate 1: the relay-context value MUST be present. Absence proves
	// the request came in on the public listener, never the apiBridge,
	// so the caller never authenticated with our system password and
	// must not be able to mint a token here. 404 instead of 401 to
	// hide the endpoint from external scans.
	rawCtx := r.Context().Value(relayCtxKey{})
	if rawCtx == nil {
		http.NotFound(w, r)
		return
	}
	// Gate 2: relay-routed, but must be system-level (peer system
	// password). User-level relay traffic doesn't grant admin even
	// with passthrough on.
	authID, _ := rawCtx.(string)
	if !trustedRelay(authID) {
		http.Error(w, "forbidden", 403)
		return
	}
	// Gate 3: operator must have explicitly enabled passthrough.
	if !s.app.Store().Get().RelayAdminPassthrough {
		http.Error(w, "passthrough disabled", 403)
		return
	}
	token := s.generateToken()
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(s.sessionTimeout())
	s.mu.Unlock()
	writeJSON(w, map[string]any{"token": token})
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
		if trustedRelay(relayAuthID(r.Context())) && s.app.Store().Get().RelayAdminPassthrough {
			next.ServeHTTP(w, r)
			return
		}
		// Collect every token the request might carry, then accept the
		// first one we recognize. The Authorization header, the
		// `?token=` query (used by SSE / EventSource which can't set
		// headers), and the `hy2_session` cookie (used by new tabs
		// that can't pre-populate sessionStorage) are all valid carriers
		// of the same secret. They're listed in parallel rather than
		// "first non-empty wins" because a remote-proxy tab carries
		// THREE different tokens at once: its own remote-node Bearer
		// (for the eventual remote authMiddleware), and its hub-origin
		// cookie (for THIS hub's /remote/ gate). The hub gate must
		// succeed on the cookie even when an unrecognized Bearer is
		// also present.
		candidates := make([]string, 0, 3)
		if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
			candidates = append(candidates, h[7:])
		}
		if q := r.URL.Query().Get("token"); q != "" {
			candidates = append(candidates, q)
		}
		if c, err := r.Cookie("hy2_session"); err == nil && c.Value != "" {
			candidates = append(candidates, c.Value)
		}
		var matched string
		s.mu.RLock()
		for _, t := range candidates {
			if expiry, ok := s.sessions[t]; ok && time.Now().Before(expiry) {
				matched = t
				break
			}
		}
		s.mu.RUnlock()
		if matched == "" {
			http.Error(w, "unauthorized", 401)
			return
		}
		// Refresh session on activity
		s.mu.Lock()
		s.sessions[matched] = time.Now().Add(s.sessionTimeout())
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// getMe returns the signed-in web user (shown in the top bar user menu).
func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	u := s.username
	s.mu.RUnlock()
	writeJSON(w, map[string]string{"username": u})
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
	lim := s.app.Tree().Limits() // current runtime values (post-clamp + env override)
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
		// plain http is being redirected right now (force on + usable cert)
		"https_active":       s.web != nil && s.web.forced(),
		"session_timeout_h":  sessionH,
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
		Listen           *string `json:"listen"`
		BasePath         *string `json:"base_path"`
		DNS              *string `json:"dns"`
		ForceHTTPS       *bool   `json:"force_https"`
		HTTPSCertID      *string `json:"https_cert_id"`
		SessionTimeoutH  *int    `json:"session_timeout_h"`
		PathLossGraceS   *int    `json:"path_loss_grace_s"`
		MaxNestedDepth   *int    `json:"max_nested_depth"`
		MaxResponseNodes *int    `json:"max_response_nodes"`
		MaxCacheEntries  *int    `json:"max_cache_entries"`
		MaxResponseBytes *int    `json:"max_response_bytes"`
		MaxFetchFanOut   *int    `json:"max_fetch_fan_out"`
		RelayAdminPassthrough *bool `json:"relay_admin_passthrough"`
		AcceptPunch           *bool `json:"accept_punch"`
		// DNS resolver (relay-routed) — see app.DNSResolverConfig docs.
		// No separate upstream knob: the resolver uses the top-level
		// `dns` field above, same as VPN-client DNS push does.
		DNSResolverEnabled        *bool `json:"dns_resolver_enabled"`
		DNSResolverCacheTTL       *int  `json:"dns_resolver_cache_ttl"`
		DNSResolverNegativeTTL    *int  `json:"dns_resolver_negative_ttl"`
		DNSResolverCacheSize      *int  `json:"dns_resolver_cache_size"`
		DNSResolverQueryTimeoutMs *int  `json:"dns_resolver_query_timeout_ms"`
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

// diagPeerHealth — ping ok/fail counters per peer (QUIC-tunnel
// application-level proxy). The hysteria.Client interface doesn't
// expose raw quic-go retransmit / RTT-srtt / pacer-bandwidth, so we
// surface the application-level shape of the same problem: ping
// stream success rate, consecutive failures, and recent byte counts.
// Cumulative since the peer's QUIC session began (counters reset on
// disconnect, not per-window).
func (s *Server) diagPeerHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"peers": s.app.Node().HealthSnapshots()})
}

// diagPeerRebinds — count of [via] rebind-probe arrivals at each
// peer. A stream-level signal: when a peer's QUIC connection
// experiences chronic per-stream disruption (without the connection
// itself dying), downstream streamBridge instances send rebind
// probes at high cadence. Spikes here are the canonical "this one
// peer's tunnel is unhappy but won't admit it" signal.
func (s *Server) diagPeerRebinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"peers": s.app.Node().RebindSnapshots()})
}

// diagPeerLatency — rolling time series of recent latency_ms samples
// per peer (~10 min ring at the 5 s probe cadence). Lets an operator
// pinpoint when a peer's RTT started drifting up, instead of seeing
// only the current point value the topology API surfaces.
func (s *Server) diagPeerLatency(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"peers": s.app.Node().LatencySnapshots()})
}

var startTime = time.Now()

// diagRuntime — process-level counters for leak hunting: goroutines,
// heap, GC, and the relay's own live-object counts. Read-only and cheap
// (one ReadMemStats); safe to poll every few seconds.
func (s *Server) diagRuntime(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st := s.app.Node().GetStats()
	writeJSON(w, map[string]any{
		"goroutines":     runtime.NumGoroutine(),
		"heap_alloc":     ms.HeapAlloc,
		"heap_inuse":     ms.HeapInuse,
		"heap_objects":   ms.HeapObjects,
		"stack_inuse":    ms.StackInuse,
		"sys":            ms.Sys,
		"num_gc":         ms.NumGC,
		"relay_conns":    st.Conns,
		"relay_objects":  s.app.Node().ObjectCounts(),
		"app_objects":    s.app.ObjectCounts(),
		"msess":          s.app.MsessStats(),
		"msess_paths":    s.app.MsessPaths(),
		"uptime_seconds": int64(time.Since(startTime).Seconds()),
	})
}

// handleLogs returns recent process log lines kept in memory.
// ?after=SEQ returns only newer lines; ?limit caps the count (newest kept).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	entries, last, gap := logbuf.Default.Since(after, limit)
	writeJSON(w, map[string]any{"entries": entries, "last": last, "gap": gap})
}

// diagPeerHistory — rolled-up time-bucketed metrics (1m / 1h / 1d
// precisions) backing the PathInfoExpand status bars. With ?peer=NAME
// only that peer is returned; otherwise all peers are surfaced so the
// frontend can build per-path views in one round trip.
func (s *Server) diagPeerHistory(w http.ResponseWriter, r *http.Request) {
	h := s.app.History()
	if h == nil {
		writeJSON(w, map[string]any{"peers": map[string]any{}})
		return
	}
	if name := r.URL.Query().Get("peer"); name != "" {
		writeJSON(w, map[string]any{"peer": name, "snapshot": h.Snapshot(name)})
		return
	}
	writeJSON(w, map[string]any{"peers": h.SnapshotAll()})
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
const Version = "1.3.12"

func init() {
	app.AppVersion = Version
	relay.NodeVersion = Version
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	capOK, _ := app.CheckCapability()
	limited := !capOK
	writeJSON(w, map[string]any{
		"node_id":       cfg.NodeID,
		// `name` was the legacy display label kept divergent from
		// node_id. The schema collapsed those — front-end consumers
		// that still read `name` keep working, just always equal to
		// node_id now.
		"name":          cfg.NodeID,
		"exit_node":     cfg.ExitNode,
		"server":        cfg.Server,
		"version":       Version,
		"limited":       limited,
		"compat":        app.IsCompatMode(),
		"hy2_user_auth": cfg.Hy2UserAuth,
		"active_paths":  s.app.AllActivePaths(),
		// peers below this lack lossless quality/aggregate (UI marks them)
		"lossless_min_version": app.MsessMinVersion,
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
		// node_id is the single identity. Accept either body field for
		// backward compat with old clients that still send `name`.
		if body.NodeID != nil && *body.NodeID != "" {
			c.NodeID = *body.NodeID
		} else if body.Name != nil && *body.Name != "" {
			c.NodeID = *body.Name
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
	oldWireName := s.app.Node().Name()
	s.app.Node().SetName(cfg.NodeID)
	s.app.Node().SetNodeID(cfg.NodeID)
	s.app.Node().SetExit(cfg.ExitNode)

	needReconnect := false
	if oldID != cfg.NodeID {
		s.app.PersistNodeID(cfg.NodeID)
		s.mu.Lock()
		s.oldNodeIDs[oldID] = cfg.NodeID
		s.mu.Unlock()
		needReconnect = true
	}
	// Reconnect if the wire-name (== NodeID after the merge) changed,
	// so peers see the new identity.
	if cfg.NodeID != oldWireName {
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
		pc.Children = s.app.Tree().Cached(p.Name)
		result = append(result, pc)
	}
	writeJSON(w, result)
}

// getTopology returns a tree structure: direct peers with their nested sub-peers.
// getGraphCanvases returns the custom topology canvases:
// { "canvases": [ {id, name, nodes, edges}, ... ] }.
func (s *Server) getGraphCanvases(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{"canvases": s.app.GraphCanvases()})
}

// maxGraphCanvasBody bounds one canvas PUT (2000 nodes + 4000 edges with
// bends fit well inside).
const maxGraphCanvasBody = 2 << 20

// putGraphCanvas creates or replaces one custom canvas. The body is the
// full canvas ({name, nodes, edges}); the id comes from the path. Writes are
// whole-canvas, last writer wins, and every SSE subscriber gets the new
// snapshot.
func (s *Server) putGraphCanvas(w http.ResponseWriter, r *http.Request) {
	var body app.GraphCanvas
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGraphCanvasBody))
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	body.ID = r.PathValue("id")
	if err := app.ValidateGraphCanvas(body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.PutGraphCanvas(body); err != nil {
		code := 500
		if errors.Is(err, app.ErrGraphCanvasLimit) {
			code = 409
		}
		http.Error(w, err.Error(), code)
		return
	}
	s.broadcastGraphCanvases()
	writeJSON(w, map[string]string{"status": "ok"})
}

// deleteGraphCanvas removes one custom canvas.
func (s *Server) deleteGraphCanvas(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteGraphCanvas(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.broadcastGraphCanvases()
	writeJSON(w, map[string]string{"status": "ok"})
}

// broadcastGraphCanvases sends the committed snapshot to every registered
// SSE subscriber. Slow consumers with a full buffer simply drop the update;
// each frame carries the complete list so a missed frame self-heals on the
// next one. Never blocks the write path.
func (s *Server) broadcastGraphCanvases() {
	snap := s.app.GraphCanvases()
	s.layoutMu.Lock()
	subs := make([]chan []app.GraphCanvas, 0, len(s.layoutSubs))
	for _, ch := range s.layoutSubs {
		subs = append(subs, ch)
	}
	s.layoutMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// streamGraphCanvases pushes canvas snapshots to the client as SSE events.
// The first event after connect carries the current state, so the client
// needs no separate GET on mount; later events fire on every write (from
// any session).
func (s *Server) streamGraphCanvases(w http.ResponseWriter, r *http.Request) {
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

	ch := make(chan []app.GraphCanvas, 4)
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

	writeEvent := func(cs []app.GraphCanvas) bool {
		payload, err := json.Marshal(map[string]interface{}{"canvases": cs})
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !writeEvent(s.app.GraphCanvases()) {
		return
	}

	// Periodic keepalive so idle connections don't hit HTTP client or
	// proxy timeouts.
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case cs, ok := <-ch:
			if !ok {
				return
			}
			if !writeEvent(cs) {
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
		Conns        int               `json:"conns,omitempty"`
		LatencyMs    int               `json:"latency_ms"`
		TxRate       uint64            `json:"tx_rate"`
		RxRate       uint64            `json:"rx_rate"`
		// MaxRate (bytes/sec) — same role as topoSubPeer.MaxRate,
		// see comment there. Populated from the local cfg.Clients
		// entry's max(MaxTx, MaxRx).
		MaxRate      uint64            `json:"max_rate,omitempty"`
		IsSelf       bool              `json:"is_self,omitempty"`
		Restricted   bool              `json:"restricted,omitempty"`
		Invisible    bool              `json:"invisible,omitempty"`
		Attrs        proto.Attrs       `json:"attrs,omitempty"`
		Egress       json.RawMessage   `json:"egress"`
		// Punched: who set up the hole-punched connection(s) to this peer
		// (this node's name and/or the peer's).
		Punched      []string          `json:"punched,omitempty"`
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
	punched := s.app.PunchedPeers()
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
				Egress:     p.Egress,
				// Inbound peers in the live `peers` slice are connected
				// by definition — `p` was added when the inbound stream
				// registered. The new `connected` field on topoSubPeer
				// has no `omitempty`, so we MUST set it to true here or
				// the row defaults to false → list shows "offline" for
				// every active inbound peer.
				Connected: true,
				Punched:   punched[p.Name],
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
			children := filterAncestorPaths(s.app.Tree().Cached(c.Name), ancestors)
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
		Egress:     selfEgressJSON(s.app),
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
		tn.Punched = punched[name]
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
				tn.Conns = p.Conns
				tn.Egress = p.Egress
				tn.Restricted = p.Restricted
				tn.Invisible = p.Invisible
				tn.Attrs = p.Attrs
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
			children := filterAncestorPaths(s.app.Tree().Cached(name), ancestors)
			tn.Children = s.filterChildrenByNestedConfig(children, name, cfg, ancestors, pathRates, latencyCache[name], 0)
			if tn.Restricted {
				relay.MarkRestricted(tn.Children)
			}
		}
		// Also load children for inbound peers nested under self
		// (they can have outbound connections visible if nested is enabled)
		result = append(result, tn)
	}
	writeJSON(w, result)
}


// filterChildrenByNestedConfig strips deeper children unless local nested config allows them.
// Uses path-qualified keys (parent/child) so same-name peers have independent nested state.
// When c.Nested is enabled but c.Children is empty, pulls the flat child list
// cached for the qualified path by the node's nested-discovery tree.
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
	// Depth cap of the tree cache; binds only if the cap was lowered after
	// the cache was filled.
	if depth >= s.app.Tree().Limits().MaxDepth {
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
		// Latencies cumulative from self: the tree caches direct children
		// cumulative already, deeper levels one-hop.
		if depth >= 1 && c.LatencyMs > 0 && parentCumLat > 0 {
			c.LatencyMs += parentCumLat
		}
		childCumLat := c.LatencyMs
		if c.Nested {
			if len(c.Children) == 0 {
				c.Children = s.app.Tree().Cached(qualifiedKey)
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
	a := map[string]bool{cfg.NodeID: true}
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
	s.app.Tree().Kick()
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
	} else if cl.Addr != "" && !app.IsPunchAddr(cl.Addr) {
		cl.Addrs = []string{cl.Addr}
	}
	if cl.Name == "" {
		cl.Name = name
	}
	if err := normalizeClient(&cl); err != nil {
		http.Error(w, err.Error(), 400)
		return
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
	} else if cl.Addr != "" && !app.IsPunchAddr(cl.Addr) {
		cl.Addrs = []string{cl.Addr}
	}
	if cl.Name == "" {
		http.Error(w, "name required", 400)
		return
	}
	if err := normalizeClient(&cl); err != nil {
		http.Error(w, err.Error(), 400)
		return
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
	if pc.Protocol == "" {
		pc.Protocol = "socks5"
	}
	// Singleton-protocol fallback: SOCKS5 / HTTP tabs in the UI manage
	// one proxy each, so a missing id is unambiguous — use protocol as
	// the id. Without this, the very first save of a fresh node returns
	// 400 "id, listen required" because the UI didn't synthesize an id.
	if pc.ID == "" {
		pc.ID = pc.Protocol
	}
	if pc.ID == "" || pc.Listen == "" {
		http.Error(w, "id, listen required", 400)
		return
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

// --- L2TP / IKEv2 / OpenVPN (userspace servers, always available) ---

func (s *Server) getL2TPConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	result := map[string]any{
		"capable":      true,
		"host_network": app.CheckHostNetwork(),
		"reason":       "",
		"running":      s.app.VPNRunning("l2tp"),
		"connected":    s.app.VPNClientCount("l2tp"),
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
	var l2tp app.L2TPConfig
	if err := json.NewDecoder(r.Body).Decode(&l2tp); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.app.Store().Update(func(c *app.Config) {
		c.L2TP = &l2tp
	})
	if err := s.app.RestartL2TP(); err != nil {
		log.Printf("[l2tp] reload failed: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) getIKEv2Config(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	result := map[string]any{
		"capable":      true,
		"host_network": app.CheckHostNetwork(),
		"reason":       "",
		"running":      s.app.VPNRunning("ikev2"),
		"connected":    s.app.VPNClientCount("ikev2"),
	}
	nodeID := cfg.NodeID
	if cfg.IKEv2 != nil {
		result["enabled"] = cfg.IKEv2.Enabled
		result["mode"] = cfg.IKEv2.Mode
		result["pool"] = cfg.IKEv2.Pool
		result["pool6"] = orDefault(cfg.IKEv2.Pool6, app.DefaultIKEv2Pool6)
		result["cert_id"] = cfg.IKEv2.CertID
		result["psk"] = cfg.IKEv2.PSK
		result["local_id"] = cfg.IKEv2.LocalID
		result["remote_id"] = cfg.IKEv2.RemoteID
		result["psk_user_mode"] = cfg.IKEv2.PSKUserMode
		result["default_exit"] = cfg.IKEv2.DefaultExit
		result["default_exit_mode"] = cfg.IKEv2.DefaultExitMode
		result["default_exit_paths"] = cfg.IKEv2.DefaultExitPaths
		result["proxy_port"] = cfg.IKEv2.ProxyPort
		result["mtu"] = cfg.IKEv2.MTU
	} else {
		result["enabled"] = false
		result["mode"] = "mschapv2"
		result["pool"] = "192.168.26.1/24"
		result["pool6"] = app.DefaultIKEv2Pool6
		result["cert_id"] = ""
		result["psk"] = ""
		result["local_id"] = nodeID
		result["remote_id"] = ""
		result["psk_user_mode"] = false
		result["default_exit"] = ""
		result["default_exit_mode"] = ""
		result["proxy_port"] = 12350
		result["mtu"] = 1400
	}
	writeJSON(w, result)
}

func (s *Server) updateIKEv2Config(w http.ResponseWriter, r *http.Request) {
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
	if err := s.app.RestartIKEv2(); err != nil {
		log.Printf("[ikev2] reload failed: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) getOpenVPNConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	result := map[string]any{
		"running":   s.app.VPNRunning("openvpn"),
		"connected": s.app.VPNClientCount("openvpn"),
		"enabled":   false,
		"port":      1194,
		"pool":      "10.8.0.1/24",
		"pool6":     app.DefaultOpenVPNPool6,
		"cert_id":   "",
		"mtu":       1400,
	}
	if c := cfg.OpenVPN; c != nil {
		result["enabled"] = c.Enabled
		result["port"] = c.Port
		result["pool"] = c.Pool
		result["pool6"] = orDefault(c.Pool6, app.DefaultOpenVPNPool6)
		result["cert_id"] = c.CertID
		result["mtu"] = c.MTU
	}
	writeJSON(w, result)
}

func (s *Server) updateOpenVPNConfig(w http.ResponseWriter, r *http.Request) {
	var ovpn app.OpenVPNConfig
	if err := json.NewDecoder(r.Body).Decode(&ovpn); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if ovpn.Port < 0 || ovpn.Port > 65535 {
		http.Error(w, "invalid port", 400)
		return
	}
	s.app.Store().Update(func(c *app.Config) {
		c.OpenVPN = &ovpn
	})
	if err := s.app.RestartOpenVPN(); err != nil {
		log.Printf("[openvpn] reload failed: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// downloadOpenVPNProfile returns a client profile (auth-user-pass) for the
// address the operator gives as ?endpoint=host.
func (s *Server) downloadOpenVPNProfile(w http.ResponseWriter, r *http.Request) {
	prof, err := s.app.OpenVPNProfile(r.URL.Query().Get("endpoint"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/x-openvpn-profile")
	w.Header().Set("Content-Disposition", "attachment; filename=hy2scale.ovpn")
	w.Write([]byte(prof))
}

// orDefault is v, or def when v is blank.
func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
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
		result["address6"] = cfg.WireGuard.WGAddress6()
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
		result["address6"] = app.DefaultWGAddress6
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
	rules := cfg.Rules
	if rules == nil {
		rules = []app.RoutingRule{}
	}
	writeJSON(w, map[string]any{
		"available": true,
		"capture":   s.app.RuleCaptureMode(),
		"rules":     rules,
	})
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
	// Use the ESCAPED path so percent-encoded slashes inside a single
	// path segment (e.g. a sub-peer addressed as `aub%2Fcn`) survive the
	// split-rejoin we do below. r.URL.Path would have decoded the %2F
	// into a real "/" before we ever see it, fusing the sub-peer name
	// with its parent and producing a remote URL with too many segments
	// — making routes like `PUT /api/peers/{name}/nested` 404 because
	// `{name}` matches one segment, not two. r.URL.RawPath is populated
	// only when the request actually had escaping; EscapedPath() handles
	// both that case and the no-escaping fast path uniformly.
	rawPath := r.URL.EscapedPath()
	raw := strings.TrimPrefix(rawPath, "/remote/")
	if raw == "" || raw == "/" {
		http.Error(w, "usage: /remote/{peer}/{path}", 400)
		return
	}

	// Split into chain + remaining. Chain = contiguous peer names (no dots, no slashes in names).
	// First segment with a dot or known as a file extension ends the chain.
	// Splitting the escaped form means %2F-inside-a-segment is preserved
	// as part of that segment rather than promoted to a path separator.
	segments := strings.Split(strings.TrimSuffix(raw, "/"), "/")
	// Known path prefixes that mark the end of the peer chain.
	// The frontend builds links like "/scale/remote/{peer}/scale/..." so
	// "scale" must be recognized as a base-path sentinel, not a peer name.
	notPeer := map[string]bool{
		"api": true, "login": true, "nodes": true, "proxies": true,
		"tls": true, "settings": true, "remote": true,
		"scale": true, "users": true, "rules": true, "assets": true,
		"interfaces": true, "overview": true, "endpoints": true, "logs": true,
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

	// Decode each chain segment back to its canonical peer-name form.
	// A chain segment is a SINGLE peer identifier — it never legitimately
	// contains "/" (the path separator) — so PathUnescape is lossless
	// here. We need the decoded form for n.peers lookups, oldNodeIDs
	// resolution, and the proxyBase URL (which we re-encode below as
	// part of URL.String()).
	chain := make([]string, chainLen)
	for i, seg := range segments[:chainLen] {
		if decoded, err := url.PathUnescape(seg); err == nil {
			chain[i] = decoded
		} else {
			chain[i] = seg
		}
	}
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

	// Native peers are vanilla hysteria2 servers — they have no hy2scale
	// relay-API listener on the other end of `_relay_api_:0`, so opening a
	// remote-UI tunnel into them just hangs or 502s with a confusing error.
	// v1.2's web UI suppressed this at the click; the React rewrite lost the
	// guard. Restore it here so a manually-typed `/remote/<native>/scale/`
	// URL also gets a clear refusal instead of a useless 502. English-only
	// by intent — this is a misuse path, not a localised user surface.
	nativeMap := s.app.Node().NativeMap()
	if nativeMap[chain[len(chain)-1]] {
		http.Error(w,
			"Remote UI is not available for native peers. The selected node is a vanilla hysteria2 server and does not host the hy2scale management API. To route traffic through it, add a user with this peer set as Exit instead of opening it as a remote node.",
			409)
		return
	}

	// Nodes reached by node access by user are traffic-only.
	if s.app.Node().IsRestricted(chain[0]) {
		http.Error(w, "Remote UI is not available through node access by user.", 409)
		return
	}

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
		// Stream non-HTML responses with an explicit flush after each
		// chunk. plain io.Copy(w, resp.Body) leaves chunks sitting in
		// Go's ~4 KB ResponseWriter buffer until enough data accumulates,
		// which made SSE endpoints (text/event-stream) silently fail
		// through the proxy: the remote graph-canvas snapshot is the
		// first event, ~100 bytes, so it never reached the browser and
		// the remote-graph node-drag positions appeared not to persist
		// on page reload.
		fl, _ := w.(http.Flusher)
		buf := make([]byte, 16*1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			}
			if rerr != nil {
				return
			}
		}
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
		{Name: "veepin (vendored, patched)", Version: "bcac178", License: "MIT", Source: "https://github.com/xen0bit/veepin"},
		{Name: "iptables (nf_tables backend)", Version: "distro-packaged", License: "GPL-2.0-or-later", Source: "https://www.netfilter.org/projects/iptables/"},
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

	if err := applyTarball(tmp.Name(), s.app.DataDir()); err != nil {
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
// archive at path. In a container it is stored in the data directory (see
// internal/selfupdate: the container's own filesystem is reset whenever it
// is recreated); otherwise it is atomically swapped into place, leaving the
// previous binary as <self>.bak. Caller is responsible for triggering os.Exit so
// the process restarts on the new binary. Used by both the manual upload
// path (uploadUpgrade) and the online-update path
// (applyDownloadedUpgrade) so the two stay in lock-step.
func applyTarball(path, dataDir string) error {
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

	if isRunningInDocker() {
		defer os.Remove(tmpPath)
		return selfupdate.Install(dataDir, tmpPath, Version)
	}

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

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
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
	"github.com/FrankoonG/hy2scale/internal/relay"
	"github.com/FrankoonG/hy2scale/internal/web"
	qrcode "github.com/skip2/go-qrcode"
)

type topoSubPeer struct {
	Name      string         `json:"name"`
	ExitNode  bool           `json:"exit_node"`
	Direction string         `json:"direction"`
	Via       string         `json:"via"`
	LatencyMs int            `json:"latency_ms"`
	Nested    bool           `json:"nested"`
	Disabled  bool           `json:"disabled,omitempty"`
	Native    bool           `json:"native,omitempty"`
	Version   string         `json:"version,omitempty"`
	TxRate    uint64         `json:"tx_rate"`
	RxRate    uint64         `json:"rx_rate"`
	Children  []topoSubPeer  `json:"children,omitempty"`
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
	return &Server{
		app:        a,
		addr:       addr,
		basePath:   bp,
		username:   username,
		passHash:   passHash,
		oldNodeIDs:    make(map[string]string),
		subPeersCache: make(map[string][]topoSubPeer),
		subPeersKick:  make(chan struct{}, 1),
		sessions: make(map[string]time.Time),
	}
}

func (s *Server) Start(ctx context.Context) error {
	apiMux := http.NewServeMux()

	// Login (no auth)
	apiMux.HandleFunc("POST /api/login", s.login)
	// Internal peer list (no auth, used for reverse nested discovery)
	apiMux.HandleFunc("GET /api/internal/peers", s.internalPeers)

	// Authed API routes
	authed := http.NewServeMux()
	authed.HandleFunc("GET /api/node", s.getNode)
	authed.HandleFunc("GET /api/stats", s.getStats)
	authed.HandleFunc("PUT /api/node", s.updateNode)
	authed.HandleFunc("GET /api/peers", s.getPeers)
	authed.HandleFunc("GET /api/topology", s.getTopology)
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
	authed.HandleFunc("GET /api/rules/tun-mode", s.getTunMode)
	authed.HandleFunc("PUT /api/rules/tun-mode", s.setTunMode)

	// Port check
	authed.HandleFunc("POST /api/check-ports", s.checkPorts)

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

	// Static files with SPA fallback — inject basePath into index.html
	staticFS, _ := fs.Sub(web.Static, "static")
	rawIndex, _ := fs.ReadFile(staticFS, "index.html")
	baseTag := `<script>window.__BASE__="` + s.basePath + `";</script>`
	indexHTML := strings.Replace(string(rawIndex), "<head>", "<head>"+baseTag, 1)
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
	return srv.Serve(ln)
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
	writeJSON(w, map[string]string{"token": token})
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
		token := r.Header.Get("Authorization")
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
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
	writeJSON(w, map[string]any{
		"listen":             s.addr,
		"base_path":          s.basePath,
		"dns":                dns,
		"force_https":        cfg.ForceHTTPS,
		"https_cert_id":      cfg.HTTPSCertID,
		"session_timeout_h":  sessionH,
	})
}

func (s *Server) updateUISettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Listen          *string `json:"listen"`
		BasePath        *string `json:"base_path"`
		DNS             *string `json:"dns"`
		ForceHTTPS      *bool   `json:"force_https"`
		HTTPSCertID     *string `json:"https_cert_id"`
		SessionTimeoutH *int    `json:"session_timeout_h"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
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
	})
	writeJSON(w, map[string]string{"status": "ok", "note": "restart required for changes to take effect"})
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
const Version = "1.3.0"

func init() {
	app.AppVersion = Version
	relay.NodeVersion = Version
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	capOK, _ := app.CheckCapability()
	// limited = no NET_ADMIN OR kernel can't run VPN services (Docker Desktop/WSL)
	limited := !capOK || (!app.CheckIKEv2Capability())
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
func (s *Server) getTopology(w http.ResponseWriter, r *http.Request) {
	peers := s.app.Node().Peers()
	cfg := s.app.Store().Get()

	// Build client lookup for addr info
	clientMap := make(map[string]app.ClientEntry)
	for _, cl := range cfg.Clients {
		clientMap[cl.Name] = cl
	}
	connected := make(map[string]bool)
	nativeMap := make(map[string]bool)
	versionMap := make(map[string]string)
	for _, p := range peers {
		connected[p.Name] = true
		if p.Native {
			nativeMap[p.Name] = true
		}
		if p.Version != "" {
			versionMap[p.Name] = p.Version
		}
	}

	type treeNode struct {
		Name       string            `json:"name"`
		Addr       string            `json:"addr,omitempty"`
		Addrs      []string          `json:"addrs,omitempty"`
		IPStatuses []relay.IPStatus  `json:"ip_statuses,omitempty"`
		ExitNode   bool              `json:"exit_node"`
		Direction string          `json:"direction"`
		Connected bool            `json:"connected"`
		Disabled  bool            `json:"disabled"`
		Nested    bool            `json:"nested"`
		Native    bool            `json:"native,omitempty"`
		Version   string          `json:"version,omitempty"`
		LatencyMs int             `json:"latency_ms"`
		TxRate    uint64          `json:"tx_rate"`
		RxRate    uint64          `json:"rx_rate"`
		IsSelf    bool            `json:"is_self,omitempty"`
		Children  []topoSubPeer   `json:"children,omitempty"`
	}

	disabledMap := make(map[string]bool)
	for _, cl := range cfg.Clients {
		if cl.Disabled {
			disabledMap[cl.Name] = true
		}
	}

	nameSet := make(map[string]bool)
	for _, p := range peers {
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
	for _, p := range peers {
		if p.Direction == "inbound" {
			inboundNames[p.Name] = true
			child := topoSubPeer{
				Name:      p.Name,
				ExitNode:  p.ExitNode,
				Direction: "inbound",
				Via:       cfg.NodeID,
				LatencyMs: latencyCache[p.Name],
				Version:   p.Version,
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

	// Add per-peer traffic and sub-peers for inbound children
	for i, c := range selfChildren {
		if pr, ok := peerRates[c.Name]; ok {
			selfChildren[i].TxRate = pr.TxRate
			selfChildren[i].RxRate = pr.RxRate
		}
		if c.Nested {
			children := filterSelfFromChildren(s.getCachedSubPeers(c.Name), cfg.NodeID)
			selfChildren[i].Children = filterChildrenByNestedConfig(children, c.Name, cfg)
		}
	}
	sort.Slice(selfChildren, func(i, j int) bool { return selfChildren[i].Name < selfChildren[j].Name })

	result := make([]treeNode, 0, len(names)+1)
	result = append(result, treeNode{
		Name:      cfg.NodeID,
		Addr:      selfServer,
		Direction: "local",
		Connected: true,
		Disabled:  selfDisabled,
		IsSelf:    true,
		LatencyMs: 0,
		Children:  selfChildren,
	})

	for _, name := range names {
		// Skip inbound peers — they're nested under self
		if inboundNames[name] {
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
		for _, p := range peers {
			if p.Name == name {
				tn.ExitNode = p.ExitNode
				tn.Direction = p.Direction
				break
			}
		}
		tn.Native = nativeMap[name]
		tn.Version = versionMap[name]
		if tn.Native {
			tn.Nested = false
		} else if pc, ok := cfg.Peers[name]; ok && pc.Nested {
			tn.Nested = true // explicitly enabled
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

		// Load sub-peers from background cache only (never blocks)
		if tn.Nested && tn.Connected && !tn.Native {
			children := filterSelfFromChildren(s.getCachedSubPeers(name), cfg.NodeID)
			tn.Children = filterChildrenByNestedConfig(children, name, cfg)
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

// StartSubPeersUpdater runs in background, periodically refreshes nested peer data.
// Uses relay streamListPeers for both outbound and inbound peers (same mechanism).
func (s *Server) StartSubPeersUpdater(ctx context.Context) {
	// Register rich peer list handler so remote nodes return tree data via relay stream
	s.app.Node().SetListPeersFunc(func() []byte {
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

		// Collect peers that need fetching
		var fetchNames []string
		for _, name := range connectedPeers {
			if nativeMap[name] {
				continue
			}
			if pc, ok := cfg.Peers[name]; !ok || !pc.Nested {
				continue
			}
			fetchNames = append(fetchNames, name)
		}

		// Fetch all in parallel (same mechanism for outbound and inbound)
		type fetchResult struct {
			name     string
			children []topoSubPeer
		}
		resultCh := make(chan fetchResult, len(fetchNames))
		for _, name := range fetchNames {
			go func(n string) {
				resultCh <- fetchResult{n, s.fetchSubPeersViaStream(n)}
			}(name)
		}
		for range fetchNames {
			select {
			case r := <-resultCh:
				if r.children != nil {
					newCache[r.name] = r.children
				}
			case <-time.After(6 * time.Second):
			}
		}

		s.subPeersMu.Lock()
		s.subPeersCache = newCache
		s.subPeersMu.Unlock()

		// Feed sub-peer data into relay's peersOfCache for path discovery
		visited := make(map[string]bool)
		var walkAndCache func(parentName string, children []topoSubPeer)
		walkAndCache = func(parentName string, children []topoSubPeer) {
			if visited[parentName] {
				return
			}
			visited[parentName] = true
			var infos []relay.PeerInfo
			for _, c := range children {
				infos = append(infos, relay.PeerInfo{
					Name:     c.Name,
					ExitNode: c.ExitNode,
					Native:   c.Native,
				})
			}
			if len(infos) > 0 {
				s.app.Node().SetPeersOfCache(parentName, infos)
			}
			for _, c := range children {
				if len(c.Children) > 0 {
					walkAndCache(c.Name, c.Children)
				} else {
					if cached, ok := newCache[c.Name]; ok && len(cached) > 0 {
						walkAndCache(c.Name, cached)
					}
				}
			}
		}
		for peerName, children := range newCache {
			walkAndCache(peerName, children)
		}
	}
}

// fetchSubPeersViaStream queries a peer's topology via relay streamListPeers.
// Same mechanism for both outbound and inbound peers — no HTTP, no exit handler.
// fetchSubPeersViaStream queries a peer's topology via relay streamListPeers.
// Does NOT filter self — cache stores unfiltered data so remote nodes can see all peers.
// Self-filtering happens only at display time (getTopology).
func (s *Server) fetchSubPeersViaStream(peerName string) []topoSubPeer {
	data, err := s.app.Node().PeersOfRaw(peerName)
	if err != nil {
		return nil
	}
	type peerWithChildren struct {
		Name      string        `json:"name"`
		ExitNode  bool          `json:"exit_node"`
		Direction string        `json:"direction"`
		Native    bool          `json:"native"`
		LatencyMs int           `json:"latency_ms"`
		Version   string        `json:"version,omitempty"`
		Children  []topoSubPeer `json:"children,omitempty"`
	}
	var remotePeers []peerWithChildren
	if err := json.Unmarshal(data, &remotePeers); err != nil {
		return nil
	}
	parentLatency := s.app.Node().GetLatency(peerName)
	children := make([]topoSubPeer, 0, len(remotePeers))
	for _, rp := range remotePeers {
		childLatency := rp.LatencyMs
		if childLatency > 0 && parentLatency > 0 {
			childLatency += parentLatency
		}
		child := topoSubPeer{
			Name:      rp.Name,
			ExitNode:  rp.ExitNode,
			Direction: rp.Direction,
			Via:       peerName,
			LatencyMs: childLatency,
			Native:    rp.Native,
			Children:  rp.Children,
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
func filterChildrenByNestedConfig(children []topoSubPeer, parentName string, cfg app.Config) []topoSubPeer {
	if len(children) == 0 {
		return children
	}
	result := make([]topoSubPeer, 0, len(children))
	for _, c := range children {
		qualifiedKey := parentName + "/" + c.Name
		pc, hasPC := cfg.Peers[qualifiedKey]
		c.Nested = hasPC && pc.Nested
		c.Disabled = hasPC && pc.Disabled
		if c.Nested && len(c.Children) > 0 {
			c.Children = filterChildrenByNestedConfig(c.Children, qualifiedKey, cfg)
		} else {
			c.Children = nil
		}
		result = append(result, c)
	}
	return result
}

// filterSelfFromChildren recursively removes self node ID from all levels of children.
func filterSelfFromChildren(children []topoSubPeer, selfID string) []topoSubPeer {
	if len(children) == 0 {
		return children
	}
	filtered := make([]topoSubPeer, 0, len(children))
	for _, c := range children {
		if c.Name == selfID {
			continue
		}
		c.Children = filterSelfFromChildren(c.Children, selfID)
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
	writeJSON(w, cfg.Proxies)
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
	capOK, _ := app.CheckL2TPCapability()
	capable := capOK
	hostNet := app.CheckHostNetwork()
	cfg := s.app.Store().Get()
	result := map[string]any{
		"capable":      capable,
		"host_network": hostNet,
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
	cfg := s.app.Store().Get()
	result := map[string]any{
		"capable":      capable,
		"host_network": hostNet,
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
	writeJSON(w, cfg.Users)
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
	result := map[string]any{
		"available": app.RuleEngineAvailable(),
		"rules":     cfg.Rules,
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

func (s *Server) getTunMode(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	tm := cfg.TunMode
	if tm == nil {
		tm = &app.TunModeConfig{}
	}
	result := map[string]any{
		"enabled": tm.Enabled,
		"mode":    tm.Mode,
		"active":  app.TunModeActive(),
	}
	writeJSON(w, result)
}

func (s *Server) setTunMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool   `json:"enabled"`
		Mode    string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.Mode == "" {
		body.Mode = "mixed"
	}
	if body.Enabled {
		if err := s.app.EnableTunMode(body.Mode); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	} else {
		s.app.DisableTunMode()
	}
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
	// Known path prefixes that mark the end of the peer chain
	notPeer := map[string]bool{
		"api": true, "login": true, "nodes": true, "proxies": true,
		"tls": true, "settings": true, "remote": true,
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
	remaining := strings.Join(segments[chainLen:], "/")
	// The remote's actual path: prepend their base path (/scale)
	// /remote/vm/ → remote /scale/
	// /remote/vm/login → remote /scale/login
	// /remote/vm/api/node → remote /scale/api/node
	// /remote/vm/style.css → remote /scale/style.css
	remotePath := "/scale/" + remaining

	// Proxy base: what the remote's __BASE__ should point to
	proxyBase := s.basePath + "/remote/" + strings.Join(chain, "/")

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var conn net.Conn
	var err error
	if len(chain) == 1 {
		conn, err = s.app.Node().DialTCP(ctx, chain[0], "127.0.0.1:5565")
	} else {
		conn, err = s.app.Node().DialVia(ctx, chain, "127.0.0.1:5565")
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("tunnel to %s failed: %v", strings.Join(chain, "/"), err), 502)
		return
	}
	defer conn.Close()

	// Construct proxied request using http.Request.Write for correctness
	outReq, _ := http.NewRequest(r.Method, "http://127.0.0.1:5565"+remotePath, r.Body)
	outReq.URL.RawQuery = r.URL.RawQuery
	outReq.Header = r.Header.Clone()
	outReq.Header.Set("Host", "127.0.0.1:5565")
	outReq.Header.Set("Connection", "close")
	outReq.Header.Set("X-Hy2scale-Proxy", "true")
	outReq.ContentLength = r.ContentLength
	outReq.Write(conn)

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		http.Error(w, "bad response from remote: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	// Rewrite __BASE__ in HTML responses so JS API calls route through proxy
	ct := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(ct, "text/html")

	for k, vv := range resp.Header {
		// Rewrite Location header for redirects
		if strings.EqualFold(k, "Location") {
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

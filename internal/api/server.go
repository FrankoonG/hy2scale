package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
	"github.com/FrankoonG/hy2scale/internal/web"
)

type topoSubPeer struct {
	Name      string         `json:"name"`
	ExitNode  bool           `json:"exit_node"`
	Direction string         `json:"direction"`
	Via       string         `json:"via"`
	LatencyMs int            `json:"latency_ms"`
	Nested    bool           `json:"nested"`
	Children  []topoSubPeer  `json:"children,omitempty"`
}

type Server struct {
	app      *app.App
	addr     string
	basePath string // e.g. "/scale"

	mu       sync.RWMutex
	username string
	password string
	sessions map[string]time.Time
}

func NewServer(a *app.App, addr, basePath string) *Server {
	bp := strings.TrimRight(basePath, "/")
	if bp != "" && !strings.HasPrefix(bp, "/") {
		bp = "/" + bp
	}
	return &Server{
		app:      a,
		addr:     addr,
		basePath: bp,
		username: "admin",
		password: "admin",
		sessions: make(map[string]time.Time),
	}
}

func (s *Server) Start(ctx context.Context) error {
	apiMux := http.NewServeMux()

	// Login (no auth)
	apiMux.HandleFunc("POST /api/login", s.login)

	// Authed API routes
	authed := http.NewServeMux()
	authed.HandleFunc("GET /api/node", s.getNode)
	authed.HandleFunc("GET /api/stats", s.getStats)
	authed.HandleFunc("PUT /api/node", s.updateNode)
	authed.HandleFunc("GET /api/peers", s.getPeers)
	authed.HandleFunc("GET /api/topology", s.getTopology)
	authed.HandleFunc("GET /api/peers/{name}/peers", s.getNestedPeers)
	authed.HandleFunc("PUT /api/peers/{name}/nested", s.setNested)
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
	authed.HandleFunc("PUT /api/settings/password", s.changePassword)
	authed.HandleFunc("GET /api/settings/ui", s.getUISettings)
	authed.HandleFunc("PUT /api/settings/ui", s.updateUISettings)

	// TLS
	authed.HandleFunc("GET /api/tls", s.listCerts)
	authed.HandleFunc("POST /api/tls/import", s.importCert)
	authed.HandleFunc("POST /api/tls/generate", s.generateCert)
	authed.HandleFunc("DELETE /api/tls/{id}", s.deleteCert)

	apiMux.Handle("/api/", s.authMiddleware(authed))

	// Static files with SPA fallback — inject basePath into index.html
	staticFS, _ := fs.Sub(web.Static, "static")
	rawIndex, _ := fs.ReadFile(staticFS, "index.html")
	baseScript := "<script>window.__BASE__=\"" + s.basePath + "\";</script><script src=\"app.js\"></script>"
	indexBytes := []byte(strings.Replace(string(rawIndex), "<script src=\"app.js\"></script>", baseScript, 1))

	// Known frontend routes that should serve index.html
	frontendRoutes := map[string]bool{
		"":         true,
		"login":    true,
		"nodes":    true,
		"proxies":  true,
		"tls":      true,
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
				}
				w.Write(data)
				return
			}
		}

		if frontendRoutes[path] {
			w.Header().Set("Content-Type", "text/html")
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
	s.mu.RLock()
	ok := subtle.ConstantTimeCompare([]byte(body.Username), []byte(s.username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.password)) == 1
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "invalid credentials", 401)
		return
	}
	token := s.generateToken()
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	s.mu.Unlock()
	writeJSON(w, map[string]string{"token": token})
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(body.CurrentPassword), []byte(s.password)) != 1 {
		http.Error(w, "current password incorrect", 403)
		return
	}
	if body.NewUsername != "" {
		s.username = body.NewUsername
	}
	if body.NewPassword != "" {
		s.password = body.NewPassword
	}
	s.sessions = make(map[string]time.Time)
	writeJSON(w, map[string]string{"status": "ok"})
}

// --- UI Settings (port, base path) ---

func (s *Server) getUISettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{
		"listen":    s.addr,
		"base_path": s.basePath,
	})
}

func (s *Server) updateUISettings(w http.ResponseWriter, r *http.Request) {
	// Port and base path changes require restart — return info
	var body struct {
		Listen   *string `json:"listen"`
		BasePath *string `json:"base_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Store in app config for next restart
	s.app.Store().Update(func(c *app.Config) {
		if body.Listen != nil {
			c.UIListen = *body.Listen
		}
		if body.BasePath != nil {
			c.UIBasePath = *body.BasePath
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

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	writeJSON(w, map[string]any{
		"node_id":   cfg.NodeID,
		"name":      cfg.Name,
		"exit_node": cfg.ExitNode,
		"server":    cfg.Server,
	})
}

func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID   *string          `json:"node_id"`
		Name     *string          `json:"name"`
		ExitNode *bool            `json:"exit_node"`
		Server   *app.ServerConfig `json:"server"`
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
	})
	// Sync name change to relay node
	cfg := s.app.Store().Get()
	s.app.Node().SetName(cfg.Name)
	s.app.Node().SetExit(cfg.ExitNode)
	// Persist node ID to file
	if body.NodeID != nil {
		s.app.PersistNodeID(cfg.NodeID)
	}
	writeJSON(w, map[string]string{"status": "ok", "note": "server config changes require restart"})
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
	for _, p := range peers {
		connected[p.Name] = true
	}

	type treeNode struct {
		Name      string          `json:"name"`
		Addr      string          `json:"addr,omitempty"`
		ExitNode  bool            `json:"exit_node"`
		Direction string          `json:"direction"`
		Connected bool            `json:"connected"`
		Disabled  bool            `json:"disabled"`
		Nested    bool            `json:"nested"`
		LatencyMs int             `json:"latency_ms"`
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

	// Measure direct peer latencies
	latencyCache := make(map[string]int)
	for _, name := range names {
		if connected[name] {
			rtt := s.app.Node().PingPeer(name)
			if rtt < 0 {
				latencyCache[name] = -1
			} else {
				latencyCache[name] = int(rtt.Milliseconds())
			}
		} else {
			latencyCache[name] = -1
		}
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
				LatencyMs: 0,
			}
			if pc, ok := cfg.Peers[p.Name]; ok {
				child.Nested = pc.Nested
			}
			selfChildren = append(selfChildren, child)
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
		tn := treeNode{Name: name, Connected: connected[name], Disabled: disabledMap[name]}
		if cl, ok := clientMap[name]; ok {
			tn.Addr = cl.Addr
			tn.Direction = "outbound"
		}
		for _, p := range peers {
			if p.Name == name {
				tn.ExitNode = p.ExitNode
				tn.Direction = p.Direction
				break
			}
		}
		if pc, ok := cfg.Peers[name]; ok {
			tn.Nested = pc.Nested
		}
		if tn.Connected && tn.Direction == "outbound" {
			tn.LatencyMs = latencyCache[name]
		} else if !tn.Connected {
			tn.LatencyMs = -1
		}

		// Load sub-peers recursively
		if tn.Nested && tn.Connected {
			tn.Children = s.loadSubPeers([]string{name}, latencyCache[name], latencyCache, cfg, 0)
		}
		// Also load children for inbound peers nested under self
		// (they can have outbound connections visible if nested is enabled)
		result = append(result, tn)
	}
	writeJSON(w, result)
}


// loadSubPeers loads nested peers. path is the chain from local to the peer being queried.
func (s *Server) loadSubPeers(path []string, parentLatency int, latencyCache map[string]int, cfg app.Config, depth int) []topoSubPeer {
	if depth > 8 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subPeers, err := s.app.Node().PeersOfVia(ctx, path)
	if err != nil {
		return nil
	}
	myID := s.app.Store().Get().NodeID
	peerName := path[len(path)-1]
	children := make([]topoSubPeer, 0, len(subPeers))
	for _, sp := range subPeers {
		// Only hide self nodeID
		if sp.Name == myID {
			continue
		}
		childLatency := -1
		if parentLatency > 0 {
			if directLat, ok := latencyCache[sp.Name]; ok && directLat > 0 {
				childLatency = parentLatency + directLat
			} else {
				childLatency = parentLatency * 2
			}
		}
		child := topoSubPeer{
			Name:      sp.Name,
			ExitNode:  sp.ExitNode,
			Direction: sp.Direction,
			Via:       peerName,
			LatencyMs: childLatency,
		}
		if pc, ok := cfg.Peers[sp.Name]; ok {
			child.Nested = pc.Nested
		}
		if child.Nested {
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
			Connected:   connected[cl.Name],
		})
	}
	writeJSON(w, result)
}

func (s *Server) getClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.app.Store().Get()
	for _, cl := range cfg.Clients {
		if cl.Name == name {
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
	cl.Name = name
	if cl.Addr == "" || cl.Password == "" {
		http.Error(w, "addr and password required", 400)
		return
	}
	if err := s.app.UpdateClient(cl); err != nil {
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
	if cl.Name == "" || cl.Addr == "" || cl.Password == "" {
		http.Error(w, "name, addr, password required", 400)
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
	writeJSON(w, cfg.Proxies)
}

func (s *Server) addProxy(w http.ResponseWriter, r *http.Request) {
	var pc app.ProxyConfig
	if err := json.NewDecoder(r.Body).Decode(&pc); err != nil {
		http.Error(w, err.Error(), 400)
		return
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

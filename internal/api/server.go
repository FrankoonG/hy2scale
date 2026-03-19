package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"

	"github.com/FrankoonG/hy2scale/internal/app"
	"github.com/FrankoonG/hy2scale/internal/web"
)

type Server struct {
	app  *app.App
	addr string
}

func NewServer(a *app.App, addr string) *Server {
	return &Server{app: a, addr: addr}
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Node
	mux.HandleFunc("GET /api/node", s.getNode)
	mux.HandleFunc("PUT /api/node", s.updateNode)

	// Peers
	mux.HandleFunc("GET /api/peers", s.getPeers)
	mux.HandleFunc("GET /api/peers/{name}/peers", s.getNestedPeers)
	mux.HandleFunc("PUT /api/peers/{name}/nested", s.setNested)

	// Clients
	mux.HandleFunc("GET /api/clients", s.getClients)
	mux.HandleFunc("POST /api/clients", s.addClient)
	mux.HandleFunc("DELETE /api/clients/{name}", s.removeClient)

	// Proxies
	mux.HandleFunc("GET /api/proxies", s.getProxies)
	mux.HandleFunc("POST /api/proxies", s.addProxy)
	mux.HandleFunc("PUT /api/proxies/{id}", s.updateProxy)
	mux.HandleFunc("DELETE /api/proxies/{id}", s.removeProxy)

	// Static files
	staticFS, _ := fs.Sub(web.Static, "static")
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux}
	go func() { <-ctx.Done(); srv.Close() }()
	log.Printf("API/UI on %s", s.addr)
	return srv.Serve(ln)
}

// --- Node ---

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	writeJSON(w, map[string]any{
		"node_id":   cfg.NodeID,
		"name":      cfg.Name,
		"exit_node": cfg.ExitNode,
	})
}

func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     *string `json:"name"`
		ExitNode *bool   `json:"exit_node"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.app.Store().Update(func(c *app.Config) {
		if body.Name != nil {
			c.Name = *body.Name
		}
		if body.ExitNode != nil {
			c.ExitNode = *body.ExitNode
		}
	})
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

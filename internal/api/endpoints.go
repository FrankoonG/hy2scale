// Add-on endpoints: WireGuard.

package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/FrankoonG/hy2scale/internal/app"
	qrcode "github.com/skip2/go-qrcode"
)

// endpointRoutes: the routes of the endpoints add-on (WireGuard).
func (s *Server) endpointRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/wireguard", s.getWireGuardConfig)
	m.HandleFunc("PUT /api/wireguard", s.updateWireGuardConfig)
	m.HandleFunc("POST /api/wireguard/generate-key", s.generateWGKey)
	m.HandleFunc("POST /api/wireguard/peers", s.addWGPeer)
	m.HandleFunc("PUT /api/wireguard/peers/{name}", s.updateWGPeer)
	m.HandleFunc("DELETE /api/wireguard/peers/{name}", s.removeWGPeer)
	m.HandleFunc("GET /api/wireguard/peers/{name}/config", s.downloadWGPeerConfig)
	m.HandleFunc("GET /api/wireguard/qr", s.wireGuardQR)
}

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
	for _, p := range wg.Peers {
		if err := s.app.CheckExitPolicy(p.ExitPolicy); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
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
	if err := s.app.CheckExitPolicy(peer.ExitPolicy); err != nil {
		http.Error(w, err.Error(), 400)
		return
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
	if err := s.app.CheckExitPolicy(updated.ExitPolicy); err != nil {
		http.Error(w, err.Error(), 400)
		return
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

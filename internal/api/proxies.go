// Add-on proxies: SOCKS5 / HTTP / SS / Trojan and the VPN servers (IKEv2, L2TP, OpenVPN).

package api

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/FrankoonG/hy2scale/internal/app"
)

// proxyRoutes: the routes of the proxies add-on (including the VPN servers).
func (s *Server) proxyRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/proxies", s.getProxies)
	m.HandleFunc("POST /api/proxies", s.addProxy)
	m.HandleFunc("PUT /api/proxies/{id}", s.updateProxy)
	m.HandleFunc("DELETE /api/proxies/{id}", s.removeProxy)
	// SS config
	m.HandleFunc("GET /api/ss", s.getSSConfig)
	m.HandleFunc("PUT /api/ss", s.updateSSConfig)
	// Trojan config
	m.HandleFunc("GET /api/trojan", s.getTrojanConfig)
	m.HandleFunc("PUT /api/trojan", s.updateTrojanConfig)
	// L2TP config
	m.HandleFunc("GET /api/l2tp", s.getL2TPConfig)
	m.HandleFunc("PUT /api/l2tp", s.updateL2TPConfig)
	// IKEv2 config
	m.HandleFunc("GET /api/ikev2", s.getIKEv2Config)
	m.HandleFunc("PUT /api/ikev2", s.updateIKEv2Config)
	m.HandleFunc("GET /api/openvpn", s.getOpenVPNConfig)
	m.HandleFunc("PUT /api/openvpn", s.updateOpenVPNConfig)
	m.HandleFunc("GET /api/openvpn/profile", s.downloadOpenVPNProfile)
}

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
	if err := s.app.CheckExitPolicy(pc.ExitPolicy); err != nil {
		http.Error(w, err.Error(), 400)
		return
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
	if err := s.app.CheckExitPolicy(pc.ExitPolicy); err != nil {
		http.Error(w, err.Error(), 400)
		return
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

func (s *Server) getTrojanConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Store().Get()
	if cfg.Trojan == nil {
		writeJSON(w, app.TrojanConfig{})
		return
	}
	writeJSON(w, cfg.Trojan)
}

func (s *Server) updateTrojanConfig(w http.ResponseWriter, r *http.Request) {
	var tc app.TrojanConfig
	if err := json.NewDecoder(r.Body).Decode(&tc); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	tc.Listen = strings.TrimSpace(tc.Listen)
	tc.Fallback = strings.TrimSpace(tc.Fallback)
	if tc.Enabled && tc.Listen == "" {
		http.Error(w, "listen required", 400)
		return
	}
	if tc.Listen != "" {
		if _, _, err := net.SplitHostPort(tc.Listen); err != nil {
			http.Error(w, "listen: "+err.Error(), 400)
			return
		}
	}
	if tc.Fallback != "" {
		if _, _, err := net.SplitHostPort(tc.Fallback); err != nil {
			http.Error(w, "fallback: "+err.Error(), 400)
			return
		}
	}
	if tc.Enabled || tc.CertID != "" {
		id := tc.CertID
		if id == "" {
			id = app.TrojanDefaultCert
		}
		if _, err := s.app.TLS().TLSConfig(id); err != nil {
			http.Error(w, "certificate: "+err.Error(), 400)
			return
		}
	}
	s.app.Store().Update(func(c *app.Config) {
		c.Trojan = &tc
	})
	go s.app.RestartTrojan()
	writeJSON(w, map[string]string{"status": "ok"})
}

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
	if err := s.app.CheckExitPolicy(ikev2.DefaultExitPolicy); err != nil {
		http.Error(w, err.Error(), 400)
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
		"running":       s.app.VPNRunning("openvpn"),
		"connected":     s.app.VPNClientCount("openvpn"),
		"enabled":       false,
		"port":          1194,
		"proto":         "udp",
		"pool":          "10.8.0.1/24",
		"pool6":         app.DefaultOpenVPNPool6,
		"cert_id":       "",
		"mtu":           1400,
		"listen_ip":     "",
		"dns":           []string{},
		"control":       "",
		"auth":          "",
		"shape":         false,
		"has_key":       false,
		"full_tunnel":   true,
		"tunnel_ipv6":   true,
		"routes":        []string{},
		"profile_extra": "",
	}
	if c := cfg.OpenVPN; c != nil {
		result["enabled"] = c.Enabled
		result["port"] = c.Port
		result["proto"] = orDefault(c.Proto, "udp")
		result["pool"] = c.Pool
		result["pool6"] = orDefault(c.Pool6, app.DefaultOpenVPNPool6)
		result["cert_id"] = c.CertID
		result["mtu"] = c.MTU
		result["listen_ip"] = c.ListenIP
		result["dns"] = append([]string{}, c.DNS...)
		result["control"] = c.Control
		result["auth"] = c.Auth
		result["shape"] = c.Shape
		result["has_key"] = c.StaticKey != ""
		result["full_tunnel"] = c.FullTunnel == nil || *c.FullTunnel
		result["tunnel_ipv6"] = c.TunnelIPv6 == nil || *c.TunnelIPv6
		result["routes"] = append([]string{}, c.Routes...)
		result["profile_extra"] = c.ProfileExtra
	}
	writeJSON(w, result)
}

func (s *Server) updateOpenVPNConfig(w http.ResponseWriter, r *http.Request) {
	var ovpn app.OpenVPNConfig
	if err := json.NewDecoder(r.Body).Decode(&ovpn); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := ovpn.Check(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.app.Store().Update(func(c *app.Config) {
		// The static key is the node's: kept across saves, made when a
		// protection mode first needs one, replaced on request.
		if c.OpenVPN != nil && !ovpn.RotateKey {
			ovpn.StaticKey = c.OpenVPN.StaticKey
		}
		if ovpn.StaticKey == "" && (ovpn.Control != "" || ovpn.RotateKey) {
			ovpn.StaticKey = app.NewOpenVPNStaticKey()
		}
		ovpn.RotateKey = false
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

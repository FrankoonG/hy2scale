package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	hyclient "github.com/apernet/hysteria/core/v2/client"
	hyserver "github.com/apernet/hysteria/core/v2/server"
	"github.com/FrankoonG/hy2scale/internal/relay"
)

type ClientEntry struct {
	Name     string `yaml:"name" json:"name"`
	Addr     string `yaml:"addr" json:"addr"`
	Password string `yaml:"password" json:"password"`

	// TLS
	SNI      string `yaml:"sni,omitempty" json:"sni"`
	Insecure bool   `yaml:"insecure" json:"insecure"`
	CA       string `yaml:"ca,omitempty" json:"ca"` // PEM content or path

	// Bandwidth (bytes/sec)
	MaxTx int `yaml:"max_tx,omitempty" json:"max_tx"`
	MaxRx int `yaml:"max_rx,omitempty" json:"max_rx"`

	// QUIC
	InitStreamWindow int `yaml:"init_stream_window,omitempty" json:"init_stream_window"`
	MaxStreamWindow  int `yaml:"max_stream_window,omitempty" json:"max_stream_window"`
	InitConnWindow   int `yaml:"init_conn_window,omitempty" json:"init_conn_window"`
	MaxConnWindow    int `yaml:"max_conn_window,omitempty" json:"max_conn_window"`

	// Misc
	FastOpen bool `yaml:"fast_open,omitempty" json:"fast_open"`
	Disabled bool `yaml:"disabled,omitempty" json:"disabled"`
}

type PeerConfig struct {
	Nested bool `yaml:"nested" json:"nested"`
}

type ServerConfig struct {
	Listen   string `yaml:"listen" json:"listen"`
	Password string `yaml:"password" json:"password"`
	TLSCert  string `yaml:"tls_cert" json:"tls_cert"`
	TLSKey   string `yaml:"tls_key" json:"tls_key"`
}

type SOCKS5Config struct {
	Listen  string `yaml:"listen"`
	ExitVia string `yaml:"exit_via"`
}

type Config struct {
	NodeID     string                `yaml:"node_id" json:"node_id"`
	Name       string                `yaml:"name" json:"name"`
	ExitNode     bool                  `yaml:"exit_node" json:"exit_node"`
	Hy2UserAuth  bool                  `yaml:"hy2_user_auth,omitempty" json:"hy2_user_auth"`
	Server     *ServerConfig         `yaml:"server" json:"server"`
	Clients    []ClientEntry         `yaml:"clients" json:"clients"`
	Peers      map[string]PeerConfig `yaml:"peers" json:"peers"`
	SOCKS5     *SOCKS5Config         `yaml:"socks5,omitempty" json:"-"`
	Users      []UserConfig          `yaml:"users" json:"users"`
	Proxies    []ProxyConfig         `yaml:"proxies" json:"proxies"`
	SS         *SSConfig             `yaml:"ss,omitempty" json:"ss,omitempty"`
	L2TP       *L2TPConfig           `yaml:"l2tp,omitempty" json:"l2tp,omitempty"`
	IKEv2      *IKEv2Config          `yaml:"ikev2,omitempty" json:"ikev2,omitempty"`
	WireGuard  *WireGuardConfig      `yaml:"wireguard,omitempty" json:"wireguard,omitempty"`
	Rules      []RoutingRule         `yaml:"rules,omitempty" json:"rules,omitempty"`
	UIListen    string                `yaml:"ui_listen,omitempty" json:"ui_listen,omitempty"`
	UIBasePath  string                `yaml:"ui_base_path,omitempty" json:"ui_base_path,omitempty"`
	WebUsername string                `yaml:"web_username,omitempty" json:"web_username,omitempty"`
	WebPassword string                `yaml:"web_password,omitempty" json:"web_password,omitempty"`
	DNS         string                `yaml:"dns,omitempty" json:"dns,omitempty"`
	ForceHTTPS  bool                  `yaml:"force_https,omitempty" json:"force_https,omitempty"`
	HTTPSCertID     string                `yaml:"https_cert_id,omitempty" json:"https_cert_id,omitempty"`
	SessionTimeoutH int                   `yaml:"session_timeout_h,omitempty" json:"session_timeout_h,omitempty"` // hours, default 12
}

type proxyHandle struct {
	listener net.Listener
	cancel   context.CancelFunc
}

type App struct {
	store        *ConfigStore
	node         *relay.Node
	tls          *TLSStore
	dataDir      string
	appCtx       context.Context
	mu           sync.Mutex
	clientCancel map[string]context.CancelFunc
	proxyHandles map[string]*proxyHandle
	srvCancel    context.CancelFunc
	ssListener   net.Listener
	ssCancel     context.CancelFunc
	l2tpCancel   context.CancelFunc
	ikev2Cancel  context.CancelFunc
	usersMu      sync.RWMutex
	userIndex    map[string]*UserConfig // username → user (for fast auth lookup)
	trafficDirty sync.Map              // username → true (needs flush)
	Sessions     *SessionManager
}

func New(dataDir string) (*App, error) {
	cfg, err := LoadOrInitConfig(dataDir)
	if err != nil {
		return nil, err
	}

	persistPath := dataDir + "/config.yaml"

	return &App{
		store:        NewConfigStore(cfg, persistPath),
		node:         relay.NewNode(cfg.Name, cfg.ExitNode),
		tls:          NewTLSStore(dataDir),
		dataDir:      dataDir,
		clientCancel: make(map[string]context.CancelFunc),
		proxyHandles: make(map[string]*proxyHandle),
		Sessions:     NewSessionManager(),
	}, nil
}

func (a *App) Store() *ConfigStore { return a.store }
func (a *App) Node() *relay.Node   { return a.node }
func (a *App) TLS() *TLSStore     { return a.tls }

func (a *App) GetConfig() Config { return a.store.Get() }

func (a *App) UpdateWebCredentials(username, passHash string) {
	a.store.Update(func(c *Config) {
		c.WebUsername = username
		c.WebPassword = passHash
	})
}

// PersistNodeID writes the node ID to the persistent file.
func (a *App) PersistNodeID(id string) {
	if a.dataDir != "" {
		os.MkdirAll(a.dataDir, 0755)
		os.WriteFile(a.dataDir+"/node-id", []byte(id), 0644)
	}
}

func (a *App) Run(ctx context.Context) error {
	a.appCtx = ctx
	cfg := a.store.Get()
	fmt.Println(`
 _   ___   ______   _____ _____ ___  _     _____
| | | \ \ / /___ \ / ____/ ____/ _ \| |   |  ___|
| |_| |\ V /  __) | (___| |  | |_| | |   | |_
|  _  | | |  |__ < \___ \| |  |  _  | |   |  _|
| | | | | |  ___) |____) | |__| | | | |___| |___
|_| |_| |_| |____/|_____/\____/_| |_|_____|_____|`)
	fmt.Printf("  v%s\n\n", AppVersion)
	log.Printf("[%s] starting node id=%s (exit=%v)", cfg.Name, cfg.NodeID, cfg.ExitNode)
	if debugMode() {
		log.Printf("[debug] DEBUG mode enabled (set DEBUG=true in environment)")
	}

	// Start rate ticker, latency prober, and traffic flusher
	go a.node.StartRateTicker(ctx)
	go a.node.StartLatencyProber(ctx)
	go a.StartTrafficFlusher(ctx)
	a.rebuildUserIndex()

	// Apply nested discovery
	for peerName, pc := range cfg.Peers {
		if pc.Nested {
			a.node.SetNestedDiscovery(peerName, true)
			log.Printf("[%s] nested discovery enabled for %q", cfg.Name, peerName)
		}
	}

	// Start hy2 server if configured — check port first
	if cfg.Server != nil {
		port := 5565
		if cfg.Server.Listen != "" {
			if _, p, err := net.SplitHostPort(cfg.Server.Listen); err == nil {
				if pn, err := strconv.Atoi(p); err == nil {
					port = pn
				}
			}
		}
		conflicts := CheckPorts([]PortConflict{{Port: port, Proto: "udp", Desc: "hy2 server"}})
		if len(conflicts) > 0 {
			return fmt.Errorf("FATAL: hy2 server port %d/udp is already in use — cannot start", port)
		}
		if err := a.restartServer(); err != nil {
			return err
		}
	}

	// Start proxies (SOCKS5 etc) — check port before each
	for _, pc := range cfg.Proxies {
		if pc.Enabled {
			if _, p, err := net.SplitHostPort(pc.Listen); err == nil {
				if pn, _ := strconv.Atoi(p); pn > 0 {
					if c := CheckPorts([]PortConflict{{Port: pn, Proto: "tcp", Desc: pc.Protocol}}); len(c) > 0 {
						log.Printf("[%s] port %d/tcp in use — proxy disabled", pc.Protocol, pn)
						continue
					}
				}
			}
		}
		a.StartProxy(pc)
	}

	// Start SS server if configured
	if cfg.SS != nil && cfg.SS.Enabled {
		if _, p, err := net.SplitHostPort(cfg.SS.Listen); err == nil {
			if pn, _ := strconv.Atoi(p); pn > 0 {
				if c := CheckPorts([]PortConflict{{Port: pn, Proto: "tcp", Desc: "shadowsocks"}}); len(c) > 0 {
					log.Printf("[ss] port %d/tcp in use — shadowsocks disabled", pn)
					cfg.SS = nil
				}
			}
		}
		if cfg.SS != nil {
			a.StartSS(*cfg.SS)
		}
	}

	// Start L2TP server if configured
	if cfg.L2TP != nil && cfg.L2TP.Enabled {
		lPort := 1701
		if cfg.L2TP.Listen != "" {
			if pn, err := strconv.Atoi(cfg.L2TP.Listen); err == nil {
				lPort = pn
			}
		}
		if c := CheckPorts([]PortConflict{{Port: lPort, Proto: "udp", Desc: "l2tp"}}); len(c) > 0 {
			log.Printf("[l2tp] port %d/udp in use — l2tp disabled", lPort)
		} else if err := a.StartL2TP(*cfg.L2TP); err != nil {
			log.Printf("[l2tp] start error: %v", err)
		}
	}

	// Start IKEv2/IPsec
	// Skip port check if L2TP already started strongswan (shared 500/4500)
	if cfg.IKEv2 != nil && cfg.IKEv2.Enabled {
		l2tpActive := cfg.L2TP != nil && cfg.L2TP.Enabled
		if !l2tpActive {
			if c := CheckPorts([]PortConflict{
				{Port: 500, Proto: "udp", Desc: "IKE"},
				{Port: 4500, Proto: "udp", Desc: "IKE NAT-T"},
			}); len(c) > 0 {
				for _, cc := range c {
					log.Printf("[ikev2] port %d/%s in use — ikev2 disabled", cc.Port, cc.Proto)
				}
				goto skipIKEv2
			}
		}
		if err := a.StartIKEv2(*cfg.IKEv2); err != nil {
			log.Printf("[ikev2] start error: %v", err)
		}
	}
	skipIKEv2:

	// Start WireGuard
	if cfg.WireGuard != nil && cfg.WireGuard.Enabled {
		wgPort := cfg.WireGuard.ListenPort
		if wgPort == 0 {
			wgPort = 51820
		}
		if c := CheckPorts([]PortConflict{{Port: wgPort, Proto: "udp", Desc: "wireguard"}}); len(c) > 0 {
			log.Printf("[wireguard] port %d/udp in use — wireguard disabled", wgPort)
		} else if err := a.StartWireGuard(*cfg.WireGuard); err != nil {
			log.Printf("[wireguard] start error: %v", err)
		}
	}

	// Start rule engine (host mode only)
	a.StartRuleEngine()

	// Start clients
	for _, cl := range cfg.Clients {
		a.StartClient(cl)
	}

	<-ctx.Done()
	a.StopRuleEngine()
	return ctx.Err()
}

// --- Dynamic client management ---

func (a *App) StartClient(cl ClientEntry) {
	if cl.Disabled {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.clientCancel[cl.Addr]; ok {
		return // already running for this address
	}
	ctx, cancel := context.WithCancel(a.appCtx)
	a.clientCancel[cl.Addr] = cancel
	go a.connectLoop(ctx, cl)
}

// ReconnectAll stops and restarts all connections (outbound + hy2 server) after ID change.
// Restarting the server disconnects all inbound peers, forcing them to reconnect and see the new ID.
func (a *App) ReconnectAll() {
	cfg := a.store.Get()
	// Stop all outbound
	a.mu.Lock()
	for name, cancel := range a.clientCancel {
		cancel()
		delete(a.clientCancel, name)
	}
	a.mu.Unlock()

	// Restart hy2 server to disconnect inbound peers
	if err := a.restartServer(); err != nil {
		log.Printf("[%s] server restart after ID change: %v", a.node.Name(), err)
	}

	time.Sleep(time.Second)
	// Restart outbound
	for _, cl := range cfg.Clients {
		if !cl.Disabled {
			a.StartClient(cl)
		}
	}
	log.Printf("[%s] reconnected all peers after ID change", a.node.Name())
}

func (a *App) StopClient(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Try by addr first, then by name (addr is the stable key)
	if cancel, ok := a.clientCancel[name]; ok {
		cancel()
		delete(a.clientCancel, name)
		return
	}
	// Look up addr from config by name
	cfg := a.store.Get()
	for _, cl := range cfg.Clients {
		if cl.Name == name {
			if cancel, ok := a.clientCancel[cl.Addr]; ok {
				cancel()
				delete(a.clientCancel, cl.Addr)
			}
			return
		}
	}
}

func (a *App) AddClient(cl ClientEntry) error {
	a.StartClient(cl)
	return a.store.Update(func(c *Config) {
		// Deduplicate by Addr (stable key)
		for _, existing := range c.Clients {
			if existing.Addr == cl.Addr {
				return
			}
		}
		c.Clients = append(c.Clients, cl)
	})
}

func (a *App) UpdateClient(cl ClientEntry) error {
	a.StopClient(cl.Name)
	a.StartClient(cl)
	return a.store.Update(func(c *Config) {
		for i, existing := range c.Clients {
			if existing.Name == cl.Name || existing.Addr == cl.Addr {
				c.Clients[i] = cl
				return
			}
		}
		c.Clients = append(c.Clients, cl)
	})
}

// UpdateClientByAddr finds a client by oldName (name or addr) and replaces it.
func (a *App) UpdateClientByAddr(oldName string, cl ClientEntry) error {
	a.StopClient(oldName)
	return a.store.Update(func(c *Config) {
		for i, existing := range c.Clients {
			if existing.Name == oldName || existing.Addr == oldName {
				cl.Addr = existing.Addr // preserve addr
				c.Clients[i] = cl
				a.StartClient(cl)
				return
			}
		}
	})
}

func (a *App) RemoveClient(name string) error {
	a.StopClient(name)
	return a.store.Update(func(c *Config) {
		for i, cl := range c.Clients {
			if cl.Name == name || cl.Addr == name {
				c.Clients = append(c.Clients[:i], c.Clients[i+1:]...)
				return
			}
		}
	})
}

func (a *App) SetClientDisabled(name string, disabled bool) error {
	if disabled {
		a.StopClient(name)
	} else {
		cfg := a.store.Get()
		for _, cl := range cfg.Clients {
			if cl.Name == name || cl.Addr == name {
				cl.Disabled = false
				a.StartClient(cl)
				break
			}
		}
	}
	return a.store.Update(func(c *Config) {
		for i, cl := range c.Clients {
			if cl.Name == name || cl.Addr == name {
				c.Clients[i].Disabled = disabled
				return
			}
		}
	})
}

// --- Dynamic proxy management ---

func (a *App) StartProxy(pc ProxyConfig) {
	if !pc.Enabled {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.proxyHandles[pc.ID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(a.appCtx)
	ln, err := net.Listen("tcp", pc.Listen)
	if err != nil {
		log.Printf("[%s] proxy %s: %v", a.node.Name(), pc.ID, err)
		cancel()
		return
	}
	a.proxyHandles[pc.ID] = &proxyHandle{listener: ln, cancel: cancel}
	log.Printf("[%s] %s proxy %s on %s (exit_via=%q)", a.node.Name(), pc.Protocol, pc.ID, pc.Listen, pc.ExitVia)
	go a.serveProxy(ctx, ln, pc)
}

func (a *App) StopProxy(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if h, ok := a.proxyHandles[id]; ok {
		h.cancel()
		h.listener.Close()
		delete(a.proxyHandles, id)
	}
}

func (a *App) AddProxy(pc ProxyConfig) error {
	a.StartProxy(pc)
	return a.store.Update(func(c *Config) {
		for _, existing := range c.Proxies {
			if existing.ID == pc.ID {
				return
			}
		}
		c.Proxies = append(c.Proxies, pc)
	})
}

func (a *App) RemoveProxy(id string) error {
	a.StopProxy(id)
	return a.store.Update(func(c *Config) {
		for i, p := range c.Proxies {
			if p.ID == id {
				c.Proxies = append(c.Proxies[:i], c.Proxies[i+1:]...)
				return
			}
		}
	})
}

func (a *App) UpdateProxy(pc ProxyConfig) error {
	a.StopProxy(pc.ID)
	a.StartProxy(pc)
	return a.store.Update(func(c *Config) {
		for i, p := range c.Proxies {
			if p.ID == pc.ID {
				c.Proxies[i] = pc
				return
			}
		}
		c.Proxies = append(c.Proxies, pc)
	})
}

// --- User management ---

func (a *App) rebuildUserIndex() {
	cfg := a.store.Get()
	index := make(map[string]*UserConfig, len(cfg.Users))
	for i := range cfg.Users {
		index[cfg.Users[i].Username] = &cfg.Users[i]
	}
	a.usersMu.Lock()
	a.userIndex = index
	a.usersMu.Unlock()
}

func (a *App) LookupUser(username, password string) (*UserConfig, error) {
	a.usersMu.RLock()
	u, ok := a.userIndex[username]
	a.usersMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("user not found")
	}
	if u.Password != password {
		return nil, fmt.Errorf("invalid password")
	}
	if !u.Enabled {
		return nil, fmt.Errorf("user disabled")
	}
	if u.ExpiryDate != "" {
		if t, err := time.Parse("2006-01-02", u.ExpiryDate); err == nil && time.Now().After(t) {
			return nil, fmt.Errorf("user expired")
		}
	}
	if u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit {
		return nil, fmt.Errorf("traffic limit exceeded")
	}
	return u, nil
}

// syncVPNSecrets updates chap-secrets (L2TP) and EAP secrets (IKEv2) after user changes.
func (a *App) syncVPNSecrets() {
	cfg := a.store.Get()
	if cfg.L2TP != nil && cfg.L2TP.Enabled {
		a.updateChapSecrets()
	}
	if cfg.IKEv2 != nil && cfg.IKEv2.Enabled && cfg.IKEv2.Mode == "mschapv2" {
		a.updateEAPSecrets()
	}
}

func (a *App) AddUser(u UserConfig) error {
	err := a.store.Update(func(c *Config) {
		for _, existing := range c.Users {
			if existing.Username == u.Username {
				return
			}
		}
		c.Users = append(c.Users, u)
	})
	if err == nil {
		a.rebuildUserIndex()
		a.syncVPNSecrets()
	}
	return err
}

func (a *App) UpdateUser(id string, u UserConfig) error {
	err := a.store.Update(func(c *Config) {
		for i, existing := range c.Users {
			if existing.ID == id {
				u.TrafficUsed = existing.TrafficUsed
				c.Users[i] = u
				return
			}
		}
	})
	if err == nil {
		a.rebuildUserIndex()
		a.syncVPNSecrets()
	}
	return err
}

func (a *App) RemoveUser(id string) error {
	err := a.store.Update(func(c *Config) {
		for i, u := range c.Users {
			if u.ID == id {
				c.Users = append(c.Users[:i], c.Users[i+1:]...)
				return
			}
		}
	})
	if err == nil {
		a.rebuildUserIndex()
		a.syncVPNSecrets()
	}
	return err
}

func (a *App) ToggleUser(id string, enabled bool) error {
	err := a.store.Update(func(c *Config) {
		for i, u := range c.Users {
			if u.ID == id {
				c.Users[i].Enabled = enabled
				return
			}
		}
	})
	if err == nil {
		a.rebuildUserIndex()
	}
	return err
}

func (a *App) ResetUserTraffic(id string) error {
	err := a.store.Update(func(c *Config) {
		for i, u := range c.Users {
			if u.ID == id {
				c.Users[i].TrafficUsed = 0
				return
			}
		}
	})
	if err == nil {
		a.rebuildUserIndex()
	}
	return err
}

func (a *App) RecordTraffic(username string, bytes int64) {
	a.usersMu.RLock()
	u, ok := a.userIndex[username]
	a.usersMu.RUnlock()
	if !ok {
		return
	}
	atomic.AddInt64(&u.TrafficUsed, bytes)
	a.trafficDirty.Store(username, true)
}

// FlushTraffic persists dirty traffic counters to config.
func (a *App) FlushTraffic() {
	dirty := false
	a.trafficDirty.Range(func(key, _ any) bool {
		dirty = true
		a.trafficDirty.Delete(key)
		return true
	})
	if !dirty {
		return
	}
	a.usersMu.RLock()
	a.store.Update(func(c *Config) {
		for i := range c.Users {
			if u, ok := a.userIndex[c.Users[i].Username]; ok {
				c.Users[i].TrafficUsed = atomic.LoadInt64(&u.TrafficUsed)
			}
		}
	})
	a.usersMu.RUnlock()
}

// StartTrafficFlusher runs a background goroutine to periodically flush traffic.
func (a *App) StartTrafficFlusher(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.FlushTraffic()
			return
		case <-t.C:
			a.FlushTraffic()
		}
	}
}

// --- Nested discovery ---

func (a *App) SetNested(peer string, enabled bool) error {
	a.node.SetNestedDiscovery(peer, enabled)
	return a.store.Update(func(c *Config) {
		if c.Peers == nil {
			c.Peers = make(map[string]PeerConfig)
		}
		pc := c.Peers[peer]
		pc.Nested = enabled
		c.Peers[peer] = pc
	})
}

// --- Internal ---

// RestartServer is the public wrapper for restartServer.
func (a *App) RestartServer() {
	if err := a.restartServer(); err != nil {
		log.Printf("[server] restart error: %v", err)
	}
}

// restartServer stops the current hy2 server (if running) and starts a new one.
// This disconnects all inbound peers, forcing them to reconnect and discover the new ID.
func (a *App) restartServer() error {
	if a.srvCancel != nil {
		a.srvCancel()
		time.Sleep(500 * time.Millisecond)
	}
	cfg := a.store.Get()
	if cfg.Server == nil || cfg.Server.Listen == "" {
		return nil
	}
	srvCtx, cancel := context.WithCancel(a.appCtx)
	a.srvCancel = cancel
	return a.startServer(srvCtx, cfg.Server)
}

func (a *App) startServer(ctx context.Context, sc *ServerConfig) error {
	cert, err := loadCert(sc.TLSCert, sc.TLSKey)
	if err != nil {
		return err
	}
	conn, err := net.ListenPacket("udp", sc.Listen)
	if err != nil {
		return err
	}
	hyServer, err := hyserver.NewServer(&hyserver.Config{
		Conn: conn,
		TLSConfig: hyserver.TLSConfig{
			Certificates: []tls.Certificate{cert},
		},
		QUICConfig: hyserver.QUICConfig{
			InitialStreamReceiveWindow:     67108864,
			MaxStreamReceiveWindow:         67108864,
			InitialConnectionReceiveWindow: 134217728,
			MaxConnectionReceiveWindow:     134217728,
			MaxIncomingStreams:              4096,
		},
		Authenticator: &hy2Auth{app: a, sysPassword: sc.Password},
		Outbound:      &nodeOutbound{app: a, node: a.node, ctx: ctx},
	})
	if err != nil {
		conn.Close()
		return err
	}
	go func() { <-ctx.Done(); hyServer.Close(); conn.Close() }()
	go hyServer.Serve()
	log.Printf("[%s] hy2 server on %s", a.node.Name(), sc.Listen)
	return nil
}

func (a *App) connectLoop(ctx context.Context, cl ClientEntry) {
	addr := cl.Addr // stable key
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Re-read latest config for this addr (Name may have changed)
		cfg := a.store.Get()
		for _, entry := range cfg.Clients {
			if entry.Addr == addr {
				cl = entry
				break
			}
		}
		log.Printf("[%s] connecting to %s (%s)", a.node.Name(), cl.Name, cl.Addr)
		if err := a.connect(ctx, cl); err != nil {
			log.Printf("[%s] %s: %v", a.node.Name(), cl.Name, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (a *App) connect(ctx context.Context, cl ClientEntry) error {
	addr, err := net.ResolveUDPAddr("udp", cl.Addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", cl.Addr, err)
	}

	// TLS
	tlsCfg := hyclient.TLSConfig{
		InsecureSkipVerify: cl.Insecure,
		ServerName:         cl.SNI,
	}
	if tlsCfg.ServerName == "" {
		tlsCfg.ServerName = "hy2scale"
	}
	if cl.CA != "" {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM([]byte(cl.CA))
		tlsCfg.RootCAs = pool
	}

	// Bandwidth
	maxTx := uint64(125000000) // 1Gbps default
	maxRx := uint64(125000000)
	if cl.MaxTx > 0 {
		maxTx = uint64(cl.MaxTx)
	}
	if cl.MaxRx > 0 {
		maxRx = uint64(cl.MaxRx)
	}

	// QUIC windows
	isw := uint64(67108864)  // 64MB
	msw := uint64(67108864)
	icw := uint64(134217728) // 128MB
	mcw := uint64(134217728)
	if cl.InitStreamWindow > 0 {
		isw = uint64(cl.InitStreamWindow)
	}
	if cl.MaxStreamWindow > 0 {
		msw = uint64(cl.MaxStreamWindow)
	}
	if cl.InitConnWindow > 0 {
		icw = uint64(cl.InitConnWindow)
	}
	if cl.MaxConnWindow > 0 {
		mcw = uint64(cl.MaxConnWindow)
	}

	c, _, err := hyclient.NewClient(&hyclient.Config{
		ServerAddr: addr,
		Auth:       cl.Password,
		TLSConfig:  tlsCfg,
		QUICConfig: hyclient.QUICConfig{
			InitialStreamReceiveWindow:     isw,
			MaxStreamReceiveWindow:         msw,
			InitialConnectionReceiveWindow: icw,
			MaxConnectionReceiveWindow:     mcw,
		},
		BandwidthConfig: hyclient.BandwidthConfig{
			MaxTx: maxTx,
			MaxRx: maxRx,
		},
		FastOpen: cl.FastOpen,
	})
	if err != nil {
		return err
	}
	defer c.Close()
	log.Printf("[%s] connected to %s (%s)", a.node.Name(), cl.Name, cl.Addr)

	// Try hy2scale relay protocol first
	err = a.node.AttachTo(ctx, cl.Name, c, func(remoteID string) {
		if remoteID != "" && remoteID != cl.Name {
			log.Printf("[%s] peer %s actual ID: %s", a.node.Name(), cl.Addr, remoteID)
			oldName := cl.Name
			a.store.Update(func(cfg *Config) {
				for i, entry := range cfg.Clients {
					if entry.Addr == cl.Addr {
						cfg.Clients[i].Name = remoteID
						break
					}
				}
				// Migrate Peers config (nested flag etc.) to new name
				if pc, ok := cfg.Peers[oldName]; ok {
					cfg.Peers[remoteID] = pc
					delete(cfg.Peers, oldName)
				}
			})
			// Clean up old name from relay
			a.node.SetNestedDiscovery(remoteID, a.node.IsNestedEnabled(oldName))
			a.node.SetNestedDiscovery(oldName, false)
			// Clear old latency so topology doesn't show stale entry
			a.node.SetLatency(oldName, 0)
		}
	})
	if err == relay.ErrNotHy2scale && ctx.Err() == nil {
		// Relay protocol not supported — native hy2 server
		log.Printf("[%s] %s is not hy2scale, attaching as native hy2", a.node.Name(), cl.Addr)
		// Need a fresh client since the old one may be broken
		c2, _, err2 := hyclient.NewClient(&hyclient.Config{
			ServerAddr: addr,
			Auth:       cl.Password,
			TLSConfig:  tlsCfg,
			QUICConfig: hyclient.QUICConfig{
				InitialStreamReceiveWindow:     isw,
				MaxStreamReceiveWindow:         msw,
				InitialConnectionReceiveWindow: icw,
				MaxConnectionReceiveWindow:     mcw,
			},
			BandwidthConfig: hyclient.BandwidthConfig{MaxTx: maxTx, MaxRx: maxRx},
			FastOpen:        cl.FastOpen,
		})
		if err2 != nil {
			return err // return original error
		}
		defer c2.Close()
		return a.node.AttachNative(ctx, cl.Name, c2)
	}
	return err
}

func (a *App) serveProxy(ctx context.Context, ln net.Listener, pc ProxyConfig) {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go a.handleSOCKS5(c)
	}
}

func (a *App) handleSOCKS5(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 512)

	// SOCKS5 greeting
	n, _ := conn.Read(buf)
	if n < 2 || buf[0] != 0x05 {
		return
	}

	// Check if users are configured
	cfg := a.store.Get()
	hasUsers := len(cfg.Users) > 0

	var user *UserConfig
	if hasUsers {
		// Require username/password auth (RFC 1929, method 0x02)
		conn.Write([]byte{0x05, 0x02})

		// Read auth: {ver=0x01, ulen, username, plen, password}
		n, _ = conn.Read(buf)
		if n < 3 || buf[0] != 0x01 {
			conn.Write([]byte{0x01, 0x01}) // auth failed
			return
		}
		ulen := int(buf[1])
		if n < 2+ulen+1 {
			conn.Write([]byte{0x01, 0x01})
			return
		}
		username := string(buf[2 : 2+ulen])
		plen := int(buf[2+ulen])
		if n < 3+ulen+plen {
			conn.Write([]byte{0x01, 0x01})
			return
		}
		password := string(buf[3+ulen : 3+ulen+plen])

		var err error
		user, err = a.LookupUser(username, password)
		if err != nil {
			conn.Write([]byte{0x01, 0x01}) // auth failed
			return
		}
		conn.Write([]byte{0x01, 0x00}) // auth success
	} else {
		// No users configured — no auth required (backward compat)
		conn.Write([]byte{0x05, 0x00})
	}

	// SOCKS5 request
	n, _ = conn.Read(buf)
	if n < 7 || buf[1] != 0x01 {
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	var addr string
	switch buf[3] {
	case 0x01:
		addr = fmt.Sprintf("%d.%d.%d.%d:%d", buf[4], buf[5], buf[6], buf[7], int(buf[8])<<8|int(buf[9]))
	case 0x03:
		dl := int(buf[4])
		addr = fmt.Sprintf("%s:%d", buf[5:5+dl], int(buf[5+dl])<<8|int(buf[5+dl+1]))
	case 0x04:
		addr = fmt.Sprintf("[%s]:%d", net.IP(buf[4:20]), int(buf[20])<<8|int(buf[21]))
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Route based on user's exit_via (or direct if no user)
	exitVia := ""
	username := ""
	exitMode := ""
	if user != nil {
		exitVia = user.ExitVia
		exitMode = user.ExitMode
		username = user.Username
	}

	var remote net.Conn
	var err error
	if exitVia == "" {
		remote, err = net.DialTimeout("tcp", addr, 10*time.Second)
	} else {
		remote, err = a.dialExitWithMode(context.Background(), exitVia, exitMode, addr)
	}
	if err != nil {
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer remote.Close()
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// Session tracking — aggregated per device (username+IP+protocol)
	remoteIP := ""
	if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		remoteIP = ta.IP.String()
	}
	ctx, cancel := context.WithCancel(context.Background())
	sid := a.Sessions.Connect(username, remoteIP, "socks5", cancel)

	var up, down int64
	done := make(chan struct{})
	go func() {
		n, _ := copyCtx(ctx, remote, conn)
		atomic.AddInt64(&up, n)
		remote.Close()
		done <- struct{}{}
	}()
	n2, _ := copyCtx(ctx, conn, remote)
	atomic.AddInt64(&down, n2)
	<-done
	cancel()
	a.Sessions.Disconnect(sid, atomic.LoadInt64(&up), atomic.LoadInt64(&down))
	if username != "" {
		a.RecordTraffic(username, atomic.LoadInt64(&up)+atomic.LoadInt64(&down))
	}
}

// copyCtx copies data respecting context cancellation.
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	done := make(chan struct{})
	var n int64
	var err error
	go func() {
		n, err = io.Copy(dst, src)
		close(done)
	}()
	select {
	case <-done:
		return n, err
	case <-ctx.Done():
		// Force close to unblock io.Copy
		if c, ok := src.(net.Conn); ok {
			c.Close()
		}
		if c, ok := dst.(net.Conn); ok {
			c.Close()
		}
		<-done
		return n, ctx.Err()
	}
}

func splitPath(s string) []string {
	var parts []string
	for _, p := range splitOn(s, '/') {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// dialExit routes traffic through an exit path, stripping the local node name prefix.
// dialExitWithMode routes traffic with the specified exit mode.
// mode: "" = direct, "stability" = adaptive failover, "speed" = load balance
func (a *App) dialExitWithMode(ctx context.Context, exitVia, exitMode, addr string) (net.Conn, error) {
	if exitMode != "" && exitVia != "" && !strings.Contains(exitVia, "/") {
		switch exitMode {
		case "stability":
			return a.dialAdaptive(ctx, exitVia, addr)
		case "speed":
			return a.dialLoadBalance(ctx, exitVia, addr)
		}
	}
	return a.dialExit(ctx, exitVia, addr)
}

// dialExit routes traffic through an exit path (direct mode, no adaptive/LB).
func (a *App) dialExit(ctx context.Context, exitVia, addr string) (net.Conn, error) {
	parts := splitPath(exitVia)
	// Strip leading self name (e.g. "AUB/64e7c9f5" on node AUB → ["64e7c9f5"])
	if len(parts) > 0 && parts[0] == a.node.Name() {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if len(parts) == 1 {
		return a.node.DialTCP(ctx, parts[0], addr)
	}
	return a.node.DialVia(ctx, parts, addr)
}

func splitOn(s string, sep byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

// --- helpers ---

type nodeOutbound struct {
	app  *App
	node *relay.Node
	ctx  context.Context
}

func (o *nodeOutbound) TCP(reqAddr string) (net.Conn, error) {
	if relay.IsRelayStream(reqAddr) {
		c1, c2 := net.Pipe()
		go o.node.HandleStream(o.ctx, reqAddr, c1)
		return c2, nil
	}
	// For native hy2 client users: their traffic is handled by hy2 server directly
	// Route through their exit_via if they authenticated as a user
	// Note: hy2 server doesn't pass user info to Outbound, so we can't do per-user
	// routing here. Native hy2 client users exit directly (local network).
	// Per-user exit routing works through SOCKS5/SS where we control the full flow.
	return net.DialTimeout("tcp", reqAddr, 10*time.Second)
}

func (o *nodeOutbound) UDP(addr string) (hyserver.UDPConn, error) {
	return &dummyUDP{}, nil
}

// hy2Auth handles authentication for the hy2 server.
// - User password → accept as user proxy client (identified by username)
// - System password → must be hy2scale relay (verified after connect via relay protocol)
type hy2Auth struct {
	app          *App
	sysPassword  string
}

func (a *hy2Auth) Authenticate(addr net.Addr, auth string, tx uint64) (bool, string) {
	// Check user passwords first
	cfg := a.app.store.Get()
	for _, u := range cfg.Users {
		if u.Password == auth && u.Enabled {
			// Check expiry
			if u.ExpiryDate != "" {
				if t, err := time.Parse("2006-01-02", u.ExpiryDate); err == nil && time.Now().After(t) {
					return false, ""
				}
			}
			// Check traffic limit
			if u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit {
				return false, ""
			}
			return true, "user:" + u.Username
		}
	}
	// Check system password (for hy2scale relay nodes)
	if auth == a.sysPassword {
		return true, "system"
	}
	return false, ""
}

type dummyUDP struct{}

func (d *dummyUDP) ReadFrom(b []byte) (int, string, error) { select {} }
func (d *dummyUDP) WriteTo(b []byte, addr string) (int, error) { return len(b), nil }
func (d *dummyUDP) Close() error                               { return nil }

func loadCert(certFile, keyFile string) (tls.Certificate, error) {
	if certFile != "" && keyFile != "" {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}
	log.Printf("generating self-signed cert")
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now(),
		NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

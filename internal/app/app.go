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
	"sync"
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
	ExitNode   bool                  `yaml:"exit_node" json:"exit_node"`
	Server     *ServerConfig         `yaml:"server" json:"server"`
	Clients    []ClientEntry         `yaml:"clients" json:"clients"`
	Peers      map[string]PeerConfig `yaml:"peers" json:"peers"`
	SOCKS5     *SOCKS5Config         `yaml:"socks5,omitempty" json:"-"`
	Proxies    []ProxyConfig         `yaml:"proxies" json:"proxies"`
	UIListen   string                `yaml:"ui_listen,omitempty" json:"ui_listen,omitempty"`
	UIBasePath string                `yaml:"ui_base_path,omitempty" json:"ui_base_path,omitempty"`
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
	}, nil
}

func (a *App) Store() *ConfigStore { return a.store }
func (a *App) Node() *relay.Node   { return a.node }
func (a *App) TLS() *TLSStore     { return a.tls }

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
	log.Printf("[%s] starting node id=%s (exit=%v)", cfg.Name, cfg.NodeID, cfg.ExitNode)

	// Start rate ticker
	go a.node.StartRateTicker(ctx)

	// Apply nested discovery
	for peerName, pc := range cfg.Peers {
		if pc.Nested {
			a.node.SetNestedDiscovery(peerName, true)
			log.Printf("[%s] nested discovery enabled for %q", cfg.Name, peerName)
		}
	}

	// Start hy2 server if configured
	if cfg.Server != nil {
		if err := a.startServer(ctx, cfg.Server); err != nil {
			return err
		}
	}

	// Start proxies
	for _, pc := range cfg.Proxies {
		a.StartProxy(pc)
	}

	// Start clients
	for _, cl := range cfg.Clients {
		a.StartClient(cl)
	}

	<-ctx.Done()
	return ctx.Err()
}

// --- Dynamic client management ---

func (a *App) StartClient(cl ClientEntry) {
	if cl.Disabled {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.clientCancel[cl.Name]; ok {
		return // already running
	}
	ctx, cancel := context.WithCancel(a.appCtx)
	a.clientCancel[cl.Name] = cancel
	go a.connectLoop(ctx, cl)
}

func (a *App) StopClient(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cancel, ok := a.clientCancel[name]; ok {
		cancel()
		delete(a.clientCancel, name)
	}
}

func (a *App) AddClient(cl ClientEntry) error {
	a.StartClient(cl)
	return a.store.Update(func(c *Config) {
		for _, existing := range c.Clients {
			if existing.Name == cl.Name {
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
			if existing.Name == cl.Name {
				c.Clients[i] = cl
				return
			}
		}
		c.Clients = append(c.Clients, cl)
	})
}

func (a *App) RemoveClient(name string) error {
	a.StopClient(name)
	return a.store.Update(func(c *Config) {
		for i, cl := range c.Clients {
			if cl.Name == name {
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
		// Find the client entry and start it
		cfg := a.store.Get()
		for _, cl := range cfg.Clients {
			if cl.Name == name {
				cl.Disabled = false
				a.StartClient(cl)
				break
			}
		}
	}
	return a.store.Update(func(c *Config) {
		for i, cl := range c.Clients {
			if cl.Name == name {
				c.Clients[i].Disabled = disabled
				return
			}
		}
	})
}

// --- Dynamic proxy management ---

func (a *App) StartProxy(pc ProxyConfig) {
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
		Authenticator: &simpleAuth{password: sc.Password},
		Outbound:      &nodeOutbound{node: a.node, ctx: ctx},
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
	for {
		select {
		case <-ctx.Done():
			return
		default:
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
	addr, _ := net.ResolveUDPAddr("udp", cl.Addr)

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
	log.Printf("[%s] connected to %s", a.node.Name(), cl.Name)
	return a.node.AttachTo(ctx, cl.Name, c)
}

func (a *App) serveProxy(ctx context.Context, ln net.Listener, pc ProxyConfig) {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go a.handleSOCKS5(c, pc.ExitVia)
	}
}

func (a *App) handleSOCKS5(conn net.Conn, exitVia string) {
	defer conn.Close()
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	if n < 2 || buf[0] != 0x05 {
		return
	}
	conn.Write([]byte{0x05, 0x00})
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

	var remote net.Conn
	var err error
	if exitVia == "" {
		remote, err = net.DialTimeout("tcp", addr, 10*time.Second)
	} else {
		parts := splitPath(exitVia)
		if len(parts) == 1 {
			remote, err = a.node.DialTCP(context.Background(), parts[0], addr)
		} else {
			remote, err = a.node.DialVia(context.Background(), parts, addr)
		}
	}
	if err != nil {
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer remote.Close()
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	go func() { io.Copy(remote, conn); remote.Close() }()
	io.Copy(conn, remote)
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
	node *relay.Node
	ctx  context.Context
}

func (o *nodeOutbound) TCP(reqAddr string) (net.Conn, error) {
	if relay.IsRelayStream(reqAddr) {
		c1, c2 := net.Pipe()
		go o.node.HandleStream(o.ctx, reqAddr, c1)
		return c2, nil
	}
	return net.DialTimeout("tcp", reqAddr, 10*time.Second)
}

func (o *nodeOutbound) UDP(addr string) (hyserver.UDPConn, error) {
	return &dummyUDP{}, nil
}

type simpleAuth struct{ password string }

func (a *simpleAuth) Authenticate(addr net.Addr, auth string, tx uint64) (bool, string) {
	if auth == a.password {
		return true, "user"
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

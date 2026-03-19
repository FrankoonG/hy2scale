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
	"time"

	hyclient "github.com/apernet/hysteria/core/v2/client"
	hyserver "github.com/apernet/hysteria/core/v2/server"
	"github.com/FrankoonG/hy2scale/internal/relay"
	"gopkg.in/yaml.v3"
)

type ClientEntry struct {
	Name      string `yaml:"name"`
	Addr      string `yaml:"addr"`
	Password  string `yaml:"password"`
	Bandwidth int    `yaml:"bandwidth"` // bytes/sec, 0 = default 125MB/s (1Gbps)
}

type PeerConfig struct {
	Nested bool `yaml:"nested"`
}

type ServerConfig struct {
	Listen   string `yaml:"listen"`
	Password string `yaml:"password"`
	TLSCert  string `yaml:"tls_cert"`
	TLSKey   string `yaml:"tls_key"`
}

type SOCKS5Config struct {
	Listen  string `yaml:"listen"`
	ExitVia string `yaml:"exit_via"` // peer name or "peer1/peer2" for nested
}

type Config struct {
	Name     string                `yaml:"name"`
	ExitNode bool                  `yaml:"exit_node"`
	Server   *ServerConfig         `yaml:"server"`
	Clients  []ClientEntry         `yaml:"clients"`
	Peers    map[string]PeerConfig `yaml:"peers"`
	SOCKS5   *SOCKS5Config         `yaml:"socks5"`
}

type App struct {
	cfg  Config
	node *relay.Node
}

func New(cfgPath string) (*App, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Name == "" {
		return nil, fmt.Errorf("name required")
	}
	return &App{
		cfg:  cfg,
		node: relay.NewNode(cfg.Name, cfg.ExitNode),
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	log.Printf("[%s] starting node (exit=%v)", a.cfg.Name, a.cfg.ExitNode)

	// Apply nested discovery settings
	for peerName, pc := range a.cfg.Peers {
		if pc.Nested {
			a.node.SetNestedDiscovery(peerName, true)
			log.Printf("[%s] nested discovery enabled for %q", a.cfg.Name, peerName)
		}
	}

	// Start hy2 server if configured
	if a.cfg.Server != nil {
		if err := a.startServer(ctx); err != nil {
			return err
		}
	}

	// Start SOCKS5 if configured
	if a.cfg.SOCKS5 != nil {
		go a.serveSOCKS5(ctx)
	}

	// Start hy2 clients
	for _, cl := range a.cfg.Clients {
		cl := cl
		go a.connectLoop(ctx, cl)
	}

	<-ctx.Done()
	return ctx.Err()
}

func (a *App) startServer(ctx context.Context) error {
	cert, err := loadCert(a.cfg.Server.TLSCert, a.cfg.Server.TLSKey)
	if err != nil {
		return err
	}

	conn, err := net.ListenPacket("udp", a.cfg.Server.Listen)
	if err != nil {
		return err
	}

	hyServer, err := hyserver.NewServer(&hyserver.Config{
		Conn: conn,
		TLSConfig: hyserver.TLSConfig{
			Certificates: []tls.Certificate{cert},
		},
		QUICConfig: hyserver.QUICConfig{
			InitialStreamReceiveWindow:     67108864,  // 64MB
			MaxStreamReceiveWindow:         67108864,
			InitialConnectionReceiveWindow: 134217728, // 128MB
			MaxConnectionReceiveWindow:     134217728,
			MaxIncomingStreams:              4096,
		},
		Authenticator: &simpleAuth{password: a.cfg.Server.Password},
		Outbound:      &nodeOutbound{node: a.node, ctx: ctx},
	})
	if err != nil {
		conn.Close()
		return err
	}

	go func() { <-ctx.Done(); hyServer.Close(); conn.Close() }()
	go hyServer.Serve()
	log.Printf("[%s] hy2 server on %s", a.cfg.Name, a.cfg.Server.Listen)
	return nil
}

func (a *App) connectLoop(ctx context.Context, cl ClientEntry) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		log.Printf("[%s] connecting to %s (%s)", a.cfg.Name, cl.Name, cl.Addr)
		if err := a.connect(ctx, cl); err != nil {
			log.Printf("[%s] %s: %v", a.cfg.Name, cl.Name, err)
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
	bw := uint64(125000000) // 1Gbps default
	if cl.Bandwidth > 0 {
		bw = uint64(cl.Bandwidth)
	}
	c, _, err := hyclient.NewClient(&hyclient.Config{
		ServerAddr: addr,
		Auth:       cl.Password,
		TLSConfig: hyclient.TLSConfig{
			InsecureSkipVerify: true,
			ServerName:         "hy2scale",
		},
		QUICConfig: hyclient.QUICConfig{
			InitialStreamReceiveWindow:     67108864, // 64MB
			MaxStreamReceiveWindow:         67108864,
			InitialConnectionReceiveWindow: 134217728, // 128MB
			MaxConnectionReceiveWindow:     134217728,
		},
		BandwidthConfig: hyclient.BandwidthConfig{
			MaxTx: bw,
			MaxRx: bw,
		},
	})
	if err != nil {
		return err
	}
	defer c.Close()
	log.Printf("[%s] connected to %s", a.cfg.Name, cl.Name)
	return a.node.AttachTo(ctx, cl.Name, c)
}

func (a *App) serveSOCKS5(ctx context.Context) {
	ln, err := net.Listen("tcp", a.cfg.SOCKS5.Listen)
	if err != nil {
		log.Printf("[%s] socks5: %v", a.cfg.Name, err)
		return
	}
	defer ln.Close()
	log.Printf("[%s] SOCKS5 on %s (exit_via=%q)", a.cfg.Name, a.cfg.SOCKS5.Listen, a.cfg.SOCKS5.ExitVia)
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
	exitVia := a.cfg.SOCKS5.ExitVia

	if exitVia == "" {
		remote, err = net.DialTimeout("tcp", addr, 10*time.Second) // local exit
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

func (a *App) Shutdown() error { return nil }

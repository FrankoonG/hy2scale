package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// IKEv2Config holds IKEv2/IPsec VPN configuration.
type IKEv2Config struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	Mode        string `yaml:"mode" json:"mode"`                   // "mschapv2" or "psk"
	Pool        string `yaml:"pool" json:"pool"`                   // e.g. "10.10.10.1/24"
	CertID      string `yaml:"cert_id" json:"cert_id"`             // TLS cert ID (for mschapv2)
	PSK         string `yaml:"psk" json:"psk"`                     // pre-shared key (for psk mode)
	LocalID     string `yaml:"local_id" json:"local_id"`           // server identity (leftid), default = node ID
	RemoteID    string `yaml:"remote_id" json:"remote_id"`         // client identity (rightid), default = %any
	PSKUserMode bool   `yaml:"psk_user_mode" json:"psk_user_mode"` // PSK: require user auth
	DefaultExit string `yaml:"default_exit" json:"default_exit"`   // exit_via when user mode off
	DNS         string `yaml:"dns" json:"dns"`                     // DNS servers, default "8.8.8.8 8.8.4.4"
	ProxyPort   int    `yaml:"proxy_port" json:"proxy_port"`       // transparent proxy port, default 12350
	MTU         int    `yaml:"mtu" json:"mtu"`                     // tunnel MTU, default 1400
}

func (c *IKEv2Config) proxyPort() int {
	if c.ProxyPort <= 0 {
		return 12350
	}
	return c.ProxyPort
}

// getDNS returns DNS servers from the global config, space-separated for strongswan.
func (a *App) getDNS() string {
	dns := a.store.Get().DNS
	if dns == "" {
		dns = "8.8.8.8,1.1.1.1"
	}
	return strings.ReplaceAll(strings.ReplaceAll(dns, " ", ","), ",", ",")
}

// ikev2Session tracks IKEv2 virtual IP to username mappings.
var ikev2Sessions = &vpnSession{ipToUser: make(map[string]string)}

// vpnSession is a shared IP→username tracker for VPN sessions (L2TP and IKEv2).
type vpnSession struct {
	mu       sync.RWMutex
	ipToUser map[string]string
}

func (s *vpnSession) Register(ip, username string) {
	s.mu.Lock()
	s.ipToUser[ip] = username
	s.mu.Unlock()
	log.Printf("[vpn] session: %s → %s", ip, username)
}

func (s *vpnSession) Unregister(ip string) {
	s.mu.Lock()
	if user, ok := s.ipToUser[ip]; ok {
		delete(s.ipToUser, ip)
		log.Printf("[vpn] session ended: %s (was %s)", ip, user)
	}
	s.mu.Unlock()
}

func (s *vpnSession) Lookup(ip string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.ipToUser[ip]
	return u, ok
}

// CheckHostNetwork returns true if the container appears to be running with --network host.
func CheckHostNetwork() bool {
	// In host network mode, /sys/class/net/eth0 typically doesn't exist as a veth,
	// or we can check if we can see the host's real interfaces.
	// Simplest: check if we can bind to port 0 on a non-local IP (host network allows it).
	// Alternative: check if docker0 bridge is visible from inside the container.
	// In host network mode, container sees many host interfaces (docker0, br-xxx, etc.)
	// In bridge mode, container only sees eth0 + lo
	entries, _ := os.ReadDir("/sys/class/net")
	return len(entries) > 3
}

// StartIKEv2 configures and starts IKEv2/IPsec VPN.
func (a *App) StartIKEv2(cfg IKEv2Config) error {
	if !cfg.Enabled {
		return nil
	}
	if !CheckL2TPCapability() {
		log.Printf("[ikev2] disabled: container lacks NET_ADMIN")
		return fmt.Errorf("insufficient privileges")
	}

	gateway, subnet, ipRange, err := parsePool(cfg.Pool)
	if err != nil {
		return fmt.Errorf("ikev2: invalid pool %q: %w", cfg.Pool, err)
	}

	// Add gateway IP to loopback for transparent proxy binding
	run("ip", "addr", "add", fmt.Sprintf("%s/32", gateway), "dev", "lo")

	proxyPort := cfg.proxyPort()
	hooksPort := proxyPort + 1

	// Generate updown script for IKEv2 session tracking (strongswan format)
	updownScript := fmt.Sprintf(`#!/bin/sh
# strongswan updown script for IKEv2 session tracking
# PLUTO_PEER_CLIENT = virtual IP assigned to client (e.g. 10.10.10.2/32)
# PLUTO_PEER_ID = peer identity (username for EAP, or IP/@id for PSK)
REMOTE_IP="${PLUTO_PEER_CLIENT%%%%/*}"
case "$PLUTO_VERB" in
  up-client|up-client-v6)
    wget -qO- "http://%s:%d/ikev2/up?ip=${REMOTE_IP}&user=${PLUTO_PEER_ID}" 2>/dev/null
    ;;
  down-client|down-client-v6)
    wget -qO- "http://%s:%d/ikev2/down?ip=${REMOTE_IP}" 2>/dev/null
    ;;
esac
`, gateway, hooksPort, gateway, hooksPort)

	os.MkdirAll("/etc/ipsec.d", 0755)
	os.WriteFile("/etc/ipsec.d/ikev2-updown.sh", []byte(updownScript), 0755)

	// Resolve Local/Remote IDs
	localID := cfg.LocalID
	if localID == "" {
		localID = a.store.Get().NodeID
	}
	remoteID := cfg.RemoteID
	if remoteID == "" {
		remoteID = "%any"
	}

	// Generate IKEv2 connection config (strongswan format)
	var connConf string
	dns := a.getDNS()
	switch cfg.Mode {
	case "psk":
		connConf = fmt.Sprintf(`
conn ikev2-psk
    keyexchange=ikev2
    auto=add
    type=tunnel
    left=%%any
    leftid=%s
    leftsubnet=0.0.0.0/0
    right=%%any
    rightid=%s
    authby=secret
    rightsourceip=%s
    rightdns=%s
    leftupdown=/etc/ipsec.d/ikev2-updown.sh
    fragmentation=yes
    rekey=no
    dpdaction=clear
    dpddelay=300s
    ike=aes256-sha256-modp2048,aes128-sha256-modp2048!
    esp=aes256-sha256,aes128-sha256!
`, localID, remoteID, ipRange, dns)

	case "mschapv2":
		// Copy cert/key to strongswan dirs
		certPath := a.tls.CertPath(cfg.CertID)
		keyPath := a.tls.KeyPath(cfg.CertID)
		if _, err := os.Stat(certPath); err != nil {
			return fmt.Errorf("ikev2: cert %s not found", cfg.CertID)
		}
		certData, _ := os.ReadFile(certPath)
		keyData, _ := os.ReadFile(keyPath)
		os.WriteFile("/etc/ipsec.d/certs/ikev2-server.cert.pem", certData, 0644)
		os.WriteFile("/etc/ipsec.d/private/ikev2-server.key.pem", keyData, 0600)

		connConf = fmt.Sprintf(`
conn ikev2-mschapv2
    keyexchange=ikev2
    auto=add
    type=tunnel
    left=%%any
    leftcert=ikev2-server.cert.pem
    leftsendcert=always
    leftsubnet=0.0.0.0/0
    right=%%any
    rightauth=eap-mschapv2
    eap_identity=%%identity
    rightsourceip=%s
    rightdns=%s
    leftupdown=/etc/ipsec.d/ikev2-updown.sh
    fragmentation=yes
    rekey=no
    dpdaction=clear
    dpddelay=300s
    ike=aes256-sha256-modp2048,aes128-sha256-modp2048!
    esp=aes256-sha256,aes128-sha256!
`, ipRange, dns)

		// Generate EAP secrets
		a.updateEAPSecrets()

	default:
		return fmt.Errorf("ikev2: unknown mode %q", cfg.Mode)
	}

	// Append IKEv2 conn to ipsec.conf (L2TP may have written the base config)
	appendToIPSecConf(connConf)

	// Update secrets
	if cfg.Mode == "psk" && cfg.PSK != "" {
		appendPSKSecret(cfg.PSK)
	}
	if cfg.Mode == "mschapv2" {
		// Detect key type and add to ipsec.secrets
		kd, _ := os.ReadFile("/etc/ipsec.d/private/ikev2-server.key.pem")
		keyType := "ECDSA"
		if strings.Contains(string(kd), "RSA PRIVATE KEY") {
			keyType = "RSA"
		}
		appendToIPSecSecrets(fmt.Sprintf(": %s ikev2-server.key.pem\n", keyType))
		a.updateEAPSecrets()
	}

	// Setup iptables (same dual-stack approach as L2TP)
	os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
	portStr := fmt.Sprintf("%d", proxyPort)

	// iptables-legacy: DNAT + FORWARD + MASQUERADE
	iptRun("iptables-legacy", "-t", "nat", "-I", "PREROUTING",
		"-s", subnet, "-p", "tcp",
		"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%s", gateway, portStr))
	iptRun("iptables-legacy", "-t", "nat", "-A", "POSTROUTING",
		"-s", subnet, "-o", "eth0", "-j", "MASQUERADE")
	iptRun("iptables-legacy", "-I", "FORWARD", "-s", subnet, "-o", "eth0", "-j", "ACCEPT")
	iptRun("iptables-legacy", "-I", "FORWARD", "-d", subnet,
		"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")

	// Restrict proxy port access
	iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", portStr,
		"-s", subnet, "-j", "ACCEPT")
	iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "DROP")
	hooksPortStr := fmt.Sprintf("%d", hooksPort)
	iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", hooksPortStr,
		"-i", "lo", "-j", "ACCEPT")
	iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", hooksPortStr, "-j", "DROP")

	// nf_tables (Docker compat)
	iptRun("iptables", "-I", "DOCKER-USER", "-s", subnet, "-j", "ACCEPT")
	iptRun("iptables", "-I", "DOCKER-USER", "-d", subnet, "-j", "ACCEPT")
	iptRun("iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", subnet, "-j", "MASQUERADE")

	// Start transparent proxy
	go a.runIKEv2Proxy(gateway, proxyPort, hooksPort, cfg)

	// Start strongswan or reload if already running
	ensureStrongswanRunning()
	// Reload config and secrets to pick up new IKEv2 connection
	time.Sleep(time.Second)
	run("ipsec", "update")
	run("ipsec", "rereadsecrets")

	log.Printf("[ikev2] server mode=%s pool=%s", cfg.Mode, cfg.Pool)
	return nil
}


// updateEAPSecrets writes EAP-MSCHAPv2 user entries to ipsec.secrets.
func (a *App) updateEAPSecrets() {
	data, _ := os.ReadFile("/etc/ipsec.secrets")
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		// Keep non-EAP lines (PSK lines from L2TP, etc.)
		if !strings.Contains(line, " : EAP ") && line != "# EAP users" {
			lines = append(lines, line)
		}
	}

	cfg := a.store.Get()
	lines = append(lines, "# EAP users")
	for _, u := range cfg.Users {
		if u.Enabled {
			lines = append(lines, fmt.Sprintf(`%s : EAP "%s"`, u.Username, u.Password))
		}
	}
	os.WriteFile("/etc/ipsec.secrets", []byte(strings.Join(lines, "\n")+"\n"), 0600)
	exec.Command("ipsec", "auto", "--rereadsecrets").Run()
}

// appendToIPSecConf appends a conn block to /etc/ipsec.conf.
func appendToIPSecConf(connBlock string) {
	f, err := os.OpenFile("/etc/ipsec.conf", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[ikev2] failed to append ipsec.conf: %v", err)
		return
	}
	defer f.Close()
	f.WriteString(connBlock)
}

// appendToIPSecSecrets appends a line to ipsec.secrets if not already present.
func appendToIPSecSecrets(line string) {
	data, _ := os.ReadFile("/etc/ipsec.secrets")
	if strings.Contains(string(data), strings.TrimSpace(line)) {
		return
	}
	f, _ := os.OpenFile("/etc/ipsec.secrets", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if f != nil {
		f.WriteString(line)
		f.Close()
	}
}

// appendPSKSecret appends a PSK entry to ipsec.secrets if not already present.
func appendPSKSecret(psk string) {
	data, _ := os.ReadFile("/etc/ipsec.secrets")
	entry := fmt.Sprintf(`%%any %%any : PSK "%s"`, psk)
	if strings.Contains(string(data), entry) {
		return
	}
	f, _ := os.OpenFile("/etc/ipsec.secrets", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if f != nil {
		f.WriteString(entry + "\n")
		f.Close()
	}
}

// ensureStrongswanRunning starts strongswan charon if not already running.
func ensureStrongswanRunning() {
	// Check if charon is already running
	if exec.Command("ipsec", "status").Run() == nil {
		return
	}
	// Clean stale xfrm state/policy from previous runs (kernel persists them across restarts)
	exec.Command("ip", "xfrm", "state", "flush").Run()
	exec.Command("ip", "xfrm", "policy", "flush").Run()
	go func() {
		cmd := exec.Command("ipsec", "start", "--nofork")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("[ipsec] strongswan exited: %v", err)
		}
	}()
	// Wait for charon to be ready
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		if exec.Command("ipsec", "status").Run() == nil {
			return
		}
	}
	log.Printf("[ipsec] warning: strongswan may not be ready")
}

// runIKEv2Proxy runs the transparent proxy and hooks server for IKEv2.
func (a *App) runIKEv2Proxy(gatewayIP string, proxyPort, hooksPort int, cfg IKEv2Config) {
	addr := fmt.Sprintf("%s:%d", gatewayIP, proxyPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[ikev2] transparent proxy listen error: %v", err)
		return
	}
	defer ln.Close()
	log.Printf("[ikev2] transparent proxy on %s", addr)

	go a.serveIKEv2Hooks(gatewayIP, hooksPort, cfg)
	go func() { <-a.appCtx.Done(); ln.Close() }()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go a.handleIKEv2Transparent(conn, cfg)
	}
}

// serveIKEv2Hooks handles IKEv2 updown notifications.
func (a *App) serveIKEv2Hooks(gatewayIP string, port int, cfg IKEv2Config) {
	httpLn, err := net.Listen("tcp", fmt.Sprintf("%s:%d", gatewayIP, port))
	if err != nil {
		log.Printf("[ikev2] hooks listener error: %v", err)
		return
	}
	defer httpLn.Close()
	go func() { <-a.appCtx.Done(); httpLn.Close() }()

	for {
		conn, err := httpLn.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 1024)
			n, _ := c.Read(buf)
			req := string(buf[:n])

			if strings.Contains(req, "/ikev2/up?") {
				if idx := strings.Index(req, "/ikev2/up?"); idx >= 0 {
					q := req[idx+10:]
					if sp := strings.IndexByte(q, ' '); sp > 0 {
						q = q[:sp]
					}
					params := parseQuery(q)
					if ip, ok := params["ip"]; ok {
						user := params["user"]
						// For PSK without user mode, use a placeholder
						if cfg.Mode == "psk" && !cfg.PSKUserMode {
							user = "__psk__"
						}
						if user != "" {
							ikev2Sessions.Register(ip, user)
						}
					}
				}
			} else if strings.Contains(req, "/ikev2/down?") {
				if idx := strings.Index(req, "/ikev2/down?"); idx >= 0 {
					q := req[idx+12:]
					if sp := strings.IndexByte(q, ' '); sp > 0 {
						q = q[:sp]
					}
					params := parseQuery(q)
					if ip, ok := params["ip"]; ok {
						ikev2Sessions.Unregister(ip)
					}
				}
			}
			c.Write([]byte("HTTP/1.0 200 OK\r\n\r\nok"))
		}(conn)
	}
}

// handleIKEv2Transparent handles a redirected TCP connection from an IKEv2 client.
func (a *App) handleIKEv2Transparent(conn net.Conn, cfg IKEv2Config) {
	defer conn.Close()

	origDst, err := getOriginalDst(conn)
	if err != nil {
		log.Printf("[ikev2] getOriginalDst failed for %s: %v", conn.RemoteAddr(), err)
		return
	}
	log.Printf("[ikev2] transparent: %s → %s", conn.RemoteAddr(), origDst)

	srcIP := conn.RemoteAddr().(*net.TCPAddr).IP.String()
	username, ok := ikev2Sessions.Lookup(srcIP)

	// Determine exit_via
	exitVia := ""
	if ok && username != "__psk__" {
		// User mode: look up user's exit_via
		user, err := a.LookupUser(username, "")
		if err != nil {
			c := a.store.Get()
			for _, u := range c.Users {
				if u.Username == username && u.Enabled {
					user = &u
					break
				}
			}
		}
		if user != nil {
			exitVia = user.ExitVia
		}
	} else if cfg.Mode == "psk" && !cfg.PSKUserMode {
		// PSK no-user mode: use default exit
		exitVia = cfg.DefaultExit
	} else if !ok {
		// Unknown session: direct exit
		exitVia = ""
	}

	var remote net.Conn
	if exitVia == "" {
		log.Printf("[ikev2] direct dial %s", origDst)
		remote, err = net.DialTimeout("tcp", origDst, 10*time.Second)
	} else {
		log.Printf("[ikev2] dial via %s to %s", exitVia, origDst)
		parts := splitPath(exitVia)
		if len(parts) == 1 {
			remote, err = a.node.DialTCP(context.Background(), parts[0], origDst)
		} else {
			remote, err = a.node.DialVia(context.Background(), parts, origDst)
		}
	}
	if err != nil {
		log.Printf("[ikev2] dial error: %v", err)
		return
	}
	defer remote.Close()
	log.Printf("[ikev2] connected to %s for user %s", origDst, username)

	var up, down int64
	done := make(chan struct{})
	go func() {
		n, _ := io.Copy(remote, conn)
		atomic.AddInt64(&up, n)
		remote.Close()
		done <- struct{}{}
	}()
	n, _ := io.Copy(conn, remote)
	atomic.AddInt64(&down, n)
	<-done
	if username != "" && username != "__psk__" {
		a.RecordTraffic(username, atomic.LoadInt64(&up)+atomic.LoadInt64(&down))
	}
}

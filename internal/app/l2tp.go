package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// L2TPConfig holds L2TP/IPsec server configuration.
type L2TPConfig struct {
	Listen    string `yaml:"listen" json:"listen"`       // UDP port, e.g. "1701"
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Pool      string `yaml:"pool" json:"pool"`           // e.g. "192.168.25.1/24"
	PSK       string `yaml:"psk" json:"psk"`             // IPsec pre-shared key
}

const (
	tproxyAddr = "10.255.255.1"
	tproxyPort = 12345
)

// pppSession maps PPP interface IP to username.
type pppSession struct {
	mu       sync.RWMutex
	ipToUser map[string]string // "192.168.25.2" → "username"
}

var pppSessions = &pppSession{ipToUser: make(map[string]string)}

func (s *pppSession) Register(ip, username string) {
	s.mu.Lock()
	s.ipToUser[ip] = username
	s.mu.Unlock()
	log.Printf("[l2tp] ppp session: %s → %s", ip, username)
}

func (s *pppSession) Unregister(ip string) {
	s.mu.Lock()
	delete(s.ipToUser, ip)
	s.mu.Unlock()
}

func (s *pppSession) Lookup(ip string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.ipToUser[ip]
	return u, ok
}

// CheckL2TPCapability tests if the container has NET_ADMIN and /dev/ppp.
func CheckL2TPCapability() bool {
	// Test 1: can we create a dummy interface? (requires NET_ADMIN)
	err := exec.Command("ip", "link", "add", "hy2cap_test", "type", "dummy").Run()
	if err != nil {
		return false
	}
	exec.Command("ip", "link", "del", "hy2cap_test").Run()

	// Test 2: does /dev/ppp exist?
	if _, err := os.Stat("/dev/ppp"); err != nil {
		return false
	}
	return true
}

// StartL2TP sets up xl2tpd, strongswan, iptables, and the transparent proxy.
func (a *App) StartL2TP(cfg L2TPConfig) error {
	if !cfg.Enabled || cfg.Pool == "" {
		return nil
	}
	if !CheckL2TPCapability() {
		log.Printf("[l2tp] disabled: container lacks NET_ADMIN or /dev/ppp")
		return fmt.Errorf("insufficient privileges")
	}

	// Parse pool: "192.168.25.1/24" → gateway=192.168.25.1, range=192.168.25.2-254
	gateway, subnet, ipRange, err := parsePool(cfg.Pool)
	if err != nil {
		return fmt.Errorf("l2tp: invalid pool %q: %w", cfg.Pool, err)
	}

	// 1. Setup dummy interface for transparent proxy
	run("ip", "link", "add", "hy2tp", "type", "dummy")
	run("ip", "addr", "add", fmt.Sprintf("%s/32", tproxyAddr), "dev", "hy2tp")
	run("ip", "link", "set", "hy2tp", "up")

	// 2. Generate xl2tpd config
	xl2tpConf := fmt.Sprintf(`[global]
port = %s

[lns default]
ip range = %s
local ip = %s
require chap = yes
refuse pap = yes
require authentication = yes
pppoptfile = /etc/ppp/options.xl2tpd
length bit = yes
`, cfg.Listen, ipRange, gateway)

	os.MkdirAll("/etc/xl2tpd", 0755)
	os.WriteFile("/etc/xl2tpd/xl2tpd.conf", []byte(xl2tpConf), 0644)

	// 3. Generate PPP options
	pppOpts := fmt.Sprintf(`ms-dns 8.8.8.8
ms-dns 8.8.4.4
auth
mtu 1400
mru 1400
nodefaultroute
proxyarp
connect-delay 5000
ip-up-script /etc/ppp/ip-up.local
ip-down-script /etc/ppp/ip-down.local
`)
	os.MkdirAll("/etc/ppp", 0755)
	os.WriteFile("/etc/ppp/options.xl2tpd", []byte(pppOpts), 0644)

	// 4. Generate chap-secrets from user management
	a.updateChapSecrets()

	// 5. Generate ip-up/ip-down scripts that call our API
	ipUpScript := `#!/bin/sh
# $1=interface $2=tty $3=speed $4=local_ip $5=remote_ip $6=ipparam
wget -qO- "http://` + tproxyAddr + fmt.Sprintf(`:%d/ppp/up?ip=$5&user=$PEERNAME" 2>/dev/null
`, tproxyPort)

	ipDownScript := `#!/bin/sh
wget -qO- "http://` + tproxyAddr + fmt.Sprintf(`:%d/ppp/down?ip=$5" 2>/dev/null
`, tproxyPort)

	os.WriteFile("/etc/ppp/ip-up.local", []byte(ipUpScript), 0755)
	os.WriteFile("/etc/ppp/ip-down.local", []byte(ipDownScript), 0755)

	// 6. Generate strongswan/ipsec config
	ipsecConf := `config setup
    charondebug="ike 0, knl 0, cfg 0"

conn l2tp
    type=transport
    keyexchange=ikev1
    authby=secret
    left=%defaultroute
    leftprotoport=17/1701
    right=%any
    rightprotoport=17/%any
    auto=add
`
	ipsecSecrets := fmt.Sprintf(`: PSK "%s"
`, cfg.PSK)

	os.MkdirAll("/etc/ipsec.d", 0755)
	os.WriteFile("/etc/ipsec.conf", []byte(ipsecConf), 0644)
	os.WriteFile("/etc/ipsec.secrets", []byte(ipsecSecrets), 0600)

	// 7. Setup iptables: DNAT L2TP subnet TCP traffic to transparent proxy
	run("iptables", "-t", "nat", "-A", "PREROUTING",
		"-s", subnet, "-p", "tcp",
		"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", tproxyAddr, tproxyPort))
	// Enable forwarding
	os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
	// Masquerade for UDP (direct exit)
	run("iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", subnet, "-p", "udp", "-j", "MASQUERADE")

	// 8. Start transparent proxy
	go a.runTransparentProxy()

	// 9. Start ipsec
	go func() {
		cmd := exec.Command("ipsec", "start", "--nofork")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	}()

	// 10. Start xl2tpd
	go func() {
		time.Sleep(2 * time.Second) // wait for ipsec
		cmd := exec.Command("xl2tpd", "-D")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("[l2tp] xl2tpd exited: %v", err)
		}
	}()

	log.Printf("[l2tp] server on :%s pool=%s", cfg.Listen, cfg.Pool)
	return nil
}

// updateChapSecrets generates /etc/ppp/chap-secrets from user config.
func (a *App) updateChapSecrets() {
	cfg := a.store.Get()
	var lines []string
	lines = append(lines, "# Autogenerated by hy2scale")
	for _, u := range cfg.Users {
		if u.Enabled {
			// Format: client server secret IP
			lines = append(lines, fmt.Sprintf("%s\t*\t%s\t*", u.Username, u.Password))
		}
	}
	os.WriteFile("/etc/ppp/chap-secrets", []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

// runTransparentProxy listens on the dummy interface for redirected connections.
func (a *App) runTransparentProxy() {
	addr := fmt.Sprintf("%s:%d", tproxyAddr, tproxyPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[l2tp] transparent proxy listen error: %v", err)
		return
	}
	defer ln.Close()
	log.Printf("[l2tp] transparent proxy on %s", addr)

	// Also serve PPP ip-up/ip-down hooks on HTTP
	go a.servePPPHooks(addr)

	go func() { <-a.appCtx.Done(); ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go a.handleTransparent(conn)
	}
}

// servePPPHooks handles ip-up/ip-down notifications from pppd.
func (a *App) servePPPHooks(addr string) {
	mux := &net.ListenConfig{}
	_ = mux
	// Simple HTTP server on the same interface for PPP hooks
	httpLn, err := net.Listen("tcp", fmt.Sprintf("%s:%d", tproxyAddr, tproxyPort+1))
	if err != nil {
		log.Printf("[l2tp] ppp hook listener error: %v", err)
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
			if strings.Contains(req, "/ppp/up?") {
				// Parse ip and user from query
				if idx := strings.Index(req, "/ppp/up?"); idx >= 0 {
					q := req[idx+8:]
					if sp := strings.IndexByte(q, ' '); sp > 0 {
						q = q[:sp]
					}
					params := parseQuery(q)
					if ip, ok := params["ip"]; ok {
						if user, ok := params["user"]; ok {
							pppSessions.Register(ip, user)
						}
					}
				}
			} else if strings.Contains(req, "/ppp/down?") {
				if idx := strings.Index(req, "/ppp/down?"); idx >= 0 {
					q := req[idx+10:]
					if sp := strings.IndexByte(q, ' '); sp > 0 {
						q = q[:sp]
					}
					params := parseQuery(q)
					if ip, ok := params["ip"]; ok {
						pppSessions.Unregister(ip)
					}
				}
			}
			c.Write([]byte("HTTP/1.0 200 OK\r\n\r\nok"))
		}(conn)
	}
}

// handleTransparent handles a redirected TCP connection from an L2TP user.
func (a *App) handleTransparent(conn net.Conn) {
	defer conn.Close()

	// Get original destination via SO_ORIGINAL_DST
	origDst, err := getOriginalDst(conn)
	if err != nil {
		return
	}

	// Get source IP to identify user
	srcIP := conn.RemoteAddr().(*net.TCPAddr).IP.String()
	username, ok := pppSessions.Lookup(srcIP)
	if !ok {
		// Unknown PPP user — direct exit
		remote, err := net.DialTimeout("tcp", origDst, 10*time.Second)
		if err != nil {
			return
		}
		defer remote.Close()
		go func() { io.Copy(remote, conn); remote.Close() }()
		io.Copy(conn, remote)
		return
	}

	// Look up user's exit_via
	user, err := a.LookupUser(username, "")
	if err != nil {
		// Auth already done by pppd — just find the user by username
		cfg := a.store.Get()
		for _, u := range cfg.Users {
			if u.Username == username && u.Enabled {
				user = &u
				break
			}
		}
	}

	exitVia := ""
	if user != nil {
		exitVia = user.ExitVia
	}

	var remote net.Conn
	if exitVia == "" {
		remote, err = net.DialTimeout("tcp", origDst, 10*time.Second)
	} else {
		parts := splitPath(exitVia)
		if len(parts) == 1 {
			remote, err = a.node.DialTCP(context.Background(), parts[0], origDst)
		} else {
			remote, err = a.node.DialVia(context.Background(), parts, origDst)
		}
	}
	if err != nil {
		return
	}
	defer remote.Close()

	// Traffic counting
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
	if username != "" {
		a.RecordTraffic(username, atomic.LoadInt64(&up)+atomic.LoadInt64(&down))
	}
}

// getOriginalDst retrieves the original destination from a DNAT'd connection.
func getOriginalDst(conn net.Conn) (string, error) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return "", fmt.Errorf("not a TCP connection")
	}
	file, err := tcpConn.File()
	if err != nil {
		return "", err
	}
	defer file.Close()

	// SO_ORIGINAL_DST = 80
	const SO_ORIGINAL_DST = 80
	var addr syscall.RawSockaddrInet4
	addrLen := uint32(unsafe.Sizeof(addr))
	_, _, errno := syscall.Syscall6(
		syscall.SYS_GETSOCKOPT,
		file.Fd(),
		syscall.SOL_IP,
		SO_ORIGINAL_DST,
		uintptr(unsafe.Pointer(&addr)),
		uintptr(unsafe.Pointer(&addrLen)),
		0,
	)
	if errno != 0 {
		return "", fmt.Errorf("getsockopt SO_ORIGINAL_DST: %v", errno)
	}

	port := binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&addr.Port))[:])
	ip := net.IPv4(addr.Addr[0], addr.Addr[1], addr.Addr[2], addr.Addr[3])
	return fmt.Sprintf("%s:%d", ip, port), nil
}

func parsePool(pool string) (gateway, subnet, ipRange string, err error) {
	parts := strings.Split(pool, "/")
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("expected CIDR format")
	}
	gateway = parts[0]
	ip := net.ParseIP(gateway)
	if ip == nil {
		return "", "", "", fmt.Errorf("invalid IP")
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", "", "", fmt.Errorf("not IPv4")
	}
	subnet = pool
	// Range: .2 to .254
	start := net.IPv4(ip4[0], ip4[1], ip4[2], ip4[3]+1)
	end := net.IPv4(ip4[0], ip4[1], ip4[2], 254)
	ipRange = fmt.Sprintf("%s-%s", start, end)
	return
}

func parseQuery(q string) map[string]string {
	result := make(map[string]string)
	for _, kv := range strings.Split(q, "&") {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

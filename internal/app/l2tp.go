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
	Listen     string `yaml:"listen" json:"listen"`            // UDP port, e.g. "1701"
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	Pool       string `yaml:"pool" json:"pool"`                // e.g. "192.168.25.1/24"
	PSK        string `yaml:"psk" json:"psk"`                  // IPsec pre-shared key
	ProxyPort  int    `yaml:"proxy_port" json:"proxy_port"`    // transparent proxy port, default 12345
	MTU        int    `yaml:"mtu" json:"mtu"`                  // PPP MTU, default 1280
}

func (c *L2TPConfig) proxyPort() int {
	if c.ProxyPort <= 0 {
		return 12345
	}
	return c.ProxyPort
}

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
	if user, ok := s.ipToUser[ip]; ok {
		delete(s.ipToUser, ip)
		log.Printf("[l2tp] ppp session ended: %s (was %s)", ip, user)
	}
	s.mu.Unlock()
}

// iptRun runs an iptables command, checking for duplicates first.
// For -I (insert) and -A (append), it checks with -C first.
func iptRun(prog string, args ...string) {
	// Try -C (check) first to detect duplicates
	for i, a := range args {
		if a == "-I" || a == "-A" {
			checkArgs := make([]string, len(args))
			copy(checkArgs, args)
			checkArgs[i] = "-C"
			// Remove position argument after -I (e.g. "-I DOCKER-USER 1" → "-C DOCKER-USER")
			if a == "-I" && i+2 < len(checkArgs) {
				if _, err := fmt.Sscanf(checkArgs[i+2], "%d", new(int)); err == nil {
					checkArgs = append(checkArgs[:i+2], checkArgs[i+3:]...)
				}
			}
			if exec.Command(prog, checkArgs...).Run() == nil {
				return // rule already exists
			}
			break
		}
	}
	run(prog, args...)
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

	// Test 2: does /dev/ppp exist? Try to create it if missing.
	if _, err := os.Stat("/dev/ppp"); err != nil {
		// Try mknod /dev/ppp c 108 0
		if exec.Command("mknod", "/dev/ppp", "c", "108", "0").Run() != nil {
			return false
		}
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

	// 1. Add gateway IP to loopback so transparent proxy can bind before ppp0 exists
	run("ip", "addr", "add", fmt.Sprintf("%s/32", gateway), "dev", "lo")

	// 2. Generate xl2tpd config
	xl2tpConf := fmt.Sprintf(`[global]
port = %s

[lns default]
ip range = %s
local ip = %s
require chap = yes
refuse pap = yes
require authentication = yes
name = l2tpd
pppoptfile = /etc/ppp/options.xl2tpd
length bit = yes
`, cfg.Listen, ipRange, gateway)

	os.MkdirAll("/etc/xl2tpd", 0755)
	os.WriteFile("/etc/xl2tpd/xl2tpd.conf", []byte(xl2tpConf), 0644)

	// 3. Generate PPP options
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = 1280
	}
	dns := a.store.Get().DNS
	if dns == "" {
		dns = "8.8.8.8,1.1.1.1"
	}
	dnsParts := strings.Split(strings.ReplaceAll(dns, " ", ","), ",")
	var dnsLines string
	for _, d := range dnsParts {
		d = strings.TrimSpace(d)
		if d != "" {
			dnsLines += "ms-dns " + d + "\n"
		}
	}
	pppOpts := fmt.Sprintf(`name l2tpd
+mschap-v2
ipcp-accept-local
ipcp-accept-remote
noccp
auth
mtu %d
mru %d
proxyarp
lcp-echo-failure 4
lcp-echo-interval 30
connect-delay 5000
%sip-up-script /etc/ppp/ip-up.local
ip-down-script /etc/ppp/ip-down.local
logfile /var/log/pppd.log
`, mtu, mtu, dnsLines)
	os.MkdirAll("/etc/ppp", 0755)
	os.WriteFile("/etc/ppp/options.xl2tpd", []byte(pppOpts), 0644)

	// 4. Generate chap-secrets from user management
	a.updateChapSecrets()

	// 5. Generate ip-up/ip-down scripts that call our API
	proxyPort := cfg.proxyPort()
	hooksPort := proxyPort + 1
	ipUpScript := `#!/bin/sh
# $1=interface $2=tty $3=speed $4=local_ip $5=remote_ip $6=ipparam
wget -qO- "http://` + gateway + fmt.Sprintf(`:%d/ppp/up?ip=$5&user=$PEERNAME" 2>/dev/null
`, hooksPort)

	ipDownScript := `#!/bin/sh
wget -qO- "http://` + gateway + fmt.Sprintf(`:%d/ppp/down?ip=$5" 2>/dev/null
`, hooksPort)

	os.WriteFile("/etc/ppp/ip-up.local", []byte(ipUpScript), 0755)
	os.WriteFile("/etc/ppp/ip-down.local", []byte(ipDownScript), 0755)

	// 6. Generate strongswan IPsec config (supports both IKEv1 for L2TP and IKEv2)
	ipsecConf := `config setup
    uniqueids=never

conn l2tp-psk
    keyexchange=ikev1
    type=transport
    authby=secret
    auto=add
    rekey=no
    forceencaps=yes
    left=%any
    leftid=%any
    leftprotoport=17/1701
    right=%any
    rightprotoport=17/%any
    ike=aes256-sha256-modp2048,aes128-sha1-modp1024,3des-sha1-modp1024!
    esp=aes256-sha256,aes128-sha1,3des-sha1!
    dpdaction=clear
    dpddelay=300s

`
	ipsecSecrets := fmt.Sprintf(`%%any %%any : PSK "%s"
`, cfg.PSK)

	// strongswan.conf: fix IKEv1 iOS compatibility
	strongswanConf := `charon {
    load_modular = yes
    max_ikev1_exchanges = 100
    plugins {
        include strongswan.d/charon/*.conf
    }
}
`

	os.MkdirAll("/etc/ipsec.d", 0755)
	os.WriteFile("/etc/strongswan.conf", []byte(strongswanConf), 0644)
	os.WriteFile("/etc/ipsec.conf", []byte(ipsecConf), 0644)
	os.WriteFile("/etc/ipsec.secrets", []byte(ipsecSecrets), 0600)

	// 7. Setup iptables for L2TP traffic forwarding
	// Kernel L2TP/IPsec uses iptables-legacy path, but Docker uses nf_tables (iptables).
	// Both must allow PPP traffic for forwarding to work.
	// Uses iptRun to avoid duplicate rules on container restart.
	os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
	portStr := fmt.Sprintf("%d", proxyPort)

	// iptables-legacy: DNAT TCP to transparent proxy, FORWARD, MASQUERADE
	iptRun("iptables-legacy", "-t", "nat", "-I", "PREROUTING",
		"-i", "ppp+", "-p", "tcp",
		"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%s", gateway, portStr))
	iptRun("iptables-legacy", "-t", "nat", "-A", "POSTROUTING",
		"-s", subnet, "-o", "eth0", "-j", "MASQUERADE")
	iptRun("iptables-legacy", "-I", "FORWARD", "-i", "ppp+", "-o", "eth0", "-j", "ACCEPT")
	iptRun("iptables-legacy", "-I", "FORWARD", "-i", "eth0", "-o", "ppp+",
		"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")

	// Restrict transparent proxy: only accept from PPP subnet, drop external access
	iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", portStr,
		"-s", subnet, "-j", "ACCEPT")
	iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "DROP")
	// Same for hooks port
	hooksPortStr := fmt.Sprintf("%d", hooksPort)
	iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", hooksPortStr,
		"-i", "lo", "-j", "ACCEPT")
	iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", hooksPortStr, "-j", "DROP")

	// iptables (nf_tables): allow PPP subnet in DOCKER-USER chain + MASQUERADE
	// Docker's nft FORWARD policy is DROP; DOCKER-USER runs first
	iptRun("iptables", "-I", "DOCKER-USER", "-s", subnet, "-j", "ACCEPT")
	iptRun("iptables", "-I", "DOCKER-USER", "-d", subnet, "-j", "ACCEPT")
	iptRun("iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", subnet, "-j", "MASQUERADE")

	// 8. Start transparent proxy
	go a.runTransparentProxy(gateway, proxyPort)

	// 9. Start strongswan (shared with IKEv2; auto=add connections load automatically)
	ensureStrongswanRunning()

	// 10. Start xl2tpd (userspace mode, no kernel L2TP)
	os.MkdirAll("/var/run/xl2tpd", 0755)
	go func() {
		time.Sleep(4 * time.Second) // wait for ipsec
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
			lines = append(lines, fmt.Sprintf("\"%s\"\tl2tpd\t\"%s\"\t*", u.Username, u.Password))
		}
	}
	os.WriteFile("/etc/ppp/chap-secrets", []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

// runTransparentProxy listens on the gateway IP for redirected connections.
func (a *App) runTransparentProxy(gatewayIP string, port int) {
	addr := fmt.Sprintf("%s:%d", gatewayIP, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[l2tp] transparent proxy listen error: %v", err)
		return
	}
	defer ln.Close()
	log.Printf("[l2tp] transparent proxy on %s", addr)

	// Also serve PPP ip-up/ip-down hooks on HTTP
	go a.servePPPHooks(gatewayIP, port+1)

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
func (a *App) servePPPHooks(gatewayIP string, port int) {
	// Simple HTTP server on the gateway IP for PPP hooks (only from localhost)
	httpLn, err := net.Listen("tcp", fmt.Sprintf("%s:%d", gatewayIP, port))
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

	// Get original destination via SO_ORIGINAL_DST (iptables-legacy DNAT)
	origDst, err := getOriginalDst(conn)
	if err != nil {
		log.Printf("[l2tp] getOriginalDst failed for %s: %v", conn.RemoteAddr(), err)
		return
	}
	log.Printf("[l2tp] transparent: %s → %s", conn.RemoteAddr(), origDst)

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
		log.Printf("[l2tp] direct dial %s", origDst)
		remote, err = net.DialTimeout("tcp", origDst, 10*time.Second)
	} else {
		log.Printf("[l2tp] dial via %s to %s", exitVia, origDst)
		remote, err = a.dialExit(context.Background(), exitVia, origDst)
	}
	if err != nil {
		log.Printf("[l2tp] dial error: %v", err)
		return
	}
	log.Printf("[l2tp] connected to %s for user %s", origDst, username)
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
// Uses SyscallConn to avoid File() which puts the conn into blocking mode.
func getOriginalDst(conn net.Conn) (string, error) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return "", fmt.Errorf("not a TCP connection")
	}
	rc, err := tcpConn.SyscallConn()
	if err != nil {
		return "", err
	}

	const SO_ORIGINAL_DST = 80
	var addr syscall.RawSockaddrInet4
	var getsockoptErr error
	err = rc.Control(func(fd uintptr) {
		addrLen := uint32(unsafe.Sizeof(addr))
		_, _, errno := syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			syscall.SOL_IP,
			SO_ORIGINAL_DST,
			uintptr(unsafe.Pointer(&addr)),
			uintptr(unsafe.Pointer(&addrLen)),
			0,
		)
		if errno != 0 {
			getsockoptErr = fmt.Errorf("getsockopt SO_ORIGINAL_DST: %v", errno)
		}
	})
	if err != nil {
		return "", err
	}
	if getsockoptErr != nil {
		return "", getsockoptErr
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

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
// iptCmd resolves the iptables command. If the container's iptables doesn't work
// but host root is mounted at /host, use chroot to run the host's iptables.
// This handles iKuai routers where container iptables is incompatible with the kernel.
// iptVariant detects the correct iptables command at startup.
// On standard Linux with nftables: "iptables" (maps to iptables-nft)
// On iKuai or legacy systems: "iptables-legacy"
var iptVariant = sync.OnceValue(func() string {
	// Check if iptables (nftables backend) works — preferred on modern systems
	if out, err := exec.Command("iptables", "-V").CombinedOutput(); err == nil {
		v := string(out)
		if strings.Contains(v, "nf_tables") {
			log.Printf("[iptables] using iptables (nf_tables backend)")
			return "iptables"
		}
	}
	// Fall back to iptables-legacy
	if _, err := exec.Command("iptables-legacy", "-L", "-n").CombinedOutput(); err == nil {
		log.Printf("[iptables] using iptables-legacy")
		return "iptables-legacy"
	}
	// Default
	log.Printf("[iptables] falling back to iptables")
	return "iptables"
})

var iptUseChroot = sync.OnceValue(func() bool {
	// Test if native iptables works
	variant := iptVariant()
	if out, err := exec.Command(variant, "-L", "-n").CombinedOutput(); err == nil {
		debugLog("[iptables] native %s works", variant)
		return false
	} else {
		debugLog("[iptables] native %s failed: %v: %s", variant, err, string(out))
	}
	// Check if host root is mounted and its iptables works
	if _, err := os.Stat("/host/usr/sbin/iptables"); err == nil {
		if out, err := exec.Command("chroot", "/host", "/usr/sbin/iptables", "-L", "-n").CombinedOutput(); err == nil {
			log.Printf("[iptables] using chroot /host for iptables (host kernel compat)")
			return true
		} else {
			debugLog("[iptables] chroot /host iptables failed: %v: %s", err, string(out))
		}
	} else {
		debugLog("[iptables] /host/usr/sbin/iptables not found (host root not mounted)")
	}
	return false
})

func iptExec(prog string, args ...string) *exec.Cmd {
	// Platform adapter: iKuai, OpenWrt, etc. may provide custom iptables binary
	if cmd := platformIPTExec(prog, args); cmd != nil {
		return cmd
	}
	if iptUseChroot() {
		hostProg := prog
		switch prog {
		case "iptables-legacy":
			hostProg = "/usr/sbin/iptables"
		case "iptables":
			hostProg = "/usr/sbin/iptables"
		}
		return exec.Command("chroot", append([]string{"/host", hostProg}, args...)...)
	}
	return exec.Command(prog, args...)
}

func iptRun(prog string, args ...string) {
	// Try -C (check) first to detect duplicates
	for i, a := range args {
		if a == "-I" || a == "-A" {
			checkArgs := make([]string, len(args))
			copy(checkArgs, args)
			checkArgs[i] = "-C"
			if a == "-I" && i+2 < len(checkArgs) {
				if _, err := fmt.Sscanf(checkArgs[i+2], "%d", new(int)); err == nil {
					checkArgs = append(checkArgs[:i+2], checkArgs[i+3:]...)
				}
			}
			if iptExec(prog, checkArgs...).Run() == nil {
				return // rule already exists
			}
			break
		}
	}
	cmd := iptExec(prog, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[iptables] %s %v: %s", prog, args, string(out))
	}
}

// testIptablesAvailable checks if iptables-legacy DNAT works (native or chroot).
func testIptablesAvailable() bool {
	cmd := iptExec(iptVariant(), "-t", "nat", "-L", "-n")
	if out, err := cmd.CombinedOutput(); err != nil {
		debugLog("[iptables] NAT table test failed: %v: %s", err, string(out))
		return false
	}
	debugLog("[iptables] NAT table test passed")
	return true
}

// cachedIptablesAvail caches iptables detection at startup.
// If standard iptables fails, tries platform-specific fix before falling back to gvisor.
var cachedIptablesAvail = sync.OnceValue(func() bool {
	if testIptablesAvailable() {
		return true
	}
	// Standard iptables failed. Try platform-specific compatibility fix.
	initPlatformAdapter()
	if platformFixIPTables() {
		// Re-test with the platform-provided binary
		if testIptablesAvailable() {
			return true
		}
		log.Printf("[iptables] platform fix applied but iptables still failing")
	}
	return false
})

// IsCompatMode returns true when the node has NET_ADMIN but no working iptables.
// This is now a last-resort fallback — iKuai with bundled host iptables runs normal mode.
func IsCompatMode() bool {
	capOK, _ := CheckCapability()
	return capOK && !cachedIptablesAvail()
}

// DetectRuntimeMode runs early detection and logs the result.
func DetectRuntimeMode() {
	capOK, _ := CheckCapability()
	hostNet := CheckHostNetwork()
	iptOK := cachedIptablesAvail()
	platform := DetectPlatform()

	if platform != PlatformLinux {
		log.Printf("[runtime] platform: %s", platform)
	}

	if !capOK {
		log.Printf("[runtime] mode: limited (no NET_ADMIN)")
	} else if hostNet && iptOK {
		log.Printf("[runtime] mode: normal (host network, iptables OK)")
	} else if !hostNet && iptOK {
		log.Printf("[runtime] mode: bridge (iptables OK, no host network)")
	} else if !iptOK {
		log.Printf("[runtime] mode: compat (NET_ADMIN OK, iptables unavailable)")
	}
}

func (s *pppSession) Lookup(ip string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.ipToUser[ip]
	return u, ok
}

// CheckCapability tests if the runtime has NET_ADMIN.
// Returns (ok, reason) where reason explains what failed.
var cachedCapability = sync.OnceValues(func() (bool, string) {
	// NET_ADMIN check — try bridge (kernel built-in), fall back to iptables
	if exec.Command("ip", "link", "add", "hy2cap_test", "type", "bridge").Run() == nil {
		exec.Command("ip", "link", "del", "hy2cap_test").Run()
		debugLog("[cap] NET_ADMIN confirmed via bridge creation")
		return true, ""
	}
	debugLog("[cap] bridge creation failed, trying iptables")
	if exec.Command(iptVariant(), "-L", "-n").Run() == nil {
		debugLog("[cap] NET_ADMIN confirmed via iptables-legacy")
		return true, ""
	}
	if exec.Command("iptables", "-L", "-n").Run() == nil {
		debugLog("[cap] NET_ADMIN confirmed via iptables-nft")
		return true, ""
	}
	if exec.Command("chroot", "/host", "/usr/sbin/iptables", "-L", "-n").Run() == nil {
		debugLog("[cap] NET_ADMIN confirmed via chroot /host iptables")
		return true, ""
	}
	return false, "no NET_ADMIN capability (bridge creation, iptables-legacy, iptables, and chroot /host all failed)"
})

func CheckCapability() (bool, string) {
	return cachedCapability()
}

// CheckL2TPCapability tests if the runtime can actually run L2TP/IPsec.
// Checks NET_ADMIN, /dev/ppp, and kernel PPP module support.
// On Docker Desktop (WSL), mknod succeeds but kernel has no PPP → must reject.
func CheckL2TPCapability() (bool, string) {
	if ok, reason := CheckCapability(); !ok {
		return false, reason
	}
	// Does /dev/ppp exist? Try to create it if missing.
	if _, err := os.Stat("/dev/ppp"); err != nil {
		if exec.Command("mknod", "/dev/ppp", "c", "108", "0").Run() != nil {
			return false, "/dev/ppp not found and mknod failed (missing device or privileges)"
		}
	}
	// Verify kernel actually supports PPP — opening /dev/ppp returns ENOENT on
	// Docker Desktop/WSL where the device node exists but the driver doesn't.
	f, err := os.OpenFile("/dev/ppp", os.O_RDWR, 0)
	if err != nil {
		return false, "kernel PPP module not available (Docker Desktop/WSL not supported)"
	}
	f.Close()
	return true, ""
}

// CheckIKEv2Capability tests if charon (strongSwan) can run.
// On Docker Desktop/WSL, charon exists but xfrm is non-functional.
func CheckIKEv2Capability() bool {
	if ok, _ := CheckCapability(); !ok {
		return false
	}
	// Test xfrm by trying to add and remove a dummy xfrm state.
	// On WSL/Docker Desktop, this fails with EPROTONOSUPPORT.
	out, err := exec.Command("ip", "xfrm", "state", "count").CombinedOutput()
	if err != nil {
		debugLog("[ikev2] xfrm not available: %v: %s", err, string(out))
		return false
	}
	return true
}

// StartL2TP sets up xl2tpd, strongswan, iptables, and the transparent proxy.
func (a *App) StartL2TP(cfg L2TPConfig) error {
	if !cfg.Enabled || cfg.Pool == "" {
		return nil
	}
	if ok, reason := CheckL2TPCapability(); !ok {
		log.Printf("[l2tp] disabled: %s", reason)
		return fmt.Errorf("insufficient privileges")
	}

	// Create cancellable context for this L2TP session
	l2tpCtx, l2tpCancelFn := context.WithCancel(a.appCtx)
	a.mu.Lock()
	a.l2tpCancel = l2tpCancelFn
	a.mu.Unlock()

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
wget -qO- "http://` + gateway + fmt.Sprintf(`:%d/ppp/up?ip=$5&user=$PEERNAME&iface=$1" 2>/dev/null
`, hooksPort)

	ipDownScript := `#!/bin/sh
wget -qO- "http://` + gateway + fmt.Sprintf(`:%d/ppp/down?ip=$5&iface=$1" 2>/dev/null
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
    ike=aes256-sha256-modp3072,aes256-sha256-modp2048,aes128-sha256-modp3072,aes128-sha256-modp2048,aes128-sha1-modp1024,3des-sha1-modp1024!
    esp=aes256-sha256,aes128-sha256,aes128-sha1,3des-sha1!
    dpdaction=clear
    dpddelay=300s

`
	ipsecSecrets := fmt.Sprintf(`%%any %%any : PSK "%s"
`, cfg.PSK)

	// strongswan.conf: fix IKEv1 iOS compatibility
	// strongswan.conf: increase log level when DEBUG is set
	charonLog := ""
	if debugMode() {
		charonLog = `
    filelog {
        /dev/stderr {
            ike = 2
            cfg = 2
            net = 1
            enc = 1
            knl = 1
            default = 1
            flush_line = yes
        }
    }`
	}
	strongswanConf := fmt.Sprintf(`charon {
    load_modular = yes
    max_ikev1_exchanges = 100%s
    plugins {
        include strongswan.d/charon/*.conf
    }
}
`, charonLog)

	os.MkdirAll("/etc/ipsec.d", 0755)
	os.WriteFile("/etc/strongswan.conf", []byte(strongswanConf), 0644)
	os.WriteFile("/etc/ipsec.conf", []byte(ipsecConf), 0644)
	os.WriteFile("/etc/ipsec.secrets", []byte(ipsecSecrets), 0600)

	// 7. Setup traffic forwarding
	os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)

	if testIptablesAvailable() {
		if iptUseChroot() {
			log.Printf("[l2tp] mode: iptables via chroot /host (host kernel compat)")
		} else {
			log.Printf("[l2tp] mode: native iptables DNAT + transparent proxy")
		}
		portStr := fmt.Sprintf("%d", proxyPort)
		iptRun(iptVariant(), "-t", "nat", "-I", "PREROUTING",
			"-i", "ppp+", "-p", "tcp",
			"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%s", gateway, portStr))
		iptRun(iptVariant(), "-t", "nat", "-A", "POSTROUTING",
			"-s", subnet, "-o", "eth0", "-j", "MASQUERADE")
		iptRun(iptVariant(), "-I", "FORWARD", "-i", "ppp+", "-o", "eth0", "-j", "ACCEPT")
		iptRun(iptVariant(), "-I", "FORWARD", "-i", "eth0", "-o", "ppp+",
			"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		iptRun(iptVariant(), "-A", "INPUT", "-p", "tcp", "--dport", portStr,
			"-s", subnet, "-j", "ACCEPT")
		iptRun(iptVariant(), "-A", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "DROP")
		hooksPortStr := fmt.Sprintf("%d", hooksPort)
		iptRun(iptVariant(), "-A", "INPUT", "-p", "tcp", "--dport", hooksPortStr,
			"-i", "lo", "-j", "ACCEPT")
		iptRun(iptVariant(), "-A", "INPUT", "-p", "tcp", "--dport", hooksPortStr, "-j", "DROP")
		iptRun("iptables", "-I", "DOCKER-USER", "-s", subnet, "-j", "ACCEPT")
		iptRun("iptables", "-I", "DOCKER-USER", "-d", subnet, "-j", "ACCEPT")
		iptRun("iptables", "-t", "nat", "-A", "POSTROUTING",
			"-s", subnet, "-j", "MASQUERADE")
		go a.runTransparentProxy(l2tpCtx, gateway, proxyPort)
	} else {
		// Compat mode: TUN capture with gvisor netstack (no iptables needed)
		log.Printf("[l2tp] iptables unavailable, using TUN capture mode (compat)")
		if err := ensureTunCapture(a, subnet); err != nil {
			log.Printf("[l2tp] TUN capture failed: %v", err)
			return err
		}
		// Start PPP hooks server (for AF_PACKET bridge on ppp interfaces)
		go a.servePPPHooks(l2tpCtx, gateway, hooksPort)
	}

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

// StopL2TP stops the L2TP service.
func (a *App) StopL2TP() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.l2tpCancel != nil {
		a.l2tpCancel()
		a.l2tpCancel = nil
	}
	// Kill xl2tpd
	exec.Command("killall", "xl2tpd").Run()
	// Remove L2TP ipsec connection
	exec.Command("ipsec", "down", "l2tp-psk").Run()
	log.Printf("[l2tp] stopped")
}

// RestartL2TP stops and restarts L2TP with current config.
func (a *App) RestartL2TP() error {
	a.StopL2TP()
	time.Sleep(500 * time.Millisecond)
	cfg := a.store.Get()
	if cfg.L2TP == nil || !cfg.L2TP.Enabled {
		return nil
	}
	return a.StartL2TP(*cfg.L2TP)
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
func (a *App) runTransparentProxy(ctx context.Context, gatewayIP string, port int) {
	addr := fmt.Sprintf("%s:%d", gatewayIP, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[l2tp] transparent proxy listen error: %v", err)
		return
	}
	defer ln.Close()
	log.Printf("[l2tp] transparent proxy on %s", addr)

	// Also serve PPP ip-up/ip-down hooks on HTTP
	go a.servePPPHooks(ctx, gatewayIP, port+1)

	go func() { <-ctx.Done(); ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go a.handleTransparent(conn)
	}
}

// servePPPHooks handles ip-up/ip-down notifications from pppd.
func (a *App) servePPPHooks(ctx context.Context, gatewayIP string, port int) {
	// Simple HTTP server on the gateway IP for PPP hooks (only from localhost)
	httpLn, err := net.Listen("tcp", fmt.Sprintf("%s:%d", gatewayIP, port))
	if err != nil {
		log.Printf("[l2tp] ppp hook listener error: %v", err)
		return
	}
	defer httpLn.Close()
	go func() { <-ctx.Done(); httpLn.Close() }()

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
						// Compat mode: start AF_PACKET bridge on PPP interface
						// to bypass FORWARD chain DROP (same approach as ipsec0 bridge)
						if ifn, ok := params["iface"]; ok && ifn != "" && TunCaptureActive() && tunCaptureInst != nil {
							registerXfrmClient(ip, ifn)
							go func() {
								if err := waitForInterface(ifn, 5*time.Second); err != nil {
									log.Printf("[l2tp] ppp iface wait: %v", err)
									return
								}
								if err := startXfrmBridge(a.appCtx, ifn, tunCaptureInst.ep); err != nil {
									log.Printf("[l2tp] ppp AF_PACKET bridge: %v", err)
								} else {
									log.Printf("[l2tp] ppp AF_PACKET bridge on %s active", ifn)
								}
							}()
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
						if ifn, ok := params["iface"]; ok && ifn != "" {
							stopXfrmBridge(ifn)
							unregisterXfrmClient(ip)
						}
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
	exitMode := ""
	if user != nil {
		exitVia = user.ExitVia
		exitMode = user.ExitMode
	}

	var remote net.Conn
	if exitVia == "" {
		log.Printf("[l2tp] direct dial %s", origDst)
		remote, err = net.DialTimeout("tcp", origDst, 10*time.Second)
	} else {
		log.Printf("[l2tp] dial via %s to %s", exitVia, origDst)
		remote, err = a.dialExitWithMode(context.Background(), exitVia, exitMode, origDst)
	}
	if err != nil {
		log.Printf("[l2tp] dial error: %v", err)
		return
	}
	log.Printf("[l2tp] connected to %s for user %s", origDst, username)
	defer remote.Close()

	ctx, cancel := context.WithCancel(context.Background())
	sid := a.Sessions.Connect(username, srcIP, "l2tp", cancel)

	var up, down int64
	done := make(chan struct{})
	go func() {
		n, _ := copyCtx(ctx, remote, conn)
		atomic.AddInt64(&up, n)
		remote.Close()
		done <- struct{}{}
	}()
	n, _ := copyCtx(ctx, conn, remote)
	atomic.AddInt64(&down, n)
	<-done
	cancel()
	a.Sessions.Disconnect(sid, atomic.LoadInt64(&up), atomic.LoadInt64(&down))
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

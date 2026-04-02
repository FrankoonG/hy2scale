package app

import (
	"context"
	"fmt"
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
	DefaultExit     string `yaml:"default_exit" json:"default_exit"`           // exit_via when user mode off
	DefaultExitMode string `yaml:"default_exit_mode,omitempty" json:"default_exit_mode,omitempty"` // ""|"quality"|"aggregate"
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

func (c *IKEv2Config) mtuVal() int {
	if c.MTU <= 0 {
		return 1400
	}
	return c.MTU
}

// writeSwanctlIKEv2 generates swanctl.conf for IKEv2 with xfrm interface support (if_id).
func (a *App) writeSwanctlIKEv2(cfg IKEv2Config, localID, remoteID, ipRange, dns string) {
	os.MkdirAll("/etc/swanctl/conf.d", 0755)
	os.MkdirAll("/etc/swanctl/secrets.d", 0755)

	var conf string
	switch cfg.Mode {
	case "psk":
		conf = fmt.Sprintf(`connections {
    ikev2-psk {
        version = 2
        local_addrs = %%any
        local-1 {
            auth = psk
            id = %s
        }
        remote-1 {
            auth = psk
            id = %s
        }
        pools = ikev2pool
        children {
            ikev2-psk {
                local_ts = 0.0.0.0/0
                remote_ts = 0.0.0.0/0
                if_id_in = %%unique
                if_id_out = %%unique
                updown = /etc/ipsec.d/ikev2-updown.sh
                esp_proposals = aes256-sha256,aes128-sha256
                rekey_time = 0s
                dpd_action = clear
            }
        }
    }
}
pools {
    ikev2pool {
        addrs = %s
        dns = %s
    }
}
`, localID, remoteID, ipRange, strings.ReplaceAll(dns, " ", ", "))

	case "mschapv2":
		// Server identity = localID (matches auto-generated cert CN/SAN)
		conf = fmt.Sprintf(`connections {
    ikev2-mschapv2 {
        version = 2
        local_addrs = %%any
        send_certreq = no
        send_cert = always
        unique = never
        local-1 {
            auth = pubkey
            certs = ikev2-server.cert.pem
            id = %s
        }
        remote-1 {
            auth = eap-mschapv2
            eap_id = %%any
        }
        pools = ikev2pool
        children {
            ikev2-mschapv2 {
                local_ts = 0.0.0.0/0
                remote_ts = 0.0.0.0/0
                if_id_in = %%unique
                if_id_out = %%unique
                updown = /etc/ipsec.d/ikev2-updown.sh
                esp_proposals = aes256-sha256,aes128-sha256
                rekey_time = 0s
                dpd_action = clear
            }
        }
    }
}
pools {
    ikev2pool {
        addrs = %s
        dns = %s
    }
}
`, localID, ipRange, strings.ReplaceAll(dns, " ", ", "))
	}

	os.WriteFile("/etc/swanctl/conf.d/ikev2.conf", []byte(conf), 0644)

	// Write secrets
	var secrets string
	switch cfg.Mode {
	case "psk":
		if cfg.PSK != "" {
			secrets = fmt.Sprintf("secrets {\n    ike-psk {\n        secret = \"%s\"\n    }\n}\n", cfg.PSK)
		}
	case "mschapv2":
		// swanctl needs: private key reference + EAP secrets for each user
		var sb strings.Builder
		sb.WriteString("secrets {\n")
		sb.WriteString("    rsa-key {\n        file = ikev2-server.key.pem\n    }\n")
		cfgStore := a.store.Get()
		for i, u := range cfgStore.Users {
			if u.Enabled {
				sb.WriteString(fmt.Sprintf("    eap-user%d {\n        id = %s\n        secret = \"%s\"\n    }\n", i, u.Username, u.Password))
			}
		}
		sb.WriteString("}\n")
		secrets = sb.String()
	}
	if secrets != "" {
		os.WriteFile("/etc/swanctl/secrets.d/ikev2.conf", []byte(secrets), 0644)
	}
	// Ensure base swanctl.conf exists
	os.WriteFile("/etc/swanctl/swanctl.conf", []byte("include conf.d/*.conf\ninclude secrets.d/*.conf\n"), 0644)

	log.Printf("[ikev2] swanctl config written with if_id for xfrm interface mode")
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
	// In host network mode the container sees host-level interfaces like
	// docker0, br-xxx, real NICs. In bridge/custom network mode, these
	// are NOT visible — only eth0 (veth) + lo + any TUN/ipsec/ppp
	// interfaces created by the app itself.
	// Check for docker0 or br-* which only exist in host network mode.
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		name := e.Name()
		if name == "docker0" || strings.HasPrefix(name, "br-") {
			return true
		}
	}
	return false
}

// StartIKEv2 configures and starts IKEv2/IPsec VPN.
func (a *App) StartIKEv2(cfg IKEv2Config) error {
	if !cfg.Enabled {
		return nil
	}
	if ok, reason := CheckCapability(); !ok {
		log.Printf("[ikev2] disabled: %s", reason)
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
		// cert_id points to a CA cert — auto-sign a server cert on each startup
		caInfo, err := a.tls.Get(cfg.CertID)
		if err != nil {
			return fmt.Errorf("ikev2: CA cert %s not found", cfg.CertID)
		}
		if !caInfo.IsCA {
			return fmt.Errorf("ikev2: cert %s is not a CA certificate", cfg.CertID)
		}
		// Auto-generate server cert: CN=localID, SAN=DNS:localID, signed by CA
		log.Printf("[ikev2] auto-signing server cert: CN=%s (CA=%s)", localID, cfg.CertID)
		if err := a.tls.SignWithCA(cfg.CertID, "__ikev2_auto__", "IKEv2 Auto", localID, 3650); err != nil {
			return fmt.Errorf("ikev2: auto-sign cert failed: %w", err)
		}
		// Install auto-generated server cert
		certData, _ := os.ReadFile(a.tls.CertPath("__ikev2_auto__"))
		keyData, _ := os.ReadFile(a.tls.KeyPath("__ikev2_auto__"))
		os.MkdirAll("/etc/ipsec.d/certs", 0755)
		os.MkdirAll("/etc/ipsec.d/private", 0755)
		os.WriteFile("/etc/ipsec.d/certs/ikev2-server.cert.pem", certData, 0644)
		os.WriteFile("/etc/ipsec.d/private/ikev2-server.key.pem", keyData, 0600)
		// Install CA cert for cert chain verification
		caData, _ := os.ReadFile(a.tls.CertPath(cfg.CertID))
		os.MkdirAll("/etc/ipsec.d/cacerts", 0755)
		os.WriteFile("/etc/ipsec.d/cacerts/ca.pem", caData, 0644)
		os.MkdirAll("/etc/swanctl/x509ca", 0755)
		os.WriteFile("/etc/swanctl/x509ca/ca.pem", caData, 0644)
		// Symlink swanctl dirs → ipsec.d dirs
		os.MkdirAll("/etc/swanctl", 0755)
		os.Symlink("/etc/ipsec.d/certs", "/etc/swanctl/x509")
		os.Symlink("/etc/ipsec.d/private", "/etc/swanctl/private")
		// Clean up temp cert from TLS store (keep only in ipsec dirs)
		a.tls.Delete("__ikev2_auto__")

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

	// Compat mode: empty ipsec.conf (no stroke connection), no stroke EAP secrets.
	// All secrets come exclusively from swanctl to avoid duplicate EAP entries
	// that cause MSCHAPv2 verification failure on strongSwan 5.8.4.
	iptablesOK := testIptablesAvailable()
	// Always write ipsec.conf. In compat mode with kernel-libipsec, the stroke
	// connection handles EAP MSCHAPv2 auth (vici MSCHAPv2 is broken on 5.8.4).
	// kernel-libipsec handles ESP in userspace via ipsec0 TUN.
	// AF_PACKET bridge on ipsec0 captures decrypted packets, bypassing FORWARD DROP.
	appendToIPSecConf(connConf)

	// Update secrets — compat mode skips ipsec.secrets EAP entries
	if cfg.Mode == "psk" && cfg.PSK != "" {
		appendPSKSecret(cfg.PSK)
	}
	if cfg.Mode == "mschapv2" && iptablesOK {
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

	if testIptablesAvailable() {
		if iptUseChroot() {
			log.Printf("[ikev2] mode: iptables via chroot /host (host kernel compat)")
		} else {
			log.Printf("[ikev2] mode: native iptables DNAT + transparent proxy")
		}
		portStr := fmt.Sprintf("%d", proxyPort)
		iptRun("iptables-legacy", "-t", "nat", "-I", "PREROUTING",
			"-s", subnet, "-p", "tcp",
			"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%s", gateway, portStr))
		iptRun("iptables-legacy", "-t", "nat", "-A", "POSTROUTING",
			"-s", subnet, "-o", "eth0", "-j", "MASQUERADE")
		iptRun("iptables-legacy", "-I", "FORWARD", "-s", subnet, "-o", "eth0", "-j", "ACCEPT")
		iptRun("iptables-legacy", "-I", "FORWARD", "-d", subnet,
			"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		iptRun("iptables-legacy", "-I", "INPUT", "-p", "tcp", "--dport", portStr,
			"-s", subnet, "-j", "ACCEPT")
		iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "DROP")
		hooksPortStr := fmt.Sprintf("%d", hooksPort)
		iptRun("iptables-legacy", "-I", "INPUT", "-p", "tcp", "--dport", hooksPortStr,
			"-i", "lo", "-j", "ACCEPT")
		iptRun("iptables-legacy", "-A", "INPUT", "-p", "tcp", "--dport", hooksPortStr, "-j", "DROP")
		iptRun("iptables", "-I", "DOCKER-USER", "-s", subnet, "-j", "ACCEPT")
		iptRun("iptables", "-I", "DOCKER-USER", "-d", subnet, "-j", "ACCEPT")
		iptRun("iptables", "-t", "nat", "-A", "POSTROUTING",
			"-s", subnet, "-j", "MASQUERADE")
		go a.runIKEv2Proxy(gateway, proxyPort, hooksPort, cfg)
	} else {
		// Compat mode: xfrm interface + TUN capture (swanctl with if_id)
		log.Printf("[ikev2] mode: compat (iptables unavailable → xfrm interface + AF_PACKET bridge + gvisor netstack)")
		// Store PSK default exit for TUN capture forwarder
		if cfg.Mode == "psk" && cfg.DefaultExit != "" {
			ikev2DefaultExitVia.Store(cfg.DefaultExit)
			log.Printf("[ikev2] PSK default exit: %s", cfg.DefaultExit)
		}
		if err := ensureTunCapture(a, subnet); err != nil {
			log.Printf("[ikev2] TUN capture failed: %v", err)
			return err
		}


		// Updown script for compat mode: session tracking only.
		// With kernel-libipsec, ipsec0 TUN + AF_PACKET bridge handles all traffic.
		// No xfrm interface (ikecN) creation needed.
		xfrmUpdown := fmt.Sprintf(`#!/bin/sh
case "$PLUTO_VERB" in
  up-client|up-client-v6)
    wget -qO- "http://%s:%d/ikev2/up?ip=${PLUTO_PEER_SOURCEIP}&user=${PLUTO_PEER_ID}" 2>/dev/null
    ;;
  down-client|down-client-v6)
    wget -qO- "http://%s:%d/ikev2/down?ip=${PLUTO_PEER_SOURCEIP}" 2>/dev/null
    ;;
esac
`, gateway, hooksPort, gateway, hooksPort)

		os.MkdirAll("/etc/ipsec.d", 0755)
		os.WriteFile("/etc/ipsec.d/ikev2-updown.sh", []byte(xfrmUpdown), 0755)

		// Generate swanctl config with if_id for xfrm interface mode
		a.writeSwanctlIKEv2(cfg, localID, remoteID, ipRange, dns)

		// Hooks server for session tracking
		go a.serveIKEv2Hooks(gateway, hooksPort, cfg)
	}

	ensureStrongswanRunning()
	time.Sleep(time.Second)
	run("ipsec", "update")
	run("ipsec", "rereadsecrets")
	run("swanctl", "--load-all", "--noprompt")

	log.Printf("[ikev2] server mode=%s pool=%s", cfg.Mode, cfg.Pool)

	// Compat mode with kernel-libipsec: start AF_PACKET bridge on ipsec0.
	// kernel-libipsec decrypts ESP in userspace and writes decrypted packets to the
	// ipsec0 TUN device. Normally the kernel would route these to hy2cap0, but the
	// container's FORWARD chain (set to DROP by Docker) blocks this.
	// AF_PACKET captures at link layer BEFORE netfilter, bypassing FORWARD.
	// This is the same mechanism as the xfrm bridge on ikecN, but on ipsec0.
	if !iptablesOK && tunCaptureInst != nil {
		go func() {
			// Wait for kernel-libipsec TUN device (ipsec0, ipsec1, etc.)
			var ipsecIf string
			for i := 0; i < 30; i++ {
				for j := 0; j < 5; j++ {
					name := fmt.Sprintf("ipsec%d", j)
					if err := waitForInterface(name, time.Second); err == nil {
						ipsecIf = name
						break
					}
				}
				if ipsecIf != "" {
					break
				}
				time.Sleep(time.Second)
			}
			if ipsecIf == "" {
				log.Printf("[ikev2] no ipsec TUN device found after 30s")
				return
			}
			if err := startXfrmBridge(a.appCtx, ipsecIf, tunCaptureInst.ep); err != nil {
				log.Printf("[ikev2] %s AF_PACKET bridge failed: %v", ipsecIf, err)
			} else {
				log.Printf("[ikev2] %s AF_PACKET bridge active (kernel-libipsec userspace ESP)", ipsecIf)
			}
		}()
	}

	return nil
}

// StopIKEv2 stops the IKEv2 service (kills strongswan connections, removes configs).
func (a *App) StopIKEv2() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ikev2Cancel != nil {
		a.ikev2Cancel()
		a.ikev2Cancel = nil
	}
	// Remove IKEv2 swanctl config and reload
	os.Remove("/etc/swanctl/conf.d/ikev2.conf")
	os.Remove("/etc/swanctl/secrets.d/ikev2.conf")
	exec.Command("swanctl", "--load-all", "--noprompt").Run()
	// Disconnect all IKEv2 connections
	exec.Command("ipsec", "down", "ikev2-mschapv2").Run()
	exec.Command("ipsec", "down", "ikev2-psk").Run()
	log.Printf("[ikev2] stopped")
}

// RestartIKEv2 stops and restarts the IKEv2 service with current config.
func (a *App) RestartIKEv2() error {
	a.StopIKEv2()
	time.Sleep(500 * time.Millisecond)
	cfg := a.store.Get()
	if cfg.IKEv2 == nil || !cfg.IKEv2.Enabled {
		return nil
	}
	return a.StartIKEv2(*cfg.IKEv2)
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
	// Reload secrets via both stroke and vici (swanctl) to ensure both paths work
	exec.Command("ipsec", "rereadsecrets").Run()
	exec.Command("swanctl", "--load-creds").Run()
}

// resolveEAPIdentity queries swanctl --list-sas to find the EAP username
// for a client with the given virtual IP. This is needed because PLUTO_PEER_ID
// gives the IKE identity, which may differ from the EAP username used for auth.
// Returns the EAP username if found, or empty string.
func resolveEAPIdentity(clientVIP string) string {
	out, err := exec.Command("swanctl", "--list-sas").Output()
	if err != nil {
		return ""
	}
	// swanctl --list-sas output format:
	//   remote 'ike-id' @ ip[port] EAP: 'eap-user' [virtual-ip]
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "remote ") {
			continue
		}
		// Check if this line contains our client VIP in brackets
		if !strings.Contains(line, "["+clientVIP+"]") {
			continue
		}
		// Extract EAP identity: EAP: 'username'
		if idx := strings.Index(line, "EAP: '"); idx >= 0 {
			rest := line[idx+6:]
			if end := strings.IndexByte(rest, '\''); end >= 0 {
				return rest[:end]
			}
		}
	}
	return ""
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

var strongswanOnce sync.Once

// ensureStrongswanRunning starts strongswan charon if not already running.
func ensureStrongswanRunning() {
	strongswanOnce.Do(func() {
		// Clean stale PID files that survive container restart
		os.Remove("/var/run/starter.charon.pid")
		os.Remove("/var/run/charon.pid")
		// Clean stale xfrm state/policy
		exec.Command("ip", "xfrm", "state", "flush").Run()
		exec.Command("ip", "xfrm", "policy", "flush").Run()

		log.Printf("[ipsec] starting strongswan...")
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
				log.Printf("[ipsec] strongswan ready")
				return
			}
		}
		log.Printf("[ipsec] warning: strongswan may not be ready after 10s")
	})
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
							// For MSCHAPv2: PLUTO_PEER_ID is the IKE identity, which may
							// differ from the EAP username. Resolve via swanctl if needed.
							if cfg.Mode == "mschapv2" && user != "__psk__" {
								go func(clientIP, ikeID string) {
									time.Sleep(300 * time.Millisecond)
									eapUser := resolveEAPIdentity(clientIP)
									if eapUser != "" && eapUser != ikeID {
										ikev2Sessions.Register(clientIP, eapUser)
										log.Printf("[ikev2] EAP identity resolved: %s → %s (IKE: %s)", clientIP, eapUser, ikeID)
									}
								}(ip, user)
							}
						}
						// In compat mode, start xfrm bridge for this client's interface
						if ifn := params["iface"]; ifn != "" && TunCaptureActive() && tunCaptureInst != nil {
							registerXfrmClient(ip, ifn)
							go func() {
								if err := waitForInterface(ifn, 5*time.Second); err != nil {
									log.Printf("[ikev2] xfrm iface wait: %v", err)
									return
								}
								if err := startXfrmBridge(a.appCtx, ifn, tunCaptureInst.ep); err != nil {
									log.Printf("[ikev2] xfrm bridge: %v", err)
								}
							}()
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
						// Stop xfrm bridge for this client
						if ifn := xfrmIfForClient(ip); ifn != "" {
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
	exitMode := ""
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
			exitMode = user.ExitMode
		}
	} else if cfg.Mode == "psk" && !cfg.PSKUserMode {
		exitVia = cfg.DefaultExit
		exitMode = cfg.DefaultExitMode
	} else if !ok {
		exitVia = ""
	}

	var remote net.Conn
	if exitVia == "" {
		log.Printf("[ikev2] direct dial %s", origDst)
		remote, err = net.DialTimeout("tcp", origDst, 10*time.Second)
	} else {
		log.Printf("[ikev2] dial via %s to %s", exitVia, origDst)
		remote, err = a.dialExitWithMode(context.Background(), exitVia, exitMode, origDst)
	}
	if err != nil {
		log.Printf("[ikev2] dial error: %v", err)
		return
	}
	defer remote.Close()
	log.Printf("[ikev2] connected to %s for user %s", origDst, username)

	ctx, cancel := context.WithCancel(context.Background())
	sid := a.Sessions.Connect(username, srcIP, "ikev2", cancel)

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
	if username != "" && username != "__psk__" {
		a.RecordTraffic(username, atomic.LoadInt64(&up)+atomic.LoadInt64(&down))
	}
}

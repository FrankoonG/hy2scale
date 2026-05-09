package app

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// regenIPSec rewrites /etc/ipsec.conf, /etc/ipsec.secrets and /etc/strongswan.conf
// from the current cfg in a single shot, then reloads charon so the new state
// becomes active without a process restart.
//
// Why this exists: L2TP's StartL2TP used to overwrite /etc/ipsec.{conf,secrets}
// without a charon reload, so PSK changes only took effect on container restart.
// IKEv2's StartIKEv2 appended new PSK lines without removing the old ones, so
// repeated PSK changes accumulated stale entries that could win the auth match.
// One regenerator avoids both bugs and makes the on-disk state always reflect
// `cfg.L2TP` and `cfg.IKEv2` exactly.
func (a *App) regenIPSec() {
	// Both StartL2TP and StartIKEv2 call regenIPSec, and they hold their own
	// (separate) restart mutexes. Without this lock, an L2TP restart and an
	// IKEv2 restart can interleave their cfg-read → file-write → reload
	// sequence and one can clobber the other's update — observed in field
	// sweeps where IKEv2 local_id changes silently disappeared because an
	// L2TP regen ran with a slightly older cfg snapshot in between.
	a.ipsecRegenMu.Lock()
	defer a.ipsecRegenMu.Unlock()

	cfg := a.store.Get()

	// /etc/ipsec.conf — base config setup + per-service conn blocks.
	var conf strings.Builder
	conf.WriteString("config setup\n    uniqueids=never\n\n")
	if cfg.L2TP != nil && cfg.L2TP.Enabled {
		conf.WriteString(buildL2TPConnBlock())
	}
	if cfg.IKEv2 != nil && cfg.IKEv2.Enabled {
		nodeID := cfg.NodeID
		localID := cfg.IKEv2.LocalID
		cfgLocalIDExplicit := localID != ""
		if localID == "" {
			localID = nodeID
		}
		remoteID := cfg.IKEv2.RemoteID
		if remoteID == "" {
			remoteID = "%any"
		}
		_, _, ipRange, err := parsePool(cfg.IKEv2.Pool)
		if err == nil {
			conf.WriteString(buildIKEv2ConnBlock(*cfg.IKEv2, localID, remoteID, ipRange, a.getDNS(), cfgLocalIDExplicit))
		}
	}

	// /etc/ipsec.secrets — PSKs from both services + cert-key reference for
	// IKEv2 mschapv2 (native iptables mode only).
	var secrets strings.Builder
	if cfg.L2TP != nil && cfg.L2TP.Enabled && cfg.L2TP.PSK != "" {
		secrets.WriteString(fmt.Sprintf("%%any %%any : PSK \"%s\"\n", cfg.L2TP.PSK))
	}
	if cfg.IKEv2 != nil && cfg.IKEv2.Enabled {
		if cfg.IKEv2.Mode == "psk" && cfg.IKEv2.PSK != "" &&
			(cfg.L2TP == nil || !cfg.L2TP.Enabled || cfg.L2TP.PSK != cfg.IKEv2.PSK) {
			secrets.WriteString(fmt.Sprintf("%%any %%any : PSK \"%s\"\n", cfg.IKEv2.PSK))
		}
		if cfg.IKEv2.Mode == "mschapv2" && testIptablesAvailable() {
			kd, _ := os.ReadFile("/etc/ipsec.d/private/ikev2-server.key.pem")
			keyType := "ECDSA"
			if strings.Contains(string(kd), "RSA PRIVATE KEY") {
				keyType = "RSA"
			}
			secrets.WriteString(fmt.Sprintf(": %s ikev2-server.key.pem\n", keyType))
		}
	}

	// /etc/strongswan.conf — charon plugin loading + log level.
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
	swConf := fmt.Sprintf(`charon {
    load_modular = yes
    max_ikev1_exchanges = 100%s
    plugins {
        include strongswan.d/charon/*.conf
    }
}
`, charonLog)

	os.MkdirAll("/etc/ipsec.d", 0755)
	if err := os.WriteFile("/etc/ipsec.conf", []byte(conf.String()), 0644); err != nil {
		log.Printf("[ipsec] write ipsec.conf: %v", err)
	}
	if err := os.WriteFile("/etc/ipsec.secrets", []byte(secrets.String()), 0600); err != nil {
		log.Printf("[ipsec] write ipsec.secrets: %v", err)
	}
	os.WriteFile("/etc/strongswan.conf", []byte(swConf), 0644)

	// Tell the running charon to pick up the new files. Skip when charon
	// hasn't been started yet — the boot path will read these on its first
	// `ipsec start`.
	if !strongswanRunning {
		return
	}
	exec.Command("ipsec", "rereadsecrets").Run()
	exec.Command("ipsec", "update").Run()
	exec.Command("swanctl", "--load-all", "--noprompt").Run()
}

// buildL2TPConnBlock returns the `conn l2tp-psk` section as it has lived
// inside StartL2TP. Pure string construction — no I/O, no cfg access — so
// regenIPSec can compose it next to the IKEv2 block.
func buildL2TPConnBlock() string {
	return `conn l2tp-psk
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
}

// buildIKEv2ConnBlock returns the `conn ikev2-{psk,mschapv2}` section, using
// the same shape StartIKEv2 already builds. Kept in sync with that path.
func buildIKEv2ConnBlock(cfg IKEv2Config, localID, remoteID, ipRange, dns string, cfgLocalIDExplicit bool) string {
	switch cfg.Mode {
	case "psk":
		return fmt.Sprintf(`
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
		leftIDLine := ""
		if cfgLocalIDExplicit {
			leftIDLine = fmt.Sprintf("    leftid=@%s\n", localID)
		}
		return fmt.Sprintf(`
conn ikev2-mschapv2
    keyexchange=ikev2
    auto=add
    type=tunnel
    left=%%any
%s    leftcert=ikev2-server.cert.pem
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
`, leftIDLine, ipRange, dns)
	}
	return ""
}

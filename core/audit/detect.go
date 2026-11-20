package audit

// Application protocol of a flow, from the first bytes each side sends
// (byte signatures; no state, no parsing beyond a header). A flow looks at
// most a few chunks; once decided (or given up) it costs one atomic load.

import (
	"bytes"
	"net"
	"strconv"
)

// Application protocols.
const (
	AppTLS        = "tls"
	AppHTTP       = "http"
	AppHTTP2      = "http2" // cleartext HTTP/2 (prior knowledge)
	AppQUIC       = "quic"
	AppDNS        = "dns"
	AppMDNS       = "mdns"
	AppSSH        = "ssh"
	AppRDP        = "rdp"
	AppVNC        = "vnc"
	AppSMB        = "smb"
	AppSMTP       = "smtp"
	AppFTP        = "ftp"
	AppPOP3       = "pop3"
	AppIMAP       = "imap"
	AppMySQL      = "mysql"
	AppPostgres   = "postgres"
	AppRedis      = "redis"
	AppMQTT       = "mqtt"
	AppXMPP       = "xmpp"
	AppSOCKS5     = "socks5"
	AppBitTorrent = "bittorrent"
	AppSTUN       = "stun"
	AppNTP        = "ntp"
	AppDTLS       = "dtls"
	AppWireGuard  = "wireguard"
	AppOpenVPN    = "openvpn"
	AppIKE        = "ike"
	AppSSDP       = "ssdp"
	AppSIP        = "sip"
)

// appTries: chunks a flow looks at before it gives up.
const appTries = 6

// Observe looks at a payload chunk (up: from the client) to tell the
// application protocol (nil-safe; cheap once decided).
func (f *Flow) Observe(p []byte, up bool) {
	if f == nil || len(p) == 0 || f.appDone.Load() {
		return
	}
	if f.appSeen.Add(1) > appTries {
		f.appDone.Store(true)
		return
	}
	f.mu.Lock()
	proto, dst, have := f.rec.Proto, f.rec.Dst, f.rec.App != ""
	f.mu.Unlock()
	if have {
		f.appDone.Store(true)
		return
	}
	if app := Classify(proto, portOf(dst), p, up); app != "" {
		f.appDone.Store(true)
		f.Set(func(r *Record) {
			if r.App == "" {
				r.App = app
			}
		})
	}
}

// AddData counts a payload chunk and looks at it (Add + Observe).
func (f *Flow) AddData(p []byte, up bool) {
	if f == nil {
		return
	}
	if up {
		f.up.Add(int64(len(p)))
	} else {
		f.down.Add(int64(len(p)))
	}
	f.Observe(p, up)
}

func portOf(hostport string) int {
	if _, p, err := net.SplitHostPort(hostport); err == nil {
		n, _ := strconv.Atoi(p)
		return n
	}
	return 0
}

// Classify tells the application protocol of one chunk ("" = unknown).
// proto is "tcp" or "udp", port the destination port (0 = unknown).
func Classify(proto string, port int, p []byte, up bool) string {
	if proto == "udp" {
		return classifyUDP(port, p, up)
	}
	return classifyTCP(port, p, up)
}

func hasPrefix(p []byte, s string) bool { return len(p) >= len(s) && string(p[:len(s)]) == s }

func anyPrefix(p []byte, ss ...string) bool {
	for _, s := range ss {
		if hasPrefix(p, s) {
			return true
		}
	}
	return false
}

func be16(p []byte) int { return int(p[0])<<8 | int(p[1]) }

// dnsQuery: a DNS query header (one question, no answers).
func dnsQuery(p []byte) bool {
	return len(p) >= 17 && p[2]&0x80 == 0 && (p[2]>>3)&0x0f == 0 && be16(p[4:]) == 1 && be16(p[6:]) == 0 && be16(p[8:]) == 0
}

func classifyTCP(port int, p []byte, up bool) string {
	if hasPrefix(p, "SSH-") {
		return AppSSH
	}
	if !up {
		switch {
		case hasPrefix(p, "220 ") || hasPrefix(p, "220-"):
			l := bytes.ToLower(p[:min(len(p), 200)])
			switch {
			case bytes.Contains(l, []byte("ftp")) || port == 21:
				return AppFTP
			case bytes.Contains(l, []byte("smtp")) || port == 25 || port == 465 || port == 587 || port == 2525:
				return AppSMTP
			}
		case hasPrefix(p, "+OK"):
			return AppPOP3
		case hasPrefix(p, "* OK"):
			return AppIMAP
		case hasPrefix(p, "RFB 00"):
			return AppVNC
		case len(p) >= 5 && p[3] == 0 && p[4] == 0x0a && int(p[0])|int(p[1])<<8|int(p[2])<<16 <= len(p)-4:
			return AppMySQL
		}
		return ""
	}
	switch {
	case len(p) >= 3 && p[0] == 0x16 && p[1] == 0x03 && p[2] <= 0x04:
		return AppTLS
	case anyPrefix(p, "GET ", "POST ", "HEAD ", "PUT ", "DELETE ", "OPTIONS ", "PATCH ", "CONNECT ", "TRACE "):
		return AppHTTP
	case hasPrefix(p, "PRI * HTTP/2.0"):
		return AppHTTP2
	case len(p) >= 11 && p[0] == 0x03 && p[1] == 0x00 && p[5] == 0xe0:
		return AppRDP
	case len(p) >= 20 && p[0] == 19 && string(p[1:20]) == "BitTorrent protocol":
		return AppBitTorrent
	case len(p) >= 8 && (string(p[4:8]) == "\xffSMB" || string(p[4:8]) == "\xfeSMB"):
		return AppSMB
	case len(p) >= 8 && (string(p[4:8]) == "\x00\x03\x00\x00" || string(p[4:8]) == "\x04\xd2\x16\x2f") &&
		int(p[0])<<24|int(p[1])<<16|int(p[2])<<8|int(p[3]) == len(p):
		return AppPostgres
	case len(p) >= 4 && p[0] == '*' && p[1] >= '1' && p[1] <= '9' && bytes.Contains(p[:min(len(p), 16)], []byte("\r\n$")):
		return AppRedis
	case len(p) >= 8 && p[0] == 0x10 && (bytes.Contains(p[:min(len(p), 16)], []byte("MQTT")) || bytes.Contains(p[:min(len(p), 16)], []byte("MQIsdp"))):
		return AppMQTT
	case anyPrefix(p, "<?xml", "<stream:stream"):
		return AppXMPP
	case len(p) >= 3 && p[0] == 5 && p[1] >= 1 && p[1] <= 10 && len(p) == 2+int(p[1]):
		return AppSOCKS5
	case port == 53 && len(p) >= 2 && be16(p) == len(p)-2 && dnsQuery(p[2:]):
		return AppDNS
	}
	return ""
}

func classifyUDP(port int, p []byte, up bool) string {
	switch {
	case len(p) >= 7 && p[0]&0xc0 == 0xc0 && quicVersion(p[1:5]):
		return AppQUIC
	case len(p) >= 20 && p[0]&0xc0 == 0 && string(p[4:8]) == "\x21\x12\xa4\x42":
		return AppSTUN
	case len(p) >= 13 && p[0] >= 0x14 && p[0] <= 0x17 && p[1] == 0xfe && (p[2] == 0xff || p[2] == 0xfd):
		return AppDTLS
	case len(p) >= 32 && p[0] >= 1 && p[0] <= 4 && p[1] == 0 && p[2] == 0 && p[3] == 0 &&
		(p[0] == 1 && len(p) == 148 || p[0] == 2 && len(p) == 92 || p[0] == 3 && len(p) == 64 || p[0] == 4 && len(p)%16 == 0):
		return AppWireGuard
	case anyPrefix(p, "M-SEARCH * HTTP/1.1", "NOTIFY * HTTP/1.1"):
		return AppSSDP
	case anyPrefix(p, "INVITE sip:", "REGISTER sip:", "OPTIONS sip:", "SUBSCRIBE sip:", "SIP/2.0 "):
		return AppSIP
	case len(p) >= 12 && p[0] == 'd' && anyPrefix(p, "d1:a", "d1:r", "d1:e", "d1:q", "d1:t") && bytes.Contains(p, []byte("1:y1:")):
		return AppBitTorrent
	case (port == 53 || port == 5353) && (dnsQuery(p) || !up && len(p) >= 12 && p[2]&0x80 != 0):
		if port == 5353 {
			return AppMDNS
		}
		return AppDNS
	case up && dnsQuery(p) && dnsName(p[12:]):
		return AppDNS
	case port == 123 && len(p) == 48 && (p[0]>>3)&7 >= 1 && (p[0]>>3)&7 <= 4:
		return AppNTP
	case (port == 500 || port == 4500) && len(p) >= 28:
		return AppIKE
	case up && len(p) >= 14 && (p[0]>>3 == 7 || p[0]>>3 == 10) && (port == 1194 || p[9] == 0):
		return AppOpenVPN
	}
	return ""
}

// quicVersion: QUIC v1, v2, drafts, or a version negotiation (0).
func quicVersion(v []byte) bool {
	switch string(v) {
	case "\x00\x00\x00\x01", "\x6b\x33\x43\xcf", "\x00\x00\x00\x00":
		return true
	}
	return v[0] == 0xff && v[1] == 0 && v[2] == 0
}

// dnsName: the question starts with a well-formed name (labels 1..63 of
// printable bytes, ending in the root), to tell DNS on other ports.
func dnsName(p []byte) bool {
	i := 0
	for n := 0; n < 128; n++ {
		if i >= len(p) {
			return false
		}
		l := int(p[i])
		if l == 0 {
			return i > 0 && i+5 <= len(p)
		}
		if l > 63 || i+1+l > len(p) {
			return false
		}
		for _, c := range p[i+1 : i+1+l] {
			if c < 0x21 || c > 0x7e {
				return false
			}
		}
		i += 1 + l
	}
	return false
}

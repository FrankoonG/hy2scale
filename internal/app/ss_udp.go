package app

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

// runSSUDP serves Shadowsocks UDP relay on the same address as the TCP
// listener (Listen). Each datagram is an independent AEAD-encrypted
// packet of shape:
//
//	[ salt (saltLen) ] [ AEAD-Seal( addr|payload, nonce=zero ) ] [ tag ]
//
// — distinct from TCP's stream framing. The salt is per-packet random and
// the AEAD nonce is fixed at zero (the salt provides freshness). The
// session subkey is derived per-packet with HKDF over (masterKey, salt,
// "ss-subkey"), matching the TCP path. This file does NOT implement the
// newer AEAD-2022 packet format; same cipher set the TCP path advertises
// (aes-128-gcm, aes-256-gcm, chacha20-ietf-poly1305).
//
// Per-flow lifecycle:
//   - Decrypt → parse SOCKS-style dst addr + payload.
//   - Identify the user by trial-decrypting against every enabled user's
//     key, same trial loop the TCP path uses. The first match locks the
//     (clientAddr → user) binding for the rest of this datagram.
//   - Open one upstream UDP conn per (clientAddr, user) via the user's
//     ExitVia / ExitPaths / ExitMode (mirrors the TCP routing exactly).
//     The conn is cached in `flows`, indexed by clientAddr. Subsequent
//     packets from the same clientAddr reuse it.
//   - A reader goroutine attached to each upstream conn re-encrypts every
//     reply with a FRESH salt + zero nonce, prepends the address header
//     that referenced the original target, and writes the response back
//     to clientAddr.
//   - Flows idle past udpFlowIdle are reaped. The reaper guards against
//     the original SS UDP design's "leaked socket per first-packet" bug
//     by tracking lastUsed under the same mutex that protects writes.
//
// Multi-hop / exit_via routing works because dialExitUDPPaths returns a
// net.Conn whose Write/Read traverse the relay chain transparently. The
// SS UDP layer therefore doesn't need to know whether the upstream is a
// direct UDP socket or a relay-tunnelled QUIC stream.

const udpFlowIdle = 90 * time.Second

type ssUDPFlow struct {
	upstream   net.Conn
	clientAddr *net.UDPAddr
	userIdx    int      // index into the enabled-users snapshot at session-creation time
	username   string   // for traffic accounting
	masterKey  []byte   // user's master key, captured so we don't have to rebuild per packet
	atyp       byte     // ATYP byte of the most recent dst (echoed back in replies)
	dstHost    string   // most recent dst hostname/IP literal (for reply addr header)
	dstPort    uint16
	mu         sync.Mutex
	lastUsed   time.Time
}

func (a *App) runSSUDP(ctx context.Context, listenAddr, method string) {
	if method == "" || method == "none" {
		// Original SS spec doesn't define a "none" UDP profile, and our TCP
		// "none" path just relays plaintext+addr header — not enough by
		// itself to support a UDP transport (no per-packet framing or
		// session demux). Skip when method is empty/none so operators don't
		// see misleading "udp listening" log lines.
		return
	}
	lAddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		log.Printf("[ss-udp] resolve %s: %v", listenAddr, err)
		return
	}
	conn, err := net.ListenUDP("udp", lAddr)
	if err != nil {
		log.Printf("[ss-udp] listen %s: %v", listenAddr, err)
		return
	}
	log.Printf("[ss-udp] server on %s (method=%s)", listenAddr, method)
	go func() { <-ctx.Done(); conn.Close() }()

	saltLen := keySize(method)
	probeAEAD, err := newAEAD(method, make([]byte, saltLen))
	if err != nil {
		log.Printf("[ss-udp] unsupported method: %s", method)
		conn.Close()
		return
	}
	overhead := probeAEAD.Overhead()
	zeroNonce := make([]byte, probeAEAD.NonceSize())

	var (
		flowsMu sync.Mutex
		flows   = map[string]*ssUDPFlow{}
	)

	// Idle reaper. Skip flows whose upstream is still nil (paranoia — this
	// implementation always sets upstream synchronously before the entry
	// lands in the map, so this branch shouldn't fire — but keeping the
	// guard tracks the same hardening we just landed in udp_transparent.go.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				flowsMu.Lock()
				for k, f := range flows {
					f.mu.Lock()
					stale := now.Sub(f.lastUsed) > udpFlowIdle
					f.mu.Unlock()
					if stale {
						if f.upstream != nil {
							f.upstream.Close()
						}
						delete(flows, k)
					}
				}
				flowsMu.Unlock()
			}
		}
	}()

	buf := make([]byte, 65535)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if n < saltLen+overhead {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		go a.handleSSUDPPacket(ctx, conn, src, pkt, method, saltLen, zeroNonce, &flowsMu, flows)
	}
}

// handleSSUDPPacket decrypts a single inbound packet and forwards it to
// the user's chosen exit. Runs in its own goroutine so a slow user-loop
// trial-decrypt (≤ users × few-μs AEAD opens) doesn't stall the read
// loop. The flow-creation path holds flowsMu only long enough to install
// the entry; the upstream dial then proceeds without the global lock.
func (a *App) handleSSUDPPacket(
	ctx context.Context,
	listener *net.UDPConn,
	src *net.UDPAddr,
	pkt []byte,
	method string,
	saltLen int,
	zeroNonce []byte,
	flowsMu *sync.Mutex,
	flows map[string]*ssUDPFlow,
) {
	cfg := a.store.Get()
	salt := pkt[:saltLen]
	ciphertext := pkt[saltLen:]
	clientKey := src.String()

	// Try existing flow first — same user binding sticks.
	flowsMu.Lock()
	existing := flows[clientKey]
	flowsMu.Unlock()
	if existing != nil {
		existing.mu.Lock()
		key := existing.masterKey
		existing.mu.Unlock()
		if dec, ok := ssUDPDecrypt(method, key, salt, ciphertext, zeroNonce); ok {
			a.ssUDPForward(ctx, listener, existing, dec, method, saltLen, zeroNonce)
			return
		}
		// Existing flow's key failed — fall through to re-trial. This is
		// rare (only when the operator changes the user's password
		// mid-session and the client keeps using the old salt-key combo);
		// the new trial below either rebinds to a different user or drops
		// the packet.
	}

	// Trial-decrypt against every eligible user.
	for _, u := range cfg.Users {
		if !u.Enabled || !u.IsProxyEnabled("ss") || a.IsPasswordConflicted(u.Username, "ss") {
			continue
		}
		key := evpBytesToKey(u.EffectivePassword("ss"), keySize(method))
		dec, ok := ssUDPDecrypt(method, key, salt, ciphertext, zeroNonce)
		if !ok {
			continue
		}
		// Plain payload = atype + addr + port + udp_data.
		host, port, atyp, payload, perr := parseSocksAddrUDP(dec)
		if perr != nil {
			return
		}
		dst := net.JoinHostPort(host, fmt.Sprintf("%d", port))

		// Create / refresh the flow under flowsMu so two concurrent first
		// packets from the same clientAddr don't race to dial.
		flowsMu.Lock()
		flow, ok := flows[clientKey]
		if !ok {
			// Set up the entry with the master key + addr-header BEFORE
			// dialing so the idle reaper can drop us cleanly if the dial
			// hangs (mirrors the nil-guard reaper hardening in
			// udp_transparent.go).
			flow = &ssUDPFlow{
				clientAddr: src,
				masterKey:  key,
				username:   u.Username,
				atyp:       atyp,
				dstHost:    host,
				dstPort:    port,
				lastUsed:   time.Now(),
			}
			flows[clientKey] = flow
		}
		flowsMu.Unlock()

		// Dial outside the lock. If a concurrent first-packet already won
		// the slot, this branch becomes the second-packet-on-existing
		// path: re-read the flow under flow.mu and skip re-dial.
		flow.mu.Lock()
		if flow.upstream == nil {
			var remote net.Conn
			var derr error
			if u.ExitVia == "" {
				remote, derr = net.DialTimeout("udp", dst, 5*time.Second)
			} else {
				remote, derr = a.dialExitUDPPaths(ctx, u.ExitVia, u.ExitPaths, u.ExitMode, dst)
			}
			if derr != nil {
				flow.mu.Unlock()
				flowsMu.Lock()
				if cur, ok := flows[clientKey]; ok && cur == flow {
					delete(flows, clientKey)
				}
				flowsMu.Unlock()
				return
			}
			flow.upstream = remote
			// Spawn the reverse-direction goroutine. It captures the
			// flow pointer so subsequent address-rotations within the
			// same flow (different dst addrs from same client) reflect
			// in the encoded reply header automatically.
			go a.ssUDPReverse(ctx, listener, flow, method, saltLen, zeroNonce)
		} else {
			// Existing flow: update dst-of-record for reply re-keying
			// when the client moves to a different upstream peer.
			flow.atyp = atyp
			flow.dstHost = host
			flow.dstPort = port
		}
		flow.lastUsed = time.Now()
		upstream := flow.upstream
		flow.mu.Unlock()

		if _, werr := upstream.Write(payload); werr != nil {
			// Drop on write failure — upstream conn may have died.
			// Don't tear the flow down here; the reverse-reader's next
			// Read will see the same error and trigger cleanup.
		}
		if u.Username != "" {
			a.RecordTraffic(u.Username, int64(len(payload)))
		}
		return
	}
	// No user matched. Could be a UDP scanner probe (silent reject ideal)
	// or a real client with a bad password / wrong cipher (operator wants
	// to see this). The TCP path resolves the same ambiguity by counting
	// `tried` vs `skippedConflict/Disabled` and logging once when zero
	// users were eligible — we don't yet plumb that diagnostic through
	// here. Drop on the floor for now; revisit if support traffic shows
	// "my client can't connect via UDP" cases that get swallowed.
}

// ssUDPForward is the "existing-flow trusted key path" — same plumbing
// as the user-trial path but skips the user lookup. Called only when an
// existing flow's master key successfully decrypts the incoming packet.
func (a *App) ssUDPForward(
	ctx context.Context,
	listener *net.UDPConn,
	flow *ssUDPFlow,
	plain []byte,
	method string,
	saltLen int,
	zeroNonce []byte,
) {
	host, port, atyp, payload, perr := parseSocksAddrUDP(plain)
	if perr != nil {
		return
	}
	flow.mu.Lock()
	flow.atyp = atyp
	flow.dstHost = host
	flow.dstPort = port
	flow.lastUsed = time.Now()
	upstream := flow.upstream
	username := flow.username
	flow.mu.Unlock()
	if upstream == nil {
		return
	}
	_, _ = upstream.Write(payload)
	if username != "" {
		a.RecordTraffic(username, int64(len(payload)))
	}
}

// ssUDPReverse reads replies from the upstream UDP conn and writes them
// back to the original SS client, freshly encrypted with a per-reply
// random salt. Lives one-per-flow until upstream Read errors or ctx
// cancels.
func (a *App) ssUDPReverse(
	ctx context.Context,
	listener *net.UDPConn,
	flow *ssUDPFlow,
	method string,
	saltLen int,
	zeroNonce []byte,
) {
	defer func() {
		flow.mu.Lock()
		if flow.upstream != nil {
			flow.upstream.Close()
		}
		flow.mu.Unlock()
	}()
	buf := make([]byte, 65535)
	for {
		if ctx.Err() != nil {
			return
		}
		flow.mu.Lock()
		up := flow.upstream
		flow.mu.Unlock()
		if up == nil {
			return
		}
		_ = up.SetReadDeadline(time.Now().Add(udpFlowIdle))
		n, err := up.Read(buf)
		if err != nil {
			return
		}
		flow.mu.Lock()
		atyp := flow.atyp
		host := flow.dstHost
		port := flow.dstPort
		clientAddr := flow.clientAddr
		key := flow.masterKey
		username := flow.username
		flow.lastUsed = time.Now()
		flow.mu.Unlock()

		// Build plaintext = atyp|addr|port|payload, then encrypt with a
		// fresh salt under the same master key.
		header := buildSocksAddrUDP(atyp, host, port)
		plain := make([]byte, 0, len(header)+n)
		plain = append(plain, header...)
		plain = append(plain, buf[:n]...)

		replySalt := make([]byte, saltLen)
		if _, err := rand.Read(replySalt); err != nil {
			return
		}
		sessionKey := make([]byte, saltLen)
		hkdfR := hkdf.New(sha1.New, key, replySalt, []byte("ss-subkey"))
		if _, err := hkdfR.Read(sessionKey); err != nil {
			return
		}
		aead, err := newAEAD(method, sessionKey)
		if err != nil {
			return
		}
		ct := aead.Seal(nil, zeroNonce, plain, nil)
		out := make([]byte, 0, len(replySalt)+len(ct))
		out = append(out, replySalt...)
		out = append(out, ct...)
		_, _ = listener.WriteToUDP(out, clientAddr)
		if username != "" {
			a.RecordTraffic(username, int64(n))
		}
	}
}

// ssUDPDecrypt opens a single AEAD packet under master key + per-packet salt.
// Returns the decrypted plaintext or (nil,false) on AEAD failure.
func ssUDPDecrypt(method string, masterKey, salt, ciphertext, zeroNonce []byte) ([]byte, bool) {
	sessionKey := make([]byte, len(masterKey))
	hkdfR := hkdf.New(sha1.New, masterKey, salt, []byte("ss-subkey"))
	if _, err := hkdfR.Read(sessionKey); err != nil {
		return nil, false
	}
	aead, err := newAEAD(method, sessionKey)
	if err != nil {
		return nil, false
	}
	plain, err := openAEAD(aead, zeroNonce, ciphertext)
	if err != nil {
		return nil, false
	}
	return plain, true
}

// openAEAD wraps Open with a defensive bounds check — Open may modify the
// ciphertext buffer in place. We pass a copy via the destination slice so
// the caller's pkt isn't clobbered (this matters for the existing-flow
// retry path that may re-trial across user keys).
func openAEAD(aead cipher.AEAD, nonce, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < aead.Overhead() {
		return nil, fmt.Errorf("short ciphertext")
	}
	dst := make([]byte, len(ciphertext)-aead.Overhead())
	return aead.Open(dst[:0], nonce, ciphertext, nil)
}

// parseSocksAddrUDP unpacks the SOCKS5-style address header used in the
// SS UDP packet (and SS UDP reply) framing. Returns host (literal/string),
// port, atyp, and the residual payload after the header.
func parseSocksAddrUDP(p []byte) (host string, port uint16, atyp byte, payload []byte, err error) {
	if len(p) < 1 {
		return "", 0, 0, nil, fmt.Errorf("empty")
	}
	atyp = p[0]
	switch atyp {
	case 0x01: // IPv4
		if len(p) < 1+4+2 {
			return "", 0, 0, nil, fmt.Errorf("short ipv4")
		}
		host = net.IP(p[1:5]).String()
		port = uint16(p[5])<<8 | uint16(p[6])
		payload = p[7:]
	case 0x03: // domain
		if len(p) < 2 {
			return "", 0, 0, nil, fmt.Errorf("short domain")
		}
		dlen := int(p[1])
		if len(p) < 2+dlen+2 {
			return "", 0, 0, nil, fmt.Errorf("short domain payload")
		}
		host = string(p[2 : 2+dlen])
		port = uint16(p[2+dlen])<<8 | uint16(p[2+dlen+1])
		payload = p[2+dlen+2:]
	case 0x04: // IPv6
		if len(p) < 1+16+2 {
			return "", 0, 0, nil, fmt.Errorf("short ipv6")
		}
		host = net.IP(p[1:17]).String()
		port = uint16(p[17])<<8 | uint16(p[18])
		payload = p[19:]
	default:
		return "", 0, 0, nil, fmt.Errorf("unknown atyp %d", atyp)
	}
	return
}

// buildSocksAddrUDP is the inverse of parseSocksAddrUDP — used to prepend
// the address header to UDP replies before encryption.
func buildSocksAddrUDP(atyp byte, host string, port uint16) []byte {
	var out bytes.Buffer
	switch atyp {
	case 0x01:
		ip := net.ParseIP(host).To4()
		if ip == nil {
			ip = []byte{0, 0, 0, 0}
		}
		out.WriteByte(0x01)
		out.Write(ip)
	case 0x03:
		// Domain replies are uncommon (most UDP applications work in IP
		// terms only), but we preserve atyp parity so a client that sent
		// us a domain gets a domain back.
		dn := host
		if len(dn) > 255 {
			dn = dn[:255]
		}
		out.WriteByte(0x03)
		out.WriteByte(byte(len(dn)))
		out.WriteString(dn)
	case 0x04:
		ip := net.ParseIP(host).To16()
		if ip == nil {
			ip = make([]byte, 16)
		}
		out.WriteByte(0x04)
		out.Write(ip)
	default:
		// Fallback to IPv4 zero so the wire format is always parseable.
		out.WriteByte(0x01)
		out.Write([]byte{0, 0, 0, 0})
	}
	out.WriteByte(byte(port >> 8))
	out.WriteByte(byte(port))
	return out.Bytes()
}

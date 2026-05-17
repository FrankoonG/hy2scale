package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// SSConfig holds Shadowsocks server configuration.
type SSConfig struct {
	Listen  string `yaml:"listen" json:"listen"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Method  string `yaml:"method" json:"method"` // aes-128-gcm, aes-256-gcm, chacha20-ietf-poly1305
}

// StartSS starts the Shadowsocks server (TCP + UDP on the same address).
//
// UDP is paired with TCP for two reasons: (1) the SS protocol clients
// universally assume `server:port` is reachable on both transports — a
// mihomo / sing-box / ss-libev client configured with port 443 expects
// UDP datagrams to land on UDP/443 too — and (2) the user/auth model is
// per-key, not per-transport, so binding both ports under one
// StartSS/RestartSS lifecycle avoids the inconsistency of "user enabled
// for TCP but not UDP." Methods that don't support UDP framing (currently
// just "none") opt out inside runSSUDP and the UDP listener is skipped.
func (a *App) StartSS(cfg SSConfig) {
	if !cfg.Enabled || cfg.Listen == "" {
		return
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Printf("[ss] listen error: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(a.appCtx)
	a.ssListener = ln
	a.ssCancel = cancel
	log.Printf("[ss] server on %s (method=%s)", cfg.Listen, cfg.Method)
	go func() { <-ctx.Done(); ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go a.handleSS(conn, cfg.Method)
		}
	}()
	go a.runSSUDP(ctx, cfg.Listen, cfg.Method)
}

// RestartSS stops and restarts the SS server with current config.
func (a *App) RestartSS() {
	if a.ssCancel != nil {
		a.ssCancel()
	}
	if a.ssListener != nil {
		a.ssListener.Close()
		a.ssListener = nil
	}
	time.Sleep(200 * time.Millisecond)
	cfg := a.store.Get()
	if cfg.SS != nil {
		a.StartSS(*cfg.SS)
	}
}

func (a *App) handleSS(conn net.Conn, method string) {
	defer conn.Close()

	// "none" method: no encryption, just relay with address header
	if method == "none" {
		a.handleSSNone(conn)
		return
	}

	// Read salt and the first length-encrypted frame ONCE from the wire.
	// Salt length and AEAD overhead are determined by the method, not by the
	// user, so these bytes are the same for every candidate user. Buffering
	// them lets us retry each enabled user's key against the same bytes —
	// before rc10's fix, the loop below consumed the salt on the first
	// iteration and could never try a second user's key, effectively
	// locking out every user except the first enabled one.
	cfg := a.store.Get()
	saltLen := keySize(method)
	probeKey := make([]byte, saltLen)
	probeAEAD, err := newAEAD(method, probeKey)
	if err != nil {
		log.Printf("[ss] unsupported method: %s", method)
		return
	}
	nonceSize := probeAEAD.NonceSize()
	overhead := probeAEAD.Overhead()

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(conn, salt); err != nil {
		return
	}
	lenBuf := make([]byte, 2+overhead)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return
	}

	// Try each user's key against the buffered salt + first frame.
	// Diagnostic counters so a "no user matched" outcome can distinguish
	// "every user got skipped by policy" (config issue, log loud) from
	// "AEAD trial-decrypt failed" (bad password / wrong cipher, normal
	// quiet rejection).
	skippedConflict := 0
	skippedDisabled := 0
	tried := 0
	for _, u := range cfg.Users {
		if !u.Enabled {
			continue
		}
		// Per-proxy disable: admin UI can revoke a user from ss only.
		if !u.IsProxyEnabled("ss") {
			skippedDisabled++
			continue
		}
		// Skip if this user's SS password conflicts with another user.
		// We log this case at end-of-loop so the operator sees a single
		// clear line per failed connection rather than silence.
		if a.IsPasswordConflicted(u.Username, "ss") {
			skippedConflict++
			continue
		}
		tried++
		key := evpBytesToKey(u.EffectivePassword("ss"), keySize(method))

		// Derive session key from this user's master key + connection salt
		sessionKey := make([]byte, keySize(method))
		hkdfReader := hkdf.New(sha1.New, key, salt, []byte("ss-subkey"))
		hkdfReader.Read(sessionKey)
		sessionAEAD, err := newAEAD(method, sessionKey)
		if err != nil {
			continue
		}

		// AEAD.Open may modify the ciphertext buffer (docs allow it), so we
		// must feed it a copy — otherwise a failed first attempt would
		// corrupt the bytes and the next user's attempt would see garbage.
		nonce := make([]byte, nonceSize)
		lenBufCopy := make([]byte, len(lenBuf))
		copy(lenBufCopy, lenBuf)
		plainLen, err := sessionAEAD.Open(lenBufCopy[:0], nonce, lenBufCopy, nil)
		if err != nil {
			continue
		}
		increment(nonce)

		// This user's key decrypted the first frame — lock it in.
		payloadLen := int(binary.BigEndian.Uint16(plainLen))
		payload := make([]byte, payloadLen+overhead)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
		plain, err := sessionAEAD.Open(payload[:0], nonce, payload, nil)
		if err != nil {
			return
		}
		increment(nonce)

		// Parse SOCKS-like address from payload
		addr, remaining, err := parseSocksAddr(plain)
		if err != nil {
			return
		}

		// DNS-pollution recovery: when the client supplied an IPv4/IPv6
		// (its resolver was poisoned, the IP it sent us is fake), peek
		// at the actual application payload for a TLS Client Hello SNI
		// or HTTP Host header. The hostname-ATYP path is left alone —
		// sniffOverrideHost no-ops when host is already a name. The
		// peeked bytes are forwarded verbatim below so the dialed server
		// sees the original packet untouched. Docs: dns-pollution-real-world.md
		//
		// Why we may need to read another AEAD frame here: mihomo and
		// most SS clients send `address` in the FIRST encrypted frame
		// and the client's first data byte in the SECOND frame. If we
		// only sniff `remaining` (residual from frame 1) it's typically
		// empty and we miss the Client Hello. Read one more frame when
		// the parsed address is IP-form and we have <32 bytes of
		// residual. The extra frame is buffered and forwarded together
		// with `remaining` once the upstream is dialed.
		if host, _, splitErr := net.SplitHostPort(addr); splitErr == nil && net.ParseIP(host) != nil {
			if len(remaining) < 32 {
				if extra, err2 := readSSFrame(conn, sessionAEAD, nonce, overhead); err2 == nil && len(extra) > 0 {
					remaining = append(remaining, extra...)
				}
			}
			addr = sniffOverrideHost(addr, remaining)
		}

		// User identified! Route via their exit_via
		exitVia := u.ExitVia
		exitMode := u.ExitMode
		exitPaths := u.ExitPaths
		username := u.Username

		var remote net.Conn
		if exitVia == "" {
			remote, err = net.DialTimeout("tcp", addr, 10*time.Second)
		} else {
			remote, err = a.dialExitWithPaths(a.appCtx, exitVia, exitPaths, exitMode, addr)
		}
		if err != nil {
			return
		}
		defer remote.Close()

		// Send remaining data
		if len(remaining) > 0 {
			remote.Write(remaining)
		}

		// Bidirectional relay with traffic counting
		var up, down int64
		done := make(chan struct{})

		// remote → client (encrypt)
		go func() {
			buf := make([]byte, 16384)
			writeNonce := make([]byte, sessionAEAD.NonceSize())
			// Generate write salt
			writeSalt := make([]byte, saltLen)
			rand.Read(writeSalt)
			// Derive write session key
			writeKey := make([]byte, keySize(method))
			wr := hkdf.New(sha1.New, key, writeSalt, []byte("ss-subkey"))
			wr.Read(writeKey)
			writeAEAD, _ := newAEAD(method, writeKey)
			conn.Write(writeSalt)
			for {
				n, err := remote.Read(buf)
				if n > 0 {
					// Encrypt length
					var lb [2]byte
					binary.BigEndian.PutUint16(lb[:], uint16(n))
					encLen := writeAEAD.Seal(nil, writeNonce, lb[:], nil)
					increment(writeNonce)
					// Encrypt payload
					encPayload := writeAEAD.Seal(nil, writeNonce, buf[:n], nil)
					increment(writeNonce)
					conn.Write(encLen)
					conn.Write(encPayload)
					atomic.AddInt64(&down, int64(n))
				}
				if err != nil {
					break
				}
			}
			done <- struct{}{}
		}()

		// client → remote (decrypt) — continue reading from existing stream
		readBuf := make([]byte, 16384)
		for {
			// Read encrypted length
			lb := make([]byte, 2+sessionAEAD.Overhead())
			if _, err := io.ReadFull(conn, lb); err != nil {
				break
			}
			plainLenBuf, err := sessionAEAD.Open(lb[:0], nonce, lb, nil)
			if err != nil {
				break
			}
			increment(nonce)
			pLen := int(binary.BigEndian.Uint16(plainLenBuf))
			if pLen > len(readBuf)+sessionAEAD.Overhead() {
				break
			}
			encData := make([]byte, pLen+sessionAEAD.Overhead())
			if _, err := io.ReadFull(conn, encData); err != nil {
				break
			}
			plainData, err := sessionAEAD.Open(encData[:0], nonce, encData, nil)
			if err != nil {
				break
			}
			increment(nonce)
			remote.Write(plainData)
			atomic.AddInt64(&up, int64(len(plainData)))
		}
		<-done
		if username != "" {
			a.RecordTraffic(username, atomic.LoadInt64(&up)+atomic.LoadInt64(&down))
		}
		return
	}
	// User-loop exited with no match — explain WHY (silent rejection
	// otherwise looks like a network glitch to the operator). Heuristic:
	// "all users skipped by policy" vs "AEAD decrypt failed for every
	// tried user". The first one is a misconfig the operator can fix.
	if tried == 0 && skippedConflict > 0 {
		log.Printf("[ss] no eligible user: every enabled user is in a password-conflict group (%d skipped). Set a per-user proxy_passwords.ss override to unblock SS auth.", skippedConflict)
	} else if tried == 0 && skippedDisabled > 0 {
		log.Printf("[ss] no eligible user: every enabled user has SS disabled (%d skipped via per-user proxy_disabled toggle).", skippedDisabled)
	}
}

func (a *App) handleSSNone(conn net.Conn) {
	// No encryption — read address header directly
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil || n < 2 {
		return
	}
	addr, remaining, err := parseSocksAddr(buf[:n])
	if err != nil {
		return
	}

	// No user identification possible with "none" — use first enabled user or direct
	cfg := a.store.Get()
	var exitVia, username string
	exitMode := ""
	var exitPaths []string
	for _, u := range cfg.Users {
		if u.Enabled {
			exitVia = u.ExitVia
			exitMode = u.ExitMode
			exitPaths = u.ExitPaths
			username = u.Username
			break
		}
	}

	var remote net.Conn
	if exitVia == "" {
		remote, err = net.DialTimeout("tcp", addr, 10*time.Second)
	} else {
		remote, err = a.dialExitWithPaths(a.appCtx, exitVia, exitPaths, exitMode, addr)
	}
	if err != nil {
		return
	}
	defer remote.Close()

	if len(remaining) > 0 {
		remote.Write(remaining)
	}

	var up, down int64
	done := make(chan struct{})
	go func() {
		// Upload: client → remote.
		n, _ := io.Copy(remote, conn)
		atomic.AddInt64(&up, n)
		// Forward client FIN to remote so the exit can finish writing
		// any final response and close. Without this the function
		// would hang on the download direction's io.Copy when the
		// client hangs up but the server has nothing more to send.
		halfCloseAndBound(remote)
		done <- struct{}{}
	}()
	// Download: remote → client.
	n2, _ := io.Copy(conn, remote)
	atomic.AddInt64(&down, n2)
	halfCloseAndBound(conn)
	<-done
	if username != "" {
		a.RecordTraffic(username, atomic.LoadInt64(&up)+atomic.LoadInt64(&down))
	}
}

func keySize(method string) int {
	switch method {
	case "aes-128-gcm", "2022-blake3-aes-128-gcm":
		return 16
	case "aes-256-gcm", "2022-blake3-aes-256-gcm":
		return 32
	case "chacha20-ietf-poly1305":
		return 32
	default:
		return 32
	}
}

func newAEAD(method string, key []byte) (cipher.AEAD, error) {
	switch method {
	case "aes-128-gcm", "aes-256-gcm":
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(block)
	case "chacha20-ietf-poly1305":
		return chacha20poly1305.New(key)
	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
}

func evpBytesToKey(password string, keyLen int) []byte {
	var b, prev []byte
	h := md5.New()
	for len(b) < keyLen {
		h.Reset()
		h.Write(prev)
		h.Write([]byte(password))
		prev = h.Sum(nil)
		b = append(b, prev...)
	}
	return b[:keyLen]
}

func increment(nonce []byte) {
	for i := range nonce {
		nonce[i]++
		if nonce[i] != 0 {
			break
		}
	}
}

func parseSocksAddr(buf []byte) (string, []byte, error) {
	if len(buf) < 2 {
		return "", nil, fmt.Errorf("too short")
	}
	var addr string
	var pos int
	switch buf[0] {
	case 0x01: // IPv4
		if len(buf) < 7 {
			return "", nil, fmt.Errorf("too short for ipv4")
		}
		addr = fmt.Sprintf("%d.%d.%d.%d:%d", buf[1], buf[2], buf[3], buf[4], int(buf[5])<<8|int(buf[6]))
		pos = 7
	case 0x03: // Domain
		dl := int(buf[1])
		if len(buf) < 2+dl+2 {
			return "", nil, fmt.Errorf("too short for domain")
		}
		addr = fmt.Sprintf("%s:%d", buf[2:2+dl], int(buf[2+dl])<<8|int(buf[2+dl+1]))
		pos = 2 + dl + 2
	case 0x04: // IPv6
		if len(buf) < 19 {
			return "", nil, fmt.Errorf("too short for ipv6")
		}
		addr = fmt.Sprintf("[%s]:%d", net.IP(buf[1:17]), int(buf[17])<<8|int(buf[18]))
		pos = 19
	default:
		return "", nil, fmt.Errorf("unknown addr type: %d", buf[0])
	}
	return addr, buf[pos:], nil
}

// readSSFrame reads ONE Shadowsocks AEAD frame from conn, advancing the
// caller-supplied nonce in place. Used only on the DNS-sniff path when
// the address-only first frame had no application-layer bytes to peek;
// we pull one more frame so we have the client's TLS Client Hello /
// HTTP request line. A 300 ms read deadline keeps us from stalling on
// server-speaks-first protocols (SMTP/FTP/SSH) — in that case we return
// what we have (possibly nil) and the sniffer no-ops; the dial then
// proceeds with the original IP, preserving legacy behaviour.
//
// Caller is responsible for forwarding the returned bytes to the
// upstream after dialing — exactly the same way the legacy `remaining`
// slice from parseSocksAddr is forwarded.
func readSSFrame(conn net.Conn, aead cipher.AEAD, nonce []byte, overhead int) ([]byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	defer conn.SetReadDeadline(time.Time{})

	lenBuf := make([]byte, 2+overhead)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, err
	}
	plainLen, err := aead.Open(lenBuf[:0], nonce, lenBuf, nil)
	if err != nil {
		return nil, err
	}
	increment(nonce)

	payloadLen := int(binary.BigEndian.Uint16(plainLen))
	if payloadLen == 0 || payloadLen > 16384 {
		return nil, fmt.Errorf("bad ss frame length %d", payloadLen)
	}
	encPayload := make([]byte, payloadLen+overhead)
	if _, err := io.ReadFull(conn, encPayload); err != nil {
		return nil, err
	}
	plain, err := aead.Open(encPayload[:0], nonce, encPayload, nil)
	if err != nil {
		return nil, err
	}
	increment(nonce)
	return plain, nil
}

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

// StartSS starts the Shadowsocks server.
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
	for _, u := range cfg.Users {
		if !u.Enabled {
			continue
		}
		// Skip if this user's SS password conflicts with another user
		if a.IsPasswordConflicted(u.Username, "ss") {
			continue
		}
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

package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/policy"
)

// TrojanConfig holds the Trojan server configuration.
//
// Trojan (https://trojan-gfw.github.io/trojan/protocol) runs over TLS:
//
//	hex(SHA224(password)) CRLF CMD ATYP DST.ADDR DST.PORT CRLF payload
//
// CMD 1 is CONNECT (the rest of the stream is the TCP payload); CMD 3 is
// UDP ASSOCIATE: the rest of the stream is a sequence of datagrams, each
// ATYP DST.ADDR DST.PORT LENGTH CRLF PAYLOAD, in both directions.
//
// Users are identified by their password (per-proxy override "trojan"),
// as Shadowsocks does. A stream that does not start with a known user's
// hash goes to Fallback when one is set (e.g. a local web server), so the
// port answers like an ordinary TLS service; otherwise it is closed.
type TrojanConfig struct {
	Listen  string `yaml:"listen" json:"listen"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
	// CertID: TLS certificate from the node's store. Empty = the store's
	// "default" certificate.
	CertID string `yaml:"cert_id,omitempty" json:"cert_id"`
	// Fallback: host:port that streams failing authentication are passed
	// to unchanged (decrypted). Empty = close them.
	Fallback string `yaml:"fallback,omitempty" json:"fallback"`
}

const (
	trojanHashLen       = 56 // hex(SHA224)
	trojanHandshakeWait = 10 * time.Second
	trojanMaxUDPFlows   = 64 // destinations kept open per UDP stream
)

type trojanServer struct {
	ln     net.Listener
	cancel context.CancelFunc
}

// StartTrojan starts the Trojan server (TCP; UDP rides inside the streams).
func (a *App) StartTrojan(cfg TrojanConfig) {
	if !cfg.Enabled || cfg.Listen == "" {
		return
	}
	tlsCfg, err := a.trojanTLS(cfg.CertID)
	if err != nil {
		log.Printf("[trojan] TLS certificate %q: %v", cfg.CertID, err)
		return
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Printf("[trojan] listen error: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(a.appCtx)
	a.trojanMu.Lock()
	a.trojan = &trojanServer{ln: ln, cancel: cancel}
	a.trojanMu.Unlock()
	log.Printf("[trojan] server on %s (fallback=%q)", cfg.Listen, cfg.Fallback)
	go func() { <-ctx.Done(); ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go a.handleTrojan(ctx, tls.Server(c, tlsCfg), cfg.Fallback)
		}
	}()
}

// StopTrojan stops the Trojan server; open streams keep running.
func (a *App) StopTrojan() {
	a.trojanMu.Lock()
	s := a.trojan
	a.trojan = nil
	a.trojanMu.Unlock()
	if s != nil {
		s.cancel()
		s.ln.Close()
	}
}

// RestartTrojan restarts the Trojan server with the current config.
func (a *App) RestartTrojan() {
	a.trojanRestartMu.Lock()
	defer a.trojanRestartMu.Unlock()
	a.StopTrojan()
	time.Sleep(200 * time.Millisecond)
	if cfg := a.store.Get(); cfg.Trojan != nil {
		a.StartTrojan(*cfg.Trojan)
	}
}

// TrojanDefaultCert is the certificate a Trojan config without one uses.
const TrojanDefaultCert = "default"

// trojanTLS: the server TLS config for a certificate of the store (the
// "default" one when id is empty).
func (a *App) trojanTLS(id string) (*tls.Config, error) {
	if id == "" {
		id = TrojanDefaultCert
	}
	c, err := a.tls.TLSConfig(id)
	if err != nil {
		return nil, err
	}
	c.MinVersion = tls.VersionTLS12
	return c, nil
}

func trojanHash(password string) string {
	h := sha256.Sum224([]byte(password))
	return hex.EncodeToString(h[:])
}

func isHexLower(b byte) bool { return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') }

// trojanUser finds the eligible user whose trojan password hashes to hash.
func (a *App) trojanUser(hash []byte) (*UserConfig, bool) {
	cfg := a.store.Get()
	var found *UserConfig
	skipped := 0
	for i := range cfg.Users {
		u := &cfg.Users[i]
		if !u.Enabled {
			continue
		}
		match := subtle.ConstantTimeCompare([]byte(trojanHash(u.EffectivePassword("trojan"))), hash) == 1
		if !match {
			continue
		}
		if !u.IsProxyEnabled("trojan") || a.IsPasswordConflicted(u.Username, "trojan") {
			skipped++
			continue
		}
		if found == nil {
			found = u
		}
	}
	if found == nil && skipped > 0 {
		log.Printf("[trojan] password matches %d user(s) with trojan disabled or in a password conflict", skipped)
	}
	return found, found != nil
}

// trojanConn is the client stream after the handshake: reads come from
// what was buffered first, then the TLS connection.
type trojanConn struct {
	*tls.Conn
	r io.Reader
}

func (c *trojanConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *trojanConn) Unwrap() net.Conn { return c.Conn }

// withPrefix: the stream with prefix to be read again first.
func (c *trojanConn) withPrefix(prefix []byte) *trojanConn {
	if len(prefix) == 0 {
		return c
	}
	return &trojanConn{Conn: c.Conn, r: io.MultiReader(bytes.NewReader(append([]byte(nil), prefix...)), c.r)}
}

func (a *App) handleTrojan(ctx context.Context, conn *tls.Conn, fallback string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(trojanHandshakeWait))
	if err := conn.HandshakeContext(ctx); err != nil {
		return
	}

	// Read the password hash and its CRLF, stopping at the first byte that
	// cannot belong to it: anything else goes to the fallback with exactly
	// the bytes read so far.
	head := make([]byte, 0, trojanHashLen+2)
	buf := make([]byte, trojanHashLen+2)
	bad := false
	for len(head) < trojanHashLen+2 && !bad {
		n, err := conn.Read(buf[:trojanHashLen+2-len(head)])
		head = append(head, buf[:n]...)
		for i := len(head) - n; i < len(head); i++ {
			if (i < trojanHashLen && !isHexLower(head[i])) ||
				(i == trojanHashLen && head[i] != '\r') || (i == trojanHashLen+1 && head[i] != '\n') {
				bad = true
				break
			}
		}
		if err != nil {
			if len(head) == 0 {
				return
			}
			bad = true
		}
	}
	var u *UserConfig
	if !bad {
		u, _ = a.trojanUser(head[:trojanHashLen])
	}
	if u == nil {
		a.trojanFallback(conn, head, fallback)
		return
	}
	if userExpired(u) || a.overTrafficLimit(u.Username) {
		log.Printf("[trojan] %s refused: expired or over its traffic limit", u.Username)
		return
	}

	br := bufio.NewReader(conn)
	var hdr [1]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return
	}
	cmd := hdr[0]
	host, port, _, err := readSocksAddr(br)
	if err != nil {
		return
	}
	var crlf [2]byte
	if _, err := io.ReadFull(br, crlf[:]); err != nil || crlf != [2]byte{'\r', '\n'} {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	tc := &trojanConn{Conn: conn, r: br}
	switch cmd {
	case 0x01:
		a.trojanConnect(tc, u, net.JoinHostPort(host, strconv.Itoa(int(port))))
	case 0x03:
		a.trojanUDP(ctx, tc, u)
	}
}

// trojanFallback passes a stream that failed authentication to the
// fallback address, starting with the bytes already read.
func (a *App) trojanFallback(conn *tls.Conn, head []byte, fallback string) {
	if fallback == "" {
		return
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	remote, err := d.Dial("tcp", fallback)
	if err != nil {
		log.Printf("[trojan] fallback %s: %v", fallback, err)
		return
	}
	defer remote.Close()
	_ = conn.SetDeadline(time.Time{})
	if len(head) > 0 {
		if _, err := remote.Write(head); err != nil {
			return
		}
	}
	done := make(chan struct{})
	go func() {
		io.Copy(remote, conn)
		halfCloseAndBound(remote)
		close(done)
	}()
	io.Copy(conn, remote)
	halfCloseAndBound(conn)
	<-done
}

func (a *App) trojanConnect(conn *trojanConn, u *UserConfig, addr string) {
	username := u.Username
	src := conn.RemoteAddr()
	rt := a.route(userSel(u), a.userSrc(policy.KindTrojan, u, src), "tcp", addr)
	af := beginUserFlow(audit.InTrojan, "tcp", username, src, addr)
	dctx := audit.NewContext(withRestrict(a.appCtx, a.userRestrict(username)), af)
	var (
		remote net.Conn
		err    error
	)
	if wantSniff(addr) {
		// IP destination: recover the host name from the client's hello
		// (sniff_override.go); the peeked bytes are read again by the relay,
		// so the meter counts them.
		peeked, waited := peekHello(conn, nil)
		remote, err = a.dialSniffedTCP(dctx, rt, addr, peeked, waited, isLoopbackAddr(src))
		conn = conn.withPrefix(peeked)
	} else {
		remote, err = rt.dial(dctx)
	}
	if err != nil {
		af.End(err)
		denyClose(conn, err)
		return
	}
	defer remote.Close()
	a.relayUser(conn, remote, username, hostOf(src), "trojan", true, af)
}

func hostOf(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	if h, _, err := net.SplitHostPort(addr.String()); err == nil {
		return h
	}
	return addr.String()
}

// trojanUDPFlow: one destination of a UDP stream.
type trojanUDPFlow struct {
	up   net.Conn
	atyp byte
	host string
	port uint16
	af   *audit.Flow
}

// trojanUDP serves a UDP ASSOCIATE stream: each datagram names its
// destination; one routed UDP flow is kept per destination and its replies
// are framed back onto the stream. Bytes go through the user meter.
func (a *App) trojanUDP(ctx context.Context, conn *trojanConn, u *UserConfig) {
	username := u.Username
	src := conn.RemoteAddr()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := func() { cancel(); conn.Close() }
	sid := a.Sessions.Connect(username, hostOf(src), "trojan", stop)
	m := a.newUserMeter(username, sid, stop)
	defer func() {
		a.Sessions.Disconnect(sid, 0, 0)
		m.done()
	}()

	var (
		mu    sync.Mutex
		wmu   sync.Mutex // frames written back to the client
		flows = map[string]*trojanUDPFlow{}
		wg    sync.WaitGroup
	)
	defer func() {
		conn.Close() // unblocks replies stuck writing to a client that stopped reading
		mu.Lock()
		for _, f := range flows {
			f.up.Close()
		}
		mu.Unlock()
		wg.Wait()
	}()

	reverse := func(key string, f *trojanUDPFlow) {
		defer wg.Done()
		defer func() {
			f.up.Close()
			mu.Lock()
			if flows[key] == f {
				delete(flows, key)
			}
			mu.Unlock()
			f.af.End(nil)
		}()
		header := buildSocksAddrUDP(f.atyp, f.host, f.port)
		b := make([]byte, 65535)
		for {
			_ = f.up.SetReadDeadline(time.Now().Add(udpFlowIdle))
			n, err := f.up.Read(b)
			if err != nil {
				return
			}
			out := make([]byte, 0, len(header)+4+n)
			out = append(out, header...)
			out = binary.BigEndian.AppendUint16(out, uint16(n))
			out = append(out, '\r', '\n')
			out = append(out, b[:n]...)
			wmu.Lock()
			_, werr := conn.Write(out)
			wmu.Unlock()
			if werr != nil {
				return
			}
			m.add(0, int64(n))
			f.af.Add(0, int64(n))
		}
	}

	payload := make([]byte, 65535)
	for {
		host, port, atyp, err := readSocksAddr(conn.r)
		if err != nil {
			return
		}
		var lb [4]byte
		if _, err := io.ReadFull(conn.r, lb[:]); err != nil || lb[2] != '\r' || lb[3] != '\n' {
			return
		}
		n := int(binary.BigEndian.Uint16(lb[:2]))
		if _, err := io.ReadFull(conn.r, payload[:n]); err != nil {
			return
		}
		if sctx.Err() != nil {
			return
		}
		dst := net.JoinHostPort(host, strconv.Itoa(int(port)))
		mu.Lock()
		f := flows[dst]
		full := f == nil && len(flows) >= trojanMaxUDPFlows
		mu.Unlock()
		if full {
			continue // too many destinations at once: drop
		}
		if f == nil {
			af := beginUserFlow(audit.InTrojan, "udp", username, src, dst)
			dctx := audit.NewContext(withRestrict(sctx, a.userRestrict(username)), af)
			up, derr := a.dialRoutedUDP(dctx, userSel(u), a.userSrc(policy.KindTrojan, u, src), dst)
			if derr != nil {
				af.End(derr)
				continue
			}
			f = &trojanUDPFlow{up: up, atyp: atyp, host: host, port: port, af: af}
			mu.Lock()
			flows[dst] = f
			mu.Unlock()
			wg.Add(1)
			go reverse(dst, f)
		}
		if _, err := f.up.Write(payload[:n]); err == nil {
			m.add(int64(n), 0)
			f.af.Add(int64(n), 0)
		}
	}
}

// readSocksAddr reads a SOCKS5-style address (ATYP, address, port).
func readSocksAddr(r io.Reader) (host string, port uint16, atyp byte, err error) {
	var b [1]byte
	if _, err = io.ReadFull(r, b[:]); err != nil {
		return
	}
	atyp = b[0]
	switch atyp {
	case 0x01:
		var ip [4]byte
		if _, err = io.ReadFull(r, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 0x04:
		var ip [16]byte
		if _, err = io.ReadFull(r, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 0x03:
		if _, err = io.ReadFull(r, b[:]); err != nil {
			return
		}
		name := make([]byte, int(b[0]))
		if _, err = io.ReadFull(r, name); err != nil {
			return
		}
		host = string(name)
	default:
		err = fmt.Errorf("unknown address type %d", atyp)
		return
	}
	var p [2]byte
	if _, err = io.ReadFull(r, p[:]); err != nil {
		return
	}
	port = binary.BigEndian.Uint16(p[:])
	return
}

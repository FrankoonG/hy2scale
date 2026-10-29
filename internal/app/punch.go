package app

// Hole punching: a direct connection to a node that is only reachable
// through the mesh (both ends possibly behind NAT), set up with help from
// nodes already connected to both.
//
//  1. The initiator learns its own NAT mapping: from a probe socket bound to
//     local port p it completes a QUIC handshake to one of its outbound
//     peers (the "reflector"), authenticating with a one-time probe token
//     the reflector issued over the mesh. The reflector records the source
//     address its Authenticate callback sees, refuses the login, and reports
//     the address back over the mesh. Two reflectors with different ports
//     mean the mapping depends on the destination (symmetric NAT): give up,
//     no port prediction.
//  2. The initiator sends a punch request along a signalling path (a mesh
//     path ending at the target). The target does the same probing from its
//     own probe port q towards the reflectors named in the request (its
//     outbound peers), opens a dedicated hy2 server on port q that accepts
//     a single one-time token, and starts sending punch packets to the
//     initiator's mapping. It answers with its mapping and the token.
//  3. The initiator binds port p again, sends punch packets to the target's
//     mapping and dials QUIC from it with the token as password. With
//     endpoint-independent NATs both mappings are what the reflectors saw,
//     and the crossing packets open both filters.
//
// Probing never touches the main hy2 socket of anyone (quic-go owns it); the
// probe is an ordinary hy2 login attempt, so a reflector needs nothing open
// but its hy2 port. Punch packets start with 0x00, which no QUIC v1 packet
// does, so quic-go drops them on arrival.
//
// The punched connection is one more address of the target's node entry
// ("@punch:<name>[#bind]"), dialled by the same loops as manual addresses,
// so quality / aggregate / lossless failover treat it like any other route.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	hyclient "github.com/apernet/hysteria/core/v2/client"

	"github.com/FrankoonG/hy2scale/internal/relay"
)

// PunchConfig is the punch side of a node entry.
type PunchConfig struct {
	// Signal: mesh paths used to reach the target for signalling, in
	// fallback order; each ends at the target.
	Signal []string `yaml:"signal" json:"signal"`
	// Reflectors: the target's outbound peers it probes its mapping with.
	Reflectors []string `yaml:"reflectors" json:"reflectors"`
	// Binds: local interface / source per punched route ("" = auto). Each is
	// its own route.
	Binds []string `yaml:"binds,omitempty" json:"binds,omitempty"`
	// Family: the address family the punch uses end to end (probes,
	// reflections, punch sockets): "ipv4" (default, also "") or "ipv6".
	Family string `yaml:"family,omitempty" json:"family,omitempty"`
}

const (
	PunchFamilyIPv4 = "ipv4"
	PunchFamilyIPv6 = "ipv6"
)

// ValidPunchFamily reports whether f names a punch address family.
func ValidPunchFamily(f string) bool {
	return f == "" || f == PunchFamilyIPv4 || f == PunchFamilyIPv6
}

// punchNet is the UDP network of a punch family.
func punchNet(family string) string {
	if family == PunchFamilyIPv6 {
		return "udp6"
	}
	return "udp4"
}

// ipInNet reports whether ip belongs to the family of network ("udp4"/"udp6").
func ipInNet(ip net.IP, network string) bool {
	if ip == nil {
		return false
	}
	return (ip.To4() != nil) == (network == "udp4")
}

// PeerAdvanced holds per-node tuning; zero means the default.
type PeerAdvanced struct {
	PunchRetryMin int `yaml:"punch_retry_min,omitempty" json:"punch_retry_min,omitempty"` // seconds, default 5
	PunchRetryMax int `yaml:"punch_retry_max,omitempty" json:"punch_retry_max,omitempty"` // seconds, default 300
	ProbeTimeout  int `yaml:"probe_timeout,omitempty" json:"probe_timeout,omitempty"`     // seconds, default 5
	PunchWindow   int `yaml:"punch_window,omitempty" json:"punch_window,omitempty"`       // seconds, default 5
}

func (p *PeerAdvanced) retryMin() time.Duration {
	return secsOr(p, func(p *PeerAdvanced) int { return p.PunchRetryMin }, 5)
}
func (p *PeerAdvanced) retryMax() time.Duration {
	return secsOr(p, func(p *PeerAdvanced) int { return p.PunchRetryMax }, 300)
}
func (p *PeerAdvanced) probeTimeout() time.Duration {
	return secsOr(p, func(p *PeerAdvanced) int { return p.ProbeTimeout }, 5)
}
func (p *PeerAdvanced) window() time.Duration {
	return secsOr(p, func(p *PeerAdvanced) int { return p.PunchWindow }, 5)
}

func secsOr(p *PeerAdvanced, f func(*PeerAdvanced) int, def int) time.Duration {
	if p != nil {
		if v := f(p); v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return time.Duration(def) * time.Second
}

const (
	punchAddrPrefix = "@punch:"
	probeAuthPrefix = "h2s-probe:"
	maxReflectors   = 2
)

// punchAddr is the virtual address of one punched route of node name.
func punchAddr(name, bind string) string {
	if bind == "" {
		return punchAddrPrefix + name
	}
	return punchAddrPrefix + name + "#" + bind
}

// IsPunchAddr reports whether addr is a punched route's virtual address.
func IsPunchAddr(addr string) bool { return strings.HasPrefix(addr, punchAddrPrefix) }

// punchAddrs lists the virtual addresses of a node entry's punch routes.
func (c ClientEntry) punchAddrs() []string {
	if c.Punch == nil || len(c.Punch.Signal) == 0 {
		return nil
	}
	binds := c.Punch.Binds
	if len(binds) == 0 {
		binds = []string{""}
	}
	out := make([]string, 0, len(binds))
	for _, b := range binds {
		out = append(out, punchAddr(c.Name, b))
	}
	return out
}

// PunchStatus is what the UI shows for one punched route.
type PunchStatus struct {
	Addr    string `json:"addr"`
	State   string `json:"state"` // "punching" | "connected" | "failed" | "waiting"
	Reason  string `json:"reason,omitempty"`
	Mapping string `json:"mapping,omitempty"` // the target's address the route uses
	Since   int64  `json:"since"`             // unix seconds
	Retries int    `json:"retries,omitempty"`
}

// punch failure reasons (stable strings; the UI translates them)
var (
	errPunchRefused   = errors.New("refused")
	errPunchSymmetric = errors.New("symmetric")
	errPunchNoReflect = errors.New("no_reflector")
	errPunchNoPath    = errors.New("no_path")
	errPunchBackoff   = errors.New("backoff")
	// the target closed the signalling stream without answering: a node
	// older than 1.4.0 (it dials the punch address as a plain target)
	errPunchUnsupported = errors.New("unsupported")
)

type probeWait struct {
	created time.Time
	addr    chan string
}

type punchManager struct {
	a *App

	mu      sync.Mutex
	probes  map[string]*probeWait // reflector side: token → observation
	status  map[string]*PunchStatus
	next    map[string]time.Time // route → earliest next attempt (backoff)
	servers int                  // live punch listeners (target side)
	inbound map[string]int       // target side: initiator → live punched connections
}

func newPunchManager(a *App) *punchManager {
	m := &punchManager{a: a, probes: map[string]*probeWait{}, status: map[string]*PunchStatus{}, next: map[string]time.Time{}, inbound: map[string]int{}}
	a.node.SetPunchHandler(m.serve)
	return m
}

// ---------------------------------------------------------------- signalling

type punchMsg struct {
	Op         string   `json:"op"`                   // probe_token | probe_result | punch
	Token      string   `json:"token,omitempty"`      // probe_result
	From       string   `json:"from,omitempty"`       // punch: initiator's node name
	Mappings   []string `json:"mappings,omitempty"`   // punch: initiator's mapped addresses
	Reflectors []string `json:"reflectors,omitempty"` // punch: target's reflectors
	WindowMS   int      `json:"window_ms,omitempty"`
	Family     string   `json:"family,omitempty"` // punch: "ipv4" (default) or "ipv6"
}

type punchReply struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Token  string `json:"token,omitempty"`
	Addr   string `json:"addr,omitempty"`
}

// call sends one request along path (the last element is the node that
// answers) and returns its reply.
func (m *punchManager) call(ctx context.Context, path []string, req punchMsg, timeout time.Duration) (punchReply, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var conn net.Conn
	var err error
	if len(path) == 1 {
		conn, err = m.a.node.DialTCP(ctx, path[0], relay.StreamPunch)
	} else {
		conn, err = m.a.node.DialVia(ctx, path, relay.StreamPunch)
	}
	if err != nil {
		return punchReply{}, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return punchReply{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return punchReply{}, err
	}
	var r punchReply
	if err := json.Unmarshal(line, &r); err != nil {
		return punchReply{}, err
	}
	return r, nil
}

// serve answers one StreamPunch request.
func (m *punchManager) serve(stream net.Conn) {
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(60 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(stream, 16<<10)).ReadBytes('\n')
	if err != nil {
		return
	}
	var req punchMsg
	if json.Unmarshal(line, &req) != nil {
		return
	}
	var rep punchReply
	switch req.Op {
	case "probe_token":
		rep = punchReply{OK: true, Token: m.newProbe()}
	case "probe_result":
		if addr, ok := m.waitProbe(req.Token, 8*time.Second); ok {
			rep = punchReply{OK: true, Addr: addr}
		} else {
			rep = punchReply{Reason: "no_probe"}
		}
	case "punch":
		rep = m.handlePunch(req)
	default:
		rep = punchReply{Reason: "unknown_op"}
	}
	b, _ := json.Marshal(rep)
	stream.Write(append(b, '\n'))
}

// ---------------------------------------------------------------- reflector

func randToken() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (m *punchManager) newProbe() string {
	t := randToken()
	m.mu.Lock()
	now := time.Now()
	for k, p := range m.probes {
		if now.Sub(p.created) > 30*time.Second {
			delete(m.probes, k)
		}
	}
	if len(m.probes) < 1024 {
		m.probes[t] = &probeWait{created: now, addr: make(chan string, 1)}
	}
	m.mu.Unlock()
	return t
}

// observeProbe is called from the hy2 Authenticate of every listener: a
// probe login is recorded and always refused.
func (m *punchManager) observeProbe(auth string, addr net.Addr) bool {
	tok, ok := strings.CutPrefix(auth, probeAuthPrefix)
	if !ok {
		return false
	}
	m.mu.Lock()
	p := m.probes[tok]
	m.mu.Unlock()
	if p != nil && addr != nil {
		select {
		case p.addr <- addr.String():
		default:
		}
	}
	return true
}

func (m *punchManager) waitProbe(tok string, d time.Duration) (string, bool) {
	m.mu.Lock()
	p := m.probes[tok]
	m.mu.Unlock()
	if p == nil {
		return "", false
	}
	defer func() {
		m.mu.Lock()
		delete(m.probes, tok)
		m.mu.Unlock()
	}()
	select {
	case a := <-p.addr:
		return a, true
	case <-time.After(d):
		return "", false
	}
}

// ---------------------------------------------------------------- probing

// peerProbeAddr is the hy2 address of an outbound peer in the family of
// network ("udp4"/"udp6"), without binding or port ranges: its connected
// address when that has the family, else its first address that does. nil
// when the entry has no address of that family.
func (m *punchManager) peerProbeAddr(name, network string) *net.UDPAddr {
	for _, cl := range m.a.store.Get().Clients {
		if cl.Name != name || cl.Disabled {
			continue
		}
		var cands []string
		if v, ok := m.a.primaryDial.Load(cl.Addr); ok {
			if s, _ := v.(string); s != "" {
				cands = append(cands, s)
			}
		}
		cands = append(cands, cl.AllAddrs()...)
		for _, dial := range cands {
			if IsPunchAddr(dial) {
				continue
			}
			ua, err := net.ResolveUDPAddr(network, extractPrimaryAddr(dial))
			if err == nil && ipInNet(ua.IP, network) {
				return ua
			}
		}
	}
	return nil
}

// probeSelf learns this node's mapping for local port `port` (0 = pick one)
// from up to two outbound peers, in the family of network ("udp4"/"udp6").
// It returns the mappings seen and the port.
func (m *punchManager) probeSelf(ctx context.Context, peers []string, bind addrBind, network string, timeout time.Duration) ([]string, int, error) {
	port := 0
	var mapped []string
	tried := 0
	for _, peer := range peers {
		if tried == maxReflectors {
			break
		}
		raddr := m.peerProbeAddr(peer, network)
		if raddr == nil || !m.a.node.HasPeer(peer) {
			continue
		}
		tried++
		tok, err := m.call(ctx, []string{peer}, punchMsg{Op: "probe_token"}, timeout)
		if err != nil || !tok.OK {
			debugLog("[punch] probe token from %s: %v %s", peer, err, tok.Reason)
			continue
		}
		sock, err := bind.listenAt(raddr.IP, port, network)
		if err != nil {
			return nil, 0, err
		}
		port = sock.LocalAddr().(*net.UDPAddr).Port
		quicProbe(sock, raddr, tok.Token, timeout)
		res, err := m.call(ctx, []string{peer}, punchMsg{Op: "probe_result", Token: tok.Token}, timeout+8*time.Second)
		if err != nil || !res.OK {
			debugLog("[punch] probe result from %s: %v %s", peer, err, res.Reason)
			continue
		}
		if ua, err := net.ResolveUDPAddr(network, res.Addr); err != nil || !ipInNet(ua.IP, network) {
			debugLog("[punch] probe result from %s: %s is not %s", peer, res.Addr, network)
			continue
		}
		mapped = append(mapped, res.Addr)
	}
	if len(mapped) == 0 {
		return nil, port, errPunchNoReflect
	}
	for _, a := range mapped[1:] {
		if portOf(a) != portOf(mapped[0]) {
			return mapped, port, errPunchSymmetric
		}
	}
	return mapped, port, nil
}

func portOf(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}

// quicProbe runs one hy2 login with the probe token from sock and closes it.
func quicProbe(sock *net.UDPConn, raddr *net.UDPAddr, token string, timeout time.Duration) {
	defer sock.Close()
	c, _, err := hyclient.NewClient(&hyclient.Config{
		ConnFactory: oneConn{sock},
		ServerAddr:  raddr,
		Auth:        probeAuthPrefix + token,
		TLSConfig:   hyclient.TLSConfig{InsecureSkipVerify: true, ServerName: "hy2scale"},
		QUICConfig:  hyclient.QUICConfig{MaxIdleTimeout: timeout},
	})
	if err == nil {
		c.Close() // a reflector should never accept a probe
	}
}

// oneConn hands hysteria a socket we already bound.
type oneConn struct{ c *net.UDPConn }

func (o oneConn) New(net.Addr) (net.PacketConn, error) { return o.c, nil }

// punchPackets are non-QUIC (first byte 0) datagrams that open NAT filters.
func punchPackets(sock *net.UDPConn, to []*net.UDPAddr, window time.Duration, stop <-chan struct{}) {
	pkt := []byte{0, 'h', '2', 's', 'p'}
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	end := time.Now().Add(window)
	for time.Now().Before(end) {
		for _, a := range to {
			sock.WriteToUDP(pkt, a)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// ---------------------------------------------------------------- target

func (m *punchManager) handlePunch(req punchMsg) punchReply {
	cfg := m.a.store.Get()
	if !cfg.AcceptPunch {
		log.Printf("[punch] request from %q refused: accept_punch is off", req.From)
		return punchReply{Reason: errPunchRefused.Error()}
	}
	if !ValidPunchFamily(req.Family) {
		return punchReply{Reason: "bad_request"}
	}
	network := punchNet(req.Family)
	var to []*net.UDPAddr
	for _, s := range req.Mappings {
		if ua, err := net.ResolveUDPAddr(network, s); err == nil && ipInNet(ua.IP, network) {
			to = append(to, ua)
		}
	}
	if len(to) == 0 {
		return punchReply{Reason: "bad_request"}
	}
	window := time.Duration(req.WindowMS) * time.Millisecond
	if window <= 0 || window > 30*time.Second {
		window = 5 * time.Second
	}
	cert := m.a.serverCert.Load()
	if cert == nil {
		return punchReply{Reason: "no_server"}
	}
	ctx, cancel := context.WithTimeout(m.a.appCtx, 30*time.Second)
	defer cancel()
	// the initiator's reflectors first, then any other node we dial
	refl := append([]string(nil), req.Reflectors...)
	for _, c := range cfg.Clients {
		if !c.Disabled && c.Name != req.From && !contains(refl, c.Name) {
			refl = append(refl, c.Name)
		}
	}
	mapped, port, err := m.probeSelf(ctx, refl, addrBind{}, network, 5*time.Second)
	if err != nil {
		log.Printf("[punch] request from %q: probing %v: %v", req.From, req.Reflectors, err)
		return punchReply{Reason: err.Error()}
	}
	sock, err := addrBind{}.listenAt(nil, port, network)
	if err != nil {
		return punchReply{Reason: "bind"}
	}
	token := randToken()
	stop, err := m.startPunchServer(sock, *cert, token, req.From, window)
	if err != nil {
		sock.Close()
		return punchReply{Reason: "server"}
	}
	// open our NAT towards the initiator's mapping while it dials
	go punchPackets(sock, to, window, stop)
	log.Printf("[punch] %q → us: our mapping %s, theirs %v", req.From, mapped[0], req.Mappings)
	return punchReply{OK: true, Addr: mapped[0], Token: token}
}

// punchListener is the target-side hy2 server of one punched connection: it
// accepts exactly the token it issued, and closes when that connection ends
// (or when nobody came).
type punchListener struct {
	*hy2Auth
	m       *punchManager
	token   string
	from    string
	used    atomic.Bool
	conns   atomic.Int32
	stop    chan struct{}
	closeFn func()
	once    sync.Once
}

func (p *punchListener) Authenticate(addr net.Addr, auth string, tx uint64) (bool, string) {
	if p.m.observeProbe(auth, addr) {
		return false, ""
	}
	if auth == p.token && p.used.CompareAndSwap(false, true) {
		log.Printf("[punch] %q connected directly from %s", p.from, addr)
		p.m.mu.Lock()
		p.m.inbound[p.from]++
		p.m.mu.Unlock()
		return true, "system"
	}
	return false, ""
}

func (p *punchListener) Connect(addr net.Addr, id string, tx uint64) { p.conns.Add(1) }

func (p *punchListener) Disconnect(addr net.Addr, id string, err error) {
	if p.conns.Add(-1) <= 0 {
		p.close()
	}
}

func (p *punchListener) close() {
	p.once.Do(func() {
		close(p.stop)
		p.closeFn()
		if p.used.Load() {
			p.m.mu.Lock()
			if p.m.inbound[p.from]--; p.m.inbound[p.from] <= 0 {
				delete(p.m.inbound, p.from)
			}
			p.m.mu.Unlock()
		}
	})
}

func (m *punchManager) startPunchServer(sock *net.UDPConn, cert tls.Certificate, token, from string, window time.Duration) (<-chan struct{}, error) {
	ctx, cancel := context.WithCancel(m.a.appCtx)
	pl := &punchListener{hy2Auth: &hy2Auth{app: m.a}, m: m, token: token, from: from, stop: make(chan struct{})}
	srv, err := m.a.newHy2Server(ctx, sock, cert, pl, pl)
	if err != nil {
		cancel()
		return nil, err
	}
	pl.closeFn = func() {
		cancel()
		srv.Close()
		sock.Close()
		m.mu.Lock()
		m.servers--
		m.mu.Unlock()
	}
	m.mu.Lock()
	m.servers++
	m.mu.Unlock()
	go srv.Serve()
	// nobody came within the window (+ handshake slack): give the port back
	go func() {
		select {
		case <-pl.stop:
		case <-time.After(window + 20*time.Second):
			if !pl.used.Load() {
				pl.close()
			}
		}
	}()
	return pl.stop, nil
}

// ---------------------------------------------------------------- initiator

// punchDial is what a dial loop needs to connect a punched route.
type punchDial struct {
	addr     *net.UDPAddr
	factory  hyclient.ConnFactory
	password string
	sock     *net.UDPConn
}

// dialTarget resolves a dial address: a punched route is punched first.
// done must be called with whether the connection came up.
func (a *App) dialTarget(ctx context.Context, cl ClientEntry, dialAddr string) (net.Addr, hyclient.ConnFactory, string, func(up bool), error) {
	if !IsPunchAddr(dialAddr) {
		addr, f, _, err := hy2Dial(dialAddr)
		return addr, f, cl.Password, func(bool) {}, err
	}
	pd, err := a.punch.prepare(ctx, cl, dialAddr)
	if err != nil {
		return nil, nil, "", nil, err
	}
	return pd.addr, pd.factory, pd.password, func(up bool) {
		if up {
			a.punch.connected(dialAddr)
			return
		}
		pd.sock.Close()
		a.punch.fail(dialAddr, cl.Advanced, errors.New("handshake"))
	}, nil
}

func (m *punchManager) setStatus(addr, state, reason, mapping string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status[addr]
	if st == nil {
		st = &PunchStatus{Addr: addr}
		m.status[addr] = st
	}
	if st.State != state {
		st.Since = time.Now().Unix()
	}
	st.State, st.Reason = state, reason
	if mapping != "" {
		st.Mapping = mapping
	}
}

// Connected marks a punched route up (called by the dial loop once the
// connection is established) and resets its backoff.
func (m *punchManager) connected(addr string) {
	m.setStatus(addr, "connected", "", "")
	m.mu.Lock()
	delete(m.next, addr)
	if st := m.status[addr]; st != nil {
		st.Retries = 0
	}
	m.mu.Unlock()
}

// lost marks a punched route down; the next attempt waits the minimum backoff.
func (m *punchManager) lost(addr string, adv *PeerAdvanced) {
	m.setStatus(addr, "waiting", "lost", "")
	m.mu.Lock()
	m.next[addr] = time.Now().Add(adv.retryMin())
	m.mu.Unlock()
}

// Statuses lists every punched route this node is working on. Routes no
// longer configured are forgotten here.
func (m *punchManager) Statuses() []PunchStatus {
	live := map[string]bool{}
	for _, c := range m.a.store.Get().Clients {
		if !c.Disabled {
			for _, a := range c.punchAddrs() {
				live[a] = true
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PunchStatus, 0, len(m.status))
	for addr, s := range m.status {
		if !live[addr] {
			delete(m.status, addr)
			delete(m.next, addr)
			continue
		}
		out = append(out, *s)
	}
	return out
}

func (m *punchManager) fail(addr string, adv *PeerAdvanced, err error) error {
	m.mu.Lock()
	st := m.status[addr]
	retries := 1
	if st != nil {
		st.Retries++
		retries = st.Retries
	}
	d := adv.retryMin()
	for i := 1; i < retries && d < adv.retryMax(); i++ {
		d *= 2
	}
	if d > adv.retryMax() {
		d = adv.retryMax()
	}
	m.next[addr] = time.Now().Add(d)
	m.mu.Unlock()
	// the status carries the reason code; details go to the log
	code, _, _ := strings.Cut(err.Error(), ":")
	m.setStatus(addr, "failed", code, "")
	log.Printf("[punch] %s: %v (retry in %s)", addr, err, d.Round(time.Second))
	return fmt.Errorf("punch %s: %w", addr, err)
}

// prepare runs the punch for one route of cl and returns how to dial it.
func (m *punchManager) prepare(ctx context.Context, cl ClientEntry, addr string) (*punchDial, error) {
	m.mu.Lock()
	next := m.next[addr]
	m.mu.Unlock()
	if time.Now().Before(next) {
		return nil, errPunchBackoff
	}
	if cl.Punch == nil {
		return nil, m.fail(addr, cl.Advanced, errPunchNoPath)
	}
	bind := addrBind{}
	if i := strings.LastIndexByte(addr, '#'); i >= 0 {
		var err error
		if _, bind, err = splitAddrBind("x:1" + addr[i:]); err != nil {
			return nil, m.fail(addr, cl.Advanced, err)
		}
	}
	if !ValidPunchFamily(cl.Punch.Family) {
		return nil, m.fail(addr, cl.Advanced, fmt.Errorf("%w: family %q", errPunchNoPath, cl.Punch.Family))
	}
	network := punchNet(cl.Punch.Family)
	if bind.src != nil && !ipInNet(bind.src, network) {
		return nil, m.fail(addr, cl.Advanced, fmt.Errorf("%w: source %s is not %s", errPunchNoReflect, bind.src, cl.Punch.Family))
	}
	m.setStatus(addr, "punching", "", "")
	timeout := cl.Advanced.probeTimeout()
	window := cl.Advanced.window()
	var lastErr error = errPunchNoPath
	for _, path := range cl.Punch.Signal {
		hops := strings.Split(path, "/")
		if len(hops) == 0 || hops[len(hops)-1] != cl.Name {
			continue
		}
		if st := m.a.PathStatus(path); !st.OK && len(hops) > 1 {
			lastErr = fmt.Errorf("%w: %s %s", errPunchNoPath, path, st.Reason)
			continue
		}
		// our own mapping: the path's first hop first, then other outbound peers
		peers := []string{hops[0]}
		for _, c := range m.a.store.Get().Clients {
			if c.Name != hops[0] && c.Name != cl.Name {
				peers = append(peers, c.Name)
			}
		}
		mine, port, err := m.probeSelf(ctx, peers, bind, network, timeout)
		if err != nil {
			lastErr = err
			if errors.Is(err, errPunchSymmetric) {
				break
			}
			continue
		}
		rep, err := m.call(ctx, hops, punchMsg{
			Op: "punch", From: m.a.node.Name(), Mappings: mine,
			Reflectors: cl.Punch.Reflectors, WindowMS: int(window / time.Millisecond),
			Family: cl.Punch.Family,
		}, 45*time.Second)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			lastErr = errPunchUnsupported
			break
		}
		if err != nil {
			lastErr = fmt.Errorf("%w: %s: %v", errPunchNoPath, path, err)
			continue
		}
		if !rep.OK {
			lastErr = errors.New(rep.Reason)
			break // the target's answer does not depend on the path
		}
		theirs, err := net.ResolveUDPAddr(network, rep.Addr)
		if err != nil || !ipInNet(theirs.IP, network) {
			lastErr = fmt.Errorf("bad mapping %q", rep.Addr)
			continue
		}
		sock, err := bind.listenAt(theirs.IP, port, network)
		if err != nil {
			lastErr = err
			continue
		}
		stop := make(chan struct{})
		go punchPackets(sock, []*net.UDPAddr{theirs}, window, stop)
		time.Sleep(300 * time.Millisecond) // let the crossing packets open both filters
		m.setStatus(addr, "punching", "", rep.Addr)
		log.Printf("[punch] %s via %s: ours %v, theirs %s", addr, path, mine, rep.Addr)
		return &punchDial{addr: theirs, factory: stopOnUse{oneConn{sock}, stop}, password: rep.Token, sock: sock}, nil
	}
	return nil, m.fail(addr, cl.Advanced, lastErr)
}

// stopOnUse ends the punch packets once hysteria takes the socket over and
// its handshake is under way (they are only needed to open the filters).
type stopOnUse struct {
	oneConn
	stop chan struct{}
}

func (s stopOnUse) New(a net.Addr) (net.PacketConn, error) {
	go func() { time.Sleep(3 * time.Second); safeClose(s.stop) }()
	return s.oneConn.New(a)
}

func safeClose(c chan struct{}) {
	defer func() { recover() }()
	close(c)
}

// PunchStatuses lists the punched routes this node is working on.
func (a *App) PunchStatuses() []PunchStatus { return a.punch.Statuses() }

// PunchedPeers maps a direct peer to who set up a live punched connection
// with it: this node (initiator) and/or the peer.
func (a *App) PunchedPeers() map[string][]string {
	m := a.punch
	out := map[string][]string{}
	m.mu.Lock()
	defer m.mu.Unlock()
	for addr, st := range m.status {
		if st.State != "connected" {
			continue
		}
		name := strings.TrimPrefix(addr, punchAddrPrefix)
		if i := strings.IndexByte(name, '#'); i >= 0 {
			name = name[:i]
		}
		if !contains(out[name], a.node.Name()) {
			out[name] = append(out[name], a.node.Name())
		}
	}
	for from := range m.inbound {
		out[from] = append(out[from], from)
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

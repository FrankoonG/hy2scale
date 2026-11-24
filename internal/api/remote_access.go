package api

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/hy2scale/core/netfilter"
	"github.com/FrankoonG/hy2scale/internal/app"
)

// Remote access, managing side: this node reaches any address from a node
// it manages (the exit, remote_access_dial.go), for the visitor who opened
// it from this node's page.
//
//   - web: a reverse proxy at <base>/rweb/<id>/ to a web address; the
//     visitor is admitted by a cookie the entry link sets.
//   - port: a port of this node (tcp or udp) forwarded to host:port; only
//     the visitor's address may use it. On the host network the port is let
//     through the firewall for that address (remote_access_fw_linux.go).
//
// Each connection is a dial request to the exit with the remote sign-in the
// page holds. An entry closes at the end of that sign-in, when the remote
// refuses it, when this node's session that opened it ends, or on request.
// Entries live in memory only.

const (
	raMaxLife     = 24 * time.Hour
	raDefaultLife = 30 * time.Minute // no expiry given (the control socket)
	raUDPIdle     = 60 * time.Second
	raKeyLife     = 2 * time.Minute
	raDialWait    = 15 * time.Second
	raCheckEvery  = 10 * time.Second
)

type raManager struct {
	mu sync.Mutex
	m  map[string]*raEntry
}

type raEntry struct {
	ID       string
	Chain    []string
	Mode     string // web | port
	Proto    string // tcp | udp | tcp,udp
	TargetID string // the exit's list entry ("" = given here)
	Target   string // web: URL; port: host:port
	Port     int
	Visitor  netip.Addr
	Created  time.Time

	hostport string // the address dialled at the exit
	tcp, udp bool
	creator  string // this node's web session that opened it
	firewall bool

	mu      sync.Mutex
	token   string
	expires time.Time
	cookie  string    // web: the visitor's cookie value
	key     string    // web: the entry key (one use)
	keyExp  time.Time // web
	client  string    // web: the last visitor address
	closers []func()
	conns   map[net.Conn]struct{}

	proxy  *httputil.ReverseProxy
	active atomic.Int32
	up     atomic.Int64
	down   atomic.Int64
	closed atomic.Bool
}

type raJSON struct {
	ID          string   `json:"id"`
	Chain       []string `json:"chain"`
	Mode        string   `json:"mode"`
	Proto       string   `json:"proto"`
	TargetID    string   `json:"target_id,omitempty"`
	Target      string   `json:"target"`
	Port        int      `json:"port,omitempty"`
	Visitor     string   `json:"visitor"`
	Created     int64    `json:"created"`
	Expires     int64    `json:"expires"`
	Active      int      `json:"active"`
	Up          int64    `json:"up"`
	Down        int64    `json:"down"`
	Firewall    bool     `json:"firewall"`
	HostNetwork bool     `json:"host_network"`
}

func (e *raEntry) json() raJSON {
	e.mu.Lock()
	exp := e.expires
	e.mu.Unlock()
	v := ""
	if e.Visitor.IsValid() {
		v = e.Visitor.String()
	}
	return raJSON{
		ID: e.ID, Chain: e.Chain, Mode: e.Mode, Proto: e.Proto, TargetID: e.TargetID, Target: e.Target, Port: e.Port,
		Visitor: v, Created: e.Created.UnixMilli(), Expires: exp.UnixMilli(),
		Active: int(e.active.Load()), Up: e.up.Load(), Down: e.down.Load(),
		Firewall: e.firewall, HostNetwork: netfilter.CheckHostNetwork(),
	}
}

func (e *raEntry) sameChain(chain []string) bool {
	if len(chain) != len(e.Chain) {
		return false
	}
	for i := range chain {
		if chain[i] != e.Chain[i] {
			return false
		}
	}
	return true
}

func (e *raEntry) track(c net.Conn) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return false
	}
	if e.conns == nil {
		e.conns = map[net.Conn]struct{}{}
	}
	e.conns[c] = struct{}{}
	return true
}

func (e *raEntry) untrack(c net.Conn) {
	e.mu.Lock()
	delete(e.conns, c)
	e.mu.Unlock()
}

// allowed: a port-mode connection from addr may use the entry.
func (e *raEntry) allowed(addr net.Addr) bool {
	ap, err := netip.ParseAddrPort(addr.String())
	return err == nil && ap.Addr().Unmap() == e.Visitor
}

func (s *Server) remoteAccessRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/remote-access", s.listRemoteAccess)
	m.HandleFunc("POST /api/remote-access", s.createRemoteAccess)
	m.HandleFunc("DELETE /api/remote-access", s.closeRemoteAccessChain)
	m.HandleFunc("DELETE /api/remote-access/{id}", s.closeRemoteAccess)
	m.HandleFunc("POST /api/remote-access/renew", s.renewRemoteAccess)
	m.HandleFunc("POST /api/remote-access/{id}/open", s.openRemoteAccess)
}

// raStart: left-over firewall rules go, and entries are checked for their
// end until ctx ends (then all close).
func (s *Server) raStart(ctx context.Context) {
	go raFWSweep()
	go func() {
		t := time.NewTicker(raCheckEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				for _, e := range s.raEntries(nil) {
					s.raClose(e, "stopping")
				}
				return
			case <-t.C:
				now := time.Now()
				for _, e := range s.raEntries(nil) {
					e.mu.Lock()
					exp := e.expires
					e.mu.Unlock()
					switch {
					case now.After(exp):
						s.raClose(e, "remote sign-in ended")
					case e.creator != "" && !s.sessionLive(e.creator):
						s.raClose(e, "signed out here")
					}
				}
			}
		}
	}()
}

// raEntries: the entries (of one chain; nil = all), oldest first.
func (s *Server) raEntries(chain []string) []*raEntry {
	s.ra.mu.Lock()
	out := make([]*raEntry, 0, len(s.ra.m))
	for _, e := range s.ra.m {
		if chain == nil || e.sameChain(chain) {
			out = append(out, e)
		}
	}
	s.ra.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (s *Server) raGet(id string) *raEntry {
	s.ra.mu.Lock()
	defer s.ra.mu.Unlock()
	return s.ra.m[id]
}

func (s *Server) raClose(e *raEntry, why string) {
	if e.closed.Swap(true) {
		return
	}
	s.ra.mu.Lock()
	delete(s.ra.m, e.ID)
	s.ra.mu.Unlock()
	e.mu.Lock()
	closers := e.closers
	conns := e.conns
	e.closers, e.conns = nil, nil
	e.mu.Unlock()
	for _, fn := range closers {
		fn()
	}
	for c := range conns {
		c.Close()
	}
	log.Printf("[remote-access] %s closed (%s → %s via %s): %s", e.ID, e.Mode, e.Target, strings.Join(e.Chain, "/"), why)
}

// raChain: the chain of a request, old node IDs resolved.
func (s *Server) raChain(chain []string) []string {
	out := append([]string(nil), chain...)
	s.mu.RLock()
	for i, name := range out {
		if newID, ok := s.oldNodeIDs[name]; ok {
			out[i] = newID
		}
	}
	s.mu.RUnlock()
	return out
}

// raExpiry: when an entry ends, from the page's remote sign-in expiry (ms).
func raExpiry(ms int64) time.Time {
	now := time.Now()
	t := time.UnixMilli(ms)
	if ms <= 0 {
		t = now.Add(raDefaultLife)
	}
	if t.After(now.Add(raMaxLife)) {
		t = now.Add(raMaxLife)
	}
	if t.Before(now.Add(time.Minute)) {
		t = now.Add(time.Minute)
	}
	return t
}

func (s *Server) listRemoteAccess(w http.ResponseWriter, r *http.Request) {
	out := []raJSON{}
	for _, e := range s.raEntries(nil) {
		out = append(out, e.json())
	}
	writeJSON(w, out)
}

// raDialError: the exit refused or failed a dial.
type raDialError struct {
	Status int
	Msg    string
}

func (e *raDialError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return "HTTP " + strconv.Itoa(e.Status)
}

// raDial opens a flow to target from the exit of e.
func (s *Server) raDial(ctx context.Context, e *raEntry, proto, target, client string) (net.Conn, error) {
	return s.raDialProbe(ctx, e, proto, target, client, false)
}

// raDialProbe: raDial; probe marks the check before an entry opens (the exit
// does not record it).
func (s *Server) raDialProbe(ctx context.Context, e *raEntry, proto, target, client string, probe bool) (net.Conn, error) {
	conn, _, err := s.dialRemoteAPI(ctx, e.Chain)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	e.mu.Lock()
	token := e.token
	e.mu.Unlock()
	q := url.Values{"target": {target}, "proto": {proto}}
	var b strings.Builder
	b.WriteString("GET /scale/api/remote-access/dial?" + q.Encode() + " HTTP/1.1\r\nHost: 127.0.0.1:5565\r\n")
	b.WriteString("Connection: Upgrade\r\nUpgrade: " + raUpgrade + "\r\n")
	if token != "" {
		b.WriteString("Authorization: Bearer " + token + "\r\n")
	}
	b.WriteString("X-Hy2scale-From: " + s.app.Node().Name() + "\r\n")
	if client != "" {
		b.WriteString("X-Hy2scale-Client: " + client + "\r\n")
	}
	if probe {
		b.WriteString("X-Hy2scale-Probe: 1\r\n")
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("bad answer from %s: %v", strings.Join(e.Chain, "/"), err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		conn.Close()
		return nil, &raDialError{Status: resp.StatusCode, Msg: strings.TrimSpace(string(msg))}
	}
	conn.SetDeadline(time.Time{})
	return &raBufConn{Conn: conn, r: br}, nil
}

// raDialFailed: a dial failed; a refused sign-in ends the entry.
func (s *Server) raDialFailed(e *raEntry, err error) {
	var de *raDialError
	if errors.As(err, &de) && de.Status == http.StatusUnauthorized {
		s.raClose(e, "remote sign-in refused")
		return
	}
	log.Printf("[remote-access] %s: %v", e.ID, err)
}

func (s *Server) createRemoteAccess(w http.ResponseWriter, r *http.Request) {
	// From this node's own page the visitor is whoever asks; the control
	// socket, another node or a proxied page names it (port mode).
	byPage := relayAuthID(r.Context()) == "" && r.Header.Get("X-Hy2scale-Proxy") == "" && !fromCtl(r)
	var body struct {
		Chain    []string `json:"chain"`
		Token    string   `json:"token"`
		TargetID string   `json:"target_id"` // an entry of the exit's list
		Mode     string   `json:"mode"`      // else a target given here
		Target   string   `json:"target"`
		TCP      bool     `json:"tcp"`
		UDP      bool     `json:"udp"`
		Proto    string   `json:"proto"` // tcp | udp (for tcp/udp)
		Port     int      `json:"port"`
		Expires  int64    `json:"expires"` // unix ms (0 = in 30 minutes)
		Visitor  string   `json:"visitor"` // the address let in (port mode)
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Chain) == 0 {
		http.Error(w, "chain required", 400)
		return
	}
	visitor := netip.Addr{}
	if v := strings.TrimSpace(body.Visitor); v != "" {
		a, err := netip.ParseAddr(strings.Trim(v, "[]"))
		if err != nil {
			http.Error(w, "visitor: an IP address", 400)
			return
		}
		visitor = a.Unmap()
	} else if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && byPage {
		visitor = ap.Addr().Unmap()
	}
	chain := s.raChain(body.Chain)
	// No remote sign-in given: this node signs in itself (a session from a
	// saved sign-in, relay trust, or the saved sign-in; as "ctl -n" does).
	if body.Token == "" {
		tok, err := s.raSignIn(r.Context(), chain)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		body.Token = tok
	}
	t := app.RemoteAccessTarget{Mode: body.Mode, Target: body.Target, TCP: body.TCP, UDP: body.UDP}
	if body.Proto != "" {
		t.TCP, t.UDP = strings.Contains(body.Proto, "tcp"), strings.Contains(body.Proto, "udp")
	}
	if t.Mode == "port" && !t.TCP && !t.UDP {
		t.TCP = true
	}
	if body.TargetID != "" {
		found, err := s.raRemoteTarget(r.Context(), chain, body.Token, body.TargetID)
		if err != nil {
			var de *raDialError
			if errors.As(err, &de) && de.Status == http.StatusUnauthorized {
				http.Error(w, "remote sign-in refused", 409)
				return
			}
			http.Error(w, err.Error(), 502)
			return
		}
		t = found
	}
	if err := normRATarget(&t); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	e := &raEntry{
		ID:       s.generateToken()[:12],
		Chain:    chain,
		Mode:     t.Mode,
		TargetID: body.TargetID,
		Target:   t.Target,
		tcp:      t.TCP,
		udp:      t.UDP,
		Visitor:  visitor,
		Created:  time.Now(),
		creator:  sessionToken(r.Context()),
		token:    body.Token,
		expires:  raExpiry(body.Expires),
	}
	switch {
	case t.TCP && t.UDP:
		e.Proto = "tcp,udp"
	case t.UDP:
		e.Proto = "udp"
	default:
		e.Proto = "tcp"
	}
	if e.Mode == "web" {
		u, hp, _ := raWebURL(t.Target)
		e.hostport = hp
		e.proxy = s.raWebProxy(e, u)
	} else {
		if body.Port < 0 || body.Port > 65535 {
			http.Error(w, "port: 1-65535", 400)
			return
		}
		if !visitor.IsValid() {
			http.Error(w, "visitor required: the address to let in", 400)
			return
		}
		e.hostport = t.Target
	}

	// The exit and the sign-in are tried before anything opens here.
	ctx, cancel := context.WithTimeout(r.Context(), raDialWait)
	probe := "tcp"
	if !e.tcp {
		probe = "udp"
	}
	c, err := s.raDialProbe(ctx, e, probe, e.hostport, r.RemoteAddr, true)
	var de0 *raDialError
	if err != nil && e.tcp && e.udp && errors.As(err, &de0) && de0.Status == http.StatusBadGateway {
		// tcp and udp: a target that only answers udp is fine too
		c, err = s.raDialProbe(ctx, e, "udp", e.hostport, r.RemoteAddr, true)
	}
	cancel()
	if err != nil {
		var de *raDialError
		if errors.As(err, &de) {
			code := 502
			if de.Status == http.StatusUnauthorized {
				code = 409
				de.Msg = "remote sign-in refused"
			}
			http.Error(w, de.Error(), code)
			return
		}
		http.Error(w, err.Error(), 502)
		return
	}
	c.Close()

	if e.Mode == "port" {
		if err := s.raListen(e, body.Port); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
	}
	s.ra.mu.Lock()
	if s.ra.m == nil {
		s.ra.m = map[string]*raEntry{}
	}
	s.ra.m[e.ID] = e
	s.ra.mu.Unlock()
	log.Printf("[remote-access] %s opened: %s %s %s via %s for %s (port %d)", e.ID, e.Mode, e.Proto, e.Target, strings.Join(e.Chain, "/"), visitor, e.Port)
	writeJSON(w, e.json())
}

// raSignIn signs this node in to the node at the end of chain the way the
// control socket does; "" = relay trust (no session needed).
func (s *Server) raSignIn(ctx context.Context, chain []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, raDialWait)
	defer cancel()
	resp, how, err := s.remoteDo(ctx, chain, "GET", "/api/remote-access/targets", nil, nil)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("no sign-in to %s: relay trust there, or a saved sign-in (hy2scale ctl login)", strings.Join(chain, "/"))
	}
	if how == "trust" {
		return "", nil
	}
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	return s.ctl.tokens[strings.Join(chain, "/")], nil
}

// raRemoteTarget reads one entry of the exit's target list.
func (s *Server) raRemoteTarget(ctx context.Context, chain []string, token, id string) (app.RemoteAccessTarget, error) {
	var t app.RemoteAccessTarget
	ctx, cancel := context.WithTimeout(ctx, raDialWait)
	defer cancel()
	resp, err := s.remoteOpen(ctx, chain, "GET", "/api/remote-access/targets", nil, nil, token)
	if err != nil {
		return t, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusNotFound {
			msg = []byte("the remote node has no remote access list (it needs a newer version)")
		}
		return t, &raDialError{Status: resp.StatusCode, Msg: strings.TrimSpace(string(msg))}
	}
	var list []app.RemoteAccessTarget
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return t, err
	}
	for _, x := range list {
		if x.ID == id {
			return x, nil
		}
	}
	return t, &raDialError{Status: 404, Msg: "not in the remote node's list"}
}

func (s *Server) closeRemoteAccess(w http.ResponseWriter, r *http.Request) {
	e := s.raGet(r.PathValue("id"))
	if e == nil {
		http.Error(w, "not found", 404)
		return
	}
	s.raClose(e, "closed")
	writeJSON(w, map[string]string{"status": "ok"})
}

// closeRemoteAccessChain closes every entry of ?chain=a/b (the remote sign-in
// of that node ended on the page), or with &target_id= those of one target.
func (s *Server) closeRemoteAccessChain(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("chain")
	if raw == "" {
		http.Error(w, "chain required", 400)
		return
	}
	target := r.URL.Query().Get("target_id")
	n := 0
	for _, e := range s.raEntries(s.raChain(strings.Split(raw, "/"))) {
		if target != "" && e.TargetID != target {
			continue
		}
		s.raClose(e, "closed for the chain")
		n++
	}
	writeJSON(w, map[string]int{"closed": n})
}

// renewRemoteAccess carries the entries of a chain over to a new remote
// sign-in.
func (s *Server) renewRemoteAccess(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Chain   []string `json:"chain"`
		Token   string   `json:"token"`
		Expires int64    `json:"expires"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Chain) == 0 {
		http.Error(w, "chain required", 400)
		return
	}
	n := 0
	for _, e := range s.raEntries(s.raChain(body.Chain)) {
		e.mu.Lock()
		e.token, e.expires = body.Token, raExpiry(body.Expires)
		e.mu.Unlock()
		n++
	}
	writeJSON(w, map[string]int{"renewed": n})
}

// openRemoteAccess gives a web entry's link: it carries a key for one
// visit, which sets the visitor's cookie.
func (s *Server) openRemoteAccess(w http.ResponseWriter, r *http.Request) {
	e := s.raGet(r.PathValue("id"))
	if e == nil || e.Mode != "web" {
		http.Error(w, "not found", 404)
		return
	}
	key := s.generateToken()
	e.mu.Lock()
	e.key, e.keyExp = key, time.Now().Add(raKeyLife)
	e.mu.Unlock()
	u, _ := url.Parse(e.Target)
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	q := u.Query()
	q.Set(raKeyParam, key)
	writeJSON(w, map[string]string{"url": s.basePath + "/rweb/" + e.ID + p + "?" + q.Encode()})
}

// --- port mode ---

// raPorts: the port candidates of the remote_access_ports setting (nil =
// any free port).
func raPorts(spec string) []int {
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
			continue
		}
		for p := a; p <= b && len(out) < 65536; p++ {
			out = append(out, p)
		}
	}
	return out
}

// validRAPorts reports whether a remote_access_ports value parses.
func validRAPorts(spec string) bool {
	if strings.TrimSpace(spec) == "" {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		if len(raPorts(part)) == 0 {
			return false
		}
	}
	return true
}

// raListen opens the entry's port (want > 0: that port) and serves it.
func (s *Server) raListen(e *raEntry, want int) error {
	cands := []int{want}
	if want == 0 {
		cands = raPorts(s.app.Store().Get().RemoteAccessPorts)
		if len(cands) == 0 {
			cands = []int{0}
		}
	}
	var lastErr error
	for _, p := range cands {
		// tcp and udp on the same port number
		var ln net.Listener
		var pc net.PacketConn
		var err error
		if e.tcp {
			if ln, err = net.Listen("tcp", ":"+strconv.Itoa(p)); err != nil {
				lastErr = err
				continue
			}
			p = ln.Addr().(*net.TCPAddr).Port
		}
		if e.udp {
			if pc, err = net.ListenPacket("udp", ":"+strconv.Itoa(p)); err != nil {
				lastErr = err
				if ln != nil {
					ln.Close()
				}
				continue
			}
			p = pc.LocalAddr().(*net.UDPAddr).Port
		}
		e.Port = p
		if ln != nil {
			e.closers = append(e.closers, func() { ln.Close() })
			go s.raServeTCP(e, ln)
			if undo := raFWOpen(e.ID, "tcp", e.Visitor, p); undo != nil {
				e.firewall = true
				e.closers = append(e.closers, undo)
			}
		}
		if pc != nil {
			e.closers = append(e.closers, func() { pc.Close() })
			go s.raServeUDP(e, pc)
			if undo := raFWOpen(e.ID, "udp", e.Visitor, p); undo != nil {
				e.firewall = true
				e.closers = append(e.closers, undo)
			}
		}
		return nil
	}
	if want != 0 {
		return fmt.Errorf("port %d: %v", want, lastErr)
	}
	return fmt.Errorf("no free port: %v", lastErr)
}

func (s *Server) raServeTCP(e *raEntry, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if !e.allowed(c.RemoteAddr()) {
			c.Close()
			continue
		}
		go func() {
			if !e.track(c) {
				c.Close()
				return
			}
			defer e.untrack(c)
			e.active.Add(1)
			defer e.active.Add(-1)
			ctx, cancel := context.WithTimeout(context.Background(), raDialWait)
			rc, err := s.raDial(ctx, e, "tcp", e.hostport, c.RemoteAddr().String())
			cancel()
			if err != nil {
				c.Close()
				s.raDialFailed(e, err)
				return
			}
			if !e.track(rc) {
				c.Close()
				rc.Close()
				return
			}
			defer e.untrack(rc)
			up, down := raSplice(c, rc)
			e.up.Add(up)
			e.down.Add(down)
		}()
	}
}

type raUDPSess struct {
	stream net.Conn
	last   atomic.Int64
}

func (s *Server) raServeUDP(e *raEntry, pc net.PacketConn) {
	var mu sync.Mutex
	sess := map[string]*raUDPSess{}
	stop := make(chan struct{})
	defer close(stop)
	go func() { // idle sessions end
		t := time.NewTicker(raUDPIdle / 4)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				cut := time.Now().Add(-raUDPIdle).UnixMilli()
				mu.Lock()
				for k, u := range sess {
					if u.last.Load() < cut {
						u.stream.Close()
						delete(sess, k)
					}
				}
				mu.Unlock()
			}
		}
	}()
	buf := make([]byte, raMaxDatagram)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			mu.Lock()
			for _, u := range sess {
				u.stream.Close()
			}
			mu.Unlock()
			return
		}
		if !e.allowed(addr) {
			continue
		}
		key := addr.String()
		mu.Lock()
		u := sess[key]
		mu.Unlock()
		if u == nil {
			ctx, cancel := context.WithTimeout(context.Background(), raDialWait)
			stream, err := s.raDial(ctx, e, "udp", e.hostport, key)
			cancel()
			if err != nil {
				s.raDialFailed(e, err)
				continue
			}
			if !e.track(stream) {
				stream.Close()
				return
			}
			u = &raUDPSess{stream: stream}
			mu.Lock()
			sess[key] = u
			mu.Unlock()
			e.active.Add(1)
			go func(u *raUDPSess, to net.Addr) { // replies
				defer e.active.Add(-1)
				defer e.untrack(u.stream)
				rb := make([]byte, raMaxDatagram)
				for {
					m, err := raReadFrame(u.stream, rb)
					if err != nil {
						u.stream.Close()
						mu.Lock()
						if sess[to.String()] == u {
							delete(sess, to.String())
						}
						mu.Unlock()
						return
					}
					u.last.Store(time.Now().UnixMilli())
					if _, err := pc.WriteTo(rb[:m], to); err == nil {
						e.down.Add(int64(m))
					}
				}
			}(u, addr)
		}
		u.last.Store(time.Now().UnixMilli())
		if raWriteFrame(u.stream, buf[:n]) == nil {
			e.up.Add(int64(n))
		}
	}
}

// --- web mode ---

const raKeyParam = "h2ra_key"

func raCookieName(id string) string { return "h2ra_" + id }

// remoteAccessWeb serves <base>/rweb/<id>/...: an entry link (with its key)
// sets the visitor's cookie; requests with that cookie go to the target.
func (s *Server) remoteAccessWeb(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/rweb/")
	id, _, _ := strings.Cut(rest, "/")
	e := s.raGet(id)
	if e == nil || e.Mode != "web" {
		http.Error(w, "remote access closed", 404)
		return
	}
	prefix := s.basePath + "/rweb/" + id
	if r.URL.Path == "/rweb/"+id {
		http.Redirect(w, r, prefix+"/", http.StatusFound)
		return
	}
	if k := r.URL.Query().Get(raKeyParam); k != "" {
		e.mu.Lock()
		ok := e.key != "" && k == e.key && time.Now().Before(e.keyExp)
		if ok {
			e.key = ""
			if e.cookie == "" {
				e.cookie = s.generateToken()
			}
		}
		cookie := e.cookie
		e.mu.Unlock()
		if !ok {
			http.Error(w, "this link was used or has expired; open it again from hy2scale", 403)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: raCookieName(id), Value: cookie, Path: prefix + "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
		q := r.URL.Query()
		q.Del(raKeyParam)
		loc := prefix + strings.TrimPrefix(r.URL.EscapedPath(), "/rweb/"+id)
		if enc := q.Encode(); enc != "" {
			loc += "?" + enc
		}
		http.Redirect(w, r, loc, http.StatusFound)
		return
	}
	c, err := r.Cookie(raCookieName(id))
	e.mu.Lock()
	ok := err == nil && e.cookie != "" && c.Value == e.cookie
	if ok {
		e.client = r.RemoteAddr
	}
	e.mu.Unlock()
	if !ok {
		http.Error(w, "open this address from hy2scale", 403)
		return
	}
	e.active.Add(1)
	defer e.active.Add(-1)
	e.proxy.ServeHTTP(w, r)
}

// raRootAttr: a root-relative URL in an HTML attribute.
var raRootAttr = regexp.MustCompile(`(?i)(\s(?:href|src|action|formaction|poster)\s*=\s*["'])/([^/"'])`)

// raWebProxy: the reverse proxy of a web entry to u.
func (s *Server) raWebProxy(e *raEntry, u *url.URL) *httputil.ReverseProxy {
	prefix := s.basePath + "/rweb/" + e.ID
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			e.mu.Lock()
			client := e.client
			e.mu.Unlock()
			c, err := s.raDial(ctx, e, "tcp", e.hostport, client)
			if err != nil {
				s.raDialFailed(e, err)
				return nil, err
			}
			if !e.track(c) {
				c.Close()
				return nil, errors.New("remote access closed")
			}
			return &raTrackedConn{Conn: c, e: e}, nil
		},
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true, ServerName: u.Hostname()},
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
		DisableCompression:  true,
	}
	e.closers = append(e.closers, tr.CloseIdleConnections)
	targetOrigin := u.Scheme + "://" + u.Host
	return &httputil.ReverseProxy{
		Transport:     tr,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			in := pr.In
			path := strings.TrimPrefix(in.URL.EscapedPath(), "/rweb/"+e.ID)
			if path == "" {
				path = "/"
			}
			out := pr.Out
			out.URL.Scheme, out.URL.Host = u.Scheme, u.Host
			out.URL.Path, out.URL.RawPath = "", ""
			if p, err := url.PathUnescape(path); err == nil {
				out.URL.Path, out.URL.RawPath = p, path
			}
			out.Host = u.Host
			// this node's cookies stay here
			var keep []string
			for _, c := range in.Cookies() {
				if c.Name == "hy2_session" || strings.HasPrefix(c.Name, "h2ra_") {
					continue
				}
				keep = append(keep, c.Name+"="+c.Value)
			}
			out.Header.Del("Cookie")
			if len(keep) > 0 {
				out.Header.Set("Cookie", strings.Join(keep, "; "))
			}
			// gzip only (the HTML rewrite reads it), and only if the
			// visitor takes it: other answers pass through as they come
			if strings.Contains(strings.ToLower(in.Header.Get("Accept-Encoding")), "gzip") {
				out.Header.Set("Accept-Encoding", "gzip")
			} else {
				out.Header.Del("Accept-Encoding")
			}
			here := "https://" + in.Host
			if in.TLS == nil {
				here = "http://" + in.Host
			}
			if o := out.Header.Get("Origin"); o != "" && strings.EqualFold(o, here) {
				out.Header.Set("Origin", targetOrigin)
			}
			if ref := out.Header.Get("Referer"); strings.HasPrefix(ref, here+prefix) {
				out.Header.Set("Referer", targetOrigin+strings.TrimPrefix(ref, here+prefix))
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			h := resp.Header
			h.Del("Strict-Transport-Security")
			h.Del("Clear-Site-Data")
			if loc := h.Get("Location"); loc != "" {
				h.Set("Location", raRewriteLocation(loc, u, prefix))
			}
			if cs := h.Values("Set-Cookie"); len(cs) > 0 {
				h.Del("Set-Cookie")
				for _, c := range cs {
					h.Add("Set-Cookie", raRewriteCookie(c, prefix))
				}
			}
			if !strings.Contains(strings.ToLower(h.Get("Content-Type")), "text/html") {
				return nil
			}
			var rd io.Reader = resp.Body
			if strings.EqualFold(h.Get("Content-Encoding"), "gzip") {
				zr, err := gzip.NewReader(resp.Body)
				if err != nil {
					return nil
				}
				rd = zr
			} else if h.Get("Content-Encoding") != "" {
				return nil
			}
			body, err := io.ReadAll(io.LimitReader(rd, 32<<20))
			resp.Body.Close()
			if err != nil {
				return err
			}
			body = raRootAttr.ReplaceAll(body, []byte("${1}"+prefix+"/${2}"))
			h.Del("Content-Encoding")
			h.Del("Content-Length")
			resp.ContentLength = int64(len(body))
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "remote access: "+err.Error(), 502)
		},
	}
}

// raRewriteLocation keeps a redirect to the target inside the entry.
func raRewriteLocation(loc string, target *url.URL, prefix string) string {
	lu, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	if lu.IsAbs() {
		if !strings.EqualFold(lu.Host, target.Host) && !strings.EqualFold(lu.Hostname(), target.Host) {
			return loc
		}
		p := lu.EscapedPath()
		if p == "" {
			p = "/"
		}
		if lu.RawQuery != "" {
			p += "?" + lu.RawQuery
		}
		return prefix + p
	}
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
		return prefix + loc
	}
	return loc
}

// raRewriteCookie scopes a cookie of the target to the entry.
func raRewriteCookie(c, prefix string) string {
	parts := strings.Split(c, ";")
	out := parts[:1]
	hasPath := false
	for _, p := range parts[1:] {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		switch strings.ToLower(k) {
		case "domain":
			continue
		case "path":
			hasPath = true
			if !strings.HasPrefix(v, "/") {
				v = "/"
			}
			out = append(out, " Path="+prefix+v)
			continue
		}
		out = append(out, p)
	}
	if !hasPath {
		out = append(out, " Path="+prefix+"/")
	}
	return strings.Join(out, ";")
}

// raTrackedConn: a web-mode connection that leaves the entry's set when
// closed.
type raTrackedConn struct {
	net.Conn
	e    *raEntry
	once sync.Once
}

func (c *raTrackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.e.down.Add(int64(n))
	return n, err
}

func (c *raTrackedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.e.up.Add(int64(n))
	return n, err
}

func (c *raTrackedConn) Close() error {
	c.once.Do(func() { c.e.untrack(c.Conn) })
	return c.Conn.Close()
}

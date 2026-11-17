// Local control socket: the API of this node, and of the nodes it manages,
// for tools running on the same host (the "hy2scale ctl" command).
//
// The socket lives in <data>/ctl/ with mode 0600, so only the account the
// node runs as (root in the container) can connect; that is the
// authentication. A request on it needs no session token.
//
// Other nodes are reached through the relay, signed in the way this node's
// web UI would: relay trust (the remote's "relay admin passthrough"), else a
// sign-in saved when the user signed in to that node from this node's web
// UI (or with "ctl login"). Saved sign-ins are only ever used from here.

package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ctlCtxKey marks a request that arrived on the control socket.
type ctlCtxKey struct{}

func fromCtl(r *http.Request) bool { return r.Context().Value(ctlCtxKey{}) != nil }

func (s *Server) ctlDir() string { return filepath.Join(s.app.DataDir(), "ctl") }

// CtlSocket: the control socket of the node using dataDir.
func CtlSocket(dataDir string) string { return filepath.Join(dataDir, "ctl", "hy2scale.sock") }

const (
	ctlMaxBody     = 64 << 20 // request body taken by a call
	ctlMeshMaxBody = 4 << 20  // answer kept per node in a fan-out
	ctlMeshWorkers = 8
)

// ctlLogin: a saved sign-in to another node.
type ctlLogin struct {
	Chain    []string  `json:"chain"`
	Username string    `json:"username"`
	Password string    `json:"password"` // SHA-256 hex, as /api/login takes it
	Saved    time.Time `json:"saved"`
	Source   string    `json:"source"` // "web" or "ctl"
}

type ctlState struct {
	mu     sync.Mutex
	logins map[string]ctlLogin // by node name
	tokens map[string]string   // chain → session token from a saved sign-in
	loaded bool
}

func (s *Server) ctlLoginsPath() string { return filepath.Join(s.ctlDir(), "logins.json") }

func (s *Server) ctlLoad() {
	if s.ctl.loaded {
		return
	}
	s.ctl.loaded = true
	s.ctl.logins = map[string]ctlLogin{}
	s.ctl.tokens = map[string]string{}
	if b, err := os.ReadFile(s.ctlLoginsPath()); err == nil {
		json.Unmarshal(b, &s.ctl.logins)
	}
}

func (s *Server) ctlSave() {
	os.MkdirAll(s.ctlDir(), 0o700)
	b, _ := json.MarshalIndent(s.ctl.logins, "", "  ")
	tmp := s.ctlLoginsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		os.Rename(tmp, s.ctlLoginsPath())
	}
}

// rememberLogin saves a sign-in to the node at the end of chain.
func (s *Server) rememberLogin(chain []string, user, hash, source string) {
	if len(chain) == 0 || user == "" || hash == "" {
		return
	}
	name := chain[len(chain)-1]
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	s.ctlLoad()
	old, had := s.ctl.logins[name]
	s.ctl.logins[name] = ctlLogin{Chain: append([]string(nil), chain...), Username: user, Password: hash, Saved: time.Now().UTC(), Source: source}
	delete(s.ctl.tokens, strings.Join(chain, "/"))
	s.ctlSave()
	if !had || old.Username != user || old.Password != hash {
		log.Printf("[ctl] sign-in to %s saved (%s)", strings.Join(chain, "/"), source)
	}
}

// forgetLogin drops the saved sign-in to the node named name.
func (s *Server) forgetLogin(name string) bool {
	s.ctl.mu.Lock()
	defer s.ctl.mu.Unlock()
	s.ctlLoad()
	l, ok := s.ctl.logins[name]
	if !ok {
		return false
	}
	delete(s.ctl.logins, name)
	delete(s.ctl.tokens, strings.Join(l.Chain, "/"))
	s.ctlSave()
	log.Printf("[ctl] saved sign-in to %s removed", name)
	return true
}

// remoteSignIn watches this node's remote proxy: a successful sign-in to a
// node from this node's web UI is saved for the control socket, a sign-out
// removes it. body: the request body of /api/login (nil otherwise).
func (s *Server) remoteSignIn(chain []string, remotePath string, method string, body []byte, status int) {
	if method != http.MethodPost || status != http.StatusOK {
		return
	}
	switch remotePath {
	case "/scale/api/login":
		var b struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if json.Unmarshal(body, &b) == nil {
			s.rememberLogin(chain, b.Username, b.Password, "web")
		}
	case "/scale/api/logout":
		s.forgetLogin(chain[len(chain)-1])
	}
}

// startCtl serves the control socket (until ctx ends). root: the node's
// HTTP handler.
func (s *Server) startCtl(ctx context.Context, root http.Handler) {
	dir := s.ctlDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[ctl] %v", err)
		return
	}
	os.Chmod(dir, 0o700)
	sock := CtlSocket(s.app.DataDir())
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		log.Printf("[ctl] control socket: %v", err)
		return
	}
	os.Chmod(sock, 0o600)

	m := http.NewServeMux()
	m.HandleFunc("GET /ctl/v1/nodes", s.ctlNodes)
	m.HandleFunc("/ctl/v1/call", func(w http.ResponseWriter, r *http.Request) { s.ctlCall(w, r, root) })
	m.HandleFunc("/ctl/v1/mesh", func(w http.ResponseWriter, r *http.Request) { s.ctlMesh(w, r, root) })
	m.HandleFunc("GET /ctl/v1/logins", s.ctlLogins)
	m.HandleFunc("POST /ctl/v1/logins", s.ctlAddLogin)
	m.HandleFunc("DELETE /ctl/v1/logins", func(w http.ResponseWriter, r *http.Request) {
		if !s.forgetLogin(r.URL.Query().Get("node")) {
			http.Error(w, "no saved sign-in for that node", 404)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	// Anything else: this node's own API and UI paths.
	m.Handle("/", root)

	srv := &http.Server{
		Handler:     m,
		ConnContext: func(c context.Context, _ net.Conn) context.Context { return context.WithValue(c, ctlCtxKey{}, true) },
	}
	go func() { <-ctx.Done(); srv.Close(); os.Remove(sock) }()
	go srv.Serve(ln)
}

// ctlChain: the chain of --node: "A/B/C" as given, a single name resolved
// to the shortest manageable chain (or itself).
func (s *Server) ctlChain(node string) []string {
	node = strings.Trim(node, "/")
	if node == "" {
		return nil
	}
	if strings.Contains(node, "/") {
		return strings.Split(node, "/")
	}
	if c, ok := s.manageable()[node]; ok {
		return c.Chain
	}
	return []string{node}
}

type ctlNode struct {
	Name    string   `json:"name"`
	Chain   []string `json:"chain"`
	Version string   `json:"version,omitempty"`
	Self    bool     `json:"self,omitempty"`
}

// manageable: the nodes this node can manage and the shortest chain to
// each, by name, from the topology (the web UI's rule: online, not
// disabled, not native, not reached by node access by user, compatible,
// behind nested-enabled hops only).
func (s *Server) manageable() map[string]ctlNode {
	type tn struct {
		Name         string `json:"name"`
		IsSelf       bool   `json:"is_self"`
		Connected    bool   `json:"connected"`
		Disabled     bool   `json:"disabled"`
		Native       bool   `json:"native"`
		Restricted   bool   `json:"restricted"`
		Invisible    bool   `json:"invisible"`
		Incompatible bool   `json:"incompatible"`
		Nested       bool   `json:"nested"`
		Version      string `json:"version"`
		Children     []tn   `json:"children"`
	}
	rec := httptest.NewRecorder()
	s.getTopology(rec, httptest.NewRequest("GET", "/api/topology", nil))
	var topo []tn
	json.Unmarshal(rec.Body.Bytes(), &topo)
	type item struct {
		n     tn
		chain []string
	}
	var level []item
	selfName := ""
	for _, n := range topo {
		if n.IsSelf {
			selfName = n.Name
			for _, c := range n.Children {
				level = append(level, item{c, []string{c.Name}})
			}
		}
	}
	for _, n := range topo {
		if !n.IsSelf {
			level = append(level, item{n, []string{n.Name}})
		}
	}
	out := map[string]ctlNode{}
	for len(level) > 0 {
		var next []item
		for _, it := range level {
			n := it.n
			if !n.Connected || n.Disabled || n.Native || n.Restricted || n.Invisible || n.Incompatible || n.Name == selfName {
				continue
			}
			if _, ok := out[n.Name]; !ok {
				out[n.Name] = ctlNode{Name: n.Name, Chain: it.chain, Version: n.Version}
			}
			if !n.Nested {
				continue
			}
		children:
			for _, c := range n.Children {
				for _, p := range it.chain {
					if p == c.Name {
						continue children
					}
				}
				next = append(next, item{c, append(append([]string(nil), it.chain...), c.Name)})
			}
		}
		level = next
	}
	return out
}

func (s *Server) sortedNodes() []ctlNode {
	m := s.manageable()
	out := make([]ctlNode, 0, len(m))
	for _, n := range m {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Chain) != len(out[j].Chain) {
			return len(out[i].Chain) < len(out[j].Chain)
		}
		return strings.Join(out[i].Chain, "/") < strings.Join(out[j].Chain, "/")
	})
	return out
}

// GET /ctl/v1/nodes[?probe=1] — this node and the nodes it manages; probe:
// how each one can be reached ("trust", "login", "none" or an error).
func (s *Server) ctlNodes(w http.ResponseWriter, r *http.Request) {
	type row struct {
		ctlNode
		SavedLogin bool   `json:"saved_login,omitempty"`
		Access     string `json:"access,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	nodes := s.sortedNodes()
	s.ctl.mu.Lock()
	s.ctlLoad()
	rows := []row{{ctlNode: ctlNode{Name: s.app.Store().Get().NodeID, Version: Version, Self: true}, Access: "local"}}
	for _, n := range nodes {
		_, saved := s.ctl.logins[n.Name]
		rows = append(rows, row{ctlNode: n, SavedLogin: saved})
	}
	s.ctl.mu.Unlock()
	if r.URL.Query().Get("probe") == "1" {
		var wg sync.WaitGroup
		sem := make(chan struct{}, ctlMeshWorkers)
		for i := 1; i < len(rows); i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
				defer cancel()
				resp, access, err := s.remoteDo(ctx, rows[i].Chain, "GET", "/api/stats", nil, nil)
				switch {
				case err != nil:
					rows[i].Error = err.Error()
				case resp.StatusCode == 200:
					rows[i].Access = access
				case resp.StatusCode == 401:
					rows[i].Access = "none"
				default:
					rows[i].Error = resp.Status
				}
				if resp != nil {
					resp.Body.Close()
				}
			}(i)
		}
		wg.Wait()
	}
	writeJSON(w, rows)
}

// GET /ctl/v1/logins — the saved sign-ins (no passwords).
func (s *Server) ctlLogins(w http.ResponseWriter, r *http.Request) {
	s.ctl.mu.Lock()
	s.ctlLoad()
	out := []map[string]any{}
	for name, l := range s.ctl.logins {
		out = append(out, map[string]any{"node": name, "chain": l.Chain, "username": l.Username, "saved": l.Saved, "source": l.Source})
	}
	s.ctl.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i]["node"].(string) < out[j]["node"].(string) })
	writeJSON(w, out)
}

// POST /ctl/v1/logins {node, username, password_hash} — sign in to a node
// and save the sign-in when it is accepted.
func (s *Server) ctlAddLogin(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Node     string `json:"node"`
		Username string `json:"username"`
		Hash     string `json:"password_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Node == "" || b.Username == "" || b.Hash == "" {
		http.Error(w, "node, username and password_hash required", 400)
		return
	}
	chain := s.ctlChain(b.Node)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := s.remoteLogin(ctx, chain, b.Username, b.Hash); err != nil {
		http.Error(w, err.Error(), 401)
		return
	}
	s.rememberLogin(chain, b.Username, b.Hash, "ctl")
	writeJSON(w, map[string]any{"status": "ok", "chain": chain})
}

// remoteOpen sends one request to the API of the node at the end of chain
// and returns its response (the caller closes the body). token: a session
// token of that node ("" for none).
func (s *Server) remoteOpen(ctx context.Context, chain []string, method, path string, body []byte, hdr http.Header, token string) (*http.Response, error) {
	chain = append([]string(nil), chain...)
	s.mu.RLock()
	for i, name := range chain {
		if newID, ok := s.oldNodeIDs[name]; ok {
			chain[i] = newID
		}
	}
	s.mu.RUnlock()
	if len(chain) == 0 {
		return nil, fmt.Errorf("empty chain")
	}
	if s.app.Node().IsInvisible(chain[0]) {
		return nil, fmt.Errorf("not over node access by user")
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := s.dialRemoteAPI(dctx, chain)
	cancel()
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	req, _ := http.NewRequest(method, "http://127.0.0.1:5565/scale"+path, bytes.NewReader(body))
	req.Host = "127.0.0.1:5565"
	for k, vv := range hdr {
		req.Header[k] = vv
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.ContentLength = int64(len(body))
	req.Close = true
	if err := req.Write(conn); err != nil {
		stop()
		conn.Close()
		return nil, fmt.Errorf("write to %s: %v", strings.Join(chain, "/"), err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		stop()
		conn.Close()
		return nil, fmt.Errorf("no answer from %s: %v", strings.Join(chain, "/"), err)
	}
	resp.Body = &closeBoth{ReadCloser: resp.Body, conn: conn, stop: stop}
	return resp, nil
}

type closeBoth struct {
	io.ReadCloser
	conn net.Conn
	stop func() bool
}

func (c *closeBoth) Close() error {
	c.stop()
	c.ReadCloser.Close()
	return c.conn.Close()
}

// remoteLogin signs in to the node at the end of chain.
func (s *Server) remoteLogin(ctx context.Context, chain []string, user, hash string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": user, "password": hash})
	resp, err := s.remoteOpen(ctx, chain, "POST", "/api/login", body, http.Header{"Content-Type": {"application/json"}}, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("sign-in to %s refused: %s", strings.Join(chain, "/"), strings.TrimSpace(string(b)))
	}
	var out struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(b, &out) != nil || out.Token == "" {
		return "", fmt.Errorf("sign-in to %s: no session in the answer", strings.Join(chain, "/"))
	}
	return out.Token, nil
}

// remoteDo sends a request to the node at the end of chain, signed in the
// way that node accepts: a session from an earlier saved sign-in, relay
// trust, or the saved sign-in. Returns the response and how it was signed
// in ("token", "trust", "login"); a 401 response when none works.
func (s *Server) remoteDo(ctx context.Context, chain []string, method, path string, body []byte, hdr http.Header) (*http.Response, string, error) {
	key := strings.Join(chain, "/")
	s.ctl.mu.Lock()
	s.ctlLoad()
	tok := s.ctl.tokens[key]
	login, haveLogin := s.ctl.logins[chain[len(chain)-1]]
	s.ctl.mu.Unlock()

	try := func(t string) (*http.Response, error) { return s.remoteOpen(ctx, chain, method, path, body, hdr, t) }
	if tok != "" {
		resp, err := try(tok)
		if err != nil || resp.StatusCode != 401 {
			return resp, "token", err
		}
		resp.Body.Close()
		s.ctl.mu.Lock()
		delete(s.ctl.tokens, key)
		s.ctl.mu.Unlock()
	}
	resp, err := try("")
	if err != nil || resp.StatusCode != 401 || !haveLogin {
		return resp, "trust", err
	}
	resp.Body.Close()
	t, err := s.remoteLogin(ctx, chain, login.Username, login.Password)
	if err != nil {
		return nil, "", fmt.Errorf("saved sign-in: %v", err)
	}
	s.ctl.mu.Lock()
	s.ctl.tokens[key] = t
	s.ctl.mu.Unlock()
	resp, err = try(t)
	return resp, "login", err
}

// ctlTarget reads node, method and path of a call: path is an API path
// ("/api/..."), with its query.
func ctlTarget(r *http.Request) (node, method, path string, err error) {
	q := r.URL.Query()
	node, path = q.Get("node"), q.Get("path")
	method = q.Get("method")
	if method == "" {
		method = r.Method
	}
	method = strings.ToUpper(method)
	if !strings.HasPrefix(path, "/api/") {
		return "", "", "", fmt.Errorf("path: an API path expected (/api/...)")
	}
	if _, err := url.ParseRequestURI(path); err != nil || strings.ContainsAny(path, " \r\n#") {
		return "", "", "", fmt.Errorf("path: not a valid request path")
	}
	return node, method, path, nil
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// localDo serves a request to this node's own API in process.
func (s *Server) localDo(ctx context.Context, root http.Handler, method, path string, body []byte, hdr http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, s.basePath+path, bytes.NewReader(body)).WithContext(context.WithValue(ctx, ctlCtxKey{}, true))
	for k, vv := range hdr {
		req.Header[k] = vv
	}
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)
	return rec
}

func ctlHeader(r *http.Request) http.Header {
	h := http.Header{}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		h.Set("Content-Type", ct)
	}
	return h
}

func ctlLogCall(node, method, path string, status int, access string) {
	if method == http.MethodGet || method == http.MethodHead {
		return
	}
	where := node
	if where == "" {
		where = "this node"
	}
	log.Printf("[ctl] %s %s on %s: %d (%s)", method, path, where, status, access)
}

// /ctl/v1/call?node=A/B&method=GET&path=/api/... — one API request, to this
// node (no node) or another. The answer is the node's, streamed; a request
// that never reached it answers 502 with X-Hy2scale-Ctl-Error.
func (s *Server) ctlCall(w http.ResponseWriter, r *http.Request, root http.Handler) {
	if !fromCtl(r) {
		http.NotFound(w, r)
		return
	}
	node, method, path, err := ctlTarget(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, ctlMaxBody))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	hdr := ctlHeader(r)
	if node == "" {
		u := *r.URL
		u.Path, u.RawPath, u.RawQuery = s.basePath+path, "", ""
		if i := strings.IndexByte(path, '?'); i != -1 {
			u.Path, u.RawQuery = s.basePath+path[:i], path[i+1:]
		}
		req := r.Clone(r.Context())
		req.Method, req.URL, req.RequestURI = method, &u, u.RequestURI()
		req.Body, req.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
		req.Header = hdr
		w.Header().Set("X-Hy2scale-Access", "local")
		sw := &statusWriter{ResponseWriter: w, code: 200}
		root.ServeHTTP(sw, req)
		ctlLogCall("", method, path, sw.code, "local")
		return
	}
	chain := s.ctlChain(node)
	w.Header().Set("X-Hy2scale-Chain", strings.Join(chain, "/"))
	resp, access, err := s.remoteDo(r.Context(), chain, method, path, body, hdr)
	if err != nil {
		w.Header().Set("X-Hy2scale-Ctl-Error", "unreachable")
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	ctlLogCall(strings.Join(chain, "/"), method, path, resp.StatusCode, access)
	for k, vv := range resp.Header {
		switch strings.ToLower(k) {
		case "content-length", "transfer-encoding", "connection", "keep-alive":
			continue
		}
		w.Header()[k] = vv
	}
	w.Header().Set("X-Hy2scale-Access", access)
	if resp.StatusCode == 401 {
		w.Header().Set("X-Hy2scale-Ctl-Error", "needs_login")
	}
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// /ctl/v1/mesh?method=GET&path=/api/...[&nodes=a,b][&self=0][&timeout=30s]
// — the same request to every manageable node (or the listed ones) at
// once. Answers one row per node; a JSON answer is embedded as JSON.
func (s *Server) ctlMesh(w http.ResponseWriter, r *http.Request, root http.Handler) {
	if !fromCtl(r) {
		http.NotFound(w, r)
		return
	}
	_, method, path, err := ctlTarget(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	q := r.URL.Query()
	timeout := 30 * time.Second
	if d, err := time.ParseDuration(q.Get("timeout")); err == nil && d > 0 {
		timeout = d
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, ctlMaxBody))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	hdr := ctlHeader(r)

	type row struct {
		Node      string          `json:"node"`
		Chain     []string        `json:"chain,omitempty"`
		Version   string          `json:"version,omitempty"`
		Access    string          `json:"access,omitempty"`
		Status    int             `json:"status,omitempty"`
		Body      json.RawMessage `json:"body,omitempty"`
		Text      string          `json:"text,omitempty"`
		Truncated bool            `json:"truncated,omitempty"`
		Error     string          `json:"error,omitempty"`
	}
	var rows []row
	if q.Get("self") != "0" {
		rows = append(rows, row{Node: s.app.Store().Get().NodeID, Version: Version, Access: "local"})
	}
	all := s.sortedNodes()
	if list := q.Get("nodes"); list != "" {
		byName := map[string]ctlNode{}
		for _, n := range all {
			byName[n.Name] = n
		}
		all = nil
		for _, name := range strings.Split(list, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if n, ok := byName[name]; ok {
				all = append(all, n)
			} else {
				all = append(all, ctlNode{Name: name, Chain: s.ctlChain(name)})
			}
		}
	}
	for _, n := range all {
		rows = append(rows, row{Node: n.Name, Chain: n.Chain, Version: n.Version})
	}

	fill := func(rw *row, status int, ct string, rd io.Reader) {
		rw.Status = status
		b, _ := io.ReadAll(io.LimitReader(rd, ctlMeshMaxBody+1))
		if len(b) > ctlMeshMaxBody {
			b, rw.Truncated = b[:ctlMeshMaxBody], true
		}
		if strings.Contains(ct, "json") && !rw.Truncated && json.Valid(b) {
			rw.Body = b
		} else {
			rw.Text = string(b)
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, ctlMeshWorkers)
	for i := range rows {
		wg.Add(1)
		go func(rw *row) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			if rw.Access == "local" {
				rec := s.localDo(ctx, root, method, path, body, hdr)
				ctlLogCall("", method, path, rec.Code, "local")
				fill(rw, rec.Code, rec.Header().Get("Content-Type"), rec.Body)
				return
			}
			resp, access, err := s.remoteDo(ctx, rw.Chain, method, path, body, hdr)
			if err != nil {
				rw.Error = err.Error()
				return
			}
			defer resp.Body.Close()
			rw.Access = access
			if resp.StatusCode == 401 {
				rw.Access = "none"
			}
			ctlLogCall(strings.Join(rw.Chain, "/"), method, path, resp.StatusCode, access)
			fill(rw, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Body)
		}(&rows[i])
	}
	wg.Wait()
	writeJSON(w, rows)
}

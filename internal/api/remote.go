// Core: management of other nodes through the mesh (_relay_api_ streams).

package api

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	relay "github.com/FrankoonG/hy2scale/core/node"
	"github.com/FrankoonG/hy2scale/internal/buildsig"
	"github.com/FrankoonG/hy2scale/internal/tools"
)

// relayCtxKey carries the hyserver authID of the QUIC connection that
// originated a request delivered through apiBridge ("" for non-relay TCP
// requests). Unexported struct type so external packages can neither synthesize
// the key nor inject a forged value via context.WithValue. The api server's
// relaySrv populates it via ConnContext from the *app.RelayAuthConn wrapping
// each pushed stream; authMiddleware reads it back to gate the
// RelayAdminPassthrough bypass on authID == "system".
type relayCtxKey struct{}

// relayAuthCarrier is the small interface satisfied by *app.RelayAuthConn —
// duck-typed here so this package doesn't have to import app/ for what is
// purely a metadata read.
type relayAuthCarrier interface {
	RelayAuthID() string
}

func relayConnContext(ctx context.Context, c net.Conn) context.Context {
	if rc, ok := c.(relayAuthCarrier); ok {
		return context.WithValue(ctx, relayCtxKey{}, rc.RelayAuthID())
	}
	return ctx
}

// relayAuthID returns the hyserver authID for the current request, or ""
// if the request didn't arrive via apiBridge (or arrived without an
// auth-tagged conn). authMiddleware uses this to decide whether to apply
// RelayAdminPassthrough.
// trustedRelay: a relay-delivered API request whose connection is trusted
// for management — a peer node (system password) or a node-access user in
// full mode (the gateway tags its streams "access:<user>").
func trustedRelay(authID string) bool {
	return authID == "system" || strings.HasPrefix(authID, "access:")
}

func relayAuthID(ctx context.Context) string {
	v, _ := ctx.Value(relayCtxKey{}).(string)
	return v
}

// streamListener is a net.Listener backed by a channel; each Accept returns a
// net.Conn that was fed via push(). Used so relay-delivered API streams can
// be served by the same *http.Server without a separate TCP socket.
type streamListener struct {
	ch     chan net.Conn
	closed chan struct{}
}

func newStreamListener() *streamListener {
	return &streamListener{ch: make(chan net.Conn, 16), closed: make(chan struct{})}
}

func (l *streamListener) push(c net.Conn) {
	select {
	case l.ch <- c:
	case <-l.closed:
		c.Close()
	default:
		// channel full — drop
		c.Close()
	}
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, fmt.Errorf("listener closed")
	}
}

func (l *streamListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *streamListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4zero, Port: 0}
}

// remoteProxy tunnels HTTP requests to a remote node's web UI through the relay chain.
// Path: /remote/{peer1}/{peer2}/.../{remaining_path}
// The last contiguous known-peer segment forms the chain; the rest is the proxied path.
// remoteProxy tunnels HTTP to a remote node's web UI.
// URL: /remote/{peer1}/{peer2}/.../{remaining}
// Maps to remote: /scale/{remaining} (auto-prepends remote's base path)
// Rewrites __BASE__ in HTML so JS API calls route back through the proxy.
func (s *Server) remoteProxy(w http.ResponseWriter, r *http.Request) {
	// Use the ESCAPED path so percent-encoded slashes inside a single
	// path segment (e.g. a sub-peer addressed as `aub%2Fcn`) survive the
	// split-rejoin we do below. r.URL.Path would have decoded the %2F
	// into a real "/" before we ever see it, fusing the sub-peer name
	// with its parent and producing a remote URL with too many segments
	// — making routes like `PUT /api/peers/{name}/nested` 404 because
	// `{name}` matches one segment, not two. r.URL.RawPath is populated
	// only when the request actually had escaping; EscapedPath() handles
	// both that case and the no-escaping fast path uniformly.
	rawPath := r.URL.EscapedPath()
	raw := strings.TrimPrefix(rawPath, "/remote/")
	if raw == "" || raw == "/" {
		http.Error(w, "usage: /remote/{peer}/{path}", 400)
		return
	}

	// Split into chain + remaining. Chain = contiguous peer names (no dots, no slashes in names).
	// First segment with a dot or known as a file extension ends the chain.
	// Splitting the escaped form means %2F-inside-a-segment is preserved
	// as part of that segment rather than promoted to a path separator.
	segments := strings.Split(strings.TrimSuffix(raw, "/"), "/")
	// Known path prefixes that mark the end of the peer chain.
	// The frontend builds links like "/scale/remote/{peer}/scale/..." so
	// "scale" must be recognized as a base-path sentinel, not a peer name.
	notPeer := map[string]bool{
		"api": true, "login": true, "nodes": true, "proxies": true,
		"tls": true, "settings": true, "remote": true,
		"scale": true, "users": true, "rules": true, "assets": true,
		"interfaces": true, "overview": true, "endpoints": true, "logs": true,
		"tools": true, "audit": true,
		// map data of a 1.4.0 pre-release (now under assets/)
		"geo": true,
	}
	chainLen := 0
	for _, seg := range segments {
		if seg == "" || strings.Contains(seg, ".") || notPeer[seg] {
			break
		}
		chainLen++
	}
	if chainLen == 0 {
		http.Error(w, "missing peer chain", 400)
		return
	}

	// Decode each chain segment back to its canonical peer-name form.
	// A chain segment is a SINGLE peer identifier — it never legitimately
	// contains "/" (the path separator) — so PathUnescape is lossless
	// here. We need the decoded form for n.peers lookups, oldNodeIDs
	// resolution, and the proxyBase URL (which we re-encode below as
	// part of URL.String()).
	chain := make([]string, chainLen)
	for i, seg := range segments[:chainLen] {
		if decoded, err := url.PathUnescape(seg); err == nil {
			chain[i] = decoded
		} else {
			chain[i] = seg
		}
	}
	// Resolve old node IDs to current ones
	s.mu.RLock()
	for i, name := range chain {
		if newID, ok := s.oldNodeIDs[name]; ok {
			chain[i] = newID
		}
	}
	s.mu.RUnlock()
	// Frontend emits /scale/remote/{chain}/scale/{remaining}. The literal
	// "scale" sentinel after the chain is the remote's base-path marker;
	// strip it so we don't end up with /scale/scale/... after prepending.
	rest := segments[chainLen:]
	if len(rest) > 0 && rest[0] == "scale" {
		rest = rest[1:]
	}
	remaining := strings.Join(rest, "/")
	// The remote's actual path: prepend their base path (/scale)
	// /remote/vm/ → remote /scale/
	// /remote/vm/scale/login → remote /scale/login
	// /remote/vm/scale/api/node → remote /scale/api/node
	remotePath := "/scale/" + remaining

	// Proxy base: what the remote's __BASE__ should point to
	proxyBase := s.basePath + "/remote/" + strings.Join(chain, "/")

	// A WebSocket (the terminal) passes through as an upgrade. The browser
	// sent its Origin to this node, so it is checked here; the remote gets
	// none.
	upgrade := tools.IsWebSocketUpgrade(r)
	if upgrade && !tools.SameOrigin(r) {
		http.Error(w, "cross-origin websocket refused", 403)
		return
	}

	// The timeout bounds the dial only; the stream lives as long as the
	// client's request (an upgraded connection: until either side closes).
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	conn, code, err := s.dialRemoteAPI(ctx, chain)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	defer conn.Close()
	if !upgrade {
		// a client that drops a long answer (NDJSON, SSE) ends the stream
		// at once instead of on the next chunk
		stop := context.AfterFunc(r.Context(), func() { conn.Close() })
		defer stop()
	}

	// Construct proxied request. Use a fresh header so we don't forward the
	// local API token (remote node authenticates independently) or Hop-by-Hop
	// headers that confuse it. Force HTTP/1.1 with Connection: close so the
	// remote sends a complete response-with-body and closes cleanly.
	outReq, _ := http.NewRequest(r.Method, "http://127.0.0.1:5565"+remotePath, r.Body)
	outReq.URL.RawQuery = r.URL.RawQuery
	outReq.Proto = "HTTP/1.1"
	outReq.ProtoMajor = 1
	outReq.ProtoMinor = 1
	outReq.Host = "127.0.0.1:5565"
	// Copy request headers. Forward Authorization so the client's remote
	// session token (stored in localStorage after the user logs into the
	// remote UI) reaches the target — the local node's token is never
	// used here because the frontend scopes tokens per __BASE__. Drop
	// hop-by-hop headers that can corrupt the proxied request.
	for k, vv := range r.Header {
		lk := strings.ToLower(k)
		if lk == "connection" || lk == "proxy-connection" ||
			lk == "keep-alive" || lk == "te" || lk == "trailers" || lk == "transfer-encoding" {
			continue
		}
		outReq.Header[k] = vv
	}
	outReq.Header.Set("X-Hy2scale-Proxy", "true")
	outReq.Close = true // force Connection: close in serialization
	outReq.ContentLength = r.ContentLength
	if upgrade {
		outReq.Close = false
		outReq.Header.Del("Origin")
		outReq.Header.Set("Connection", "Upgrade")
		outReq.Header.Set("Upgrade", r.Header.Get("Upgrade"))
		tools.SpliceUpgrade(w, outReq, conn)
		return
	}

	if err := outReq.Write(conn); err != nil {
		http.Error(w, "write to remote failed: "+err.Error(), 502)
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), outReq)
	if err != nil {
		http.Error(w, "bad response from remote: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	// Rewrite __BASE__ in HTML responses so JS API calls route through proxy
	ct := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(ct, "text/html")

	for k, vv := range resp.Header {
		lk := strings.ToLower(k)
		// Drop hop-by-hop headers and framing headers. Content-Length and
		// Transfer-Encoding from upstream are invalidated because:
		//   (a) we rewrite HTML bodies (length changes), and
		//   (b) we serve on a different HTTP/1.1 connection with its own
		//       framing that Go's net/http decides based on what we write.
		// Keeping the upstream Content-Length causes Go to reject Write()
		// calls that exceed it with "wrote more than the declared
		// Content-Length" and send zero body bytes to the client.
		if lk == "content-length" || lk == "transfer-encoding" ||
			lk == "connection" || lk == "keep-alive" ||
			lk == "proxy-connection" || lk == "trailer" || lk == "te" {
			continue
		}
		// Rewrite Location header for redirects
		if lk == "location" {
			for i, v := range vv {
				// Rewrite /scale/... → /scale/remote/{chain}/...
				if strings.HasPrefix(v, "/scale/") {
					vv[i] = proxyBase + "/" + strings.TrimPrefix(v, "/scale/")
				}
			}
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if isHTML {
		body, _ := io.ReadAll(resp.Body)
		// Rewrite __BASE__ to proxy path and inject __PROXY__
		html := strings.Replace(string(body),
			"window.__BASE__=\"/scale\"",
			"window.__BASE__=\""+proxyBase+"\";window.__PROXY__=true",
			1)
		w.Write([]byte(html))
	} else {
		// Stream non-HTML responses with an explicit flush after each
		// chunk. plain io.Copy(w, resp.Body) leaves chunks sitting in
		// Go's ~4 KB ResponseWriter buffer until enough data accumulates,
		// which made SSE endpoints (text/event-stream) silently fail
		// through the proxy: the remote graph-canvas snapshot is the
		// first event, ~100 bytes, so it never reached the browser and
		// the remote-graph node-drag positions appeared not to persist
		// on page reload.
		fl, _ := w.(http.Flusher)
		buf := make([]byte, 16*1024)
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
}

// dialRemoteAPI opens the management API stream of the node at the end of
// chain; on failure it returns the HTTP status to answer with.
func (s *Server) dialRemoteAPI(ctx context.Context, chain []string) (net.Conn, int, error) {
	// Native peers are vanilla hysteria2 servers: they have no hy2scale
	// relay-API listener on the other end of `_relay_api_:0`, so a tunnel
	// into them just hangs or 502s with a confusing error. English-only by
	// intent: this is a misuse path, not a localised user surface.
	if s.app.Node().NativeMap()[chain[len(chain)-1]] {
		return nil, 409, fmt.Errorf("Remote UI is not available for native peers. The selected node is a vanilla hysteria2 server and does not host the hy2scale management API. To route traffic through it, add a user with this peer set as Exit instead of opening it as a remote node.")
	}
	// Nodes reached by node access by user are traffic-only.
	if s.app.Node().IsRestricted(chain[0]) {
		return nil, 409, fmt.Errorf("Remote UI is not available through node access by user.")
	}
	var conn net.Conn
	var err error
	if len(chain) == 1 {
		conn, err = s.app.Node().DialTCP(ctx, chain[0], relay.StreamAPI)
	} else {
		conn, err = s.app.Node().DialVia(ctx, chain, relay.StreamAPI)
	}
	if err != nil {
		return nil, 502, fmt.Errorf("tunnel to %s failed: %v", strings.Join(chain, "/"), err)
	}
	return conn, 0, nil
}

// pushBinary installs this node's own binary on a remote node: the binary
// is packed as an upgrade archive and posted to the remote's upgrade
// endpoint over the management stream, signed in with the remote session
// token the caller holds. The remote checks the platform, stores the binary
// and restarts. The answer carries the remote's status and message; a
// remote 401 is not passed through as a 401 (that would sign the caller
// out of this node).
func (s *Server) pushBinary(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Chain []string `json:"chain"`
		Token string   `json:"token"`
		// The user accepted installing a binary that is not an official
		// build on an official remote.
		AllowUnofficial bool `json:"allow_unofficial"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Chain) == 0 {
		http.Error(w, "chain required", 400)
		return
	}
	chain := append([]string(nil), body.Chain...)
	s.mu.RLock()
	for i, name := range chain {
		if newID, ok := s.oldNodeIDs[name]; ok {
			chain[i] = newID
		}
	}
	s.mu.RUnlock()

	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	var f *os.File
	if err == nil {
		f, err = os.Open(exe)
	}
	if err != nil {
		http.Error(w, "own binary: "+err.Error(), 500)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "own binary: "+err.Error(), 500)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	conn, code, err := s.dialRemoteAPI(ctx, chain)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()

	// Its release signature goes along, so an official build stays one.
	sig, _ := os.ReadFile(buildsig.SigPath(exe))

	pr, pw := io.Pipe()
	defer pr.Close()
	go func() {
		gz := gzip.NewWriter(pw)
		tw := tar.NewWriter(gz)
		// Plain "hy2scale": the name every version takes as its own platform's.
		err := tw.WriteHeader(&tar.Header{
			Name: "hy2scale", Mode: 0o755,
			Size: fi.Size(), ModTime: fi.ModTime(), Typeflag: tar.TypeReg,
		})
		if err == nil {
			_, err = io.Copy(tw, f)
		}
		if err == nil && len(sig) > 0 {
			err = tw.WriteHeader(&tar.Header{Name: "hy2scale.sig", Mode: 0o644, Size: int64(len(sig)), ModTime: fi.ModTime(), Typeflag: tar.TypeReg})
			if err == nil {
				_, err = tw.Write(sig)
			}
		}
		if err == nil {
			err = tw.Close()
		}
		if err == nil {
			err = gz.Close()
		}
		pw.CloseWithError(err)
	}()

	target := "http://127.0.0.1:5565/scale/api/upgrade"
	if body.AllowUnofficial {
		target += "?allow_unofficial=1"
	}
	req, _ := http.NewRequest("POST", target, pr)
	req.Host = "127.0.0.1:5565"
	req.Header.Set("Content-Type", "application/gzip")
	if body.Token != "" {
		req.Header.Set("Authorization", "Bearer "+body.Token)
	}
	req.Close = true
	// The remote may answer before reading the whole body (refusal).
	go req.Write(conn)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		http.Error(w, "no answer from remote: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	log.Printf("[upgrade] pushed own binary (%s, %d bytes) to %s: %d", Version, fi.Size(), strings.Join(chain, "/"), resp.StatusCode)
	out := map[string]any{
		"ok":      resp.StatusCode == 200,
		"status":  resp.StatusCode,
		"message": strings.TrimSpace(string(msg)),
		"version": Version,
		"bytes":   fi.Size(),
	}
	// The remote runs an official build and this one is not: its warning.
	var un struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if resp.StatusCode == http.StatusConflict && json.Unmarshal(msg, &un) == nil && un.Error == "unofficial" {
		out["unofficial"] = true
		out["message"] = un.Reason
	}
	writeJSON(w, out)
}

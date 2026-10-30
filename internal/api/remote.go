// Core: management of other nodes through the mesh (_relay_api_ streams).

package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	relay "github.com/FrankoonG/hy2scale/core/node"
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

	// Native peers are vanilla hysteria2 servers — they have no hy2scale
	// relay-API listener on the other end of `_relay_api_:0`, so opening a
	// remote-UI tunnel into them just hangs or 502s with a confusing error.
	// v1.2's web UI suppressed this at the click; the React rewrite lost the
	// guard. Restore it here so a manually-typed `/remote/<native>/scale/`
	// URL also gets a clear refusal instead of a useless 502. English-only
	// by intent — this is a misuse path, not a localised user surface.
	nativeMap := s.app.Node().NativeMap()
	if nativeMap[chain[len(chain)-1]] {
		http.Error(w,
			"Remote UI is not available for native peers. The selected node is a vanilla hysteria2 server and does not host the hy2scale management API. To route traffic through it, add a user with this peer set as Exit instead of opening it as a remote node.",
			409)
		return
	}

	// Nodes reached by node access by user are traffic-only.
	if s.app.Node().IsRestricted(chain[0]) {
		http.Error(w, "Remote UI is not available through node access by user.", 409)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var conn net.Conn
	var err error
	if len(chain) == 1 {
		conn, err = s.app.Node().DialTCP(ctx, chain[0], relay.StreamAPI)
	} else {
		conn, err = s.app.Node().DialVia(ctx, chain, relay.StreamAPI)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("tunnel to %s failed: %v", strings.Join(chain, "/"), err), 502)
		return
	}
	defer conn.Close()

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

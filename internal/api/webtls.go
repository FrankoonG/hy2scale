package api

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
)

// Web UI TLS. The UI port speaks plain HTTP and TLS at the same time: the
// first byte of a connection decides (0x16 = TLS handshake record). TLS is
// served whenever Settings → Web has a usable certificate selected; with
// force_https on, plain HTTP requests are redirected to https. Both settings
// are read per connection / request, so a save applies without a restart.

const sniffTimeout = 30 * time.Second

var errNoWebCert = errors.New("web ui: no usable https certificate")

type webTLS struct {
	store *app.TLSStore
	conf  func() app.Config

	mu     sync.Mutex
	id     string
	mod    time.Time
	cert   *tls.Certificate
	badKey string // id+mtime of the last failed load, logged once
}

// certificate returns the selected certificate, reloading it when the id or
// the files change. nil when none is selected or it cannot be loaded.
func (w *webTLS) certificate() *tls.Certificate {
	id := w.conf().HTTPSCertID
	if id == "" {
		return nil
	}
	cp, kp := w.store.CertPath(id), w.store.KeyPath(id)
	ci, err1 := os.Stat(cp)
	ki, err2 := os.Stat(kp)
	if err1 != nil || err2 != nil {
		w.logBad(id, time.Time{}, "certificate files missing")
		return nil
	}
	mod := ci.ModTime()
	if ki.ModTime().After(mod) {
		mod = ki.ModTime()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cert != nil && w.id == id && w.mod.Equal(mod) {
		return w.cert
	}
	c, err := tls.LoadX509KeyPair(cp, kp)
	if err != nil {
		w.cert = nil
		w.logBadLocked(id, mod, err.Error())
		return nil
	}
	w.id, w.mod, w.cert = id, mod, &c
	return w.cert
}

func (w *webTLS) logBad(id string, mod time.Time, why string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.logBadLocked(id, mod, why)
}

func (w *webTLS) logBadLocked(id string, mod time.Time, why string) {
	k := id + "@" + mod.String()
	if w.badKey != k {
		w.badKey = k
		log.Printf("[web] https certificate %q unusable (%s): serving plain http only", id, why)
	}
}

// forced: plain HTTP must be redirected (force_https on and a usable cert).
func (w *webTLS) forced() bool {
	return w.conf().ForceHTTPS && w.certificate() != nil
}

func (w *webTLS) serverConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if c := w.certificate(); c != nil {
				return c, nil
			}
			return nil, errNoWebCert
		},
	}
}

// redirect wraps the public handler. Requests that already arrived over TLS,
// or behind a TLS-terminating proxy, pass through.
func (w *webTLS) redirect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") && w.forced() {
			if r.Host == "" {
				http.Error(rw, "https required", http.StatusBadRequest)
				return
			}
			// 307, not 308: a permanent redirect is cached by browsers and
			// would outlive turning the setting off again.
			http.Redirect(rw, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// sniffListener hands out plain conns and *tls.Conn (for http.Server to
// handshake) from one TCP listener. Classification runs per connection, off
// the accept loop, so a silent client cannot stall others.
type sniffListener struct {
	net.Listener
	web  *webTLS
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
	err  error
}

func newSniffListener(ln net.Listener, w *webTLS) *sniffListener {
	l := &sniffListener{Listener: ln, web: w, ch: make(chan net.Conn), done: make(chan struct{})}
	go l.acceptLoop()
	return l
}

func (l *sniffListener) acceptLoop() {
	var backoff time.Duration
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				backoff = min(max(2*backoff, 5*time.Millisecond), time.Second)
				time.Sleep(backoff)
				continue
			}
			l.shut(err)
			return
		}
		backoff = 0
		go l.classify(c)
	}
}

func (l *sniffListener) classify(c net.Conn) {
	var b [1]byte
	c.SetReadDeadline(time.Now().Add(sniffTimeout))
	if _, err := io.ReadFull(c, b[:]); err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	var out net.Conn = &prefixConn{Conn: c, head: b[:]}
	if b[0] == 0x16 {
		if l.web.certificate() == nil {
			c.Close()
			return
		}
		out = tls.Server(out, l.web.serverConfig())
	}
	select {
	case l.ch <- out:
	case <-l.done:
		c.Close()
	}
}

func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, l.err
	}
}

func (l *sniffListener) shut(err error) {
	l.once.Do(func() {
		l.err = err
		close(l.done)
	})
}

func (l *sniffListener) Close() error {
	l.shut(net.ErrClosed)
	return l.Listener.Close()
}

// prefixConn replays the sniffed byte before reading from the socket.
type prefixConn struct {
	net.Conn
	head []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.head) > 0 {
		n := copy(p, c.head)
		c.head = c.head[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

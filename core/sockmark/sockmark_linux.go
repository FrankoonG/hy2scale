//go:build linux

package sockmark

import (
	"context"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// denied: setting SO_MARK failed with EPERM once (no CAP_NET_ADMIN); later
// sockets are not tried again.
var denied atomic.Bool

var deniedOnce sync.Once

// Control is a net.Dialer / net.ListenConfig Control hook setting SO_MARK.
// It never fails the dial: without the privilege the socket stays
// unmarked.
func Control(_, _ string, c syscall.RawConn) error {
	if denied.Load() {
		return nil
	}
	c.Control(func(fd uintptr) { set(int(fd)) })
	return nil
}

func set(fd int) {
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_MARK, Mark); err != nil {
		if err == syscall.EPERM || err == syscall.EACCES {
			denied.Store(true)
			deniedOnce.Do(func() { log.Printf("[sockmark] SO_MARK not permitted: sockets unmarked") })
		}
	}
}

// Conn marks an existing socket (anything with SyscallConn).
func Conn(c any) {
	sc, ok := c.(syscall.Conn)
	if !ok || denied.Load() {
		return
	}
	if rc, err := sc.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) { set(int(fd)) })
	}
}

// Available reports whether this process may set SO_MARK.
var Available = sync.OnceValue(func() bool {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	return syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_MARK, Mark) == nil
})

var installOnce sync.Once

// Install marks the process-wide defaults: net.DefaultResolver (the pure-Go
// resolver, dialling its servers marked) and http.DefaultTransport (and so
// http.DefaultClient and every clone made afterwards).
func Install() {
	installOnce.Do(func() {
		net.DefaultResolver.PreferGo = true
		net.DefaultResolver.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			return Dialer(net.Dialer{}).DialContext(ctx, network, address)
		}
		if t, ok := http.DefaultTransport.(*http.Transport); ok {
			t.DialContext = Dialer(net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		}
	})
}

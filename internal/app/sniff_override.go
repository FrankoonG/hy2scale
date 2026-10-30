package app

import (
	"context"
	"net"
	"time"

	"github.com/FrankoonG/hy2scale/core/exit"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// Host-name recovery for flows whose client asked for an IP: core/exit
// (sniff.go). This file keeps the VPN / WireGuard side, where the flow is
// a forwarded SYN of a userspace stack.

type flowRoute = exit.RouteFunc

func peekHello(conn net.Conn, prefix []byte) ([]byte, bool) { return exit.PeekHello(conn, prefix) }

func wantSniff(dst string) bool { return exit.WantSniff(dst) }

func isLoopbackAddr(addr net.Addr) bool { return exit.IsLoopbackAddr(addr) }

// exitRoute is the route of a user flow: its own exit, else the matching
// rule's exit, else here.
func (a *App) exitRoute(exitVia string, exitPaths []string, exitMode string) flowRoute {
	return func(addr string) (string, []string, string, error) {
		return a.routeFor(exitVia, exitPaths, exitMode, addr)
	}
}

func (a *App) dialSniffedTCP(ctx context.Context, route flowRoute, here func(context.Context, string) (net.Conn, error),
	dst string, peeked []byte, waited, localClient bool) (net.Conn, error) {
	return a.exit.DialSniffed(ctx, route, here, dst, peeked, waited, localClient)
}

func newDeferredDialConn(dial func(peeked []byte, waited bool) (net.Conn, error)) net.Conn {
	return exit.NewDeferredDial(dial)
}

// sniffAfterAccept reports whether a VPN or WireGuard flow to dst is
// accepted before it is dialled, so the client's hello can be read: IPv4
// web ports only. Every other flow is dialled before its SYN is answered, so
// a destination the exit cannot reach is refused at once (an IPv6 client
// then falls back to IPv4; polluted answers are IPv4 anyway).
func sniffAfterAccept(dst string) bool {
	host, port, err := net.SplitHostPort(dst)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return false
	}
	switch port {
	case "80", "443", "8080", "8443":
		return wantSniff(dst)
	}
	return false
}

// acceptForwarded answers a forwarded SYN; nil when the endpoint could not
// be created (the SYN is refused).
func acceptForwarded(r *tcp.ForwarderRequest, tag, src, dst string) (*gonet.TCPConn, tcpip.Endpoint) {
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		debugLog("%s %s → %s: %v", tag, src, dst, terr)
		r.Complete(true)
		return nil, nil
	}
	r.Complete(false)
	ep.SocketOptions().SetKeepAlive(true)
	return gonet.NewTCPConn(&wq, ep), ep
}

// dialAccepted reads the hello of an accepted flow to dst, dials it and
// forwards the hello. On failure the client connection is reset.
func (a *App) dialAccepted(ctx context.Context, conn *gonet.TCPConn, ep tcpip.Endpoint, route flowRoute, dst string) (net.Conn, bool) {
	peeked, waited := peekHello(conn, nil)
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	remote, err := a.dialSniffedTCP(dctx, route, nil, dst, peeked, waited, false)
	cancel()
	if err == nil && len(peeked) > 0 {
		if _, err = remote.Write(peeked); err != nil {
			remote.Close()
		}
	}
	if err != nil {
		debugLog("[sniff] %s: %v", dst, err)
		ep.Abort()
		conn.Close()
		return nil, false
	}
	return remote, true
}

package app

import (
	"context"
	"net"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/exit"
	"github.com/FrankoonG/hy2scale/core/policy"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// Host-name recovery for flows whose client asked for an IP: core/exit
// (sniff.go). This file keeps the VPN / WireGuard side, where the flow is
// a forwarded SYN of a userspace stack.

func peekHello(conn net.Conn, prefix []byte) ([]byte, bool) { return exit.PeekHello(conn, prefix) }

func wantSniff(dst string) bool { return exit.WantSniff(dst) }

func isLoopbackAddr(addr net.Addr) bool { return exit.IsLoopbackAddr(addr) }

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
func (a *App) dialAccepted(ctx context.Context, conn *gonet.TCPConn, ep tcpip.Endpoint, route *routed, dst string) (net.Conn, bool) {
	peeked, waited := peekHello(conn, nil)
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	remote, err := a.dialSniffedTCP(dctx, route, dst, peeked, waited, false)
	cancel()
	if err == nil && len(peeked) > 0 {
		if _, err = remote.Write(peeked); err != nil {
			remote.Close()
		} else {
			audit.FromContext(ctx).Add(int64(len(peeked)), 0)
		}
	}
	if err != nil {
		debugLog("[sniff] %s: %v", dst, err)
		if _, deny := policy.AsDeny(err); !deny || isReject(err) {
			ep.Abort() // RST; a policy drop just closes
		}
		conn.Close()
		return nil, false
	}
	return remote, true
}

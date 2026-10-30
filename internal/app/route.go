package app

import (
	"context"
	"fmt"
	"net"
)

// Routing of a user's flow (VPN, WireGuard, SOCKS5/HTTP/SS clients):
//
//   - the user has its own exit: every flow leaves through it, rules do not
//     apply (the exit is exclusive);
//   - the user is direct: a routing rule that matches the destination sends
//     the flow through the rule's exit, and only there (fail closed);
//   - otherwise the flow is dialled directly from this node, through its
//     egress (egress.go).
//
// Rules match IP destinations against IP/CIDR/range targets (IPv4 and IPv6)
// and host names against domain-rule targets (the name or a subdomain).

// dialRoutedTCP dials addr ("host:port", host an IP or a name) for a user
// whose own exit is exitVia/exitPaths/exitMode ("" = direct).
func (a *App) dialRoutedTCP(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, error) {
	via, paths, mode, err := a.routeFor(exitVia, exitPaths, exitMode, addr)
	if err != nil {
		return nil, err
	}
	if via == "" {
		return a.dialDirect(ctx, "tcp", addr)
	}
	return a.dialExitWithPaths(ctx, via, paths, mode, addr)
}

// dialRoutedUDP is the UDP counterpart of dialRoutedTCP: one connected
// datagram flow to addr.
func (a *App) dialRoutedUDP(ctx context.Context, exitVia string, exitPaths []string, exitMode, addr string) (net.Conn, error) {
	via, paths, mode, err := a.routeFor(exitVia, exitPaths, exitMode, addr)
	if err != nil {
		return nil, err
	}
	if via == "" {
		return a.dialDirect(ctx, "udp", addr)
	}
	return a.dialExitUDPPaths(ctx, via, paths, mode, addr)
}

// routeFor resolves the exit a user flow to addr takes ("" = direct).
func (a *App) routeFor(exitVia string, exitPaths []string, exitMode, addr string) (string, []string, string, error) {
	if exitVia != "" {
		return exitVia, exitPaths, exitMode, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", nil, "", fmt.Errorf("bad address %q: %w", addr, err)
	}
	if via, mode, paths, ok := a.rules.ExitFor(host); ok {
		return via, paths, mode, nil
	}
	return "", nil, "", nil
}

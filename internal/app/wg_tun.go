package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// createWGNetstack creates a WireGuard TUN + netstack and starts a
// transparent SOCKS5/TCP proxy on the virtual interface.
func createWGNetstack(localAddresses, dnsServers []netip.Addr, mtu int, app *App, cfg WireGuardConfig) (tun.Device, error) {
	tunDev, tnet, err := netstack.CreateNetTUN(localAddresses, dnsServers, mtu)
	if err != nil {
		return nil, err
	}

	// Start TCP proxy on the WG virtual IP, port 1 (catch-all via client-side DNAT)
	// Actually, listen on a fixed high port. WG client config will route
	// traffic through the tunnel and we handle it via SOCKS5-less forwarding.
	//
	// Simple approach: listen on every port that a client might connect to.
	// We use port 80 and 443 as the most common, plus a SOCKS5 proxy port.

	gateway := localAddresses[0].String()

	// Start SOCKS5 proxy on the WG virtual interface
	go runWGSocksProxy(tnet, gateway, 1080, app, cfg)

	return tunDev, nil
}

// runWGSocksProxy runs a minimal SOCKS5 proxy on the WireGuard virtual interface.
// WG clients configure their system proxy to 10.99.99.1:1080 to route traffic.
func runWGSocksProxy(tnet *netstack.Net, gateway string, port int, app *App, cfg WireGuardConfig) {
	addr := &net.TCPAddr{
		IP:   net.ParseIP(gateway),
		Port: port,
	}
	ln, err := tnet.ListenTCP(addr)
	if err != nil {
		log.Printf("[wireguard] SOCKS5 listen error on %s:%d: %v", gateway, port, err)
		return
	}
	log.Printf("[wireguard] SOCKS5 proxy on %s:%d", gateway, port)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleWGSocks5(conn, app, cfg)
	}
}

// handleWGSocks5 handles a SOCKS5 connection from a WG peer.
func handleWGSocks5(conn net.Conn, app *App, cfg WireGuardConfig) {
	defer conn.Close()
	buf := make([]byte, 512)

	// SOCKS5 greeting
	n, err := conn.Read(buf)
	if err != nil || n < 2 || buf[0] != 0x05 {
		return
	}
	// No auth
	conn.Write([]byte{0x05, 0x00})

	// SOCKS5 request
	n, err = conn.Read(buf)
	if err != nil || n < 7 || buf[1] != 0x01 { // CONNECT
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	var addr string
	switch buf[3] {
	case 0x01: // IPv4
		addr = fmt.Sprintf("%d.%d.%d.%d:%d", buf[4], buf[5], buf[6], buf[7],
			int(buf[8])<<8|int(buf[9]))
	case 0x03: // Domain
		dl := int(buf[4])
		addr = fmt.Sprintf("%s:%d", buf[5:5+dl],
			int(buf[5+dl])<<8|int(buf[5+dl+1]))
	case 0x04: // IPv6
		addr = fmt.Sprintf("[%s]:%d", net.IP(buf[4:20]),
			int(buf[20])<<8|int(buf[21]))
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Find exit_via for this peer (by source IP)
	srcIP := conn.RemoteAddr().(*net.TCPAddr).IP.String()
	exitVia := findPeerExitVia(cfg, srcIP)

	var remote net.Conn
	if exitVia == "" {
		remote, err = net.DialTimeout("tcp", addr, 10*time.Second)
	} else {
		remote, err = app.dialExit(context.Background(), exitVia, addr)
	}
	if err != nil {
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer remote.Close()

	// Success
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	done := make(chan struct{})
	go func() { io.Copy(remote, conn); done <- struct{}{} }()
	io.Copy(conn, remote)
	<-done
}

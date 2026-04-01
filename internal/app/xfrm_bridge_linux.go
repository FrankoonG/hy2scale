package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// xfrmBridge captures packets from xfrm interfaces using AF_PACKET sockets
// and injects them into the gvisor netstack, bypassing the kernel's FORWARD chain.
// Response packets from gvisor are written back to the xfrm interface.

type xfrmClient struct {
	ifName string
	ifIdx  int
	fd     int    // AF_PACKET SOCK_RAW for reading (captures with pkt_type)
	wfd    int    // AF_PACKET SOCK_DGRAM for writing (no link-layer header)
	cancel context.CancelFunc
}

var (
	xfrmBridgeMu      sync.Mutex
	xfrmBridgeClients = map[string]*xfrmClient{} // ifName -> client
)

// startXfrmBridge opens an AF_PACKET socket on the given xfrm interface and
// bridges packets between it and the gvisor netstack channel endpoint.
func startXfrmBridge(ctx context.Context, ifName string, ep *channel.Endpoint) error {
	xfrmBridgeMu.Lock()
	defer xfrmBridgeMu.Unlock()

	// Get interface index
	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		return fmt.Errorf("interface %s: %w", ifName, err)
	}

	if existing, ok := xfrmBridgeClients[ifName]; ok {
		if existing.ifIdx == iface.Index {
			return nil // already bridged on correct interface
		}
		// Interface was recreated with a different index (e.g., ppp0 reconnect).
		// Stop the stale bridge and create a fresh one.
		log.Printf("[xfrm-bridge] %s: index changed %d→%d, restarting bridge", ifName, existing.ifIdx, iface.Index)
		existing.cancel()
		syscall.Close(existing.fd)
		syscall.Close(existing.wfd)
		delete(xfrmBridgeClients, ifName)
	}

	// Open AF_PACKET raw socket (SOCK_RAW for ARPHRD_NONE xfrm interfaces)
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		return fmt.Errorf("AF_PACKET socket: %w", err)
	}

	// Bind to the specific xfrm interface
	addr := syscall.SockaddrLinklayer{
		Protocol: htons(syscall.ETH_P_ALL),
		Ifindex:  iface.Index,
	}
	if err := syscall.Bind(fd, &addr); err != nil {
		syscall.Close(fd)
		return fmt.Errorf("bind to %s: %w", ifName, err)
	}

	// Open a raw IP socket for writing, bound to the interface.
	wfd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		syscall.Close(fd)
		return fmt.Errorf("raw IP socket: %w", err)
	}
	syscall.SetsockoptInt(wfd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1)
	syscall.SetsockoptString(wfd, syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, ifName)

	childCtx, cancel := context.WithCancel(ctx)
	client := &xfrmClient{
		ifName: ifName,
		ifIdx:  iface.Index,
		fd:     fd,
		wfd:    wfd,
		cancel: cancel,
	}
	xfrmBridgeClients[ifName] = client

	log.Printf("[xfrm-bridge] started AF_PACKET bridge on %s (idx=%d)", ifName, iface.Index)

	// Read packets from xfrm interface → inject into gvisor
	go xfrmReadLoop(childCtx, client, ep)

	return nil
}

// xfrmReadLoop reads raw IP packets from the AF_PACKET socket and injects
// them into the gvisor netstack.
func xfrmReadLoop(ctx context.Context, client *xfrmClient, ep *channel.Endpoint) {
	buf := make([]byte, 2048)
	var pktCount uint64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set a read deadline so we can check ctx periodically
		tv := syscall.Timeval{Sec: 1}
		syscall.SetsockoptTimeval(client.fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

		n, from, err := syscall.Recvfrom(client.fd, buf, 0)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Timeout or transient error, retry
			continue
		}
		if n == 0 {
			continue
		}

		// Filter: only process PACKET_HOST (inbound) packets.
		// Skip PACKET_OUTGOING (4) to avoid re-capturing our own writes.
		if sa, ok := from.(*syscall.SockaddrLinklayer); ok {
			if sa.Pkttype == 4 { // PACKET_OUTGOING
				continue
			}
		}

		pkt := buf[:n]

		// For SOCK_RAW on ARPHRD_NONE interfaces, check for link-layer header
		ipStart := 0
		version := pkt[0] >> 4
		if version != 4 && version != 6 && n > 14 {
			// Some kernels add a fake 14-byte link header
			if pkt[14]>>4 == 4 || pkt[14]>>4 == 6 {
				ipStart = 14
			}
		}

		if ipStart >= n {
			continue
		}
		ipPkt := pkt[ipStart:]

		pktCount++
		if pktCount == 1 {
			debugLog("[xfrm-bridge] %s: first packet received (%d bytes, IPv%d)", client.ifName, len(ipPkt), ipPkt[0]>>4)
		}

		pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(ipPkt),
		})
		switch ipPkt[0] >> 4 {
		case 4:
			ep.InjectInbound(header.IPv4ProtocolNumber, pkb)
		case 6:
			ep.InjectInbound(header.IPv6ProtocolNumber, pkb)
		}
		pkb.DecRef()
	}
}

// writeToXfrm sends a raw IP packet back through the xfrm interface.
// This triggers xfrm encapsulation (ESP) for the return path.
func writeToXfrm(ifName string, data []byte) {
	xfrmBridgeMu.Lock()
	client, ok := xfrmBridgeClients[ifName]
	xfrmBridgeMu.Unlock()
	if !ok {
		return
	}

	// Determine destination IP from packet for the sockaddr
	if len(data) < 20 {
		return
	}

	switch data[0] >> 4 {
	case 4:
		var sa4 syscall.SockaddrInet4
		copy(sa4.Addr[:], data[16:20])
		syscall.Sendto(client.wfd, data, 0, &sa4)
	case 6:
		// TODO: IPv6
	}
}

// stopXfrmBridge stops the bridge for a specific xfrm interface.
func stopXfrmBridge(ifName string) {
	xfrmBridgeMu.Lock()
	defer xfrmBridgeMu.Unlock()

	if client, ok := xfrmBridgeClients[ifName]; ok {
		client.cancel()
		syscall.Close(client.fd)
		syscall.Close(client.wfd)
		delete(xfrmBridgeClients, ifName)
		log.Printf("[xfrm-bridge] stopped bridge on %s", ifName)
	}
}

// xfrmBridgeGvisorWrite is called from the gvisor→TUN bridge goroutine.
// It checks if the destination IP belongs to an xfrm client, and if so,
// writes the packet to the xfrm interface instead of the TUN.
// Returns true if the packet was handled.
func xfrmBridgeGvisorWrite(data []byte) bool {
	if len(data) < 20 {
		return false
	}

	// Extract destination IP
	var dstIP string
	switch data[0] >> 4 {
	case 4:
		dstIP = fmt.Sprintf("%d.%d.%d.%d", data[16], data[17], data[18], data[19])
	case 6:
		if len(data) < 40 {
			return false
		}
		dstIP = net.IP(data[24:40]).String()
	default:
		return false
	}

	// Check if this destination belongs to an IKEv2 client
	_, ok := ikev2Sessions.Lookup(dstIP)
	if !ok {
		return false
	}

	// Find the xfrm interface for this client.
	// With kernel-libipsec, all clients use ipsec0/ipsec1 instead of per-client ikecN.
	ifName := xfrmIfForClient(dstIP)
	if ifName == "" {
		// Check if any ipsec bridge is active (kernel-libipsec mode)
		xfrmBridgeMu.Lock()
		for name := range xfrmBridgeClients {
			if len(name) >= 5 && name[:5] == "ipsec" {
				ifName = name
				break
			}
		}
		xfrmBridgeMu.Unlock()
		if ifName == "" {
			return false
		}
	}

	writeToXfrm(ifName, data)
	return true
}

// xfrmIfForClient returns the xfrm interface name that routes to the given client IP.
var (
	xfrmClientMapMu sync.RWMutex
	xfrmClientMap   = map[string]string{} // clientIP -> ifName
)

func xfrmIfForClient(clientIP string) string {
	xfrmClientMapMu.RLock()
	defer xfrmClientMapMu.RUnlock()
	return xfrmClientMap[clientIP]
}

func registerXfrmClient(clientIP, ifName string) {
	xfrmClientMapMu.Lock()
	defer xfrmClientMapMu.Unlock()
	xfrmClientMap[clientIP] = ifName
}

func unregisterXfrmClient(clientIP string) {
	xfrmClientMapMu.Lock()
	defer xfrmClientMapMu.Unlock()
	delete(xfrmClientMap, clientIP)
}

func htons(v uint16) uint16 {
	var buf [2]byte
	binary.BigEndian.PutUint16(buf[:], v)
	return *(*uint16)(unsafe.Pointer(&buf[0]))
}

// waitForInterface waits until a network interface appears (up to timeout).
func waitForInterface(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := net.InterfaceByName(name); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("interface %s not found after %v", name, timeout)
}

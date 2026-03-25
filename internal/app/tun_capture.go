package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// TUN capture: kernel TUN + gvisor netstack fallback for L2TP/IKEv2
// when iptables is unavailable (e.g. iKuai router containers).

const (
	tunCaptureName = "hy2cap0"
	tunCaptureIP   = "169.254.99.1"
	tunCaptureMTU  = 1300 // Conservative: must fit within ESP encapsulation (IKEv2) and PPP (L2TP)
	tunRouteTable  = "100"
)

// Linux TUN ioctl constants
const (
	_TUNSETIFF = 0x400454ca
	_IFF_TUN   = 0x0001
	_IFF_NO_PI = 0x1000
)

var (
	tunCaptureActive    atomic.Bool
	tunCaptureMu        sync.Mutex
	tunCaptureInst      *tunCaptureState
	ikev2DefaultExitVia atomic.Value // stores string for PSK mode default exit
)

type tunCaptureState struct {
	tunFile *os.File
	ep      *channel.Endpoint
	gvStack *stack.Stack
	cancel  context.CancelFunc
	subnets []string
}

// TunCaptureActive returns true if TUN capture mode is in use.
func TunCaptureActive() bool { return tunCaptureActive.Load() }

// openKernelTun creates a kernel TUN device via /dev/net/tun ioctl.
func openKernelTun(name string) (*os.File, error) {
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}

	// struct ifreq: 16 bytes name + 2 bytes flags
	var ifr [18]byte
	copy(ifr[:], name)
	flags := uint16(_IFF_TUN | _IFF_NO_PI)
	ifr[16] = byte(flags)
	ifr[17] = byte(flags >> 8)

	_, _, errno := syscallIoctl(f.Fd(), _TUNSETIFF, uintptr(unsafe.Pointer(&ifr[0])))
	if errno != 0 {
		f.Close()
		return nil, fmt.Errorf("TUNSETIFF: %v", errno)
	}
	return f, nil
}

func syscallIoctl(fd uintptr, req uintptr, arg uintptr) (uintptr, uintptr, uintptr) {
	return rawSyscall(sysIoctl, fd, req, arg)
}

// createCaptureStack creates a gvisor netstack for TUN capture.
// Same pattern as WireGuard's createWGNetstack — HandleLocal=false + promiscuous + spoofing.
func createCaptureStack(mtu int) (*channel.Endpoint, *stack.Stack, error) {
	s := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol, ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol, udp.NewProtocol,
			icmp.NewProtocol6, icmp.NewProtocol4,
		},
		// HandleLocal=false: allows promiscuous + spoofing to intercept ALL traffic
	})

	ep := channel.New(1024, uint32(mtu), "")

	sackOpt := tcpip.TCPSACKEnabled(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sackOpt)

	if tcpipErr := s.CreateNIC(1, ep); tcpipErr != nil {
		return nil, nil, fmt.Errorf("CreateNIC: %v", tcpipErr)
	}

	localAddr, _ := netip.ParseAddr(tunCaptureIP)
	s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFromSlice(localAddr.AsSlice()).WithPrefix(),
	}, stack.AddressProperties{})

	s.SetSpoofing(1, true)
	s.SetPromiscuousMode(1, true)

	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: 1},
		{Destination: header.IPv6EmptySubnet, NIC: 1},
	})

	return ep, s, nil
}

// startTunBridge relays packets between kernel TUN and gvisor netstack.
func startTunBridge(ctx context.Context, tunFile *os.File, ep *channel.Endpoint, mtu int) {
	// Kernel TUN → gvisor
	go func() {
		buf := make([]byte, mtu+64)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := tunFile.Read(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if n == 0 {
				continue
			}
			pkt := buf[:n]
			pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
				Payload: buffer.MakeWithData(pkt),
			})
			switch pkt[0] >> 4 {
			case 4:
				ep.InjectInbound(header.IPv4ProtocolNumber, pkb)
			case 6:
				ep.InjectInbound(header.IPv6ProtocolNumber, pkb)
			}
			pkb.DecRef()
		}
	}()

	// gvisor → kernel TUN
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			pkt := ep.Read()
			if pkt == nil {
				time.Sleep(time.Millisecond)
				continue
			}
			view := pkt.ToView()
			pkt.DecRef()
			data := view.AsSlice()
			if len(data) > 0 {
				// Check if this packet should go to an xfrm interface
				// (IKEv2 client response) instead of the TUN
				if !xfrmBridgeGvisorWrite(data) {
					tunFile.Write(data)
				}
			}
		}
	}()
}

// installCaptureForwarders sets up TCP/UDP forwarders on the gvisor stack.
// These forward traffic from L2TP/IKEv2 clients to the real network.
func installCaptureForwarders(s *stack.Stack, a *App) {
	// TCP forwarder
	tcpFwd := tcp.NewForwarder(s, 0, 65535, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		dstAddr := net.JoinHostPort(id.LocalAddress.String(), fmt.Sprintf("%d", id.LocalPort))

		var wq waiter.Queue
		ep, tcpErr := r.CreateEndpoint(&wq)
		if tcpErr != nil {
			r.Complete(true)
			return
		}
		r.Complete(false)
		tunConn := gonet.NewTCPConn(&wq, ep)

		go func() {
			defer tunConn.Close()
			srcIP := id.RemoteAddress.String()

			// Identify user from PPP or IKEv2 sessions
			username, ok := pppSessions.Lookup(srcIP)
			protocol := "l2tp"
			if !ok {
				username, ok = ikev2Sessions.Lookup(srcIP)
				protocol = "ikev2"
			}

			// Determine exit_via
			exitVia := ""
			if ok && username != "" && username != "__psk__" {
				cfg := a.store.Get()
				for _, u := range cfg.Users {
					if u.Username == username && u.Enabled {
						exitVia = u.ExitVia
						break
					}
				}
			} else if protocol == "ikev2" {
				// PSK mode: use default exit from IKEv2 config
				if v := ikev2DefaultExitVia.Load(); v != nil {
					exitVia = v.(string)
				}
			}

			debugLog("[tun-fwd] TCP %s(%s/%s) → %s exit=%q",
				srcIP, username, protocol, dstAddr, exitVia)

			var remote net.Conn
			var err error
			if exitVia == "" {
				remote, err = net.DialTimeout("tcp", dstAddr, 10*time.Second)
			} else {
				remote, err = a.dialExit(context.Background(), exitVia, dstAddr)
			}
			if err != nil {
				debugLog("[tun-fwd] dial error: %s → %s: %v", srcIP, dstAddr, err)
				return
			}
			defer remote.Close()

			// Session tracking
			ctx, cancel := context.WithCancel(context.Background())
			sid := a.Sessions.Connect(username, srcIP, protocol, cancel)
			defer func() {
				cancel()
				a.Sessions.Disconnect(sid, 0, 0)
			}()

			done := make(chan struct{})
			go func() { copyCtx(ctx, remote, tunConn); done <- struct{}{} }()
			copyCtx(ctx, tunConn, remote)
			<-done

			if username != "" && username != "__psk__" {
				a.RecordTraffic(username, 0) // traffic already counted in copyCtx
			}
		}()
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// UDP forwarder
	udpFwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
		id := r.ID()
		dstAddr := net.JoinHostPort(id.LocalAddress.String(), fmt.Sprintf("%d", id.LocalPort))

		var wq waiter.Queue
		ep, err := r.CreateEndpoint(&wq)
		if err != nil {
			return
		}
		udpConn := gonet.NewUDPConn(&wq, ep)

		go func() {
			defer udpConn.Close()
			remote, derr := net.DialTimeout("udp", dstAddr, 5*time.Second)
			if derr != nil {
				return
			}
			defer remote.Close()
			remote.SetDeadline(time.Now().Add(30 * time.Second))
			udpConn.SetDeadline(time.Now().Add(30 * time.Second))

			done := make(chan struct{})
			go func() {
				buf := make([]byte, 4096)
				for {
					n, e := udpConn.Read(buf)
					if e != nil || n == 0 {
						break
					}
					remote.Write(buf[:n])
				}
				done <- struct{}{}
			}()
			buf := make([]byte, 4096)
			for {
				n, e := remote.Read(buf)
				if e != nil || n == 0 {
					break
				}
				udpConn.Write(buf[:n])
			}
			<-done
		}()
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
}

// setupCaptureRouting configures kernel routing for a VPN subnet to go through the TUN.
func setupCaptureRouting(tunName, subnet string) error {
	// Bring up TUN interface
	exec.Command("ip", "link", "set", tunName, "up").Run()
	exec.Command("ip", "addr", "add", tunCaptureIP+"/32", "dev", tunName).Run()

	// Policy route: traffic from VPN subnet → table 100 → via TUN
	exec.Command("ip", "rule", "add", "from", subnet, "lookup", tunRouteTable).Run()
	exec.Command("ip", "route", "replace", "default", "dev", tunName, "table", tunRouteTable).Run()

	// Enable IP forwarding
	os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)

	log.Printf("[tun-capture] route: from %s → dev %s (table %s)", subnet, tunName, tunRouteTable)
	return nil
}

// ensureTunCapture creates or reuses the shared TUN capture infrastructure.
// Safe to call multiple times — idempotent for existing subnets.
func ensureTunCapture(a *App, subnet string) error {
	tunCaptureMu.Lock()
	defer tunCaptureMu.Unlock()

	if tunCaptureInst != nil {
		// Already running — just add routing for new subnet
		for _, s := range tunCaptureInst.subnets {
			if s == subnet {
				return nil // already configured
			}
		}
		tunCaptureInst.subnets = append(tunCaptureInst.subnets, subnet)
		return setupCaptureRouting(tunCaptureName, subnet)
	}

	// First initialization
	log.Printf("[tun-capture] creating kernel TUN %s with gvisor netstack (MTU=%d)", tunCaptureName, tunCaptureMTU)

	tunFile, err := openKernelTun(tunCaptureName)
	if err != nil {
		return fmt.Errorf("open TUN: %w", err)
	}

	ep, gvStack, err := createCaptureStack(tunCaptureMTU)
	if err != nil {
		tunFile.Close()
		return fmt.Errorf("create stack: %w", err)
	}

	// Install TCP/UDP forwarders
	installCaptureForwarders(gvStack, a)

	// Start bridge
	ctx, cancel := context.WithCancel(a.appCtx)
	startTunBridge(ctx, tunFile, ep, tunCaptureMTU)

	// Setup routing for first subnet
	if err := setupCaptureRouting(tunCaptureName, subnet); err != nil {
		cancel()
		tunFile.Close()
		return fmt.Errorf("setup routing: %w", err)
	}

	tunCaptureInst = &tunCaptureState{
		tunFile: tunFile,
		ep:      ep,
		gvStack: gvStack,
		cancel:  cancel,
		subnets: []string{subnet},
	}
	tunCaptureActive.Store(true)
	log.Printf("[tun-capture] TUN capture mode active (compat)")
	return nil
}

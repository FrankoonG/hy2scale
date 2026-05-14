package app

import (
	"context"
	"encoding/binary"
	"log"
	"net"
	"sync"
	"syscall"
	"time"
)

// udpClientResolver maps a client source IP (from inside the VPN's vIP pool)
// to its exit_via routing decision. Returns ok=false if the client is not
// authorised (no session, expired, etc.) — caller drops the datagram.
type udpClientResolver func(srcIP string) (username, exitVia string, exitPaths []string, exitMode string, ok bool)

// udpFlowKey distinguishes simultaneous UDP flows from the same client to
// different destinations. Each flow gets its own remote conn so reply
// datagrams from one flow don't bleed into another's wrapper.
type udpFlowKey struct {
	src     string // "10.10.10.2:54321"
	origDst string // "10.246.0.30:9991"
}

type udpFlow struct {
	remote   net.Conn
	lastUsed atomic_time
	// pending holds packets received while the upstream dial is still
	// in flight. Once the dial completes, the launcher goroutine
	// drains and writes them in order, then clears this slice.
	pending [][]byte
}

// atomic_time is a poor-man's atomic Time — protected by the flows-map mutex.
// (sync/atomic.Value avoids races but adds boxing; we already hold the mutex
// at every read/write, so a plain field is enough.)
type atomic_time struct{ t time.Time }

// readUDPMsgOrigDst reads one DNAT'd UDP datagram and extracts the original
// pre-DNAT destination from the IP_RECVORIGDSTADDR control message. Returns
// origDst == nil if the cmsg was missing — caller should drop the packet
// since there is no way to know where the client wanted to talk.
func readUDPMsgOrigDst(conn *net.UDPConn, listenAddr *net.UDPAddr, buf, oob []byte) (n int, src, origDst *net.UDPAddr, err error) {
	var oobn int
	n, oobn, _, src, err = conn.ReadMsgUDP(buf, oob)
	if err != nil {
		return
	}
	msgs, perr := syscall.ParseSocketControlMessage(oob[:oobn])
	if perr != nil {
		return n, src, nil, nil
	}
	for _, m := range msgs {
		// IP_RECVORIGDSTADDR = 20 on Linux. Header.Level == SOL_IP.
		if m.Header.Level == syscall.SOL_IP && m.Header.Type == 20 && len(m.Data) >= 8 {
			port := binary.BigEndian.Uint16(m.Data[2:4])
			ip := net.IPv4(m.Data[4], m.Data[5], m.Data[6], m.Data[7])
			cand := &net.UDPAddr{IP: ip, Port: int(port)}
			// On some kernels with iptables PREROUTING DNAT the cmsg
			// returns the listener's own bind address rather than the
			// pre-NAT dst. Detect that and fall through to the conntrack
			// fallback below.
			if listenAddr == nil || !cand.IP.Equal(listenAddr.IP) || cand.Port != listenAddr.Port {
				origDst = cand
				return
			}
			break
		}
	}
	// Fallback: query conntrack for the original tuple. This is the
	// same path rules.go uses for its own UDP transparent proxy.
	if src != nil {
		listenPort := 0
		if listenAddr != nil {
			listenPort = listenAddr.Port
		}
		if od := conntrackOrigDst("udp", src.String(), listenPort); od != "" {
			if a, perr := net.ResolveUDPAddr("udp4", od); perr == nil {
				origDst = a
			}
		}
	}
	return
}

// listenUDPDNATOrig opens a UDP listener that sets IP_RECVORIGDSTADDR so
// each ReadMsgUDP returns the pre-DNAT destination of the packet via a
// SOL_IP / type 20 control message. Unlike listenUDPTransparent in
// rules_udp_linux.go this does NOT set IP_TRANSPARENT — TPROXY semantics
// cause the kernel to skip the conntrack DNAT lookup and the cmsg ends up
// echoing the listener's own bind address instead of the original dst.
// For our PREROUTING DNAT case the listener is bound to its actual local
// address, so IP_TRANSPARENT is unnecessary.
func listenUDPDNATOrig(addr string) (*net.UDPConn, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var err error
			c.Control(func(fd uintptr) {
				err = syscall.SetsockoptInt(int(fd), syscall.SOL_IP, 20, 1) // IP_RECVORIGDSTADDR
			})
			return err
		},
	}
	pc, err := lc.ListenPacket(context.Background(), "udp4", addr)
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

// runUDPTransparentProxy listens for DNAT'd UDP datagrams from VPN clients
// (IKEv2 / L2TP) and forwards each (src → origDst) flow through the relay
// chain (or direct if no exit_via). Reply datagrams are written back via the
// listener socket so conntrack reverse-NATs them to the original dst as
// source — exactly mirroring the TCP transparent-proxy pattern.
//
// This closes the long-standing TCP-only gap in the iptables-native mode of
// internal/app/ikev2.go and l2tp.go. See docs/udp-cross-tunnel-investigation.md.
func (a *App) runUDPTransparentProxy(ctx context.Context, listenAddr, tag string, resolve udpClientResolver) {
	conn, err := listenUDPDNATOrig(listenAddr)
	if err != nil {
		log.Printf("[%s-udp] listen %s: %v", tag, listenAddr, err)
		return
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()
	log.Printf("[%s-udp] transparent UDP proxy on %s", tag, listenAddr)

	var (
		mu    sync.Mutex
		flows = map[udpFlowKey]*udpFlow{}
	)

	// Idle reaper: any flow without traffic for ~90s gets closed. The
	// reader goroutine's Read returns an error once Close runs, the
	// goroutine cleans up its map entry on the way out.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mu.Lock()
				now := time.Now()
				for k, f := range flows {
					if now.Sub(f.lastUsed.t) > 90*time.Second {
						f.remote.Close()
						delete(flows, k)
					}
				}
				mu.Unlock()
			}
		}
	}()

	listenUDP, _ := net.ResolveUDPAddr("udp4", listenAddr)
	buf := make([]byte, 65535)
	oob := make([]byte, 1024)
	for {
		n, src, origDst, rerr := readUDPMsgOrigDst(conn, listenUDP, buf, oob)
		if rerr != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[%s-udp] read: %v", tag, rerr)
			continue
		}
		if origDst == nil {
			// IP_RECVORIGDSTADDR missing — packet either wasn't DNAT'd
			// or the kernel doesn't support the cmsg. Drop silently.
			continue
		}

		key := udpFlowKey{src: src.String(), origDst: origDst.String()}
		mu.Lock()
		f, ok := flows[key]
		if !ok {
			// Reserve the flow slot synchronously so concurrent reads of
			// the same client → same dst don't all race to dial.
			username, exitVia, exitPaths, exitMode, authOK := resolve(src.IP.String())
			if !authOK {
				mu.Unlock()
				continue
			}
			origStr := origDst.String()
			// Buffer the first packet — the dial happens off the main
			// read loop so a slow relay dial cannot stall further
			// incoming UDP. Subsequent packets land here too while the
			// dial is in flight and get appended to the pending buffer.
			f = &udpFlow{}
			f.lastUsed.t = time.Now()
			pending := make([]byte, n)
			copy(pending, buf[:n])
			f.pending = append(f.pending, pending)
			flows[key] = f
			mu.Unlock()
			// Now dial without holding mu; once the conn is up, swap it
			// in and flush the pending packets.
			go func(k udpFlowKey, flow *udpFlow, src *net.UDPAddr, username, exitVia, exitMode string, exitPaths []string, origStr string) {
				var remote net.Conn
				var derr error
				if exitVia == "" {
					remote, derr = net.DialTimeout("udp", origStr, 5*time.Second)
				} else {
					remote, derr = a.dialExitUDPPaths(ctx, exitVia, exitPaths, exitMode, origStr)
				}
				if derr != nil {
					log.Printf("[%s-udp] dial %s via %q: %v", tag, origStr, exitVia, derr)
					mu.Lock()
					if cur, ok := flows[k]; ok && cur == flow {
						delete(flows, k)
					}
					mu.Unlock()
					return
				}
				log.Printf("[%s-udp] flow %s → %s (user=%s exit=%q)", tag, src, origStr, username, exitVia)
				mu.Lock()
				flow.remote = remote
				pendingPkts := flow.pending
				flow.pending = nil
				mu.Unlock()
				// Flush all queued packets that arrived during the dial.
				for _, p := range pendingPkts {
					if _, werr := remote.Write(p); werr != nil {
						break
					}
				}
				// Reverse direction
				rbuf := make([]byte, 65535)
				defer func() {
					mu.Lock()
					if cur, ok := flows[k]; ok && cur == flow {
						delete(flows, k)
					}
					mu.Unlock()
					remote.Close()
				}()
				for {
					_ = remote.SetReadDeadline(time.Now().Add(120 * time.Second))
					rn, rerr := remote.Read(rbuf)
					if rerr != nil {
						return
					}
					mu.Lock()
					flow.lastUsed.t = time.Now()
					mu.Unlock()
					if _, werr := conn.WriteToUDP(rbuf[:rn], src); werr != nil {
						return
					}
				}
			}(key, f, src, username, exitVia, exitMode, exitPaths, origStr)
			continue
		}
		f.lastUsed.t = time.Now()
		if f.remote == nil {
			// Dial still in flight — queue this packet so it gets sent
			// once the upstream conn comes up.
			cp := make([]byte, n)
			copy(cp, buf[:n])
			f.pending = append(f.pending, cp)
			mu.Unlock()
			continue
		}
		// Fast path with existing flow.
		mu.Unlock()
		if _, werr := f.remote.Write(buf[:n]); werr != nil {
			mu.Lock()
			if cur, ok := flows[key]; ok && cur == f {
				delete(flows, key)
			}
			mu.Unlock()
			f.remote.Close()
		}
	}
}

package app

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// listenUDPTransparent creates a UDP socket with IP_TRANSPARENT.
// This allows receiving REDIRECT'd packets on any local address.
func listenUDPTransparent(addr string) (*net.UDPConn, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var err error
			c.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.SOL_IP, syscall.IP_TRANSPARENT, 1)
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

// dialUDPMarked creates a UDP connection with SO_MARK set to bypass iptables REDIRECT.
func dialUDPMarked(addr string, mark int) (net.Conn, error) {
	d := net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			var err error
			c.Control(func(fd uintptr) {
				err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
			})
			return err
		},
	}
	return d.Dial("udp", addr)
}

// conntrackOrigDst looks up the original destination for a NAT'd UDP connection
// using `conntrack -L`. This is needed because IP_RECVORIGDSTADDR doesn't work
// for OUTPUT chain REDIRECT/DNAT on Linux.
func conntrackOrigDst(proto, srcAddr string, dstPort int) string {
	parts := strings.SplitN(srcAddr, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	srcPort := parts[1]

	// conntrack -L output format:
	// udp 17 28 src=172.17.0.1 dst=172.17.0.3 sport=58964 dport=19999 ... src=127.0.0.1 dst=... sport=12381 ...
	// We match on: sport=<srcPort> and dport=<dstPort> in the reply tuple,
	// then extract dst= and dport= from the original tuple.
	out, err := exec.Command("conntrack", "-L", "-p", proto).Output()
	if err != nil {
		return ""
	}

	// Look for lines where reply has sport=dstPort and dport=srcPort
	replyNeedle := fmt.Sprintf("sport=%d dport=%s", dstPort, srcPort)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, replyNeedle) {
			continue
		}
		// Extract original dst and dport from the first occurrence
		var origDst, origDport string
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "dst=") && origDst == "" {
				origDst = field[4:]
			} else if strings.HasPrefix(field, "dport=") && origDport == "" {
				origDport = field[6:]
			}
			if origDst != "" && origDport != "" {
				break
			}
		}
		if origDst != "" && origDport != "" && origDst != "127.0.0.1" {
			return origDst + ":" + origDport
		}
	}
	return ""
}

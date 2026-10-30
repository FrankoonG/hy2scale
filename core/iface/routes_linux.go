package iface

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Linux: the routing table from /proc, interface pinning with
// SO_BINDTODEVICE.

// loadRoutes parses /proc/net/route and /proc/net/ipv6_route.
func loadRoutes() *routeInfo {
	r := &routeInfo{gateway4: map[string]string{}, gateway6: map[string]string{}}
	if f, err := os.Open("/proc/net/route"); err == nil {
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 8 {
				continue
			}
			dst, gw, mask := le32(fl[1]), le32(fl[2]), le32(fl[7])
			bits := maskBits(mask)
			if bits == 0 {
				if gw.IsValid() && !gw.IsUnspecified() {
					r.gateway4[fl[0]] = gw.String()
				}
				continue
			}
			r.specific = append(r.specific, netip.PrefixFrom(dst, bits))
		}
		f.Close()
	}
	if f, err := os.Open("/proc/net/ipv6_route"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 10 || fl[9] == "lo" {
				continue
			}
			dst, plen, gw := hex16(fl[0]), hexInt(fl[1]), hex16(fl[4])
			if plen == 0 {
				if gw.IsValid() && !gw.IsUnspecified() {
					r.gateway6[fl[9]] = gw.String()
				}
				continue
			}
			if dst.IsLinkLocalUnicast() || dst.IsMulticast() {
				continue
			}
			r.specific = append(r.specific, netip.PrefixFrom(dst, plen))
		}
		f.Close()
	}
	return r
}

func le32(h string) netip.Addr {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 4 {
		return netip.Addr{}
	}
	return netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]})
}

func hex16(h string) netip.Addr {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 16 {
		return netip.Addr{}
	}
	return netip.AddrFrom16([16]byte(b))
}

func hexInt(h string) int {
	v, _ := strconv.ParseInt(h, 16, 32)
	return int(v)
}

func maskBits(m netip.Addr) int {
	if !m.IsValid() {
		return 0
	}
	b := m.As4()
	n := 0
	for _, x := range b {
		for ; x&0x80 != 0; x <<= 1 {
			n++
		}
	}
	return n
}

// kernelRoutes reports whether the system can route the internet out of
// iface for fam (a socket pinned to it finds a route). No packet is sent.
func kernelRoutes(iface string, fam int) bool {
	d := net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		c.Control(func(fd uintptr) {
			serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
		})
		return serr
	}}
	c, err := d.Dial(FamilyNet("udp", fam), probeTargets[fam])
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// bindControl pins a socket to iface.
func bindControl(iface string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
		}); err != nil {
			return err
		}
		if serr != nil {
			return fmt.Errorf("bind to %s: %w", iface, serr)
		}
		return nil
	}
}

package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"syscall"

	"github.com/FrankoonG/hy2scale/core/sockmark"
	"golang.org/x/sys/unix"
)

// A peer address may name the local interface and/or source address its
// connection leaves from:
//
//	host:ports#iface        e.g. 1.2.3.4:5565#eth1
//	host:ports#iface/src    e.g. 1.2.3.4:5565#eth1/192.168.2.10
//	host:ports#/src         e.g. [2001:db8::1]:443#/2001:db8::20
//
// The suffix is part of the address string, so the same host via two
// interfaces is two addresses, with their own connection, status and
// latency. No suffix means the kernel's choice, as before.

type addrBind struct {
	iface string
	src   net.IP
}

func (b addrBind) none() bool { return b.iface == "" && b.src == nil }

func (b addrBind) String() string {
	if b.none() {
		return ""
	}
	s := "#" + b.iface
	if b.src != nil {
		s += "/" + b.src.String()
	}
	return s
}

// splitAddrBind separates the dial address from its binding suffix.
func splitAddrBind(spec string) (string, addrBind, error) {
	i := strings.LastIndexByte(spec, '#')
	if i < 0 {
		return spec, addrBind{}, nil
	}
	addr, suf := spec[:i], spec[i+1:]
	var b addrBind
	iface, src, hasSrc := strings.Cut(suf, "/")
	if iface != "" {
		if !validIfaceName(iface) {
			return "", b, fmt.Errorf("invalid interface name %q", iface)
		}
		b.iface = iface
	}
	if hasSrc {
		ip := net.ParseIP(src)
		if ip == nil {
			return "", b, fmt.Errorf("invalid source address %q", src)
		}
		b.src = ip
	}
	if b.none() {
		return "", b, fmt.Errorf("empty binding in %q", spec)
	}
	return addr, b, nil
}

// stripAddrBind is splitAddrBind for callers that only need the address.
func stripAddrBind(spec string) string {
	if i := strings.LastIndexByte(spec, '#'); i >= 0 {
		return spec[:i]
	}
	return spec
}

func validIfaceName(s string) bool {
	if s == "" || len(s) > unix.IFNAMSIZ-1 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == '/' || r == ':' || r == '#' || r > '~' {
			return false
		}
	}
	return true
}

var bindDeviceWarn sync.Once

// childListen opens a UDP socket on a running child interface (set by
// the App; ok=false when iface is not a child interface).
var childListen func(iface string, raddr net.IP) (pc net.PacketConn, ok bool, err error)

// childListenPort opens a UDP socket of a family at a local port on a
// running child interface (set by the App; ok=false when iface is not one).
var childListenPort func(iface string, v6 bool, port int) (pc net.PacketConn, ok bool, err error)

// listen opens the UDP socket a connection to raddr uses.
func (b addrBind) listen(raddr net.IP) (net.PacketConn, error) {
	if b.none() {
		return sockmark.ListenUDP("udp", nil)
	}
	if b.iface != "" && childListen != nil {
		if pc, ok, err := childListen(b.iface, raddr); ok {
			return pc, err
		}
	}
	return b.listenAt(raddr, 0, "udp")
}

// listenAt opens a UDP socket with the binding on a given local port (0 =
// any). network narrows the family ("udp4") when the caller needs one.
func (b addrBind) listenAt(raddr net.IP, port int, network string) (net.PacketConn, error) {
	// a built-in child has its own stack and address: the socket lives there
	if b.iface != "" && childListenPort != nil {
		v6 := network == "udp6" || (network == "udp" && raddr != nil && raddr.To4() == nil) ||
			(b.src != nil && b.src.To4() == nil)
		if pc, ok, err := childListenPort(b.iface, v6, port); ok {
			return pc, err
		}
	}
	laddr := &net.UDPAddr{Port: port}
	src := b.src
	if src == nil && b.iface != "" && raddr != nil {
		// Old kernels refuse unprivileged SO_BINDTODEVICE; the interface's
		// own address still steers the source (and with policy routing, the
		// way out).
		src = ifaceAddrFor(b.iface, raddr)
	}
	if src != nil {
		laddr.IP = src
		if src.To4() != nil {
			network = "udp4"
		} else {
			network = "udp6"
		}
	}
	lc := net.ListenConfig{}
	if b.iface != "" {
		lc.Control = func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, b.iface)
			}); err != nil {
				return err
			}
			if serr != nil {
				if src == nil {
					return fmt.Errorf("bind to %s: %w", b.iface, serr)
				}
				bindDeviceWarn.Do(func() {
					log.Printf("[bind] SO_BINDTODEVICE %s: %v — binding the source address only", b.iface, serr)
				})
			}
			return nil
		}
	}
	lc.Control = sockmark.Chain(lc.Control)
	pc, err := lc.ListenPacket(context.Background(), network, laddr.String())
	if err != nil {
		return nil, err
	}
	return pc, nil
}

// ifaceAddrFor is an address of iface in raddr's family, or nil.
func ifaceAddrFor(iface string, raddr net.IP) net.IP {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil
	}
	addrs, _ := ifi.Addrs()
	want4 := raddr.To4() != nil
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		if (ipn.IP.To4() != nil) == want4 {
			return ipn.IP
		}
	}
	return nil
}

// boundConnFactory creates a connection's socket with its binding.
type boundConnFactory struct{ bind addrBind }

func (f boundConnFactory) New(addr net.Addr) (net.PacketConn, error) {
	var ip net.IP
	if ua, ok := addr.(*net.UDPAddr); ok {
		ip = ua.IP
	}
	return f.bind.listen(ip)
}

// IfaceInfo is one interface the process can bind to.
type IfaceInfo struct {
	Name     string   `json:"name"`
	Up       bool     `json:"up"`
	Loopback bool     `json:"loopback"`
	Addrs    []string `json:"addrs"` // IPs, without prefix length
	Kind     string   `json:"kind,omitempty"`   // "child" for hy2scale's child interfaces
	Parent   string   `json:"parent,omitempty"` // a child's physical interface
}

// SystemInterfaces lists the interfaces this process sees (in a container,
// the container's own).
func SystemInterfaces() []IfaceInfo {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]IfaceInfo, 0, len(ifs))
	for _, ifi := range ifs {
		info := IfaceInfo{Name: ifi.Name, Up: ifi.Flags&net.FlagUp != 0, Loopback: ifi.Flags&net.FlagLoopback != 0, Addrs: []string{}}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLinkLocalUnicast() {
				info.Addrs = append(info.Addrs, ipn.IP.String())
			}
		}
		out = append(out, info)
	}
	return out
}

// SplitAddrBind validates an address's binding suffix (see splitAddrBind).
func SplitAddrBind(spec string) (addr, iface, src string, err error) {
	a, b, err := splitAddrBind(spec)
	if err != nil {
		return "", "", "", err
	}
	if b.src != nil {
		src = b.src.String()
	}
	return a, b.iface, src, nil
}

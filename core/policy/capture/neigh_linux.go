//go:build linux

package capture

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/FrankoonG/hy2scale/core/vnic"
)

// neighCache answers "which interface and MAC does this source come
// from" for layer 2 flows: the neighbour table (one netlink dump, reused
// for a few seconds), else the route back to the source (interface only).
type neighCache struct {
	mu     sync.Mutex
	at     time.Time
	tab    map[netip.Addr]neighEntry
	routes map[netip.Addr]routeEntry
}

type neighEntry struct {
	iface string
	mac   string
}

type routeEntry struct {
	iface string
	at    time.Time
}

const (
	neighTTL = 5 * time.Second
	routeTTL = 60 * time.Second
)

func (n *neighCache) lookup(ip netip.Addr, selected []string) (iface, mac string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.tab == nil || time.Since(n.at) > neighTTL {
		n.refresh()
	} else if _, ok := n.tab[ip]; !ok && time.Since(n.at) > time.Second {
		n.refresh()
	}
	if e, ok := n.tab[ip]; ok {
		return e.iface, e.mac
	}
	if len(selected) == 1 {
		return selected[0], ""
	}
	if n.routes == nil {
		n.routes = map[netip.Addr]routeEntry{}
	}
	if r, ok := n.routes[ip]; ok && time.Since(r.at) < routeTTL {
		return r.iface, ""
	}
	r := routeEntry{iface: routeDev(ip), at: time.Now()}
	if len(n.routes) > 4096 {
		n.routes = map[netip.Addr]routeEntry{}
	}
	n.routes[ip] = r
	return r.iface, ""
}

func (n *neighCache) refresh() {
	tab, err := neighDump()
	if err != nil {
		debugf("[capture] neighbour table: %v", err)
	}
	n.tab, n.at = tab, time.Now()
}

const (
	rtmNewNeigh = 28
	rtmGetNeigh = 30
	ndaDst      = 1
	ndaLLAddr   = 2
	nudFailed   = 0x20
	nudIncomp   = 0x01
)

// neighDump reads the kernel's neighbour table (ARP and NDP).
func neighDump() (map[netip.Addr]neighEntry, error) {
	b, err := syscall.NetlinkRIB(rtmGetNeigh, syscall.AF_UNSPEC)
	if err != nil {
		return map[netip.Addr]neighEntry{}, err
	}
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return map[netip.Addr]neighEntry{}, err
	}
	names := map[int]string{}
	out := map[netip.Addr]neighEntry{}
	for _, m := range msgs {
		if m.Header.Type != rtmNewNeigh || len(m.Data) < 12 {
			continue
		}
		d := m.Data
		ifindex := int(int32(binary.LittleEndian.Uint32(d[4:8])))
		state := binary.LittleEndian.Uint16(d[8:10])
		if state&(nudFailed|nudIncomp) != 0 {
			continue
		}
		var ip netip.Addr
		var mac string
		for a := d[12:]; len(a) >= 4; {
			l := int(binary.LittleEndian.Uint16(a[0:2]))
			t := binary.LittleEndian.Uint16(a[2:4])
			if l < 4 || l > len(a) {
				break
			}
			v := a[4:l]
			switch t {
			case ndaDst:
				if x, ok := netip.AddrFromSlice(v); ok {
					ip = x.Unmap()
				}
			case ndaLLAddr:
				if len(v) == 6 {
					mac = net.HardwareAddr(v).String()
				}
			}
			al := (l + 3) &^ 3
			if al > len(a) {
				break
			}
			a = a[al:]
		}
		if !ip.IsValid() {
			continue
		}
		name, ok := names[ifindex]
		if !ok {
			if ifi, err := net.InterfaceByIndex(ifindex); err == nil {
				name = ifi.Name
			}
			names[ifindex] = name
		}
		out[ip] = neighEntry{iface: name, mac: mac}
	}
	return out, nil
}

// routeDev: the interface the route back to ip leaves through.
func routeDev(ip netip.Addr) string {
	args := []string{"-o", "route", "get", ip.String()}
	if ip.Is6() {
		args = append([]string{"-6"}, args...)
	}
	out, err := exec.Command("ip", args...).Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "dev" {
			return f[i+1]
		}
	}
	return ""
}

// systemIfaces lists the kernel's interfaces for FilterInterfaces.
func systemIfaces() []RawIface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]RawIface, 0, len(ifs))
	for _, ifi := range ifs {
		r := RawIface{Name: ifi.Name, Up: ifi.Flags&net.FlagUp != 0, Loopback: ifi.Flags&net.FlagLoopback != 0,
			Type: vnic.LinkType(ifi.Name), Addrs: []string{}}
		if l, err := os.Readlink("/sys/class/net/" + ifi.Name + "/master"); err == nil {
			r.Master = l[strings.LastIndex(l, "/")+1:]
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLinkLocalUnicast() {
				ones, _ := ipn.Mask.Size()
				r.Addrs = append(r.Addrs, fmt.Sprintf("%s/%d", ipn.IP, ones))
			}
		}
		out = append(out, r)
	}
	return out
}

var debugMode = func() bool {
	v := strings.ToLower(os.Getenv("DEBUG"))
	return v == "true" || v == "1" || v == "yes"
}()

func debugf(format string, args ...any) {
	if debugMode {
		log.Printf(format, args...)
	}
}

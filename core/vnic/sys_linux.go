//go:build linux

package vnic

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func sysNet(name, file string) string {
	return "/sys/class/net/" + name + "/" + file
}

func validIfName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00") && len(name) < unix.IFNAMSIZ
}

// IsPhysical reports whether name is an Ethernet interface backed by a real
// device (/sys/class/net/<name>/device exists and type is ARPHRD_ETHER).
func IsPhysical(name string) bool {
	if !validIfName(name) {
		return false
	}
	if _, err := os.Stat(sysNet(name, "device")); err != nil {
		return false
	}
	b, err := os.ReadFile(sysNet(name, "type"))
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// IsWireless reports whether name is a Wi-Fi interface.
func IsWireless(name string) bool {
	if !validIfName(name) {
		return false
	}
	for _, f := range []string{"wireless", "phy80211"} {
		if _, err := os.Stat(sysNet(name, f)); err == nil {
			return true
		}
	}
	return false
}

// LinkType names what kind of link name is: ethernet, wlan, loopback,
// bridge, bond, veth, vlan, macvlan, ipvlan, tun, wireguard, vxlan, dummy,
// ovs, or "" when it can't be told. The kernel driver name (ethtool) is the
// primary signal; sysfs fills in what the driver doesn't say.
func LinkType(name string) string {
	if !validIfName(name) {
		return ""
	}
	if b, err := os.ReadFile(sysNet(name, "type")); err == nil && strings.TrimSpace(string(b)) == "772" {
		return "loopback"
	}
	if IsWireless(name) {
		return "wlan"
	}
	if fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0); err == nil {
		info, err := unix.IoctlGetEthtoolDrvinfo(fd, name)
		unix.Close(fd)
		if err == nil {
			switch d := unix.ByteSliceToString(info.Driver[:]); d {
			case "veth", "bridge", "tun", "wireguard", "macvlan", "ipvlan", "vxlan", "dummy":
				return d
			case "macvtap":
				return "macvlan"
			case "bonding":
				return "bond"
			case "openvswitch":
				return "ovs"
			case "802.1Q VLAN Support":
				return "vlan"
			}
		}
	}
	for dir, t := range map[string]string{"bridge": "bridge", "bonding": "bond", "tun_flags": "tun"} {
		if _, err := os.Stat(sysNet(name, dir)); err == nil {
			return t
		}
	}
	if b, err := os.ReadFile(sysNet(name, "uevent")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "DEVTYPE="); ok {
				switch v {
				case "vlan", "wireguard", "bridge", "bond", "vxlan":
					return v
				}
			}
		}
	}
	if IsPhysical(name) {
		return "ethernet"
	}
	return ""
}

// CanCreate checks that a packet socket can be opened and bound on parent
// (CAP_NET_RAW, the parent visible in this network namespace). The bound
// socket uses protocol 0, so it never receives a frame.
func CanCreate(parent string) error {
	if !validIfName(parent) {
		return fmt.Errorf("invalid interface name %q", parent)
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			return errNoPermission
		}
		return fmt.Errorf("packet socket: %w", err)
	}
	defer unix.Close(fd)
	ifindex, _, _, err := parentInfo(fd, parent)
	if err != nil {
		return err
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Ifindex: ifindex}); err != nil {
		return fmt.Errorf("bind packet socket to %s: %w", parent, err)
	}
	return nil
}

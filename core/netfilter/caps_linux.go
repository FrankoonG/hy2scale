// Package netfilter runs the system's iptables (whatever the variant or
// the platform needs) and detects what the runtime may do with it.
package netfilter

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// IptVariant detects the correct iptables command at startup.
// On standard Linux with nftables: "iptables" (maps to iptables-nft)
// On iKuai or legacy systems: "iptables-legacy"
var IptVariant = sync.OnceValue(func() string {
	// Check if iptables (nftables backend) works — preferred on modern systems
	if out, err := exec.Command("iptables", "-V").CombinedOutput(); err == nil {
		v := string(out)
		if strings.Contains(v, "nf_tables") {
			log.Printf("[iptables] using iptables (nf_tables backend)")
			return "iptables"
		}
	}
	// Fall back to iptables-legacy
	if _, err := exec.Command("iptables-legacy", "-L", "-n").CombinedOutput(); err == nil {
		log.Printf("[iptables] using iptables-legacy")
		return "iptables-legacy"
	}
	// Default
	log.Printf("[iptables] falling back to iptables")
	return "iptables"
})

func Exec(prog string, args ...string) *exec.Cmd {
	// Platform adapter: iKuai, OpenWrt, etc. may provide custom iptables binary
	if cmd := platformIPTExec(prog, args); cmd != nil {
		return cmd
	}
	return exec.Command(prog, args...)
}

func Run(prog string, args ...string) {
	// Try -C (check) first to detect duplicates
	for i, a := range args {
		if a == "-I" || a == "-A" {
			checkArgs := make([]string, len(args))
			copy(checkArgs, args)
			checkArgs[i] = "-C"
			if a == "-I" && i+2 < len(checkArgs) {
				if _, err := fmt.Sscanf(checkArgs[i+2], "%d", new(int)); err == nil {
					checkArgs = append(checkArgs[:i+2], checkArgs[i+3:]...)
				}
			}
			if Exec(prog, checkArgs...).Run() == nil {
				return // rule already exists
			}
			break
		}
	}
	cmd := Exec(prog, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[iptables] %s %v: %s", prog, args, string(out))
	}
}

// testIptablesAvailable checks if the nat table can be listed.
func testIptablesAvailable() bool {
	cmd := Exec(IptVariant(), "-t", "nat", "-L", "-n")
	if out, err := cmd.CombinedOutput(); err != nil {
		debugLog("[iptables] NAT table test failed: %v: %s", err, string(out))
		return false
	}
	debugLog("[iptables] NAT table test passed")
	return true
}

// Available caches iptables detection at startup.
// If standard iptables fails, the platform's own fix is tried (iKuai: the
// bundled iptables); without either, the node runs without iptables.
var Available = sync.OnceValue(func() bool {
	if testIptablesAvailable() {
		return true
	}
	// Standard iptables failed. Try platform-specific compatibility fix.
	initPlatformAdapter()
	if platformFixIPTables() {
		// Re-test with the platform-provided binary
		if testIptablesAvailable() {
			return true
		}
		log.Printf("[iptables] platform fix applied but iptables still failing")
	}
	return false
})

// IsCompatMode returns true when the node has NET_ADMIN but no working iptables.
// This is now a last-resort fallback — iKuai with bundled host iptables runs normal mode.
func IsCompatMode() bool {
	capOK, _ := CheckCapability()
	return capOK && !Available()
}

// DetectRuntimeMode runs early detection and logs the result.
func DetectRuntimeMode() {
	capOK, _ := CheckCapability()
	hostNet := CheckHostNetwork()
	iptOK := Available()
	platform := DetectPlatform()

	if platform != PlatformLinux {
		log.Printf("[runtime] platform: %s", platform)
	}

	if !capOK {
		log.Printf("[runtime] mode: limited (no NET_ADMIN)")
	} else if hostNet && iptOK {
		log.Printf("[runtime] mode: normal (host network, iptables OK)")
	} else if !hostNet && iptOK {
		log.Printf("[runtime] mode: bridge (iptables OK, no host network)")
	} else if !iptOK {
		log.Printf("[runtime] mode: compat (NET_ADMIN OK, iptables unavailable)")
	}
}

// CheckCapability tests if the runtime has NET_ADMIN.
// Returns (ok, reason) where reason explains what failed.
var cachedCapability = sync.OnceValues(func() (bool, string) {
	// NET_ADMIN check — try bridge (kernel built-in), fall back to iptables
	if exec.Command("ip", "link", "add", "hy2cap_test", "type", "bridge").Run() == nil {
		exec.Command("ip", "link", "del", "hy2cap_test").Run()
		debugLog("[cap] NET_ADMIN confirmed via bridge creation")
		return true, ""
	}
	debugLog("[cap] bridge creation failed, trying iptables")
	if exec.Command(IptVariant(), "-L", "-n").Run() == nil {
		debugLog("[cap] NET_ADMIN confirmed via iptables-legacy")
		return true, ""
	}
	if exec.Command("iptables", "-L", "-n").Run() == nil {
		debugLog("[cap] NET_ADMIN confirmed via iptables-nft")
		return true, ""
	}
	if exec.Command("chroot", "/host", "/usr/sbin/iptables", "-L", "-n").Run() == nil {
		debugLog("[cap] NET_ADMIN confirmed via chroot /host iptables")
		return true, ""
	}
	return false, "no NET_ADMIN capability (bridge creation, iptables-legacy, iptables, and chroot /host all failed)"
})

func CheckCapability() (bool, string) {
	return cachedCapability()
}

// OriginalDst retrieves the original destination from a DNAT'd connection.
// Uses SyscallConn to avoid File() which puts the conn into blocking mode.
func OriginalDst(conn net.Conn) (string, error) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return "", fmt.Errorf("not a TCP connection")
	}
	rc, err := tcpConn.SyscallConn()
	if err != nil {
		return "", err
	}

	const SO_ORIGINAL_DST = 80
	var addr syscall.RawSockaddrInet4
	var getsockoptErr error
	err = rc.Control(func(fd uintptr) {
		addrLen := uint32(unsafe.Sizeof(addr))
		_, _, errno := syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			syscall.SOL_IP,
			SO_ORIGINAL_DST,
			uintptr(unsafe.Pointer(&addr)),
			uintptr(unsafe.Pointer(&addrLen)),
			0,
		)
		if errno != 0 {
			getsockoptErr = fmt.Errorf("getsockopt SO_ORIGINAL_DST: %v", errno)
		}
	})
	if err != nil {
		return "", err
	}
	if getsockoptErr != nil {
		return "", getsockoptErr
	}

	port := binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&addr.Port))[:])
	ip := net.IPv4(addr.Addr[0], addr.Addr[1], addr.Addr[2], addr.Addr[3])
	return fmt.Sprintf("%s:%d", ip, port), nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CheckHostNetwork returns true if the container appears to be running with --network host.
func CheckHostNetwork() bool {
	// In host network mode the container sees host-level interfaces like
	// docker0, br-xxx, real NICs. In bridge/custom network mode, these
	// are NOT visible — only eth0 (veth) + lo + any TUN/ipsec/ppp
	// interfaces created by the app itself.
	// Check for docker0 or br-* which only exist in host network mode.
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		name := e.Name()
		if name == "docker0" || strings.HasPrefix(name, "br-") {
			return true
		}
	}
	return false
}

// ip6Variant picks the ip6tables binary the same way iptVariant does.
var ip6Variant = sync.OnceValue(func() string {
	if out, err := exec.Command("ip6tables", "-V").CombinedOutput(); err == nil && strings.Contains(string(out), "nf_tables") {
		return "ip6tables"
	}
	if exec.Command("ip6tables-legacy", "-L", "-n").Run() == nil {
		return "ip6tables-legacy"
	}
	return "ip6tables"
})

// Prog: the iptables (v6: ip6tables) binary.
func Prog(v6 bool) string {
	if v6 {
		return ip6Variant()
	}
	return IptVariant()
}

// OK runs one iptables command and reports whether it succeeded.
func OK(v6 bool, args ...string) bool {
	return Exec(Prog(v6), args...).Run() == nil
}

// IP runs one ip(8) command (v6: ip -6).
func IP(v6 bool, args ...string) error {
	if v6 {
		args = append([]string{"-6"}, args...)
	}
	return exec.Command("ip", args...).Run()
}

var debugMode = func() bool {
	v := strings.ToLower(os.Getenv("DEBUG"))
	return v == "true" || v == "1" || v == "yes"
}()

func debugLog(format string, args ...any) {
	if debugMode {
		log.Printf(format, args...)
	}
}

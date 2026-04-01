package app

import (
	"log"
	"os"
	"syscall"
)

// ensureTunDevice creates /dev/net/tun if it doesn't exist.
// This is needed when the container uses device_cgroup_rules: ['c 10:200 rwm']
// instead of --device /dev/net/tun (which iKuai's Docker security checker prohibits).
// The mknod call requires the device cgroup to allow major 10, minor 200.
func ensureTunDevice() {
	// Create /dev/net/tun (TUN/TAP) if missing — needed by kernel-libipsec + TUN capture
	ensureCharDevice("/dev/net/tun", 10, 200)
	// Create /dev/ppp if missing — needed by pppd for L2TP
	ensureCharDevice("/dev/ppp", 108, 0)
}

func ensureCharDevice(path string, major, minor int) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	dir := path[:len(path)-len(path[findLastSlash(path):])]
	os.MkdirAll(dir, 0755)
	dev := int((major << 8) | minor)
	if err := syscall.Mknod(path, syscall.S_IFCHR|0666, dev); err != nil {
		log.Printf("[dev] mknod %s (c %d:%d) failed: %v", path, major, minor, err)
		return
	}
	log.Printf("[dev] created %s (c %d:%d) via mknod", path, major, minor)
}

func findLastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return 0
}

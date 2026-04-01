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
	const tunPath = "/dev/net/tun"
	if _, err := os.Stat(tunPath); err == nil {
		return // already exists
	}
	os.MkdirAll("/dev/net", 0755)
	// mknod /dev/net/tun c 10 200
	// dev_t = major<<8 | minor (Linux dev_t encoding for old-style mknod)
	dev := int((10 << 8) | 200)
	if err := syscall.Mknod(tunPath, syscall.S_IFCHR|0666, dev); err != nil {
		log.Printf("[tun] mknod %s failed: %v (need device_cgroup_rules: ['c 10:200 rwm'])", tunPath, err)
		return
	}
	log.Printf("[tun] created %s via mknod", tunPath)
}

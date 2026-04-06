package app

import (
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Platform identifiers
const (
	PlatformLinux   = "linux"
	PlatformIKuai   = "ikuai"
	PlatformOpenWrt = "openwrt"
)

// platformAdapter defines the interface for platform-specific compatibility.
// Each platform (iKuai, OpenWrt, etc.) can provide its own implementation.
type platformAdapter interface {
	// Name returns the platform identifier.
	Name() string
	// FixIPTables attempts to make iptables work on this platform.
	// Returns true if iptables is now functional.
	FixIPTables() bool
	// IPTExec returns a command for executing iptables on this platform.
	// prog is "iptables-legacy" or "iptables", args are the iptables arguments.
	// Returns nil if no platform-specific handling is needed.
	IPTExec(prog string, args []string) *exec.Cmd
}

// activePlatform holds the platform adapter after detection.
var activePlatform platformAdapter

// detectedPlatform caches the detected platform at startup.
var detectedPlatform = sync.OnceValue(func() string {
	// /proc/ikuai is iKuai's custom procfs — definitive
	if _, err := os.Stat("/proc/ikuai"); err == nil {
		log.Printf("[platform] detected iKuai (/proc/ikuai exists)")
		return PlatformIKuai
	}
	// /proc/version contains compiler/platform string — kernel-level, always readable
	if data, err := os.ReadFile("/proc/version"); err == nil {
		v := string(data)
		if strings.Contains(v, "iKuai") {
			log.Printf("[platform] detected iKuai (kernel version string)")
			return PlatformIKuai
		}
		if strings.Contains(v, "OpenWrt") {
			log.Printf("[platform] detected OpenWrt (kernel version string)")
			return PlatformOpenWrt
		}
	}
	// Custom iptables targets as last resort
	if data, err := os.ReadFile("/proc/net/ip_tables_targets"); err == nil {
		if strings.Contains(string(data), "IKQUEUE") {
			log.Printf("[platform] detected iKuai (IKQUEUE target)")
			return PlatformIKuai
		}
	}
	return PlatformLinux
})

// DetectPlatform returns the detected platform identifier.
func DetectPlatform() string {
	return detectedPlatform()
}

// initPlatformAdapter creates the appropriate adapter for the detected platform.
func initPlatformAdapter() {
	switch DetectPlatform() {
	case PlatformIKuai:
		activePlatform = &ikuaiAdapter{}
	// Future: case PlatformOpenWrt: activePlatform = &openwrtAdapter{}
	default:
		activePlatform = nil
	}
}

// platformFixIPTables attempts platform-specific iptables fix.
// Returns true if iptables is now working.
func platformFixIPTables() bool {
	if activePlatform == nil {
		return false
	}
	log.Printf("[platform] attempting %s iptables compatibility fix...", activePlatform.Name())
	if activePlatform.FixIPTables() {
		log.Printf("[platform] %s iptables fix successful", activePlatform.Name())
		return true
	}
	log.Printf("[platform] %s iptables fix failed, will fall back to gvisor compat", activePlatform.Name())
	return false
}

// platformIPTExec returns a platform-specific iptables command, or nil for default.
func platformIPTExec(prog string, args []string) *exec.Cmd {
	if activePlatform != nil {
		return activePlatform.IPTExec(prog, args)
	}
	return nil
}

// --- iKuai adapter ---

const (
	// Bundles are stored compressed; extracted on demand at runtime.
	platformBundleDir = "/opt/platform-compat"
)

type ikuaiAdapter struct {
	iptBin string // path to extracted iptables binary
	ldPath string // LD_LIBRARY_PATH for the binary
	ready  bool
}

func (a *ikuaiAdapter) Name() string { return "ikuai" }

func (a *ikuaiAdapter) FixIPTables() bool {
	bundlePath := platformBundleDir + "/ikuai-iptables.tar.gz"
	extractDir := platformBundleDir + "/ikuai"

	if _, err := os.Stat(bundlePath); err != nil {
		log.Printf("[ikuai] iptables bundle not found at %s", bundlePath)
		return false
	}

	// Extract on demand (stays compressed until needed)
	binPath := extractDir + "/usr/sbin/xtables-legacy-multi"
	if _, err := os.Stat(binPath); err != nil {
		log.Printf("[ikuai] extracting iptables bundle...")
		os.MkdirAll(extractDir, 0755)
		cmd := exec.Command("sh", "-c", "gunzip -c "+bundlePath+" | tar xf - -C "+extractDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[ikuai] extract failed: %v: %s", err, string(out))
			return false
		}
		// Create required symlinks for the uClibc runtime
		os.Symlink("libuClibc-1.0.40.so", extractDir+"/lib/libc.so.0")
		os.Symlink("libip4tc.so.2.0.0", extractDir+"/usr/lib/libip4tc.so.2")
		os.Symlink("libip6tc.so.2.0.0", extractDir+"/usr/lib/libip6tc.so.2")
		os.Symlink("libxtables.so.12.2.0", extractDir+"/usr/lib/libxtables.so.12")
	}

	// The ELF interpreter is hardcoded as /lib/ld64-uClibc.so.0 in the binary.
	// Symlink to the extracted copy so the kernel can find it.
	os.Symlink(extractDir+"/lib/ld64-uClibc-1.0.40.so", "/lib/ld64-uClibc.so.0")
	os.Symlink(extractDir+"/lib/ld64-uClibc-1.0.40.so", "/lib/ld64-uClibc.so.1")

	a.iptBin = binPath
	a.ldPath = extractDir + "/lib:" + extractDir + "/usr/lib"

	// Test before marking ready
	testCmd := exec.Command("sh", "-c",
		"LD_LIBRARY_PATH="+a.ldPath+" "+a.iptBin+" iptables -t nat -L -n")
	if out, err := testCmd.CombinedOutput(); err != nil {
		log.Printf("[ikuai] iptables test failed: %v: %s", err, string(out))
		return false
	}

	a.ready = true
	return true
}

func (a *ikuaiAdapter) IPTExec(prog string, args []string) *exec.Cmd {
	if !a.ready || prog != "iptables-legacy" {
		return nil
	}
	shellCmd := "LD_LIBRARY_PATH=" + a.ldPath + " " + a.iptBin + " iptables"
	for _, arg := range args {
		shellCmd += " " + arg
	}
	return exec.Command("sh", "-c", shellCmd)
}

package app

import (
	"os"
	"os/exec"

	"github.com/FrankoonG/hy2scale/core/netfilter"
)

// IsCompatMode: NET_ADMIN but no working iptables (core/netfilter).
func IsCompatMode() bool { return netfilter.IsCompatMode() }

// CheckCapability tests if the runtime has NET_ADMIN.
func CheckCapability() (bool, string) { return netfilter.CheckCapability() }

// CheckHostNetwork: the node runs in the host's network namespace.
func CheckHostNetwork() bool { return netfilter.CheckHostNetwork() }

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

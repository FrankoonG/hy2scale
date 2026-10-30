//go:build !linux

package netfilter

import (
	"errors"
	"net"
	"os/exec"
)

// Other platforms have no netfilter: nothing is captured or redirected.

var errUnsupported = errors.New("not supported on this platform")

const (
	PlatformLinux   = "linux"
	PlatformIKuai   = "ikuai"
	PlatformOpenWrt = "openwrt"
)

func IptVariant() string                         { return "" }
func Exec(prog string, args ...string) *exec.Cmd { return exec.Command(prog, args...) }
func Run(prog string, args ...string)            {}
func Available() bool                            { return false }
func IsCompatMode() bool                         { return false }
func DetectRuntimeMode()                         {}
func DetectPlatform() string                     { return "" }
func CheckCapability() (bool, string)            { return false, errUnsupported.Error() }
func CheckHostNetwork() bool                     { return false }
func OriginalDst(net.Conn) (string, error)       { return "", errUnsupported }
func Prog(v6 bool) string                        { return "" }
func OK(v6 bool, args ...string) bool            { return false }
func IP(v6 bool, args ...string) error           { return errUnsupported }

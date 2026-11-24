//go:build linux && !386

package netfilter

import "syscall"

const sysGetsockopt = syscall.SYS_GETSOCKOPT

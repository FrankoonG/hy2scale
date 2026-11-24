//go:build linux && 386

package netfilter

// 386 has no getsockopt in the syscall package (glibc goes through
// socketcall); the direct system call exists since Linux 4.3.
const sysGetsockopt = 365

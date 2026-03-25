package app

import "syscall"

const sysIoctl = syscall.SYS_IOCTL

func rawSyscall(trap, a1, a2, a3 uintptr) (uintptr, uintptr, uintptr) {
	r1, r2, errno := syscall.RawSyscall(trap, a1, a2, a3)
	return r1, r2, uintptr(errno)
}

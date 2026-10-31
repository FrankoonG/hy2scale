//go:build !linux

package sockmark

import "syscall"

// Other platforms have no socket marks: the helpers dial plainly.

// Control is a no-op hook.
func Control(_, _ string, _ syscall.RawConn) error { return nil }

// Conn is a no-op.
func Conn(any) {}

// Available: never.
func Available() bool { return false }

// Install is a no-op.
func Install() {}

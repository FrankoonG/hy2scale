//go:build linux

package mdns

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// reuseControl lets the socket share port 5353 with avahi, systemd-resolved
// and other responders on the host.
func reuseControl(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
		if serr == nil {
			// Best effort: very old kernels lack SO_REUSEPORT.
			_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		}
	})
	if err != nil {
		return err
	}
	return serr
}

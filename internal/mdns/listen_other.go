//go:build !linux

package mdns

import "syscall"

// reuseControl is nil off Linux: the socket is opened plainly and fails to
// bind if another responder holds port 5353 exclusively.
var reuseControl func(network, address string, c syscall.RawConn) error

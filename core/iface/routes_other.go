//go:build !linux

package iface

import (
	"fmt"
	"syscall"
)

// Other platforms: the routing table is not read (every destination goes
// to the egress) and a socket can't be pinned to an interface, so only the
// interface the system routes through can carry the egress.

func loadRoutes() *routeInfo {
	return &routeInfo{gateway4: map[string]string{}, gateway6: map[string]string{}}
}

func kernelRoutes(iface string, fam int) bool { return DefaultRouteIface(fam) == iface }

func bindControl(iface string) func(string, string, syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error {
		return fmt.Errorf("binding to %s: not supported on this platform", iface)
	}
}

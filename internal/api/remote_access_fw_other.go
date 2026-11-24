//go:build !linux

package api

import "net/netip"

// No host firewall handling off Linux.
func raFWOpen(id, proto string, visitor netip.Addr, port int) func() { return nil }

func raFWSweep() {}

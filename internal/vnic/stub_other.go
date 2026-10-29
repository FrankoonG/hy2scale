//go:build !linux

package vnic

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

var errUnsupported = errors.New("vnic: built-in child interfaces need Linux")

// Iface is a running built-in child interface (Linux only).
type Iface struct{}

func Start(ctx context.Context, cfg Config) (*Iface, error) { return nil, errUnsupported }
func (i *Iface) Status() Status                             { return Status{} }
func (i *Iface) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return nil, errUnsupported
}
func (i *Iface) ListenPacket(network string, laddr netip.AddrPort) (net.PacketConn, error) {
	return nil, errUnsupported
}
func (i *Iface) LookupIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return nil, errUnsupported
}
func (i *Iface) Renew() error { return errUnsupported }
func (i *Iface) Close() error { return nil }

func CanCreate(parent string) error { return errUnsupported }
func IsPhysical(name string) bool   { return false }
func IsWireless(name string) bool   { return false }
func LinkType(name string) string   { return "" }

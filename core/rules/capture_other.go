//go:build !linux

package rules

import (
	"context"
	"errors"
	"net"
	"time"
)

// Other platforms: no capture; rules route VPN and proxy users' flows only.

const (
	ruleProxyIP  = "127.0.0.99"
	ruleProxyIP6 = "::1"
)

var errNoCapture = errors.New("capture is not supported on this platform")

func (e *Engine) startCapture(ctx context.Context)               {}
func captureTeardown()                                           {}
func (e *Engine) captureLines(target string) []string            { return nil }
func runCaptureLine(line string, del bool)                       {}
func udpOrigDst(oob []byte) *net.UDPAddr                         { return nil }
func conntrackOrigDst(proto, srcAddr string, dstPort int) string { return "" }
func dialUDPAs(from, to *net.UDPAddr) (*net.UDPConn, error)      { return nil, errNoCapture }

func dialUDPMarked(addr string, mark int) (net.Conn, error) {
	return net.DialTimeout("udp", addr, 5*time.Second)
}

func dialTCPMarked(addr string, mark int) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 10*time.Second)
}

package rules

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/FrankoonG/hy2scale/core/sockmark"
)

// System traffic capture moved to core/policy/capture (spec §3); the rule
// engine only routes the flows handed to it. These stubs keep the engine
// building until it is removed.

const (
	ruleProxyIP  = "127.0.0.99"
	ruleProxyIP6 = "::1"
)

var errNoCapture = errors.New("capture moved to core/policy/capture")

func (e *Engine) startCapture(ctx context.Context)               {}
func captureTeardown()                                           {}
func (e *Engine) captureLines(target string) []string            { return nil }
func runCaptureLine(line string, del bool)                       {}
func udpOrigDst(oob []byte) *net.UDPAddr                         { return nil }
func conntrackOrigDst(proto, srcAddr string, dstPort int) string { return "" }
func dialUDPAs(from, to *net.UDPAddr) (*net.UDPConn, error)      { return nil, errNoCapture }

func dialUDPMarked(addr string, mark int) (net.Conn, error) {
	return sockmark.DialTimeout("udp", addr, 5*time.Second)
}

func dialTCPMarked(addr string, mark int) (net.Conn, error) {
	return sockmark.DialTimeout("tcp", addr, 10*time.Second)
}

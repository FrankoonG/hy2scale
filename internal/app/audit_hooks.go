package app

import (
	"net"
	"strings"

	"github.com/FrankoonG/hy2scale/core/audit"
)

// Audit records of user flows (core/audit). Each inbound begins the record
// where it knows the user, client and destination, carries it in the dial
// context (routing, exit and egress add to it: route.go, egress.go,
// core/exit) and ends it when the flow closes; bytes are counted by the
// user meter (meter.go). With audit off every helper returns or takes nil.

// beginUserFlow starts the record of a user flow from src to dst.
func beginUserFlow(inbound, proto, user string, src net.Addr, dst string) *audit.Flow {
	if !audit.Enabled() {
		return nil
	}
	s := ""
	if src != nil {
		s = src.String()
	}
	return beginUserFlowStr(inbound, proto, user, s, dst)
}

// beginUserFlowStr: beginUserFlow with the client address as a string.
func beginUserFlowStr(inbound, proto, user, src, dst string) *audit.Flow {
	f := audit.Begin(inbound, proto)
	if f == nil {
		return nil
	}
	if user == "__psk__" { // IKEv2 shared-key clients: no user account
		user = ""
	}
	f.SetUser(user)
	f.SetSrc(src, "")
	f.SetDst(dst)
	return f
}

// auditRefused records a connection refused at authentication (the name
// it tried, if any).
func auditRefused(inbound, user string, conn net.Conn, err error) {
	f := beginUserFlow(inbound, "tcp", user, conn.RemoteAddr(), "")
	f.Fail(audit.ResultRefused, err)
	f.End(nil)
}

// wgPeerName: the WireGuard peer of a connection key ("peer:<name>"), the
// identity its flows are recorded under.
func wgPeerName(key string) string {
	if name, ok := strings.CutPrefix(key, "peer:"); ok {
		return name
	}
	return ""
}

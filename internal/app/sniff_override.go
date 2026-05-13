package app

import (
	"log"
	"net"
	"time"

	"github.com/FrankoonG/hy2scale/internal/sniff"
)

// peekFirstChunk reads up to sniff.MinPeek bytes from `conn` with a short
// deadline so we don't stall on protocols where the server speaks first
// (SMTP, FTP, SSH). Returns whatever arrived within the window; the bytes
// are NOT pushed back into the stream — the caller forwards them verbatim
// to the upstream after dialing, so this is safe and consume-once.
//
// Window choice: TLS Client Hello and HTTP request lines are typically
// produced within milliseconds of the client receiving its proxy SUCCESS
// reply. 300 ms is conservative enough to cover slow links while still
// being invisible end-to-end. On timeout (server-speaks-first) we return
// the partial buffer (often nil); sniffOverrideHost then no-ops and the
// dial proceeds with the original IP.
func peekFirstChunk(conn net.Conn) []byte {
	buf := make([]byte, sniff.MinPeek)
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, _ := conn.Read(buf)
	_ = conn.SetReadDeadline(time.Time{}) // clear
	if n <= 0 {
		return nil
	}
	return buf[:n]
}

// sniffOverrideHost replaces an IP-form `host:port` destination with one
// whose host part comes from the application-layer hostname recovered from
// the client's first-chunk payload (TLS Client Hello SNI, HTTP Host).
//
// Use case: a DNS-poisoned client resolved `www.google.com` to a fake
// IP, dialed THAT IP through our proxy, and our listener received ATYP=
// IPv4 with the fake IP. Forwarding that IP literally to the exit's
// net.Dial dials the wrong server. By peeking at the next bytes the
// client sends (which is the TLS Client Hello for HTTPS, or the HTTP
// request for plain HTTP), we recover the real hostname; the exit's
// own clean resolver then turns it back into a real IP.
//
// Behaviour:
//   - If the destination's host is NOT a literal IP, returns dst unchanged
//     (client already sent a hostname, nothing to sniff — the legacy
//     hostname-via-exit path already handles it).
//   - If the destination IS an IP but `peeked` contains no usable hostname
//     (truncated, non-TLS/non-HTTP, malformed), returns dst unchanged.
//   - If a hostname is recovered, returns "<sniffed>:<port>" using the
//     ORIGINAL port from dst.
//
// The peeked bytes are NOT consumed here — the caller must still forward
// them verbatim to the upstream after dialing.
func sniffOverrideHost(dst string, peeked []byte) string {
	host, port, err := net.SplitHostPort(dst)
	if err != nil || net.ParseIP(host) == nil {
		return dst
	}
	sniffed := sniff.Host(peeked)
	if sniffed == "" {
		return dst
	}
	log.Printf("[sniff] override %s → %s:%s (recovered from %d-byte peek)",
		dst, sniffed, port, len(peeked))
	return net.JoinHostPort(sniffed, port)
}

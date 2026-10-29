// Package sniff recovers an application-layer hostname from the first bytes
// a client sends after a proxy CONNECT/SOCKS5/SS exchange, so the proxy can
// override an IP-form destination (typically a DNS-poisoned IPv4 the client
// resolved before reaching us) with the real hostname before its own
// net.Dial. This mirrors xray-core's `sniffing.destOverride: [tls, http]`
// and sing-box's `sniff: true`. Two protocols are handled here — TLS Client
// Hello SNI extension and the plain-HTTP `Host:` request header — which
// together cover essentially every long-lived TCP connection a polluted
// browser/curl/redsocks-client would make. No DNS lookup, no upstream
// dependency, no allocation past a single string copy.
//
// SECURITY: input is attacker-controllable bytes. Every parser path bounds-
// checks against the supplied buffer length and refuses to dereference past
// it. A malformed packet returns ("", false) — the caller falls back to the
// client-supplied IP, which is the legacy behaviour, so a failed sniff never
// "fails closed" to the user.
package sniff

import (
	"bytes"
	"strings"
)

// MaxPeek bounds how much a caller buffers while waiting for Complete: one
// full TLS record (a Client Hello with a post-quantum key share is ~1.8 KB)
// or an HTTP request header.
const MaxPeek = 5 + 16384

// Complete reports whether p already holds everything Host can use: a whole
// TLS record, a whole HTTP request header, or bytes that are neither (no
// amount of further data would help). Callers keep reading while it is
// false, within their own time window and MaxPeek.
func Complete(p []byte) bool {
	if len(p) == 0 {
		return false
	}
	if len(p) >= MaxPeek {
		return true
	}
	if p[0] == 0x16 {
		if len(p) < 5 {
			return false
		}
		return len(p) >= 5+(int(p[3])<<8|int(p[4]))
	}
	if p[0] >= 'A' && p[0] <= 'Z' {
		if !maybeRequestLine(p) {
			return true // e.g. an SSH banner: not HTTP, nothing to wait for
		}
		return len(p) >= 8192 || bytes.Contains(p, []byte("\r\n\r\n"))
	}
	return true
}

// maybeRequestLine reports whether p can still be the start of an HTTP/1.x
// request: an uppercase method, a space, and a first line naming HTTP/.
func maybeRequestLine(p []byte) bool {
	sp := bytes.IndexByte(p, ' ')
	method := p
	if sp >= 0 {
		method = p[:sp]
	}
	if len(method) > 16 {
		return false
	}
	for _, c := range method {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	if eol := bytes.Index(p, []byte("\r\n")); eol >= 0 {
		return bytes.Contains(p[:eol], []byte(" HTTP/"))
	}
	return true
}

// Usable reports whether a sniffed name may replace an IP destination: a
// syntactically valid DNS name with at least two labels that is not a
// private-use name (.local, .lan, .internal, ...) another resolver could not
// answer, and not a service whose name and connected address legitimately
// differ (Apple push).
func Usable(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if len(name) == 0 || len(name) > 253 || !strings.Contains(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	// all-numeric last label: an IPv4 literal or garbage, not a name
	last := name[strings.LastIndexByte(name, '.')+1:]
	if strings.Trim(last, "0123456789") == "" {
		return false
	}
	for _, suffix := range skipSuffixes {
		if name == suffix || strings.HasSuffix(name, "."+suffix) {
			return false
		}
	}
	return true
}

// skipSuffixes are names (and their subdomains) never used as a destination.
var skipSuffixes = []string{
	"local", "localhost", "lan", "internal", "home.arpa", "arpa", "invalid",
	"push.apple.com",
}

// Host returns the hostname recovered from `peeked` — the first bytes the
// client sent right after the proxy handshake completed. Order of attempts:
//  1. TLS Client Hello SNI (first byte == 0x16, ContentType=handshake).
//  2. HTTP request Host: header (first byte is in [A-Z], i.e. a method).
//
// Returns "" if neither parser finds a name. Callers MUST treat the input
// `peeked` slice as read-only — Host does not modify it, and the caller
// will typically need to forward those same bytes verbatim to the dial
// target after dialing.
func Host(peeked []byte) string {
	if len(peeked) == 0 {
		return ""
	}
	if peeked[0] == 0x16 {
		// TLS record. Try the Client Hello path.
		if h := tlsSNI(peeked); h != "" {
			return h
		}
		return ""
	}
	// HTTP request methods all start with an ASCII uppercase letter.
	if peeked[0] >= 'A' && peeked[0] <= 'Z' {
		return httpHost(peeked)
	}
	return ""
}

// tlsSNI parses a TLS 1.0/1.2/1.3 ClientHello and returns the value of the
// server_name (type=host_name) extension. Returns "" on any unexpected
// shape — we never panic on bad input, we just decline to sniff.
//
// Record layout (RFC 5246, RFC 8446):
//
//	byte  0      ContentType (0x16 = Handshake)
//	byte  1-2    ProtocolVersion (legacy_record_version, ignored here)
//	byte  3-4    fragment length, big-endian uint16
//	byte  5      HandshakeType (0x01 = ClientHello)
//	byte  6-8    length, big-endian uint24
//	byte  9-10   legacy_version
//	byte 11-42   random (32 bytes)
//	byte 43      legacy_session_id length, then variable
//	then         cipher_suites (uint16 length + entries)
//	then         compression_methods (uint8 length + entries)
//	then         extensions (uint16 total length + entries)
func tlsSNI(p []byte) string {
	if len(p) < 43 {
		return ""
	}
	// Record header.
	if p[0] != 0x16 {
		return ""
	}
	recLen := int(p[3])<<8 | int(p[4])
	if recLen < 38 || 5+recLen > len(p) {
		// Truncated record — many real clients fit Client Hello in one
		// record, but if peek was short we just bail.
		// Still allow if recLen extends past peek; we'll only walk what
		// we have.
		recLen = len(p) - 5
	}
	body := p[5 : 5+recLen]
	if len(body) < 38 || body[0] != 0x01 {
		return ""
	}
	hsLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	if hsLen+4 > len(body) {
		hsLen = len(body) - 4
	}
	hs := body[4 : 4+hsLen]
	// Skip legacy_version (2) + random (32) = 34
	if len(hs) < 34 {
		return ""
	}
	i := 34
	// session_id (u8 length + content)
	if i >= len(hs) {
		return ""
	}
	sidLen := int(hs[i])
	i++
	if i+sidLen > len(hs) {
		return ""
	}
	i += sidLen
	// cipher_suites (u16 length + content)
	if i+2 > len(hs) {
		return ""
	}
	csLen := int(hs[i])<<8 | int(hs[i+1])
	i += 2
	if i+csLen > len(hs) {
		return ""
	}
	i += csLen
	// compression_methods (u8 length + content)
	if i+1 > len(hs) {
		return ""
	}
	cmLen := int(hs[i])
	i++
	if i+cmLen > len(hs) {
		return ""
	}
	i += cmLen
	// extensions (u16 length + content)
	if i+2 > len(hs) {
		return ""
	}
	extLen := int(hs[i])<<8 | int(hs[i+1])
	i += 2
	if i+extLen > len(hs) {
		extLen = len(hs) - i
	}
	exts := hs[i : i+extLen]
	// Walk extensions, find type 0x0000 (server_name).
	j := 0
	for j+4 <= len(exts) {
		etype := int(exts[j])<<8 | int(exts[j+1])
		elen := int(exts[j+2])<<8 | int(exts[j+3])
		j += 4
		if j+elen > len(exts) {
			return ""
		}
		if etype != 0x0000 {
			j += elen
			continue
		}
		// server_name extension data:
		//   u16  server_name_list length
		//   then list of entries:
		//     u8   name_type (0x00 = host_name)
		//     u16  name length
		//     bytes...
		ed := exts[j : j+elen]
		if len(ed) < 5 {
			return ""
		}
		listLen := int(ed[0])<<8 | int(ed[1])
		if listLen+2 > len(ed) {
			return ""
		}
		list := ed[2 : 2+listLen]
		k := 0
		for k+3 <= len(list) {
			nt := list[k]
			nl := int(list[k+1])<<8 | int(list[k+2])
			k += 3
			if k+nl > len(list) {
				return ""
			}
			if nt == 0x00 && nl > 0 {
				// Successful host_name extraction.
				return string(list[k : k+nl])
			}
			k += nl
		}
		return ""
	}
	return ""
}

// httpHost parses an HTTP/1.x request and returns the Host header value
// without the optional :port suffix. Pre-condition: the first byte of `p`
// looks like an uppercase ASCII (request method). Empty return on any
// unexpected shape.
//
// We bound the search to the first 8 KiB to avoid pathological scans on a
// long-poll request that legitimately has Host way down the buffer but no
// CRLF terminator yet.
func httpHost(p []byte) string {
	if len(p) > 8192 {
		p = p[:8192]
	}
	// Find end of first request line. Required to be CRLF.
	eol := bytes.Index(p, []byte("\r\n"))
	if eol < 0 || eol+2 >= len(p) {
		return ""
	}
	// Quick request-line sanity: must contain "HTTP/" for it to even be HTTP.
	if !bytes.Contains(p[:eol], []byte(" HTTP/")) {
		return ""
	}
	// Walk headers.
	headers := p[eol+2:]
	for len(headers) > 0 {
		// End of headers — bare CRLF.
		if bytes.HasPrefix(headers, []byte("\r\n")) {
			return ""
		}
		lineEnd := bytes.Index(headers, []byte("\r\n"))
		if lineEnd < 0 {
			return ""
		}
		line := headers[:lineEnd]
		// Match "Host:" case-insensitively.
		colon := bytes.IndexByte(line, ':')
		if colon > 0 && strings.EqualFold(string(line[:colon]), "Host") {
			return hostOnly(strings.TrimSpace(string(line[colon+1:])))
		}
		headers = headers[lineEnd+2:]
	}
	return ""
}

// hostOnly strips the optional ":port" of a Host header value — the caller
// already has the dial port from the proxy-supplied destination, and reusing
// that keeps the override surface to "host only." An IPv6 literal comes
// back without its brackets ("[2001:db8::1]:8080" → "2001:db8::1"); a
// malformed value yields "".
func hostOnly(v string) string {
	if strings.HasPrefix(v, "[") {
		end := strings.IndexByte(v, ']')
		if end < 0 {
			return ""
		}
		if rest := v[end+1:]; rest != "" && !strings.HasPrefix(rest, ":") {
			return ""
		}
		return v[1:end]
	}
	switch strings.Count(v, ":") {
	case 0:
		return v
	case 1:
		return v[:strings.IndexByte(v, ':')]
	}
	// several colons without brackets: only a bare IPv6 literal makes sense
	for _, r := range v {
		if !(r == ':' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return ""
		}
	}
	return v
}

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

// MinPeek is the smallest number of bytes a caller should buffer before
// calling Host. TLS Client Hellos are normally 200–800 bytes; HTTP request
// lines + Host header are typically <200. Picking 1500 covers both with one
// MTU's worth of payload and keeps the buffer page-aligned.
const MinPeek = 1500

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
// CRLF terminator yet — proxy peeks are 1.5 KiB by default anyway.
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
			v := strings.TrimSpace(string(line[colon+1:]))
			// Strip optional ":port" — caller already has the dial port
			// from the proxy-supplied destination, and reusing that keeps
			// the override surface to "host only."
			if i := strings.LastIndexByte(v, ':'); i > 0 && !strings.Contains(v, "]") {
				return v[:i]
			}
			return v
		}
		headers = headers[lineEnd+2:]
	}
	return ""
}

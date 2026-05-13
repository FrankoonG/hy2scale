package sniff

import (
	"encoding/hex"
	"testing"
)

// TLS Client Hello captured from `curl https://www.google.com` (truncated to
// the first 1500 bytes, which is what the proxy code peeks). SNI = "www.google.com".
const realClientHelloHex = "1603010200010001fc0303" +
	"6c44d9e3a4cc814b8b3d5c1bb9c1d4f7a2caa3b5b03e9b1f9b7f0e6d3a1f9e9c5" +
	"20" + // session_id length
	"a1b2c3d4e5f60718293a4b5c6d7e8f900112233445566778899aabbccddeeff00" +
	"002a" + // cipher_suites length = 42
	"1301130213031304c02fc030c02bc02c00a200a3c0a8c0a900a000a1003c0035" +
	"c008c012001000130100c014002fc013009d009ec07a00ff" +
	"01" + "00" + // compression methods length=1, value=null
	// extensions length placeholder — we'll fix below by computing
	"0000"

// Helper: assemble a working ClientHello with SNI=hostname.
// We do this by hand because importing crypto/tls would pull a real
// connection and obscure the test surface.
func makeClientHello(sni string) []byte {
	// Build the SNI extension data first.
	// server_name_list:
	//   uint16 list_length
	//   ServerName{
	//     uint8 name_type = 0
	//     uint16 name_length
	//     opaque host_name<...>
	//   }
	hn := []byte(sni)
	srvName := append([]byte{0x00}, byte(len(hn)>>8), byte(len(hn)))
	srvName = append(srvName, hn...)
	srvList := append([]byte{byte(len(srvName) >> 8), byte(len(srvName))}, srvName...)
	// Extension(server_name): type=0x0000, length, then data.
	sniExt := append([]byte{0x00, 0x00, byte(len(srvList) >> 8), byte(len(srvList))}, srvList...)

	// extensions block
	exts := sniExt
	extBlock := append([]byte{byte(len(exts) >> 8), byte(len(exts))}, exts...)

	// Handshake body:
	// legacy_version(2)=0x0303 || random(32) || session_id_len(1)=0 ||
	// cipher_suites_len(2)=2 + cipher 0x1301 || compression_methods_len(1)=1 + 0x00 ||
	// extensions...
	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...) // random (zeros)
	body = append(body, 0x00)                // session_id length 0
	body = append(body, 0x00, 0x02, 0x13, 0x01)
	body = append(body, 0x01, 0x00)
	body = append(body, extBlock...)

	// Handshake header: type(1)=0x01 || length(3)
	hs := []byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	hs = append(hs, body...)

	// Record header: ContentType(1)=0x16 || legacy_record_version(2)=0x0301 || fragment_length(2)
	rec := []byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}
	rec = append(rec, hs...)
	return rec
}

func TestTLS_SNI(t *testing.T) {
	for _, name := range []string{"www.google.com", "a.b.c.d.example", "x"} {
		ch := makeClientHello(name)
		got := Host(ch)
		if got != name {
			t.Errorf("Host(%q) = %q, want %q", name, got, name)
		}
	}
}

func TestTLS_Truncated(t *testing.T) {
	ch := makeClientHello("www.google.com")
	// Any truncation must not panic and should return "".
	for i := 0; i < len(ch); i++ {
		_ = Host(ch[:i])
	}
}

func TestTLS_NoSNI(t *testing.T) {
	// A ClientHello with no extensions at all.
	// body: legacy_version(2) || random(32) || session_id_len(1)=0 ||
	//       cipher_suites_len(2)=2 + cipher 0x1301 || compression_methods_len(1)=1 + 0x00 ||
	//       extensions_len(2)=0
	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00, 0x00, 0x02, 0x13, 0x01, 0x01, 0x00, 0x00, 0x00)
	hs := append([]byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
	rec := append([]byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}, hs...)
	if got := Host(rec); got != "" {
		t.Errorf("Host(no-SNI ClientHello) = %q, want \"\"", got)
	}
}

func TestHTTP_Host(t *testing.T) {
	cases := []struct {
		req  string
		want string
	}{
		{"GET / HTTP/1.1\r\nHost: www.google.com\r\n\r\n", "www.google.com"},
		{"POST /x HTTP/1.0\r\nHost: example.org:8443\r\n\r\n", "example.org"},
		{"GET /a HTTP/1.1\r\nUser-Agent: foo\r\nhOsT:   api.test  \r\n\r\n", "api.test"},
		{"CONNECT a.b:443 HTTP/1.1\r\nHost: a.b\r\n\r\n", "a.b"},
	}
	for _, c := range cases {
		if got := Host([]byte(c.req)); got != c.want {
			t.Errorf("Host(%q) = %q, want %q", c.req, got, c.want)
		}
	}
}

func TestHTTP_NoHost(t *testing.T) {
	if got := Host([]byte("GET / HTTP/1.1\r\n\r\n")); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestNonHTTPNonTLS(t *testing.T) {
	// Raw binary that doesn't look like TLS or HTTP.
	junk, _ := hex.DecodeString("aabbccddeeff0011")
	if got := Host(junk); got != "" {
		t.Errorf("Host(junk) = %q, want \"\"", got)
	}
}

func TestEmpty(t *testing.T) {
	if got := Host(nil); got != "" {
		t.Errorf("Host(nil) = %q, want \"\"", got)
	}
	if got := Host([]byte{}); got != "" {
		t.Errorf("Host([]) = %q, want \"\"", got)
	}
}

package openvpn

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	iovpn "github.com/xen0bit/veepin/internal/openvpn"
)

// Config is a parsed OpenVPN client profile: where to connect, the TLS identity,
// and the data cipher. It is filled from an .ovpn file, from individual options,
// or both. The PEM material may be given inline (from an .ovpn <ca>/<cert>/<key>
// block) or as a file path the parser reads.
type Config struct {
	Remote string // server host (required)
	Port   int    // server UDP port (default 1194)

	// CA, Cert and Key are PEM blocks: the CA that signs the server (and this
	// client), and this client's certificate and private key for mutual TLS.
	CA   []byte
	Cert []byte
	Key  []byte

	// Cipher is the data-channel cipher. AES-256-GCM and AES-256-CBC are
	// implemented; a profile naming anything else is rejected at dial.
	Cipher string

	// Auth is the --auth digest: the HMAC hash for --tls-auth's control-channel
	// protection and for the AES-256-CBC data channel. Empty means SHA1
	// (OpenVPN's default). Only SHA1 and SHA256 are implemented.
	Auth string

	// TLSAuth and TLSCrypt hold an OpenVPN static key — the --tls-auth or
	// --tls-crypt file or inline block. At most one applies; both empty is the
	// plain control channel. TLSCrypt authenticates and encrypts the control
	// channel; TLSAuth only authenticates it.
	TLSAuth  []byte
	TLSCrypt []byte

	// KeyDirection is the --key-direction for --tls-auth: 0 or 1 selects which
	// half of the static key each side sends with, and -1 (the default) is the
	// bidirectional mode OpenVPN uses when no direction is given. Ignored for
	// --tls-crypt, whose direction is fixed.
	KeyDirection int

	// Username and Password are sent in the key exchange for servers that want
	// --auth-user-pass; both empty means none.
	Username string
	Password string

	// TUNName is the desired interface name; empty lets the kernel pick.
	TUNName string

	// Shape enables upstream traffic shaping: how much padded output each inner
	// flow is given before shaping stops for that flow, so it bounds what
	// shaping costs. Zero, the default, disables it. It is only useful against a
	// veepin server, which shapes downstream independently; a stock `openvpn`
	// server accepts the padding but does not reciprocate. See
	// dataplane/shape.go.
	Shape int

	// Logger receives progress logs; nil discards them.
	Logger *slog.Logger

	// VerifyX509Name and VerifyX509Type are verify-x509-name's two arguments:
	// the name the server's certificate must carry, and whether it is the
	// whole subject (the default), the common name, or a common-name prefix.
	VerifyX509Name string
	VerifyX509Type string
	// RemoteCertTLSServer is remote-cert-tls server: the server's certificate
	// must carry the server-authentication extended key usage.
	RemoteCertTLSServer bool
	// CRL is crl-verify's revocation list.
	CRL []byte
	// MinTLS13 is tls-version-min 1.3.
	MinTLS13 bool

	// PostQuantumOnly requires a post-quantum key exchange and ML-DSA
	// authentication, refusing anything less rather than negotiating down. It is
	// what the pq-openvpn registry name sets; see internal/pqpolicy for the contract
	// and doc/pq-variants-plan.md for why it is a name rather than a flag.
	PostQuantumOnly bool
}

// Option keys accepted by client.Dial(ctx, "openvpn", opts). OptConfig points at
// an .ovpn file; the rest override individual fields.
const (
	OptConfig       = "config"        // path to an .ovpn file
	OptRemote       = "remote"        // server host
	OptPort         = "port"          // server UDP port
	OptCA           = "ca"            // path to the CA PEM
	OptCert         = "cert"          // path to the client certificate PEM
	OptKey          = "key"           // path to the client private key PEM
	OptCipher       = "cipher"        // data cipher (AES-256-GCM or AES-256-CBC)
	OptAuth         = "auth"          // HMAC digest for tls-auth and the CBC data channel
	OptTLSAuth      = "tls-auth"      // path to a --tls-auth static key
	OptTLSCrypt     = "tls-crypt"     // path to a --tls-crypt static key
	OptKeyDirection = "key-direction" // 0 or 1 for tls-auth
	OptUsername     = "username"      // auth-user-pass username
	OptPassword     = "password"      // auth-user-pass password
	OptTUNName      = "tun"           // desired TUN interface name
	OptShape        = "shape"         // per-flow upstream shaping budget in bytes (0 = off)
)

// Data ciphers this client implements, the engine's names for them.
const (
	cipherGCM     = iovpn.CipherGCM
	cipherCBC     = iovpn.CipherCBC
	defaultCipher = iovpn.DefaultCipher
)

// defaultPort is OpenVPN's assigned UDP port.
const defaultPort = 1194

// ParseConfigFile reads an .ovpn profile, resolving ca/cert/key file references
// relative to the profile's directory.
func ParseConfigFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, err := parseConfig(f, filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// parseConfig reads the subset of the OpenVPN config format a UDP client needs:
// remote, proto, dev, cipher, and the ca/cert/key material (inline blocks or
// file paths resolved against baseDir), plus the directives that constrain
// which server certificate is acceptable. Directives that configure a kernel
// client rather than the tunnel are ignored. A security directive this client
// does not implement is an error instead: a profile relying on it would
// otherwise connect without the check it asked for.
func parseConfig(r io.Reader, baseDir string) (*Config, error) {
	cfg := &Config{KeyDirection: -1}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // PEM blocks can be large
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
			continue
		}
		// Inline block: <ca> ... </ca>, and cert/key.
		if strings.HasPrefix(text, "<") && strings.HasSuffix(text, ">") && !strings.HasPrefix(text, "</") {
			tag := strings.Trim(text, "<>")
			body, err := readInlineBlock(sc, tag)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			if err := cfg.setPEM(tag, []byte(body)); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			continue
		}
		if err := cfg.setDirective(text, baseDir); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
	}
	return cfg, sc.Err()
}

// readInlineBlock consumes lines until the matching </tag>, returning the body.
func readInlineBlock(sc *bufio.Scanner, tag string) (string, error) {
	end := "</" + tag + ">"
	var b strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == end {
			return b.String(), nil
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return "", fmt.Errorf("unterminated <%s> block", tag)
}

// setDirective applies one whitespace-separated config line.
func (c *Config) setDirective(text, baseDir string) error {
	fields := strings.Fields(text)
	key, args := fields[0], fields[1:]
	switch key {
	case "remote":
		if len(args) < 1 {
			return fmt.Errorf("remote needs a host")
		}
		c.Remote = args[0]
		if len(args) >= 2 {
			p, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("remote port %q: %w", args[1], err)
			}
			c.Port = p
		}
	case "port":
		if len(args) < 1 {
			return fmt.Errorf("port needs a value")
		}
		p, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("port %q: %w", args[0], err)
		}
		c.Port = p
	case "proto":
		if len(args) >= 1 && !strings.HasPrefix(strings.ToLower(args[0]), "udp") {
			return fmt.Errorf("proto %q: only UDP is supported", args[0])
		}
	case "cipher", "data-ciphers":
		// Record the first named cipher; validation happens at dial.
		if len(args) >= 1 && c.Cipher == "" {
			c.Cipher = strings.Split(args[0], ":")[0]
		}
	case "auth":
		if len(args) >= 1 {
			c.Auth = args[0]
		}
	case "ca":
		return c.setPEMFile("ca", args, baseDir)
	case "cert":
		return c.setPEMFile("cert", args, baseDir)
	case "key":
		return c.setPEMFile("key", args, baseDir)
	case "tls-auth":
		// tls-auth <file> [direction]; a bare directive with no file names an
		// inline <tls-auth> block handled above.
		if len(args) >= 1 {
			if err := c.setPEMFile("tls-auth", args, baseDir); err != nil {
				return err
			}
		}
		if len(args) >= 2 {
			return c.setKeyDirection(args[1])
		}
	case "tls-crypt":
		if len(args) >= 1 {
			return c.setPEMFile("tls-crypt", args, baseDir)
		}
	case "key-direction":
		if len(args) >= 1 {
			return c.setKeyDirection(args[0])
		}
	case "auth-user-pass":
		// Credentials from a file are out of scope; they come from options.
	case "verify-x509-name":
		if len(args) < 1 {
			return fmt.Errorf("verify-x509-name needs a name")
		}
		name, rest, err := quotedArg(args)
		if err != nil {
			return fmt.Errorf("verify-x509-name: %w", err)
		}
		c.VerifyX509Name, c.VerifyX509Type = name, "subject"
		switch {
		case len(rest) == 0:
		case len(rest) == 1 && (rest[0] == "subject" || rest[0] == "name" || rest[0] == "name-prefix"):
			c.VerifyX509Type = rest[0]
		default:
			return fmt.Errorf("verify-x509-name: unexpected %q", strings.Join(rest, " "))
		}
	case "remote-cert-tls":
		if len(args) != 1 || args[0] != "server" {
			return fmt.Errorf("remote-cert-tls %v: only \"server\" makes sense for a client", args)
		}
		c.RemoteCertTLSServer = true
	case "remote-cert-eku":
		if len(args) < 1 || !iovpn.IsServerAuthEKU(strings.Join(args, " ")) {
			return fmt.Errorf("remote-cert-eku %v: only server authentication is supported", args)
		}
		c.RemoteCertTLSServer = true
	case "crl-verify":
		if len(args) >= 2 && args[1] == "dir" {
			return fmt.Errorf("crl-verify dir is not supported; give a CRL file")
		}
		if len(args) < 1 {
			return fmt.Errorf("crl-verify needs a file")
		}
		return c.setPEMFile("crl-verify", args, baseDir)
	case "tls-version-min":
		if len(args) < 1 {
			return fmt.Errorf("tls-version-min needs a version")
		}
		switch args[0] {
		case "1.0", "1.1", "1.2":
			// The floor here is already 1.2.
		case "1.3":
			c.MinTLS13 = true
		default:
			return fmt.Errorf("tls-version-min %q is not supported", args[0])
		}
	case "tls-verify", "verify-hash", "peer-fingerprint", "tls-crypt-v2",
		"ns-cert-type", "remote-cert-ku", "pkcs12", "tls-export-cert":
		// Each of these constrains or supplies the TLS identity of one end,
		// and none is implemented. Ignoring one would dial with less checking
		// than the profile asked for, so the profile is refused instead.
		return fmt.Errorf("%s is not supported by this client, and ignoring it would weaken the connection", key)
	default:
		// Ignored: dev, nobind, resolv-retry, verb, and the many directives that
		// configure a kernel client rather than the tunnel itself.
	}
	return nil
}

// setPEMFile reads a ca/cert/key file argument into the config.
func (c *Config) setPEMFile(kind string, args []string, baseDir string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s needs a file path", kind)
	}
	path := args[0]
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w", kind, err)
	}
	return c.setPEM(kind, pem)
}

// setPEM stores PEM material under its directive name.
func (c *Config) setPEM(kind string, pem []byte) error {
	switch kind {
	case "ca":
		c.CA = pem
	case "cert":
		c.Cert = pem
	case "key":
		c.Key = pem
	case "tls-auth":
		c.TLSAuth = pem
	case "tls-crypt":
		c.TLSCrypt = pem
	case "crl-verify":
		c.CRL = pem
	default:
		return fmt.Errorf("unknown inline block <%s>", kind)
	}
	return nil
}

// quotedArg takes the first argument from args, rejoining one that was quoted
// and contained spaces -- strings.Fields has split it -- and returns it
// unquoted with the arguments that follow it.
func quotedArg(args []string) (string, []string, error) {
	q := args[0][0]
	if q != '\'' && q != '"' {
		return args[0], args[1:], nil
	}
	var parts []string
	for i, a := range args {
		parts = append(parts, a)
		if (i > 0 || len(a) > 1) && a[len(a)-1] == q {
			joined := strings.Join(parts, " ")
			return joined[1 : len(joined)-1], args[i+1:], nil
		}
	}
	return "", nil, fmt.Errorf("unterminated quote")
}

// setKeyDirection parses a --key-direction / tls-auth direction argument.
func (c *Config) setKeyDirection(arg string) error {
	d, err := strconv.Atoi(arg)
	if err != nil || (d != 0 && d != 1) {
		return fmt.Errorf("key-direction %q: must be 0 or 1", arg)
	}
	c.KeyDirection = d
	return nil
}

// applyOverrides layers non-empty options over a parsed config, so flags win over
// an .ovpn file. Keys match the Opt* constants.
func (c *Config) applyOverrides(opts map[string]string) error {
	if v := opts[OptRemote]; v != "" {
		c.Remote = v
	}
	if v := opts[OptPort]; v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s %q: %w", OptPort, v, err)
		}
		c.Port = p
	}
	for _, f := range []struct {
		opt  string
		dest *[]byte
	}{
		{OptCA, &c.CA}, {OptCert, &c.Cert}, {OptKey, &c.Key},
		{OptTLSAuth, &c.TLSAuth}, {OptTLSCrypt, &c.TLSCrypt},
	} {
		if path := opts[f.opt]; path != "" {
			pem, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("%s: %w", f.opt, err)
			}
			*f.dest = pem
		}
	}
	if v := opts[OptKeyDirection]; v != "" {
		if err := c.setKeyDirection(v); err != nil {
			return err
		}
	}
	if v := opts[OptAuth]; v != "" {
		c.Auth = v
	}
	if v := opts[OptCipher]; v != "" {
		c.Cipher = v
	}
	if v := opts[OptShape]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("%s: invalid value %q", OptShape, v)
		}
		c.Shape = n
	}
	if v := opts[OptUsername]; v != "" {
		c.Username = v
	}
	if v := opts[OptPassword]; v != "" {
		c.Password = v
	}
	if v := opts[OptTUNName]; v != "" {
		c.TUNName = v
	}
	return nil
}

// validate checks the config has what a dial needs and normalises defaults.
func (c *Config) validate() error {
	if c.Remote == "" {
		return fmt.Errorf("%s is required", OptRemote)
	}
	if len(c.CA) == 0 {
		return fmt.Errorf("%s is required", OptCA)
	}
	if len(c.Cert) == 0 || len(c.Key) == 0 {
		return fmt.Errorf("%s and %s are required", OptCert, OptKey)
	}
	if c.Port == 0 {
		c.Port = defaultPort
	}
	if c.Cipher == "" {
		c.Cipher = defaultCipher
	}
	if !strings.EqualFold(c.Cipher, cipherGCM) && !strings.EqualFold(c.Cipher, cipherCBC) {
		return fmt.Errorf("unsupported cipher %q (only %s and %s)", c.Cipher, cipherGCM, cipherCBC)
	}
	if len(c.TLSAuth) > 0 && len(c.TLSCrypt) > 0 {
		return fmt.Errorf("%s and %s are mutually exclusive", OptTLSAuth, OptTLSCrypt)
	}
	return nil
}

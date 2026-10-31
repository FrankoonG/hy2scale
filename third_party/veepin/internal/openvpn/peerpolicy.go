package openvpn

// Server-certificate checks beyond the chain: the .ovpn directives that pin
// which certificate from a CA is the right one.
//
// A provider commonly signs every node, and sometimes every client, from one
// CA, and tells nodes apart with verify-x509-name. A chain check alone then
// accepts any certificate that CA ever issued. Stock OpenVPN honours these
// directives, so a profile that carries one is relying on it; ignoring it is
// accepting what the profile's author said to refuse.

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// PeerPolicy is what a profile says about the server's certificate, past
// chaining to the CA.
type PeerPolicy struct {
	// X509Name is verify-x509-name's name; empty checks nothing.
	X509Name string
	// X509NameType is "subject" (the default: the full distinguished name),
	// "name" (the common name exactly) or "name-prefix".
	X509NameType string
	// RequireServerUsage is remote-cert-tls server: the certificate must carry
	// the TLS Web Server Authentication extended key usage, and a key usage
	// that permits it.
	RequireServerUsage bool
	// CRL is crl-verify's revocation list, PEM or DER. A server certificate
	// listed in it is refused.
	CRL []byte
}

// check applies p to the server's leaf, which has already chained to the CA
// through issuer (nil when the leaf is itself the anchor).
func (p PeerPolicy) check(leaf, issuer *x509.Certificate) error {
	if p.X509Name != "" {
		if err := p.checkName(leaf); err != nil {
			return err
		}
	}
	if p.RequireServerUsage {
		if err := requireServerUsage(leaf); err != nil {
			return err
		}
	}
	if len(p.CRL) > 0 {
		if err := checkCRL(p.CRL, leaf, issuer); err != nil {
			return err
		}
	}
	return nil
}

func (p PeerPolicy) checkName(leaf *x509.Certificate) error {
	cn := leaf.Subject.CommonName
	switch p.X509NameType {
	case "name":
		if cn == p.X509Name {
			return nil
		}
	case "name-prefix":
		if strings.HasPrefix(cn, p.X509Name) {
			return nil
		}
	case "", "subject":
		if subjectMatches(leaf.Subject, p.X509Name) {
			return nil
		}
	default:
		return fmt.Errorf("verify-x509-name: unknown type %q", p.X509NameType)
	}
	return fmt.Errorf("server certificate %q does not match verify-x509-name %q", leaf.Subject, p.X509Name)
}

// attrNames are the short names OpenVPN prints a subject with.
var attrNames = map[string]string{
	"2.5.4.3":                    "CN",
	"2.5.4.6":                    "C",
	"2.5.4.7":                    "L",
	"2.5.4.8":                    "ST",
	"2.5.4.10":                   "O",
	"2.5.4.11":                   "OU",
	"2.5.4.5":                    "serialNumber",
	"2.5.4.9":                    "street",
	"2.5.4.17":                   "postalCode",
	"2.5.4.42":                   "GN",
	"2.5.4.4":                    "SN",
	"2.5.4.12":                   "title",
	"2.5.4.41":                   "name",
	"0.9.2342.19200300.100.1.25": "DC",
	"1.2.840.113549.1.9.1":       "emailAddress",
}

// subjectMatches compares a subject against the string verify-x509-name was
// given, in either of the forms OpenVPN has printed subjects in: "C=US, O=Acme,
// CN=vpn" or the older "/C=US/O=Acme/CN=vpn". It compares the attributes as a
// set, so the order the profile lists them in does not matter, and it fails
// closed: an attribute it cannot name, or a malformed string, is a mismatch.
func subjectMatches(subject pkix.Name, want string) bool {
	have := map[string]int{}
	for _, atv := range subject.Names {
		name, ok := attrNames[atv.Type.String()]
		if !ok {
			return false
		}
		v, ok := atv.Value.(string)
		if !ok {
			return false
		}
		have[name+"="+v]++
	}
	sep := ","
	if strings.HasPrefix(want, "/") {
		sep = "/"
	}
	n := 0
	for part := range strings.SplitSeq(strings.TrimPrefix(want, "/"), sep) {
		part = strings.TrimSpace(part)
		k, v, ok := strings.Cut(part, "=")
		if !ok || k == "" {
			return false
		}
		key := strings.TrimSpace(k) + "=" + strings.TrimSpace(v)
		if have[key] == 0 {
			return false
		}
		have[key]--
		n++
	}
	return n > 0 && n == len(subject.Names)
}

// requireServerUsage is remote-cert-tls server. Unlike Go's default chain
// check, which treats a certificate with no extended key usage as good for
// anything, the server-auth usage must be present -- that is the whole of what
// stops a client certificate from the same CA standing in for the server.
func requireServerUsage(leaf *x509.Certificate) error {
	hasEKU := false
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			hasEKU = true
		}
	}
	if !hasEKU {
		return errors.New("remote-cert-tls server: the server certificate lacks the TLS Web Server Authentication extended key usage")
	}
	const want = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageKeyAgreement
	if leaf.KeyUsage != 0 && leaf.KeyUsage&want == 0 {
		return errors.New("remote-cert-tls server: the server certificate's key usage does not permit TLS")
	}
	return nil
}

// checkCRL refuses leaf when the list names its serial. The list must be
// signed by the leaf's issuer, so a list for some other CA cannot vouch for
// nothing being revoked here.
func checkCRL(raw []byte, leaf, issuer *x509.Certificate) error {
	der := raw
	if blk, _ := pem.Decode(raw); blk != nil {
		der = blk.Bytes
	}
	crl, err := x509.ParseRevocationList(der)
	if err != nil {
		return fmt.Errorf("crl-verify: %w", err)
	}
	if issuer == nil {
		issuer = leaf
	}
	if err := crl.CheckSignatureFrom(issuer); err != nil {
		return fmt.Errorf("crl-verify: the list is not signed by the server certificate's issuer: %w", err)
	}
	for _, e := range crl.RevokedCertificateEntries {
		if e.SerialNumber != nil && e.SerialNumber.Cmp(leaf.SerialNumber) == 0 {
			return errors.New("crl-verify: the server certificate has been revoked")
		}
	}
	return nil
}

// oidServerAuth is id-kp-serverAuth, for remote-cert-eku's numeric form.
var oidServerAuth = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 1}

// IsServerAuthEKU reports whether a remote-cert-eku argument names server
// authentication, by OID or by either of the names OpenVPN accepts.
func IsServerAuthEKU(arg string) bool {
	switch arg {
	case "TLS Web Server Authentication", "serverAuth":
		return true
	}
	return arg == oidServerAuth.String()
}

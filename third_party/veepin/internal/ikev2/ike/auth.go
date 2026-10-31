package ike

import (
	"crypto/hmac"
	"errors"
	"fmt"
	"net"

	"github.com/xen0bit/veepin/internal/cryptoutil"
	"github.com/xen0bit/veepin/internal/ikev2/payload"
)

// Identity describes a local or peer identity for IKE_AUTH.
type Identity struct {
	Type payload.IDType
	Data []byte
}

// FQDNIdentity builds an FQDN identity.
func FQDNIdentity(name string) Identity {
	return Identity{Type: payload.IDFQDN, Data: []byte(name)}
}

// IPIdentity builds an IPv4/IPv6 identity from an IP.
func IPIdentity(ip net.IP) Identity {
	if v4 := ip.To4(); v4 != nil {
		return Identity{Type: payload.IDIPv4Addr, Data: v4}
	}
	return Identity{Type: payload.IDIPv6Addr, Data: ip.To16()}
}

// idPayloadBody returns the ID payload body: type octet + 3 reserved + data.
// This is exactly what goes on the wire and what prf(SK_p, .) hashes.
func idPayloadBody(id Identity) []byte {
	return payload.MarshalID(payload.IDPayload{Type: id.Type, Data: id.Data})
}

// computePSKAuth returns the AUTH payload value for PSK authentication.
//
// signedOctets = realMessage | peerNonce | prf(SK_p<self>, IDpayloadBody)
// AUTH        = prf(prf(PSK, "Key Pad for IKEv2"), signedOctets)
//
// realMessage is this endpoint's own first IKE_SA_INIT message; peerNonce is
// the other party's nonce; skp is this endpoint's own SK_p (SK_pi if we are the
// initiator, SK_pr if responder).
func computePSKAuth(prf *cryptoutil.PRF, psk, realMessage, peerNonce, skp, idBody, intAuth []byte) []byte {
	octets := AuthOctets(prf, realMessage, peerNonce, skp, idBody, intAuth)
	return PSKAuth(prf, psk, octets)
}

// verifyPeerPSKAuth checks the peer's AUTH payload under PSK.
//
// The peer signs: peerRealMessage | ourNonce | prf(peerSK_p, peerIDbody).
//
// An empty PSK is refused outright rather than computed with. A side configured
// for certificates alone has no PSK, and prf("", ...) is something anyone can
// evaluate: every other input to the AUTH value is either the peer's own
// message or a key it derived from its own Diffie-Hellman. Accepting method 2
// there would let any peer authenticate by naming it.
func verifyPeerPSKAuth(prf *cryptoutil.PRF, psk, peerRealMessage, ourNonce, peerSKp, peerIDBody, intAuth, gotAuth []byte) error {
	if len(psk) == 0 {
		return errNoPSK
	}
	octets := AuthOctets(prf, peerRealMessage, ourNonce, peerSKp, peerIDBody, intAuth)
	want := PSKAuth(prf, psk, octets)
	if !hmac.Equal(want, gotAuth) {
		return fmt.Errorf("ike: PSK authentication failed")
	}
	return nil
}

// errNoPSK is the refusal of PSK authentication by a side that has no PSK.
var errNoPSK = errors.New("ike: PSK authentication offered but no PSK is configured")

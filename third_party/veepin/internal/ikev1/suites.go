package ikev1

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// The suites a responder accepts, PFS in Quick Mode, and the NO-PROPOSAL-CHOSEN
// answer to an offer it cannot take. (hy2scale P8)
//
// Routers rarely let the user choose: iKuai's L2TP client offers exactly one
// phase-1 transform, AES-128/SHA2-256/MODP-3072, and strongSwan's IKEv1
// default ESP leads with AES-GCM. A responder that accepts only its own
// initiator's set silently drops those, and the client times out with no
// reason given.

// IKEv2 transform IDs the internal/ikev2/esp data path takes for the suites
// added here.
const (
	espEncrAESGCM16      = 20 // ENCR_AES_GCM_16
	espAuthHMACSHA384192 = 13 // AUTH_HMAC_SHA2_384_192
	espAuthHMACSHA512256 = 14 // AUTH_HMAC_SHA2_512_256
)

// notifyNoProposalChosen is the ISAKMP error a responder answers an
// unacceptable SA with (RFC 2408 section 3.14.1).
const notifyNoProposalChosen = 14

func aesKeyBitsOK(bits uint16) bool { return bits == 128 || bits == 192 || bits == 256 }

func hashOK(id uint16) bool {
	_, err := hashCtor(id)
	return err == nil
}

func groupOK(id uint16) bool {
	_, err := dhGroup(id)
	return err == nil
}

func espAuthOK(alg uint16) bool {
	switch alg {
	case authHMACSHA, authHMACSHA2256, authHMACSHA2384, authHMACSHA2512:
		return true
	}
	return false
}

// espEncKeyLen is the ESP encryption key length in octets: AES-GCM carries a
// 4-octet salt after the key (RFC 4106 section 8.1).
func espEncKeyLen(p espProposal) int {
	n := int(p.keyBits) / 8
	if p.transformID == espTransformGCM16 {
		n += 4
	}
	return n
}

// integKeyLenFor is the HMAC key length for an ESP integrity transform (the
// IKEv2 ID); 0 for an AEAD suite.
func integKeyLenFor(integID uint16) int {
	switch integID {
	case 0:
		return 0
	case espAuthHMACSHA256128:
		return 32
	case espAuthHMACSHA384192:
		return 48
	case espAuthHMACSHA512256:
		return 64
	}
	return 20 // HMAC-SHA1
}

// buildPhase2SAChosen renders the responder's Quick Mode SA: our inbound SPI
// and the one transform chosen, under the proposal and transform numbers the
// initiator gave it.
func buildPhase2SAChosen(spi uint32, chosen parsedTransform, p espProposal) []byte {
	t := buildTransform(payloadNone, chosen.num, p.transformID, p.attrs())
	prop := buildProposal(payloadNone, chosen.propNum, protoESP, be32(spi), 1, t)
	return append(saPrefix(), prop...)
}

// notifyBody renders a Notification payload body: DOI, protocol, SPI, type.
func notifyBody(proto uint8, spi []byte, typ uint16) []byte {
	body := make([]byte, 8+len(spi))
	binary.BigEndian.PutUint32(body[0:], doiIPsec)
	body[4] = proto
	body[5] = uint8(len(spi))
	binary.BigEndian.PutUint16(body[6:], typ)
	copy(body[8:], spi)
	return body
}

// sendPhase1NoProposal answers a Main Mode SA offer with nothing acceptable in
// it. No keys exist yet, so it is a plaintext Informational exchange naming the
// ISAKMP SA by its cookie pair. Best effort: the session fails either way.
func (s *Session) sendPhase1NoProposal() {
	spi := append(append([]byte(nil), s.initCookie[:]...), s.respCookie[:]...)
	msg := marshalMessage(s.mmHeader(exchangeInformational, 0, randSPI()), []payload{
		{typ: payloadNotify, body: notifyBody(protoISAKMP, spi, notifyNoProposalChosen)},
	})
	_ = s.cfg.Send(msg, s.floated)
}

// sendQuickNoProposal answers a Quick Mode SA offer with nothing acceptable in
// it: a protected Informational exchange naming the initiator's ESP SPI.
func (s *Session) sendQuickNoProposal(spi []byte) {
	if err := s.sendInformational([]payload{
		{typ: payloadNotify, body: notifyBody(protoESP, spi, notifyNoProposalChosen)},
	}); err != nil {
		s.logger.Printf("ikev1: NO-PROPOSAL-CHOSEN: %v", err)
	}
}

// describeOffer summarises an SA offer (each transform's basic attributes) for
// the log line a refusal leaves.
func describeOffer(transforms []parsedTransform) string {
	var b strings.Builder
	for i, t := range transforms {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "#%d id=%d", t.num, t.id)
		for _, a := range t.attrs {
			if v, ok := attrUint16(a); ok && a.basic {
				fmt.Fprintf(&b, " %d=%d", a.typ, v)
			}
		}
	}
	return b.String()
}

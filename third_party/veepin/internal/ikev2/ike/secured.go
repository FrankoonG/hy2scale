package ike

import (
	"net"

	"github.com/xen0bit/veepin/internal/ikev2/payload"
)

// handleSecured decrypts and dispatches a protected exchange (everything after
// IKE_SA_INIT).
func (s *Server) handleSecured(pkt []byte, hdr payload.Header, remote *net.UDPAddr, ex payload.ExchangeType, on4500 bool) {
	if hdr.IsResponse() {
		return // pure responder
	}
	sa := s.lookupByRSPI(hdr.ResponderSPI)
	if sa == nil {
		s.log.Printf("ikev2: %s from %s has no IKE SA (rSPI=%#x)", ex, remote, hdr.ResponderSPI)
		return
	}

	sa.mu.Lock()
	defer sa.mu.Unlock()

	if hdr.MessageID != sa.RecvMsgID {
		s.log.Printf("ikev2: %s from %s bad msgid %d (want %d)", ex, remote, hdr.MessageID, sa.RecvMsgID)
		return
	}

	msg, err := payload.ParseMessage(pkt)
	if err != nil {
		s.log.Printf("ikev2: %s parse from %s: %v", ex, remote, err)
		return
	}
	var firstInner payload.PayloadType
	var inner []byte
	if skfPay := msg.Find(payload.TypeSKF); skfPay != nil {
		// RFC 7383 fragment: decrypt it and hold it until every fragment of this
		// message has arrived. Reassembly proceeds only once, on completion.
		if !sa.fragEnabled {
			s.log.Printf("ikev2: %s from %s sent an SKF fragment without negotiating fragmentation", ex, remote)
			return
		}
		fragNum, total, fi, chunk, derr := decryptSKF(pkt, *skfPay, sa.Suite, sa.Keys, sa.dirForInbound())
		if derr != nil {
			s.log.Warnf("ikev2: %s from %s SKF decrypt failed: %v", ex, remote, derr)
			return
		}
		reasm, rfi, complete, rerr := sa.fragReasm.add(hdr.MessageID, fragNum, total, fi, chunk)
		if rerr != nil {
			s.log.Printf("ikev2: %s from %s fragment reassembly: %v", ex, remote, rerr)
			return
		}
		if !complete {
			return // await the remaining fragments
		}
		inner, firstInner = reasm, rfi
	} else {
		skPay := msg.Find(payload.TypeSK)
		if skPay == nil {
			s.log.Printf("ikev2: %s from %s missing SK payload", ex, remote)
			return
		}
		fi, in, derr := decryptSK(pkt, hdr, *skPay, sa.Suite, sa.Keys, sa.dirForInbound())
		if derr != nil {
			s.log.Warnf("ikev2: %s from %s decrypt failed: %v", ex, remote, derr)
			return
		}
		firstInner, inner = fi, in
	}

	// Only now has the datagram proved it came from the holder of this SA's
	// keys. The SPI, message ID and source address it arrived with are all
	// cleartext, so moving the SA's return address -- or counting it as a sign
	// of life -- any earlier would let a forger with a copy of the SPI steer
	// our responses somewhere else.
	sa.touch()
	if on4500 && !sa.OnPort4500 {
		sa.OnPort4500 = true
	}
	sa.RemoteAddr = remote

	inners, err := parseInnerPayloads(firstInner, inner)
	if err != nil {
		s.log.Printf("ikev2: %s from %s inner parse: %v", ex, remote, err)
		return
	}

	// RFC 7296 §1.2: until IKE_AUTH completes, the only protected exchanges an
	// initiator may run are IKE_AUTH itself and RFC 9242's IKE_INTERMEDIATE.
	// The keys that decrypted this message come from an unauthenticated
	// Diffie-Hellman, so before then they prove only that the sender is the
	// party who ran IKE_SA_INIT -- anyone. A CREATE_CHILD_SA accepted at this
	// point would key a Child SA off SK_d for a peer who never authenticated
	// and put its ESP straight onto the TUN. And once IKE_AUTH has completed,
	// a second one is refused, so it cannot re-run the authentication against
	// an SA that already carries an identity and an address.
	if !exchangeAllowed(sa.State, ex) {
		s.log.Warnf("ikev2: refusing %s from %s in state %s", ex, remote, sa.State)
		return
	}

	switch ex {
	case payload.IKE_AUTH:
		s.handleIKEAuth(sa, hdr, inners, remote)
	case payload.IKE_INTERMEDIATE:
		s.handleIKEIntermediate(sa, hdr, inners, remote)
	case payload.CREATE_CHILD_SA:
		s.handleCreateChildSA(sa, hdr, inners, remote)
	case payload.INFORMATIONAL:
		s.handleInformational(sa, hdr, inners, remote)
	}
}

// respondEncrypted builds and sends an encrypted response for the given
// request message ID, wrapping the supplied inner payload chain. The response
// is sent on whichever port the SA has floated to.
func (s *Server) respondEncrypted(sa *IKESA, ex payload.ExchangeType, msgID uint32,
	firstInner payload.PayloadType, innerChain []byte, remote *net.UDPAddr) {

	hdr := payload.Header{
		InitiatorSPI: sa.InitiatorSPI,
		ResponderSPI: sa.ResponderSPI,
		ExchangeType: ex,
		Flags:        payload.FlagResponse,
		MessageID:    msgID,
	}
	// sa.fragEnabled records whether the initiator advertised RFC 7383 in
	// IKE_SA_INIT. It gates fragmentation for exactly the same reason the
	// client's c.frag does: a peer that did not advertise support will not
	// reassemble SKF payloads, and sending them to one is a dead handshake.
	pkts, err := sealMaybeFragment(hdr, sa.Suite, sa.Keys, sa.dirForOutbound(),
		firstInner, innerChain, sa.fragEnabled)
	if err != nil {
		s.log.Printf("ikev2: build encrypted %s response: %v", ex, err)
		return
	}
	if len(pkts) > 1 {
		s.log.Printf("ikev2: fragmenting %s response (msgid %d) into %d RFC 7383 fragments", ex, msgID, len(pkts))
	}
	for _, pkt := range pkts {
		s.send(pkt, remote, sa.OnPort4500)
	}
}

// respondEncryptedNotify sends an encrypted response carrying a single Notify.
func (s *Server) respondEncryptedNotify(sa *IKESA, ex payload.ExchangeType, msgID uint32,
	nt payload.NotifyType, remote *net.UDPAddr) {

	b := payload.NewBuilder()
	b.Add(payload.TypeNotify, false, payload.MarshalNotify(payload.NotifyPayload{
		Protocol: payload.ProtoNone, Type: nt,
	}))
	s.respondEncrypted(sa, ex, msgID, b.FirstType(), b.Bytes(), remote)
}

// exchangeAllowed reports whether a protected exchange of type ex may be
// processed on an IKE SA in state st. It is the whole of the responder's
// exchange-ordering policy, so it lives in one place rather than at the top of
// each handler, where a new handler could forget it.
func exchangeAllowed(st SAState, ex payload.ExchangeType) bool {
	switch ex {
	case payload.IKE_AUTH:
		return st == StateSAInitDone || st == StateIntermediate
	case payload.IKE_INTERMEDIATE:
		return st == StateSAInitDone || st == StateIntermediate
	case payload.CREATE_CHILD_SA:
		return st == StateEstablished
	case payload.INFORMATIONAL:
		// StateDeleting is an SA that was established and then rekeyed: the
		// peer's INFORMATIONAL{D(IKE)} for it arrives under its keys.
		return st == StateEstablished || st == StateDeleting
	}
	return false
}

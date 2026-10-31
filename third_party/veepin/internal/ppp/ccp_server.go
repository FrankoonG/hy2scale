package ppp

import (
	"encoding/binary"
	"fmt"

	"github.com/xen0bit/veepin/internal/mschap"
)

// CCP (RFC 1962) for the server session, offering only MPPE. (hy2scale P10)
//
// The server answers CCP rather than starting it: a client that wants MPPE
// sends its Configure-Request after authentication, and only then does the
// server send its own, asking for MPPE exactly when the client did and in the
// client's mode (stateless or stateful). Anything else a client offers
// (Deflate, BSD-Compress, MPPC without encryption) is rejected, which leaves a
// client that does not require MPPE on a plain link, as before.
//
// Each Configure-Request names what its sender will receive, so the client's
// acknowledged request sets how the server encrypts and the server's
// acknowledged request how the client does.

const (
	ccpOptMPPE      = 18
	mppeBitH        = 0x01000000 // stateless
	mppeBitS        = 0x00000040 // 128-bit keys
	ccpResetRequest = 14
	ccpResetAck     = 15

	codeProtocolReject = 8 // LCP (RFC 1661 section 5.7)
)

// ccpServer is the server session's CCP state, guarded by the session mutex.
// The two MPPE directions it produces live on the session as atomics, since
// the data path uses them without that lock.
type ccpServer struct {
	keys             bool // MS-CHAPv2 succeeded, so MPPE keys exist
	sendKey, recvKey [16]byte

	started               bool   // our Configure-Request has gone out
	reqBits               uint32 // MPPE bits in it; 0 for an empty request
	reqID                 byte
	req                   []byte
	localOpen, remoteOpen bool
	naks                  int
	restart               restartTimer
}

// maxCCPNaks bounds how often the server follows a Configure-Nak before it
// asks for nothing (RFC 1661 Max-Failure).
const maxCCPNaks = 5

// setMPPEKeys records the MPPE start keys a completed MS-CHAPv2 exchange gives.
func (s *ServerSession) setMPPEKeys(password string, nt [mschap.NTResponseLen]byte) {
	s.ccp.sendKey, s.ccp.recvKey = mschap.ServerMPPEKeys(password, nt)
	s.ccp.keys = true
}

func (s *ServerSession) handleCCP(payload []byte) {
	if s.phase != phaseIPCP && s.phase != phaseUp {
		return // CCP belongs to the network phase
	}
	pkt, ok := parseCP(payload)
	if !ok {
		return
	}
	c := &s.ccp
	switch pkt.Code {
	case codeConfigureRequest:
		s.handleCCPConfigReq(pkt)
	case codeConfigureAck:
		if pkt.ID == c.reqID && c.started {
			c.restart.stop()
			c.localOpen = true
			if c.reqBits != 0 {
				s.mppeRx.Store(newMPPE(c.recvKey, c.reqBits&mppeBitH != 0))
			} else {
				s.mppeRx.Store(nil)
			}
			s.maybeCCPUp()
		}
	case codeConfigureNak:
		if pkt.ID != c.reqID || !c.started {
			return
		}
		c.restart.alive()
		c.reqBits = 0
		// A peer that keeps Nak'ing settles for no compression this way.
		if c.naks++; c.naks > maxCCPNaks {
			s.sendCCPConfigReq()
			return
		}
		if opts, ok := parseOptions(pkt.Body); ok {
			for _, o := range opts {
				if o.Type == ccpOptMPPE && len(o.Value) == 4 {
					if bits := binary.BigEndian.Uint32(o.Value); bits&mppeBitS != 0 {
						c.reqBits = mppeBitS | bits&mppeBitH
					}
				}
			}
		}
		s.sendCCPConfigReq()
	case codeConfigureReject:
		if pkt.ID != c.reqID || !c.started {
			return
		}
		c.restart.alive()
		c.reqBits = 0
		s.sendCCPConfigReq()
	case codeTerminateRequest:
		s.send(ProtocolCCP, cpPacket{Code: codeTerminateAck, ID: pkt.ID}.marshal())
		c.restart.stop()
		c.localOpen, c.remoteOpen, c.started = false, false, false
		s.mppeTx.Store(nil)
		s.mppeRx.Store(nil)
	case ccpResetRequest:
		// Stateful MPPE: the client lost sync. Rekey on the next packet and
		// flag it; the Ack is what RFC 1962 asks for.
		if tx := s.mppeTx.Load(); tx != nil {
			tx.flush()
		}
		s.send(ProtocolCCP, cpPacket{Code: ccpResetAck, ID: pkt.ID}.marshal())
	}
}

func (s *ServerSession) handleCCPConfigReq(pkt cpPacket) {
	opts, ok := parseOptions(pkt.Body)
	if !ok {
		return
	}
	c := &s.ccp
	var rej, nak []option
	var bits uint32
	mppe := false
	for _, o := range opts {
		if o.Type != ccpOptMPPE || !c.keys || len(o.Value) != 4 {
			rej = append(rej, o)
			continue
		}
		offered := binary.BigEndian.Uint32(o.Value)
		if offered&mppeBitS == 0 {
			rej = append(rej, o) // 40- and 56-bit keys, or MPPC alone: not offered
			continue
		}
		mppe = true
		bits = mppeBitS | offered&mppeBitH
		if offered != bits {
			nak = append(nak, option{Type: ccpOptMPPE, Value: binary.BigEndian.AppendUint32(nil, bits)})
		}
	}
	switch {
	case len(rej) > 0:
		s.send(ProtocolCCP, cpPacket{Code: codeConfigureReject, ID: pkt.ID, Body: marshalOptions(rej)}.marshal())
	case len(nak) > 0:
		s.send(ProtocolCCP, cpPacket{Code: codeConfigureNak, ID: pkt.ID, Body: marshalOptions(nak)}.marshal())
	default:
		s.send(ProtocolCCP, cpPacket{Code: codeConfigureAck, ID: pkt.ID, Body: pkt.Body}.marshal())
		c.remoteOpen = true
		if mppe {
			s.mppeTx.Store(newMPPE(c.sendKey, bits&mppeBitH != 0))
		} else {
			s.mppeTx.Store(nil)
		}
	}
	if !c.started {
		// Ask for MPPE exactly when the client offered it, in its mode.
		c.started = true
		if mppe {
			c.reqBits = bits
		}
		s.sendCCPConfigReq()
	}
	s.maybeCCPUp()
}

func (s *ServerSession) sendCCPConfigReq() {
	c := &s.ccp
	var opts []option
	if c.reqBits != 0 {
		opts = append(opts, option{Type: ccpOptMPPE, Value: binary.BigEndian.AppendUint32(nil, c.reqBits)})
	}
	c.reqID = s.nextID()
	c.localOpen = false
	c.req = cpPacket{Code: codeConfigureRequest, ID: c.reqID, Body: marshalOptions(opts)}.marshal()
	s.resendCCPConfigReq()
}

func (s *ServerSession) resendCCPConfigReq() {
	s.send(ProtocolCCP, s.ccp.req)
	// An unanswered CCP gives up quietly: the link can run without it, and a
	// client that requires MPPE closes it itself.
	s.ccp.restart.arm(s.withLock, s.resendCCPConfigReq, func() {})
}

// maybeCCPUp runs once both directions are agreed. A client requiring MPPE
// holds IPCP until CCP is up and ignored the IPCP request sent before then,
// so it is repeated now rather than on the next Restart tick.
func (s *ServerSession) maybeCCPUp() {
	if !s.ccp.localOpen || !s.ccp.remoteOpen {
		return
	}
	if s.phase == phaseIPCP && !s.ipcpLocalOpen && s.ipcpConfigReq != nil {
		s.ipcpRestart.stop()
		s.resendIPCPConfigReq()
	}
}

// protocolReject answers a frame of a protocol this session does not run, as
// RFC 1661 section 5.7 requires once LCP is open. Silently dropping it leaves
// the peer waiting out its own timers (IPV6CP, BACP, ...).
func (s *ServerSession) protocolReject(protocol uint16, payload []byte) {
	if s.phase == phaseLCP {
		return
	}
	if max := defaultMRU - 6; len(payload) > max {
		payload = payload[:max]
	}
	body := binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(payload)), protocol)
	body = append(body, payload...)
	s.send(ProtocolLCP, cpPacket{Code: codeProtocolReject, ID: s.nextID(), Body: body}.marshal())
}

// Inbound prepares a received frame for the data path: a compressed datagram
// is decrypted into the frame it carried, and with MPPE on, a cleartext
// network-layer frame is refused. ok is false for a frame to drop.
func (s *ServerSession) Inbound(frame []byte) (out []byte, ok bool) {
	protocol, payload, valid := decodeFrame(frame)
	if !valid {
		return frame, true
	}
	rx := s.mppeRx.Load()
	switch {
	case protocol == ProtocolCompressed:
		if rx == nil {
			return nil, false
		}
		plain, reset, ok := rx.decrypt(payload)
		if reset {
			id := byte(s.resetID.Add(1))
			_ = s.tr.SendPPP(encodeFrame(ProtocolCCP, cpPacket{Code: ccpResetRequest, ID: id}.marshal()))
		}
		if !ok {
			return nil, false
		}
		return append([]byte{hdlcAddress, hdlcControl}, plain...), true
	case rx != nil && protocol >= 0x0021 && protocol <= 0x00fa:
		return nil, false
	}
	return frame, true
}

// Outbound encrypts a network-layer frame once MPPE is on; other frames pass.
func (s *ServerSession) Outbound(frame []byte) []byte {
	tx := s.mppeTx.Load()
	if tx == nil {
		return frame
	}
	protocol, payload, ok := decodeFrame(frame)
	if !ok || protocol < 0x0021 || protocol > 0x00fa {
		return frame
	}
	// MPPE encrypts the protocol field with the information.
	if len(frame) >= frameHeaderLen && frame[0] == hdlcAddress && frame[1] == hdlcControl && frame[2]&1 == 0 {
		return tx.encrypt(frame[2:])
	}
	plain := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(plain, protocol)
	copy(plain[2:], payload)
	return tx.encrypt(plain)
}

// MPPEMode describes the encryption in effect each way, for logs.
func (s *ServerSession) MPPEMode() string {
	mode := func(m *mppe) string {
		if m == nil {
			return "off"
		}
		if m.stateless {
			return "128-bit stateless"
		}
		return "128-bit stateful"
	}
	return fmt.Sprintf("tx %s, rx %s", mode(s.mppeTx.Load()), mode(s.mppeRx.Load()))
}

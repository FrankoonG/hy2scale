package ppp

// MPPE (RFC 3078) with 128-bit keys, the encryption pppd-based L2TP clients
// insist on: iKuai's client always runs "mppe required,stateless", and pppd
// fails the link with "MPPE required but peer negotiation failed" when the
// server never answers CCP. (hy2scale P10)
//
// The cipher follows the Linux ppp_mppe driver, which is what the client end
// runs: RC4 keyed from the MS-CHAPv2 start key, a 12-bit coherency count, and
// a rekey before every packet in stateless mode (every 256 packets, or after a
// CCP Reset-Request, in stateful mode). The keys come from RFC 3079.

import (
	"bytes"
	"crypto/rc4"  //nolint:gosec // RC4 is what MPPE is defined over
	"crypto/sha1" //nolint:gosec // as is SHA-1 for its key changes
	"encoding/binary"
	"sync"
)

// PPP protocol numbers for CCP and the datagrams it compresses (encrypts).
const (
	ProtocolCCP        = 0x80fd
	ProtocolCompressed = 0x00fd
)

// The MPPE header that opens a compressed datagram: flag bits over a 12-bit
// coherency count (RFC 3078 section 3).
const (
	mppeFlushed    = 0x8000 // A: the sender rekeyed (stateless: always)
	mppeEncrypted  = 0x1000 // D
	mppeCountMask  = 0x0fff
	mppeCountSpace = 4096
)

var (
	mppeSHAPad1 = make([]byte, 40)
	mppeSHAPad2 = bytes.Repeat([]byte{0xf2}, 40)
)

// mppe is one direction of an MPPE link.
type mppe struct {
	mu        sync.Mutex
	master    [16]byte // the start key from RFC 3079
	session   [16]byte
	rc4       *rc4.Cipher
	ccount    uint16
	stateless bool
	flushNext bool // tx, stateful: a Reset-Request asked for a rekey
	discard   bool // rx, stateful: lost sync, waiting for a flushed packet
}

func newMPPE(key [16]byte, stateless bool) *mppe {
	m := &mppe{master: key, session: key, stateless: stateless, ccount: mppeCountSpace - 1}
	m.rekey(true)
	return m
}

// rekey is GetNewKeyFromSHA, then (except for the initial key) the RC4
// encryption of the interim key with itself (RFC 3078 section 7.3).
func (m *mppe) rekey(initial bool) {
	h := sha1.New()
	h.Write(m.master[:])
	h.Write(mppeSHAPad1)
	h.Write(m.session[:])
	h.Write(mppeSHAPad2)
	interim := h.Sum(nil)[:16]
	if initial {
		copy(m.session[:], interim)
	} else {
		c, _ := rc4.NewCipher(interim)
		c.XORKeyStream(m.session[:], interim)
	}
	m.rc4, _ = rc4.NewCipher(m.session[:])
}

// encrypt turns plain — a PPP protocol number and its information — into a
// complete compressed-datagram frame.
func (m *mppe) encrypt(plain []byte) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ccount = (m.ccount + 1) % mppeCountSpace
	hdr := m.ccount | mppeEncrypted
	if m.stateless || m.ccount&0xff == 0xff || m.flushNext {
		m.flushNext = false
		m.rekey(false)
		hdr |= mppeFlushed
	}
	out := make([]byte, frameHeaderLen+2+len(plain))
	putFrameHeader(out, ProtocolCompressed)
	binary.BigEndian.PutUint16(out[frameHeaderLen:], hdr)
	m.rc4.XORKeyStream(out[frameHeaderLen+2:], plain)
	return out
}

// flush makes the next packet rekey and say so, answering a Reset-Request.
func (m *mppe) flush() {
	m.mu.Lock()
	m.flushNext = true
	m.mu.Unlock()
}

// decrypt opens a compressed datagram's information field (the MPPE header
// and ciphertext). reset reports that a stateful link just lost sync, so the
// caller should send a CCP Reset-Request.
func (m *mppe) decrypt(data []byte) (plain []byte, reset, ok bool) {
	if len(data) < 3 {
		return nil, false, false
	}
	hdr := binary.BigEndian.Uint16(data)
	if hdr&mppeEncrypted == 0 {
		return nil, false, false
	}
	ccount := hdr & mppeCountMask
	flushed := hdr&mppeFlushed != 0

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateless {
		// Every packet is rekeyed, so a lost one costs nothing: catch up by
		// as many key changes as the count moved. A packet from the past (or
		// a repeat) cannot be decrypted any more.
		ahead := (ccount + mppeCountSpace - m.ccount) % mppeCountSpace
		if !flushed || ahead == 0 || ahead > mppeCountSpace/2 {
			return nil, false, false
		}
		for m.ccount != ccount {
			m.rekey(false)
			m.ccount = (m.ccount + 1) % mppeCountSpace
		}
	} else {
		if !m.discard {
			m.ccount = (m.ccount + 1) % mppeCountSpace
			if ccount != m.ccount {
				m.discard = true
				return nil, true, false
			}
		} else {
			if !flushed {
				return nil, false, false
			}
			// One key change for every flag packet missed, then resync.
			for ccount&^0xff != m.ccount&^0xff {
				m.rekey(false)
				m.ccount = (m.ccount + 256) % mppeCountSpace
			}
			m.discard = false
			m.ccount = ccount
		}
		if flushed {
			m.rekey(false)
		}
	}
	plain = make([]byte, len(data)-2)
	m.rc4.XORKeyStream(plain, data[2:])
	return plain, false, true
}

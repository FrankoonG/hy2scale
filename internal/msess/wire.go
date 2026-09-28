// Package msess is the end-to-end multi-path session layer used by the
// quality and aggregate exit modes: an ingress↔exit byte stream carried over
// any number of relay paths ("subflows") that survives paths dying,
// degrading or being replaced, without losing, duplicating or reordering a
// byte. See docs/v1.3.10-session-design.md.
package msess

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

const version = 1

// HELLO kinds.
const (
	kindOpen       = 1 // new session: exit dials target
	kindJoin       = 2 // attach another subflow to an existing session
	kindProbe      = 3 // path probe: PING/PONG only, no session
	kindOpenPacket = 4 // new datagram session: exit opens a UDP socket to target
)

// HELLO_ACK status.
const (
	stOK         = 0
	stUnknown    = 1 // join for a session the exit doesn't have
	stDialFailed = 2 // exit could not reach the target
	stRefused    = 3 // exit at capacity / session exists
	stVersion    = 4
)

var helloMagic = [3]byte{'H', 'M', 'S'}

type hello struct {
	kind   byte
	sid    [16]byte
	sub    uint32
	rxNext uint64 // sender's delivered offset (acts as an ACK)
	mode   Mode
	grace  uint16 // open: seconds the session may live without any path (0 = default)
	target string
}

func writeHello(w io.Writer, h hello) error {
	b, err := encodeHello(h)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func encodeHello(h hello) ([]byte, error) {
	if len(h.target) > 0xFFFF {
		return nil, errors.New("msess: target too long")
	}
	b := make([]byte, 0, 40+len(h.target))
	b = append(b, helloMagic[:]...)
	b = append(b, version, h.kind)
	b = append(b, h.sid[:]...)
	b = binary.BigEndian.AppendUint32(b, h.sub)
	b = binary.BigEndian.AppendUint64(b, h.rxNext)
	b = append(b, byte(h.mode))
	b = binary.BigEndian.AppendUint16(b, h.grace)
	b = binary.BigEndian.AppendUint16(b, uint16(len(h.target)))
	b = append(b, h.target...)
	return b, nil
}

func readHello(r io.Reader) (hello, error) {
	var h hello
	var b [38]byte // magic 3, ver 1, kind 1, sid 16, sub 4, rxNext 8, mode 1, grace 2, tlen 2
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return h, err
	}
	if [3]byte(b[:3]) != helloMagic {
		return h, errors.New("msess: bad hello magic")
	}
	if b[3] != version {
		return h, fmt.Errorf("msess: hello version %d", b[3])
	}
	h.kind = b[4]
	copy(h.sid[:], b[5:21])
	h.sub = binary.BigEndian.Uint32(b[21:25])
	h.rxNext = binary.BigEndian.Uint64(b[25:33])
	h.mode = Mode(b[33])
	h.grace = binary.BigEndian.Uint16(b[34:36])
	if tl := int(binary.BigEndian.Uint16(b[36:38])); tl > 0 {
		t := make([]byte, tl)
		if _, err := io.ReadFull(r, t); err != nil {
			return h, err
		}
		h.target = string(t)
	}
	return h, nil
}

type helloAck struct {
	status byte
	rxNext uint64
	msg    string
}

func writeHelloAck(w io.Writer, a helloAck) error {
	if len(a.msg) > 1024 {
		a.msg = a.msg[:1024]
	}
	b := make([]byte, 0, 16+len(a.msg))
	b = append(b, helloMagic[:]...)
	b = append(b, version, a.status)
	b = binary.BigEndian.AppendUint64(b, a.rxNext)
	b = binary.BigEndian.AppendUint16(b, uint16(len(a.msg)))
	b = append(b, a.msg...)
	_, err := w.Write(b)
	return err
}

func readHelloAck(r io.Reader) (helloAck, error) {
	var a helloAck
	var b [15]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return a, err
	}
	if [3]byte(b[:3]) != helloMagic || b[3] != version {
		return a, errors.New("msess: bad hello ack")
	}
	a.status = b[4]
	a.rxNext = binary.BigEndian.Uint64(b[5:13])
	ml := int(binary.BigEndian.Uint16(b[13:15]))
	if ml > 1024 {
		return a, errors.New("msess: bad hello ack msg")
	}
	if ml > 0 {
		m := make([]byte, ml)
		if _, err := io.ReadFull(r, m); err != nil {
			return a, err
		}
		a.msg = string(m)
	}
	return a, nil
}

// Frames: magic(1) type(1) fseq(4) len(2) payload.
const (
	frameMagic = 0xA5
	hdrLen     = 8
)

const (
	fData  = 1 // off(8) crc32c(4) bytes
	fAck   = 2 // next(8) flags(1)
	fPing  = 3 // id(4) t(8)
	fPong  = 4 // id(4) t(8)
	fFin   = 5 // off(8)
	fRst   = 6 // msg
	fRole  = 7 // active(1)
	fDgram = 8 // crc32c(4) datagram (packet sessions; unreliable, no ACK)
)

const ackFinDelivered = 0x01 // ACK flag: your FIN was delivered to my consumer

type frame struct {
	typ     byte
	fseq    uint32
	payload []byte
}

var errBadFrame = errors.New("msess: malformed frame")

func appendHdr(b []byte, typ byte, fseq uint32, n int) []byte {
	b = append(b, frameMagic, typ)
	b = binary.BigEndian.AppendUint32(b, fseq)
	return binary.BigEndian.AppendUint16(b, uint16(n))
}

// readFrame reads one frame; payload aliases buf when it fits.
func readFrame(r *bufio.Reader, buf []byte) (frame, error) {
	var h [hdrLen]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return frame{}, err
	}
	if h[0] != frameMagic || h[1] < fData || h[1] > fDgram {
		return frame{}, errBadFrame
	}
	n := int(binary.BigEndian.Uint16(h[6:8]))
	var p []byte
	if n <= cap(buf) {
		p = buf[:n]
	} else {
		p = make([]byte, n)
	}
	if _, err := io.ReadFull(r, p); err != nil {
		return frame{}, err
	}
	return frame{typ: h[1], fseq: binary.BigEndian.Uint32(h[2:6]), payload: p}, nil
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// dataCRC covers the offset and payload of a DATA frame: an intermediate
// hop that splices or damages the stream must never deliver wrong bytes.
func dataCRC(off uint64, data []byte) uint32 {
	var o [8]byte
	binary.BigEndian.PutUint64(o[:], off)
	return crc32.Update(crc32.Update(0, castagnoli, o[:]), castagnoli, data)
}

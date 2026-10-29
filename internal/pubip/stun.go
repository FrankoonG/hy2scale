package pubip

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"
)

const (
	stunMagicCookie      = 0x2112A442
	stunHeaderLen        = 20
	stunBindingRequest   = 0x0001
	stunBindingSuccess   = 0x0101
	stunBindingError     = 0x0111
	stunAttrMapped       = 0x0001
	stunAttrXorMapped    = 0x0020
	stunAttrXorMappedOld = 0x8020 // pre-RFC 5389 servers (draft value)
	stunDefaultPort      = 3478
)

// parseSTUNTarget splits "host:port" / "host" / "[v6]:port" from a stun: entry.
func parseSTUNTarget(s string) (host string, port uint16, err error) {
	if s == "" {
		return "", 0, errors.New("empty stun target")
	}
	h, ps, err := net.SplitHostPort(s)
	if err != nil {
		// No port given: "host" or bare IPv6 literal / "[v6]".
		h = s
		if len(h) > 1 && h[0] == '[' && h[len(h)-1] == ']' {
			h = h[1 : len(h)-1]
		}
		return h, stunDefaultPort, nil
	}
	n, err := strconv.ParseUint(ps, 10, 16)
	if err != nil || n == 0 {
		return "", 0, fmt.Errorf("bad stun port %q", ps)
	}
	if h == "" {
		return "", 0, errors.New("empty stun host")
	}
	return h, uint16(n), nil
}

func (p *prober) probeSTUN(ctx context.Context, target string) (netip.Addr, error) {
	host, port, err := parseSTUNTarget(target)
	if err != nil {
		return netip.Addr{}, err
	}
	addrs, err := p.lookup(ctx, host)
	if err != nil {
		return netip.Addr{}, err
	}
	var lastErr error
	for _, a := range addrs {
		ip, err := p.stunExchange(ctx, netip.AddrPortFrom(a, port))
		if err == nil {
			return ip, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return netip.Addr{}, lastErr
}

func (p *prober) stunExchange(ctx context.Context, server netip.AddrPort) (netip.Addr, error) {
	conn, err := p.dial(ctx, "udp"+p.suffix(), server.String())
	if err != nil {
		return netip.Addr{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	var tid [12]byte
	if _, err := rand.Read(tid[:]); err != nil {
		return netip.Addr{}, err
	}
	req := make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(req[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(req[2:4], 0)
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	copy(req[8:20], tid[:])

	deadline, hasDeadline := ctx.Deadline()
	buf := make([]byte, 1500)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := conn.Write(req); err != nil {
			return netip.Addr{}, ctxErrOr(ctx, err)
		}
		wait := time.Now().Add(stunRetransmit)
		if attempt == 1 || (hasDeadline && deadline.Before(wait)) {
			// Last attempt: wait for whatever the ctx allows.
			if hasDeadline {
				wait = deadline
			} else {
				wait = time.Now().Add(2 * stunRetransmit)
			}
		}
		if ctx.Err() != nil {
			return netip.Addr{}, ctx.Err()
		}
		conn.SetReadDeadline(wait)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() && ctx.Err() == nil {
					lastErr = errors.New("stun: no response")
					break // retransmit (or give up after the last attempt)
				}
				return netip.Addr{}, ctxErrOr(ctx, err)
			}
			ip, perr := parseSTUNResponse(buf[:n], tid)
			if perr == errSTUNIgnore {
				continue // stray packet / other transaction: keep reading
			}
			if perr != nil {
				return netip.Addr{}, perr
			}
			if verr := p.validAnswer(ip); verr != nil {
				return netip.Addr{}, verr
			}
			return ip.Unmap(), nil
		}
	}
	return netip.Addr{}, lastErr
}

func ctxErrOr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

var errSTUNIgnore = errors.New("stun: not our response")

// parseSTUNResponse validates a Binding response for transaction tid and
// returns the mapped address (XOR-MAPPED-ADDRESS preferred, MAPPED-ADDRESS as
// fallback). errSTUNIgnore means the packet is a well-formed STUN message for
// another transaction (or not a response) and should be skipped; anything
// that is not STUN at all fails the server immediately, since a connected
// socket only receives from that server.
func parseSTUNResponse(b []byte, tid [12]byte) (netip.Addr, error) {
	if len(b) < stunHeaderLen || b[0]&0xC0 != 0 ||
		binary.BigEndian.Uint32(b[4:8]) != stunMagicCookie {
		return netip.Addr{}, errors.New("stun: response is not a STUN message")
	}
	typ := binary.BigEndian.Uint16(b[0:2])
	mlen := int(binary.BigEndian.Uint16(b[2:4]))
	if !bytes.Equal(b[8:20], tid[:]) {
		return netip.Addr{}, errSTUNIgnore
	}
	if mlen%4 != 0 || stunHeaderLen+mlen > len(b) {
		return netip.Addr{}, errors.New("stun: malformed message length")
	}
	switch typ {
	case stunBindingSuccess:
	case stunBindingError:
		return netip.Addr{}, errors.New("stun: binding error response")
	default:
		return netip.Addr{}, errSTUNIgnore
	}
	attrs := b[stunHeaderLen : stunHeaderLen+mlen]
	var mapped, xorMapped netip.Addr
	for len(attrs) >= 4 {
		at := binary.BigEndian.Uint16(attrs[0:2])
		al := int(binary.BigEndian.Uint16(attrs[2:4]))
		if 4+al > len(attrs) {
			return netip.Addr{}, errors.New("stun: truncated attribute")
		}
		val := attrs[4 : 4+al]
		switch at {
		case stunAttrXorMapped, stunAttrXorMappedOld:
			if !xorMapped.IsValid() {
				if a, ok := decodeSTUNAddr(val, true, b[4:20]); ok {
					xorMapped = a
				}
			}
		case stunAttrMapped:
			if !mapped.IsValid() {
				if a, ok := decodeSTUNAddr(val, false, nil); ok {
					mapped = a
				}
			}
		}
		pad := (al + 3) &^ 3
		if 4+pad > len(attrs) {
			break
		}
		attrs = attrs[4+pad:]
	}
	if xorMapped.IsValid() {
		return xorMapped, nil
	}
	if mapped.IsValid() {
		return mapped, nil
	}
	return netip.Addr{}, errors.New("stun: no mapped address in response")
}

// decodeSTUNAddr decodes a (XOR-)MAPPED-ADDRESS value. key is the 16-byte
// magic cookie + transaction id used for XOR decoding.
func decodeSTUNAddr(v []byte, xor bool, key []byte) (netip.Addr, bool) {
	if len(v) < 4 {
		return netip.Addr{}, false
	}
	var ip []byte
	switch v[1] {
	case 0x01:
		if len(v) < 8 {
			return netip.Addr{}, false
		}
		ip = append([]byte(nil), v[4:8]...)
	case 0x02:
		if len(v) < 20 {
			return netip.Addr{}, false
		}
		ip = append([]byte(nil), v[4:20]...)
	default:
		return netip.Addr{}, false
	}
	if xor {
		for i := range ip {
			ip[i] ^= key[i]
		}
	}
	a, ok := netip.AddrFromSlice(ip)
	return a, ok
}

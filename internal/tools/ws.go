package tools

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A minimal RFC 6455 server side (no extensions, no subprotocols): enough
// for the terminal.

// WebSocket opcodes.
const (
	OpText   = 1
	OpBinary = 2
	OpClose  = 8
	OpPing   = 9
	OpPong   = 10
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsMaxMessage bounds a client message.
const wsMaxMessage = 1 << 20

// WSConn is an accepted WebSocket connection.
type WSConn struct {
	c   net.Conn
	br  *bufio.Reader
	wmu sync.Mutex
}

// IsWebSocketUpgrade: r asks to switch to WebSocket.
func IsWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && headerToken(r.Header, "Connection", "upgrade")
}

func headerToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// SameOrigin: r carries no Origin (not a browser) or one naming the host
// the request was sent to. A cross-site page must not reach the terminal
// on the strength of the session cookie.
func SameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	fh := r.Header.Get("X-Forwarded-Host")
	return fh != "" && strings.EqualFold(u.Host, strings.TrimSpace(strings.Split(fh, ",")[0]))
}

// AcceptWS completes the opening handshake (it answers the error itself).
func AcceptWS(w http.ResponseWriter, r *http.Request) (*WSConn, error) {
	if r.Method != http.MethodGet || !IsWebSocketUpgrade(r) {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return nil, errors.New("not a websocket upgrade")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "unsupported websocket version", http.StatusUpgradeRequired)
		return nil, errors.New("websocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("no key")
	}
	if !SameOrigin(r) {
		http.Error(w, "cross-origin websocket refused", http.StatusForbidden)
		return nil, errors.New("cross origin")
	}
	c, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "websocket not supported here", http.StatusInternalServerError)
		return nil, err
	}
	c.SetDeadline(time.Time{})
	sum := sha1.Sum([]byte(key + wsGUID))
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err == nil {
		err = rw.Flush()
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return &WSConn{c: c, br: rw.Reader}, nil
}

// ReadMessage returns the next text or binary message; pings are answered
// and a close frame is echoed (io.EOF).
func (ws *WSConn) ReadMessage() (op byte, data []byte, err error) {
	var msgOp byte
	var msg []byte
	for {
		fin, fop, payload, err := ws.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch fop {
		case OpPing:
			if err := ws.WriteMessage(OpPong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case OpPong:
			continue
		case OpClose:
			code := []byte{0x03, 0xe8} // 1000
			if len(payload) >= 2 {
				code = payload[:2]
			}
			ws.WriteMessage(OpClose, code)
			return 0, nil, io.EOF
		case 0: // continuation
			if msgOp == 0 {
				return 0, nil, errors.New("websocket: unexpected continuation")
			}
		case OpText, OpBinary:
			if msgOp != 0 {
				return 0, nil, errors.New("websocket: interleaved message")
			}
			msgOp = fop
		default:
			return 0, nil, fmt.Errorf("websocket: opcode %d", fop)
		}
		if len(msg)+len(payload) > wsMaxMessage {
			return 0, nil, errors.New("websocket: message too large")
		}
		msg = append(msg, payload...)
		if fin {
			return msgOp, msg, nil
		}
	}
}

func (ws *WSConn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(ws.br, h[:]); err != nil {
		return
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0f
	if h[0]&0x70 != 0 {
		return false, 0, nil, errors.New("websocket: reserved bits set")
	}
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(ws.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(ws.br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if op >= 8 && (n > 125 || !fin) {
		return false, 0, nil, errors.New("websocket: bad control frame")
	}
	if !masked {
		return false, 0, nil, errors.New("websocket: client frame not masked")
	}
	if n > wsMaxMessage {
		return false, 0, nil, errors.New("websocket: frame too large")
	}
	var mask [4]byte
	if _, err = io.ReadFull(ws.br, mask[:]); err != nil {
		return
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(ws.br, payload); err != nil {
		return
	}
	for i := range payload {
		payload[i] ^= mask[i&3]
	}
	return
}

// WriteMessage sends one unfragmented frame; safe for concurrent use.
func (ws *WSConn) WriteMessage(op byte, data []byte) error {
	h := make([]byte, 0, 10+len(data))
	h = append(h, 0x80|op)
	switch n := len(data); {
	case n < 126:
		h = append(h, byte(n))
	case n <= 0xffff:
		h = append(h, 126, byte(n>>8), byte(n))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	h = append(h, data...)
	ws.wmu.Lock()
	defer ws.wmu.Unlock()
	_, err := ws.c.Write(h)
	return err
}

// CloseWith sends a close frame with code, then closes the connection.
func (ws *WSConn) CloseWith(code int) error {
	ws.WriteMessage(OpClose, []byte{byte(code >> 8), byte(code)})
	return ws.c.Close()
}

// Close closes the connection.
func (ws *WSConn) Close() error { return ws.c.Close() }

package api

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
)

// Remote access, exit side: a node that can be managed from another node
// (relay trust or a sign-in) dials any address for it. The managing node
// asks over the management stream:
//
//	GET <base>/api/remote-access/dial?target=host:port&proto=tcp|udp
//	Connection: Upgrade
//	Upgrade: hy2scale-dial
//
// The target is dialled first (failure: 502 with the reason); then 101 and
// the stream carries the flow: TCP as is, UDP as datagrams each preceded by
// its length (2 bytes, big endian). The request has passed authMiddleware,
// so whoever asks may manage this node anyway.

const raUpgrade = "hy2scale-dial"

// raMaxDatagram: the largest datagram carried (the length prefix is 16-bit).
const raMaxDatagram = 65535

func (s *Server) remoteAccessDialRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/remote-access/dial", s.remoteAccessDial)
}

func (s *Server) remoteAccessDial(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), raUpgrade) {
		http.Error(w, "upgrade "+raUpgrade+" required", 400)
		return
	}
	q := r.URL.Query()
	target := q.Get("target")
	proto := q.Get("proto")
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		http.Error(w, "proto must be tcp or udp", 400)
		return
	}
	host, port, err := net.SplitHostPort(target)
	if p, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || p < 1 || p > 65535 {
		http.Error(w, "target must be host:port", 400)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection cannot be taken over", 500)
		return
	}

	// The managing node's check before it opens an entry is not a flow.
	var f *audit.Flow
	if r.Header.Get("X-Hy2scale-Probe") == "" {
		f = audit.Begin(audit.InRemote, proto)
	}
	f.SetSrc(r.Header.Get("X-Hy2scale-Client"), r.Header.Get("X-Hy2scale-From"))
	f.SetDst(target)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	d := net.Dialer{}
	out, err := d.DialContext(ctx, proto, target)
	cancel()
	if err != nil {
		f.SetDirect("", "", "")
		f.End(err)
		http.Error(w, err.Error(), 502)
		return
	}
	dstIP, _, _ := net.SplitHostPort(out.RemoteAddr().String())
	srcAddr := ""
	if la := out.LocalAddr(); la != nil {
		srcAddr = la.String()
	}
	f.SetDirect(dstIP, "", srcAddr)
	c, brw, err := hj.Hijack()
	if err != nil {
		out.Close()
		f.End(err)
		return
	}
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + raUpgrade + "\r\n\r\n")
	if err := brw.Flush(); err != nil {
		c.Close()
		out.Close()
		f.End(err)
		return
	}
	// Bytes the client sent after the request are already in brw.
	stream := &raBufConn{Conn: c, r: brw.Reader}
	if proto == "tcp" {
		up, down := raSplice(stream, out)
		f.Add(up, down)
		f.End(nil)
		return
	}
	up, down := raUDPExit(stream, out)
	f.Add(up, down)
	f.End(nil)
}

// raBufConn: a connection whose first bytes may sit in a reader already.
type raBufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *raBufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// raSplice copies both ways until either side ends, then closes both; up
// counts a→b, down b→a.
func raSplice(a, b net.Conn) (up, down int64) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		down, _ = io.Copy(a, b)
		a.Close()
		b.Close()
	}()
	up, _ = io.Copy(b, a)
	a.Close()
	b.Close()
	wg.Wait()
	return up, down
}

// raWriteFrame writes one datagram to a stream.
func raWriteFrame(w io.Writer, p []byte) error {
	if len(p) > raMaxDatagram {
		return errors.New("datagram too large")
	}
	buf := make([]byte, 2+len(p))
	binary.BigEndian.PutUint16(buf, uint16(len(p)))
	copy(buf[2:], p)
	_, err := w.Write(buf)
	return err
}

// raReadFrame reads one datagram from a stream into buf.
func raReadFrame(r io.Reader, buf []byte) (int, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n > len(buf) {
		return 0, fmt.Errorf("datagram of %d bytes", n)
	}
	_, err := io.ReadFull(r, buf[:n])
	return n, err
}

// raUDPExit carries datagrams between the stream and the dialled UDP
// socket until the stream ends (the managing node closes it when idle).
func raUDPExit(stream net.Conn, out net.Conn) (up, down int64) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, raMaxDatagram)
		for {
			n, err := out.Read(buf)
			if err != nil {
				stream.Close()
				return
			}
			if raWriteFrame(stream, buf[:n]) != nil {
				out.Close()
				return
			}
			down += int64(n)
		}
	}()
	buf := make([]byte, raMaxDatagram)
	for {
		n, err := raReadFrame(stream, buf)
		if err != nil {
			break
		}
		if _, err := out.Write(buf[:n]); err == nil {
			up += int64(n)
		}
	}
	stream.Close()
	out.Close()
	wg.Wait()
	return up, down
}

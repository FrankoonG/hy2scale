package msess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// refusedCapacity: HELLO_ACK message for a server at MaxSessions.
const refusedCapacity = "at capacity"

// ServerConfig configures the exit side.
type ServerConfig struct {
	// DialTarget opens the user's destination (exit-local dial).
	DialTarget func(ctx context.Context, addr string) (net.Conn, error)
	// DialPacketTarget opens a connected UDP socket for a packet session
	// (nil = net.Dialer "udp").
	DialPacketTarget func(ctx context.Context, addr string) (net.Conn, error)
	MaxSessions      int // 0 = 20000
	Logf             func(string, ...any)
}

// Server terminates subflows at the exit and owns the target sockets.
type Server struct {
	cfg      ServerConfig
	mu       sync.Mutex
	sessions map[[16]byte]*session
	pending  map[[16]byte]bool
	closed   bool
}

func NewServer(cfg ServerConfig) *Server {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 20000
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Server{cfg: cfg, sessions: map[[16]byte]*session{}, pending: map[[16]byte]bool{}}
}

// Handle serves one incoming subflow stream (a StreamMsess relay stream).
func (sv *Server) Handle(conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReaderSize(conn, 32<<10)
	h, err := readHello(br)
	if err != nil {
		conn.Close()
		return
	}
	switch h.kind {
	case kindProbe:
		if writeHelloAck(conn, helloAck{status: stOK}) != nil {
			conn.Close()
			return
		}
		sv.serveProbe(conn, br)
	case kindOpen, kindOpenPacket:
		sv.open(conn, br, h)
	case kindJoin:
		sv.mu.Lock()
		s := sv.sessions[h.sid]
		sv.mu.Unlock()
		if s == nil || h.mode != s.mode {
			writeHelloAck(conn, helloAck{status: stUnknown, msg: "unknown session"})
			conn.Close()
			return
		}
		s.mu.Lock()
		s.onAckLocked(h.rxNext, 0)
		rx := s.rRead
		dead := s.dead
		s.mu.Unlock()
		if dead {
			writeHelloAck(conn, helloAck{status: stUnknown, msg: "session ended"})
			conn.Close()
			return
		}
		if writeHelloAck(conn, helloAck{status: stOK, rxNext: rx}) != nil {
			conn.Close()
			return
		}
		conn.SetReadDeadline(time.Time{})
		s.attach(conn, br, h.sub, "")
	default:
		conn.Close()
	}
}

func (sv *Server) open(conn net.Conn, br *bufio.Reader, h hello) {
	if h.mode != ModeSelector && h.mode != ModeBond {
		writeHelloAck(conn, helloAck{status: stRefused, msg: "bad mode"})
		conn.Close()
		return
	}
	sv.mu.Lock()
	switch {
	case sv.closed:
		sv.mu.Unlock()
		writeHelloAck(conn, helloAck{status: stRefused, msg: "closing"})
		conn.Close()
		return
	case sv.sessions[h.sid] != nil || sv.pending[h.sid]:
		sv.mu.Unlock()
		writeHelloAck(conn, helloAck{status: stRefused, msg: "session exists"})
		conn.Close()
		return
	case len(sv.sessions)+len(sv.pending) >= sv.cfg.MaxSessions:
		sv.mu.Unlock()
		writeHelloAck(conn, helloAck{status: stRefused, msg: refusedCapacity})
		conn.Close()
		return
	}
	sv.pending[h.sid] = true
	sv.mu.Unlock()

	packet := h.kind == kindOpenPacket
	dialTarget := sv.cfg.DialTarget
	if packet {
		dialTarget = sv.cfg.DialPacketTarget
		if dialTarget == nil {
			dialTarget = func(ctx context.Context, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "udp", addr)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	target, err := dialTarget(ctx, h.target)
	cancel()
	if err != nil {
		sv.mu.Lock()
		delete(sv.pending, h.sid)
		sv.mu.Unlock()
		writeHelloAck(conn, helloAck{status: stDialFailed, msg: err.Error()})
		conn.Close()
		return
	}
	s := newSession(h.sid, h.mode, true, sv.cfg.Logf)
	// The ingress decides how long the user's connection may wait for a
	// path; wait a little longer here so a rejoin at its deadline still
	// finds the session.
	s.mu.Lock()
	s.orphan = ClampGrace(time.Duration(h.grace)*time.Second) + 5*time.Second
	s.packet = packet
	s.lastDgram = time.Now()
	s.mu.Unlock()
	sv.mu.Lock()
	delete(sv.pending, h.sid)
	sv.sessions[h.sid] = s
	sv.mu.Unlock()
	if writeHelloAck(conn, helloAck{status: stOK}) != nil {
		conn.Close()
		target.Close()
		s.abort(errors.New("handshake write failed"))
		sv.forget(s)
		return
	}
	conn.SetReadDeadline(time.Time{})
	s.attach(conn, br, h.sub, "")
	if packet {
		go sv.pumpPacket(s, target)
	} else {
		go sv.pump(s, target)
	}
}

func (sv *Server) forget(s *session) {
	sv.mu.Lock()
	if sv.sessions[s.id] == s {
		delete(sv.sessions, s.id)
	}
	sv.mu.Unlock()
}

// pump joins the session with the target socket; the session ends when
// both directions finished (or either side aborted).
func (sv *Server) pump(s *session, target net.Conn) {
	var wg sync.WaitGroup
	var closedByUs atomic.Bool
	wg.Add(2)
	go func() { // target → session
		defer wg.Done()
		buf := make([]byte, 32<<10)
		for {
			n, err := target.Read(buf)
			if n > 0 {
				if _, werr := s.Write(buf[:n]); werr != nil {
					target.Close()
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) || closedByUs.Load() {
					s.CloseWrite()
				} else {
					s.abort(fmt.Errorf("target: %v", err))
				}
				return
			}
		}
	}()
	go func() { // session → target
		defer wg.Done()
		_, err := io.Copy(target, readerOnly{s})
		if err == nil {
			if cw, ok := target.(interface{ CloseWrite() error }); ok {
				cw.CloseWrite()
				return
			}
		}
		closedByUs.Store(true)
		target.Close()
	}()
	wg.Wait()
	s.Close()
	target.Close()
	<-s.done
	sv.forget(s)
}

// pumpPacket moves datagrams between the session and one UDP socket.
func (sv *Server) pumpPacket(s *session, target net.Conn) {
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := target.Read(buf)
			if n > 0 {
				if _, werr := s.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				// connected UDP reports ICMP errors (refused, unreachable)
				// on read; the flow itself goes on
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()
	buf := make([]byte, 65535)
	for {
		n, err := s.Read(buf)
		if err != nil {
			break
		}
		target.Write(buf[:n])
	}
	target.Close()
	<-s.done
	sv.forget(s)
}

type readerOnly struct{ s *session }

func (r readerOnly) Read(p []byte) (int, error) { return r.s.Read(p) }

// serveProbe answers PINGs on a probe stream until it goes quiet.
func (sv *Server) serveProbe(conn net.Conn, br *bufio.Reader) {
	defer conn.Close()
	buf := make([]byte, 64)
	var seq uint32
	for {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		f, err := readFrame(br, buf)
		if err != nil || f.typ != fPing || len(f.payload) < 12 {
			return
		}
		seq++
		out := append(appendHdr(make([]byte, 0, hdrLen+12), fPong, seq, 12), f.payload[:12]...)
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

// Close aborts every session.
func (sv *Server) Close() {
	sv.mu.Lock()
	sv.closed = true
	ss := make([]*session, 0, len(sv.sessions))
	for _, s := range sv.sessions {
		ss = append(ss, s)
	}
	sv.mu.Unlock()
	for _, s := range ss {
		s.abort(errors.New("exit shutting down"))
	}
}

// ServerStats is a point-in-time count for diagnostics.
type ServerStats struct {
	Sessions int
	Subflows int
	Pending  int
}

func (sv *Server) Stats() ServerStats {
	sv.mu.Lock()
	ss := make([]*session, 0, len(sv.sessions))
	for _, s := range sv.sessions {
		ss = append(ss, s)
	}
	st := ServerStats{Sessions: len(sv.sessions), Pending: len(sv.pending)}
	sv.mu.Unlock()
	for _, s := range ss {
		s.mu.Lock()
		st.Subflows += len(s.subs)
		s.mu.Unlock()
	}
	return st
}

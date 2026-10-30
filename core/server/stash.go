package server

import (
	"net"
	"runtime"
	"strconv"
	"sync"
)

// hysteria's Authenticator returns the auth ID at connection setup, but its
// Outbound.TCP / UDP only get the target. Its EventLogger.TCPRequest /
// UDPRequest run immediately before them on the same goroutine, so the auth
// ID and client address are stashed under the goroutine id and taken by
// the Outbound call. Should a hysteria release break that sequence, the
// lookup comes back empty and the stream is treated as a node's with no
// auth ID: remote management then refuses it (default deny).

type stashed struct{ auth, addr string }

var stashes sync.Map // goroutine id → stashed

func goid() uint64 {
	var buf [32]byte
	n := runtime.Stack(buf[:], false)
	line := buf[:n]
	if len(line) < 10 {
		return 0
	}
	line = line[len("goroutine "):]
	end := 0
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	id, _ := strconv.ParseUint(string(line[:end]), 10, 64)
	return id
}

func stash(addr net.Addr, id string) {
	s := stashed{auth: id}
	if addr != nil {
		s.addr = addr.String()
	}
	stashes.Store(goid(), s)
}

func take() (authID, peerAddr string) {
	v, ok := stashes.LoadAndDelete(goid())
	if !ok {
		return "", ""
	}
	s := v.(stashed)
	return s.auth, s.addr
}

// Stashed: entries waiting for their Outbound call (diagnostics).
func Stashed() int {
	n := 0
	stashes.Range(func(_, _ any) bool { n++; return true })
	return n
}

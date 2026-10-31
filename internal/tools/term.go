package tools

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// shellSession is a shell running on a PTY (pty_linux.go).
type shellSession interface {
	io.ReadWriter
	Resize(cols, rows int) error
	// Done is closed when the shell has exited; Code is then its status.
	Done() <-chan struct{}
	Code() int
	// Kill ends every process of the shell's session and frees the PTY.
	Kill()
}

// TerminalHandler serves GET /api/tools/terminal (WebSocket). Binary
// frames carry PTY bytes both ways; the client's text frames carry
// {"type":"resize","cols":N,"rows":M}; when the shell exits the server sends
// {"type":"exit","code":N} and closes. Dropping the socket kills the shell.
func TerminalHandler(w http.ResponseWriter, r *http.Request) {
	if ok, why := terminalAvailable(); !ok {
		http.Error(w, why, http.StatusNotImplemented)
		return
	}
	q := r.URL.Query()
	cols, err := intParam(q, "cols", 80, 1, 1000)
	if err == nil {
		var rows int
		if rows, err = intParam(q, "rows", 24, 1, 1000); err == nil {
			serveTerminal(w, r, cols, rows)
			return
		}
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func serveTerminal(w http.ResponseWriter, r *http.Request, cols, rows int) {
	ws, err := AcceptWS(w, r)
	if err != nil {
		return
	}
	defer ws.Close()
	sh, err := startShell(cols, rows)
	if err != nil {
		ws.WriteMessage(OpText, []byte(`{"type":"error","error":`+strconv.Quote(err.Error())+`}`))
		ws.CloseWith(1011)
		return
	}
	log.Printf("[tools] terminal opened from %s", r.RemoteAddr)
	defer log.Printf("[tools] terminal from %s closed", r.RemoteAddr)
	defer sh.Kill()

	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		buf := make([]byte, 32<<10)
		for {
			n, err := sh.Read(buf)
			if n > 0 && ws.WriteMessage(OpBinary, buf[:n]) != nil {
				return
			}
			if err != nil {
				return
			}
		}
	}()
	readDone := make(chan struct{})
	var once sync.Once
	go func() {
		select {
		case <-sh.Done():
			// let the last output through (a background child may keep the
			// PTY open: don't wait for it)
			select {
			case <-outDone:
			case <-time.After(500 * time.Millisecond):
			}
			once.Do(func() {
				ws.WriteMessage(OpText, []byte(`{"type":"exit","code":`+strconv.Itoa(sh.Code())+`}`))
				ws.CloseWith(1000)
			})
		case <-readDone:
		}
	}()
	for {
		op, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		switch op {
		case OpBinary:
			sh.Write(data)
		case OpText:
			var m struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &m) == nil && m.Type == "resize" &&
				m.Cols > 0 && m.Rows > 0 && m.Cols <= 1000 && m.Rows <= 1000 {
				sh.Resize(m.Cols, m.Rows)
			}
		}
	}
	close(readDone)
	once.Do(func() {})
}

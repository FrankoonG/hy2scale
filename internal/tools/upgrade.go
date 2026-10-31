package tools

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// SpliceUpgrade forwards an upgrade request (out, its Connection / Upgrade
// headers intact) over upstream and reads the answer. A 101 is passed to
// the client, whose connection is then taken over and spliced to upstream
// until either side closes; any other answer is relayed as it is. upstream
// is closed on return.
func SpliceUpgrade(w http.ResponseWriter, out *http.Request, upstream net.Conn) error {
	defer upstream.Close()
	if err := out.Write(upstream); err != nil {
		http.Error(w, "write to remote failed: "+err.Error(), http.StatusBadGateway)
		return err
	}
	ur := bufio.NewReader(upstream)
	resp, err := http.ReadResponse(ur, out)
	if err != nil {
		http.Error(w, "bad response from remote: "+err.Error(), http.StatusBadGateway)
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			switch http.CanonicalHeaderKey(k) {
			case "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "Upgrade":
				continue
			}
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return nil
	}
	client, crw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "upgrade not supported here", http.StatusInternalServerError)
		return err
	}
	defer client.Close()
	client.SetDeadline(time.Time{})
	fmt.Fprintf(crw, "HTTP/1.1 101 %s\r\n", http.StatusText(http.StatusSwitchingProtocols))
	h := resp.Header.Clone()
	h.Del("Content-Length")
	h.Set("Connection", "Upgrade")
	if h.Get("Upgrade") == "" {
		h.Set("Upgrade", out.Header.Get("Upgrade"))
	}
	h.Write(crw)
	crw.WriteString("\r\n")
	// bytes the remote sent right after its 101 are already in ur
	if n := ur.Buffered(); n > 0 {
		b, _ := ur.Peek(n)
		crw.Write(b)
		ur.Discard(n)
	}
	if err := crw.Flush(); err != nil {
		return err
	}
	var once sync.Once
	shut := func() { once.Do(func() { client.Close(); upstream.Close() }) }
	done := make(chan struct{})
	go func() {
		io.Copy(upstream, crw.Reader) // drains what the client already sent
		shut()
		close(done)
	}()
	io.Copy(client, upstream)
	shut()
	<-done
	return nil
}

// Automatic update: once a day at the configured time (node local time),
// look for a new stable release on GitHub, fetch it, check that it is an
// official build and install it (the supervisor restarts the node). With
// FromPeers, a release that cannot be fetched is taken from a node that
// already runs a newer official stable build, over the fastest path first.

package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/internal/buildsig"
)

const (
	autoUpdateWindow   = time.Hour        // after the set time, attempts are made within this
	autoUpdateRetry    = 10 * time.Minute // between failed attempts in the window
	autoUpdatePeers    = 5                // nodes tried for the binary
	autoUpdateDownload = 15 * time.Minute
)

// autoUpdateResult: the last run, as the settings show it.
type autoUpdateResult struct {
	At      string `json:"at"`
	Result  string `json:"result"` // updated | up_to_date | failed
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"` // "github" or the path of the node
	Error   string `json:"error,omitempty"`
}

var (
	autoUpdateMu   sync.Mutex
	autoUpdateLast = map[*Server]*autoUpdateResult{}
)

func (s *Server) autoUpdateLast() *autoUpdateResult {
	autoUpdateMu.Lock()
	defer autoUpdateMu.Unlock()
	if r := autoUpdateLast[s]; r != nil {
		c := *r
		return &c
	}
	return nil
}

func (s *Server) setAutoUpdateLast(r autoUpdateResult) {
	r.At = time.Now().UTC().Format(time.RFC3339)
	autoUpdateMu.Lock()
	autoUpdateLast[s] = &r
	autoUpdateMu.Unlock()
}

// autoUpdateClock: the update clock (node local time). Tests shift it with
// HY2SCALE_AUTO_UPDATE_CLOCK_OFFSET (a Go duration).
var autoUpdateOffset = func() time.Duration {
	d, _ := time.ParseDuration(os.Getenv("HY2SCALE_AUTO_UPDATE_CLOCK_OFFSET"))
	return d
}()

func autoUpdateNow() time.Time { return time.Now().Add(autoUpdateOffset).Local() }

// startAutoUpdate runs the daily check for the life of the process.
func (s *Server) startAutoUpdate() {
	go func() {
		var doneDay string    // the day whose run ended (updated or up to date)
		var lastTry time.Time // the last failed attempt
		for {
			time.Sleep(30 * time.Second)
			cfg := s.app.Store().Get().AutoUpdate
			if !cfg.Enabled || !canSelfUpgrade() {
				continue
			}
			now := autoUpdateNow()
			at, err := time.ParseInLocation("15:04", cfg.UpdateTime(), now.Location())
			if err != nil {
				continue
			}
			slot := time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, now.Location())
			day := slot.Format("2006-01-02")
			if now.Before(slot) || now.Sub(slot) >= autoUpdateWindow || doneDay == day || time.Since(lastTry) < autoUpdateRetry {
				continue
			}
			if s.autoUpdateOnce(cfg.FromPeers) {
				doneDay = day
			} else {
				lastTry = time.Now()
			}
		}
	}()
}

// autoUpdateOnce makes one attempt; true when nothing is left to do today.
// A successful install exits the process (the supervisor restarts it).
func (s *Server) autoUpdateOnce(fromPeers bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	rel, err := fetchLatestRelease(ctx)
	cancel()
	want := ""
	var failure error
	switch {
	case err != nil:
		failure = err
	case !semverGreater(rel.Version, Version):
		s.setAutoUpdateLast(autoUpdateResult{Result: "up_to_date", Version: Version, Source: "github"})
		return true
	case rel.URL == "":
		want, failure = rel.Version, fmt.Errorf("release %s has no %s", rel.Version, rel.Asset)
	default:
		want = rel.Version
		log.Printf("[auto-update] %s → %s from GitHub", Version, want)
		failure = s.autoUpdateFrom("github", want, func(ctx context.Context, w io.Writer) error {
			return httpGet(ctx, rel.URL, w)
		})
	}
	if failure == nil {
		return true // not reached: the install exits
	}
	log.Printf("[auto-update] %v", failure)
	if fromPeers {
		perr := s.autoUpdateFromPeers(want)
		if perr == nil {
			return true
		}
		failure = fmt.Errorf("%v; from nodes: %v", failure, perr)
		log.Printf("[auto-update] from nodes: %v", perr)
	}
	s.setAutoUpdateLast(autoUpdateResult{Result: "failed", Version: want, Error: failure.Error()})
	return false
}

// autoUpdateFromPeers takes the binary from a node already running a newer
// official stable build (want, when the release is known), fastest path
// first. A node of another platform or not official refuses.
func (s *Server) autoUpdateFromPeers(want string) error {
	tried, seen := 0, map[string]bool{}
	var lastErr error
	for _, r := range s.app.Reachable() {
		node := r.Hops[len(r.Hops)-1]
		v := strings.TrimPrefix(r.Version, "v")
		if seen[node] || v == "" || strings.Contains(v, "-") || !semverGreater(v, Version) || (want != "" && v != want) {
			continue
		}
		if s.app.Node().NativeMap()[node] {
			continue
		}
		seen[node] = true
		if tried++; tried > autoUpdatePeers {
			break
		}
		path := r.Path()
		log.Printf("[auto-update] %s → %s from %s", Version, v, path)
		err := s.autoUpdateFrom(path, v, func(ctx context.Context, w io.Writer) error {
			return s.peerBinary(ctx, r.Hops, w)
		})
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%s: %v", path, err)
		log.Printf("[auto-update] %v", lastErr)
	}
	if lastErr == nil {
		return fmt.Errorf("no node runs a newer official release")
	}
	return lastErr
}

// autoUpdateFrom fetches an upgrade archive, checks that it is an official
// build of version want and installs it, then exits for the restart.
func (s *Server) autoUpdateFrom(source, want string, fetch func(context.Context, io.Writer) error) error {
	tmp, err := os.CreateTemp("", "hy2scale-auto-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	ctx, cancel := context.WithTimeout(context.Background(), autoUpdateDownload)
	err = fetch(ctx, tmp)
	cancel()
	tmp.Close()
	if err != nil {
		return fmt.Errorf("download: %v", err)
	}
	a, err := readUpgradeArchive(tmp.Name())
	if err != nil {
		return err
	}
	if a.check.Status != buildsig.Official || a.check.Manifest == nil {
		a.remove()
		return fmt.Errorf("not an official build (%s %s)", a.check.Status, a.check.Reason)
	}
	if v := a.check.Manifest.Version; v != want || !semverGreater(v, Version) {
		a.remove()
		return fmt.Errorf("got version %s, want %s", v, want)
	}
	if err := installUpgrade(a, s.app.DataDir(), false); err != nil {
		return err
	}
	s.setAutoUpdateLast(autoUpdateResult{Result: "updated", Version: want, Source: source})
	log.Printf("[auto-update] installed %s from %s — restarting", want, source)
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
	return nil
}

func httpGet(ctx context.Context, u string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "hy2scale/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, 512<<20))
	return err
}

// peerBinary fetches the binary of the node at the end of chain (its
// /api/upgrade/peer-binary) into w.
func (s *Server) peerBinary(ctx context.Context, chain []string, w io.Writer) error {
	if s.app.Node().IsInvisible(chain[0]) {
		return fmt.Errorf("not over node access by user")
	}
	conn, _, err := s.dialRemoteAPI(ctx, chain)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	req, _ := http.NewRequest("GET", "http://127.0.0.1:5565/scale/api/upgrade/peer-binary?platform="+url.QueryEscape(buildsig.Platform()), nil)
	req.Host = "127.0.0.1:5565"
	req.Close = true
	if err := req.Write(conn); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, 512<<20))
	return err
}

// servePeerBinary: GET /api/upgrade/peer-binary?platform=P — this node's
// binary as an upgrade archive, for a node updating itself. Served over the
// relay to nodes only (the system password), and only an official build
// of the asked platform: the receiver checks the signature anyway.
func (s *Server) servePeerBinary(w http.ResponseWriter, r *http.Request) {
	raw := r.Context().Value(relayCtxKey{})
	if raw == nil {
		http.NotFound(w, r)
		return
	}
	if authID, _ := raw.(string); authID != "system" {
		http.Error(w, "not over this connection", 403)
		return
	}
	if p := r.URL.Query().Get("platform"); p != buildsig.Platform() {
		http.Error(w, "platform "+buildsig.Platform(), 409)
		return
	}
	if buildsig.Self().Status != buildsig.Official {
		http.Error(w, "not an official build", 409)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	if err := writeOwnArchive(w); err != nil {
		log.Printf("[auto-update] serving own binary: %v", err)
	}
}

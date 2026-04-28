package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Online-update subsystem. Goal: any logged-in session can press a single
// button to fetch the latest GitHub Release tarball for the running
// platform; the download runs once on the server and every other session
// observes the same progress; on completion the same button morphs into
// "Apply Update" which calls applyDownloadedUpgrade. Server-singleton
// state means switching browsers / tabs / devices doesn't restart the
// download.
//
// State machine:
//   idle → checking → idle           (check-update, no download triggered)
//   idle → downloading → ready       (happy path)
//   downloading|ready → applying     (apply triggers binary swap + os.Exit)
//   any → error (sticky until next check or download attempt)

const (
	upgradeStateIdle        = "idle"
	upgradeStateChecking    = "checking"
	upgradeStateDownloading = "downloading"
	upgradeStateReady       = "ready"
	upgradeStateApplying    = "applying"
	upgradeStateError       = "error"
)

// githubLatestURL is the GitHub REST endpoint for a repo's latest release.
// Hard-coded to FrankoonG/hy2scale to match the repository field surfaced
// by /api/build-info — switching the in-app updater to a fork would
// require a code change, which is the intended behaviour (operators
// shouldn't be able to redirect updates via runtime config).
const githubLatestURL = "https://api.github.com/repos/FrankoonG/hy2scale/releases/latest"

// upgradeJob holds the singleton state observed by all sessions.
type upgradeJob struct {
	mu sync.Mutex

	State       string  `json:"state"`
	Current     string  `json:"current"`
	Latest      string  `json:"latest"`
	Asset       string  `json:"asset"`
	DownloadURL string  `json:"download_url"`
	Progress    float64 `json:"progress"` // 0–100
	BytesDone   int64   `json:"bytes_done"`
	BytesTotal  int64   `json:"bytes_total"`
	Error       string  `json:"error,omitempty"`

	filePath string // local path to downloaded tarball (state=ready)
	cancel   context.CancelFunc

	// SSE subscribers — fan out a snapshot on every state mutation. Slow
	// consumers drop frames; each frame carries a full snapshot so a
	// missed event self-heals on the next.
	subs    map[uint64]chan upgradeJobSnapshot
	subSeq  uint64
	subsMu  sync.Mutex
}

type upgradeJobSnapshot struct {
	State       string  `json:"state"`
	Current     string  `json:"current"`
	Latest      string  `json:"latest"`
	Asset       string  `json:"asset"`
	DownloadURL string  `json:"download_url"`
	Progress    float64 `json:"progress"`
	BytesDone   int64   `json:"bytes_done"`
	BytesTotal  int64   `json:"bytes_total"`
	Error       string  `json:"error,omitempty"`
}

func newUpgradeJob() *upgradeJob {
	return &upgradeJob{
		State:   upgradeStateIdle,
		Current: Version,
		subs:    make(map[uint64]chan upgradeJobSnapshot),
	}
}

// snapshot copies the public fields under the lock. Caller must hold j.mu.
func (j *upgradeJob) snapshotLocked() upgradeJobSnapshot {
	return upgradeJobSnapshot{
		State:       j.State,
		Current:     j.Current,
		Latest:      j.Latest,
		Asset:       j.Asset,
		DownloadURL: j.DownloadURL,
		Progress:    j.Progress,
		BytesDone:   j.BytesDone,
		BytesTotal:  j.BytesTotal,
		Error:       j.Error,
	}
}

// broadcast fans out a snapshot to every SSE subscriber. Holds subsMu only;
// callers should not hold j.mu when invoking it.
func (j *upgradeJob) broadcast(snap upgradeJobSnapshot) {
	j.subsMu.Lock()
	chs := make([]chan upgradeJobSnapshot, 0, len(j.subs))
	for _, ch := range j.subs {
		chs = append(chs, ch)
	}
	j.subsMu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// expectedAsset returns the GitHub release asset filename for the running
// platform — must match the tar-naming convention in
// .github/workflows/release.yml ("hy2scale-{goos}-{goarch}.tar.gz").
func expectedAsset() string {
	return fmt.Sprintf("hy2scale-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

// semverGreater compares two MAJOR.MINOR.PATCH strings (no pre-release /
// build metadata) and returns true iff a > b. Used to gate the
// "update available" flag — without this we'd happily offer a downgrade
// when a v1.3.1 release sits at /releases/latest while we're already on
// v1.3.2 (the existing `latest != Version` check returns true for any
// difference, including older). Anything malformed → false (treat as
// "not greater"); the user can still see the version mismatch in the UI.
func semverGreater(a, b string) bool {
	parse := func(s string) (int, int, int, bool) {
		parts := strings.SplitN(s, ".", 3)
		if len(parts) != 3 {
			return 0, 0, 0, false
		}
		ints := make([]int, 3)
		for i, p := range parts {
			// Strip any pre-release suffix from the patch field
			// (e.g. "1.3.2-rc1" → 2). Defensive — release tags
			// have been stripped of "v" already by the caller.
			if i == 2 {
				if dash := strings.IndexAny(p, "-+"); dash != -1 {
					p = p[:dash]
				}
			}
			n, err := strconv.Atoi(p)
			if err != nil {
				return 0, 0, 0, false
			}
			ints[i] = n
		}
		return ints[0], ints[1], ints[2], true
	}
	aM, am, ap, ok1 := parse(a)
	bM, bm, bp, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	if aM != bM {
		return aM > bM
	}
	if am != bm {
		return am > bm
	}
	return ap > bp
}

// --- Handlers ---

// checkUpdate queries the GitHub releases API for the latest stable release
// (excluding prereleases via /releases/latest, which already filters them
// out). Returns the parsed result and updates the singleton state so a
// subsequent download can use the same URL/version.
func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	j := s.upgrade
	j.mu.Lock()
	if j.State == upgradeStateDownloading || j.State == upgradeStateApplying {
		// Don't disturb an in-flight job — return its current state.
		snap := j.snapshotLocked()
		j.mu.Unlock()
		writeJSON(w, snap)
		return
	}
	j.State = upgradeStateChecking
	j.Error = ""
	snapBefore := j.snapshotLocked()
	j.mu.Unlock()
	j.broadcast(snapBefore)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", githubLatestURL, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hy2scale/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		j.markError("check failed: " + err.Error())
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		j.markError(fmt.Sprintf("github status %d: %s", resp.StatusCode, string(body)))
		http.Error(w, fmt.Sprintf("github status %d", resp.StatusCode), 502)
		return
	}

	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		j.markError("parse: " + err.Error())
		http.Error(w, err.Error(), 502)
		return
	}

	// Strip leading "v" from tag — release tooling tags are vX.Y.Z but our
	// in-process Version constant is bare X.Y.Z.
	latest := strings.TrimPrefix(rel.TagName, "v")
	wanted := expectedAsset()
	var url string
	var size int64
	for _, a := range rel.Assets {
		if a.Name == wanted {
			url = a.BrowserDownloadURL
			size = a.Size
			break
		}
	}

	j.mu.Lock()
	j.Latest = latest
	j.Asset = wanted
	j.DownloadURL = url
	j.BytesTotal = size
	j.State = upgradeStateIdle
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.broadcast(snap)

	writeJSON(w, map[string]any{
		"current":          Version,
		"latest":           latest,
		"asset":            wanted,
		"download_url":     url,
		"size":             size,
		// Only flag as available when the published release is STRICTLY newer.
		// `latest != Version` would happily offer a downgrade when GitHub's
		// /releases/latest still points at an older tag than what we ship
		// (e.g. running 1.3.2 with the v1.3.1 release still flagged as
		// "latest"). Asset URL also has to exist for the local platform.
		"update_available": url != "" && latest != "" && semverGreater(latest, Version),
	})
}

// startDownload kicks off a background download of the asset URL recorded by
// the most recent checkUpdate call. Idempotent: a second POST while a
// download is already running returns the current snapshot without
// starting a duplicate.
func (s *Server) startDownload(w http.ResponseWriter, r *http.Request) {
	if !isRunningInDocker() {
		http.Error(w, "online update is only available in Docker deployments", 403)
		return
	}
	j := s.upgrade
	j.mu.Lock()
	if j.State == upgradeStateDownloading || j.State == upgradeStateApplying {
		snap := j.snapshotLocked()
		j.mu.Unlock()
		writeJSON(w, snap)
		return
	}
	if j.DownloadURL == "" {
		j.mu.Unlock()
		http.Error(w, "no update available — call /api/upgrade/check first", 400)
		return
	}
	// If a previously downloaded file is sitting in state=ready and the
	// caller hits /download again, treat it as "redownload": clear the
	// stale file before starting fresh.
	if j.filePath != "" {
		os.Remove(j.filePath)
		j.filePath = ""
	}
	j.State = upgradeStateDownloading
	j.Progress = 0
	j.BytesDone = 0
	j.Error = ""
	url := j.DownloadURL
	expected := j.BytesTotal
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	snapStart := j.snapshotLocked()
	j.mu.Unlock()
	j.broadcast(snapStart)

	go j.runDownload(ctx, url, expected)

	writeJSON(w, snapStart)
}

// runDownload streams the asset to a temp file, broadcasting progress
// snapshots roughly every ~500ms or every 1MB, whichever comes first.
func (j *upgradeJob) runDownload(ctx context.Context, url string, expected int64) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		j.markError("request: " + err.Error())
		return
	}
	req.Header.Set("User-Agent", "hy2scale/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		j.markError("download: " + err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		j.markError(fmt.Sprintf("download status %d", resp.StatusCode))
		return
	}
	// Trust the response Content-Length over the GitHub API "size" if
	// they disagree (CDN compressed transfers, etc.).
	if resp.ContentLength > 0 {
		expected = resp.ContentLength
	}

	tmp, err := os.CreateTemp("", "hy2scale-update-*.tar.gz")
	if err != nil {
		j.markError("temp file: " + err.Error())
		return
	}

	buf := make([]byte, 64*1024)
	var done int64
	lastBroadcast := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				tmp.Close()
				os.Remove(tmp.Name())
				j.markError("write: " + werr.Error())
				return
			}
			done += int64(n)
			if expected > 0 {
				j.mu.Lock()
				j.BytesDone = done
				j.BytesTotal = expected
				j.Progress = float64(done) / float64(expected) * 100
				if j.Progress > 100 {
					j.Progress = 100
				}
				j.mu.Unlock()
			}
			// Throttle SSE: every 500ms OR every 4MB.
			if time.Since(lastBroadcast) > 500*time.Millisecond || done%(4<<20) == 0 {
				j.mu.Lock()
				snap := j.snapshotLocked()
				j.mu.Unlock()
				j.broadcast(snap)
				lastBroadcast = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			j.markError("read: " + rerr.Error())
			return
		}
		if ctx.Err() != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			j.markError("cancelled")
			return
		}
	}
	tmp.Close()

	j.mu.Lock()
	j.State = upgradeStateReady
	j.Progress = 100
	j.BytesDone = done
	if expected > 0 {
		j.BytesTotal = expected
	}
	j.filePath = tmp.Name()
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.broadcast(snap)
	log.Printf("[upgrade] downloaded %s (%d bytes) → %s", j.Asset, done, j.filePath)
}

// markError clears any partial state and broadcasts the failure.
func (j *upgradeJob) markError(msg string) {
	j.mu.Lock()
	j.State = upgradeStateError
	j.Error = msg
	if j.filePath != "" {
		os.Remove(j.filePath)
		j.filePath = ""
	}
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.broadcast(snap)
}

// getUpgradeStatus returns the current snapshot — primarily used by frontend
// mounts to populate state before subscribing to the SSE stream.
func (s *Server) getUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	j := s.upgrade
	j.mu.Lock()
	snap := j.snapshotLocked()
	j.mu.Unlock()
	writeJSON(w, snap)
}

// streamUpgradeEvents pushes upgrade snapshots over SSE. The first frame
// after connect carries the current state so the client doesn't need a
// separate GET on mount.
func (s *Server) streamUpgradeEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	j := s.upgrade
	ch := make(chan upgradeJobSnapshot, 8)
	j.subsMu.Lock()
	j.subSeq++
	id := j.subSeq
	j.subs[id] = ch
	j.subsMu.Unlock()
	defer func() {
		j.subsMu.Lock()
		delete(j.subs, id)
		j.subsMu.Unlock()
	}()

	writeEvent := func(snap upgradeJobSnapshot) bool {
		payload, err := json.Marshal(snap)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Initial snapshot.
	j.mu.Lock()
	initial := j.snapshotLocked()
	j.mu.Unlock()
	if !writeEvent(initial) {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case snap := <-ch:
			if !writeEvent(snap) {
				return
			}
		}
	}
}

// applyDownloadedUpgrade applies a tarball previously fetched into the job's
// filePath. Reuses the same in-place swap logic as the manual uploadUpgrade
// handler — the heavy lifting (extract, verify, rename, restart) lives in
// applyTarball so both code paths stay in lock-step.
func (s *Server) applyDownloadedUpgrade(w http.ResponseWriter, r *http.Request) {
	if !isRunningInDocker() {
		http.Error(w, "upgrade via web is only available in Docker deployments", 403)
		return
	}
	j := s.upgrade
	j.mu.Lock()
	if j.State != upgradeStateReady || j.filePath == "" {
		j.mu.Unlock()
		http.Error(w, "no downloaded update ready to apply", 400)
		return
	}
	path := j.filePath
	j.State = upgradeStateApplying
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.broadcast(snap)

	if err := applyTarball(path); err != nil {
		j.markError("apply: " + err.Error())
		http.Error(w, err.Error(), 500)
		return
	}

	// Successful apply schedules os.Exit; reply before the process dies so
	// the client gets a clean ack.
	writeJSON(w, map[string]string{"status": "ok", "message": "upgrade applied — restarting"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

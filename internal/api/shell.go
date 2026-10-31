// Program shell: backup, restore, upgrade, restart.

package api

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/internal/buildsig"
	"github.com/FrankoonG/hy2scale/internal/selfupdate"
)

// shellRoutes: the routes of the program shell (backup, upgrade, restart).
func (s *Server) shellRoutes(m *http.ServeMux) {
	// Backup / Restore
	m.HandleFunc("GET /api/backup", s.downloadBackup)
	m.HandleFunc("POST /api/restore", s.uploadRestore)
	// Upgrade
	m.HandleFunc("GET /api/system/arch", s.getSystemArch)
	m.HandleFunc("GET /api/build-info", s.getBuildInfo)
	m.HandleFunc("POST /api/upgrade", s.uploadUpgrade)
	// Install this node's binary on a remote node (see remote.go).
	m.HandleFunc("POST /api/remote/push-binary", s.pushBinary)
	// Online-update endpoints. /check polls GitHub for the latest release;
	// /download starts a server-side download whose progress every session
	// observes via /events (SSE); /apply swaps in the downloaded binary.
	m.HandleFunc("GET /api/upgrade/check", s.checkUpdate)
	m.HandleFunc("POST /api/upgrade/download", s.startDownload)
	m.HandleFunc("GET /api/upgrade/status", s.getUpgradeStatus)
	m.HandleFunc("GET /api/upgrade/events", s.streamUpgradeEvents)
	m.HandleFunc("POST /api/upgrade/apply", s.applyDownloadedUpgrade)
	m.HandleFunc("POST /api/system/restart", s.restartProcess)
}

// SetRestart installs the process restart used by POST /api/system/restart.
func (s *Server) SetRestart(fn func()) { s.restart = fn }

// restartProcess replies, then restarts the node (same path as a stop, so
// counters and history are flushed first).
func (s *Server) restartProcess(w http.ResponseWriter, r *http.Request) {
	if s.restart == nil {
		http.Error(w, "restart not available", http.StatusNotImplemented)
		return
	}
	log.Printf("[api] restart requested from the web UI")
	writeJSON(w, map[string]string{"status": "ok"})
	go func() {
		time.Sleep(300 * time.Millisecond) // let the reply go out
		s.restart()
	}()
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	dataDir := s.app.DataDir()
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", "attachment; filename=hy2scale-backup.tar")
	tw := tar.NewWriter(w)
	defer tw.Close()
	filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dataDir, path)
		if rel == "." {
			return nil
		}
		// The stored upgrade binary is not configuration, nor are the
		// uploaded GeoIP databases (large, and re-uploadable) or the kept
		// logs and audit records.
		if notBackedUp[rel] && info.IsDir() {
			return filepath.SkipDir
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			io.Copy(tw, f)
		}
		return nil
	})
}

// notBackedUp: top-level data directories that backups leave out and
// restores skip.
var notBackedUp = map[string]bool{"bin": true, "geoip": true, "logs": true, "audit": true}

func (s *Server) uploadRestore(w http.ResponseWriter, r *http.Request) {
	dataDir := s.app.DataDir()

	// Read tar from request body (limit 50MB)
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	tr := tar.NewReader(r.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "invalid tar: "+err.Error(), 400)
			return
		}
		target := filepath.Join(dataDir, filepath.Clean(hdr.Name))
		// Never the stored upgrade binary: a restore must not change what
		// runs (judged by where the entry lands, whatever its spelling).
		// Nor what backups leave out.
		if rel, err := filepath.Rel(dataDir, target); err != nil || notBackedUp[strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]] {
			continue
		}
		// Security: ensure target is within dataDir
		if !strings.HasPrefix(target, filepath.Clean(dataDir)+string(os.PathSeparator)) && target != filepath.Clean(dataDir) {
			continue
		}
		if hdr.Typeflag == tar.TypeDir {
			os.MkdirAll(target, 0755)
		} else {
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.Create(target)
			if err != nil {
				continue
			}
			io.Copy(f, tr)
			f.Close()
		}
	}
	log.Printf("[backup] config restored from upload, restarting...")
	writeJSON(w, map[string]string{"status": "ok"})

	// Restart the process after a short delay
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0) // container/systemd will restart the process
	}()
}

func isRunningInDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	// Fallback: check cgroup for docker/containerd
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		if strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "/lxc/") {
			return true
		}
	}
	return false
}

func (s *Server) getSystemArch(w http.ResponseWriter, r *http.Request) {
	self, _ := os.Executable()
	var size int64
	if fi, err := os.Stat(self); err == nil {
		size = fi.Size()
	}
	writeJSON(w, map[string]any{
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"version":     Version,
		"binary":      filepath.Base(self),
		"binary_size": size,
		"in_docker":   isRunningInDocker(),
		"official":    buildsig.Self().Status == buildsig.Official,
	})
}

// getBuildInfo returns the project's license metadata and the full
// dependency list (Go module graph compiled in + the significant
// native bundles that ship in the Docker image). Exposed to the web
// UI so users can see exactly what third-party code is running and
// under which licence, without having to inspect the binary.
func (s *Server) getBuildInfo(w http.ResponseWriter, r *http.Request) {
	type dep struct {
		Path    string `json:"path"`
		Version string `json:"version"`
	}
	type native struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		License string `json:"license"`
		Source  string `json:"source"`
	}
	type credit struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		License string `json:"license"`
		Source  string `json:"source"`
	}
	goDeps := []dep{}
	var goVersion, commit, commitTime string
	var modified bool
	if info, ok := debug.ReadBuildInfo(); ok {
		goVersion = info.GoVersion
		for _, kv := range info.Settings {
			switch kv.Key {
			case "vcs.revision":
				commit = kv.Value
			case "vcs.time":
				commitTime = kv.Value
			case "vcs.modified":
				modified = kv.Value == "true"
			}
		}
		for _, m := range info.Deps {
			// Skip replaced / anonymous — keep the resolved identity.
			resolved := m
			if m.Replace != nil {
				resolved = m.Replace
			}
			if resolved.Path == "" || resolved.Version == "" {
				continue
			}
			goDeps = append(goDeps, dep{Path: resolved.Path, Version: resolved.Version})
		}
	}
	sort.Slice(goDeps, func(i, j int) bool { return goDeps[i].Path < goDeps[j].Path })
	// The upstream projects this one is built on, with the version linked.
	known := []struct{ name, module, license, source string }{
		{"Hysteria 2", "github.com/apernet/hysteria/core/v2", "MIT", "https://github.com/apernet/hysteria"},
		{"quic-go", "github.com/apernet/quic-go", "MIT", "https://github.com/apernet/quic-go"},
		{"wireguard-go", "golang.zx2c4.com/wireguard", "MIT", "https://git.zx2c4.com/wireguard-go"},
		{"gVisor", "gvisor.dev/gvisor", "Apache-2.0", "https://github.com/google/gvisor"},
	}
	credits := []credit{}
	for _, k := range known {
		for _, d := range goDeps {
			if d.Path == k.module {
				credits = append(credits, credit{Name: k.name, Version: d.Version, License: k.license, Source: k.source})
				break
			}
		}
	}
	// Native components bundled into the Docker image but not tracked
	// by the Go module graph. Kept short and hand-maintained — these
	// are the pieces whose licences directly constrain the umbrella
	// licence of this project.
	natives := []native{
		{Name: "veepin (vendored, patched)", Version: "bcac178", License: "MIT", Source: "https://github.com/xen0bit/veepin"},
		{Name: "iptables (nf_tables backend)", Version: "distro-packaged", License: "GPL-2.0-or-later", Source: "https://www.netfilter.org/projects/iptables/"},
		{Name: "iptables-legacy (built from source)", Version: "1.8.3", License: "GPL-2.0-or-later", Source: "https://www.netfilter.org/projects/iptables/"},
		// Docker image only, used on iKuai routers, whose kernel only works
		// with iKuai's own iptables build (taken from iKuai firmware).
		{Name: "iptables for iKuai (from iKuai firmware)", Version: "libxtables 12", License: "GPL-2.0-or-later", Source: "https://www.netfilter.org/projects/iptables/"},
		{Name: "uClibc-ng / musl runtime for iKuai (from iKuai firmware)", Version: "1.0.40 / —", License: "LGPL-2.1 / MIT", Source: "https://uclibc-ng.org/"},
	}
	writeJSON(w, map[string]any{
		"version":     Version,
		"license":     "GPL-3.0-or-later",
		"repository":  "https://github.com/FrankoonG/hy2scale",
		"go_deps":     goDeps,
		"natives":     natives,
		"credits":     credits,
		"go_version":  goVersion,
		"platform":    runtime.GOOS + "/" + runtime.GOARCH,
		"commit":      commit,
		"commit_time": commitTime,
		"modified":    modified,
		"build":       buildsig.Self(),
	})
}

func (s *Server) uploadUpgrade(w http.ResponseWriter, r *http.Request) {
	if !isRunningInDocker() {
		http.Error(w, "upgrade via web is only available in Docker deployments", 403)
		return
	}
	// Limit to 200MB
	r.Body = http.MaxBytesReader(w, r.Body, 200<<20)

	tmp, err := os.CreateTemp("", "hy2scale-upload-*.tar.gz")
	if err != nil {
		http.Error(w, "temp file: "+err.Error(), 500)
		return
	}
	if _, err := io.Copy(tmp, r.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		http.Error(w, "save: "+err.Error(), 500)
		return
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	a, err := readUpgradeArchive(tmp.Name())
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := installUpgrade(a, s.app.DataDir(), r.URL.Query().Get("allow_unofficial") == "1"); err != nil {
		var ue unofficialError
		if errors.As(err, &ue) {
			writeUnofficial(w, ue)
			return
		}
		http.Error(w, err.Error(), 400)
		return
	}

	log.Printf("[upgrade] binary upgraded from upload, restarting...")
	writeJSON(w, map[string]string{"status": "ok", "message": "upgrade successful, restarting"})

	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

// upgradeArchive: the binary for this platform taken from an upgrade
// archive (a temp file), its signature file and the result of checking them.
type upgradeArchive struct {
	bin   string
	sig   []byte
	check buildsig.Result
}

// readUpgradeArchive takes the binary for this platform (and its signature
// file, if any) out of the gzipped tar at path. The binary is hashed as it
// is written, so the check is about exactly the bytes that get installed;
// an archive with more than one candidate binary or signature is refused.
func readUpgradeArchive(path string) (*upgradeArchive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("invalid gzip: %w", err)
	}
	defer gr.Close()

	plat := buildsig.Platform()
	names := map[string]bool{"hy2scale": true}
	for _, p := range []string{plat, runtime.GOOS + "-" + runtime.GOARCH} {
		names["hy2scale-"+p] = true
		names["hy2scale_"+p] = true
	}
	a := &upgradeArchive{}
	var sum []byte
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			a.remove()
			return nil, fmt.Errorf("invalid tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(hdr.Name)
		switch {
		case names[name]:
			if a.bin != "" {
				a.remove()
				return nil, fmt.Errorf("more than one binary for %s in the archive", plat)
			}
			tmp, err := os.CreateTemp("", "hy2scale-upgrade-*")
			if err != nil {
				return nil, fmt.Errorf("temp file: %w", err)
			}
			a.bin = tmp.Name()
			h := sha256.New()
			_, err = io.Copy(io.MultiWriter(tmp, h), tr)
			tmp.Close()
			if err != nil {
				a.remove()
				return nil, fmt.Errorf("extract: %w", err)
			}
			os.Chmod(a.bin, 0o755)
			sum = h.Sum(nil)
		case strings.HasSuffix(name, ".sig") && names[strings.TrimSuffix(name, ".sig")]:
			if a.sig != nil {
				a.remove()
				return nil, fmt.Errorf("more than one signature in the archive")
			}
			a.sig, err = io.ReadAll(io.LimitReader(tr, buildsig.MaxFile))
			if err != nil {
				a.remove()
				return nil, fmt.Errorf("extract: %w", err)
			}
		}
	}
	if a.bin == "" {
		return nil, fmt.Errorf("no binary for %s in the archive", plat)
	}
	a.check = buildsig.Check(a.sig, sum)
	return a, nil
}

func (a *upgradeArchive) remove() {
	if a.bin != "" {
		os.Remove(a.bin)
	}
}

// unofficialError: installing a binary that is not an official build on a
// node that runs one, without the user having accepted it.
type unofficialError struct{ r buildsig.Result }

func (e unofficialError) Error() string {
	if e.r.Reason != "" {
		return "not an official build: " + e.r.Reason
	}
	return "not an official build"
}

// writeUnofficial answers 409 with what the warning dialog shows.
func writeUnofficial(w http.ResponseWriter, e unofficialError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	json.NewEncoder(w).Encode(map[string]any{"error": "unofficial", "status": e.r.Status, "reason": e.r.Reason})
}

// installUpgrade installs the archive's binary. A node running an official
// build only takes one that is not official when allowUnofficial is set
// (the user accepted the warning). In a container the binary is stored in
// the data directory (see internal/selfupdate: the container's own
// filesystem is reset whenever it is recreated); otherwise it is swapped
// into place, leaving the previous binary as <self>.bak. Its signature file
// goes along with it. The caller triggers os.Exit so the process restarts
// on the new binary.
func installUpgrade(a *upgradeArchive, dataDir string, allowUnofficial bool) error {
	defer a.remove()
	if a.check.Status != buildsig.Official && buildsig.Self().Status == buildsig.Official && !allowUnofficial {
		return unofficialError{a.check}
	}
	var sig []byte
	want := ""
	if a.check.Status == buildsig.Official {
		sig, want = a.sig, a.check.Manifest.Version
	}
	log.Printf("[upgrade] installing a binary: %s %s", a.check.Status, a.check.Reason)
	if isRunningInDocker() {
		return selfupdate.Install(dataDir, a.bin, Version, sig, want)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find self: %w", err)
	}
	self, _ = filepath.EvalSymlinks(self)
	bakPath := self + ".bak"
	os.Remove(bakPath)
	if err := os.Rename(self, bakPath); err != nil {
		return fmt.Errorf("backup old binary: %w", err)
	}
	if err := os.Rename(a.bin, self); err != nil {
		os.Rename(bakPath, self)
		return fmt.Errorf("install new binary: %w", err)
	}
	os.Chmod(self, 0o755)
	if sig != nil {
		os.WriteFile(buildsig.SigPath(self), sig, 0o644)
	} else {
		os.Remove(buildsig.SigPath(self))
	}
	return nil
}

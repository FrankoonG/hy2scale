// Program shell: backup, restore, upgrade, restart.

package api

import (
	"archive/tar"
	"compress/gzip"
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

	if err := applyTarball(tmp.Name(), s.app.DataDir()); err != nil {
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

// applyTarball extracts the matching platform binary from a gzipped tar
// archive at path. In a container it is stored in the data directory (see
// internal/selfupdate: the container's own filesystem is reset whenever it
// is recreated); otherwise it is atomically swapped into place, leaving the
// previous binary as <self>.bak. Caller is responsible for triggering os.Exit so
// the process restarts on the new binary. Used by both the manual upload
// path (uploadUpgrade) and the online-update path
// (applyDownloadedUpgrade) so the two stay in lock-step.
func applyTarball(path, dataDir string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("invalid gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	expectedArch := runtime.GOOS + "-" + runtime.GOARCH
	expectedBin := "hy2scale"

	var tmpPath string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(hdr.Name)
		isMatch := name == expectedBin ||
			name == expectedBin+"-"+expectedArch ||
			name == expectedBin+"_"+expectedArch
		if !isMatch {
			continue
		}
		tmp, err := os.CreateTemp("", "hy2scale-upgrade-*")
		if err != nil {
			return fmt.Errorf("temp file: %w", err)
		}
		if _, err := io.Copy(tmp, tr); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return fmt.Errorf("extract: %w", err)
		}
		tmp.Close()
		os.Chmod(tmp.Name(), 0755)
		tmpPath = tmp.Name()
		break
	}
	if tmpPath == "" {
		return fmt.Errorf("no matching binary for %s found in archive", expectedArch)
	}

	if isRunningInDocker() {
		defer os.Remove(tmpPath)
		return selfupdate.Install(dataDir, tmpPath, Version)
	}

	self, err := os.Executable()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("cannot find self: %w", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	bakPath := self + ".bak"
	os.Remove(bakPath)
	if err := os.Rename(self, bakPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("backup old binary: %w", err)
	}
	if err := os.Rename(tmpPath, self); err != nil {
		os.Rename(bakPath, self)
		return fmt.Errorf("install new binary: %w", err)
	}
	os.Chmod(self, 0755)
	return nil
}

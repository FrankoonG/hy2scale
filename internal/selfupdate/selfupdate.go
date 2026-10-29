// Package selfupdate keeps an in-app upgrade across container recreation.
//
// In a container, the image's binary comes back whenever the container is
// recreated (a router app-package upgrade, compose down/up, docker rm). An
// in-app upgrade therefore stores the new binary in the data directory, the
// only place that persists, and every start hands over to it while it is
// newer than the image's own binary. Once the image catches up (a package
// upgrade to the same or a newer version), the stored binary is dropped.
package selfupdate

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	envHandoff = "HY2SCALE_PERSISTED" // set on the handed-over process
	binName    = "hy2scale"
	maxTries   = 3 // starts in a row that never reached Healthy
)

func binDir(dataDir string) string { return filepath.Join(dataDir, "bin") }

// Path is where an in-app upgrade stores the new binary.
func Path(dataDir string) string { return filepath.Join(binDir(dataDir), binName) }

func triesPath(dataDir string) string { return filepath.Join(binDir(dataDir), "starts") }

// Persisted reports whether this process is the stored binary.
func Persisted() bool { return os.Getenv(envHandoff) != "" }

// Handoff runs first thing at start. In the image's binary it execs the
// stored binary when that one is newer, and removes it when it is not. It
// returns when this process should simply continue.
func Handoff(dataDir, selfVersion string) {
	if Persisted() {
		return
	}
	p := Path(dataDir)
	if _, err := os.Stat(p); err != nil {
		return
	}
	v := versionOf(p)
	if !Newer(v, selfVersion) {
		log.Printf("[upgrade] stored binary %q is not newer than %s, removing it", v, selfVersion)
		os.RemoveAll(binDir(dataDir))
		return
	}
	tries, _ := strconv.Atoi(strings.TrimSpace(readFile(triesPath(dataDir))))
	if tries >= maxTries {
		log.Printf("[upgrade] stored binary %s failed to start %d times, keeping %s", v, tries, selfVersion)
		os.Rename(p, p+".failed")
		os.Remove(triesPath(dataDir))
		return
	}
	os.WriteFile(triesPath(dataDir), []byte(strconv.Itoa(tries+1)), 0644)
	log.Printf("[upgrade] handing over to stored binary %s (image has %s)", v, selfVersion)
	err := syscall.Exec(p, os.Args, append(os.Environ(), envHandoff+"=1"))
	log.Printf("[upgrade] exec %s: %v — continuing with %s", p, err, selfVersion)
}

// Healthy marks the stored binary's start as good (called once it has run
// for a while), so later restarts keep handing over to it.
func Healthy(dataDir string) {
	if Persisted() {
		os.Remove(triesPath(dataDir))
	}
}

// Install stores the binary at src as the in-app upgrade, atomically. It
// must be newer than current (the running version): anything else would be
// dropped again at the next start.
func Install(dataDir, src, current string) error {
	if err := os.MkdirAll(binDir(dataDir), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := Path(dataDir) + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	v := versionOf(tmp)
	if v == "" {
		os.Remove(tmp)
		return fmt.Errorf("the uploaded binary does not run here")
	}
	if !Newer(v, current) {
		os.Remove(tmp)
		return fmt.Errorf("version %s is not newer than the running %s", v, current)
	}
	os.Remove(Path(dataDir) + ".failed")
	os.Remove(triesPath(dataDir))
	return os.Rename(tmp, Path(dataDir))
}

// versionOf runs "<p> --version" ("hy2scale X.Y.Z") and returns X.Y.Z, or
// "" when the binary does not run here.
func versionOf(p string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "--version")
	cmd.Env = append(os.Environ(), envHandoff+"=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) != 2 || f[0] != binName {
		return ""
	}
	return f[1]
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// Newer compares two MAJOR.MINOR.PATCH strings and returns true iff a > b.
// A pre-release suffix on the patch field is ignored; anything malformed is
// "not newer".
func Newer(a, b string) bool {
	parse := func(s string) ([3]int, bool) {
		var v [3]int
		parts := strings.SplitN(strings.TrimPrefix(s, "v"), ".", 3)
		if len(parts) != 3 {
			return v, false
		}
		for i, p := range parts {
			if i == 2 {
				if dash := strings.IndexAny(p, "-+"); dash != -1 {
					p = p[:dash]
				}
			}
			n, err := strconv.Atoi(p)
			if err != nil {
				return v, false
			}
			v[i] = n
		}
		return v, true
	}
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

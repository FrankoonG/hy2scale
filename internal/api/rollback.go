package api

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/FrankoonG/hy2scale/internal/selfupdate"
)

// A config kept when an in-app upgrade kept crashing and the image's older
// binary took over (selfupdate.Handoff): that binary rewrote the config in
// its own form, dropping what it did not know. The kept file stays until it
// is restored or removed by hand; the node says so (log, /api/node).

type rollbackConfig struct {
	File    string `json:"file"`
	Version string `json:"version"`
	Time    int64  `json:"time"` // unix ms
}

func rollbackConfigs(dir string) []rollbackConfig {
	out := []rollbackConfig{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		v, ok := strings.CutPrefix(e.Name(), selfupdate.RollbackPrefix)
		if !ok || e.IsDir() || v == "" {
			continue
		}
		t := int64(0)
		if fi, err := e.Info(); err == nil {
			t = fi.ModTime().UnixMilli()
		}
		out = append(out, rollbackConfig{File: filepath.Join(dir, e.Name()), Version: v, Time: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}

// logRollbackConfigs tells at start about kept configs.
func (s *Server) logRollbackConfigs() {
	for _, r := range rollbackConfigs(s.app.DataDir()) {
		log.Printf("[upgrade] %s fell back to an older binary; its config is kept at %s (this config was written by %q): restore it by hand if anything is missing",
			r.Version, r.File, s.app.Store().LoadedBy())
	}
}

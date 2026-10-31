package geoip

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// meta.json: where each database came from (and its HTTP validators, for
// conditional requests) and the outcome of the last automatic check.

type fileMeta struct {
	Source       string `json:"source"`
	URL          string `json:"url,omitempty"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	Fetched      string `json:"fetched,omitempty"`
}

// source: files without a record were uploaded (before meta.json existed).
func (f *fileMeta) source() string {
	if f == nil || f.Source == "" {
		return SourceUpload
	}
	return f.Source
}

type autoMeta struct {
	Source     string `json:"source,omitempty"`
	LastCheck  string `json:"last_check,omitempty"`
	LastUpdate string `json:"last_update,omitempty"`
	Error      string `json:"error,omitempty"`
	Failed     bool   `json:"failed,omitempty"`
}

type meta struct {
	Files map[string]*fileMeta `json:"files"`
	Auto  autoMeta             `json:"auto"`
}

func (s *Store) metaPath() string { return filepath.Join(s.dir, "meta.json") }

func (s *Store) loadMeta() {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	s.meta = meta{}
	if b, err := os.ReadFile(s.metaPath()); err == nil {
		if err := json.Unmarshal(b, &s.meta); err != nil {
			log.Printf("[geoip] %s: %v", s.metaPath(), err)
			s.meta = meta{}
		}
	}
}

// saveMeta writes meta.json atomically; s.metaMu is held.
func (s *Store) saveMeta() {
	b, _ := json.MarshalIndent(s.meta, "", " ")
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return
	}
	f, err := os.CreateTemp(s.dir, ".upload-meta-*.json")
	if err != nil {
		log.Printf("[geoip] meta: %v", err)
		return
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.metaPath())
	}
	if err != nil {
		os.Remove(f.Name())
		log.Printf("[geoip] meta: %v", err)
	}
}

// fileMeta: the record of kind (nil = none); a copy.
func (s *Store) fileMeta(kind string) *fileMeta {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if f := s.meta.Files[kind]; f != nil {
		c := *f
		return &c
	}
	return nil
}

func (s *Store) setFileMeta(kind string, f *fileMeta) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if f == nil {
		if _, ok := s.meta.Files[kind]; !ok {
			return
		}
		delete(s.meta.Files, kind)
	} else {
		if s.meta.Files == nil {
			s.meta.Files = map[string]*fileMeta{}
		}
		c := *f
		s.meta.Files[kind] = &c
	}
	s.saveMeta()
}

func (s *Store) updateAutoMeta(fn func(*autoMeta)) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	fn(&s.meta.Auto)
	s.saveMeta()
}

func (s *Store) autoMeta() autoMeta {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	return s.meta.Auto
}

// Add-on GeoIP: local MaxMind-format databases the operator uploads
// (internal/geoip) and lookups against them.

package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/FrankoonG/hy2scale/internal/geoip"
)

// geoStore: the node's GeoIP databases (<data>/geoip).
var geoStore *geoip.Store

// maxLookupIPs: addresses one POST /api/geoip/lookup may ask about.
const maxLookupIPs = 512

// geoipRoutes: the routes of the GeoIP add-on.
func (s *Server) geoipRoutes(m *http.ServeMux) {
	if geoStore == nil {
		dir := "geoip"
		if d := s.app.DataDir(); d != "" {
			dir = filepath.Join(d, "geoip")
		}
		geoStore = geoip.New(dir)
		geoStore.Load()
	}
	m.HandleFunc("GET /api/geoip/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, geoStore.Status())
	})
	m.HandleFunc("POST /api/geoip/upload", geoUpload)
	m.HandleFunc("DELETE /api/geoip/{kind}", func(w http.ResponseWriter, r *http.Request) {
		kind := r.PathValue("kind")
		if !geoip.ValidKind(kind) {
			http.Error(w, "unknown database kind (city, country or asn)", http.StatusBadRequest)
			return
		}
		if err := geoStore.Remove(kind); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[geoip] %s database removed", kind)
		writeJSON(w, map[string]any{"ok": true})
	})
	m.HandleFunc("POST /api/geoip/lookup", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IPs  []string `json:"ips"`
			Lang string   `json:"lang"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body.IPs) > maxLookupIPs {
			http.Error(w, "too many addresses (at most 512)", http.StatusBadRequest)
			return
		}
		writeJSON(w, geoResults(body.IPs, body.Lang))
	})
	m.HandleFunc("GET /api/geoip/lookup", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(w, geoResults([]string{q.Get("ip")}, q.Get("lang")))
	})
}

func geoResults(ips []string, lang string) map[string]any {
	res := make(map[string]geoip.Result, len(ips))
	for _, ip := range ips {
		res[ip] = geoStore.LookupString(ip, lang)
	}
	return map[string]any{"results": res}
}

// geoUpload: POST /api/geoip/upload — the raw file, or multipart field
// "file".
func geoUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, geoip.MaxUpload)
	var src io.Reader = r.Body
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); strings.HasPrefix(mt, "multipart/") {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "invalid multipart body: "+err.Error(), http.StatusBadRequest)
			return
		}
		for src == r.Body {
			p, err := mr.NextPart()
			if err != nil {
				if !uploadTooLarge(w, err) {
					http.Error(w, `no "file" field in the upload`, http.StatusBadRequest)
				}
				return
			}
			if p.FormName() == "file" {
				src = p
			}
		}
	}
	info, err := geoStore.Install(src)
	if err != nil {
		if uploadTooLarge(w, err) {
			return
		}
		code := http.StatusInternalServerError
		if errors.Is(err, geoip.ErrInvalid) {
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]any{"kind": info.Kind, "type": info.Type, "build": info.Build})
}

func uploadTooLarge(w http.ResponseWriter, err error) bool {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) || errors.Is(err, geoip.ErrTooLarge) {
		http.Error(w, "file too large (at most 300 MB uploaded, 1 GB unpacked)", http.StatusRequestEntityTooLarge)
		return true
	}
	return false
}

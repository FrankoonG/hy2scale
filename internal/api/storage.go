// Storage settings (Settings → Storage), stored logs and audit records.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/FrankoonG/hy2scale/internal/app"
	"github.com/FrankoonG/hy2scale/internal/storage"
)

func (s *Server) storageRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/settings/storage", s.getStorage)
	m.HandleFunc("PUT /api/settings/storage", s.putStorage)
	m.HandleFunc("DELETE /api/storage/logs", s.wipeLogs)
	m.HandleFunc("DELETE /api/storage/audit", s.wipeAudit)
	m.HandleFunc("GET /api/audit", s.getAudit)
	m.HandleFunc("GET /api/audit/export", s.exportAudit)
	m.HandleFunc("GET /api/audit/stats", s.auditStats)
}

func (s *Server) getStorage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.app.StorageSettings())
}

func (s *Server) putStorage(w http.ResponseWriter, r *http.Request) {
	var p app.StoragePatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.app.SetStorage(p); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, s.app.StorageSettings())
}

func (s *Server) wipeLogs(w http.ResponseWriter, r *http.Request) {
	if err := s.app.WipeLogs(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) wipeAudit(w http.ResponseWriter, r *http.Request) {
	if err := s.app.WipeAudit(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// auditQuery reads the filters shared by the audit endpoints.
func auditQuery(r *http.Request) (storage.Query, error) {
	v := r.URL.Query()
	q := storage.Query{
		User: v.Get("user"), Src: v.Get("src"), Dst: v.Get("dst"), Inbound: v.Get("inbound"),
		Exit: v.Get("exit"), Proto: v.Get("proto"), Result: v.Get("result"), Q: v.Get("q"),
		Cursor: v.Get("cursor"),
	}
	for _, f := range []struct {
		key string
		dst *int64
	}{{"from", &q.From}, {"to", &q.To}} {
		if s := v.Get(f.key); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 0 {
				return q, fmt.Errorf("%s: unix milliseconds expected", f.key)
			}
			*f.dst = n
		}
	}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return q, errors.New("limit: a positive number expected")
		}
		q.Limit = n
	}
	return q, nil
}

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	q, err := auditQuery(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	res, err := s.app.AuditStore().Query(r.Context(), q)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{
		"enabled":     s.app.AuditStore().Enabled(),
		"records":     res.Records,
		"next_cursor": res.NextCursor,
		"scanned":     res.Scanned,
		"truncated":   res.Truncated,
	})
}

func (s *Server) auditStats(w http.ResponseWriter, r *http.Request) {
	q, err := auditQuery(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	res, err := s.app.AuditStore().Stats(r.Context(), q)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{
		"enabled":          s.app.AuditStore().Enabled(),
		"flows":            res.Flows,
		"up":               res.Up,
		"down":             res.Down,
		"top_users":        res.TopUsers,
		"top_sources":      res.TopSrc,
		"top_destinations": res.TopDst,
		"scanned":          res.Scanned,
		"truncated":        res.Truncated,
	})
}

// exportAudit streams the matching records (newest first, at most
// storage.ExportMaxRecords). The trailers X-Audit-Records and
// X-Audit-Truncated say how it ended.
func (s *Server) exportAudit(w http.ResponseWriter, r *http.Request) {
	q, err := auditQuery(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	q.Cursor = ""
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	name := "audit-" + time.Now().Format("20060102-150405")
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		name += ".csv"
	case "jsonl":
		w.Header().Set("Content-Type", "application/x-ndjson")
		name += ".jsonl"
	default:
		http.Error(w, "format: csv or jsonl", 400)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Trailer", "X-Audit-Records, X-Audit-Truncated")
	res, _ := s.app.AuditStore().Export(r.Context(), w, q, format)
	w.Header().Set("X-Audit-Records", strconv.Itoa(res.Records))
	w.Header().Set("X-Audit-Truncated", strconv.FormatBool(res.Truncated))
}

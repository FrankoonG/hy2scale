// Core: certificates.

package api

import (
	"encoding/json"
	"net/http"
	"os"
)

func (s *Server) listCerts(w http.ResponseWriter, r *http.Request) {
	store := s.app.TLS()
	if store == nil {
		writeJSON(w, []any{})
		return
	}
	certs, err := store.List()
	if err != nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, certs)
}

func (s *Server) importCert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.ID == "" || body.Cert == "" {
		http.Error(w, "id and cert required", 400)
		return
	}
	if body.Name == "" {
		body.Name = body.ID
	}
	if err := s.app.TLS().Import(body.ID, body.Name, body.Cert, body.Key); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) getCertPEM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	certPEM, _ := s.app.TLS().GetPEM(id)
	keyData, _ := os.ReadFile(s.app.TLS().KeyPath(id))
	writeJSON(w, map[string]string{"cert": certPEM, "key": string(keyData)})
}

func (s *Server) importCertFromPath(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		CertPath string `json:"cert_path"`
		KeyPath  string `json:"key_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.ID == "" || body.CertPath == "" {
		http.Error(w, "id and cert_path required", 400)
		return
	}
	if body.Name == "" {
		body.Name = body.ID
	}
	certData, err := os.ReadFile(body.CertPath)
	if err != nil {
		http.Error(w, "cannot read cert file: "+err.Error(), 400)
		return
	}
	var keyData []byte
	if body.KeyPath != "" {
		keyData, err = os.ReadFile(body.KeyPath)
		if err != nil {
			http.Error(w, "cannot read key file: "+err.Error(), 400)
			return
		}
	}
	if err := s.app.TLS().Import(body.ID, body.Name, string(certData), string(keyData)); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) generateCert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Domains []string `json:"domains"`
		Days    int      `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.ID == "" || len(body.Domains) == 0 {
		http.Error(w, "id and domains required", 400)
		return
	}
	if body.Name == "" {
		body.Name = body.ID
	}
	if body.Days <= 0 {
		body.Days = 365
	}
	if err := s.app.TLS().Generate(body.ID, body.Name, body.Domains, body.Days); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) signCertWithCA(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CAID string `json:"ca_id"`
		ID   string `json:"id"`
		Name string `json:"name"`
		CN   string `json:"cn"`
		Days int    `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.CAID == "" || body.ID == "" || body.CN == "" {
		http.Error(w, "ca_id, id, and cn required", 400)
		return
	}
	if body.Name == "" {
		body.Name = body.CN
	}
	if body.Days <= 0 {
		body.Days = 7300
	}
	if err := s.app.TLS().SignWithCA(body.CAID, body.ID, body.Name, body.CN, body.Days); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) deleteCert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.app.TLS().Delete(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

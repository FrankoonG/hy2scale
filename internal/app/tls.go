package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CertInfo describes a stored certificate.
type CertInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Subject    string `json:"subject"`
	Issuer     string `json:"issuer"`
	NotAfter   string `json:"not_after"`
	IsCA       bool   `json:"is_ca"`
	KeyFile    string `json:"key_file,omitempty"`
	CertFile   string `json:"cert_file,omitempty"`
	CAParentID string `json:"ca_parent_id,omitempty"`
}

// TLSStore manages certificates in the data directory.
type TLSStore struct {
	dir string // e.g. /data/tls
}

func NewTLSStore(dataDir string) *TLSStore {
	dir := filepath.Join(dataDir, "tls")
	os.MkdirAll(dir, 0755)
	return &TLSStore{dir: dir}
}

// List returns all certificates.
func (s *TLSStore) List() ([]CertInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var result []CertInfo
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".crt" {
			continue
		}
		id := e.Name()[:len(e.Name())-4]
		info, err := s.parseCert(id)
		if err != nil {
			continue
		}
		result = append(result, info)
	}
	return result, nil
}

// Get returns a single certificate's info.
func (s *TLSStore) Get(id string) (CertInfo, error) {
	return s.parseCert(id)
}

// GetPEM returns the certificate PEM content.
func (s *TLSStore) GetPEM(id string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, id+".crt"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Import saves a certificate (and optional key) from PEM content.
func (s *TLSStore) Import(id, name, certPEM, keyPEM string) error {
	// Validate cert PEM
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return fmt.Errorf("invalid certificate PEM")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return fmt.Errorf("invalid certificate: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, id+".crt"), []byte(certPEM), 0644); err != nil {
		return err
	}
	if keyPEM != "" {
		if err := os.WriteFile(filepath.Join(s.dir, id+".key"), []byte(keyPEM), 0600); err != nil {
			return err
		}
	}
	// Save display name
	os.WriteFile(filepath.Join(s.dir, id+".name"), []byte(name), 0644)
	return nil
}

// Generate creates a self-signed certificate.
func (s *TLSStore) Generate(id, name string, domains []string, days int) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domains[0]},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     domains,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	// Encode cert PEM
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	// Encode key PEM
	keyBytes, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return s.Import(id, name, string(certPEM), string(keyPEM))
}

// SignWithCA uses a CA certificate to sign a new server certificate.
func (s *TLSStore) SignWithCA(caID, newID, name, cn string, days int) error {
	// Load CA cert
	caCertData, err := os.ReadFile(filepath.Join(s.dir, caID+".crt"))
	if err != nil {
		return fmt.Errorf("CA cert not found: %w", err)
	}
	caKeyData, err := os.ReadFile(filepath.Join(s.dir, caID+".key"))
	if err != nil {
		return fmt.Errorf("CA private key not found: %w", err)
	}

	caCertBlock, _ := pem.Decode(caCertData)
	if caCertBlock == nil {
		return fmt.Errorf("invalid CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(caCertBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}
	if !caCert.IsCA {
		return fmt.Errorf("selected certificate is not a CA")
	}

	caKeyBlock, _ := pem.Decode(caKeyData)
	if caKeyBlock == nil {
		return fmt.Errorf("invalid CA key PEM")
	}
	caKey, err := x509.ParsePKCS8PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		// Try PKCS1 RSA
		caKey, err = x509.ParsePKCS1PrivateKey(caKeyBlock.Bytes)
		if err != nil {
			// Try EC
			caKey, err = x509.ParseECPrivateKey(caKeyBlock.Bytes)
			if err != nil {
				return fmt.Errorf("parse CA key: unsupported format")
			}
		}
	}

	// Generate new key for the server cert
	newKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	if days <= 0 {
		days = 7300 // 20 years
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &newKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("sign certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, _ := x509.MarshalECPrivateKey(newKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	// Store with a reference to the CA that signed it
	if err := s.Import(newID, name, string(certPEM), string(keyPEM)); err != nil {
		return err
	}
	// Save CA ID reference
	os.WriteFile(filepath.Join(s.dir, newID+".ca"), []byte(caID), 0644)
	return nil
}

// CAParent returns the CA ID that signed this cert, or empty string.
func (s *TLSStore) CAParent(id string) string {
	data, err := os.ReadFile(filepath.Join(s.dir, id+".ca"))
	if err != nil {
		return ""
	}
	return string(data)
}

// Delete removes a certificate.
func (s *TLSStore) Delete(id string) error {
	os.Remove(filepath.Join(s.dir, id+".crt"))
	os.Remove(filepath.Join(s.dir, id+".key"))
	os.Remove(filepath.Join(s.dir, id+".name"))
	os.Remove(filepath.Join(s.dir, id+".ca"))
	return nil
}

// CertPath returns the file path for a cert (for hy2 server config).
func (s *TLSStore) CertPath(id string) string {
	return filepath.Join(s.dir, id+".crt")
}

// KeyPath returns the file path for a key.
func (s *TLSStore) KeyPath(id string) string {
	return filepath.Join(s.dir, id+".key")
}

func (s *TLSStore) parseCert(id string) (CertInfo, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, id+".crt"))
	if err != nil {
		return CertInfo{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return CertInfo{}, fmt.Errorf("invalid PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return CertInfo{}, err
	}
	name := id
	if nameData, err := os.ReadFile(filepath.Join(s.dir, id+".name")); err == nil {
		name = string(nameData)
	}
	_, keyErr := os.Stat(filepath.Join(s.dir, id+".key"))
	info := CertInfo{
		ID:       id,
		Name:     name,
		Subject:  cert.Subject.CommonName,
		Issuer:   cert.Issuer.CommonName,
		NotAfter: cert.NotAfter.Format("2006-01-02"),
		IsCA:     cert.IsCA,
	}
	info.CertFile = filepath.Join(s.dir, id+".crt")
	if keyErr == nil {
		info.KeyFile = filepath.Join(s.dir, id+".key")
	}
	info.CAParentID = s.CAParent(id)
	return info, nil
}

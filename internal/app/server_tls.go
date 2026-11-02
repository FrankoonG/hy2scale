package app

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// ServerTLSPaths turns the hy2 server's certificate setting into file
// paths. It accepts a TLS store ID ("default") as well as a path. A path
// that does not exist but names a store certificate resolves to the store's
// files: UIs before 1.4.2 wrote /data/tls/<id>.crt whatever the data
// directory was, which broke installs outside Docker.
func (a *App) ServerTLSPaths(cert, key string) (string, string) {
	if cert == "" || a.tls == nil {
		return cert, key
	}
	id := ""
	if !strings.ContainsRune(cert, '/') {
		id = strings.TrimSuffix(cert, ".crt")
	} else if _, err := os.Stat(cert); err != nil {
		id = strings.TrimSuffix(filepath.Base(cert), ".crt")
	}
	if id == "" {
		return cert, key
	}
	if _, err := os.Stat(a.tls.CertPath(id)); err != nil {
		return cert, key
	}
	return a.tls.CertPath(id), a.tls.KeyPath(id)
}

// fixServerTLSPaths rewrites a stored server certificate path that points
// nowhere but names a store certificate (see ServerTLSPaths).
func (a *App) fixServerTLSPaths() {
	cfg := a.store.Get()
	if cfg.Server == nil {
		return
	}
	c, k := a.ServerTLSPaths(cfg.Server.TLSCert, cfg.Server.TLSKey)
	if c == cfg.Server.TLSCert && k == cfg.Server.TLSKey {
		return
	}
	log.Printf("[server] certificate %s → %s", cfg.Server.TLSCert, c)
	a.store.Update(func(cf *Config) {
		if cf.Server != nil {
			cf.Server.TLSCert, cf.Server.TLSKey = c, k
		}
	})
}

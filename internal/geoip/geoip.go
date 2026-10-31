// Package geoip answers geo questions about IP addresses from local
// MaxMind-format databases (.mmdb) that the operator uploads. Nothing is
// downloaded and nothing is bundled: without a database there is simply
// no geo information.
//
// Databases live in one directory as city.mmdb, country.mmdb and asn.mmdb
// (one per kind). A city database also answers country questions; every
// loaded database contributes what it knows to a lookup (city, then
// country, then asn). Databases are swapped in place while lookups run.
package geoip

import (
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

// Database kinds, in lookup order.
const (
	KindCity    = "city"
	KindCountry = "country"
	KindASN     = "asn"
)

// Kinds lists every kind in lookup order.
var Kinds = []string{KindCity, KindCountry, KindASN}

// ValidKind says whether k is a database kind.
func ValidKind(k string) bool { return k == KindCity || k == KindCountry || k == KindASN }

// ErrInvalid wraps every reason an uploaded file is not accepted.
var ErrInvalid = errors.New("invalid database")

// DBInfo describes one loaded database (GET /api/geoip/status).
type DBInfo struct {
	Kind      string   `json:"kind"`
	Type      string   `json:"type"`
	Build     string   `json:"build,omitempty"` // RFC 3339, UTC
	Size      int64    `json:"size"`
	Languages []string `json:"languages"`
	IPVersion int      `json:"ip_version"`
	Nodes     int      `json:"nodes"`
}

// Status is GET /api/geoip/status.
type Status struct {
	Available bool     `json:"available"`
	Databases []DBInfo `json:"databases"`
}

type db struct {
	r    *maxminddb.Reader
	info DBInfo
}

// Store holds the databases of one directory.
type Store struct {
	dir string

	mu  sync.RWMutex // dbs; held (read) for the whole of a lookup
	dbs map[string]*db

	install sync.Mutex // one upload / removal at a time
	cache   *lru
}

// New is the store of dir (created when needed); Load opens what is there.
func New(dir string) *Store {
	return &Store{dir: dir, dbs: map[string]*db{}, cache: newLRU(4096)}
}

// Dir is the store's directory.
func (s *Store) Dir() string { return s.dir }

// Path is the file of a kind.
func (s *Store) Path(kind string) string { return filepath.Join(s.dir, kind+".mmdb") }

// Load opens the databases found in the directory and removes leftovers of
// interrupted uploads. A file that does not open is logged and skipped.
func (s *Store) Load() {
	s.install.Lock()
	defer s.install.Unlock()
	if m, _ := filepath.Glob(filepath.Join(s.dir, ".upload-*")); len(m) > 0 {
		for _, f := range m {
			os.Remove(f)
		}
	}
	for _, k := range Kinds {
		p := s.Path(k)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		d, err := openDB(p)
		if err != nil {
			log.Printf("[geoip] %s: %v", p, err)
			continue
		}
		if d.info.Kind != k {
			log.Printf("[geoip] %s holds a %s database; ignored", p, d.info.Kind)
			d.r.Close()
			continue
		}
		s.swap(k, d)
		log.Printf("[geoip] loaded %s (%s, built %s)", p, d.info.Type, d.info.Build)
	}
}

// Close closes every database.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, d := range s.dbs {
		d.r.Close()
		delete(s.dbs, k)
	}
	s.cache.clear()
}

// Available says whether any database is loaded.
func (s *Store) Available() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.dbs) > 0
}

// Status lists the loaded databases.
func (s *Store) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{Databases: []DBInfo{}}
	for _, k := range Kinds {
		if d := s.dbs[k]; d != nil {
			st.Databases = append(st.Databases, d.info)
		}
	}
	st.Available = len(st.Databases) > 0
	return st
}

// swap installs d as kind (nil = remove) and closes the one it replaces
// once no lookup uses it.
func (s *Store) swap(kind string, d *db) {
	s.mu.Lock()
	old := s.dbs[kind]
	if d != nil {
		s.dbs[kind] = d
	} else {
		delete(s.dbs, kind)
	}
	s.cache.clear()
	s.mu.Unlock()
	if old != nil {
		old.r.Close() // no reader holds s.mu, so nothing uses old any more
	}
}

// Remove deletes the database of kind.
func (s *Store) Remove(kind string) error {
	if !ValidKind(kind) {
		return fmt.Errorf("unknown kind %q", kind)
	}
	s.install.Lock()
	defer s.install.Unlock()
	s.swap(kind, nil)
	if err := os.Remove(s.Path(kind)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// InstallFile validates the mmdb at tmp (a file in the store's directory),
// moves it to its kind's place and swaps it in. tmp is gone afterwards.
func (s *Store) InstallFile(tmp string) (DBInfo, error) {
	defer os.Remove(tmp)
	d, err := openDB(tmp)
	if err != nil {
		return DBInfo{}, err
	}
	kind := d.info.Kind
	d.r.Close()

	s.install.Lock()
	defer s.install.Unlock()
	dst := s.Path(kind)
	if runtime.GOOS == "windows" {
		// a mapped file cannot be replaced there
		s.swap(kind, nil)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return DBInfo{}, err
	}
	nd, err := openDB(dst)
	if err != nil {
		return DBInfo{}, err
	}
	s.swap(kind, nd)
	log.Printf("[geoip] installed %s (%s, built %s)", dst, nd.info.Type, nd.info.Build)
	return nd.info, nil
}

// openDB opens and checks one database file.
func openDB(path string) (*db, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: not a MaxMind DB file (%v)", ErrInvalid, err)
	}
	kind, err := detect(r)
	if err != nil {
		r.Close()
		return nil, err
	}
	md := r.Metadata
	info := DBInfo{Kind: kind, Type: md.DatabaseType, Languages: append([]string{}, md.Languages...),
		IPVersion: int(md.IPVersion), Nodes: int(md.NodeCount)}
	if md.BuildEpoch > 0 {
		info.Build = time.Unix(int64(md.BuildEpoch), 0).UTC().Format(time.RFC3339)
	}
	if fi, err := os.Stat(path); err == nil {
		info.Size = fi.Size()
	}
	return &db{r: r, info: info}, nil
}

// KindOfType is the kind a database_type names ("" = not recognised).
func KindOfType(t string) string {
	l := strings.ToLower(t)
	switch {
	case strings.Contains(l, "city"):
		return KindCity
	case strings.Contains(l, "country"):
		return KindCountry
	case strings.Contains(l, "asn"):
		return KindASN
	}
	return ""
}

// probeIPs: addresses every real database (and MaxMind's test databases)
// has a record for.
var probeIPs = []string{
	"8.8.8.8", "1.1.1.1", "9.9.9.9", "208.67.222.222", "81.2.69.142", "216.160.83.56", "89.160.20.112",
	"2.125.160.216", "2001:4860:4860::8888", "2606:4700:4700::1111", "2001:218::", "2a02:cf40::",
}

// detect finds the kind of an open database: by its database_type, or for
// other schemas by what its records hold. The test lookups also prove that
// the data section decodes.
func detect(r *maxminddb.Reader) (string, error) {
	byType := KindOfType(r.Metadata.DatabaseType)
	var seen fields
	found := false
	for _, s := range probeIPs {
		ip := netip.MustParseAddr(s)
		if ip.Is6() && r.Metadata.IPVersion == 4 {
			continue
		}
		var m map[string]any
		res := r.Lookup(ip)
		if err := res.Err(); err != nil {
			return "", fmt.Errorf("%w: lookup failed (%v)", ErrInvalid, err)
		}
		if !res.Found() {
			continue
		}
		if err := res.Decode(&m); err != nil {
			return "", fmt.Errorf("%w: record does not decode (%v)", ErrInvalid, err)
		}
		found = true
		seen.add(decodeRecord(m, append([]string{"en"}, r.Metadata.Languages...)))
	}
	if byType != "" {
		return byType, nil
	}
	if !found {
		return "", fmt.Errorf("%w: unsupported database type %q", ErrInvalid, r.Metadata.DatabaseType)
	}
	switch {
	case seen.city:
		return KindCity, nil
	case seen.country:
		return KindCountry, nil
	case seen.asn:
		return KindASN, nil
	}
	return "", fmt.Errorf("%w: database type %q has no city, country or ASN data", ErrInvalid, r.Metadata.DatabaseType)
}

type fields struct{ city, country, asn bool }

func (f *fields) add(r Result) {
	f.city = f.city || r.City != "" || r.Lat != nil
	f.country = f.country || r.Country != ""
	f.asn = f.asn || r.ASN != 0
}

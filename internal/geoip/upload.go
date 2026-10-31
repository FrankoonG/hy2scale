package geoip

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// MaxUpload is the largest upload accepted (as sent, compressed or not).
const MaxUpload = 300 << 20

// MaxDatabase is the largest database accepted once unpacked.
const MaxDatabase = 1 << 30

// ErrTooLarge: the upload or the database in it exceeds the limits.
var ErrTooLarge = errors.New("file too large")

// Install reads a database as distributed — a bare .mmdb, a .mmdb.gz, or
// a .tar.gz / .tgz (or plain .tar) holding one .mmdb, told apart by their
// content — into a temporary file of the store's directory, validates it
// and swaps it in as the kind it is.
func (s *Store) Install(r io.Reader) (DBInfo, error) {
	return s.installStream(r, "", fileMeta{Source: SourceUpload})
}

// installStream is Install of a database that must be of kind want ("" =
// any), recorded with fm.
func (s *Store) installStream(r io.Reader, want string, fm fileMeta) (DBInfo, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return DBInfo{}, err
	}
	f, err := os.CreateTemp(s.dir, ".upload-*.mmdb")
	if err != nil {
		return DBInfo{}, err
	}
	tmp := f.Name()
	err = unpack(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return DBInfo{}, err
	}
	return s.installTemp(tmp, want, fm)
}

// unpack writes the database in r to w.
func unpack(w io.Writer, r io.Reader) error {
	br := bufio.NewReaderSize(r, 64<<10)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("%w: bad gzip data (%v)", ErrInvalid, err)
		}
		defer gz.Close()
		gz.Multistream(false)
		br = bufio.NewReaderSize(gz, 64<<10)
	}
	if hdr, _ := br.Peek(263); len(hdr) == 263 && bytes.Equal(hdr[257:262], []byte("ustar")) {
		return fromTar(w, br)
	}
	return copyLimited(w, br)
}

// fromTar copies the first .mmdb of a tar archive.
func fromTar(w io.Writer, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%w: the archive holds no .mmdb file", ErrInvalid)
		}
		if err != nil {
			return fmt.Errorf("%w: bad archive (%v)", ErrInvalid, err)
		}
		if h.Typeflag == tar.TypeReg && strings.HasSuffix(strings.ToLower(h.Name), ".mmdb") {
			return copyLimited(w, tr)
		}
	}
}

func copyLimited(w io.Writer, r io.Reader) error {
	n, err := io.Copy(w, io.LimitReader(r, MaxDatabase+1))
	if err != nil {
		if errors.Is(err, gzip.ErrChecksum) || errors.Is(err, gzip.ErrHeader) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: truncated or corrupt data (%v)", ErrInvalid, err)
		}
		return err
	}
	if n > MaxDatabase {
		return ErrTooLarge
	}
	if n == 0 {
		return fmt.Errorf("%w: empty file", ErrInvalid)
	}
	return nil
}

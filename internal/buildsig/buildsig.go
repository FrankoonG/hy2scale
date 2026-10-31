// Package buildsig tells official builds from others. The release workflow
// of the project's repository signs every binary it publishes: a small
// manifest (version, platform, sha256 of the binary, commit, tag) is signed
// with an Ed25519 key that only that workflow holds, and the signature file
// "<binary>.sig" ships next to the binary (in the release archive, the
// Docker image, and wherever an upgrade stores it). The public keys are
// below; a binary whose bytes match a manifest signed by one of them is
// official.
//
// This only informs: a node running an official build warns before a
// binary that is not official is installed on it, and the user decides.
package buildsig

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

// keys: the release signing keys, by id (a new key gets a new id; old ids
// stay so older releases keep verifying).
var keys = map[string]string{
	"hy2scale-1": "qacIdvM9YVk+SbRNEsSiXqTyxTizsKKlJm06Z1KyYlc=",
}

// ExtraKey is an additional trusted key "id:base64", set only at build time
// (-ldflags -X) for test builds. Release builds never set it, so it changes
// nothing on an official node.
var ExtraKey string

// Manifest is what a release signature covers.
type Manifest struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Platform string `json:"platform"` // e.g. linux-amd64, linux-armv7
	SHA256   string `json:"sha256"`
	Commit   string `json:"commit"`
	Tag      string `json:"tag"`
}

// File is the signature file: the manifest's exact bytes and their signature.
type File struct {
	Key      string `json:"key"`
	Manifest string `json:"manifest"` // base64 of the manifest JSON
	Sig      string `json:"sig"`      // base64 Ed25519 signature of those bytes
}

// Status of a binary.
type Status string

const (
	Official Status = "official" // signed by a release key, bytes match
	Unsigned Status = "unsigned" // no signature
	Invalid  Status = "invalid"  // a signature that does not hold for these bytes
)

// Result of a check.
type Result struct {
	Status   Status    `json:"status"`
	Manifest *Manifest `json:"manifest,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}

// MaxFile bounds a signature file read from an archive.
const MaxFile = 16 << 10

// Platform is this build's release platform name: GOOS-GOARCH, with the ARM
// level (linux-armv7) or the MIPS float mode (linux-mipsle-softfloat).
func Platform() string { return platform() }

var platform = sync.OnceValue(func() string {
	p := runtime.GOOS + "-" + runtime.GOARCH
	settings := map[string]string{}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			settings[s.Key] = s.Value
		}
	}
	switch runtime.GOARCH {
	case "arm":
		v := settings["GOARM"]
		if v == "" {
			v = "7"
		}
		p += "v" + strings.SplitN(v, ",", 2)[0]
	case "mips", "mipsle":
		if settings["GOMIPS"] == "softfloat" {
			p += "-softfloat"
		}
	}
	return p
})

func trusted(id string) ed25519.PublicKey {
	b64 := keys[id]
	if i := strings.IndexByte(ExtraKey, ':'); i > 0 && ExtraKey[:i] == id {
		b64 = ExtraKey[i+1:]
	}
	k, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(k) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(k)
}

// Check verifies a signature file (nil: none) against the sha256 of the
// binary's exact bytes and this node's platform.
func Check(sig []byte, sum []byte) Result {
	if len(sig) == 0 {
		return Result{Status: Unsigned}
	}
	bad := func(why string) Result { return Result{Status: Invalid, Reason: why} }
	var f File
	if err := json.Unmarshal(sig, &f); err != nil {
		return bad("unreadable signature")
	}
	k := trusted(f.Key)
	if k == nil {
		return bad("unknown signing key " + f.Key)
	}
	mb, err1 := base64.StdEncoding.DecodeString(f.Manifest)
	sb, err2 := base64.StdEncoding.DecodeString(f.Sig)
	if err1 != nil || err2 != nil {
		return bad("unreadable signature")
	}
	if !ed25519.Verify(k, mb, sb) {
		return bad("signature does not verify")
	}
	var m Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return bad("unreadable manifest")
	}
	if !strings.EqualFold(m.SHA256, hex.EncodeToString(sum)) {
		return bad("the binary is not the signed one")
	}
	if m.Platform != Platform() {
		return bad("signed for " + m.Platform)
	}
	return Result{Status: Official, Manifest: &m}
}

// SigPath is where the signature of the binary at p lives.
func SigPath(p string) string { return p + ".sig" }

// CheckFile checks the binary at p against its signature file next to it.
func CheckFile(p string) Result {
	f, err := os.Open(p)
	if err != nil {
		return Result{Status: Unsigned, Reason: err.Error()}
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Result{Status: Unsigned, Reason: err.Error()}
	}
	sig, err := os.ReadFile(SigPath(p))
	if err != nil {
		return Result{Status: Unsigned}
	}
	return Check(sig, h.Sum(nil))
}

// Self is the status of the running binary (checked once).
func Self() Result { return self() }

var self = sync.OnceValue(func() Result {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return Result{Status: Unsigned, Reason: err.Error()}
	}
	return CheckFile(exe)
})

// ErrUnofficial: an install that would replace an official build with one
// that is not, without the user having accepted that.
var ErrUnofficial = errors.New("not an official build")

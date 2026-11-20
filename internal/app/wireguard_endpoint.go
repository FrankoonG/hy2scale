package app

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Where WireGuard peers connect from (their current endpoint), for audit
// records: read from the device's configuration, at most every few seconds.

var wgEndpoints struct {
	mu   sync.Mutex
	at   time.Time
	byPK map[string]string // public key (hex) → endpoint
}

const wgEndpointsTTL = 5 * time.Second

// wgEndpoint: the endpoint of the WireGuard peer that owns tunnel address
// srcIP ("" = unknown).
func (a *App) wgEndpoint(srcIP string) string {
	cfg := a.store.Get().WireGuard
	if cfg == nil {
		return ""
	}
	p := findWGPeer(*cfg, srcIP)
	if p == nil {
		return ""
	}
	pk, err := base64.StdEncoding.DecodeString(p.PublicKey)
	if err != nil {
		return ""
	}
	key := hex.EncodeToString(pk)
	wgEndpoints.mu.Lock()
	defer wgEndpoints.mu.Unlock()
	if time.Since(wgEndpoints.at) > wgEndpointsTTL {
		wgEndpoints.byPK = readWGEndpoints()
		wgEndpoints.at = time.Now()
	}
	return wgEndpoints.byPK[key]
}

func readWGEndpoints() map[string]string {
	out := map[string]string{}
	wgMu.Lock()
	inst := wgRunning
	wgMu.Unlock()
	if inst == nil || inst.dev == nil {
		return out
	}
	conf, err := inst.dev.IpcGet()
	if err != nil {
		return out
	}
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(conf))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "public_key":
			cur = v
		case "endpoint":
			if cur != "" {
				out[cur] = v
			}
		}
	}
	return out
}

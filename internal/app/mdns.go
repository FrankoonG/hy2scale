package app

// The LAN name: this node answers <name>.local by mDNS on its LAN
// interfaces (internal/mdns).

import (
	"log"
	"os"
	"sync"

	"github.com/FrankoonG/hy2scale/core/netfilter"
	"github.com/FrankoonG/hy2scale/internal/mdns"
)

// MDNSConfig: the LAN name. Nil = on, as hy2scale.local.
type MDNSConfig struct {
	Disabled bool   `yaml:"disabled,omitempty" json:"disabled"`
	Name     string `yaml:"name,omitempty" json:"name"` // label without ".local"; "" = "hy2scale"
}

// onLAN: this process sees the host's own LAN (not a container's bridge
// network). HY2SCALE_LAN=1 says so where the detection cannot tell.
func onLAN() bool {
	if os.Getenv("HY2SCALE_LAN") == "1" {
		return true
	}
	return netfilter.CheckHostNetwork()
}

var mdnsState struct {
	sync.Mutex
	r     *mdns.Responder
	label string
}

// MDNSLabel: the label the LAN name is claimed from.
func (a *App) MDNSLabel() string {
	cfg := a.store.Get()
	name := ""
	if cfg.MDNS != nil {
		name = cfg.MDNS.Name
	}
	if l := mdns.SanitizeLabel(name); l != "" {
		return l
	}
	return "hy2scale"
}

// MDNSStatus: the name claimed ("" = none) and why there is none.
func (a *App) MDNSStatus() (name, state string) {
	cfg := a.store.Get()
	switch {
	case cfg.MDNS != nil && cfg.MDNS.Disabled:
		return "", "off"
	case !onLAN():
		return "", "no_lan"
	}
	mdnsState.Lock()
	defer mdnsState.Unlock()
	if mdnsState.r == nil {
		return "", "failed"
	}
	if n := mdnsState.r.Name(); n != "" {
		return n, "on"
	}
	return "", "probing"
}

// ApplyMDNS starts, restarts or stops the LAN name after a change.
func (a *App) ApplyMDNS() {
	cfg := a.store.Get()
	want := ""
	if (cfg.MDNS == nil || !cfg.MDNS.Disabled) && onLAN() {
		want = a.MDNSLabel()
	}
	mdnsState.Lock()
	defer mdnsState.Unlock()
	if mdnsState.r != nil && mdnsState.label == want {
		return
	}
	if mdnsState.r != nil {
		mdnsState.r.Close()
		mdnsState.r, mdnsState.label = nil, ""
	}
	if want == "" {
		return
	}
	r, err := mdns.Start(mdns.Config{Label: want, Skip: VirtualIface, Logf: func(f string, v ...any) { log.Printf("[mdns] "+f, v...) }})
	if err != nil {
		log.Printf("[mdns] %v", err)
		return
	}
	mdnsState.r, mdnsState.label = r, want
}

// stopMDNS says goodbye on shutdown.
func stopMDNS() {
	mdnsState.Lock()
	defer mdnsState.Unlock()
	if mdnsState.r != nil {
		mdnsState.r.Close()
		mdnsState.r = nil
	}
}

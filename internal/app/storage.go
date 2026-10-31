package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/FrankoonG/hy2scale/core/audit"
	"github.com/FrankoonG/hy2scale/core/logbuf"
	"github.com/FrankoonG/hy2scale/internal/storage"
)

// Settings → Storage: what this node keeps on disk. Logs (the Logs page,
// <data>/logs) and audit records (<data>/audit) each have a switch, off by
// default — also for configs written before the block existed. Off means
// nothing is recorded or kept on this node; process stdout/stderr logging
// is unaffected.

// StorageConfig is the `storage` block of the config.
type StorageConfig struct {
	Logs  StorageKind `yaml:"logs" json:"logs"`
	Audit StorageKind `yaml:"audit" json:"audit"`
}

// StorageKind: one kind of stored data. Zero limits mean the defaults.
type StorageKind struct {
	Enabled       bool `yaml:"enabled" json:"enabled"`
	RetentionDays int  `yaml:"retention_days,omitempty" json:"retention_days,omitempty"`
	MaxMB         int  `yaml:"max_mb,omitempty" json:"max_mb,omitempty"`
}

// Defaults and bounds of the storage limits.
const (
	LogsDefaultDays  = 7
	LogsDefaultMB    = 100
	AuditDefaultDays = 30
	AuditDefaultMB   = 512
	StorageMinDays   = 1
	StorageMaxDays   = 3650
	StorageMinMB     = 1
	StorageMaxMB     = 102400
)

func (k StorageKind) withDefaults(days, mb int) StorageKind {
	if k.RetentionDays <= 0 {
		k.RetentionDays = days
	}
	if k.MaxMB <= 0 {
		k.MaxMB = mb
	}
	return k
}

// EffectiveStorage: the storage settings with defaults filled in.
func EffectiveStorage(c *StorageConfig) StorageConfig {
	var s StorageConfig
	if c != nil {
		s = *c
	}
	s.Logs = s.Logs.withDefaults(LogsDefaultDays, LogsDefaultMB)
	s.Audit = s.Audit.withDefaults(AuditDefaultDays, AuditDefaultMB)
	return s
}

// StorageKindPatch is a partial update of one kind (nil = unchanged).
type StorageKindPatch struct {
	Enabled       *bool `json:"enabled"`
	RetentionDays *int  `json:"retention_days"`
	MaxMB         *int  `json:"max_mb"`
}

// StoragePatch is a partial update of the storage settings.
type StoragePatch struct {
	Logs  *StorageKindPatch `json:"logs"`
	Audit *StorageKindPatch `json:"audit"`
}

func (p *StorageKindPatch) apply(k *StorageKind, what string) error {
	if p == nil {
		return nil
	}
	if p.RetentionDays != nil {
		if d := *p.RetentionDays; d < StorageMinDays || d > StorageMaxDays {
			return fmt.Errorf("%s.retention_days must be %d..%d", what, StorageMinDays, StorageMaxDays)
		}
	}
	if p.MaxMB != nil {
		if m := *p.MaxMB; m < StorageMinMB || m > StorageMaxMB {
			return fmt.Errorf("%s.max_mb must be %d..%d", what, StorageMinMB, StorageMaxMB)
		}
	}
	if p.Enabled != nil {
		k.Enabled = *p.Enabled
	}
	if p.RetentionDays != nil {
		k.RetentionDays = *p.RetentionDays
	}
	if p.MaxMB != nil {
		k.MaxMB = *p.MaxMB
	}
	return nil
}

// stores: the node's log and audit stores.
type stores struct {
	logs  *storage.LogStore
	audit *storage.AuditStore
	mu    sync.Mutex // applying settings
}

func (a *App) initStorage() {
	a.st = &stores{
		logs:  storage.NewLogStore(filepath.Join(a.dataDir, "logs")),
		audit: storage.NewAuditStore(filepath.Join(a.dataDir, "audit")),
	}
	// sequence numbers continue after what is on disk
	logbuf.Default.SetSeq(a.st.logs.LastSeq())
	logbuf.Default.SetSink(a.st.logs.Sink)
	audit.SetIfaceResolver(a.ifaceOfIP)
	audit.SetSink(a.st.audit.Submit)
	a.applyStorage()
}

// applyStorage makes the configured settings effective.
func (a *App) applyStorage() {
	a.st.mu.Lock()
	defer a.st.mu.Unlock()
	s := EffectiveStorage(a.store.Get().Storage)
	a.st.logs.Segments().SetSegmentMax(storage.SegmentMaxFor(int64(s.Logs.MaxMB) << 20))
	a.st.audit.Segments().SetSegmentMax(storage.SegmentMaxFor(int64(s.Audit.MaxMB) << 20))
	if s.Logs.Enabled && !logbuf.Default.Enabled() {
		logbuf.Default.SetSeq(a.st.logs.LastSeq())
	}
	logbuf.Default.SetEnabled(s.Logs.Enabled)
	a.st.audit.SetEnabled(s.Audit.Enabled)
}

func (a *App) pruneLogs() {
	s := EffectiveStorage(a.store.Get().Storage)
	a.st.logs.Segments().Prune(s.Logs.RetentionDays, int64(s.Logs.MaxMB)<<20, time.Now())
}

func (a *App) pruneAudit() {
	s := EffectiveStorage(a.store.Get().Storage)
	a.st.audit.Segments().Prune(s.Audit.RetentionDays, int64(s.Audit.MaxMB)<<20, time.Now())
}

// runStorage runs the writers and retention until ctx ends.
func (a *App) runStorage(ctx context.Context) {
	a.persisters.Add(2)
	go func() { defer a.persisters.Done(); a.st.logs.Run(ctx, a.pruneLogs) }()
	go func() { defer a.persisters.Done(); a.st.audit.Run(ctx, a.pruneAudit) }()
}

// StorageKindView is one kind as the API shows it.
type StorageKindView struct {
	Enabled       bool   `json:"enabled"`
	RetentionDays int    `json:"retention_days"`
	MaxMB         int    `json:"max_mb"`
	UsedBytes     int64  `json:"used_bytes"`
	Dropped       *int64 `json:"dropped,omitempty"`
}

// StorageView: GET /api/settings/storage.
type StorageView struct {
	Logs  StorageKindView `json:"logs"`
	Audit StorageKindView `json:"audit"`
}

// StorageSettings reports the settings and the space in use.
func (a *App) StorageSettings() StorageView {
	s := EffectiveStorage(a.store.Get().Storage)
	dropped := a.st.audit.Dropped()
	return StorageView{
		Logs: StorageKindView{Enabled: s.Logs.Enabled, RetentionDays: s.Logs.RetentionDays, MaxMB: s.Logs.MaxMB,
			UsedBytes: a.st.logs.Segments().Used()},
		Audit: StorageKindView{Enabled: s.Audit.Enabled, RetentionDays: s.Audit.RetentionDays, MaxMB: s.Audit.MaxMB,
			UsedBytes: a.st.audit.Segments().Used(), Dropped: &dropped},
	}
}

// SetStorage applies a partial update; it takes effect at once.
func (a *App) SetStorage(p StoragePatch) error {
	cur := EffectiveStorage(a.store.Get().Storage)
	if err := p.Logs.apply(&cur.Logs, "logs"); err != nil {
		return err
	}
	if err := p.Audit.apply(&cur.Audit, "audit"); err != nil {
		return err
	}
	if err := a.store.Update(func(c *Config) { s := cur; c.Storage = &s }); err != nil {
		return err
	}
	a.applyStorage()
	a.pruneLogs()
	a.pruneAudit()
	log.Printf("[storage] logs %s (%d days, %d MB), audit %s (%d days, %d MB)",
		onOff(cur.Logs.Enabled), cur.Logs.RetentionDays, cur.Logs.MaxMB,
		onOff(cur.Audit.Enabled), cur.Audit.RetentionDays, cur.Audit.MaxMB)
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// WipeLogs deletes the stored log lines (the live view too).
func (a *App) WipeLogs() error {
	err := a.st.logs.Wipe(logbuf.Default.Last())
	logbuf.Default.Clear()
	return err
}

// WipeAudit deletes the stored audit records.
func (a *App) WipeAudit() error { return a.st.audit.Wipe() }

// LogStore: the stored log lines.
func (a *App) LogStore() *storage.LogStore { return a.st.logs }

// AuditStore: the stored audit records.
func (a *App) AuditStore() *storage.AuditStore { return a.st.audit }

// ---------------------------------------------------------------- egress interface lookup

// ifaceTable caches which local interface owns which address.
type ifaceTable struct {
	mu sync.Mutex
	at time.Time
	m  map[string]string
}

var localIfaces ifaceTable

// ifaceOfIP: the local interface whose address is ip: a system interface,
// else (a userspace child interface's address) the egress selected for that
// family when it is a child interface.
func (a *App) ifaceOfIP(ip net.IP) string {
	if ip == nil || ip.IsUnspecified() {
		return ""
	}
	key := ip.String()
	t := &localIfaces
	t.mu.Lock()
	if t.m == nil || time.Since(t.at) > 30*time.Second {
		m := map[string]string{}
		if ifs, err := net.Interfaces(); err == nil {
			for _, ifi := range ifs {
				addrs, _ := ifi.Addrs()
				for _, ad := range addrs {
					if ipn, ok := ad.(*net.IPNet); ok {
						m[ipn.IP.String()] = ifi.Name
					}
				}
			}
		}
		t.m, t.at = m, time.Now()
	}
	name := t.m[key]
	t.mu.Unlock()
	if name != "" {
		return name
	}
	sel := a.ifaces.Selection()
	fam := sel.IPv4
	if ip.To4() == nil {
		fam = sel.IPv6
	}
	if fam != "" && a.ifaces.IsChild(fam) {
		return fam
	}
	return ""
}

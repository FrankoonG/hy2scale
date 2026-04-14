package app

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Device represents an active client device, aggregated by (username, IP, protocol).
// Multiple TCP connections from the same device are counted, not listed separately.
type Device struct {
	Key       string    // username|ip|protocol
	Username  string
	RemoteIP  string
	Protocol  string // "socks5", "ss", "hy2", "l2tp", "ikev2"
	ConnectAt time.Time
	ConnCount atomic.Int32  // number of active TCP connections
	TxBytes   atomic.Int64
	RxBytes   atomic.Int64
	cancels   sync.Map // connID → cancel func
}

type DeviceJSON struct {
	Key       string `json:"key"`
	Username  string `json:"username"`
	RemoteIP  string `json:"remote_ip"`
	Protocol  string `json:"protocol"`
	ConnectAt int64  `json:"connect_at"`
	Duration  int    `json:"duration"`
	ConnCount int    `json:"conn_count"`
	TxBytes   int64  `json:"tx_bytes"`
	RxBytes   int64  `json:"rx_bytes"`
}

// SessionManager tracks active devices across proxy protocols.
// WireGuard is excluded — it has its own peer management.
type SessionManager struct {
	mu      sync.RWMutex
	devices map[string]*Device // key → device
	blocked sync.Map           // key → unblock time
	seq     atomic.Int64
}

func NewSessionManager() *SessionManager {
	return &SessionManager{devices: make(map[string]*Device)}
}

// deviceKey creates a unique key for a device.
func deviceKey(username, remoteIP, protocol string) string {
	return username + "|" + remoteIP + "|" + protocol
}

// IsBlocked checks if a device is temporarily blocked (kicked).
func (m *SessionManager) IsBlocked(username, remoteIP, protocol string) bool {
	key := deviceKey(username, remoteIP, protocol)
	if v, ok := m.blocked.Load(key); ok {
		if time.Now().Before(v.(time.Time)) {
			return true
		}
		m.blocked.Delete(key)
	}
	return false
}

// Connect registers a new connection from a device. Returns a connID for Disconnect.
// Returns empty string if blocked.
func (m *SessionManager) Connect(username, remoteIP, protocol string, cancel func()) string {
	key := deviceKey(username, remoteIP, protocol)

	// Check if kicked/blocked
	if v, ok := m.blocked.Load(key); ok {
		if time.Now().Before(v.(time.Time)) {
			if cancel != nil {
				cancel() // immediately reject
			}
			return ""
		}
		m.blocked.Delete(key)
	}

	connID := itoa(int(m.seq.Add(1)))

	m.mu.Lock()
	dev, ok := m.devices[key]
	if !ok {
		dev = &Device{
			Key:       key,
			Username:  username,
			RemoteIP:  remoteIP,
			Protocol:  protocol,
			ConnectAt: time.Now(),
		}
		m.devices[key] = dev
	}
	m.mu.Unlock()

	dev.ConnCount.Add(1)
	if cancel != nil {
		dev.cancels.Store(connID, cancel)
	}
	return key + "#" + connID
}

// Disconnect removes a connection. If the device has no more connections, it's removed.
func (m *SessionManager) Disconnect(sessionID string, txBytes, rxBytes int64) {
	if sessionID == "" {
		return // blocked connection
	}
	parts := splitSessionID(sessionID)
	if len(parts) != 2 {
		return
	}
	key, connID := parts[0], parts[1]

	m.mu.RLock()
	dev, ok := m.devices[key]
	m.mu.RUnlock()
	if !ok {
		return
	}

	dev.TxBytes.Add(txBytes)
	dev.RxBytes.Add(rxBytes)
	dev.cancels.Delete(connID)
	remaining := dev.ConnCount.Add(-1)

	if remaining <= 0 {
		m.mu.Lock()
		delete(m.devices, key)
		m.mu.Unlock()
	}
}

// Kick disconnects all connections for a device and blocks reconnection for 60s.
func (m *SessionManager) Kick(key string) bool {
	m.mu.RLock()
	dev, ok := m.devices[key]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	// Block for 60 seconds
	m.blocked.Store(key, time.Now().Add(60*time.Second))
	// Cancel all active connections
	dev.cancels.Range(func(k, v any) bool {
		if cancel, ok := v.(func()); ok {
			cancel()
		}
		return true
	})
	m.mu.Lock()
	delete(m.devices, key)
	m.mu.Unlock()
	return true
}

// KickUser disconnects all active sessions for a given username.
func (m *SessionManager) KickUser(username string) int {
	m.mu.RLock()
	var keys []string
	for key := range m.devices {
		if strings.HasPrefix(key, username+"|") {
			keys = append(keys, key)
		}
	}
	m.mu.RUnlock()
	for _, key := range keys {
		m.Kick(key)
	}
	return len(keys)
}

// List returns all active devices.
func (m *SessionManager) List() []DeviceJSON {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now()
	result := make([]DeviceJSON, 0, len(m.devices))
	for _, d := range m.devices {
		result = append(result, DeviceJSON{
			Key:       d.Key,
			Username:  d.Username,
			RemoteIP:  d.RemoteIP,
			Protocol:  d.Protocol,
			ConnectAt: d.ConnectAt.Unix(),
			Duration:  int(now.Sub(d.ConnectAt).Seconds()),
			ConnCount: int(d.ConnCount.Load()),
			TxBytes:   d.TxBytes.Load(),
			RxBytes:   d.RxBytes.Load(),
		})
	}
	return result
}

// Count returns total active devices.
func (m *SessionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.devices)
}

func splitSessionID(id string) []string {
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '#' {
			return []string{id[:i], id[i+1:]}
		}
	}
	return nil
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

package app

import (
	"sync"
	"sync/atomic"
	"time"
)

// Session represents an active client connection.
type Session struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	RemoteIP  string    `json:"remote_ip"`
	Protocol  string    `json:"protocol"` // "hy2", "socks5", "ss", "l2tp", "ikev2", "wireguard"
	ConnectAt time.Time `json:"connect_at"`
	TxBytes   atomic.Int64 `json:"-"`
	RxBytes   atomic.Int64 `json:"-"`
	TxRate    atomic.Uint64 `json:"-"` // bytes/sec
	RxRate    atomic.Uint64 `json:"-"`
	cancel    func() // to kick
}

type SessionJSON struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	RemoteIP  string `json:"remote_ip"`
	Protocol  string `json:"protocol"`
	ConnectAt int64  `json:"connect_at"` // unix timestamp
	Duration  int    `json:"duration"`   // seconds
	TxBytes   int64  `json:"tx_bytes"`
	RxBytes   int64  `json:"rx_bytes"`
	TxRate    uint64 `json:"tx_rate"`
	RxRate    uint64 `json:"rx_rate"`
}

// SessionManager tracks active connections across all protocols.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	seq      atomic.Int64
}

func NewSessionManager() *SessionManager {
	return &SessionManager{sessions: make(map[string]*Session)}
}

// Add registers a new session and returns its ID.
func (m *SessionManager) Add(username, remoteIP, protocol string, cancel func()) string {
	id := time.Now().Format("20060102150405") + "-" + itoa(int(m.seq.Add(1)))
	s := &Session{
		ID:        id,
		Username:  username,
		RemoteIP:  remoteIP,
		Protocol:  protocol,
		ConnectAt: time.Now(),
		cancel:    cancel,
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return id
}

// Remove removes a session by ID.
func (m *SessionManager) Remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// Kick disconnects a session by ID.
func (m *SessionManager) Kick(id string) bool {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	if s.cancel != nil {
		s.cancel()
	}
	m.Remove(id)
	return true
}

// Get returns a session by ID.
func (m *SessionManager) Get(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// List returns all active sessions as JSON-safe structs.
func (m *SessionManager) List() []SessionJSON {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now()
	result := make([]SessionJSON, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, SessionJSON{
			ID:        s.ID,
			Username:  s.Username,
			RemoteIP:  s.RemoteIP,
			Protocol:  s.Protocol,
			ConnectAt: s.ConnectAt.Unix(),
			Duration:  int(now.Sub(s.ConnectAt).Seconds()),
			TxBytes:   s.TxBytes.Load(),
			RxBytes:   s.RxBytes.Load(),
			TxRate:    s.TxRate.Load(),
			RxRate:    s.RxRate.Load(),
		})
	}
	return result
}

// CountByProtocol returns the number of active sessions per protocol.
func (m *SessionManager) CountByProtocol() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := make(map[string]int)
	for _, s := range m.sessions {
		counts[s.Protocol]++
	}
	return counts
}

// Count returns total active sessions.
func (m *SessionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// UpdateRates snapshots current traffic and calculates per-second rates.
// Should be called periodically (e.g. every second).
// UpdateRates is reserved for future per-second rate calculation.
func (m *SessionManager) UpdateRates() {}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

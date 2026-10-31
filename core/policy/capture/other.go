//go:build !linux

package capture

import "github.com/FrankoonG/hy2scale/core/policy"

// Other platforms: no system capture.

// Manager reports the capture unavailable.
type Manager struct{ sys policy.System }

// New returns the manager.
func New() *Manager { return &Manager{} }

// Capabilities: nothing is available.
func (m *Manager) Capabilities() Capabilities {
	na := Avail{Reason: ReasonPlatform}
	return Capabilities{
		System:        SystemAvail{Local: na, Forward: ForwardAvail{Reason: ReasonPlatform}},
		Heartbeat:     na,
		IfaceTerminal: na,
		DNSProxy:      na,
		Cgroup:        na,
	}
}

// Status: inactive; enabled layers carry the reason.
func (m *Manager) Status() Status {
	var st Status
	if m.sys.Local.Enabled {
		st.Local.Error = ReasonPlatform
	}
	if m.sys.Forward.Enabled {
		st.Forward.Error = ReasonPlatform
	}
	return st
}

// Interfaces: none.
func (m *Manager) Interfaces(own func(string) bool) []Interface { return []Interface{} }

// Apply records the settings only.
func (m *Manager) Apply(o Options) error { m.sys = o.System; return nil }

// Stop does nothing.
func (m *Manager) Stop() {}

// Recover re-panics.
func (m *Manager) Recover() {
	if r := recover(); r != nil {
		panic(r)
	}
}

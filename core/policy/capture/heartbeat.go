package capture

import (
	"context"
	"sync"
	"time"
)

// Heartbeat keeps the heartbeat sets (h2s_alive4/6) filled while the
// process runs: the open-on-stop layers only take traffic whose
// destination is in them, and their entries expire HeartbeatTimeout after
// the last refresh, so a killed process stops taking traffic within that
// time.
type Heartbeat struct {
	Run func(Cmd) error  // runs one command
	Now func() time.Time // nil = time.Now

	mu     sync.Mutex
	v4, v6 bool
	active bool
	ok     bool
	last   time.Time
}

func (h *Heartbeat) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Start creates and fills the sets of the given families.
func (h *Heartbeat) Start(v4, v6 bool) error {
	h.mu.Lock()
	h.v4, h.v6, h.active = v4, v6, true
	h.mu.Unlock()
	var cmds []Cmd
	if v4 {
		cmds = append(cmds, HeartbeatCreate(false)...)
	}
	if v6 {
		cmds = append(cmds, HeartbeatCreate(true)...)
	}
	return h.runAll(cmds)
}

// Refresh re-adds the entries (resets their timeout).
func (h *Heartbeat) Refresh() error {
	h.mu.Lock()
	v4, v6, active := h.v4, h.v6, h.active
	h.mu.Unlock()
	if !active {
		return nil
	}
	var cmds []Cmd
	if v4 {
		cmds = append(cmds, HeartbeatRefresh(false)...)
	}
	if v6 {
		cmds = append(cmds, HeartbeatRefresh(true)...)
	}
	return h.runAll(cmds)
}

func (h *Heartbeat) runAll(cmds []Cmd) error {
	var err error
	for _, c := range cmds {
		if e := h.Run(c); e != nil && err == nil {
			err = e
		}
	}
	h.mu.Lock()
	h.ok = err == nil
	if err == nil {
		h.last = h.now()
	}
	h.mu.Unlock()
	return err
}

// Stop ends refreshing (the sets are destroyed by the caller after the
// rules referencing them are gone).
func (h *Heartbeat) Stop() {
	h.mu.Lock()
	h.active = false
	h.mu.Unlock()
}

// Active reports whether the heartbeat is running.
func (h *Heartbeat) Active() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

// Status is the API view.
func (h *Heartbeat) Status() HeartbeatStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := HeartbeatStatus{OK: h.active && h.ok}
	if !h.last.IsZero() {
		st.Last = h.last.Unix()
	}
	return st
}

// Loop refreshes every interval until ctx ends.
func (h *Heartbeat) Loop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.Refresh()
		}
	}
}

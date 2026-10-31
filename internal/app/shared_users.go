package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

// Shared users: one node (the manager) owns a user and pushes it to other
// nodes over the relay mesh (internal/api/shared_users.go). A receiving node
// keeps its own exit settings and traffic counter for it; everything else
// comes from the manager. See docs/v1.4.0-shared-users.md.

// SharedFrom marks a user received from a manager.
type SharedFrom struct {
	NodeID string `yaml:"node_id" json:"node_id"`
	Name   string `yaml:"name,omitempty" json:"name,omitempty"`
	// UserID: the user's id on the manager (the local id may differ).
	UserID   string `yaml:"user_id" json:"user_id"`
	Rev      int64  `yaml:"rev" json:"rev"`
	ResetRev int64  `yaml:"reset_rev,omitempty" json:"reset_rev,omitempty"`
	SyncedAt string `yaml:"synced_at,omitempty" json:"synced_at,omitempty"`
}

// ManagerName: how the UI names the manager.
func (s *SharedFrom) ManagerName() string {
	if s.Name != "" {
		return s.Name
	}
	return s.NodeID
}

// SharedDelete: a shared user to delete on a target.
type SharedDelete struct {
	Target string `yaml:"target" json:"target"`
	UserID string `yaml:"user_id" json:"user_id"`
}

func addSharedDelete(c *Config, d SharedDelete) {
	for _, e := range c.SharedDeletes {
		if e == d {
			return
		}
	}
	c.SharedDeletes = append(c.SharedDeletes, d)
}

func dropSharedDelete(c *Config, d SharedDelete) {
	out := c.SharedDeletes[:0]
	for _, e := range c.SharedDeletes {
		if e != d {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		out = nil
	}
	c.SharedDeletes = out
}

// SharedPayload: the user as pushed to targets — no exit settings, no
// counters, no share bookkeeping (the receiver keeps its own).
func SharedPayload(u UserConfig) UserConfig {
	u.ExitVia, u.ExitPaths, u.ExitMode = "", nil, ""
	u.TrafficUsed = 0
	u.TrafficResetAt, u.TrafficResetFrom = "", ""
	u.ShareTargets, u.ShareRev, u.ShareResetRev = nil, 0, 0
	u.SharedFrom = nil
	return u
}

// NormalizeShareTarget checks a relay chain "a/b/c": no empty hop, no hop
// twice, never this node itself (nested rule 1: self is always an ancestor).
func NormalizeShareTarget(t, selfID, selfName string) (string, error) {
	t = strings.Trim(strings.TrimSpace(t), "/")
	if t == "" {
		return "", fmt.Errorf("empty target")
	}
	seen := map[string]bool{}
	for _, hop := range strings.Split(t, "/") {
		if hop == "" {
			return "", fmt.Errorf("target %q: empty hop", t)
		}
		if hop == selfID || hop == selfName {
			return "", fmt.Errorf("target %q: contains this node", t)
		}
		if seen[hop] {
			return "", fmt.Errorf("target %q: %s appears twice", t, hop)
		}
		seen[hop] = true
	}
	return t, nil
}

// nextShareRev: revisions only grow, also across a config restored from an
// older backup (they follow the clock).
func nextShareRev(cur int64) int64 {
	r := time.Now().UnixMilli()
	if r <= cur {
		r = cur + 1
	}
	return r
}

// SetShareTargets sets the targets of a user owned here and bumps its
// revision; targets dropped are queued for deletion. Returns the dropped
// targets.
func (a *App) SetShareTargets(id string, targets []string) (removed []string, err error) {
	found := false
	uerr := a.store.Update(func(c *Config) {
		for i := range c.Users {
			u := &c.Users[i]
			if u.ID != id {
				continue
			}
			found = true
			if u.SharedFrom != nil {
				err = fmt.Errorf("shared user: managed by %s", u.SharedFrom.ManagerName())
				return
			}
			keep := map[string]bool{}
			for _, t := range targets {
				keep[t] = true
				dropSharedDelete(c, SharedDelete{Target: t, UserID: id})
			}
			for _, t := range u.ShareTargets {
				if !keep[t] {
					removed = append(removed, t)
					addSharedDelete(c, SharedDelete{Target: t, UserID: id})
				}
			}
			if len(targets) == 0 {
				u.ShareTargets = nil
			} else {
				u.ShareTargets = append([]string(nil), targets...)
			}
			u.ShareRev = nextShareRev(u.ShareRev)
			return
		}
	})
	if err == nil && uerr != nil {
		err = uerr
	}
	if err == nil && !found {
		err = fmt.Errorf("user not found")
	}
	return removed, err
}

// BumpShareRev records a change of a user shared out (manualReset: its
// traffic was reset by hand, the targets reset theirs too). Returns its
// targets (nil when it is not shared out).
func (a *App) BumpShareRev(id string, manualReset bool) []string {
	shared := false
	for _, u := range a.store.Get().Users {
		if u.ID == id && len(u.ShareTargets) > 0 {
			shared = true
		}
	}
	if !shared {
		return nil
	}
	var targets []string
	a.store.Update(func(c *Config) {
		for i := range c.Users {
			u := &c.Users[i]
			if u.ID != id || len(u.ShareTargets) == 0 {
				continue
			}
			u.ShareRev = nextShareRev(u.ShareRev)
			if manualReset {
				u.ShareResetRev = u.ShareRev
			}
			targets = append([]string(nil), u.ShareTargets...)
			return
		}
	})
	return targets
}

// DoneSharedDelete: the target confirmed the deletion.
func (a *App) DoneSharedDelete(d SharedDelete) {
	a.store.Update(func(c *Config) { dropSharedDelete(c, d) })
}

// SharedApply is the outcome of a push on the receiving node.
type SharedApply struct {
	State       string `json:"state"` // "synced" | "stale" | "conflict"
	ID          string `json:"id,omitempty"`
	Rev         int64  `json:"rev"`
	TrafficUsed int64  `json:"traffic_used"`
	Error       string `json:"error,omitempty"`
}

func randomUserID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// FindSharedUser: the user received from manager with its manager-side id.
func (a *App) FindSharedUser(managerID, userID string) (UserConfig, bool) {
	for _, u := range a.store.Get().Users {
		if u.SharedFrom != nil && u.SharedFrom.NodeID == managerID && u.SharedFrom.UserID == userID {
			if live, ok := a.liveUser(u.Username); ok {
				u.TrafficUsed = live
			}
			return u, true
		}
	}
	return UserConfig{}, false
}

func (a *App) liveUser(username string) (int64, bool) {
	a.usersMu.RLock()
	defer a.usersMu.RUnlock()
	if u, ok := a.userIndex[username]; ok {
		return atomic.LoadInt64(&u.TrafficUsed), true
	}
	return 0, false
}

// ApplySharedUser upserts a user pushed by a manager. Older revisions are
// ignored; a username already used here by another user is a conflict and
// nothing is changed. The local exit settings and traffic counter stay; the
// counter is reset when the manager reset its own by hand (resetRev).
func (a *App) ApplySharedUser(managerID, managerName, userID string, rev, resetRev int64, in UserConfig) (SharedApply, error) {
	in = SharedPayload(in)
	now := TrafficNow()
	stamp := now.Format(time.RFC3339)
	var res SharedApply
	var kick string
	changed := false
	a.usersMu.RLock()
	err := a.store.Update(func(c *Config) {
		idx := -1
		for i, u := range c.Users {
			if u.SharedFrom != nil && u.SharedFrom.NodeID == managerID && u.SharedFrom.UserID == userID {
				idx = i
				break
			}
		}
		if idx >= 0 && rev < c.Users[idx].SharedFrom.Rev {
			res = SharedApply{State: "stale", ID: c.Users[idx].ID, Rev: c.Users[idx].SharedFrom.Rev}
			return
		}
		for i, u := range c.Users {
			if i != idx && u.Username == in.Username {
				res = SharedApply{State: "conflict", Error: fmt.Sprintf("username %s is already used on this node", in.Username)}
				return
			}
		}
		if c.Server != nil && c.Server.Password != "" && c.Server.Password == in.Password {
			res = SharedApply{State: "conflict", Error: "user password collides with this node's hy2 server password"}
			return
		}
		from := &SharedFrom{NodeID: managerID, Name: managerName, UserID: userID, Rev: rev, ResetRev: resetRev, SyncedAt: stamp}
		if idx < 0 {
			nu := in
			nu.ID = userID
			for _, u := range c.Users {
				if u.ID == nu.ID {
					nu.ID = randomUserID()
					break
				}
			}
			if nu.TrafficReset != "" {
				nu.TrafficResetFrom = stamp
			}
			nu.SharedFrom = from
			c.Users = append(c.Users, nu)
			res = SharedApply{State: "synced", ID: nu.ID, Rev: rev}
			changed = true
			return
		}
		cur := &c.Users[idx]
		if rev == cur.SharedFrom.Rev {
			cur.SharedFrom.SyncedAt = stamp
			res = SharedApply{State: "synced", ID: cur.ID, Rev: rev, TrafficUsed: atomic.LoadInt64(&cur.TrafficUsed)}
			return
		}
		nu := in
		nu.ID = cur.ID
		nu.ExitVia, nu.ExitPaths, nu.ExitMode = cur.ExitVia, cur.ExitPaths, cur.ExitMode
		nu.TrafficUsed = atomic.LoadInt64(&cur.TrafficUsed)
		nu.TrafficResetAt, nu.TrafficResetFrom = cur.TrafficResetAt, cur.TrafficResetFrom
		if nu.TrafficReset != cur.TrafficReset || nu.TrafficResetDay != cur.TrafficResetDay {
			nu.TrafficResetFrom = ""
			if nu.TrafficReset != "" {
				nu.TrafficResetFrom = stamp
			}
		}
		if resetRev > cur.SharedFrom.ResetRev {
			if live, ok := a.userIndex[cur.Username]; ok {
				atomic.StoreInt64(&live.TrafficUsed, 0)
			}
			nu.TrafficUsed = 0
			nu.TrafficResetAt = stamp
		}
		if cur.Password != nu.Password || fmt.Sprint(cur.ProxyPasswords) != fmt.Sprint(nu.ProxyPasswords) ||
			fmt.Sprint(cur.ProxyDisabled) != fmt.Sprint(nu.ProxyDisabled) || cur.Username != nu.Username ||
			(cur.Enabled && !nu.Enabled) {
			kick = cur.Username
		}
		nu.SharedFrom = from
		*cur = nu
		res = SharedApply{State: "synced", ID: nu.ID, Rev: rev, TrafficUsed: nu.TrafficUsed}
		changed = true
	})
	a.usersMu.RUnlock()
	if err != nil {
		return res, err
	}
	if changed {
		a.rebuildUserIndex()
		log.Printf("[shared] user %s from %s: rev %d applied", in.Username, managerName, rev)
	}
	if kick != "" {
		a.Sessions.KickUser(kick)
	}
	return res, nil
}

// RemoveSharedUser deletes the user received from manager (false: no such
// user, e.g. it was detached).
func (a *App) RemoveSharedUser(managerID, userID string) (bool, error) {
	var name string
	err := a.store.Update(func(c *Config) {
		for i, u := range c.Users {
			if u.SharedFrom != nil && u.SharedFrom.NodeID == managerID && u.SharedFrom.UserID == userID {
				name = u.Username
				c.Users = append(c.Users[:i], c.Users[i+1:]...)
				return
			}
		}
	})
	if err != nil || name == "" {
		return false, err
	}
	a.rebuildUserIndex()
	a.Sessions.KickUser(name)
	log.Printf("[shared] user %s deleted by its manager %s", name, managerID)
	return true, nil
}

// DetachSharedUser turns a received user into an ordinary local one: its
// manager's pushes are no longer accepted for it.
func (a *App) DetachSharedUser(id string) error {
	found := false
	err := a.store.Update(func(c *Config) {
		for i := range c.Users {
			if c.Users[i].ID == id && c.Users[i].SharedFrom != nil {
				c.Users[i].SharedFrom = nil
				found = true
				return
			}
		}
	})
	if err == nil && !found {
		return fmt.Errorf("not a shared user")
	}
	if err == nil {
		a.rebuildUserIndex()
	}
	return err
}

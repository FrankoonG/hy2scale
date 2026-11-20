package app

// The one-time migration to exit kinds (user_creds.go), run on load:
//   - a password of its own for one protocol (proxy_passwords) becomes a new
//     user "<name>_<proto>" with only that protocol and the same settings;
//     the original user stops serving that protocol;
//   - node access with a direct exit becomes kind access; node access with
//     another exit is dropped;
//   - a policy set becomes kind policy with one row (the password → that set);
//   - everything else is kind group.

import (
	"fmt"
	"log"
	"strconv"
)

// migrateRowID: the id of the row the migration makes (the same on every
// holder of a shared user).
const migrateRowID = "r1"

// MigrateUserKinds: the migration; reports whether anything changed.
func MigrateUserKinds(c *Config) bool {
	changed := false
	taken := map[string]bool{}
	for _, u := range c.Users {
		taken[u.Username] = true
	}
	var add []UserConfig
	for i := range c.Users {
		u := &c.Users[i]
		if u.ExitKind != "" {
			continue
		}
		changed = true
		// a protocol's own password → a user of its own
		for _, proto := range PasswordOnlyProxies {
			pw := u.ProxyPasswords[proto]
			if pw == "" || pw == u.Password || !u.IsProxyEnabled(proto) {
				continue
			}
			nu := *u
			nu.ID = randomUserID()
			nu.Username = uniqueName(taken, u.Username+"_"+proto)
			nu.Password = pw
			nu.ProxyPasswords = nil
			nu.ProxyDisabled = nil
			for _, p := range allProxyKeys {
				if p != proto {
					nu.ProxyDisabled = append(nu.ProxyDisabled, p)
				}
			}
			nu.TrafficUsed, nu.RemoteUsed, nu.TrafficResetAt = 0, 0, ""
			nu.Shared = nil
			nu.NodeAccess, nu.NodeAccessMode = false, ""
			setKind(&nu)
			add = append(add, nu)
			u.ProxyDisabled = append(u.ProxyDisabled, proto)
			log.Printf("[users] %s: its own %s password moved to the new user %s", u.Username, proto, nu.Username)
		}
		u.ProxyPasswords = nil
		setKind(u)
	}
	c.Users = append(c.Users, add...)
	return changed
}

// setKind: the kind of a user still configured the old way.
func setKind(u *UserConfig) {
	direct := u.ExitVia == "" && len(u.ExitPaths) == 0 && u.ExitPolicy == ""
	switch {
	case u.NodeAccess && direct:
		u.ExitKind = KindAccess
	case u.ExitPolicy != "":
		u.ExitKind = KindPolicy
		u.Rows = []UserRow{{ID: migrateRowID, Password: u.Password}}
		u.RowPolicy = map[string]string{migrateRowID: u.ExitPolicy}
		u.ExitPolicy = ""
		u.NodeAccess = false
	default:
		u.ExitKind = KindGroup
		u.NodeAccess = false
	}
}

// allProxyKeys: every protocol a user can be enabled on.
var allProxyKeys = []string{"hy2", "ss", "trojan", "socks5", "http", "l2tp", "ikev2", "openvpn"}

func uniqueName(taken map[string]bool, name string) string {
	n := name
	for i := 2; taken[n]; i++ {
		n = name + strconv.Itoa(i)
	}
	taken[n] = true
	return n
}

// rowLabel: the audit label of user name's row ("#2 Japan").
func (a *App) rowLabel(name, row string) string {
	a.usersMu.RLock()
	u, ok := a.userIndex[name]
	a.usersMu.RUnlock()
	if !ok {
		return ""
	}
	idx := u.RowIndex(row)
	label := fmt.Sprintf("#%d", idx)
	if p := a.store.Get().Policy; p != nil {
		if ps := p.PolicySetByID(u.RowPolicy[row]); ps != nil && ps.Name != "" {
			label += " " + ps.Name
		}
	}
	return label
}

// needsKindMigration: some user has no exit kind yet.
func needsKindMigration(c Config) bool {
	for _, u := range c.Users {
		if u.ExitKind == "" {
			return true
		}
	}
	return false
}

// RefreshSharedCaps: this node's member entry of every shared user states
// what this version understands (SharedCaps); the gids changed are synced.
func (a *App) RefreshSharedCaps() []string {
	self := a.SelfIID()
	var changed []string
	a.store.Update(func(c *Config) {
		for i := range c.Users {
			st := c.Users[i].Shared
			if st == nil {
				continue
			}
			m, ok := st.Members[self]
			if !ok || m.HasCap("kinds") {
				continue
			}
			m.Caps = SharedCaps
			m.S = a.hlcNow()
			st.Members[self] = m
			changed = append(changed, st.GID)
		}
	})
	for _, gid := range changed {
		a.sharedChanged(gid)
	}
	return changed
}

// SharedLacksKinds: the members of a shared user (not this node, not left)
// whose version does not understand exit kinds and password rows.
func SharedLacksKinds(u UserConfig, self string) []string {
	if u.Shared == nil {
		return nil
	}
	var out []string
	for iid, m := range u.Shared.Members {
		if iid == self || m.Left || m.HasCap("kinds") {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.NodeID
		}
		out = append(out, name)
	}
	return out
}

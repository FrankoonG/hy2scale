package app

import (
	"github.com/FrankoonG/hy2scale/core/proto"
)

// Restrictions on what a flow may reach from the node that dials it. A
// user's restrictions ride on the dial context here; flows that leave
// through another node carry them as a tag on the target (proto.Restrict),
// which a 1.4.0+ exit applies to its own dial.

// userRestrict: the restrictions of a user's flows.
func (a *App) userRestrict(username string) proto.Restrict {
	a.usersMu.RLock()
	u, ok := a.userIndex[username]
	a.usersMu.RUnlock()
	if ok && u.DenyInternal {
		return proto.DenyInternal
	}
	return 0
}

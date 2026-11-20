package app

// A user's exit kind and the passwords it signs in with: kind "group" (one
// password, the exit fields), "policy" (rows of password → policy set; the
// policy set of each row is this node's own) or "access" (node access: one
// hy2 password; traffic leaving here goes direct, the node that signed in
// picks its own paths).

import (
	"strings"

	"github.com/xen0bit/veepin/dataplane"
)

// User exit kinds.
const (
	KindGroup  = "group"
	KindPolicy = "policy"
	KindAccess = "access"
)

// UserRow: one password of a policy-kind user (shared between holders; the
// policy set of each row is per node, UserConfig.RowPolicy).
type UserRow struct {
	ID       string `yaml:"id" json:"id"`
	Password string `yaml:"password" json:"password"`
}

// RowSep joins a username and a row id in a signed-in identity
// (usernames may not contain control characters).
const RowSep = "\x1f"

// Ident: the identity of user name signed in with row ("" = the main
// password).
func Ident(name, row string) string {
	if row == "" {
		return name
	}
	return name + RowSep + row
}

// SplitIdent: the username and row of an identity.
func SplitIdent(id string) (name, row string) {
	name, row, _ = strings.Cut(id, RowSep)
	return name, row
}

// hy2Ident: the username and row of an hy2 identity ("user:<ident>").
func hy2Ident(authID string) (name, row string) {
	return SplitIdent(strings.TrimPrefix(authID, "user:"))
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// userEntry: the label audit records give a user's row ("#2 Japan"); set
// by New.
var userEntry func(name, row string) string

// Kind: the user's exit kind (group when unset).
func (u *UserConfig) Kind() string {
	switch u.ExitKind {
	case KindPolicy, KindAccess:
		return u.ExitKind
	}
	return KindGroup
}

// Serves: u may sign in on proxy (an access-kind user only on hy2).
func (u *UserConfig) Serves(proxy string) bool {
	if u.Kind() == KindAccess {
		return proxy == "hy2" || proxy == ""
	}
	return proxy == "" || u.IsProxyEnabled(proxy)
}

// Cred: a password u signs in with and its row ("" = the main password).
type Cred struct {
	Row      string
	Password string
}

// Creds: the passwords u signs in with on this node: a policy-kind user's
// rows that have a policy set here, else the main password.
func (u *UserConfig) Creds() []Cred {
	if u.Kind() != KindPolicy {
		if u.Password == "" {
			return nil
		}
		return []Cred{{Password: u.Password}}
	}
	var out []Cred
	for _, r := range u.Rows {
		if r.Password != "" && u.RowPolicy[r.ID] != "" {
			out = append(out, Cred{Row: r.ID, Password: r.Password})
		}
	}
	return out
}

// CredFor: the row of u that password signs in with.
func (u *UserConfig) CredFor(password string) (row string, ok bool) {
	for _, c := range u.Creds() {
		if c.Password == password {
			return c.Row, true
		}
	}
	return "", false
}

// SinglePolicy: the policy set every row of a policy-kind user goes to
// here ("" = none, or rows going to different sets).
func (u *UserConfig) SinglePolicy() string {
	if u.Kind() != KindPolicy {
		return ""
	}
	one := ""
	for _, r := range u.Rows {
		p := u.RowPolicy[r.ID]
		if p == "" || (one != "" && p != one) {
			return ""
		}
		one = p
	}
	return one
}

// RowIndex: the 1-based position of row among u's rows (0 = none).
func (u *UserConfig) RowIndex(row string) int {
	for i, r := range u.Rows {
		if r.ID == row {
			return i + 1
		}
	}
	return 0
}

// vpnCreds: the passwords user name signs in with on a VPN (proto: l2tp,
// ikev2, openvpn), each reporting its identity (Ident).
func (a *App) vpnCreds(proto, name string) ([]dataplane.Credential, bool) {
	u := a.vpnUser(name, proto)
	if u == nil {
		return nil, false
	}
	var out []dataplane.Credential
	for _, c := range u.Creds() {
		out = append(out, dataplane.Credential{Password: c.Password, Identity: Ident(u.Username, c.Row)})
	}
	return out, len(out) > 0
}

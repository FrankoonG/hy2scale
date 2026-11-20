package dataplane

// Credential is one password a user may sign in with, and the identity the
// session reports when this password is the one that matched (hy2scale P17:
// a user with several passwords).
type Credential struct {
	Password string
	Identity string
}

// Package names is the single policy point for user-supplied simple-vps
// identifiers. Generated host/container artifact names live in
// internal/identity.
package names

import "regexp"

const (
	AppPattern        = `^[a-z][a-z0-9-]{1,40}$`
	EnvPattern        = `^[a-z][a-z0-9-]{0,30}$`
	ProcessPattern    = EnvPattern
	ReleasePattern    = `^(?:[a-f0-9]{7,64}|[a-f0-9]{7,40}-dirty-[0-9]{8}t[0-9]{6}z)(?:-s[0-9a-f]{12})?$`
	SystemUserPattern = `^[a-z_][a-z0-9_-]{0,31}\$?$`
	EnvKeyPattern     = `^[A-Za-z_][A-Za-z0-9_]*$`
)

var (
	AppRe        = regexp.MustCompile(AppPattern)
	EnvRe        = regexp.MustCompile(EnvPattern)
	ProcessRe    = regexp.MustCompile(ProcessPattern)
	ReleaseRe    = regexp.MustCompile(ReleasePattern)
	SystemUserRe = regexp.MustCompile(SystemUserPattern)
	EnvKeyRe     = regexp.MustCompile(EnvKeyPattern)
)

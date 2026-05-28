// Package identity centralizes the naming of per-(app, env) and
// per-(app, env, service) artifacts: system users, Podman networks,
// container names, image tags, and on-disk paths.
//
// Per ADR-0005 cutover items 6-10 and ADR-0006 Cut 4, identifiers carry
// both the app and env so prod and staging on the same VPS never collide
// on user, network, paths, or image storage.
//
// ADR-0006 Cut 4 says simple-vps may internally hash identifiers when
// they would exceed Linux limits (31-char usernames in particular).
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	linuxUserNameLimit = 31
	dnsLabelLimit      = 63
)

// SystemUser is the Linux account that owns the per-env app data and
// runs the container processes (via --user). Format: `app-<app>-<env>`.
func SystemUser(app, env string) string {
	return boundedIdentityName("app", linuxUserNameLimit, app, env)
}

// Network is the per-(app, env) Podman network used for intra-app
// container-to-container DNS (e.g., a worker addressing the web service
// by name). Caddy reaches app services over a separate shared
// `ingress` network created at host install time.
func Network(app, env string) string {
	return fmt.Sprintf("app-%s-%s", app, env)
}

// ContainerName names the live container for a service.
func ContainerName(app, env, service string) string {
	return boundedIdentityName("app", dnsLabelLimit, app, env, service)
}

func boundedIdentityName(prefix string, limit int, parts ...string) string {
	full := prefix + "-" + strings.Join(parts, "-")
	if len(full) <= limit {
		return full
	}

	hash := shortHash(strings.Join(parts, "\x00"), 8)
	segmentBudget := limit - len(prefix) - len(hash) - 2
	if segmentBudget < 1 {
		return prefix + "-" + hash
	}
	base := strings.Join(parts, "-")
	if len(base) > segmentBudget {
		base = base[:segmentBudget]
	}
	base = strings.Trim(base, "-")
	if base == "" {
		base = "x"
	}
	return fmt.Sprintf("%s-%s-%s", prefix, base, hash)
}

func shortHash(value string, chars int) string {
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	return encoded[:chars]
}

// ImageRepo is the local Podman image repo (without tag) for one
// (app, env) pair. The full image reference is `ImageTag(app, env, sha)`.
func ImageRepo(app, env string) string {
	return fmt.Sprintf("simple-vps/%s-%s", app, env)
}

// ImageTag is the full image reference for a deploy.
func ImageTag(app, env, sha string) string {
	return fmt.Sprintf("%s:%s", ImageRepo(app, env), sha)
}

// AppRoot is the parent directory holding every env's data for one app.
// Removed only when the last env of an app is destroyed (ADR-0005 §12).
func AppRoot(app string) string {
	return fmt.Sprintf("/var/apps/%s", app)
}

// AppEnvRoot is the per-(app, env) root directory.
func AppEnvRoot(app, env string) string {
	return fmt.Sprintf("/var/apps/%s/%s", app, env)
}

// SharedDir is the per-(app, env) persistent directory bind-mounted
// into the container. Holds the env file and any app state.
func SharedDir(app, env string) string {
	return AppEnvRoot(app, env) + "/shared"
}

// EnvFile is the resolved runtime env file written into shared/ before
// the container starts. Holds resolved secret values; mode 0600, owned
// by SystemUser(app, env). Not in git, not in helper state.
func EnvFile(app, env string) string {
	return SharedDir(app, env) + "/.env"
}

// Package identity centralizes every host-side name derived from an
// `(app, env)` pair.
//
// Human-readable host paths use `/var/apps/<app>.<env>`. Linux, Podman,
// DNS, and lock identifiers use a bounded derived infra ID so names stay
// within platform limits without becoming reverse-parsed state.
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

// InfraID is the deterministic bounded ID for one `(app, env)` pair.
// It is stable before the env identity file exists, which lets setup
// and locking use the same name as later lifecycle operations.
func InfraID(app, env string) string {
	return "svps-" + shortHash(app+"\x00"+env, 12)
}

// SystemUser is the Linux account that owns /data files and runs
// container processes.
func SystemUser(app, env string) string {
	return boundedIdentityName(InfraID(app, env), linuxUserNameLimit)
}

// Network is the per-(app, env) Podman network used for intra-app DNS.
func Network(app, env string) string {
	return boundedIdentityName(InfraID(app, env), dnsLabelLimit)
}

// ContainerName names one versioned process container. Caddy points at
// these names directly during the web handoff.
func ContainerName(app, env, process, release string) string {
	return boundedIdentityName(InfraID(app, env)+"-"+process+"-"+release, dnsLabelLimit)
}

func boundedIdentityName(base string, limit int) string {
	if len(base) <= limit {
		return base
	}
	hash := shortHash(base, 8)
	segmentBudget := limit - len(hash) - 1
	if segmentBudget < 1 {
		return hash[:limit]
	}
	prefix := strings.Trim(base[:segmentBudget], "-")
	if prefix == "" {
		prefix = "x"
	}
	return fmt.Sprintf("%s-%s", prefix, hash)
}

func shortHash(value string, chars int) string {
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	return encoded[:chars]
}

// ImageRepo is the local Podman image repo (without tag) for one
// `(app, env)` pair. The full image reference is `ImageTag(app, env, sha)`.
func ImageRepo(app, env string) string {
	return "simple-vps/" + InfraID(app, env)
}

// ImageTag is the full image reference for a deploy.
func ImageTag(app, env, sha string) string {
	return fmt.Sprintf("%s:%s", ImageRepo(app, env), sha)
}

// EnvRoot is the host root for one `(app, env)` lifecycle unit.
func EnvRoot(app, env string) string {
	return fmt.Sprintf("/var/apps/%s.%s", app, env)
}

// DataDir is mounted into container apps as /data and is included in backups.
func DataDir(app, env string) string {
	return EnvRoot(app, env) + "/data"
}

// RuntimeDir holds generated runtime config. It is not backed up as user data.
func RuntimeDir(app, env string) string {
	return EnvRoot(app, env) + "/runtime"
}

// EnvFile is the resolved runtime env file passed to Podman via --env-file.
func EnvFile(app, env string) string {
	return RuntimeDir(app, env) + "/.env"
}

// StaticDir is the root for static assets/releases.
func StaticDir(app, env string) string {
	return EnvRoot(app, env) + "/static"
}

// ReleaseDir stores per-release metadata such as manifest snapshots.
func ReleaseDir(app, env string) string {
	return EnvRoot(app, env) + "/releases"
}

// ReleaseManifestFile is the manifest snapshot that produced one release.
func ReleaseManifestFile(app, env, release string) string {
	return ReleaseDir(app, env) + "/" + release + "/simple-vps.toml"
}

// ReleaseMetadataFile stores non-authoritative but user-visible release
// details such as dirty state and base commit.
func ReleaseMetadataFile(app, env, release string) string {
	return ReleaseDir(app, env) + "/" + release + "/release.json"
}

// ManifestFile is the last manifest successfully applied for one `(app, env)`.
func ManifestFile(app, env string) string {
	return EnvRoot(app, env) + "/simple-vps.toml"
}

// IdentityFile is the durable env identity anchor.
func IdentityFile(app, env string) string {
	return EnvRoot(app, env) + "/simple-vps.json"
}

// CaddyFragmentFile is the generated ingress fragment for one `(app, env)`.
func CaddyFragmentFile(app, env string) string {
	return "/etc/caddy/conf.d/simple-vps-" + InfraID(app, env) + ".caddy"
}

// Package identity centralizes every host-side name derived from an
// `(app, env)` pair.
//
// Human-readable host paths use `/var/apps/<app>.<env>`. Linux, Podman,
// DNS, and lock identifiers use a bounded derived infra ID so names stay
// within platform limits without becoming reverse-parsed state.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	linuxUserNameLimit = 31
	dnsLabelLimit      = 63
)

// InfraID is the deterministic bounded ID for one `(app, env)` pair.
// It is stable before the env identity file exists, which lets setup
// and locking use the same name as later lifecycle operations.
func InfraID(app, env string) string {
	return "ship-" + shortHash(app+"\x00"+env, 12)
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

// ContainerInstanceName names an extra container for the same process and
// release. It is used when redeploying the same release so a fresh web
// container can be started and routed before the previous one is removed.
func ContainerInstanceName(app, env, process, release, instance string) string {
	return boundedIdentityName(InfraID(app, env)+"-"+process+"-"+release+"-"+instance, dnsLabelLimit)
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
	return "ship/" + InfraID(app, env)
}

// ImageTag is the full image reference for a deploy.
func ImageTag(app, env, sha string) string {
	return fmt.Sprintf("%s:%s", ImageRepo(app, env), sha)
}

// AppsRoot is the host root containing every app environment. Tests can
// override it with SHIP_APPS_DIR, mirroring the secrets package's test hook.
func AppsRoot() string {
	if v := os.Getenv("SHIP_APPS_DIR"); v != "" {
		return v
	}
	return "/var/apps"
}

// EnvRoot is the host root for one `(app, env)` lifecycle unit.
func EnvRoot(app, env string) string {
	return filepath.Join(AppsRoot(), fmt.Sprintf("%s.%s", app, env))
}

// DataDir is mounted into container apps as /data and is included in backups.
func DataDir(app, env string) string {
	return EnvRoot(app, env) + "/data"
}

// RuntimeDir holds generated runtime config. It is not backed up as user data.
func RuntimeDir(app, env string) string {
	return EnvRoot(app, env) + "/runtime"
}

// ActivationsDir holds immutable resolved env files, one per activation.
func ActivationsDir(app, env string) string {
	return filepath.Join(RuntimeDir(app, env), "activations")
}

// ActivationEnvFile is the resolved runtime env file for one activation.
func ActivationEnvFile(app, env, activation string) string {
	return filepath.Join(ActivationsDir(app, env), activation+".env")
}

// StaticDir is the root for static assets/releases.
func StaticDir(app, env string) string {
	return EnvRoot(app, env) + "/static"
}

// ReleaseDir stores the deploy journal. Release payloads live in images or
// static release directories; no manifest/metadata snapshots are written.
func ReleaseDir(app, env string) string {
	return EnvRoot(app, env) + "/releases"
}

// DeployJournalFile stores append-only deploy/rollback attempts for one env.
func DeployJournalFile(app, env string) string {
	return ReleaseDir(app, env) + "/journal.jsonl"
}

// ActiveFile is the single durable intent pointer for one environment.
func ActiveFile(app, env string) string {
	return filepath.Join(EnvRoot(app, env), "active.json")
}

// IdentityFile is the durable env identity anchor.
func IdentityFile(app, env string) string {
	return EnvRoot(app, env) + "/ship.json"
}

// CaddyFragmentFile is the generated ingress fragment for one `(app, env)`.
func CaddyFragmentFile(app, env string) string {
	return "/etc/caddy/conf.d/ship-" + InfraID(app, env) + ".caddy"
}

// EnvIdentity is the durable identity anchor stored at IdentityFile(app, env).
type EnvIdentity struct {
	Version int              `json:"version"`
	App     string           `json:"app"`
	Env     string           `json:"env"`
	InfraID string           `json:"infra_id"`
	Preview *PreviewIdentity `json:"preview,omitempty"`
}

// PreviewIdentity stores the branch mapping for one preview environment.
type PreviewIdentity struct {
	Branch     string     `json:"branch"`
	LastShipAt time.Time  `json:"last_ship_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// NewActivationID returns a release-scoped activation identity. The random
// suffix prevents a redeploy of the same release from reusing its env file.
func NewActivationID(release string) (string, error) {
	if release == "" || strings.ContainsAny(release, "/\\") {
		return "", fmt.Errorf("invalid release for activation: %q", release)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate activation id: %w", err)
	}
	return release + "-" + hex.EncodeToString(suffix), nil
}

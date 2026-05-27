package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, root string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "simple-vps.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeDockerfile(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeStaticDir(t *testing.T, root string, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0755); err != nil {
		t.Fatal(err)
	}
}

func checkErrors(t *testing.T, root string, env string) []string {
	t.Helper()
	errors, _, err := CheckManifest(root, env)
	if err != nil {
		t.Fatal(err)
	}
	return errors
}

// --- Strict decoding: unknown TOML fields are rejected ---
//
// Pre-user repo with no compat window. Stale config that looks honored
// but does nothing (legacy `runtime = "..."`, `[build]`, the deferred
// `keep_releases`, or a plain typo) is worse than a clear "unknown
// field" error. ReadManifest uses toml.Decoder.DisallowUnknownFields.

func TestReadManifestRejectsLegacyRuntimeField(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"
`)
	_, err := ReadManifest(root)
	if err == nil {
		t.Fatal("expected legacy runtime field to be rejected")
	}
	if !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("expected error to mention the offending field, got %v", err)
	}
}

func TestReadManifestRejectsLegacyBuildBlock(t *testing.T) {
	root := t.TempDir()
	writeStaticDir(t, root, "dist")
	writeManifest(t, root, `
name = "site"
static = "dist"

[build]
command = "npm run build"

[env.production]
server = "deploy@100.x.y.z"
`)
	_, err := ReadManifest(root)
	if err == nil {
		t.Fatal("expected legacy [build] block to be rejected")
	}
	if !strings.Contains(err.Error(), "build") {
		t.Fatalf("expected error to mention the offending field, got %v", err)
	}
}

func TestReadManifestRejectsDeferredKeepReleasesField(t *testing.T) {
	// `keep_releases` is in ADR-0005 §6 but isn't wired up in the new
	// container/Podman flow yet. Accepting it as a silent no-op is a
	// foot-gun: users would think they had configured retention.
	// Strict decoding refuses it until the field comes back with
	// behavior attached.
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"
keep_releases = 3
`)
	_, err := ReadManifest(root)
	if err == nil {
		t.Fatal("expected keep_releases to be rejected (not implemented yet)")
	}
	if !strings.Contains(err.Error(), "keep_releases") {
		t.Fatalf("expected error to mention keep_releases, got %v", err)
	}
}

func TestReadManifestRejectsArbitraryTypo(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"
serfer = "deploy@100.x.y.z"

[env.production]
server = "deploy@100.x.y.z"
`)
	_, err := ReadManifest(root)
	if err == nil {
		t.Fatal("expected a misspelled top-level field to be rejected")
	}
	if !strings.Contains(err.Error(), "serfer") {
		t.Fatalf("expected error to point at the typo, got %v", err)
	}
}

func TestCheckManifestAcceptsContainerAppWithDockerfile(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
`)

	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestAcceptsStaticAppWithStaticField(t *testing.T) {
	root := t.TempDir()
	writeStaticDir(t, root, "dist")
	writeManifest(t, root, `
name = "site"
static = "dist"

[env.production]
server = "deploy@100.x.y.z"

[routes.app]
host = "site.example.com"
type = "static"
`)

	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestRejectsManifestWithNeitherShape(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "manifest is missing a shape: add a Dockerfile (container app) or set top-level static = \"<dir>\" (static app)") {
		t.Fatalf("expected missing-shape error, got %v", errors)
	}
}

func TestCheckManifestRejectsManifestWithBothShapes(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeStaticDir(t, root, "dist")
	writeManifest(t, root, `
name = "api"
static = "dist"

[env.production]
server = "deploy@100.x.y.z"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "manifest declares both shapes: a Dockerfile is present and static = \"dist\" is set; pick one") {
		t.Fatalf("expected both-shapes error, got %v", errors)
	}
}

func TestCheckManifestRejectsStaticFieldPointingAtMissingDir(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `
name = "site"
static = "dist"

[env.production]
server = "deploy@100.x.y.z"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "static = \"dist\": directory does not exist") {
		t.Fatalf("expected missing-static-dir error, got %v", errors)
	}
}

func TestCheckManifestRejectsStaticFieldOutsideRoot(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `
name = "site"
static = "../escape"

[env.production]
server = "deploy@100.x.y.z"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "static must be a relative path without '..' or globs") {
		t.Fatalf("expected static-escape error, got %v", errors)
	}
}

func TestCheckManifestDropsPathEnforcement(t *testing.T) {
	// Per ADR-0005 cutover item 5: path enforcement is removed.
	// Manifest with no path field should validate fine for a container app.
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"
`)

	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestContainerAppServiceWithoutPortIsWorker(t *testing.T) {
	// Per ADR-0005 Section 13: services without a port are workers.
	// They do not require healthcheck and skip Caddy upstream.
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.worker]
command = "bun run src/worker.ts"
`)

	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors for worker service, got %v", errors)
	}
}

func TestCheckManifestStaticAppCannotDeclareServices(t *testing.T) {
	root := t.TempDir()
	writeStaticDir(t, root, "dist")
	writeManifest(t, root, `
name = "site"
static = "dist"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "static apps cannot declare services") {
		t.Fatalf("expected static-services error, got %v", errors)
	}
}

func TestCheckManifestStaticAppCannotDeclareEnvScopedServices(t *testing.T) {
	root := t.TempDir()
	writeStaticDir(t, root, "dist")
	writeManifest(t, root, `
name = "site"
static = "dist"

[env.production]
server = "deploy@100.x.y.z"

[env.production.services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "static apps cannot declare services") {
		t.Fatalf("expected static-services error for env-scoped services, got %v", errors)
	}
}

func TestCheckManifestRejectsStaticFieldPointingAtFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "dist"), []byte("not a dir"), 0644); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, `
name = "site"
static = "dist"

[env.production]
server = "deploy@100.x.y.z"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "static = \"dist\": must be a directory") {
		t.Fatalf("expected static-not-directory error, got %v", errors)
	}
}

// --- services.<name>.tmpfs (real-box smoke finding 5) ---

func TestCheckManifestAcceptsServiceWithoutTmpfs(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"
`)
	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestAcceptsServiceTmpfsValidEntries(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[services.web.tmpfs]
"/var/cache/nginx" = "64m"
"/var/run" = "16m"
"/run/lock" = "1k"
"/var/lib/scratch" = "1g"
`)
	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestRejectsRelativeTmpfsPath(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[services.web.tmpfs]
"var/cache" = "64m"
`)
	errors := checkErrors(t, root, "production")
	want := `[services.web.tmpfs]."var/cache" must be an absolute path`
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsTmpfsPathWithDotDot(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[services.web.tmpfs]
"/var/../etc" = "16m"
`)
	errors := checkErrors(t, root, "production")
	want := `[services.web.tmpfs]."/var/../etc" must not contain ".."`
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsTmpfsPathOnDenylist(t *testing.T) {
	// Mounting tmpfs over root, /etc, /proc, /sys, /dev would either
	// brick the container immediately or hide critical config from
	// the entrypoint. Block at check time.
	for _, denied := range []string{"/", "/etc", "/proc", "/sys", "/dev"} {
		t.Run(denied, func(t *testing.T) {
			root := t.TempDir()
			writeDockerfile(t, root)
			writeManifest(t, root, fmt.Sprintf(`
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[services.web.tmpfs]
%q = "16m"
`, denied))
			errors := checkErrors(t, root, "production")
			want := fmt.Sprintf("[services.web.tmpfs].%q is reserved and cannot be a tmpfs mount", denied)
			if !slices.Contains(errors, want) {
				t.Fatalf("expected %q, got %v", want, errors)
			}
		})
	}
}

func TestCheckManifestRejectsTmpfsReservedTmpPath(t *testing.T) {
	// /tmp is the always-on tmpfs from the container security floor.
	// Manifest can't override or duplicate it.
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[services.web.tmpfs]
"/tmp" = "128m"
`)
	errors := checkErrors(t, root, "production")
	want := `[services.web.tmpfs]."/tmp" is reserved and cannot be a tmpfs mount`
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsBadTmpfsSize(t *testing.T) {
	for _, bad := range []string{"", "64", "64M", "64MiB", "1.5g", "0m", "g", "abc", "16m ", " 16m"} {
		t.Run("size="+bad, func(t *testing.T) {
			root := t.TempDir()
			writeDockerfile(t, root)
			writeManifest(t, root, fmt.Sprintf(`
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[services.web.tmpfs]
"/var/cache" = %q
`, bad))
			errors := checkErrors(t, root, "production")
			want := fmt.Sprintf(`[services.web.tmpfs]."/var/cache" size %q must match ^[1-9][0-9]*(k|m|g)$`, bad)
			if !slices.Contains(errors, want) {
				t.Fatalf("expected %q, got %v", want, errors)
			}
		})
	}
}

// --- routes.<name>.tls knob (real-box smoke finding 6) ---

func TestCheckManifestAcceptsTLSAuto(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
tls = "auto"
`)
	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestAcceptsTLSInternal(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
tls = "internal"
`)
	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestAcceptsRouteWithoutTLS(t *testing.T) {
	// Empty/missing tls is the canonical default; equivalent to "auto".
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
`)
	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestRejectsTLSGarbage(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
tls = "garbage"
`)
	errors := checkErrors(t, root, "production")
	want := "[routes.app].tls must be \"auto\" or \"internal\""
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsTLSOffWithDeferralNotice(t *testing.T) {
	// `off` has a sensible Caddyfile shape (http:// site) but is
	// deferred until users ask. Reject explicitly so a typo doesn't
	// silently roll back to a less-secure posture later.
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
tls = "off"
`)
	errors := checkErrors(t, root, "production")
	want := "[routes.app].tls must be \"auto\" or \"internal\""
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestLoadAppContextReturnsContainerShape(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"
`)

	ctx, err := LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Shape != ShapeContainer {
		t.Fatalf("expected shape %q, got %q", ShapeContainer, ctx.Shape)
	}
	if ctx.AppName != "api" || ctx.EnvName != "production" {
		t.Fatalf("unexpected context: %+v", ctx)
	}
	if ctx.AppRoot != "/var/apps/api/production" {
		t.Fatalf("expected per-env app root /var/apps/api/production, got %q", ctx.AppRoot)
	}
}

// --- [env.<env>.env] string-only blocks (ADR-0005 cutover item 3) ---

func TestCheckManifestAcceptsStringEnvValues(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
LOG_LEVEL = "info"
PUBLIC_API_URL = "https://api.example.com"

[services.web]
port = 3000
healthcheck = "/health"
`)

	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestRejectsBoolEnvValue(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
DEBUG = true

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	want := "[env.production.env].DEBUG must be a string; if you want \"true\", write it as a quoted string"
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsIntEnvValue(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
PORT = 3000

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	want := "[env.production.env].PORT must be a string; if you want \"3000\", write it as a quoted string"
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsArrayEnvValue(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
FLAGS = ["one", "two"]

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	want := "[env.production.env].FLAGS must be a string; arrays and tables are not supported"
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsInlineTableEnvValue(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
CONF = { key = "val" }

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	want := "[env.production.env].CONF must be a string; arrays and tables are not supported"
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsInvalidEnvKey(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
"1BAD" = "value"

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	want := "[env.production.env].1BAD key must match ^[A-Za-z_][A-Za-z0-9_]*$"
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

// --- @secret:KEY whole-value references (ADR-0005 cutover item 4) ---

func TestCheckManifestAcceptsValidSecretReference(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
DATABASE_URL = "@secret:db_url"

[services.web]
port = 3000
healthcheck = "/health"
`)

	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestCheckManifestRejectsSecretReferenceWithEmptyKey(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
SOMETHING = "@secret:"

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	want := "[env.production.env].SOMETHING value starts with reserved prefix '@secret:', use the secret store instead"
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsSecretReferenceWithInvalidKey(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
SOMETHING = "@secret: hello world"

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	want := "[env.production.env].SOMETHING value starts with reserved prefix '@secret:', use the secret store instead"
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestAllowsLiteralContainingSecretSubstring(t *testing.T) {
	// Per ADR-0005: partial interpolation is not supported. A literal that
	// happens to contain '@secret:' in the middle is just a literal — no
	// interpolation, no error. Only WHOLE-VALUE references starting with
	// '@secret:' are special.
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
WEIRD = "prefix@secret:foo suffix"

[services.web]
port = 3000
healthcheck = "/health"
`)

	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

func TestLoadAppContextExposesEnvAndSecretRefs(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.production.env]
LOG_LEVEL = "info"
DATABASE_URL = "@secret:db_url"

[services.web]
port = 3000
healthcheck = "/health"
`)

	ctx, err := LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if got := ctx.Env["LOG_LEVEL"]; got != "info" {
		t.Fatalf("expected LOG_LEVEL=info, got %q", got)
	}
	if got := ctx.SecretRefs["DATABASE_URL"]; got != "db_url" {
		t.Fatalf("expected DATABASE_URL ref to db_url, got %q", got)
	}
	if _, present := ctx.Env["DATABASE_URL"]; present {
		t.Fatalf("DATABASE_URL should not appear in Env when it is a secret ref")
	}
}

func TestLoadAppContextReturnsStaticShape(t *testing.T) {
	root := t.TempDir()
	writeStaticDir(t, root, "dist")
	writeManifest(t, root, `
name = "site"
static = "dist"

[env.production]
server = "deploy@100.x.y.z"

[routes.app]
host = "site.example.com"
type = "static"
`)

	ctx, err := LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Shape != ShapeStatic {
		t.Fatalf("expected shape %q, got %q", ShapeStatic, ctx.Shape)
	}
	if ctx.StaticDir != "dist" {
		t.Fatalf("expected StaticDir %q, got %q", "dist", ctx.StaticDir)
	}
}

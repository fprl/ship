package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeManifest(t *testing.T, root string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "simple-vps.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeLockfile(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "bun.lock"), []byte("\n"), 0644); err != nil {
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

func TestCheckManifestAcceptsNoBuildBunApp(t *testing.T) {
	root := t.TempDir()
	writeLockfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[services.web]
command = "bun run src/server.ts"
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

func TestCheckManifestValidatesEffectiveEnvOverrides(t *testing.T) {
	root := t.TempDir()
	writeLockfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[services.web]
command = "bun run src/server.ts"

[env.production.services.web]
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

func TestCheckManifestRejectsPathWhenNameMissing(t *testing.T) {
	root := t.TempDir()
	writeLockfile(t, root)
	writeManifest(t, root, `
[env.production]
server = "deploy@100.x.y.z"
path = "/root/evil"
runtime = "bun"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "name is required") {
		t.Fatalf("expected missing name error, got %v", errors)
	}
	if !slices.Contains(errors, "[env.production].path requires a valid top-level name") {
		t.Fatalf("expected invalid path dependency error, got %v", errors)
	}
}

func TestCheckManifestAllowsStaticAppWithoutLockfile(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `
name = "site"

[build]
command = "cp -r public dist"
output = "dist"

[env.production]
server = "deploy@100.x.y.z"
runtime = "static"

[routes.app]
host = "site.example.com"
type = "static"
`)

	if errors := checkErrors(t, root, "production"); len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
}

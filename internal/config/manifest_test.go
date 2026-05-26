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

func TestCheckManifestRejectsLegacyRuntimeField(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[services.web]
port = 3000
healthcheck = "/health"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "[env.production].runtime is no longer supported; shape is inferred from Dockerfile or static = \"<dir>\"") {
		t.Fatalf("expected legacy-runtime error, got %v", errors)
	}
}

func TestCheckManifestRejectsLegacyBuildBlock(t *testing.T) {
	root := t.TempDir()
	writeStaticDir(t, root, "dist")
	writeManifest(t, root, `
name = "site"
static = "dist"

[build]
command = "npm run build"
output = "dist"

[env.production]
server = "deploy@100.x.y.z"
`)

	errors := checkErrors(t, root, "production")
	if !slices.Contains(errors, "[build] block is no longer supported; container apps build via Dockerfile, static apps ship a pre-built directory") {
		t.Fatalf("expected legacy-build error, got %v", errors)
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

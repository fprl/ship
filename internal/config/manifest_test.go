package config

import (
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
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func validContainerManifest() string {
	return `name = "api"

[env.production]
server = "deploy@example.com"

[vars]
LOG_LEVEL = "info"
DATABASE_URL = "@secret:DATABASE_URL"

[deploy]
release = "bun run migrate"

[processes.web]
command = "bun run src/server.ts"
port = 3000
health = "/health"
resources = { memory = "512m", cpus = 0.5 }

[processes.worker]
command = "bun run worker"

[routes.app]
host = "api.example.com"
process = "web"
`
}

func TestCheckManifestAcceptsContainerV2(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, validContainerManifest())

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}

	ctx, err := LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Shape != ShapeContainer {
		t.Fatalf("shape = %q, want container", ctx.Shape)
	}
	if !ctx.NeedsImage || ctx.HasStaticRoutes {
		t.Fatalf("unexpected capabilities: needsImage=%v hasStaticRoutes=%v", ctx.NeedsImage, ctx.HasStaticRoutes)
	}
	web := ctx.Processes["web"]
	if web.Port == nil || *web.Port != 3000 || web.Health != "/health" {
		t.Fatalf("unexpected web process: %+v", web)
	}
	if web.Resources.Memory == nil || *web.Resources.Memory != "512m" {
		t.Fatalf("memory not loaded: %+v", web.Resources)
	}
	if web.Resources.CPUs == nil || *web.Resources.CPUs != 0.5 {
		t.Fatalf("cpus not loaded: %+v", web.Resources)
	}
	if ctx.Vars["LOG_LEVEL"] != "info" {
		t.Fatalf("LOG_LEVEL not loaded: %+v", ctx.Vars)
	}
	if ctx.SecretRefs["DATABASE_URL"] != "DATABASE_URL" {
		t.Fatalf("secret ref not loaded: %+v", ctx.SecretRefs)
	}
	if ctx.Deploy.Release != "bun run migrate" {
		t.Fatalf("release command not loaded: %+v", ctx.Deploy)
	}
}

func TestCheckManifestAcceptsStaticOnlyServeRoutes(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs-dist"), 0755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, `name = "site"

[env.production]
server = "deploy@example.com"

[routes.home]
host = "example.com"
serve = "dist"

[routes.docs]
host = "example.com"
path = "/docs"
serve = "docs-dist"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
	ctx, err := LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Shape != ShapeStatic {
		t.Fatalf("shape = %q, want static", ctx.Shape)
	}
	if ctx.NeedsImage || !ctx.HasStaticRoutes {
		t.Fatalf("unexpected capabilities: needsImage=%v hasStaticRoutes=%v", ctx.NeedsImage, ctx.HasStaticRoutes)
	}
}

func TestCheckManifestRejectsDockerfileOnlyManifest(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(errors, "manifest must declare at least one [processes.<name>] block or route") {
		t.Fatalf("expected no-entrypoint error, got %v", errors)
	}
}

func TestCheckManifestRejectsStaticVars(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, `name = "site"

[env.production]
server = "deploy@example.com"

[vars]
DATABASE_URL = "@secret:DATABASE_URL"

[routes.home]
host = "example.com"
serve = "dist"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(errors, "[vars] is only supported for container apps") {
		t.Fatalf("expected static vars error, got %v", errors)
	}
}

func TestReadManifestRejectsOldFields(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `name = "api"
runtime = "bun"

[env.production]
server = "deploy@example.com"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
`)
	_, err := ReadManifest(root)
	if err == nil {
		t.Fatal("expected strict decode error")
	}
	msg := err.Error()
	for _, field := range []string{"runtime", "services.web", "type", "service"} {
		if !strings.Contains(msg, field) {
			t.Fatalf("expected error to mention %q, got %v", field, err)
		}
	}
}

func TestEnvVarsOverrideTopLevelVars(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[vars]
LOG_LEVEL = "info"
DATABASE_URL = "@secret:DATABASE_URL"

[env.production.vars]
LOG_LEVEL = "debug"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"
`)

	ctx, err := LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Vars["LOG_LEVEL"] != "debug" {
		t.Fatalf("env var override failed: %+v", ctx.Vars)
	}
	if ctx.SecretRefs["DATABASE_URL"] != "DATABASE_URL" {
		t.Fatalf("secret ref missing: %+v", ctx.SecretRefs)
	}
}

func TestCheckManifestAcceptsMixedProcessAndServeRoutes(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"

[routes.docs]
host = "api.example.com"
path = "/docs"
serve = "dist"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(errors) != 0 {
		t.Fatalf("expected no errors, got %v", errors)
	}
	ctx, err := LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Shape != ShapeContainer || !ctx.NeedsImage || !ctx.HasStaticRoutes {
		t.Fatalf("unexpected mixed context: shape=%q needsImage=%v hasStaticRoutes=%v", ctx.Shape, ctx.NeedsImage, ctx.HasStaticRoutes)
	}
}

func TestCheckManifestRejectsBadResourcesAndRouteTargets(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"
resources = { memory = "512MB", cpus = 0 }

[routes.app]
host = "api.example.com"
process = "web"
redirect = "https://example.com"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{
		`[processes.web].resources.memory "512MB" must match ^[1-9][0-9]*(k|m|g)$`,
		`[processes.web].resources.cpus must be positive`,
		`[routes.app] must set exactly one of process, serve, or redirect`,
	}
	for _, want := range wants {
		if !slices.Contains(errors, want) {
			t.Fatalf("expected %q in %v", want, errors)
		}
	}
}

func TestCheckManifestRejectsRootPath(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
path = "/"
process = "web"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(errors, "[routes.app].path must be omitted for the host root") {
		t.Fatalf("missing root path error: %v", errors)
	}
}

func TestCheckManifestRejectsDuplicateRoutesAndTLSConflicts(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"

[routes.dup]
host = "api.example.com"
process = "web"

[routes.docs]
host = "api.example.com"
path = "/docs"
tls = "internal"
process = "web"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(errors, "[routes.dup] conflicts with [routes.app]: host/path already used") {
		t.Fatalf("missing duplicate route error: %v", errors)
	}
	if !slices.Contains(errors, "[routes.docs].tls conflicts with another route for host api.example.com") {
		t.Fatalf("missing TLS conflict error: %v", errors)
	}
}

func TestCheckManifestRejectsBadVars(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root)
	writeManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[vars]
DEBUG = true
BAD_REF = "@secret:"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(errors, `[env.production.vars].DEBUG must be a string; if you want "true", write it as a quoted string`) {
		t.Fatalf("missing DEBUG error: %v", errors)
	}
	if !slices.Contains(errors, `[env.production.vars].BAD_REF value starts with reserved prefix '@secret:', use the secret store instead`) {
		t.Fatalf("missing BAD_REF error: %v", errors)
	}
}

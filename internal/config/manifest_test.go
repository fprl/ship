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
	if err := os.WriteFile(filepath.Join(root, "ship.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeDockerfile(t *testing.T, root string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func validContainerManifest() string {
	return `name = "api"
box = "example.com"
production_branch = "stable"
release = "bun run migrate"
probe = "/health"
notify = "https://ntfy.sh/api"

[env]
LOG_LEVEL = "info"
DATABASE_URL = "@secret"
SMTP_URL = "@secret:MAIL_URL"

[processes]
web = { cmd = "bun run src/server.ts", port = 3000, resources = { memory = "512m", cpus = 0.5 } }
worker = { cmd = "bun run worker", preview = false }

[routes]
"api.example.com" = "web"
`
}

func TestCheckManifestAcceptsContainerV2(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\nEXPOSE 3000\n")
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
	if ctx.Server != "example.com" {
		t.Fatalf("box not loaded: %q", ctx.Server)
	}
	if ctx.ProductionBranch != "stable" {
		t.Fatalf("production_branch not loaded: %q", ctx.ProductionBranch)
	}
	if !ctx.NeedsImage || ctx.HasStaticRoutes {
		t.Fatalf("unexpected capabilities: needsImage=%v hasStaticRoutes=%v", ctx.NeedsImage, ctx.HasStaticRoutes)
	}
	web := ctx.Processes["web"]
	if web.Command != "bun run src/server.ts" || web.Port == nil || *web.Port != 3000 || !web.Preview {
		t.Fatalf("unexpected web process: %+v", web)
	}
	worker := ctx.Processes["worker"]
	if worker.Command != "bun run worker" || worker.Preview {
		t.Fatalf("preview flag not parsed: %+v", worker)
	}
	if web.Resources.Memory == nil || *web.Resources.Memory != "512m" {
		t.Fatalf("memory not loaded: %+v", web.Resources)
	}
	if web.Resources.CPUs == nil || *web.Resources.CPUs != 0.5 {
		t.Fatalf("cpus not loaded: %+v", web.Resources)
	}
	if ctx.Vars["LOG_LEVEL"] != "info" {
		t.Fatalf("[env] literal not loaded: %+v", ctx.Vars)
	}
	if ctx.SecretRefs["DATABASE_URL"] != "DATABASE_URL" || ctx.SecretRefs["SMTP_URL"] != "MAIL_URL" {
		t.Fatalf("@secret refs not loaded: %+v", ctx.SecretRefs)
	}
	if ctx.Release != "bun run migrate" || ctx.Probe != "/health" || ctx.Notify != "https://ntfy.sh/api" {
		t.Fatalf("top-level release/probe/notify not loaded: release=%q probe=%q notify=%q", ctx.Release, ctx.Probe, ctx.Notify)
	}
}

func TestCheckManifestRejectsUserAtBox(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\n")
	writeManifest(t, root, `name = "api"
box = "deploy@203.0.113.7"

[processes]
web = { port = 3000 }
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	want := `box must be a host, not user@host; remove the user part (use box = "203.0.113.7")`
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestLoadAppContextAppliesPreviewEnvOverlay(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\nEXPOSE 3000\n")
	writeManifest(t, root, `name = "api"
box = "example.com"

[env]
LOG_LEVEL = "info"
BASE_ONLY = "kept"
DATABASE_URL = "@secret:PROD_DB"

[env.preview]
LOG_LEVEL = "debug"
DATABASE_URL = "@secret:PREVIEW_DB"
API_TOKEN = "@secret"
SMTP_URL = "@secret:MAIL_URL"

[processes]
web = { port = 3000 }
`)

	prod, err := LoadAppContext(root, ProductionEnvName)
	if err != nil {
		t.Fatal(err)
	}
	if prod.Vars["LOG_LEVEL"] != "info" || prod.Vars["BASE_ONLY"] != "kept" {
		t.Fatalf("Production vars should use base [env] only: %+v", prod.Vars)
	}
	if prod.SecretRefs["DATABASE_URL"] != "PROD_DB" {
		t.Fatalf("Production secret refs should ignore [env.preview]: %+v", prod.SecretRefs)
	}
	if _, ok := prod.SecretRefs["API_TOKEN"]; ok {
		t.Fatalf("Production secret refs should not include preview-only key: %+v", prod.SecretRefs)
	}

	preview, err := LoadAppContext(root, "feat-x-ab12")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Vars["LOG_LEVEL"] != "debug" || preview.Vars["BASE_ONLY"] != "kept" {
		t.Fatalf("Preview vars should merge base with overlay winning: %+v", preview.Vars)
	}
	wants := map[string]string{
		"DATABASE_URL": "PREVIEW_DB",
		"API_TOKEN":    "API_TOKEN",
		"SMTP_URL":     "MAIL_URL",
	}
	for envKey, secretKey := range wants {
		if preview.SecretRefs[envKey] != secretKey {
			t.Fatalf("preview secret ref %s = %q, want %q in %+v", envKey, preview.SecretRefs[envKey], secretKey, preview.SecretRefs)
		}
	}
}

func TestReadManifestRejectsLegacyProcessRouteTable(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\n")
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = { process = "web" }
`)

	_, err := ReadManifest(root)
	if err == nil || !strings.Contains(err.Error(), `unknown route target field "process"`) {
		t.Fatalf("expected legacy process table rejection, got %v", err)
	}
}

func TestReadManifestRejectsVarsAndDeploy(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `name = "api"
box = "example.com"

[vars]
LOG_LEVEL = "info"

[deploy]
release = "bun run migrate"
`)
	_, err := ReadManifest(root)
	if err == nil {
		t.Fatal("expected strict decode error")
	}
	msg := err.Error()
	for _, field := range []string{"vars", "deploy"} {
		if !strings.Contains(msg, field) {
			t.Fatalf("expected error to mention %q, got %v", field, err)
		}
	}
}

func TestReadManifestRejectsProcessHealth(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = { port = 3000, health = "/health" }
`)
	_, err := ReadManifest(root)
	if err == nil || !strings.Contains(err.Error(), "health is not supported") {
		t.Fatalf("expected health rejection, got %v", err)
	}
}

func TestReadManifestRejectsDuplicateRouteKeys(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = "web"
"api.example.com" = { static = "dist" }
`)
	_, err := ReadManifest(root)
	if err == nil {
		t.Fatal("expected duplicate route key parse error")
	}
}

func TestCheckManifestAcceptsStaticAndRedirectRouteTargets(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs-dist"), 0755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, `name = "site"
box = "example.com"

[routes]
"example.com" = { static = "dist" }
"example.com/docs" = { static = "docs-dist" }
"www.example.com" = { redirect = "example.com" }
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
	if ctx.Routes["example.com/docs"].Path != "/docs" || ctx.Routes["example.com/docs"].Serve != "docs-dist" {
		t.Fatalf("static route not loaded: %+v", ctx.Routes["example.com/docs"])
	}
	if ctx.Routes["www.example.com"].Redirect != "example.com" {
		t.Fatalf("redirect route not loaded: %+v", ctx.Routes["www.example.com"])
	}
}

func TestCheckManifestDefaultsRoutedProcessPortFromSoleExpose(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\nEXPOSE 8080/tcp\n")
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = "bun run src/server.ts"
worker = { cmd = "bun run worker" }

[routes]
"api.example.com" = "web"
`)
	ctx, err := LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Processes["web"].Port == nil || *ctx.Processes["web"].Port != 8080 {
		t.Fatalf("routed process did not inherit EXPOSE port: %+v", ctx.Processes["web"])
	}
	if ctx.Processes["worker"].Port != nil {
		t.Fatalf("unrouted worker should stay portless: %+v", ctx.Processes["worker"])
	}
}

func TestCheckManifestDefaultsRoutedProcessPortTo3000WithoutSoleExpose(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\nEXPOSE 8080 9090\n")
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = "bun run src/server.ts"

[routes]
"api.example.com" = "web"
`)
	ctx, err := LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Processes["web"].Port == nil || *ctx.Processes["web"].Port != 3000 {
		t.Fatalf("routed process did not default to 3000: %+v", ctx.Processes["web"])
	}
}

func TestCheckManifestRejectsUnknownEnvSubtables(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\n")
	writeManifest(t, root, `name = "api"
box = "example.com"

[env.staging]
LOG_LEVEL = "debug"

[processes]
web = { port = 3000 }
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	want := "[env.staging] is not supported; only [env.preview] exists. Per-branch values ride branches or --branch secrets."
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsReservedEnvPreviewKey(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\n")
	writeManifest(t, root, `name = "api"
box = "example.com"

[env]
preview = "literal"

[processes]
web = { port = 3000 }
`)

	errors, _, err := CheckManifest(root, ProductionEnvName)
	if err != nil {
		t.Fatal(err)
	}
	want := "[env].preview is reserved for the [env.preview] overlay; choose another environment variable name"
	if !slices.Contains(errors, want) {
		t.Fatalf("expected %q, got %v", want, errors)
	}
}

func TestCheckManifestRejectsStaticEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, `name = "site"
box = "example.com"

[env]
DATABASE_URL = "@secret"

[routes]
"example.com" = { static = "dist" }
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(errors, "[env] is only supported for container apps") {
		t.Fatalf("expected static env error, got %v", errors)
	}
}

func TestReadManifestRejectsOldFields(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `name = "api"
runtime = "bun"
box = "example.com"

[services.web]
port = 3000
healthcheck = "/health"
`)
	_, err := ReadManifest(root)
	if err == nil {
		t.Fatal("expected strict decode error")
	}
	msg := err.Error()
	for _, field := range []string{"runtime", "services.web"} {
		if !strings.Contains(msg, field) {
			t.Fatalf("expected error to mention %q, got %v", field, err)
		}
	}
}

func TestReadManifestRejectsLegacyProcessRouteTableWithOtherTargets(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\n")
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = { process = "web", static = "dist" }
`)
	_, err := ReadManifest(root)
	if err == nil || !strings.Contains(err.Error(), `unknown route target field "process"`) {
		t.Fatalf("expected legacy process table rejection, got %v", err)
	}
}

func TestCheckManifestAcceptsMixedProcessAndStaticRoutes(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\nEXPOSE 3000\n")
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = "bun run src/server.ts"

[routes]
"api.example.com" = "web"
"api.example.com/docs" = { static = "dist" }
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

func TestCheckManifestRejectsServeSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target.html"), []byte("target"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../target.html", filepath.Join(root, "dist", "index.html")); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, `name = "site"
box = "example.com"

[routes]
"site.example.com" = { static = "dist" }
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(errors, `[routes."site.example.com"].static must not contain symlink "dist/index.html"`) {
		t.Fatalf("missing serve symlink error: %v", errors)
	}
}

func TestCheckManifestRejectsBadResourcesAndRouteTargets(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\n")
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = { port = 3000, resources = { memory = "512MB", cpus = 0 } }

[routes]
"api.example.com" = "missing"
"www.example.com" = { redirect = "https://example.com" }
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{
		`[processes.web].resources.memory "512MB" must match ^[1-9][0-9]*(k|m|g)$`,
		`[processes.web].resources.cpus must be positive`,
		`[routes."api.example.com"] references unknown process: missing`,
		`[routes."www.example.com"] redirect must be a hostname`,
	}
	for _, want := range wants {
		if !slices.Contains(errors, want) {
			t.Fatalf("expected %q in %v", want, errors)
		}
	}
}

func TestCheckManifestRejectsRootPath(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\n")
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = { port = 3000 }

[routes]
"api.example.com/" = "web"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(errors, `[routes."api.example.com/"] path must be omitted for the host root`) {
		t.Fatalf("missing root path error: %v", errors)
	}
}

func TestCheckManifestRejectsRouteMatcherSyntax(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\n")
	writeManifest(t, root, `name = "api"
box = "example.com"

[processes]
web = { port = 3000 }

[routes]
"api.example.com/docs*" = "web"
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(errors, `[routes."api.example.com/docs*"] path must not contain Caddy matcher syntax`) {
		t.Fatalf("missing matcher syntax error: %v", errors)
	}
}

func TestCheckManifestRejectsBadEnvProbeNotifyAndBranch(t *testing.T) {
	root := t.TempDir()
	writeDockerfile(t, root, "FROM alpine\n")
	writeManifest(t, root, `name = "api"
box = "example.com"
production_branch = "bad branch"
probe = "health"
notify = "ftp://example.com/hook"

[env]
DEBUG = true
BAD_REF = "@secret:"

[processes]
web = { port = 3000 }
`)

	errors, _, err := CheckManifest(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{
		`production_branch must be a valid git branch name`,
		`probe must start with /`,
		`notify must use http or https`,
		`[env].DEBUG must be a string; if you want "true", write it as a quoted string`,
		`[env].BAD_REF value starts with reserved prefix '@secret:', use a valid secret key`,
	}
	for _, want := range wants {
		if !slices.Contains(errors, want) {
			t.Fatalf("expected %q in %v", want, errors)
		}
	}
}

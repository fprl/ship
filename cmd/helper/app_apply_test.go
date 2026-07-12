package helper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/secrets"
	"golang.org/x/crypto/bcrypt"
)

func TestResolveEnvMergesLiteralsAndSecrets(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	if err := secrets.Put("api", "production", "db_url", []byte("postgres://x")); err != nil {
		t.Fatal(err)
	}
	got, err := resolveEnv("api", "production",
		map[string]string{"LOG_LEVEL": "info"},
		map[string]string{"DATABASE_URL": "db_url"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got["LOG_LEVEL"] != "info" || got["DATABASE_URL"] != "postgres://x" {
		t.Fatalf("unexpected resolved env: %v", got)
	}
}

func TestResolveEnvFailsOnMissingSecretBeforeAnyContainerStarts(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	_, err := resolveEnv("api", "production", nil, map[string]string{"DATABASE_URL": "db_url"})
	if err == nil {
		t.Fatal("expected error for missing @secret reference")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "db_url") {
		t.Fatalf("error should name the missing env-var and secret key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ship secret set") {
		t.Fatalf("error should point at `ship secret set`, got: %v", err)
	}
}

func TestResolveEnvPreviewUsesSharedPreviewNotProductionSecret(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	t.Setenv("SHIP_APPS_DIR", t.TempDir())
	preview := &identity.PreviewIdentity{Branch: "feat/x", SanitizedBranch: "feat-x", Env: "feat-x-ab12", Suffix: "ab12", LastShipAt: time.Now(), ExpiresAt: ptrTime(time.Now().Add(time.Hour))}
	writePreviewIdentityForResolveTest(t, "api", "feat-x-ab12", preview)
	if err := secrets.Put("api", "prod", "db_url", []byte("postgres://prod")); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put("api", sharedPreviewSecretsEnvName, "db_url", []byte("postgres://preview")); err != nil {
		t.Fatal(err)
	}

	got, err := resolveEnv("api", "feat-x-ab12", nil, map[string]string{"DATABASE_URL": "db_url"})
	if err != nil {
		t.Fatal(err)
	}
	if got["DATABASE_URL"] != "postgres://preview" {
		t.Fatalf("preview should use shared preview secret, got %+v", got)
	}
}

func TestResolveEnvPreviewBranchSecretWinsOverSharedPreview(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	t.Setenv("SHIP_APPS_DIR", t.TempDir())
	preview := &identity.PreviewIdentity{Branch: "feat/x", SanitizedBranch: "feat-x", Env: "feat-x-ab12", Suffix: "ab12", LastShipAt: time.Now(), ExpiresAt: ptrTime(time.Now().Add(time.Hour))}
	writePreviewIdentityForResolveTest(t, "api", "feat-x-ab12", preview)
	if err := secrets.Put("api", sharedPreviewSecretsEnvName, "db_url", []byte("postgres://preview")); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put("api", "feat-x-ab12", "db_url", []byte("postgres://branch")); err != nil {
		t.Fatal(err)
	}

	got, err := resolveEnv("api", "feat-x-ab12", nil, map[string]string{"DATABASE_URL": "db_url"})
	if err != nil {
		t.Fatal(err)
	}
	if got["DATABASE_URL"] != "postgres://branch" {
		t.Fatalf("branch secret should win over shared preview, got %+v", got)
	}
}

func TestResolveEnvPreviewMissingSecretUsesScopedRemediation(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	t.Setenv("SHIP_APPS_DIR", t.TempDir())
	preview := &identity.PreviewIdentity{Branch: "feat/x", SanitizedBranch: "feat-x", Env: "feat-x-ab12", Suffix: "ab12", LastShipAt: time.Now(), ExpiresAt: ptrTime(time.Now().Add(time.Hour))}
	writePreviewIdentityForResolveTest(t, "api", "feat-x-ab12", preview)
	_, err := resolveEnv("api", "feat-x-ab12", nil, map[string]string{"DATABASE_URL": "db_url"})
	if err == nil {
		t.Fatal("expected preview missing secret error")
	}
	if !errcat.Is(err, errcat.CodeSecretMissing) ||
		!strings.Contains(err.Error(), "ship secret set db_url [--preview|--branch <name>]") {
		t.Fatalf("unexpected missing secret error: %v", err)
	}
}

func TestResolveEnvDoesNotMutateInputMaps(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	_ = secrets.Put("api", "production", "k", []byte("v"))
	literals := map[string]string{"L": "lit"}
	refs := map[string]string{"R": "k"}
	if _, err := resolveEnv("api", "production", literals, refs); err != nil {
		t.Fatal(err)
	}
	if _, ok := literals["R"]; ok {
		t.Fatal("resolveEnv leaked resolved secrets back into the literals map")
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func writePreviewIdentityForResolveTest(t *testing.T, app, env string, preview *identity.PreviewIdentity) {
	t.Helper()
	path := identity.IdentityFile(app, env)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	file := identity.EnvIdentity{
		Version: 1,
		App:     app,
		Env:     env,
		InfraID: identity.InfraID(app, env),
		Preview: preview,
	}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateReleaseRejectsPathTraversal(t *testing.T) {
	for _, release := range []string{"abc1234", "abc1234-s012345abcdef", "abc1234-dirty-20260530t143012000000000z", "abc1234-dirty-20260530t143012000000000z-s012345abcdef"} {
		if err := validateRelease(release); err != nil {
			t.Fatalf("expected %q to be valid: %v", release, err)
		}
	}
	for _, release := range []string{"", "abc123", "../abc", "abc/def", "ABC123", "abc_def", "abc.def", "dirty-20260528123456", "abc1234-dirty-20260530T143012Z", "abc1234-dirty-20260530t143012z"} {
		if err := validateRelease(release); err == nil {
			t.Fatalf("expected %q to be invalid", release)
		}
	}
}

func TestReleaseMetadataValidation(t *testing.T) {
	meta, err := newReleaseMetadata("abc1234-dirty-20260530t143012000000000z", true, "abc1234abc1234abc1234abc1234abc1234abc1234", "2026-05-30T14:30:12Z")
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Dirty || meta.Release != "abc1234-dirty-20260530t143012000000000z" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	staticMeta, err := newReleaseMetadata("abc1234-s012345abcdef", false, "abc1234abc1234abc1234abc1234abc1234abc1234", "2026-05-30T14:30:12Z")
	if err != nil {
		t.Fatalf("expected clean static release metadata to pass: %v", err)
	}
	if staticMeta.StaticHash != "012345abcdef" {
		t.Fatalf("expected static hash in metadata, got %+v", staticMeta)
	}
	if _, err := newReleaseMetadata("abc1234-dirty-20260530t143012000000000z-s012345abcdef", true, "abc1234abc1234abc1234abc1234abc1234abc1234", "2026-05-30T14:30:12Z"); err != nil {
		t.Fatalf("expected dirty static release metadata to pass: %v", err)
	}
	if _, err := newReleaseMetadata("ABC", false, "abc1234", "2026-05-30T14:30:12Z"); err == nil {
		t.Fatal("expected invalid release metadata to fail")
	}
	if _, err := newReleaseMetadata("abc1234", false, "not-a-sha", "2026-05-30T14:30:12Z"); err == nil {
		t.Fatal("expected invalid base commit to fail")
	}
	if _, err := newReleaseMetadata("abc1234-dirty-20260530t143012000000000z", false, "abc1234", "2026-05-30T14:30:12Z"); err == nil {
		t.Fatal("expected dirty metadata mismatch to fail")
	}
	if _, err := newReleaseMetadata("abc1234-dirty-20260530t143013000000000z", true, "abc1234abc1234abc1234abc1234abc1234abc1234", "2026-05-30T14:30:12Z"); err == nil {
		t.Fatal("expected dirty timestamp mismatch to fail")
	}
	if _, err := newReleaseMetadata("def1234-dirty-20260530t143012000000000z", true, "abc1234abc1234abc1234abc1234abc1234abc1234", "2026-05-30T14:30:12Z"); err == nil {
		t.Fatal("expected dirty base commit mismatch to fail")
	}
	if _, err := newReleaseMetadata("abc1234", false, "abc1234", "not-a-time"); err == nil {
		t.Fatal("expected invalid created_at to fail")
	}
}

func TestReadReleaseMetadataRequiresFile(t *testing.T) {
	_, err := readReleaseMetadata("missing-release-metadata-test", "production", "abc1234")
	if err == nil || !strings.Contains(err.Error(), "read release metadata") {
		t.Fatalf("expected missing release metadata error, got %v", err)
	}
}

func TestApplyRejectsManifestForDifferentApp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ship.toml"), []byte(`name = "other"
box = "example.com"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = "web"
`), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := (appApplyCmd{App: "api", Env: "production"}).loadApplyContext(root)
	if err == nil || !strings.Contains(err.Error(), "uploaded manifest names app other, expected api") {
		t.Fatalf("expected app mismatch error, got %v", err)
	}
}

func TestPodmanBuildArgsLabelsWithDerivedIdentity(t *testing.T) {
	args := podmanBuildArgs("api", "production", identity.ImageTag("api", "production", "abc123"), "abc123", "/tmp/Dockerfile", "/tmp/ctx", false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"build",
		"-t " + identity.ImageTag("api", "production", "abc123"),
		"--label ship.app=api",
		"--label ship.env=production",
		"--label ship.infra_id=" + identity.InfraID("api", "production"),
		"--label ship.release=abc123",
		"-f /tmp/Dockerfile",
		"/tmp/ctx",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("build args missing %q: %s", want, joined)
		}
	}
}

func TestContainersForRemovedProcesses(t *testing.T) {
	entries := []containerEntry{
		{Names: []string{"web-old"}, Labels: map[string]string{"ship.process": "web"}},
		{Names: []string{"worker-old"}, Labels: map[string]string{"ship.process": "worker"}},
		{Names: []string{"release-job"}, Labels: map[string]string{"ship.process": "release"}},
	}
	got := containersForRemovedProcesses(entries, map[string]config.Process{"web": {}})
	if len(got) != 1 || got[0] != "worker-old" {
		t.Fatalf("unexpected stale containers: %+v", got)
	}
}

func TestContainersOutsideDesiredRelease(t *testing.T) {
	desired := identity.ContainerName("api", "production", "web", "abc123")
	entries := []containerEntry{
		{Names: []string{desired}, Labels: map[string]string{"ship.process": "web"}},
		{Names: []string{"stale-web"}, Labels: map[string]string{"ship.process": "web"}},
		{Names: []string{"stale-worker"}, Labels: map[string]string{"ship.process": "worker"}},
	}
	got := containersOutsideDesiredRelease(entries, "api", "production", map[string]config.Process{"web": {}}, "abc123")
	if len(got) != 2 || got[0] != "stale-web" || got[1] != "stale-worker" {
		t.Fatalf("unexpected restore cleanup set: %+v", got)
	}
}

func TestNextProcessContainerNameUsesInstanceWhenDefaultExists(t *testing.T) {
	base := identity.ContainerName("api", "production", "web", "abc123")
	got := nextProcessContainerName([]containerEntry{
		{Names: []string{base}, Labels: map[string]string{"ship.process": "web", "ship.release": "abc123"}},
	}, "api", "production", "web", "abc123", "20260530t143012000000000z")
	want := identity.ContainerInstanceName("api", "production", "web", "abc123", "20260530t143012000000000z")
	if got != want {
		t.Fatalf("expected instance container %q, got %q", want, got)
	}
}

func TestNextProcessContainerNameUsesDefaultWhenFree(t *testing.T) {
	got := nextProcessContainerName(nil, "api", "production", "web", "abc123", "20260530t143012000000000z")
	want := identity.ContainerName("api", "production", "web", "abc123")
	if got != want {
		t.Fatalf("expected default container %q, got %q", want, got)
	}
}

func TestPodmanBuildArgsRebuildBypassesCacheAndPullsBases(t *testing.T) {
	args := podmanBuildArgs("api", "production", identity.ImageTag("api", "production", "abc123"), "abc123", "/tmp/Dockerfile", "/tmp/ctx", true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--no-cache --pull=always") {
		t.Fatalf("rebuild should pass --no-cache and --pull=always together: %s", joined)
	}
}

func TestBuildPodmanRunArgsEmitsHardeningDataMountResourcesAndLabels(t *testing.T) {
	memory := "512m"
	cpus := 0.5
	proc := config.Process{
		Command:   "/usr/bin/myserver --foo",
		Resources: config.Resources{Memory: &memory, CPUs: &cpus},
	}
	containerName := identity.ContainerName("api", "production", "web", "abc123")
	args := buildPodmanRunArgs("api", "production", "web", proc, identity.ImageTag("api", "production", "abc123"), "999", "988", "abc123", containerName, true, false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"--pids-limit 512",
		"--read-only",
		"--tmpfs /tmp:size=64m,mode=1777",
		"--user 999:988",
		"--network " + identity.Network("api", "production"),
		"--network ingress",
		"-v " + identity.DataDir("api", "production") + ":/data:Z",
		"--env-file " + identity.EnvFile("api", "production"),
		"--memory 512m",
		"--cpus 0.5",
		"--label ship.process=web",
		"--label ship.release=abc123",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in args:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, identity.ImageTag("api", "production", "abc123")+" /bin/sh -c") {
		t.Fatalf("image should precede command override:\n%s", joined)
	}
}

func TestBuildPodmanRunArgsSkipsEnvFileWhenAbsent(t *testing.T) {
	args := buildPodmanRunArgs("api", "production", "web", config.Process{}, "img:tag", "999", "988", "abc123", identity.ContainerName("api", "production", "web", "abc123"), false, false)
	for _, a := range args {
		if a == "--env-file" {
			t.Fatalf("did not expect --env-file when env file is absent, args:\n%s", strings.Join(args, " "))
		}
	}
}

func TestBuildPodmanRunArgsAppliesDefaultPreviewResourceCaps(t *testing.T) {
	args := buildPodmanRunArgs("api", "feat-x-ab12", "web", config.Process{}, "img:tag", "999", "988", "abc123", identity.ContainerName("api", "feat-x-ab12", "web", "abc123"), false, true)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--memory 512m", "--cpus 0.5"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("preview args missing %q:\n%s", want, joined)
		}
	}
}

func TestBuildPodmanRunArgsLeavesProdUncappedByDefault(t *testing.T) {
	args := buildPodmanRunArgs("api", "prod", "web", config.Process{}, "img:tag", "999", "988", "abc123", identity.ContainerName("api", "prod", "web", "abc123"), false, false)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--memory") || strings.Contains(joined, "--cpus") {
		t.Fatalf("prod args should not get default resource caps:\n%s", joined)
	}
}

func TestBuildPodmanExecRunArgsTTYOnlyWhenRequested(t *testing.T) {
	command := []string{"env"}
	injected := map[string]string{"SHIP_RELEASE": "abc123"}
	base := buildPodmanExecRunArgs("api", "feat-x", "exec-name", identity.ImageTag("api", "feat-x", "abc123"), "999", "988", "abc123", command, injected, true, true, false)
	joined := strings.Join(base, " ")
	if strings.Contains(joined, " -t ") {
		t.Fatalf("non-tty exec args should not request a tty: %s", joined)
	}
	if !strings.Contains(joined, "run --rm -i") {
		t.Fatalf("exec args should keep stdin open without tty: %s", joined)
	}
	if !strings.Contains(joined, "--memory 512m") || !strings.Contains(joined, "--cpus 0.5") {
		t.Fatalf("preview exec args should include default caps: %s", joined)
	}
	if !strings.Contains(joined, "--env-file "+identity.EnvFile("api", "feat-x")) {
		t.Fatalf("exec args should include the runtime env file: %s", joined)
	}

	withTTY := buildPodmanExecRunArgs("api", "feat-x", "exec-name", identity.ImageTag("api", "feat-x", "abc123"), "999", "988", "abc123", command, injected, true, true, true)
	joinedTTY := strings.Join(withTTY, " ")
	if !strings.Contains(joinedTTY, " -t ") {
		t.Fatalf("tty exec args should request a tty: %s", joinedTTY)
	}
}

func TestRenderEnvFileEmitsSortedKeyValuePairs(t *testing.T) {
	got := renderEnvFile(map[string]string{"LOG_LEVEL": "info", "DEBUG": "false", "PORT": "3000"})
	want := "DEBUG=false\nLOG_LEVEL=info\nPORT=3000\n"
	if got != want {
		t.Fatalf("renderEnvFile mismatch:\nwant: %q\n got: %q", want, got)
	}
}

func TestRenderEnvFileEmptyMapProducesEmptyString(t *testing.T) {
	if got := renderEnvFile(nil); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestRenderAppCaddyfileProcessRouteUsesVersionedContainerDNS(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		Processes: map[string]config.Process{"web": {Port: &port}},
		Routes: map[string]config.Route{
			"app": {Host: "api.example.com", Process: "web"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("api", "production", ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"api.example.com" {`) {
		t.Fatalf("expected quoted host block, got:\n%s", got)
	}
	want := "reverse_proxy http://" + identity.ContainerName("api", "production", "web", "abc123") + ":3000"
	if !strings.Contains(got, want) {
		t.Fatalf("expected versioned container reverse_proxy %q, got:\n%s", want, got)
	}
}

func TestRenderAppCaddyfileProtectsPreviewButNeverProduction(t *testing.T) {
	port := 3000
	base := &config.AppContext{
		PreviewProtected:   true,
		PreviewPassword:    "team-password",
		PreviewBypassToken: "bypass-token",
		Processes:          map[string]config.Process{"web": {Port: &port}},
		Routes:             map[string]config.Route{"app": {Host: "api.example.com", Process: "web"}},
	}
	preview, err := renderAppCaddyfileWithProcessNames("api", "feat-x-ab12", base, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview, "basic_auth @ship_auth") || !strings.Contains(preview, "not header x-ship-bypass \"bypass-token\"") {
		t.Fatalf("protected preview fragment missing auth+bypass directives:\n%s", preview)
	}
	foundHash := false
	for _, field := range strings.Fields(preview) {
		if strings.HasPrefix(field, "\"$2") {
			hash := strings.Trim(field, "\"")
			if bcrypt.CompareHashAndPassword([]byte(hash), []byte("team-password")) != nil {
				t.Fatalf("basic_auth hash does not match password: %q", hash)
			}
			foundHash = true
			break
		}
	}
	if !foundHash {
		t.Fatalf("protected preview basic_auth hash missing:\n%s", preview)
	}
	prod, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, base, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prod, "basic_auth") || strings.Contains(prod, "ship_auth") {
		t.Fatalf("production fragment must never be protected:\n%s", prod)
	}
}

func TestRenderAppCaddyfileCanUseSpecificProcessContainerName(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		Processes: map[string]config.Process{"web": {Port: &port}},
		Routes: map[string]config.Route{
			"app": {Host: "api.example.com", Process: "web"},
		},
	}
	upstream := identity.ContainerInstanceName("api", "production", "web", "abc123", "20260530t143012000000000z")
	got, err := renderAppCaddyfileWithProcessNames("api", "production", ctx, "abc123", map[string]string{"web": upstream})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "reverse_proxy http://"+upstream+":3000") {
		t.Fatalf("expected Caddy to point at specific container %q, got:\n%s", upstream, got)
	}
}

func TestRenderAppCaddyfileStaticPathUsesHandlePath(t *testing.T) {
	ctx := &config.AppContext{
		Routes: map[string]config.Route{
			"docs": {Host: "example.com", Path: "/docs", Serve: "docs-dist"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("site", "production", ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "handle_path /docs/*") {
		t.Fatalf("expected handle_path for static prefix, got:\n%s", got)
	}
	if !strings.Contains(got, `root * "/var/apps/site.production/static/releases/abc123/docs"`) {
		t.Fatalf("expected static route root, got:\n%s", got)
	}
	if !strings.Contains(got, "file_server") {
		t.Fatalf("expected file_server, got:\n%s", got)
	}
}

func TestRenderAppCaddyfileOrdersLongestPathFirst(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		Processes: map[string]config.Process{"web": {Port: &port}},
		Routes: map[string]config.Route{
			"root": {Host: "example.com", Process: "web"},
			"docs": {Host: "example.com", Path: "/docs", Process: "web"},
			"api":  {Host: "example.com", Path: "/docs/api", Process: "web"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("api", "production", ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	apiIdx := strings.Index(got, "handle /docs/api {")
	docsIdx := strings.Index(got, "handle /docs {")
	rootIdx := strings.Index(got, "\thandle {")
	if apiIdx < 0 || docsIdx < 0 || rootIdx < 0 {
		t.Fatalf("missing expected handle blocks:\n%s", got)
	}
	if !(apiIdx < docsIdx && docsIdx < rootIdx) {
		t.Fatalf("expected longest paths before shorter paths:\n%s", got)
	}
}

func TestRenderAppCaddyfileMixedRoutesUseOneRelease(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		Processes: map[string]config.Process{"web": {Port: &port}},
		Routes: map[string]config.Route{
			"app":  {Host: "example.com", Process: "web"},
			"docs": {Host: "example.com", Path: "/docs", Serve: "docs-dist"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("api", "production", ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "reverse_proxy http://"+identity.ContainerName("api", "production", "web", "abc123")+":3000") {
		t.Fatalf("expected process route to use release container:\n%s", got)
	}
	if !strings.Contains(got, `root * "/var/apps/api.production/static/releases/abc123/docs"`) {
		t.Fatalf("expected static route to use release-pinned root:\n%s", got)
	}
}

func TestRenderAppCaddyfileGroupsEmptyTLSWithAuto(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		Processes: map[string]config.Process{"web": {Port: &port}},
		Routes: map[string]config.Route{
			"app":  {Host: "example.com", Process: "web"},
			"docs": {Host: "example.com", Path: "/docs", Serve: "docs-dist", TLS: "auto"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("api", "production", ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, `"example.com" {`) != 1 {
		t.Fatalf("expected one host block for empty/auto TLS routes:\n%s", got)
	}
}

func TestRenderAppCaddyfileEmitsInternalTLS(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		Processes: map[string]config.Process{"web": {Port: &port}},
		Routes: map[string]config.Route{
			"app": {Host: "example.com", Process: "web", TLS: "internal"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("api", "production", ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\ttls internal\n") {
		t.Fatalf("expected internal TLS directive:\n%s", got)
	}
}

func TestRenderAppCaddyfileRedirectRouteQuotesTarget(t *testing.T) {
	ctx := &config.AppContext{
		Routes: map[string]config.Route{
			"r": {Host: "old.example.com", Redirect: "new.example.com"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("api", "production", ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `redir "https://new.example.com{uri}" permanent`) {
		t.Fatalf("expected quoted redir directive, got:\n%s", got)
	}
}

func TestRenderAppCaddyfileRejectsProcessWithoutPort(t *testing.T) {
	ctx := &config.AppContext{
		Processes: map[string]config.Process{"worker": {}},
		Routes: map[string]config.Route{
			"r": {Host: "x.example.com", Process: "worker"},
		},
	}
	if _, err := renderAppCaddyfileWithProcessNames("api", "production", ctx, "abc123", nil); err == nil {
		t.Fatal("expected error for process route pointing at portless process")
	}
}

func TestSnapshotAndRestoreCaddyFragmentRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frag.caddy")
	original := []byte("\"api.example.com\" {\n\treverse_proxy http://x:3000\n}\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	snapshot, existed, err := snapshotCaddyFragment(path)
	if err != nil {
		t.Fatal(err)
	}
	if !existed || string(snapshot) != string(original) {
		t.Fatalf("snapshot mismatch existed=%v snapshot=%q", existed, snapshot)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := restoreCaddyFragment(path, snapshot, existed); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("restore mismatch:\nwant: %q\n got: %q", original, got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("restored plain Caddy fragment mode = %o, want 0644", info.Mode().Perm())
	}
}

func TestRestoreCaddyFragmentKeepsProtectedFragmentRootOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frag.caddy")
	protected := []byte("\"pv.example.com\" {\n\t@ship_auth {\n\t\tnot header x-ship-bypass \"tok\"\n\t}\n\tbasic_auth @ship_auth {\n\t\tteam \"hash\"\n\t}\n\treverse_proxy http://x:3000\n}\n")
	if err := os.WriteFile(path, []byte("garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := restoreCaddyFragment(path, protected, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("restored protected Caddy fragment mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestRestoreCaddyFragmentRemovesWhenNoPreviousExisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.caddy")
	if err := os.WriteFile(path, []byte("bad fragment"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := restoreCaddyFragment(path, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected fragment removed, stat err = %v", err)
	}
}

func TestRestoreStaticCurrentRoundTripsSymlink(t *testing.T) {
	staticRoot := t.TempDir()
	previous := filepath.Join(staticRoot, "releases", "old")
	next := filepath.Join(staticRoot, "releases", "next")
	if err := os.MkdirAll(previous, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(next, 0755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(staticRoot, "current")
	if err := os.Symlink(previous, current); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotStaticCurrentAt(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(next, current); err != nil {
		t.Fatal(err)
	}
	if err := restoreStaticCurrentAt(current, snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(current)
	if err != nil {
		t.Fatal(err)
	}
	if got != previous {
		t.Fatalf("current symlink = %q, want %q", got, previous)
	}
}

func TestValidateAppEnvAcceptsCanonicalNames(t *testing.T) {
	if err := validateAppEnv("api", "production"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateAppEnv("multi-word-app", "stage-2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateAppEnv("api", "1-preview-ab12"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAppEnvRejectsInvalidAppNamesAndEnvPunctuation(t *testing.T) {
	for _, name := range []string{"1bad", "-bad", "bad name", "BAD"} {
		if err := validateAppEnv(name, "production"); err == nil {
			t.Fatalf("expected error for app=%q", name)
		}
	}
	for _, name := range []string{"-bad", "bad name", "BAD"} {
		if err := validateAppEnv("good", name); err == nil {
			t.Fatalf("expected error for env=%q", name)
		}
	}
}

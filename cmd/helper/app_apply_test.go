package helper

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/deployrequest"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/secrets"
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
	preview := &identity.PreviewIdentity{Branch: "feat/x", LastShipAt: time.Now(), ExpiresAt: ptrTime(time.Now().Add(time.Hour))}
	writePreviewIdentityForResolveTest(t, "api", "feat-x-ab12", preview)
	if err := secrets.Put("api", "production", "db_url", []byte("postgres://prod")); err != nil {
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
	preview := &identity.PreviewIdentity{Branch: "feat/x", LastShipAt: time.Now(), ExpiresAt: ptrTime(time.Now().Add(time.Hour))}
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
	preview := &identity.PreviewIdentity{Branch: "feat/x", LastShipAt: time.Now(), ExpiresAt: ptrTime(time.Now().Add(time.Hour))}
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

func TestCaddyStageActionErrorPassesThroughOtherErrors(t *testing.T) {
	if err := caddyStageActionError(nil, "deploy"); err != nil {
		t.Fatalf("nil error = %v", err)
	}
	original := errors.New("unrelated failure")
	if got := caddyStageActionError(original, "deploy"); got != original {
		t.Fatalf("pass-through error = %v, want original", got)
	}
}

func TestRecordDeployFailureKeepsProbeErrorWhenJournalAppendFails(t *testing.T) {
	t.Setenv("SHIP_APPS_DIR", t.TempDir())
	journalPath := identity.DeployJournalFile("api", "production")
	if err := os.MkdirAll(journalPath, 0755); err != nil {
		t.Fatal(err)
	}

	probeErr := newJournalStepError("probe", errors.New("health check failed"), nil, nil)
	cmd := appApplyCmd{Request: deployrequest.Request{App: "api", Env: "production", SHA: "abc1234"}}
	var got error
	stderr := captureStderr(t, func() {
		got = cmd.recordDeployFailure(nil, "", time.Now().UTC(), probeErr)
	})
	if got != probeErr {
		t.Fatalf("returned error = %v, want original probe error %v", got, probeErr)
	}
	if !errcat.Is(applyExitError(got), errcat.CodeProbeFailed) {
		t.Fatalf("apply exit error = %v, want probe_failed", applyExitError(got))
	}
	if !strings.Contains(stderr, "warning: failed to write deploy journal:") || !strings.Contains(stderr, "next: ship box doctor") {
		t.Fatalf("journal append warning = %q", stderr)
	}
}

func TestCommittedDeployErrorsCarryStableCodesAndConvergeNextStep(t *testing.T) {
	cmd := appApplyCmd{Request: deployrequest.Request{App: "api", Env: "production", SHA: "new222"}}
	for _, tt := range []struct {
		name string
		code errcat.Code
		call func(error) error
		want string
	}{
		{
			name: "unconverged",
			code: errcat.CodeDeployCommittedUnconverged,
			call: func(err error) error { return cmd.recordCommittedUnconverged(nil, "old111", time.Now().UTC(), err) },
			want: "committed but not converged",
		},
		{
			name: "degraded",
			code: errcat.CodeDeployCommittedDegraded,
			call: func(err error) error { return cmd.recordCommittedDegraded(nil, "old111", time.Now().UTC(), err) },
			want: "committed but degraded",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldAppend := appendDeployJournal
			appendDeployJournal = func(string, string, activationrecords.JournalEntry, []string) error { return nil }
			t.Cleanup(func() { appendDeployJournal = oldAppend })

			err := tt.call(errors.New("caddy unavailable"))
			if !errcat.Is(err, tt.code) {
				t.Fatalf("error = %v, want %s", err, tt.code)
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "next: ship converge") {
				t.Fatalf("error = %q, want human wording and converge next step", err)
			}
		})
	}
}

func TestCompleteCommittedDeployWarnsButDoesNotAbortWhenJournalAppendFails(t *testing.T) {
	setupJournalHostTest(t)
	previous := activationrecords.JournalEntry{
		Outcome:          "failed",
		StartedAt:        "2026-07-14T09:59:00Z",
		EndedAt:          "2026-07-14T09:59:01Z",
		AttemptedRelease: "old111",
		FailingStep:      "probe",
	}
	if err := appendDeployJournalEntry("api", "production", previous, nil); err != nil {
		t.Fatal(err)
	}
	sink := newWebhookTestSink(t)
	oldAppend := appendDeployJournal
	appendDeployJournal = func(string, string, activationrecords.JournalEntry, []string) error {
		return errors.New("journal disk is read-only")
	}
	t.Cleanup(func() {
		appendDeployJournal = oldAppend
	})

	cmd := appApplyCmd{Request: deployrequest.Request{App: "api", Env: "production", SHA: "new222"}}
	app := &config.AppContext{Webhook: sink.URL, ProductionBranch: "main"}
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureApplyStdout(t, func() {
			if err := cmd.completeCommittedDeploy(app, "old111", time.Now().UTC(), applyReleaseResult{}); err != nil {
				t.Fatal(err)
			}
		})
	})
	if !strings.Contains(stdout, "Deployed api (production) at new222\n") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "warning: deployed but failed to write deploy journal ") || !strings.Contains(stderr, "cleanup/GC were skipped; next: ship box doctor") {
		t.Fatalf("stderr = %q", stderr)
	}
	entries, err := readDeployJournalEntries("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Outcome != "failed" {
		t.Fatalf("journal entries = %+v, want only prior failure", entries)
	}
	if len(sink.bodies) != 0 {
		t.Fatalf("journal failure emitted a recovery webhook")
	}
}

func TestCommittedJournalFailureSkipsCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	logPath := filepath.Join(root, "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" >> \"$PODMAN_LOG\"\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldAppend := appendDeployJournal
	appendDeployJournal = func(string, string, activationrecords.JournalEntry, []string) error {
		return errors.New("journal disk is read-only")
	}
	t.Cleanup(func() { appendDeployJournal = oldAppend })
	cmd := appApplyCmd{Request: deployrequest.Request{App: "api", Env: "production", SHA: "new222"}}
	if err := cmd.completeCommittedDeploy(&config.AppContext{}, "old111", time.Now().UTC(), applyReleaseResult{containersToRemove: []string{"old-container"}}); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "rm -f old-container") {
		t.Fatalf("cleanup ran after journal failure: %s", log)
	}
}

func TestPreCommitCleanupRemovesOnlyAttemptCandidates(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	logPath := filepath.Join(root, "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
printf '%s\n' "$*" >> "$PODMAN_LOG"
if [ "$1" = "ps" ]; then
  printf '%s\n' '[{"Names":["candidate"],"State":"running","Labels":{"ship.activation":"attempt-a1","ship.process":"web"}},{"Names":["serving"],"State":"running","Labels":{"ship.activation":"serving-a1","ship.process":"web"}}]'
fi
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	removePreparedCandidates("api", "production", "attempt-a1")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, "rm -f candidate") {
		t.Fatalf("pre-commit cleanup did not remove candidate: %s", log)
	}
	if strings.Contains(log, "rm -f serving") {
		t.Fatalf("pre-commit cleanup touched serving release: %s", log)
	}
}

func captureApplyStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
	}()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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
	_, err := (&appApplyCmd{Request: deployrequest.Request{App: "api", Env: "production"}}).loadApplyContext(root)
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
		"--label ship.release=abc123",
		"-f /tmp/Dockerfile",
		"/tmp/ctx",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("build args missing %q: %s", want, joined)
		}
	}
}

func TestPodmanBuildArgsCarriesReleaseEnvelopeLabel(t *testing.T) {
	value := "YWJjLWVudmVsb3Bl"
	args := podmanBuildArgsWithEnvelope("api", "production", "ship/api:abc123", "abc123", "/tmp/Dockerfile", "/tmp/ctx", false, value)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--label ship.release_envelope="+value) {
		t.Fatalf("build args missing release envelope label: %s", joined)
	}
}

func TestApplyStaticPublishesHashNamedReleaseWithoutTouchingOldBytes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	releaseDir := filepath.Join(identity.StaticDir("api", "production"), "releases", "abc1234-deadbeef")
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(releaseDir, "still-active")
	if err := os.WriteFile(sentinel, []byte("serving"), 0644); err != nil {
		t.Fatal(err)
	}
	ctxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ctxDir, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "dist", "index.html"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	meta, err := newReleaseMetadata("abc1234", false, "abc1234", "2026-07-16T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	env, _, err := releaseEnvelope([]byte("name = \"api\"\nbox = \"example.com\"\n\n[routes]\n\"site.example.com\" = { static = \"dist\" }\n"), meta)
	if err != nil {
		t.Fatal(err)
	}
	cmd := appApplyCmd{Request: deployrequest.Request{App: "api", Env: "production", SHA: "abc1234"}, Envelope: env}
	_, _, err = cmd.applyStatic(ctxDir, &config.AppContext{
		Routes: map[string]config.Route{"site": {Serve: "dist"}},
	})
	if err != nil {
		t.Fatalf("applyStatic error = %v", err)
	}
	if data, readErr := os.ReadFile(sentinel); readErr != nil || string(data) != "serving" {
		t.Fatalf("old release changed after prepare: %q, %v", data, readErr)
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
	envFile := identity.ActivationEnvFile("api", "production", "abc123-00112233")
	args := buildPodmanRunArgsWithActivation("api", "production", "web", proc, identity.ImageTag("api", "production", "abc123"), "999", "988", "abc123", "", containerName, true, false, envFile)
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
		"--env-file " + envFile,
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

func TestBuildPodmanRunArgsMatchesActivationShape(t *testing.T) {
	args := buildPodmanRunArgsWithActivation("api", "production", "web", config.Process{}, "img:tag", "999", "988", "abc123", "abc123-a1b2", "web-new", false, false, "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--label ship.release=abc123") || !strings.Contains(joined, "--label ship.activation=abc123-a1b2") {
		t.Fatalf("runtime args do not carry exact release/activation identity: %s", joined)
	}
}

func TestBuildPodmanRunArgsSkipsEnvFileWhenAbsent(t *testing.T) {
	args := buildPodmanRunArgsWithActivation("api", "production", "web", config.Process{}, "img:tag", "999", "988", "abc123", "", identity.ContainerName("api", "production", "web", "abc123"), false, false, "")
	for _, a := range args {
		if a == "--env-file" {
			t.Fatalf("did not expect --env-file when env file is absent, args:\n%s", strings.Join(args, " "))
		}
	}
}

func TestBuildPodmanRunArgsAppliesDefaultPreviewResourceCaps(t *testing.T) {
	args := buildPodmanRunArgsWithActivation("api", "feat-x-ab12", "web", config.Process{}, "img:tag", "999", "988", "abc123", "", identity.ContainerName("api", "feat-x-ab12", "web", "abc123"), false, true, "")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--memory 512m", "--cpus 0.5"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("preview args missing %q:\n%s", want, joined)
		}
	}
}

func TestBuildPodmanRunArgsLeavesProdUncappedByDefault(t *testing.T) {
	args := buildPodmanRunArgsWithActivation("api", "production", "web", config.Process{}, "img:tag", "999", "988", "abc123", "", identity.ContainerName("api", "production", "web", "abc123"), false, false, "")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--memory") || strings.Contains(joined, "--cpus") {
		t.Fatalf("production args should not get default resource caps:\n%s", joined)
	}
}

func TestBuildPodmanExecRunArgsTTYOnlyWhenRequested(t *testing.T) {
	command := []string{"env"}
	injected := map[string]string{"SHIP_RELEASE": "abc123"}
	envFile := identity.ActivationEnvFile("api", "feat-x", "abc123-00112233")
	base := buildPodmanExecRunArgsWithActivation("api", "feat-x", "exec-name", identity.ImageTag("api", "feat-x", "abc123"), "999", "988", "abc123", "", command, injected, true, true, false, envFile)
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
	if !strings.Contains(joined, "--env-file "+envFile) {
		t.Fatalf("exec args should include the runtime env file: %s", joined)
	}

	withTTY := buildPodmanExecRunArgsWithActivation("api", "feat-x", "exec-name", identity.ImageTag("api", "feat-x", "abc123"), "999", "988", "abc123", "", command, injected, true, true, true, "")
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
	got, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"api.example.com" {`) {
		t.Fatalf("expected quoted host block, got:\n%s", got)
	}
	want := "reverse_proxy http://" + identity.ContainerName("api", productionEnvName, "web", "abc123") + ":3000"
	if !strings.Contains(got, want) {
		t.Fatalf("expected versioned container reverse_proxy %q, got:\n%s", want, got)
	}
}

func TestRenderAppCaddyfileProtectsPreviewButNeverProduction(t *testing.T) {
	port := 3000
	base := &config.AppContext{
		PreviewCapabilityToken: "capability-token",
		Processes:              map[string]config.Process{"web": {Port: &port}},
		Routes:                 map[string]config.Route{"app": {Host: "api.example.com", Process: "web"}},
	}
	preview, err := renderAppCaddyfileWithProcessNames("api", "feat-x-ab12", base, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview, "@ship_capability_query query \"ship=capability-token\"") || !strings.Contains(preview, "not header x-ship-capability \"capability-token\"") {
		t.Fatalf("preview fragment missing capability directives:\n%s", preview)
	}
	prod, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, base, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prod, "ship_capability") || strings.Contains(prod, "x-ship-capability") {
		t.Fatalf("production fragment must never be protected:\n%s", prod)
	}
}

func TestRenderAppCaddyfileIncludesCapabilityStanza(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		PreviewCapabilityToken: "capability-token",
		Processes:              map[string]config.Process{"web": {Port: &port}},
		Routes:                 map[string]config.Route{"app": {Host: "api.example.com", Process: "web"}},
	}
	got, err := renderAppCaddyfileWithProcessNames("api", "feat-x-ab12", ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "\troute {\n\t\t@ship_capability_query query \"ship=capability-token\"\n\t\thandle @ship_capability_query {\n\t\t\theader Set-Cookie \"ship=capability-token; Path=/; HttpOnly; Secure\"\n\t\t\tredir {path} temporary\n\t\t}\n\t\t@ship_capability_denied {\n\t\t\tnot header x-ship-capability \"capability-token\"\n\t\t\tnot header Cookie \"*ship=capability-token*\"\n\t\t}\n\t\trespond @ship_capability_denied 401\n"
	if !strings.Contains(got, want) {
		t.Fatalf("capability stanza mismatch:\nwant contained:\n%s\ngot:\n%s", want, got)
	}
	strip := "\t\trequest_header -x-ship-capability\n\t\trequest_header Cookie " + `"(^)ship=capability-token(?:;[ \\t]*|$)|(;[ \\t]*)ship=capability-token;[ \\t]*|;[ \\t]*ship=capability-token$"` + " \"$1$2\"\n"
	if !strings.Contains(got, strip) {
		t.Fatalf("preview capabilities are not stripped before proxying:\nwant contained:\n%s\ngot:\n%s", strip, got)
	}
	if strings.Index(got, strip) > strings.Index(got, "reverse_proxy") {
		t.Fatalf("credential stripping must precede reverse_proxy:\n%s", got)
	}
	// The 401 gate must sit inside a route block: at Caddy's default
	// directive order the strips would otherwise run first and deny
	// every request.
	if strings.Index(got, "\troute {") > strings.Index(got, "@ship_capability_denied") {
		t.Fatalf("capability gate is not wrapped in a route block:\n%s", got)
	}
	if mode := caddyFragmentMode([]byte(got)); mode != 0600 {
		t.Fatalf("share-bearing fragment mode = %o, want 0600", mode)
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
	got, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, ctx, "abc123", map[string]string{"web": upstream})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "reverse_proxy http://"+upstream+":3000") {
		t.Fatalf("expected Caddy to point at specific container %q, got:\n%s", upstream, got)
	}
}

func TestRenderAppCaddyfileStaticPathUsesHandlePath(t *testing.T) {
	ctx := &config.AppContext{
		StaticHash: strings.Repeat("a", 64),
		Routes: map[string]config.Route{
			"docs": {Host: "example.com", Path: "/docs", Serve: "docs-dist"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("site", productionEnvName, ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "handle_path /docs/*") {
		t.Fatalf("expected handle_path for static prefix, got:\n%s", got)
	}
	if !strings.Contains(got, `root * "/var/apps/site.production/static/releases/abc123-`+strings.Repeat("a", 64)+`/docs"`) {
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
	got, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, ctx, "abc123", nil)
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
		StaticHash: strings.Repeat("a", 64),
		Processes:  map[string]config.Process{"web": {Port: &port}},
		Routes: map[string]config.Route{
			"app":  {Host: "example.com", Process: "web"},
			"docs": {Host: "example.com", Path: "/docs", Serve: "docs-dist"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "reverse_proxy http://"+identity.ContainerName("api", productionEnvName, "web", "abc123")+":3000") {
		t.Fatalf("expected process route to use release container:\n%s", got)
	}
	if !strings.Contains(got, `root * "/var/apps/api.production/static/releases/abc123-`+strings.Repeat("a", 64)+`/docs"`) {
		t.Fatalf("expected static route to use release-pinned root:\n%s", got)
	}
}

func TestRenderAppCaddyfileGroupsEmptyTLSWithAuto(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		StaticHash: strings.Repeat("a", 64),
		Processes:  map[string]config.Process{"web": {Port: &port}},
		Routes: map[string]config.Route{
			"app":  {Host: "example.com", Process: "web"},
			"docs": {Host: "example.com", Path: "/docs", Serve: "docs-dist", TLS: "auto"},
		},
	}
	got, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, ctx, "abc123", nil)
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
	got, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, ctx, "abc123", nil)
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
	got, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, ctx, "abc123", nil)
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
	if _, err := renderAppCaddyfileWithProcessNames("api", productionEnvName, ctx, "abc123", nil); err == nil {
		t.Fatal("expected error for process route pointing at portless process")
	}
}

func TestStaticReleaseLayoutHasNoCurrentSymlink(t *testing.T) {
	staticRoot := t.TempDir()
	if _, err := os.Lstat(filepath.Join(staticRoot, "current")); !os.IsNotExist(err) {
		t.Fatalf("static current must not exist, err=%v", err)
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

package helper

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/store"
)

func prepareTestActivationEnv(t *testing.T, app, env, activationID string) {
	t.Helper()
	path := identity.ActivationEnvFile(app, env, activationID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("TOKEN=test\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConvergeUsesShipApprovalGate(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
	})
	setServerMemberFingerprint(aliceFingerprint)
	_, err := authorizeHelper(helperVerbShip, authTargetForAppEnv("api", "production", "converge"))
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("agent converge authorization = %v, want approval_required", err)
	}
}

func TestConvergeWorkerStartFailureRestartsOldAndReportsDegraded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	writeFakeCommand(t, bin, "id", "#!/usr/bin/env sh\nprintf '1000\\n'")
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0")
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" >> \"$PODMAN_LOG\"\nif [ \"$1\" = \"run\" ]; then exit 1; fi\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := identity.ContainerName("api", "production", "worker", "old111")
	entries := []containerEntry{{Names: []string{old}, State: "running", Labels: map[string]string{"ship.process": "worker", "ship.release": "old111"}}}
	ctx := &config.AppContext{NeedsImage: true, Processes: map[string]config.Process{"worker": {Command: "run worker"}}}
	prepareTestActivationEnv(t, "api", "production", "new222-a1b2")
	_, _, err := convergeProcesses("api", "production", "new222", "new222-a1b2", ctx, entries)
	if err == nil || !strings.Contains(err.Error(), "degraded") || !strings.Contains(err.Error(), "old worker restarted") {
		t.Fatalf("convergence error = %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(log), "stop "+old) || !strings.Contains(string(log), "start "+old) {
		t.Fatalf("worker compensation log = %s", log)
	}
}

func TestConvergeWorkerPlainStartFailureRestartsOldAndReportsDegraded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	writeFakeCommand(t, bin, "id", "#!/usr/bin/env sh\nexit 1\n")
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" >> \"$PODMAN_LOG\"\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := identity.ContainerName("api", "production", "worker", "old111")
	entries := []containerEntry{{Names: []string{old}, State: "running", Labels: map[string]string{"ship.process": "worker", "ship.release": "old111"}}}
	ctx := &config.AppContext{NeedsImage: true, Processes: map[string]config.Process{"worker": {Command: "run worker"}}}
	prepareTestActivationEnv(t, "api", "production", "new222-a1b2")
	_, _, err := convergeProcesses("api", "production", "new222", "new222-a1b2", ctx, entries)
	if err == nil || !strings.Contains(err.Error(), "degraded") || !strings.Contains(err.Error(), "old worker restarted") {
		t.Fatalf("convergence error = %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(log), "stop "+old) || !strings.Contains(string(log), "start "+old) {
		t.Fatalf("worker compensation log = %s", log)
	}
}

func TestPrepareStartsOnlyPortfulProcesses(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	writeFakeCommand(t, bin, "id", "#!/usr/bin/env sh\nprintf '1000\\n'")
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0")
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" >> \"$PODMAN_LOG\"\nexit 0")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	port := 3000
	ctx := &config.AppContext{Processes: map[string]config.Process{
		"web":    {Port: &port},
		"worker": {Command: "run-worker"},
	}}
	result, err := startReleaseProcesses(startReleaseProcessesParams{
		App: "api", Env: "production", Release: "new222", Activation: "new222-a1b2",
		Context: ctx, OnlyPortful: true, EnvFile: func() string {
			prepareTestActivationEnv(t, "api", "production", "new222-a1b2")
			return identity.ActivationEnvFile("api", "production", "new222-a1b2")
		}(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Started) != 1 || result.Started[0] != identity.ContainerName("api", "production", "web", "new222") {
		t.Fatalf("prepare started = %+v, want only web", result.Started)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "worker") {
		t.Fatalf("prepare started worker: %s", log)
	}
}

func TestConvergeWorkerStopsOldBeforeStartingReplacement(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	writeFakeCommand(t, bin, "id", "#!/usr/bin/env sh\nprintf '1000\\n'")
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0")
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" >> \"$PODMAN_LOG\"\nexit 0")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := identity.ContainerName("api", "production", "worker", "abcdef1")
	entries := []containerEntry{{Names: []string{old}, State: "running", Labels: map[string]string{
		"ship.process": "worker", "ship.release": "abcdef1", "ship.activation": "abcdef1-old",
	}}}
	ctx := &config.AppContext{NeedsImage: true, Processes: map[string]config.Process{"worker": {Command: "run-worker"}}}
	prepareTestActivationEnv(t, "api", "production", "abcdef2-new")
	if _, _, err := convergeProcesses("api", "production", "abcdef2", "abcdef2-new", ctx, entries); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	stopAt := strings.Index(log, "stop "+old)
	runAt := strings.Index(log, "run --replace -d")
	if stopAt < 0 || runAt < 0 || stopAt > runAt {
		t.Fatalf("worker replacement was not stop-old-then-start-new: %s", log)
	}
}

func TestConvergeWebFailureDoesNotCompensateWorkers(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	writeFakeCommand(t, bin, "id", "#!/usr/bin/env sh\nprintf '1000\\n'")
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0")
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" >> \"$PODMAN_LOG\"\nif [ \"$1\" = \"run\" ]; then exit 1; fi\nexit 0")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	port := 3000
	old := identity.ContainerName("api", "production", "worker", "abcdef1")
	entries := []containerEntry{{Names: []string{old}, State: "running", Labels: map[string]string{
		"ship.process": "worker", "ship.release": "abcdef1", "ship.activation": "abcdef1-old",
	}}}
	ctx := &config.AppContext{NeedsImage: true, Processes: map[string]config.Process{
		"web": {Port: &port}, "worker": {Command: "run-worker"},
	}}
	prepareTestActivationEnv(t, "api", "production", "abcdef2-new")
	if _, _, err := convergeProcesses("api", "production", "abcdef2", "abcdef2-new", ctx, entries); err == nil {
		t.Fatal("web start failure unexpectedly converged")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "stop "+old) || strings.Contains(string(data), "start "+old) {
		t.Fatalf("web failure compensated a worker: %s", data)
	}
}

func TestStaticTargetMarksOldImageContainersStale(t *testing.T) {
	old := identity.ContainerName("api", "production", "web", "abcdef1")
	entries := []containerEntry{{Names: []string{old}, State: "running", Labels: map[string]string{
		"ship.app": "api", "ship.env": "production", "ship.process": "web",
		"ship.release": "abcdef1", "ship.activation": "abcdef1-old",
	}}}
	got := staleAppContainers(entries, nil, "", "")
	if len(got) != 1 || got[0] != old {
		t.Fatalf("static target stale containers = %+v, want %q", got, old)
	}
}

func TestConvergeCaddySecondRunUsesCaddyNoOp(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		bobFingerprint: {Name: "bob", Role: store.MemberRoleOwner},
	})
	setServerMemberFingerprint(bobFingerprint)
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	caddyDir := filepath.Join(root, "caddy")
	t.Setenv("SHIP_CADDY_DIR", caddyDir)
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "caddy.log")
	t.Setenv("CADDY_LOG", logPath)
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0")
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nif [ \"$1\" = \"exec\" ] && [ \"$4\" = \"reload\" ]; then printf 'reload\\n' >> \"$CADDY_LOG\"; fi\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.MkdirAll(caddyDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" >> \"$CADDY_LOG\"\nif [ \"$1\" = \"ps\" ]; then printf '[]\\n'; fi\nexit 0\n")
	meta, err := newReleaseMetadata("abcdef2", false, "abcdef2", "2026-07-16T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte("name = \"api\"\nbox = \"example.com\"\n\n[routes]\n\"web.example.com\" = { static = \"dist\" }\n")
	e, label, err := releaseEnvelope(manifest, meta)
	if err != nil {
		t.Fatal(err)
	}
	staticHash := strings.Repeat("a", 64)
	releaseDir := filepath.Join(staticReleasePath("api", "production", "abcdef2", staticHash), config.RouteStorageName("web.example.com"))
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeStaticReleaseEnvelope("api", "production", "abcdef2", e); err != nil {
		t.Fatal(err)
	}
	if err := activationrecords.Publish("api", "production", activationrecords.Pointer{Version: 2, Activation: "abcdef2-a1b2", Artifact: activationrecords.Tuple{Release: "abcdef2", StaticHash: staticHash, EnvelopeHash: envelope.HashLabel(label)}}); err != nil {
		t.Fatal(err)
	}
	prepareTestActivationEnv(t, "api", "production", "abcdef2-a1b2")
	if _, err := convergeActive("api", "production"); err != nil {
		t.Fatal(err)
	}
	pointer, err := readActive("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveArtifact("api", "production", pointer.Artifact)
	static := staticStatusFromResolved("api", "production", resolved)
	if err != nil || !activePointerRuntimeConvergedResolved("api", "production", pointer, resolved, nil, static) {
		t.Fatalf("static-only target should converge: static=%+v err=%v", static, err)
	}
	if activePointerRuntimeConvergedResolved("api", "production", pointer, resolved, []processStatus{{Process: "old", State: "running", Release: "old111", Activation: "old-a1b2"}}, static) {
		t.Fatal("extra running app container must make status non-converged")
	}
	firstLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := (appConvergeCmd{App: "api", Env: "production", JSON: true}).Run(); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(log), "reload"); got != 2 || strings.Count(string(firstLog), "reload") != 1 {
		t.Fatalf("Caddy reload count = %d, want 2 total and 1 after first converge", got)
	}
	if strings.Contains(string(log), "run ") || strings.Contains(string(log), " stop ") || strings.Contains(string(log), " start ") {
		t.Fatalf("second converge churned containers: %s", log)
	}
	if _, err := os.Stat(identity.DeployJournalFile("api", "production")); !os.IsNotExist(err) {
		t.Fatalf("no-op converge appended a journal entry: %v", err)
	}
}

func TestConvergedAliasPreviewReportsConverged(t *testing.T) {
	setupPreviewHostTest(t)
	root := t.TempDir()
	caddyDir := filepath.Join(root, "caddy")
	t.Setenv("SHIP_CADDY_DIR", caddyDir)
	if err := os.MkdirAll(caddyDir, 0755); err != nil {
		t.Fatal(err)
	}
	env := "feat-x-ab12"
	host := names.SynthesizedHostLabel("api", env) + ".preview.example.com"
	manifest := "name = \"api\"\nbox = \"example.com\"\n\n[preview]\naliases = true\n\n[routes]\n\"" + host + "\" = { static = \"dist\" }\n"
	writeActiveEnvelopeForPreviewAliasTest(t, "api", env, manifest)
	routeDir := filepath.Join(staticReleasePath("api", env, "abc1234", strings.Repeat("a", 64)), config.RouteStorageName(host))
	if err := os.MkdirAll(routeDir, 0755); err != nil {
		t.Fatal(err)
	}
	prepareTestActivationEnv(t, "api", env, "abc1234-activation")
	if _, err := convergeActive("api", env); err != nil {
		t.Fatal(err)
	}
	fragment, err := os.ReadFile(caddyfilePath("api", env))
	if err != nil {
		t.Fatal(err)
	}
	alias := names.PreviewBranchSlug(env) + ".preview.example.com"
	if !strings.Contains(string(fragment), alias) {
		t.Fatalf("converged preview fragment should carry alias %s:\n%s", alias, fragment)
	}
	pointer, err := readActive("api", env)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveArtifact("api", env, pointer.Artifact)
	static := staticStatusFromResolved("api", env, resolved)
	if err != nil || !activePointerRuntimeConvergedResolved("api", env, pointer, resolved, nil, static) {
		t.Fatalf("converged alias preview should report converged: static=%+v err=%v", static, err)
	}

	// Another env claiming the alias host makes the installed alias-bearing
	// fragment stale: the exact predicate must report not converged.
	writeIdentityForTest(t, identity.EnvIdentity{
		Version: 1, App: "rival", Env: "production",
	})
	rivalManifest := "name = \"rival\"\nbox = \"example.com\"\n\n[routes]\n\"" + alias + "\" = { static = \"dist\" }\n"
	writeActiveEnvelopeForPreviewAliasTest(t, "rival", "production", rivalManifest)
	if activePointerRuntimeConvergedResolved("api", env, pointer, resolved, nil, static) {
		t.Fatal("a fragment carrying an alias owned by another env must not report converged")
	}
}

func TestCrashOnlyJournalOutcomeTable(t *testing.T) {
	for _, tc := range []struct {
		name, step string
		outcome    activationrecords.Outcome
	}{{"before commit", "probe", activationrecords.Failed}, {"after commit", "converge", activationrecords.CommittedUnconverged}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.outcome == activationrecords.Failed {
				entry, _ := deployJournalFailureEntry("api", "production", "old111", "new222", activationrecords.Identity{}, time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC), newJournalStepError(tc.step, errors.New("boom"), nil, nil))
				if entry.Outcome != tc.outcome || entry.FailingStep != tc.step {
					t.Fatalf("entry = %+v", entry)
				}
			}
		})
	}
}

func TestAppConvergeWithoutActiveReportsNoDeploys(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	oldAppend := appendConvergeJournal
	t.Cleanup(func() { appendConvergeJournal = oldAppend })
	var journaled int
	appendConvergeJournal = func(string, string, activationrecords.JournalEntry, []string) error {
		journaled++
		return nil
	}
	summary, err := (appConvergeCmd{App: "api", Env: "production"}).runLocked()
	if err == nil || !errcat.Is(err, errcat.CodeNoDeploys) || !strings.Contains(err.Error(), "nothing deployed yet") {
		t.Fatalf("no-deploy converge error=%v", err)
	}
	if summary.Outcome != "no_deploys" || journaled != 0 {
		t.Fatalf("no-deploy summary=%+v journaled=%d", summary, journaled)
	}
}

func TestAppConvergeActivePointerReadFailureIsNotJournaled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	path := identity.ActiveFile("api", "production")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	oldAppend := appendConvergeJournal
	t.Cleanup(func() { appendConvergeJournal = oldAppend })
	var journaled int
	appendConvergeJournal = func(string, string, activationrecords.JournalEntry, []string) error {
		journaled++
		return nil
	}
	summary, err := (appConvergeCmd{App: "api", Env: "production"}).runLocked()
	if err == nil || !strings.Contains(err.Error(), "cannot determine committed state") {
		t.Fatalf("pointer read error = %v", err)
	}
	if summary.Outcome != "active_pointer_unreadable" || journaled != 0 {
		t.Fatalf("pointer read summary=%+v journaled=%d", summary, journaled)
	}
}

func TestStatusUsesCommittedNotConvergedWording(t *testing.T) {
	out := renderStatusText("api", "production", nil, true, &statusRelease{Release: "new222", State: committedNotConvergedState, Next: convergenceNextStep}, nil)
	for _, want := range []string{committedNotConvergedState, "next: " + convergenceNextStep} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
}

func TestStaticCurrentNameIsAbsentFromExecutableSources(t *testing.T) {
	root := "."
	var found []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".go" {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), "static/"+"current") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("dead static/%s reference(s): %s", "current", strings.Join(found, ", "))
	}
}

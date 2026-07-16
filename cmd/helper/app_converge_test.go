package helper

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/store"
)

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
		Context: ctx, OnlyPortful: true,
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
	if _, _, err := convergeProcesses("api", "production", "abcdef2", "abcdef2-new", ctx, entries); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	stopAt := strings.Index(log, "stop "+old)
	runAt := strings.Index(log, "run -d")
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

func TestConvergeCaddySecondRunDoesNotReload(t *testing.T) {
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
	releaseDir := filepath.Join(identity.StaticDir("api", "production"), "releases", "abcdef2", config.RouteStorageName("web.example.com"))
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeStaticReleaseEnvelope("api", "production", "abcdef2", e); err != nil {
		t.Fatal(err)
	}
	if err := activation.Write("api", "production", activation.Pointer{Version: 1, Release: "abcdef2", Activation: "abcdef2-a1b2", EnvelopeHash: envelope.HashLabel(label)}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeActivationEnvFile("api", "production", "abcdef2-a1b2", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := convergeActive("api", "production"); err != nil {
		t.Fatal(err)
	}
	pointer, err := readActive("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	static, err := activeStaticStatus("api", "production")
	if err != nil || !activePointerRuntimeConverged("api", "production", pointer, nil, static) {
		t.Fatalf("static-only target should converge: static=%+v err=%v", static, err)
	}
	if activePointerRuntimeConverged("api", "production", pointer, []processStatus{{Process: "old", State: "running", Release: "old111", Activation: "old-a1b2"}}, static) {
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
	if got := strings.Count(string(log), "reload"); got != 1 || strings.Count(string(firstLog), "reload") != 1 {
		t.Fatalf("Caddy reload count = %d, want 1", got)
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
	routeDir := filepath.Join(identity.StaticDir("api", env), "releases", "abc1234", config.RouteStorageName(host))
	if err := os.MkdirAll(routeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeActivationEnvFile("api", env, "abc1234-activation", nil); err != nil {
		t.Fatal(err)
	}
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
	static, err := activeStaticStatus("api", env)
	if err != nil || !activePointerRuntimeConverged("api", env, pointer, nil, static) {
		t.Fatalf("converged alias preview should report converged: static=%+v err=%v", static, err)
	}
}

func TestCrashOnlyJournalOutcomeTable(t *testing.T) {
	for _, tc := range []struct{ name, step, outcome string }{{"before commit", "probe", "failed"}, {"after commit", "converge", "committed_unconverged"}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.outcome == "failed" {
				entry, _ := deployJournalFailureEntry("api", "production", "old111", "new222", deployIdentity{}, time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC), newJournalStepError(tc.step, errors.New("boom"), nil, nil))
				if entry.Outcome != tc.outcome || entry.FailingStep != tc.step {
					t.Fatalf("entry = %+v", entry)
				}
			}
		})
	}
}

func TestAppConvergeRecordsConvergedAndCommittedUnconvergedOutcomes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	if err := activation.Write("api", "production", activation.Pointer{Version: 1, Release: "abcdef1", Activation: "abcdef1-a1b2", EnvelopeHash: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	oldConverge := convergeActiveForCommand
	oldAppend := appendConvergeJournal
	t.Cleanup(func() {
		convergeActiveForCommand = oldConverge
		appendConvergeJournal = oldAppend
	})
	var entries []deployJournalEntry
	appendConvergeJournal = func(_ string, _ string, entry deployJournalEntry, _ []string) error {
		entries = append(entries, entry)
		return nil
	}
	convergeActiveForCommand = func(_, _ string) (convergeResult, error) {
		return convergeResult{StaleContainers: []string{"old"}, Changed: true}, nil
	}
	summary, err := (appConvergeCmd{App: "api", Env: "production"}).runLocked()
	if err != nil || summary.Outcome != "converged" || len(entries) != 1 || entries[0].Outcome != "converged" {
		t.Fatalf("success summary=%+v err=%v entries=%+v", summary, err, entries)
	}
	convergeActiveForCommand = func(_, _ string) (convergeResult, error) {
		return convergeResult{}, errors.New("caddy unavailable")
	}
	summary, err = (appConvergeCmd{App: "api", Env: "production"}).runLocked()
	if err == nil || summary.Outcome != "committed_unconverged" || entries[1].Outcome != "committed_unconverged" {
		t.Fatalf("failure summary=%+v err=%v entries=%+v", summary, err, entries)
	}
}

func TestAppConvergeNoopDoesNotJournal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	if err := activation.Write("api", "production", activation.Pointer{Version: 1, Release: "abcdef1", Activation: "abcdef1-a1b2", EnvelopeHash: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	oldConverge := convergeActiveForCommand
	oldAppend := appendConvergeJournal
	t.Cleanup(func() {
		convergeActiveForCommand = oldConverge
		appendConvergeJournal = oldAppend
	})
	convergeActiveForCommand = func(_, _ string) (convergeResult, error) { return convergeResult{}, nil }
	var journaled int
	appendConvergeJournal = func(string, string, deployJournalEntry, []string) error {
		journaled++
		return nil
	}
	summary, err := (appConvergeCmd{App: "api", Env: "production"}).runLocked()
	if err != nil || summary.Outcome != "converged" || journaled != 0 {
		t.Fatalf("no-op summary=%+v err=%v journaled=%d", summary, err, journaled)
	}
}

func TestAppConvergeWithoutActiveReportsNoDeploys(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	oldAppend := appendConvergeJournal
	t.Cleanup(func() { appendConvergeJournal = oldAppend })
	var journaled int
	appendConvergeJournal = func(string, string, deployJournalEntry, []string) error {
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
	appendConvergeJournal = func(string, string, deployJournalEntry, []string) error {
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

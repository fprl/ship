package helper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
)

func TestCurrentReleaseRejectsEmptyOrMixedProcesses(t *testing.T) {
	if _, err := currentRelease(nil); err == nil || !strings.Contains(err.Error(), "no processes running") {
		t.Fatalf("expected empty-processes error, got %v", err)
	}
	_, err := currentRelease([]processStatus{
		{Process: "web", Release: "aaa"},
		{Process: "worker", Release: "bbb"},
	})
	if err == nil || !strings.Contains(err.Error(), "different releases") {
		t.Fatalf("expected mixed-release error, got %v", err)
	}
}

func TestSelectRollbackRelease(t *testing.T) {
	images := []imageRelease{
		{Release: "3333333"},
		{Release: "2222222"},
		{Release: "1111111"},
	}
	got, err := selectRollbackRelease(images, "3333333", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Release != "2222222" {
		t.Fatalf("expected previous release 2222222, got %+v", got)
	}

	got, err = selectRollbackRelease(images, "3333333", "1111111")
	if err != nil {
		t.Fatal(err)
	}
	if got.Release != "1111111" {
		t.Fatalf("expected requested release 1111111, got %+v", got)
	}
}

func TestSelectRollbackReleaseErrors(t *testing.T) {
	_, err := selectRollbackRelease([]imageRelease{{Release: "3333333"}}, "3333333", "")
	if err == nil || !strings.Contains(err.Error(), "no previous release") {
		t.Fatalf("expected no previous release error, got %v", err)
	}
	_, err = selectRollbackRelease([]imageRelease{{Release: "3333333"}}, "3333333", "2222222")
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("expected missing release error, got %v", err)
	}
	_, err = selectRollbackRelease([]imageRelease{{Release: "3333333"}}, "3333333", "3333333")
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error, got %v", err)
	}
}

func TestRollbackHistoryTearRefusesAutomaticSelectionButAllowsExplicitEnvelope(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	release := "abc1234"
	manifest := []byte("name = \"api\"\nbox = \"example.com\"\n\n[routes]\n\"example.com\" = { static = \"dist\" }\n")
	meta, err := newReleaseMetadata(release, false, release, "2026-07-14T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	e, _, err := releaseEnvelope(manifest, meta)
	if err != nil {
		t.Fatal(err)
	}
	releaseDir := filepath.Join(identity.StaticDir("api", "production"), "releases", release)
	if err := os.MkdirAll(filepath.Join(releaseDir, config.RouteStorageName("example.com")), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeStaticReleaseEnvelope("api", "production", release, e); err != nil {
		t.Fatal(err)
	}
	if err := appendDeployJournalEntry("api", "production", deployJournalEntry{Outcome: "deployed", AttemptedRelease: release}, nil); err != nil {
		t.Fatal(err)
	}
	journalPath := identity.DeployJournalFile("api", "production")
	file, err := os.OpenFile(journalPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"outcome":"deployed"}`)
	_ = file.Close()
	if _, err := availableRollbackReleases("api", "production", ""); err == nil || err.Error() != "history incomplete; pass an explicit release" {
		t.Fatalf("automatic rollback error = %v", err)
	}
	got, err := availableRollbackReleases("api", "production", release)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Release != release {
		t.Fatalf("explicit candidates = %+v", got)
	}
}

func TestImageReleasesFromEntriesUsesPodmanLabels(t *testing.T) {
	entries := []imageEntry{
		{
			Names: []string{"localhost/ship/ship-de70a215abfd:3333333"},
			Labels: map[string]string{
				"ship.app":      "hello",
				"ship.env":      "production",
				"ship.infra_id": "ship-de70a215abfd",
				"ship.release":  "3333333",
			},
		},
		{
			Names: []string{"localhost/ship/ship-de70a215abfd:2222222"},
			Labels: map[string]string{
				"ship.app":      "hello",
				"ship.env":      "production",
				"ship.infra_id": "ship-de70a215abfd",
				"ship.release":  "2222222",
			},
		},
		{
			Names: []string{"localhost/ship/ship-other:ignored"},
			Labels: map[string]string{
				"ship.app":      "hello",
				"ship.env":      "production",
				"ship.infra_id": "ship-other",
				"ship.release":  "ignored",
			},
		},
		{
			Names: []string{"localhost/ship/ship-de70a215abfd:1111111"},
			Tag:   "1111111",
			Labels: map[string]string{
				"ship.app":      "hello",
				"ship.env":      "production",
				"ship.infra_id": "ship-de70a215abfd",
			},
		},
	}

	got := imageReleasesFromEntries("hello", "production", entries)
	if len(got) != 2 || got[0].Release != "3333333" || got[1].Release != "2222222" {
		t.Fatalf("unexpected releases: %+v", got)
	}
}

func TestRenderRollbackText(t *testing.T) {
	out := renderRollbackText(rollbackPayload{
		App:       "api",
		Env:       "production",
		Previous:  "3333333",
		Release:   "2222222",
		Processes: []string{"web"},
	})
	if !strings.Contains(out, "Rolled back api (production) from 3333333 to 2222222") {
		t.Fatalf("missing rollback summary:\n%s", out)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "running") {
		t.Fatalf("missing process row:\n%s", out)
	}
}

func TestRollbackTargetStartupFailureRestartsStoppedOldWorkers(t *testing.T) {
	app, oldWorker, targetWorker, logPath, oldEnv, _ := setupRollbackFailureTest(t, true, false)

	_, err := (appRollbackCmd{App: "api", Env: "production", ActivationID: "2222222-test"}).rollbackToTarget("3333333", "2222222", app)
	if err == nil {
		t.Fatal("expected target startup failure")
	}

	assertRollbackFile(t, identity.ActivationEnvFile("api", "production", "3333333-old"), oldEnv)
	log := readRollbackLog(t, logPath)
	for _, want := range []string{"stop " + oldWorker, "rm -f " + targetWorker, "start " + oldWorker} {
		if !strings.Contains(log, want) {
			t.Fatalf("podman calls missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "rm -f "+oldWorker) {
		t.Fatalf("old worker was removed instead of stopped:\n%s", log)
	}
}

func TestRollbackTargetStartupFailureReportsOldWorkerRestartFailure(t *testing.T) {
	app, oldWorker, _, _, _, _ := setupRollbackFailureTest(t, true, false)
	t.Setenv("PODMAN_FAIL_START", "1")

	_, err := (appRollbackCmd{App: "api", Env: "production", ActivationID: "2222222-test"}).rollbackToTarget("3333333", "2222222", app)
	if err == nil || !strings.Contains(err.Error(), "rollback restore failed") || !strings.Contains(err.Error(), "restart containers "+oldWorker) {
		t.Fatalf("rollback error = %v, want restart failure joined to restore error", err)
	}
}

func TestRollbackPersistFailureRestoresLiveState(t *testing.T) {
	app, oldWorker, targetWorker, logPath, oldEnv, _ := setupRollbackFailureTest(t, false, true)
	oldCaddy := []byte("old caddy fragment\n")
	caddyPath := filepath.Join(t.TempDir(), "api.production.caddy")
	previousCaddyPath := rollbackCaddyPath
	rollbackCaddyPath = func(string, string) string { return caddyPath }
	t.Cleanup(func() { rollbackCaddyPath = previousCaddyPath })
	if err := os.WriteFile(caddyPath, oldCaddy, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := (appRollbackCmd{App: "api", Env: "production", ActivationID: "2222222-test"}).rollbackToTarget("3333333", "2222222", app)
	if err == nil {
		t.Fatal("expected manifest persist failure")
	}

	assertRollbackFile(t, identity.ActivationEnvFile("api", "production", "3333333-old"), oldEnv)
	assertRollbackFile(t, caddyPath, oldCaddy)
	log := readRollbackLog(t, logPath)
	if strings.Count(log, "rm -f "+targetWorker) < 2 {
		t.Fatalf("target worker was not removed after it started:\n%s", log)
	}
	if !strings.Contains(log, "start "+oldWorker) {
		t.Fatalf("old worker was not restarted:\n%s", log)
	}
	if strings.Count(log, "exec caddy caddy reload") < 2 {
		t.Fatalf("Caddy was not reloaded for both the switch and restore:\n%s", log)
	}
}

func TestRollbackJournalFailureWarnsAfterRollbackSuccess(t *testing.T) {
	oldAppend := appendRollbackDeployJournal
	appendRollbackDeployJournal = func(string, string, deployJournalEntry, []string) error {
		return errors.New("journal disk is read-only")
	}
	t.Cleanup(func() { appendRollbackDeployJournal = oldAppend })

	cmd := appRollbackCmd{App: "api", Env: "production"}
	result := rollbackPayload{
		App:       "api",
		Env:       "production",
		Previous:  "3333333",
		Release:   "2222222",
		Processes: []string{"worker"},
	}
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureApplyStdout(t, func() {
			cmd.recordRollbackSuccess(result, time.Now().UTC())
		})
	})
	if !strings.Contains(stderr, "warning: rollback succeeded but failed to write deploy journal: journal disk is read-only; run ship box doctor\n") {
		t.Fatalf("journal warning = %q", stderr)
	}
	if !strings.Contains(stdout, "Rolled back api (production) from 3333333 to 2222222") {
		t.Fatalf("rollback summary = %q", stdout)
	}
}

func setupRollbackFailureTest(t *testing.T, failTargetStart, failPersist bool) (*config.AppContext, string, string, string, []byte, []byte) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(root, "podman.log")
	psPath := filepath.Join(root, "podman-ps.json")
	t.Setenv("PODMAN_LOG", logPath)
	t.Setenv("PODMAN_PS_FILE", psPath)
	if failTargetStart {
		t.Setenv("PODMAN_FAIL_RUN", "1")
	}
	if failPersist {
		t.Setenv("ROLLBACK_FAIL_PERSIST", "1")
	}

	oldRelease := "3333333"
	targetRelease := "2222222"
	oldWorker := identity.ContainerName("api", "production", "worker", oldRelease)
	targetWorker := identity.ContainerName("api", "production", "worker", targetRelease)
	ps := fmt.Sprintf(`[{"Names":["%s"],"State":"running","Labels":{"ship.app":"api","ship.env":"production","ship.infra_id":"%s","ship.process":"worker","ship.release":"%s"}}]`, oldWorker, identity.InfraID("api", "production"), oldRelease)
	if err := os.WriteFile(psPath, []byte(ps), 0644); err != nil {
		t.Fatal(err)
	}

	writeFakeCommand(t, bin, "id", `#!/usr/bin/env sh
if [ "$1" = "-u" ] || [ "$1" = "-g" ]; then
  printf '1000\n'
  exit 0
fi
exit 1
`)
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
if [ "$1" = "ps" ]; then
  cat "$PODMAN_PS_FILE"
  exit 0
fi
printf '%s\n' "$*" >> "$PODMAN_LOG"
if [ "$1" = "run" ] && [ "${PODMAN_FAIL_RUN:-0}" = "1" ]; then
  echo "forced target startup failure" >&2
  exit 1
fi
if [ "$1" = "start" ] && [ "${PODMAN_FAIL_START:-0}" = "1" ]; then
  echo "forced old worker restart failure" >&2
  exit 1
fi
exit 0
`)
	markerPath := filepath.Join(root, "manifest-chown.failed")
	t.Setenv("ROLLBACK_MANIFEST", identity.ActiveFile("api", "production"))
	t.Setenv("ROLLBACK_CHOWN_MARKER", markerPath)
	writeFakeCommand(t, bin, "chown", `#!/usr/bin/env sh
case "$2" in *active.json*) is_pointer=1 ;; *) is_pointer=0 ;; esac
if [ "${ROLLBACK_FAIL_PERSIST:-0}" = "1" ] && [ "$1" = "root:root" ] && [ "$is_pointer" = "1" ] && [ ! -e "$ROLLBACK_CHOWN_MARKER" ]; then
  : > "$ROLLBACK_CHOWN_MARKER"
  echo "forced manifest persist failure" >&2
  exit 1
fi
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldEnv := []byte("VERSION=old\n")
	oldManifest := []byte("name = \"api\"\n# old release\n")
	for path, data := range map[string][]byte{
		identity.ActivationEnvFile("api", "production", "3333333-old"): oldEnv,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	return &config.AppContext{
		AppName:    "api",
		EnvName:    "production",
		NeedsImage: true,
		Processes:  map[string]config.Process{"worker": {Command: "run worker"}},
		Vars:       map[string]string{"VERSION": "new"},
	}, oldWorker, targetWorker, logPath, oldEnv, oldManifest
}

func assertRollbackFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func readRollbackLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/identity"
)

func TestGCRemovesOrphansButKeepsActiveArtifactsAndSkipsFreshTemp(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
case "$1" in
  ps) printf '%s\n' '[{"Names":["active"],"State":"running","Labels":{"ship.app":"api","ship.env":"production","ship.release":"abcdef1","ship.activation":"abcdef1-a1b2"}},{"Names":["failed"],"State":"exited","Labels":{"ship.app":"api","ship.env":"production","ship.release":"old1111","ship.activation":"old1111-a1b2"}}]' ;;
	  images) printf '%s\n' '[{"Repository":"ship/ignored","Tag":"dead111","Labels":{"ship.app":"api","ship.env":"production","ship.infra_id":"`+identity.InfraID("api", "production")+`","ship.release":"dead111"}}]' ;;
  rm|rmi) printf '%s\n' "$*" >> "$PODMAN_LOG" ;;
esac
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := activation.Write("api", "production", activation.Pointer{Version: 1, Release: "abcdef1", Activation: "abcdef1-a1b2", EnvelopeHash: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	activeDir := filepath.Join(identity.StaticDir("api", "production"), "releases", "abcdef1")
	orphanDir := filepath.Join(identity.StaticDir("api", "production"), "releases", "old1111")
	for _, dir := range []string{activeDir, orphanDir, identity.ActivationsDir("api", "production")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"abcdef1-a1b2", "orphan-a1b2"} {
		if err := os.WriteFile(identity.ActivationEnvFile("api", "production", id), []byte("TOKEN=x\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(orphanDir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(identity.ActivationEnvFile("api", "production", "orphan-a1b2"), old, old); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(identity.StaticDir("api", "production"), ".staging-fresh")
	if err := os.MkdirAll(fresh, 0755); err != nil {
		t.Fatal(err)
	}
	oldNow := gcNow
	t.Cleanup(func() { gcNow = oldNow })
	now := time.Now()
	gcNow = func() time.Time { return now }
	summary, err := gcEnv("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(activeDir); err != nil {
		t.Fatalf("active release removed: %v", err)
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("orphan release still exists: %v", err)
	}
	if _, err := os.Stat(identity.ActivationEnvFile("api", "production", "abcdef1-a1b2")); err != nil {
		t.Fatalf("active activation removed: %v", err)
	}
	if _, err := os.Stat(identity.ActivationEnvFile("api", "production", "orphan-a1b2")); !os.IsNotExist(err) {
		t.Fatalf("orphan activation still exists: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh staging dir was not skipped: %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "rm -f failed") || !strings.Contains(string(log), "rmi ship/"+identity.InfraID("api", "production")+":dead111") {
		t.Fatalf("GC podman removals=%q summary=%+v", log, summary)
	}
}

func TestGCGracePeriodSkipsFreshPaths(t *testing.T) {
	root := t.TempDir()
	oldNow := gcNow
	t.Cleanup(func() { gcNow = oldNow })
	now := time.Now()
	gcNow = func() time.Time { return now }
	path := filepath.Join(root, "fresh")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if !freshPath(path) {
		t.Fatal("fresh path was not inside the grace period")
	}
}

func TestGCProtectsAllArtifactsForUnverifiableRelease(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
case "$1" in
  ps) printf '%s\n' '[]' ;;
  images) printf '%s\n' '[{"Repository":"ship/ignored","Tag":"dead111","Labels":{"ship.app":"api","ship.env":"production","ship.infra_id":"`+identity.InfraID("api", "production")+`","ship.release":"dead111"}}]' ;;
  rmi) printf '%s\n' "$*" >> "$PODMAN_LOG" ;;
esac
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := activation.Write("api", "production", activation.Pointer{Version: 1, Release: "abcdef1", Activation: "abcdef1-a1b2", EnvelopeHash: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(identity.StaticDir("api", "production"), "releases", "bad1111")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	oldActivation := identity.ActivationEnvFile("api", "production", "bad1111-a1b2")
	if err := os.MkdirAll(filepath.Dir(oldActivation), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldActivation, []byte("TOKEN=x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := appendDeployJournalEntry("api", "production", deployJournalEntry{
		Outcome: "deployed", StartedAt: "2026-07-16T10:00:00Z", EndedAt: "2026-07-16T10:00:01Z",
		AttemptedRelease: "bad1111", Activation: "bad1111-a1b2",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := gcEnv("api", "production"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("protected static release removed: %v", err)
	}
	if _, err := os.Stat(oldActivation); err != nil {
		t.Fatalf("protected activation removed: %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "rmi ship/"+identity.InfraID("api", "production")+":dead111") {
		t.Fatalf("unrelated image was not removed: %s", log)
	}
}

func TestGCSkipsEnvOnTornJournal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := activation.Write("api", "production", activation.Pointer{Version: 1, Release: "abcdef1", Activation: "abcdef1-a1b2", EnvelopeHash: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := appendDeployJournalEntry("api", "production", deployJournalEntry{
		Outcome: "deployed", StartedAt: "2026-07-16T10:00:00Z", EndedAt: "2026-07-16T10:00:01Z", AttemptedRelease: "abcdef1",
	}, nil); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(identity.DeployJournalFile("api", "production"), os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"outcome":"deployed"}`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		summary, gcErr := gcEnv("api", "production")
		if gcErr == nil || len(summary.Failures) == 0 || len(summary.Removed) != 0 {
			t.Fatalf("torn journal GC summary=%+v err=%v", summary, gcErr)
		}
	})
	if !strings.Contains(stderr, tornDeployJournalWarning) {
		t.Fatalf("torn journal warning=%q", stderr)
	}
}

func TestGCUsesLongGraceForSharedDeployTemps(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_DEPLOY_TMP_DIR", root)
	now := time.Now()
	oldNow := gcNow
	t.Cleanup(func() { gcNow = oldNow })
	gcNow = func() time.Time { return now }
	oneHour := filepath.Join(root, "upload-one-hour")
	twoDays := filepath.Join(root, "upload-two-days")
	for _, path := range []string{oneHour, twoDays} {
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(oneHour, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(twoDays, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var summary gcBoxSummary
	gcRemoveGlobalDeployTemps(&summary)
	if _, err := os.Stat(oneHour); err != nil {
		t.Fatalf("legitimate upload temp removed too early: %v", err)
	}
	if _, err := os.Stat(twoDays); !os.IsNotExist(err) {
		t.Fatalf("stale deploy temp was not removed: %v", err)
	}
}

func TestGCNoopDoesNotAppendJournal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nprintf '%s\\n' '[]'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := activation.Write("api", "production", activation.Pointer{Version: 1, Release: "abcdef1", Activation: "abcdef1-a1b2", EnvelopeHash: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	if summary, err := gcEnv("api", "production"); err != nil || len(summary.Removed) != 0 {
		t.Fatalf("no-op GC summary=%+v err=%v", summary, err)
	}
	if _, err := os.Stat(identity.DeployJournalFile("api", "production")); !os.IsNotExist(err) {
		t.Fatalf("no-op GC created journal: %v", err)
	}
}

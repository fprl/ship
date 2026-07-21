package helper

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/artifact"
	"github.com/fprl/ship/internal/deployoutcome"
	"github.com/fprl/ship/internal/deployrequest"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
)

func TestDeployJournalFailureEntryUsesApplyForUnwrappedErrors(t *testing.T) {
	startedAt := time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)
	entry, _ := deployJournalFailureEntry("api", "production", "old111", "new222", deployIdentity{}, startedAt, errors.New("corrupt upload tar"))
	if entry.FailingStep != "apply" || entry.Outcome != "failed" || entry.StderrTail != "corrupt upload tar" {
		t.Fatalf("unwrapped journal entry = %+v", entry)
	}

	for _, tt := range []struct {
		step    string
		outcome deployoutcome.Kind
	}{
		{step: "build", outcome: deployoutcome.Failed},
		{step: "probe", outcome: deployoutcome.Failed},
		{step: "release", outcome: deployoutcome.Failed},
	} {
		t.Run(tt.step, func(t *testing.T) {
			entry, _ := deployJournalFailureEntry("api", "production", "old111", "new222", deployIdentity{}, startedAt, newJournalStepError(tt.step, errors.New(tt.step+" failed"), nil, nil))
			if entry.FailingStep != tt.step || entry.Outcome != tt.outcome {
				t.Fatalf("wrapped journal entry = %+v", entry)
			}
		})
	}
}

func TestCommittedFailuresRecordResolveStepAndCommittedTuple(t *testing.T) {
	oldAppend := appendSanitizedDeployJournal
	t.Cleanup(func() { appendSanitizedDeployJournal = oldAppend })
	var got deployJournalEntry
	appendSanitizedDeployJournal = func(_ string, _ string, entry deployJournalEntry) error {
		got = entry
		return nil
	}
	c := appApplyCmd{Request: deployrequest.Request{App: "api", Env: "production", SHA: "abc1234"}, ImageID: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
	if err := c.recordCommittedUnconverged(nil, "old", time.Now(), &convergeError{Step: "resolve", Err: errors.New("gone")}); err == nil {
		t.Fatal("expected committed unconverged error")
	}
	if got.FailingStep != "resolve" || got.Artifact == nil || got.Artifact.ImageID != c.ImageID || got.Artifact.StaticHash != c.StaticHash {
		t.Fatalf("journal entry=%+v, want resolve and committed tuple", got)
	}

	oldRollbackAppend := appendRollbackDeployJournal
	t.Cleanup(func() { appendRollbackDeployJournal = oldRollbackAppend })
	appendRollbackDeployJournal = func(_ string, _ string, entry deployJournalEntry, _ []string) error {
		got = entry
		return nil
	}
	result := rollbackPayload{Previous: "old", Release: "abc1234", Artifact: artifact.Tuple{Release: "abc1234", ImageID: strings.Repeat("c", 64)}}
	(appRollbackCmd{}).recordRollbackFailure(result, time.Now(), &convergeError{Step: "resolve", Err: errors.New("gone")})
	if got.FailingStep != "resolve" {
		t.Fatalf("rollback journal entry=%+v, want resolve", got)
	}
}

func TestDeployJournalScrubsResolvedEnvValues(t *testing.T) {
	setupJournalHostTest(t)
	secretValue := "super-secret-token"
	entry := deployJournalEntry{
		Outcome:          "failed",
		StartedAt:        "2026-07-07T10:00:00Z",
		EndedAt:          "2026-07-07T10:00:01Z",
		PreviousRelease:  "aaa111",
		AttemptedRelease: "bbb222",
		FailingStep:      "release",
		StderrTail:       "first line\nleaked " + secretValue + "\nlast line",
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
		Probe:            &journalProbe{BodySnippet: "body " + secretValue},
	}
	if err := appendDeployJournalEntry("api", "production", entry, []string{secretValue}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(identity.DeployJournalFile("api", "production"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretValue) {
		t.Fatalf("journal file leaked secret value:\n%s", raw)
	}
	latest, err := readLatestDeployJournalEntry("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if latest.SchemaVersion != deployJournalSchemaVersion || latest.App != "api" || latest.Env != "production" {
		t.Fatalf("unexpected journal identity: %+v", latest)
	}
	if !strings.Contains(latest.StderrTail, "[redacted]") || strings.Contains(latest.StderrTail, secretValue) {
		t.Fatalf("stderr tail was not scrubbed: %+v", latest)
	}
	if latest.Probe == nil || !strings.Contains(latest.Probe.BodySnippet, "[redacted]") {
		t.Fatalf("probe body was not scrubbed: %+v", latest.Probe)
	}
}

func TestV2DeployDeletesMalformedOldJournalWithoutSniffing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	oldPath := identity.LegacyDeployJournalFile("api", "production")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("not json\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := resetLegacyDeployJournalForV2("api", "production"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old journal survived reset: %v", err)
	}
}

func TestLatestSuccessfulDeployJournalEntrySkipsFailures(t *testing.T) {
	setupJournalHostTest(t)
	failed := deployJournalEntry{
		Outcome:          "failed",
		StartedAt:        "2026-07-07T10:00:00Z",
		EndedAt:          "2026-07-07T10:00:01Z",
		AttemptedRelease: "bad222",
		FailingStep:      "probe",
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}
	if err := appendDeployJournalEntry("api", "production", failed, nil); err != nil {
		t.Fatal(err)
	}
	deployed := deployJournalEntry{
		Outcome:          "deployed",
		StartedAt:        "2026-07-07T10:01:00Z",
		EndedAt:          "2026-07-07T10:01:01Z",
		AttemptedRelease: "good333",
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}
	if err := appendDeployJournalEntry("api", "production", deployed, nil); err != nil {
		t.Fatal(err)
	}

	got, err := readLatestSuccessfulDeployJournalEntry("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "deployed" || got.AttemptedRelease != "good333" {
		t.Fatalf("unexpected successful entry: %+v", got)
	}
}

func TestLatestDeployJournalEntrySkipsGC(t *testing.T) {
	setupJournalHostTest(t)
	deployed := deployJournalEntry{
		Outcome:          "deployed",
		StartedAt:        "2026-07-07T10:00:00Z",
		EndedAt:          "2026-07-07T10:00:01Z",
		AttemptedRelease: "good333",
	}
	if err := appendDeployJournalEntry("api", "production", deployed, nil); err != nil {
		t.Fatal(err)
	}
	if err := appendDeployJournalEntry("api", "production", deployJournalEntry{
		Outcome: "gc", StartedAt: "2026-07-07T11:00:00Z", EndedAt: "2026-07-07T11:00:01Z", GC: "none",
	}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := readLatestDeployJournalEntry("api", "production")
	if err != nil || got.Outcome != "deployed" || got.AttemptedRelease != "good333" {
		t.Fatalf("latest after GC = %+v, err=%v", got, err)
	}
}

func TestLatestDeployJournalEntryNoDeploysError(t *testing.T) {
	setupJournalHostTest(t)
	_, err := readLatestDeployJournalEntry("api", "production")
	if err == nil {
		t.Fatal("expected no_deploys error")
	}
	want := "deploy journal lookup failed\nno deploys recorded for api (production)\nnext: ship"
	if !errcat.Is(err, errcat.CodeNoDeploys) || err.Error() != want {
		t.Fatalf("unexpected no_deploys error:\n%s", err.Error())
	}
}

func TestExecReleaseSelectionUsesActivePointerDespiteTornDeployJournalTail(t *testing.T) {
	setupJournalHostTest(t)
	release := "abcdef1"
	manifest := []byte("name = \"api\"\nbox = \"example.com\"\n\n[processes]\nweb = { cmd = \"run-web\" }\n")
	meta, err := newReleaseMetadata(release, false, release, "2026-07-14T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	_, label, err := releaseEnvelope(manifest, meta)
	if err != nil {
		t.Fatal(err)
	}
	imageID := strings.Repeat("a", 64)
	if err := activation.Write("api", "production", activation.Pointer{Version: 2, Activation: release + "-activation", Artifact: artifact.Tuple{Release: release, ImageID: imageID}}); err != nil {
		t.Fatal(err)
	}
	prepareTestActivationEnv(t, "api", "production", release+"-activation")
	bin := t.TempDir()
	payload := fmt.Sprintf(`[{"Id":"sha256:%s","Labels":{"ship.app":"api","ship.env":"production","ship.release":"%s","ship.release_envelope":"%s"}}]`, imageID, release, label)
	writeFakeCommand(t, bin, "podman", fmt.Sprintf("#!/usr/bin/env sh\nprintf '%%s\\n' '%s'\n", payload))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	journalPath := identity.DeployJournalFile("api", "production")
	entry := deployJournalEntry{Outcome: "deployed", AttemptedRelease: release}
	if err := appendDeployJournalEntry("api", "production", entry, nil); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(journalPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"outcome":"deployed","attempted_release":"newer"}`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	var target execTarget
	var resolveErr error
	stderr := captureStderr(t, func() {
		target, resolveErr = resolveExecTarget("api", "production")
	})
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	defer target.Cleanup()
	if target.Release != release {
		t.Fatalf("exec release = %q, want %q", target.Release, release)
	}
	if stderr != "" {
		t.Fatalf("active exec selection should not consult torn history, stderr = %q", stderr)
	}
}

func setupJournalHostTest(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return string(out)
}

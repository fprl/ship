package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
)

func TestDeployJournalScrubsResolvedEnvValues(t *testing.T) {
	setupJournalHostTest(t)
	secretValue := "super-secret-token"
	entry := deployJournalEntry{
		Outcome:          "aborted_release",
		StartedAt:        "2026-07-07T10:00:00Z",
		EndedAt:          "2026-07-07T10:00:01Z",
		PreviousRelease:  "aaa111",
		AttemptedRelease: "bbb222",
		FailingStep:      "release",
		StderrTail:       "first line\nleaked " + secretValue + "\nlast line",
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
		Probe:            &journalProbe{BodySnippet: "body " + secretValue},
	}
	if err := appendDeployJournalEntry("api", "prod", entry, []string{secretValue}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(os.Getenv("SHIP_APPS_DIR"), "api.prod", "releases", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretValue) {
		t.Fatalf("journal file leaked secret value:\n%s", raw)
	}
	latest, err := readLatestDeployJournalEntry("api", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if latest.SchemaVersion != deployJournalSchemaVersion || latest.App != "api" || latest.Env != "prod" {
		t.Fatalf("unexpected journal identity: %+v", latest)
	}
	if !strings.Contains(latest.StderrTail, "[redacted]") || strings.Contains(latest.StderrTail, secretValue) {
		t.Fatalf("stderr tail was not scrubbed: %+v", latest)
	}
	if latest.Probe == nil || !strings.Contains(latest.Probe.BodySnippet, "[redacted]") {
		t.Fatalf("probe body was not scrubbed: %+v", latest.Probe)
	}
}

func TestLatestSuccessfulDeployJournalEntrySkipsFailures(t *testing.T) {
	setupJournalHostTest(t)
	failed := deployJournalEntry{
		Outcome:          "aborted_probe",
		StartedAt:        "2026-07-07T10:00:00Z",
		EndedAt:          "2026-07-07T10:00:01Z",
		AttemptedRelease: "bad222",
		FailingStep:      "probe",
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}
	if err := appendDeployJournalEntry("api", "prod", failed, nil); err != nil {
		t.Fatal(err)
	}
	deployed := deployJournalEntry{
		Outcome:          "deployed",
		StartedAt:        "2026-07-07T10:01:00Z",
		EndedAt:          "2026-07-07T10:01:01Z",
		AttemptedRelease: "good333",
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}
	if err := appendDeployJournalEntry("api", "prod", deployed, nil); err != nil {
		t.Fatal(err)
	}

	got, err := readLatestSuccessfulDeployJournalEntry("api", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "deployed" || got.AttemptedRelease != "good333" {
		t.Fatalf("unexpected successful entry: %+v", got)
	}
}

func TestLatestDeployJournalEntryNoDeploysError(t *testing.T) {
	setupJournalHostTest(t)
	_, err := readLatestDeployJournalEntry("api", "prod")
	if err == nil {
		t.Fatal("expected no_deploys error")
	}
	want := "deploy journal lookup failed\nno deploys recorded for api (prod)\nnext: ship"
	if !errcat.Is(err, errcat.CodeNoDeploys) || err.Error() != want {
		t.Fatalf("unexpected no_deploys error:\n%s", err.Error())
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

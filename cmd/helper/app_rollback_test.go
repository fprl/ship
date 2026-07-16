package helper

import (
	"errors"
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
	_, err := currentRelease([]processStatus{{Process: "web", Release: "aaa"}, {Process: "worker", Release: "bbb"}})
	if err == nil || !strings.Contains(err.Error(), "different releases") {
		t.Fatalf("expected mixed-release error, got %v", err)
	}
}

func TestSelectRollbackRelease(t *testing.T) {
	images := []imageRelease{{Release: "3333333"}, {Release: "2222222"}, {Release: "1111111"}}
	got, err := selectRollbackRelease(images, "3333333", "")
	if err != nil || got.Release != "2222222" {
		t.Fatalf("previous rollback = %+v, err=%v", got, err)
	}
	got, err = selectRollbackRelease(images, "3333333", "1111111")
	if err != nil || got.Release != "1111111" {
		t.Fatalf("explicit rollback = %+v, err=%v", got, err)
	}
}

func TestSelectRollbackReleaseErrors(t *testing.T) {
	for _, tc := range []struct{ name, current, requested, want string }{
		{"no previous", "3333333", "", "no previous release"},
		{"missing", "3333333", "2222222", "not available"},
		{"same", "3333333", "3333333", "already running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := selectRollbackRelease([]imageRelease{{Release: "3333333"}}, tc.current, tc.requested)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
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
	if err != nil || len(got) != 1 || got[0].Release != release {
		t.Fatalf("explicit candidates = %+v, err=%v", got, err)
	}
}

func TestImageReleasesFromEntriesUsesPodmanLabels(t *testing.T) {
	entries := []imageEntry{
		{Names: []string{"localhost/ship/ship-de70a215abfd:3333333"}, Labels: map[string]string{"ship.app": "hello", "ship.env": "production", "ship.infra_id": "ship-de70a215abfd", "ship.release": "3333333"}},
		{Names: []string{"localhost/ship/ship-de70a215abfd:2222222"}, Labels: map[string]string{"ship.app": "hello", "ship.env": "production", "ship.infra_id": "ship-de70a215abfd", "ship.release": "2222222"}},
		{Names: []string{"localhost/ship/ship-other:ignored"}, Labels: map[string]string{"ship.app": "hello", "ship.env": "production", "ship.infra_id": "ship-other", "ship.release": "ignored"}},
		{Names: []string{"localhost/ship/ship-de70a215abfd:1111111"}, Tag: "1111111", Labels: map[string]string{"ship.app": "hello", "ship.env": "production", "ship.infra_id": "ship-de70a215abfd"}},
	}
	got := imageReleasesFromEntries("hello", "production", entries)
	if len(got) != 2 || got[0].Release != "3333333" || got[1].Release != "2222222" {
		t.Fatalf("unexpected releases: %+v", got)
	}
}

func TestRenderRollbackText(t *testing.T) {
	out := renderRollbackText(rollbackPayload{App: "api", Env: "production", Previous: "3333333", Release: "2222222", Processes: []string{"web"}})
	if !strings.Contains(out, "Rolled back api (production) from 3333333 to 2222222") || !strings.Contains(out, "web") || !strings.Contains(out, "running") {
		t.Fatalf("rollback summary = %s", out)
	}
}

func TestRollbackJournalFailureWarnsAfterRollbackSuccess(t *testing.T) {
	oldAppend := appendRollbackDeployJournal
	appendRollbackDeployJournal = func(string, string, deployJournalEntry, []string) error {
		return errors.New("journal disk is read-only")
	}
	t.Cleanup(func() { appendRollbackDeployJournal = oldAppend })
	cmd := appRollbackCmd{App: "api", Env: "production"}
	result := rollbackPayload{App: "api", Env: "production", Previous: "3333333", Release: "2222222", Processes: []string{"worker"}}
	var stdout string
	stderr := captureStderr(t, func() { stdout = captureApplyStdout(t, func() { cmd.recordRollbackSuccess(result, time.Now().UTC()) }) })
	if !strings.Contains(stderr, "warning: rollback succeeded but failed to write deploy journal: journal disk is read-only; run ship box doctor\n") {
		t.Fatalf("journal warning = %q", stderr)
	}
	if !strings.Contains(stdout, "Rolled back api (production) from 3333333 to 2222222") {
		t.Fatalf("rollback summary = %q", stdout)
	}
}

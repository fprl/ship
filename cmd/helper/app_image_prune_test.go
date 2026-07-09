package helper

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/identity"
)

func TestReleaseImageKeepSetKeepsFiveNewestProdDeploys(t *testing.T) {
	entries := deployEntries("2026-07-09T10:00:00Z",
		"000000000001",
		"000000000002",
		"000000000003",
		"000000000004",
		"000000000005",
		"000000000006",
		"000000000007",
	)

	keep, err := releaseImageKeepSet(entries, nil, "", productionReleaseImageKeep)
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{"000000000003", "000000000004", "000000000005", "000000000006", "000000000007"} {
		if !keep[release] {
			t.Fatalf("expected to keep %s, keep=%v", release, keep)
		}
	}
	for _, release := range []string{"000000000001", "000000000002"} {
		if keep[release] {
			t.Fatalf("expected to prune %s, keep=%v", release, keep)
		}
	}
}

func TestReleaseImageKeepSetKeepsLiveReleaseOutsideWindow(t *testing.T) {
	entries := deployEntries("2026-07-09T10:00:00Z",
		"000000000001",
		"000000000002",
		"000000000003",
		"000000000004",
	)

	keep, err := releaseImageKeepSet(entries, nil, "000000000001", previewReleaseImageKeep)
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{"000000000001", "000000000003", "000000000004"} {
		if !keep[release] {
			t.Fatalf("expected to keep %s, keep=%v", release, keep)
		}
	}
	if keep["000000000002"] {
		t.Fatalf("expected release 000000000002 outside keep set, keep=%v", keep)
	}
}

func TestReleaseImageKeepSetKeepsRollbackTargetInsideWindow(t *testing.T) {
	entries := deployEntries("2026-07-09T10:00:00Z",
		"000000000001",
		"000000000002",
		"000000000003",
		"000000000004",
		"000000000005",
		"000000000006",
		"000000000007",
	)

	keep, err := releaseImageKeepSet(entries, nil, "000000000007", productionReleaseImageKeep)
	if err != nil {
		t.Fatal(err)
	}
	if !keep["000000000003"] {
		t.Fatalf("in-window rollback target should remain keepable, keep=%v", keep)
	}
}

func TestImageReleasesFromEntriesFiltersOtherAppsAndEnvs(t *testing.T) {
	entries := []imageEntry{
		imageEntryFor("api", "prod", "000000000001"),
		imageEntryFor("api", "feat-x-ab12", "000000000002"),
		imageEntryFor("worker", "prod", "000000000003"),
	}

	got := imageReleasesFromEntries("api", "prod", entries)
	if len(got) != 1 || got[0].Release != "000000000001" || got[0].Image != identity.ImageTag("api", "prod", "000000000001") {
		t.Fatalf("unexpected filtered images: %+v", got)
	}
}

func TestBestEffortPruneReleaseImagesAfterDeploySwallowsPodmanFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
case "$1" in
  images|ps)
    printf '[]\n'
    exit 0
    ;;
  image)
    echo "forced image prune failure" >&2
    exit 42
    ;;
esac
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	entry := deployEntries("2026-07-09T10:00:00Z", "000000000001")[0]
	stderr := captureStderr(t, func() {
		summary := bestEffortPruneReleaseImagesAfterDeploy("api", "prod", "000000000001", entry)
		if summary != "pruned 0 old images" {
			t.Fatalf("unexpected prune summary: %q", summary)
		}
	})
	if strings.Count(strings.TrimSpace(stderr), "\n") != 0 {
		t.Fatalf("prune failure should log one line, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "image prune failed for api (prod): podman image prune -f: forced image prune failure") {
		t.Fatalf("unexpected prune failure log:\n%s", stderr)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = old
	}()
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

func deployEntries(start string, releases ...string) []deployJournalEntry {
	base, err := time.Parse(time.RFC3339, start)
	if err != nil {
		panic(err)
	}
	entries := make([]deployJournalEntry, 0, len(releases))
	for i, release := range releases {
		at := base.Add(time.Duration(i) * time.Minute)
		entries = append(entries, deployJournalEntry{
			Outcome:          "deployed",
			StartedAt:        at.Add(-10 * time.Second).Format(time.RFC3339Nano),
			EndedAt:          at.Format(time.RFC3339Nano),
			AttemptedRelease: release,
			Identity:         deployIdentity{SSHKeyComment: "test", GitAuthor: "Test <test@example.com>"},
		})
	}
	return entries
}

func imageEntryFor(app, env, release string) imageEntry {
	return imageEntry{
		Repository: identity.ImageRepo(app, env),
		Tag:        release,
		Names:      []string{identity.ImageTag(app, env, release)},
		Labels: map[string]string{
			"ship.app":      app,
			"ship.env":      env,
			"ship.infra_id": identity.InfraID(app, env),
			"ship.release":  release,
		},
	}
}

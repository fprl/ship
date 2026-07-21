package helper

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/artifact"
	"github.com/fprl/ship/internal/deployoutcome"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/identity"
)

func writeLegacyPointerForTest(t *testing.T, app, env, release, activationID, envelopeHash string) {
	t.Helper()
	path := identity.ActiveFile(app, env)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		Release      string `json:"release"`
		Activation   string `json:"activation"`
		EnvelopeHash string `json:"envelope_hash"`
	}{release, activationID, envelopeHash})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func testTuple(release, image, static string) artifact.Tuple {
	return artifact.Tuple{Release: release, ImageID: image, StaticHash: static}
}

func committedHistoryForTest(t *testing.T, app, env string) ([]Tuple, bool, error) {
	t.Helper()
	pointer, err := readActive(app, env)
	if err != nil {
		return nil, false, err
	}
	return committedHistoryWithPointer(app, env, pointer)
}

func TestCommittedHistoryDeduplicatesTuplesKeepsRepeatedReleasesAndReportsTorn(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	a := testTuple("release-a", strings.Repeat("a", 64), "")
	b := testTuple("release-a", strings.Repeat("b", 64), "")
	c := testTuple("release-c", strings.Repeat("c", 64), "")
	if err := activation.Write("api", "production", activation.Pointer{Version: 2, Activation: "a", Artifact: a}); err != nil {
		t.Fatal(err)
	}
	for _, tuple := range []artifact.Tuple{b, a, c, b} {
		copy := tuple
		if err := appendDeployJournalEntry("api", "production", deployJournalEntry{Outcome: "deployed", Artifact: &copy}, nil); err != nil {
			t.Fatal(err)
		}
	}
	history, torn, err := committedHistoryForTest(t, "api", "production")
	if err != nil || torn {
		t.Fatalf("history=%v torn=%v err=%v", history, torn, err)
	}
	want := []artifact.Tuple{a, b, c}
	if len(history) != len(want) {
		t.Fatalf("history=%v, want=%v", history, want)
	}
	for i := range want {
		if history[i] != want[i] {
			t.Fatalf("history[%d]=%v, want=%v", i, history[i], want[i])
		}
	}
	path := identity.DeployJournalFile("api", "production")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"schema_version":2,"outcome":"deployed"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, torn, err = committedHistoryForTest(t, "api", "production")
	if err != nil || !torn {
		t.Fatalf("torn history err=%v torn=%v", err, torn)
	}
}

func TestCommittedHistoryIgnoresV1Journal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	tuple := artifact.Tuple{Release: "release-a", StaticHash: strings.Repeat("a", 64), EnvelopeHash: strings.Repeat("b", 64)}
	if err := activation.Write("api", "production", activation.Pointer{Version: 2, Artifact: tuple}); err != nil {
		t.Fatal(err)
	}
	path := identity.LegacyDeployJournalFile("api", "production")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"outcome":"deployed","attempted_release":"old"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	history, torn, err := committedHistoryForTest(t, "api", "production")
	if err != nil || torn || len(history) != 1 || history[0] != tuple {
		t.Fatalf("history=%v torn=%v err=%v", history, torn, err)
	}
}

func TestCommittedHistoryIncludesPostCommitArtifacts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	active := testTuple("active", strings.Repeat("a", 64), "")
	unconverged := testTuple("unconverged", strings.Repeat("b", 64), "")
	degraded := testTuple("degraded", strings.Repeat("c", 64), "")
	if err := activation.Write("api", "production", activation.Pointer{Version: 2, Activation: "active-a1b2", Artifact: active}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		outcome deployoutcome.Kind
		tuple   artifact.Tuple
	}{
		{deployoutcome.CommittedUnconverged, unconverged},
		{deployoutcome.CommittedDegraded, degraded},
	} {
		tuple := item.tuple
		if err := appendDeployJournalEntry("api", "production", deployJournalEntry{Outcome: item.outcome, Artifact: &tuple}, nil); err != nil {
			t.Fatal(err)
		}
	}
	history, _, err := committedHistoryForTest(t, "api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 || history[1] != degraded || history[2] != unconverged {
		t.Fatalf("history=%v, want active plus post-commit tuples", history)
	}
}

func TestResolveArtifactNeverFallsBackToAnotherImageWithTheSameRelease(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	inspected := strings.Repeat("b", 64)
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
printf '%s\n' '[{"Id":"sha256-`+inspected+`","Labels":{"ship.app":"api","ship.env":"production","ship.release":"abcdef1"}}]'
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	wanted := artifact.Tuple{Release: "abcdef1", ImageID: strings.Repeat("a", 64)}
	if _, err := ResolveArtifact("api", "production", wanted); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("resolve error = %v, want exact-image mismatch", err)
	}
}

func TestResolveArtifactRejectsStaticHashShapeThatDisagreesWithManifest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	release := "abc1234"
	meta, err := newReleaseMetadata(release, false, release+strings.Repeat("a", 33), "2026-07-16T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	e, label, err := releaseEnvelope([]byte("name = \"api\"\nbox = \"example.com\"\n\n[processes]\nworker = {}\n"), meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStaticReleaseEnvelope("api", "production", release, e); err != nil {
		t.Fatal(err)
	}
	staticHash := strings.Repeat("b", 64)
	if err := os.MkdirAll(staticReleasePath("api", "production", release, staticHash), 0755); err != nil {
		t.Fatal(err)
	}
	_, err = resolveArtifact("api", "production", artifact.Tuple{Release: release, StaticHash: staticHash, EnvelopeHash: envelope.HashLabel(label)})
	if err == nil || !strings.Contains(err.Error(), "static_hash does not match manifest serve routes") {
		t.Fatalf("resolve mismatch error = %v", err)
	}
}

func TestInspectExactImageClassifiesConfirmedAbsence(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
if [ "$2" = "inspect" ]; then exit 2; fi
if [ "$2" = "exists" ]; then exit 1; fi
exit 2
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := inspectExactImage(strings.Repeat("a", 64))
	var absent *artifactAbsentError
	if !errors.As(err, &absent) {
		t.Fatalf("error=%v, want confirmed absence", err)
	}
}

func TestSharedCandidatesClassifiesImageAndStaticAbsenceSeparately(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
if [ "$2" = "inspect" ]; then exit 2; fi
if [ "$2" = "exists" ]; then exit 1; fi
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	active := artifact.Tuple{Release: "abc1234", StaticHash: strings.Repeat("a", 64), EnvelopeHash: strings.Repeat("b", 64)}
	missing := artifact.Tuple{Release: "abc1235", ImageID: strings.Repeat("c", 64), StaticHash: strings.Repeat("d", 64)}
	if err := activation.Write("api", "production", activation.Pointer{Version: 2, Activation: "active-a1b2", Artifact: active}); err != nil {
		t.Fatal(err)
	}
	if err := appendDeployJournalEntry("api", "production", deployJournalEntry{Outcome: "deployed", Artifact: &missing}, nil); err != nil {
		t.Fatal(err)
	}
	resolveErr := func() error { _, err := resolveArtifact("api", "production", missing); return err }()
	var absent *artifactAbsentError
	if !errors.As(resolveErr, &absent) || !isMissingPath(staticReleasePath("api", "production", missing.Release, missing.StaticHash)) {
		t.Fatalf("direct missing classification error=%v absent=%v staticMissing=%v", resolveErr, absent, isMissingPath(staticReleasePath("api", "production", missing.Release, missing.StaticHash)))
	}
	set, err := sharedArtifactCandidatesWithPointer("api", "production", activation.Pointer{Version: 2, Artifact: active})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Absent) != 1 || set.Absent[0] != missing || len(set.Protected) != 0 {
		t.Fatalf("candidate classification=%+v, want one absent and no protected", set)
	}
}

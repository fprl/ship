package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/artifact"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
)

func TestReadActiveMissingReturnsNoDeploys(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))

	if _, err := readActive("api", "production"); err == nil || !errcat.Is(err, errcat.CodeNoDeploys) || !strings.Contains(err.Error(), "nothing deployed yet") {
		t.Fatalf("missing active pointer error = %v, want coded no-deploys error", err)
	}
}

func TestActivePointerSurvivesCrashBeforeJournalAppend(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	meta, err := newReleaseMetadata("abc1234", false, "abc1234", "2026-07-14T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	_, label, err := releaseEnvelope([]byte("name = \"api\"\n"), meta)
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyPointerForTest(t, "api", "production", "abc1234", "abc1234-deadbeef", envelope.HashLabel(label))
	if _, err := readActive("api", "production"); err != nil {
		t.Fatal(err)
	}
	if _, err := readLatestSuccessfulDeployJournalEntry("api", "production"); err == nil || !strings.Contains(err.Error(), "no deploys recorded") {
		t.Fatalf("journal should still be empty after simulated crash: %v", err)
	}
	if _, err := filepath.Abs(identity.ActiveFile("api", "production")); err != nil {
		t.Fatal(err)
	}
}

func TestWriteActivePreparesOwnershipBeforePublishing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := activation.Pointer{Version: 1, Release: "old1234", Activation: "old1234-old", EnvelopeHash: strings.Repeat("a", 64)}
	writeLegacyPointerForTest(t, "api", "production", old.Release, old.Activation, old.EnvelopeHash)
	if err := activation.Write("api", "production", activation.Pointer{Version: 2, Activation: "new1234-new", Artifact: artifact.Tuple{Release: "new1234", StaticHash: strings.Repeat("b", 64), EnvelopeHash: strings.Repeat("c", 64)}}); err != nil {
		t.Fatalf("writeActive should not invoke root chown: %v", err)
	}
	got, err := readActive("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || got.Activation != "new1234-new" || got.Artifact.Release != "new1234" {
		t.Fatalf("active pointer was not published: %+v", got)
	}
}

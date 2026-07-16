package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/identity"
)

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
	if err := activation.Write("api", "production", activation.Pointer{Version: 1, Release: "abc1234", Activation: "abc1234-deadbeef", EnvelopeHash: envelope.HashLabel(label)}); err != nil {
		t.Fatal(err)
	}
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
	chownTarget := filepath.Join(root, "chown-target")
	t.Setenv("CHOWN_TARGET", chownTarget)
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nprintf '%s\\n' \"$2\" > \"$CHOWN_TARGET\"\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := activation.Pointer{Version: 1, Release: "old1234", Activation: "old1234-old", EnvelopeHash: strings.Repeat("a", 64)}
	if err := activation.Write("api", "production", old); err != nil {
		t.Fatal(err)
	}
	next := activation.Pointer{Version: 1, Release: "new1234", Activation: "new1234-new", EnvelopeHash: strings.Repeat("b", 64)}
	if err := writeActive("api", "production", next); err == nil {
		t.Fatal("writeActive succeeded despite chown failure")
	}
	got, err := readActive("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if got != old {
		t.Fatalf("active pointer changed before ownership preparation: %+v", got)
	}
	target, err := os.ReadFile(chownTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(target) == identity.ActiveFile("api", "production") {
		t.Fatal("chown ran against the serving active.json path")
	}
}

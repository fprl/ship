package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/errcat"
)

func TestReadActiveMissingReturnsNoDeploys(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))

	if _, err := readActive("api", "production"); err == nil || !errcat.Is(err, errcat.CodeNoDeploys) || !strings.Contains(err.Error(), "nothing deployed yet") {
		t.Fatalf("missing active pointer error = %v, want coded no-deploys error", err)
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
	if err := activationrecords.Publish("api", "production", activationrecords.Pointer{Version: 2, Activation: "abc1234-new", Artifact: activationrecords.Tuple{Release: "abc1234", StaticHash: strings.Repeat("b", 64), EnvelopeHash: strings.Repeat("c", 64)}}); err != nil {
		t.Fatalf("writeActive should not invoke root chown: %v", err)
	}
	got, err := readActive("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || got.Activation != "abc1234-new" || got.Artifact.Release != "abc1234" {
		t.Fatalf("active pointer was not published: %+v", got)
	}
}

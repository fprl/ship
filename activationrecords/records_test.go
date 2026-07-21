package activationrecords

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionZeroPointerIsUnsupported(t *testing.T) {
	err := Validate(Pointer{Version: 0})
	if err == nil || err.Error() != "unsupported active.json version 0" {
		t.Fatalf("Validate(v0) = %v, want unsupported-version error", err)
	}
}

func TestPublishUsesFullArtifactGrammar(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	pointer := Pointer{Version: 2, Activation: "active-a1b2", Artifact: Tuple{Release: "not-a-release", ImageID: strings.Repeat("a", 64)}}
	if err := Publish("api", "production", pointer); err == nil {
		t.Fatal("Publish() accepted an invalid release")
	}
}

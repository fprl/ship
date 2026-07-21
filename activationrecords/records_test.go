package activationrecords

import (
	"encoding/json"
	"errors"
	"os"
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

func TestReadUsesFullArtifactGrammar(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	pointer := Pointer{Version: Version, Activation: "active-a1b2", Artifact: Tuple{Release: "not-a-release", ImageID: strings.Repeat("a", 64)}}
	data, err := json.Marshal(pointer)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "apps", "api.production", "active.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read("api", "production"); err == nil {
		t.Fatal("Read() accepted an invalid artifact")
	} else {
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) {
			t.Fatalf("Read() error = %T %v, want ValidationError", err, err)
		}
	}
}

package activation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPointerRoundTripsAtActivePath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	pointer := Pointer{Version: 1, Release: "abc1234", Activation: "abc1234-deadbeef", EnvelopeHash: strings.Repeat("a", 64)}
	path := filepath.Join(root, "apps", "api.production", "active.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		Release      string `json:"release"`
		Activation   string `json:"activation"`
		EnvelopeHash string `json:"envelope_hash"`
	}{pointer.Release, pointer.Activation, pointer.EnvelopeHash})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := Read("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != pointer.Version || got.Activation != pointer.Activation || got.Release != pointer.Release || got.EnvelopeHash != pointer.EnvelopeHash {
		t.Fatalf("pointer = %+v, want %+v", got, pointer)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("active.json mode = %o, want 644", info.Mode().Perm())
	}
}

func TestVersionZeroPointerIsUnsupported(t *testing.T) {
	err := Validate(Pointer{Version: 0})
	if err == nil || err.Error() != "unsupported active.json version 0" {
		t.Fatalf("Validate(v0) = %v, want unsupported-version error", err)
	}
	if (Pointer{Version: 0}).IsLegacy() {
		t.Fatal("version 0 must not be treated as legacy")
	}
}

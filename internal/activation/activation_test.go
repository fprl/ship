package activation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPointerRoundTripsAtActivePath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	pointer := Pointer{Version: 1, Release: "abc1234", Activation: "abc1234-deadbeef", EnvelopeHash: strings.Repeat("a", 64)}
	if err := Write("api", "production", pointer); err != nil {
		t.Fatal(err)
	}
	got, err := Read("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if got != pointer {
		t.Fatalf("pointer = %+v, want %+v", got, pointer)
	}
	info, err := os.Stat(filepath.Join(root, "apps", "api.production", "active.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("active.json mode = %o, want 644", info.Mode().Perm())
	}
}

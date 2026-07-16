package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/config"
)

func TestStagedCaddyValidationFailureLeavesServingFragmentUntouched(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nif [ \"$1\" = \"exec\" ] && [ \"$4\" = \"validate\" ]; then echo invalid >&2; exit 1; fi\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(root, "ship.caddy")
	old := []byte("old serving fragment\n")
	if err := os.WriteFile(path, old, 0644); err != nil {
		t.Fatal(err)
	}
	port := 3000
	ctx := &config.AppContext{Routes: map[string]config.Route{"web.example.com": {Host: "web.example.com", Process: "web"}}, Processes: map[string]config.Process{"web": {Port: &port}}}
	err := renderAndReloadAppCaddy(path, "api", "production", ctx, "abc1234", nil)
	if err == nil || !strings.Contains(err.Error(), "caddy validate failed") {
		t.Fatalf("validation error = %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(old) {
		t.Fatalf("serving fragment changed on staged validation failure: %q", got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".caddy" && entry.Name() != filepath.Base(path) {
			t.Fatalf("validation left a serving-directory Caddy fragment: %s", entry.Name())
		}
	}
}

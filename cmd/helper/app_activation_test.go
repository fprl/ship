package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/activation"
)

func TestActivationEnvFilesAreImmutableAndUseNewNames(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	first, err := writeActivationEnvFile("api", "production", "abc1234-one", map[string]string{"TOKEN": "old"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeActivationEnvFile("api", "production", "abc1234-two", map[string]string{"TOKEN": "new"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("second deploy reused the first activation env path")
	}
	if got, _ := os.ReadFile(first); string(got) != "TOKEN=old\n" {
		t.Fatalf("first activation env changed: %q", got)
	}
	if mode := mustFileMode(t, first); mode != 0600 {
		t.Fatalf("activation env mode = %o, want 600", mode)
	}
	if err := os.Chmod(first, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeActivationEnvFile("api", "production", "abc1234-one", map[string]string{"TOKEN": "old"}); err != nil {
		t.Fatalf("same-content activation env repair failed: %v", err)
	}
	if mode := mustFileMode(t, first); mode != 0600 {
		t.Fatalf("repaired activation env mode = %o, want 600", mode)
	}
	if _, err := writeActivationEnvFile("api", "production", "abc1234-one", map[string]string{"TOKEN": "changed"}); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable rewrite error = %v", err)
	}
}

func TestActiveEnvFileRefusesToReResolveMissingFrozenActivation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	const activationID = "abc1234-frozen"
	if err := activation.Write("api", "production", activation.Pointer{
		Version: 1, Release: "abc1234", Activation: activationID, EnvelopeHash: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := activeEnvFile("api", "production")
	if err == nil || !strings.Contains(err.Error(), "frozen environment for active activation "+activationID) || !strings.Contains(err.Error(), "next: ship") {
		t.Fatalf("missing frozen env error = %v", err)
	}
}

func TestActivationEnvFileRetentionReplacesTheOldCap(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for i := 0; i < 33; i++ {
		if _, err := writeActivationEnvFile("api", "production", "abc1234-"+strings.Repeat("x", i+1), map[string]string{"N": "1"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writeActivationEnvFile("api", "production", "abc1234-overflow", nil); err != nil {
		t.Fatalf("retention should permit a new activation before GC: %v", err)
	}
}

func mustFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

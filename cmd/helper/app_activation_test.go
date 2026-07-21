package helper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/identity"
)

func TestActivationAllocationIsExclusiveAndWritesPrivateOwnedEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	chownLog := filepath.Join(root, "chown.log")
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" >> \""+chownLog+"\"\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	generatedID, err := newActivationID("api", "production", "generated")
	if err != nil {
		t.Fatal(err)
	}
	generatedPath := identity.ActivationEnvFile("api", "production", generatedID)
	if file, err := os.OpenFile(generatedPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); !os.IsExist(err) {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("second generated activation allocation error=%v, want EEXIST", err)
	}

	id := "abc1234-activation"
	path := identity.ActivationEnvFile("api", "production", id)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); !os.IsExist(err) {
		t.Fatalf("second exclusive allocation error=%v, want EEXIST", err)
	}
	if _, err := writeActivationEnvFile("api", "production", id, map[string]string{"TOKEN": "secret"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("activation env mode=%o, want 600", info.Mode().Perm())
	}
	log, err := os.ReadFile(chownLog)
	if err != nil || !strings.Contains(string(log), identity.SystemUser("api", "production")+":"+identity.SystemUser("api", "production")) {
		t.Fatalf("ownership call log=%q err=%v", log, err)
	}
}

func TestActivationEnvWriteCleansUpAfterOwnershipFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	id := "abc1234-ownership-failure"
	path := identity.ActivationEnvFile("api", "production", id)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := writeActivationEnvFile("api", "production", id, map[string]string{"TOKEN": "secret"}); err == nil {
		t.Fatal("ownership failure unexpectedly succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed activation env survived: %v", err)
	}
}

func TestExecReportsMissingFrozenEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	release := "abc1234"
	imageID := strings.Repeat("a", 64)
	meta, err := newReleaseMetadata(release, false, release, "2026-07-16T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte("name = \"api\"\nbox = \"example.com\"\n\n[processes]\nweb = { cmd = \"run-web\" }\n")
	_, label, err := releaseEnvelope(manifest, meta)
	if err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`[{"Id":"sha256:%s","Labels":{"ship.app":"api","ship.env":"production","ship.release_envelope":"%s"}}]`, imageID, label)
	writeFakeCommand(t, bin, "podman", "#!/usr/bin/env sh\nprintf '%s\\n' '"+payload+"'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := activationrecords.Publish("api", "production", activationrecords.Pointer{Version: 2, Activation: release + "-activation", Artifact: activationrecords.Tuple{Release: release, ImageID: imageID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExecTarget("api", "production"); err == nil || !strings.Contains(err.Error(), "frozen environment for active activation") {
		t.Fatalf("missing frozen environment error=%v", err)
	}
}

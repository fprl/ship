package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/identity"
)

func TestStaticEnvelopeSidecarsAreHashNamedAndPointerSelectable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	releaseDir := filepath.Join(identity.StaticDir("api", "production"), "releases", "abc1234")
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		t.Fatal(err)
	}
	meta, err := newReleaseMetadata("abc1234", false, "abc1234", "2026-07-16T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	first, firstLabel, err := releaseEnvelope([]byte("name = \"api\"\nbox = \"example.com\"\n"), meta)
	if err != nil {
		t.Fatal(err)
	}
	second, secondLabel, err := releaseEnvelope([]byte("name = \"api\"\nbox = \"example.com\"\n\n[tls]\nmode = \"internal\"\n"), meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStaticReleaseEnvelope("api", "production", "abc1234", first); err != nil {
		t.Fatal(err)
	}
	if err := writeStaticReleaseEnvelope("api", "production", "abc1234", second); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(releaseDir))
	if err != nil {
		t.Fatal(err)
	}
	var sidecars []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ship-release-") {
			sidecars = append(sidecars, entry.Name())
		}
	}
	if len(sidecars) != 2 {
		t.Fatalf("sidecars = %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(releaseDir), ".ship-release")); !os.IsNotExist(err) {
		t.Fatalf("unqualified sidecar exists: %v", err)
	}
	for _, tc := range []struct {
		label string
		want  envelope.Envelope
	}{
		{firstLabel, first},
		{secondLabel, second},
	} {
		data, err := os.ReadFile(staticReleaseEnvelopePath("api", "production", "abc1234", envelope.HashLabel(tc.label)))
		got, decodeErr := envelope.DecodeJSON(data)
		if err == nil {
			err = decodeErr
		}
		if err != nil || got.Manifest != tc.want.Manifest {
			t.Fatalf("sidecar %s = %+v, err=%v", tc.label, got, err)
		}
	}
}

package helper

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/secrets"
)

func TestDestroyContainerNamesUsesLabelledProcesses(t *testing.T) {
	processes := []processStatus{
		{Process: "web", Container: "ship-a8f9b2-web-abc1234"},
		{Process: "worker", Container: "ship-a8f9b2-worker-abc1234"},
		{Process: "broken"},
	}

	got := destroyContainerNames(processes)
	want := []string{"ship-a8f9b2-web-abc1234", "ship-a8f9b2-worker-abc1234"}
	if len(got) != len(want) {
		t.Fatalf("unexpected names:\nwant: %#v\n got: %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected names:\nwant: %#v\n got: %#v", want, got)
		}
	}
}

func TestRenderDestroyText(t *testing.T) {
	out := renderDestroyText("api", "production", destroySummary{
		Containers:    []string{"app-api-production-web"},
		CaddyFragment: true,
		SecretsPurged: true,
	})

	for _, want := range []string{
		"Destroyed api (production)",
		"containers: 1 removed",
		"route: removed",
		"secrets: purged",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("destroy summary missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDestroyTextEmpty(t *testing.T) {
	out := renderDestroyText("api", "staging", destroySummary{})

	for _, want := range []string{
		"Destroyed api (staging)",
		"containers: none",
		"route: none",
		"secrets: kept",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("destroy summary missing %q:\n%s", want, out)
		}
	}
}

func TestCleanupDestroyedEnvCredentialsRemovesCapabilityWithoutPurge(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_SECRETS_DIR", filepath.Join(root, "secrets"))
	if err := secrets.PutPreviewCapability("api", "preview", []byte("capability")); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put("api", "preview", "DATABASE_URL", []byte("postgres://example")); err != nil {
		t.Fatal(err)
	}

	purged, err := cleanupDestroyedEnvCredentials("api", "preview", false)
	if err != nil {
		t.Fatal(err)
	}
	if purged {
		t.Fatal("non-purge cleanup reported user secrets as purged")
	}
	if _, err := secrets.GetPreviewCapability("api", "preview"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("capability after cleanup = %v, want ErrNotFound", err)
	}
	if _, err := secrets.Get("api", "preview", "DATABASE_URL"); err != nil {
		t.Fatalf("user secret after non-purge cleanup = %v", err)
	}
}

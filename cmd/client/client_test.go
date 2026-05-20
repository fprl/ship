package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeClientManifest(t *testing.T, root string, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "simple-vps.toml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeClientLockfile(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "bun.lock"), []byte("\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestReadTargetServerUsesSingleManifestEnv(t *testing.T) {
	root := t.TempDir()
	writeClientLockfile(t, root)
	writeClientManifest(t, root, `
name = "api"

[env.staging]
server = "deploy@100.x.y.z"
runtime = "bun"
`)

	server, err := readTargetServer(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if server != "deploy@100.x.y.z" {
		t.Fatalf("unexpected server: %s", server)
	}
}

func TestReadTargetServerRejectsMultipleManifestEnvs(t *testing.T) {
	root := t.TempDir()
	writeClientLockfile(t, root)
	writeClientManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[env.staging]
server = "deploy@100.x.y.z"
runtime = "bun"
`)

	_, err := readTargetServer(root, "")
	if err == nil || !strings.Contains(err.Error(), "exactly one env") {
		t.Fatalf("expected exactly-one-env error, got %v", err)
	}
}

func TestParseServerFlagRejectsSshOptions(t *testing.T) {
	_, _, err := parseServerFlag([]string{"--server", "-oProxyCommand=sh"})
	if err == nil || !strings.Contains(err.Error(), "SSH target") {
		t.Fatalf("expected SSH target validation error, got %v", err)
	}
}

func TestValidateArtifactDotenvRejectsSecretsButAllowsExamples(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".env.example", ".env.sample", ".env.defaults"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("KEY=value\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateArtifactDotenv(root); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, ".env.production"), []byte("SECRET=1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := validateArtifactDotenv(root)
	if err == nil || !strings.Contains(err.Error(), ".env.production") {
		t.Fatalf("expected dotenv rejection, got %v", err)
	}
}

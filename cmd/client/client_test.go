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

func writeClientDockerfile(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestReadTargetServerUsesSingleManifestEnv(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, `
name = "api"

[env.staging]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"
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
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, `
name = "api"

[env.production]
server = "deploy@100.x.y.z"

[env.staging]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"
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

func TestServerAppApplyCommandPutsTypedFlagsBeforePositional(t *testing.T) {
	got := serverAppApplyCommand("api", "production", "/tmp/simple-vps-deploy/x.tar", "/tmp/simple-vps-deploy/x.toml", "abc1234")
	want := "sudo simple-vps server app apply --tarball /tmp/simple-vps-deploy/x.tar --manifest /tmp/simple-vps-deploy/x.toml --sha abc1234 api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppSetupEnvCommand(t *testing.T) {
	got := serverAppSetupEnvCommand("api", "production")
	want := "sudo simple-vps server app setup-env api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppDestroyEnvCommand(t *testing.T) {
	got := serverAppDestroyEnvCommand("api", "production", false)
	want := "sudo simple-vps server app destroy-env api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppDestroyEnvCommand("api", "production", true)
	want = "sudo simple-vps server app destroy-env --purge api production"
	if got != want {
		t.Fatalf("unexpected purge command:\nwant: %s\n got: %s", want, got)
	}
}

func TestValidateDestroyConfirmation(t *testing.T) {
	if err := validateDestroyConfirmation("api", "api", false); err != nil {
		t.Fatalf("confirming the app name should pass: %v", err)
	}
	if err := validateDestroyConfirmation("api", "", true); err != nil {
		t.Fatalf("--yes should pass without confirm: %v", err)
	}
	if err := validateDestroyConfirmation("api", "wrong", false); err == nil || !strings.Contains(err.Error(), "--confirm api") {
		t.Fatalf("expected confirmation error naming the app, got %v", err)
	}
}

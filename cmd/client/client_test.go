package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/simple-vps/internal/config"
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

func TestPublishCommandsPutFlagsBeforeHost(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		AppName: "api",
		AppRoot: "/var/apps/api",
		Services: map[string]config.Service{
			"web": {Port: &port},
		},
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "cloudflare",
			got:  cloudflarePublishCommand("api", "api.example.com"),
			want: "sudo simple-vps server cloudflare publish --app api api.example.com",
		},
		{
			name: "proxy",
			got: routePublishCommand(ctx, config.Route{
				Host:    "api.example.com",
				Type:    "proxy",
				Service: "web",
			}),
			want: "sudo simple-vps server route proxy --port 3000 --app api --service web api.example.com",
		},
		{
			name: "proxy without service",
			got: routePublishCommand(ctx, config.Route{
				Host: "api.example.com",
				Type: "proxy",
			}),
			want: "sudo simple-vps server route proxy --port 80 --app api api.example.com",
		},
		{
			name: "static",
			got: routePublishCommand(ctx, config.Route{
				Host: "static.example.com",
				Type: "static",
			}),
			want: "sudo simple-vps server route static --root /var/apps/api/current --app api static.example.com",
		},
		{
			name: "redirect",
			got: routePublishCommand(ctx, config.Route{
				Host: "old.example.com",
				Type: "redirect",
				To:   "https://new.example.com",
			}),
			want: "sudo simple-vps server route redirect --to https://new.example.com --app api old.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("unexpected command:\nwant: %s\n got: %s", tt.want, tt.got)
			}
		})
	}
}

func TestReleasePermissionsCommandGrantsAppGroupWrite(t *testing.T) {
	got := releasePermissionsCommand("api", "/var/apps/api/releases/a1b2c3")
	want := strings.Join([]string{
		"chgrp -R app-api /var/apps/api/releases/a1b2c3",
		"chmod -R g+rwX /var/apps/api/releases/a1b2c3",
		"find /var/apps/api/releases/a1b2c3 -type d -exec chmod g+s {} +",
		"chmod 2775 /var/apps/api/releases/a1b2c3",
	}, " && ")
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

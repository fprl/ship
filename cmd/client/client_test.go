package client

import (
	"os"
	"os/exec"
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

func TestParseHostFlagsAllowsJsonAroundSubcommand(t *testing.T) {
	flags, err := parseHostFlags([]string{"--json", "doctor", "--server", "deploy@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !flags.json {
		t.Fatal("expected json flag")
	}
	if flags.server != "deploy@example.com" {
		t.Fatalf("unexpected server: %s", flags.server)
	}
	if got := strings.Join(flags.rest, " "); got != "doctor" {
		t.Fatalf("unexpected rest: %q", got)
	}
}

func TestDefaultAppNameUsesCurrentDirectoryBase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "simple-vps-local-demo")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}

	if got := defaultAppName(root); got != "simple-vps-local-demo" {
		t.Fatalf("defaultAppName = %q", got)
	}
}

func TestNormalizeAppNameReturnsValidManifestName(t *testing.T) {
	cases := map[string]string{
		".":                        "app",
		"@scope/My_App":            "my-app",
		"123-api":                  "app-123-api",
		"a":                        "ap",
		strings.Repeat("abc-", 20): strings.Repeat("abc-", 10) + "a",
	}
	for input, want := range cases {
		if got := normalizeAppName(input); got != want {
			t.Fatalf("normalizeAppName(%q) = %q, want %q", input, got, want)
		}
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

func TestStaticTreeHashChangesWhenServeBytesChange(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, "dist", "index.html")
	if err := os.WriteFile(index, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	v1, err := staticTreeHash(root, []string{"dist"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	v2, err := staticTreeHash(root, []string{"dist"})
	if err != nil {
		t.Fatal(err)
	}
	if v1 == v2 {
		t.Fatalf("static hash did not change: %s", v1)
	}
}

func TestWriteSourceTarAppendsIgnoredStaticDirsForCleanArchive(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("dist/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("tracked"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "index.html"), []byte("static"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init")
	runGit(t, root, "add", ".gitignore", "README.md")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")

	tarPath := filepath.Join(t.TempDir(), "source.tar")
	if err := writeSourceTar(root, tarPath, false, []string{"dist"}); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("tar", "-tf", tarPath).CombinedOutput()
	if err != nil {
		t.Fatalf("tar list failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "dist/index.html") {
		t.Fatalf("ignored static dir missing from archive:\n%s", out)
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestServerAppApplyCommandPutsTypedFlagsBeforePositional(t *testing.T) {
	got := serverAppApplyCommand("api", "production", "/tmp/simple-vps-deploy/x.tar", "/tmp/simple-vps-deploy/x.toml", "abc1234", false)
	want := "sudo simple-vps server app apply --tarball /tmp/simple-vps-deploy/x.tar --manifest /tmp/simple-vps-deploy/x.toml --sha abc1234 api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppApplyCommandSupportsRebuild(t *testing.T) {
	got := serverAppApplyCommand("api", "production", "/tmp/simple-vps-deploy/x.tar", "/tmp/simple-vps-deploy/x.toml", "abc1234", true)
	want := "sudo simple-vps server app apply --rebuild --tarball /tmp/simple-vps-deploy/x.tar --manifest /tmp/simple-vps-deploy/x.toml --sha abc1234 api production"
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

func TestServerAppListCommandSupportsJSON(t *testing.T) {
	got := serverAppListCommand(false)
	want := "sudo simple-vps server app list"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppListCommand(true)
	want = "sudo simple-vps server app list --json"
	if got != want {
		t.Fatalf("unexpected json command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppRollbackCommandSupportsRelease(t *testing.T) {
	got := serverAppRollbackCommand("api", "production", "")
	want := "sudo simple-vps server app rollback api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppRollbackCommand("api", "production", "abc1234")
	want = "sudo simple-vps server app rollback api production abc1234"
	if got != want {
		t.Fatalf("unexpected release command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppBackupCommands(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "create",
			got:  serverAppBackupCommand("api", "production", "", false),
			want: "sudo simple-vps server app backup create api production",
		},
		{
			name: "create json",
			got:  serverAppBackupCommand("api", "production", "", true),
			want: "sudo simple-vps server app backup create --json api production",
		},
		{
			name: "create to",
			got:  serverAppBackupCommand("api", "production", "/tmp/backups", false),
			want: "sudo simple-vps server app backup create --to /tmp/backups api production",
		},
		{
			name: "list",
			got:  serverAppBackupListCommand("api", "production", false),
			want: "sudo simple-vps server app backup list api production",
		},
		{
			name: "list json",
			got:  serverAppBackupListCommand("api", "production", true),
			want: "sudo simple-vps server app backup list --json api production",
		},
		{
			name: "rm",
			got:  serverAppBackupRmCommand("api", "production", "backup-id"),
			want: "sudo simple-vps server app backup rm api production backup-id",
		},
		{
			name: "restore",
			got:  serverAppRestoreCommand("api", "production", "backup-id", false),
			want: "sudo simple-vps server app backup restore --from backup-id api production",
		},
		{
			name: "restore dry run",
			got:  serverAppRestoreCommand("api", "production", "backup-id", true),
			want: "sudo simple-vps server app backup restore --from backup-id --dry-run api production",
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

func TestServerHostReadCommandsSupportJSON(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "status text",
			got:  serverStatusCommand(false),
			want: "sudo simple-vps server status",
		},
		{
			name: "status json",
			got:  serverStatusCommand(true),
			want: "sudo simple-vps server status --json",
		},
		{
			name: "doctor text",
			got:  serverDoctorCommand(false),
			want: "sudo simple-vps server doctor",
		},
		{
			name: "doctor json",
			got:  serverDoctorCommand(true),
			want: "sudo simple-vps server doctor --json",
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

func TestServerAppSecretListCommandSupportsJSON(t *testing.T) {
	got := serverAppSecretListCommand("api", "production", false)
	want := "sudo simple-vps server app secret list api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppSecretListCommand("api", "production", true)
	want = "sudo simple-vps server app secret list --json api production"
	if got != want {
		t.Fatalf("unexpected json command:\nwant: %s\n got: %s", want, got)
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

func TestDestroyTargetSupportsManifestFreeTargeting(t *testing.T) {
	app, server, err := destroyTarget(t.TempDir(), "production", "api", "deploy@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if app != "api" || server != "deploy@example.com" {
		t.Fatalf("unexpected target: app=%s server=%s", app, server)
	}

	if _, _, err := destroyTarget(t.TempDir(), "production", "api", ""); err == nil || !strings.Contains(err.Error(), "both --app and --server") {
		t.Fatalf("expected paired flag error, got %v", err)
	}
	if _, _, err := destroyTarget(t.TempDir(), "production", "Api", "deploy@example.com"); err == nil || !strings.Contains(err.Error(), "invalid app name") {
		t.Fatalf("expected app validation error, got %v", err)
	}
}

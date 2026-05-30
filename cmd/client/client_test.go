package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/simple-vps/internal/config"
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

func TestValidateArtifactDotenvIgnoresUndeployedDirs(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".git", "node_modules"} {
		path := filepath.Join(root, dir)
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ".env"), []byte("SECRET=ignored\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateArtifactDotenv(root); err != nil {
		t.Fatalf("dotenv scan should ignore undeployed dirs, got %v", err)
	}
}

func TestDirtyReleaseIDIncludesBaseCommit(t *testing.T) {
	at := time.Date(2026, 5, 30, 14, 30, 12, 123456789, time.UTC)
	got := dirtyReleaseID("a1b2c3d4e5f6", at)
	want := "a1b2c3d4e5f6-dirty-20260530t143012123456789z"
	if got != want {
		t.Fatalf("dirtyReleaseID = %q, want %q", got, want)
	}
}

func TestCheckAndDeployShareDirtyWorktreeDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"
`)
	runGit(t, root, "init")
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}

	checkDiags, err := checkDiagnostics(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	_, deployDiags, err := buildLocalDeployPlan(root, "production", localDeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkErrors := checkDiags.errorMessages()
	deployErrors := deployDiags.errorMessages()
	if len(checkErrors) != 1 || len(deployErrors) != 1 || checkErrors[0] != deployErrors[0] || checkErrors[0] != "working tree is dirty" {
		t.Fatalf("check/deploy diagnostics diverged:\ncheck=%v\ndeploy=%v", checkErrors, deployErrors)
	}
}

func TestCheckDiagnosticsExplainsMissingGitRepo(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"
`)

	diags, err := checkDiagnostics(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	errors := diags.errorMessages()
	if len(errors) != 1 || errors[0] != "git repository not found" {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if !strings.Contains(diags[0].Hint, "git init") {
		t.Fatalf("expected git init hint, got %q", diags[0].Hint)
	}
}

func TestCheckDiagnosticsListsRequiredSecretsWithoutFailing(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[vars]
DATABASE_URL = "@secret:DATABASE_URL"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"
`)
	runGit(t, root, "init")
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")

	diags, err := checkDiagnostics(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if diags.hasErrors() {
		t.Fatalf("secret guidance should not fail local check: %+v", diags)
	}
	if len(diags) != 1 || diags[0].Level != diagnosticWarning {
		t.Fatalf("expected one warning, got %+v", diags)
	}
	if !strings.Contains(diags[0].Message, "secret DATABASE_URL must be set before deploy") {
		t.Fatalf("unexpected secret message: %q", diags[0].Message)
	}
	if !strings.Contains(diags[0].Hint, "simple-vps secret set DATABASE_URL --env production") {
		t.Fatalf("unexpected secret hint: %q", diags[0].Hint)
	}
}

func TestGitWorktreeDirtyIsScopedToAppRoot(t *testing.T) {
	repo := t.TempDir()
	appRoot := filepath.Join(repo, "apps", "api")
	if err := os.MkdirAll(appRoot, 0755); err != nil {
		t.Fatal(err)
	}
	writeClientDockerfile(t, appRoot)
	writeClientManifest(t, appRoot, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"
`)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("root"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(repo, "root-dirty.txt"), []byte("dirty outside app"), 0644); err != nil {
		t.Fatal(err)
	}

	dirty, err := gitWorktreeDirty(appRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("app root should not be dirty when only a sibling/root file changed")
	}

	if err := os.WriteFile(filepath.Join(appRoot, "dirty.txt"), []byte("dirty inside app"), 0644); err != nil {
		t.Fatal(err)
	}
	dirty, err = gitWorktreeDirty(appRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("app root should be dirty when a file inside the app root changed")
	}
}

func TestBuildLocalDeployPlanAllowsIgnoredDotenvOutsideCleanArtifact(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"
`)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".env\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init")
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")

	_, diags, err := buildLocalDeployPlan(root, "production", localDeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if diags.hasErrors() {
		t.Fatalf("ignored local dotenv should not block clean deploy artifact, got %+v", diags)
	}
}

func TestBuildLocalDeployPlanAllowsUntrackedServeDir(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"

[routes.docs]
host = "api.example.com"
path = "/docs"
serve = "dist"
`)
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "index.html"), []byte("generated"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init")
	runGit(t, root, "add", "Dockerfile", "simple-vps.toml")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")

	plan, diags, err := buildLocalDeployPlan(root, "production", localDeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if diags.hasErrors() {
		t.Fatalf("untracked serve dir should not require --dirty, got %+v", diags)
	}
	if !strings.Contains(plan.Release, "-s") {
		t.Fatalf("release should include static hash suffix, got %q", plan.Release)
	}
}

func TestBuildLocalDeployPlanRejectsDotenvInsideServeDir(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"

[routes.docs]
host = "api.example.com"
path = "/docs"
serve = "dist"
`)
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", ".env"), []byte("SECRET=bad"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init")
	runGit(t, root, "add", "Dockerfile", "simple-vps.toml")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")

	_, diags, err := buildLocalDeployPlan(root, "production", localDeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	errors := diags.errorMessages()
	if len(errors) != 1 || !strings.Contains(errors[0], "dist/.env") {
		t.Fatalf("expected serve dotenv rejection, got %+v", diags)
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

func TestWriteSourceTarUsesAppRootInMonorepo(t *testing.T) {
	repo := t.TempDir()
	appRoot := filepath.Join(repo, "apps", "api")
	if err := os.MkdirAll(appRoot, 0755); err != nil {
		t.Fatal(err)
	}
	writeClientDockerfile(t, appRoot)
	writeClientManifest(t, appRoot, `name = "api"

[env.production]
server = "deploy@example.com"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "api.example.com"
process = "web"
`)
	if err := os.WriteFile(filepath.Join(repo, "root-only.txt"), []byte("should not deploy"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")

	tarPath := filepath.Join(t.TempDir(), "source.tar")
	if err := writeSourceTar(appRoot, tarPath, false, nil); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("tar", "-tf", tarPath).CombinedOutput()
	if err != nil {
		t.Fatalf("tar list failed: %v\n%s", err, out)
	}
	list := string(out)
	for _, want := range []string{"Dockerfile", "simple-vps.toml"} {
		if !strings.Contains(list, want) {
			t.Fatalf("archive missing %s:\n%s", want, list)
		}
	}
	if strings.Contains(list, "root-only.txt") || strings.Contains(list, "apps/api/") {
		t.Fatalf("archive should contain app-root contents only:\n%s", list)
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
	plan := testLocalDeployPlan("abc1234", false)
	got := serverAppApplyCommand("api", "production", "/tmp/simple-vps-deploy/x.tar", "/tmp/simple-vps-deploy/x.toml", plan, false)
	want := "sudo -n /usr/local/bin/simple-vps server app apply --tarball /tmp/simple-vps-deploy/x.tar --manifest /tmp/simple-vps-deploy/x.toml --sha abc1234 --base-commit abc1234abc1234abc1234abc1234abc1234abc1234 --created-at 2026-05-30T14:30:12Z api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppApplyCommandSupportsRebuild(t *testing.T) {
	plan := testLocalDeployPlan("abc1234", true)
	got := serverAppApplyCommand("api", "production", "/tmp/simple-vps-deploy/x.tar", "/tmp/simple-vps-deploy/x.toml", plan, true)
	want := "sudo -n /usr/local/bin/simple-vps server app apply --rebuild --dirty --tarball /tmp/simple-vps-deploy/x.tar --manifest /tmp/simple-vps-deploy/x.toml --sha abc1234 --base-commit abc1234abc1234abc1234abc1234abc1234abc1234 --created-at 2026-05-30T14:30:12Z api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func testLocalDeployPlan(release string, dirty bool) localDeployPlan {
	return localDeployPlan{
		Release:    release,
		BaseCommit: "abc1234abc1234abc1234abc1234abc1234abc1234",
		Dirty:      dirty,
		CreatedAt:  time.Date(2026, 5, 30, 14, 30, 12, 0, time.UTC),
	}
}

func TestServerAppSetupEnvCommand(t *testing.T) {
	got := serverAppSetupEnvCommand("api", "production")
	want := "sudo -n /usr/local/bin/simple-vps server app setup-env api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppPreflightCommandIncludesRequiredSecrets(t *testing.T) {
	got := serverAppPreflightCommand("api", "production", []string{"DATABASE_URL", "API_KEY"})
	want := "sudo -n /usr/local/bin/simple-vps server app preflight --secret DATABASE_URL --secret API_KEY api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL"})
	want = "sudo -n /usr/local/bin/simple-vps server app preflight --json --secret DATABASE_URL api production"
	if got != want {
		t.Fatalf("unexpected json command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppListCommandSupportsJSON(t *testing.T) {
	got := serverAppListCommand(false)
	want := "sudo -n /usr/local/bin/simple-vps server app list"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppListCommand(true)
	want = "sudo -n /usr/local/bin/simple-vps server app list --json"
	if got != want {
		t.Fatalf("unexpected json command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppRollbackCommandSupportsRelease(t *testing.T) {
	got := serverAppRollbackCommand("api", "production", "")
	want := "sudo -n /usr/local/bin/simple-vps server app rollback api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppRollbackCommand("api", "production", "abc1234")
	want = "sudo -n /usr/local/bin/simple-vps server app rollback api production abc1234"
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
			want: "sudo -n /usr/local/bin/simple-vps server app backup create api production",
		},
		{
			name: "create json",
			got:  serverAppBackupCommand("api", "production", "", true),
			want: "sudo -n /usr/local/bin/simple-vps server app backup create --json api production",
		},
		{
			name: "create to",
			got:  serverAppBackupCommand("api", "production", "/tmp/backups", false),
			want: "sudo -n /usr/local/bin/simple-vps server app backup create --to /tmp/backups api production",
		},
		{
			name: "list",
			got:  serverAppBackupListCommand("api", "production", false),
			want: "sudo -n /usr/local/bin/simple-vps server app backup list api production",
		},
		{
			name: "list json",
			got:  serverAppBackupListCommand("api", "production", true),
			want: "sudo -n /usr/local/bin/simple-vps server app backup list --json api production",
		},
		{
			name: "rm",
			got:  serverAppBackupRmCommand("api", "production", "backup-id"),
			want: "sudo -n /usr/local/bin/simple-vps server app backup rm api production backup-id",
		},
		{
			name: "restore",
			got:  serverAppRestoreCommand("api", "production", "backup-id", false),
			want: "sudo -n /usr/local/bin/simple-vps server app backup restore --from backup-id api production",
		},
		{
			name: "restore dry run",
			got:  serverAppRestoreCommand("api", "production", "backup-id", true),
			want: "sudo -n /usr/local/bin/simple-vps server app backup restore --from backup-id --dry-run api production",
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
	want := "sudo -n /usr/local/bin/simple-vps server app destroy-env api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppDestroyEnvCommand("api", "production", true)
	want = "sudo -n /usr/local/bin/simple-vps server app destroy-env --purge api production"
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
			want: "sudo -n /usr/local/bin/simple-vps server status",
		},
		{
			name: "status json",
			got:  serverStatusCommand(true),
			want: "sudo -n /usr/local/bin/simple-vps server status --json",
		},
		{
			name: "doctor text",
			got:  serverDoctorCommand(false),
			want: "sudo -n /usr/local/bin/simple-vps server doctor",
		},
		{
			name: "doctor json",
			got:  serverDoctorCommand(true),
			want: "sudo -n /usr/local/bin/simple-vps server doctor --json",
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
	want := "sudo -n /usr/local/bin/simple-vps server app secret list api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppSecretListCommand("api", "production", true)
	want = "sudo -n /usr/local/bin/simple-vps server app secret list --json api production"
	if got != want {
		t.Fatalf("unexpected json command:\nwant: %s\n got: %s", want, got)
	}
}

type fakeSSHRunner struct {
	responses map[string]string
	failures  map[string]string
	commands  []string
}

func (f *fakeSSHRunner) RunSSH(_ string, command string) (string, string, int, error) {
	f.commands = append(f.commands, command)
	if out, ok := f.failures[command]; ok {
		return out, "", 1, nil
	}
	if out, ok := f.responses[command]; ok {
		return out, "", 0, nil
	}
	return "", fmt.Sprintf("unexpected command: %s", command), 1, nil
}

func TestDeployRemotePreflightIsReadOnlyAndChecksSecrets(t *testing.T) {
	ctx := &config.AppContext{
		AppName:    "api",
		EnvName:    "production",
		Server:     "deploy@example.com",
		SecretRefs: map[string]string{"DATABASE_URL": "DATABASE_URL"},
	}
	runner := &fakeSSHRunner{responses: map[string]string{
		"true":                              `ok`,
		"test -x /usr/local/bin/simple-vps": "",
		"command -v rsync >/dev/null":       "",
		serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL"}): `{"app":"api","env":"production","healthy":true,"findings":[]}`,
	}}

	if err := deployRemotePreflight(runner, ctx); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.commands, "\n")
	for _, mutating := range []string{"mkdir", "setup-env", "apply", "podman run", "podman rm"} {
		if strings.Contains(joined, mutating) {
			t.Fatalf("preflight ran mutating command %q:\n%s", mutating, joined)
		}
	}
}

func TestDeployRemotePreflightFailsMissingSecrets(t *testing.T) {
	ctx := &config.AppContext{
		AppName:    "api",
		EnvName:    "production",
		Server:     "deploy@example.com",
		SecretRefs: map[string]string{"DATABASE_URL": "DATABASE_URL"},
	}
	runner := &fakeSSHRunner{responses: map[string]string{
		"true":                              `ok`,
		"test -x /usr/local/bin/simple-vps": "",
		"command -v rsync >/dev/null":       "",
	}, failures: map[string]string{
		serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL"}): `{"app":"api","env":"production","healthy":false,"findings":["missing secret DATABASE_URL; run ` + "`" + `simple-vps secret set DATABASE_URL --env production` + "`" + `"]}`,
	}}

	err := deployRemotePreflight(runner, ctx)
	if err == nil || !strings.Contains(err.Error(), "missing secret DATABASE_URL") || !strings.Contains(err.Error(), "simple-vps secret set DATABASE_URL --env production") {
		t.Fatalf("expected missing secret hint, got %v", err)
	}
	if !strings.Contains(err.Error(), "No remote files, routes, or containers were changed.") {
		t.Fatalf("expected read-only preflight boundary in error, got %v", err)
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

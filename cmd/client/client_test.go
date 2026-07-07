package client

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/errcat"
)

func writeClientManifest(t *testing.T, root string, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "ship.toml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeClientDockerfile(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func clientContainerManifest() string {
	return `name = "api"
box = "deploy@example.com"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = "web"
`
}

func clientMixedManifest() string {
	return `name = "api"
box = "deploy@example.com"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = "web"
"api.example.com/docs" = { static = "dist" }
`
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

func TestSanitizeBranchEnvName(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{branch: "feat/x", want: "feat-x"},
		{branch: "--Feat///X--", want: "feat-x"},
		{branch: "mañana/Über", want: "ma-ana-ber"},
		{branch: "こんにちは-feature", want: "feature"},
		{branch: strings.Repeat("a", 40), want: strings.Repeat("a", 28)},
		{branch: strings.Repeat("a", 27) + "/x", want: strings.Repeat("a", 27)},
	}
	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			if got := sanitizeBranchEnvName(tt.branch); got != tt.want {
				t.Fatalf("sanitizeBranchEnvName(%q) = %q, want %q", tt.branch, got, tt.want)
			}
		})
	}
}

func TestEnvNameForBranchRejectsUnmappableBranchName(t *testing.T) {
	_, err := envNameForBranch("日本語", "main")
	if !errcat.Is(err, errcat.CodeUnmappableBranchName) || !strings.Contains(err.Error(), "next: git branch -m <new-name>") {
		t.Fatalf("expected unmappable_branch_name with rename guidance, got %v", err)
	}
}

func TestResolveDeployAddressMapsBranchesToEnvs(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
	initCommittedGitApp(t, root, "main")

	addr, err := resolveDeployAddress(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if addr.EnvName != "prod" || !addr.ProductionBranch || addr.Branch != "main" {
		t.Fatalf("main should resolve to prod production branch, got %+v", addr)
	}

	runGit(t, root, "checkout", "-B", "feat/x")
	addr, err = resolveDeployAddress(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if addr.EnvName != "feat-x" || addr.ProductionBranch {
		t.Fatalf("feat/x should resolve to preview feat-x, got %+v", addr)
	}
}

func TestResolveDeployAddressHonorsProductionBranchOverride(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, `name = "api"
box = "deploy@example.com"
production_branch = "stable"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = "web"
`)
	initCommittedGitApp(t, root, "main")

	addr, err := resolveDeployAddress(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if addr.EnvName != "main" || addr.ProductionBranch {
		t.Fatalf("main should be a preview when production_branch=stable, got %+v", addr)
	}

	runGit(t, root, "checkout", "-B", "stable")
	addr, err = resolveDeployAddress(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if addr.EnvName != "prod" || !addr.ProductionBranch {
		t.Fatalf("stable should resolve to prod, got %+v", addr)
	}
}

func TestResolveDeployAddressDetachedBranchGate(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
	initCommittedGitApp(t, root, "main")

	if _, err := resolveDeployAddress(root, "", "feat/x"); !errcat.Is(err, errcat.CodeBranchFlagRequiresDetachedHead) {
		t.Fatalf("expected checked-out --branch rejection, got %v", err)
	}

	runGit(t, root, "checkout", "--detach")
	if _, err := resolveDeployAddress(root, "", ""); !errcat.Is(err, errcat.CodeDetachedHeadRequiresBranch) {
		t.Fatalf("expected detached HEAD rejection, got %v", err)
	}

	addr, err := resolveDeployAddress(root, "", "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if addr.EnvName != "feat-x" || addr.Branch != "feat/x" {
		t.Fatalf("detached --branch should resolve preview env, got %+v", addr)
	}
}

func TestResolveDeployAddressReportsNotGitRepo(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())

	_, err := resolveDeployAddress(root, "", "")
	if !errcat.Is(err, errcat.CodeNotAGitRepo) || !strings.Contains(err.Error(), "next:") {
		t.Fatalf("expected not_a_git_repo with next step, got %v", err)
	}
}

func TestWriteShipResultOutputContracts(t *testing.T) {
	result := ShipResult{
		URL:        "https://api.example.com",
		Env:        "prod",
		Release:    "abc1234",
		Processes:  []string{"web"},
		DurationMs: 1234,
	}

	urlOut := captureClientStdout(t, func() {
		writeShipResult(result, false)
	})
	if urlOut != "https://api.example.com\n" {
		t.Fatalf("plain ship stdout = %q, want exactly URL newline", urlOut)
	}

	jsonOut := captureClientStdout(t, func() {
		writeShipResult(result, true)
	})
	var payload ShipResult
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("ship --json stdout is not JSON: %v\n%s", err, jsonOut)
	}
	if payload.URL != result.URL || payload.Env != result.Env || payload.Release != result.Release || payload.DurationMs != result.DurationMs || len(payload.Processes) != 1 || payload.Processes[0] != "web" {
		t.Fatalf("unexpected ship --json payload: %+v", payload)
	}
}

func TestRewriteRollbackSummaryUsesSurfaceEnvironmentName(t *testing.T) {
	read := readContext{
		AppContext: &config.AppContext{AppName: "api", EnvName: "prod", ProductionBranch: "main"},
		Address:    readAddress{EnvName: "prod", ProductionBranch: true},
		EnvName:    "prod",
	}
	out := rewriteRollbackSummary("Rolled back api (prod) from def456 to abc123\n  web          running\n", read)
	if !strings.Contains(out, "Rolled back Production main from def456 to abc123") {
		t.Fatalf("rollback summary leaked internal env:\n%s", out)
	}
	if !strings.Contains(out, "web") {
		t.Fatalf("rollback process lines should be preserved:\n%s", out)
	}
}

func TestRewriteRestoreSummaryUsesSurfaceEnvironmentName(t *testing.T) {
	read := readContext{
		AppContext: &config.AppContext{AppName: "api", EnvName: "feat-x-ab12"},
		Address:    readAddress{EnvName: "feat-x", PreviewBranch: "feat/x"},
		EnvName:    "feat-x-ab12",
	}
	out := rewriteRestoreSummary("Restored api (feat-x-ab12) from backup-id at release abc123\n", read)
	if !strings.Contains(out, "Restored Preview feat/x from backup-id at release abc123") {
		t.Fatalf("restore summary leaked internal env:\n%s", out)
	}
}

func TestDeploymentURLSynthesizesSSLIPWithoutRoutes(t *testing.T) {
	ctx := &config.AppContext{AppName: "api", EnvName: "prod", Server: "deploy@203.0.113.7"}
	got := deploymentURL(ctx, "prod")
	want := "https://prod.203-0-113-7.sslip.io"
	if got != want {
		t.Fatalf("fallback URL = %q, want %q", got, want)
	}
}

func TestDeploymentURLPrefersRootWebRoute(t *testing.T) {
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "prod",
		Server:  "deploy@example.com",
		Routes: map[string]config.Route{
			"api.example.com/docs": {Host: "api.example.com", Path: "/docs", Process: "web"},
			"api.example.com":      {Host: "api.example.com", Process: "web"},
		},
	}
	got := deploymentURL(ctx, "prod")
	want := "https://api.example.com"
	if got != want {
		t.Fatalf("routed URL = %q, want %q", got, want)
	}
}

func TestPrepareDeployRoutesSynthesizesSSLIPRouteForRoutelessApp(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "prod",
		Server:  "deploy@example.com",
		Processes: map[string]config.Process{
			"web": {Port: &port},
		},
	}
	plan, err := prepareDeployRoutes(ctx, "prod", deployRouteOptions{BoxIP: "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RewritesManifest || !plan.NoConfiguredDomain {
		t.Fatalf("expected rewritten no-domain plan: %+v", plan)
	}
	route := plan.Context.Routes["prod.203-0-113-7.sslip.io"]
	if route.Host != "prod.203-0-113-7.sslip.io" || route.Process != "web" {
		t.Fatalf("unexpected synthesized route: %+v", route)
	}
}

func TestPrepareDeployRoutesRejectsMultipleProcessesWithoutWeb(t *testing.T) {
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "prod",
		Processes: map[string]config.Process{
			"api":    {},
			"worker": {},
		},
	}
	_, err := prepareDeployRoutes(ctx, "prod", deployRouteOptions{BoxIP: "203.0.113.7"})
	if !errcat.Is(err, errcat.CodeMultiProcessNoWebRoute) {
		t.Fatalf("expected multi-process/no-web error, got %v", err)
	}
}

func TestPrepareDeployRoutesCollapsesPreviewToSSLIPHost(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "feat-x-ab12",
		Server:  "deploy@example.com",
		Processes: map[string]config.Process{
			"web": {Port: &port},
		},
		Routes: map[string]config.Route{
			"api.example.com":      {Host: "api.example.com", Process: "web"},
			"api.example.com/docs": {Host: "api.example.com", Path: "/docs", Serve: "dist"},
			"old.example.com":      {Host: "old.example.com", Redirect: "api.example.com"},
		},
	}
	plan, err := prepareDeployRoutes(ctx, "feat-x-ab12", deployRouteOptions{Preview: true, TLS: "internal", BoxIP: "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RewritesManifest || plan.NoConfiguredDomain {
		t.Fatalf("unexpected preview route plan: %+v", plan)
	}
	if _, ok := plan.Context.Routes["old.example.com"]; ok {
		t.Fatalf("preview routes should drop redirects and extra hosts: %+v", plan.Context.Routes)
	}
	root := plan.Context.Routes["feat-x-ab12.203-0-113-7.sslip.io"]
	docs := plan.Context.Routes["feat-x-ab12.203-0-113-7.sslip.io/docs"]
	if root.Host != "feat-x-ab12.203-0-113-7.sslip.io" || root.Process != "web" || root.TLS != "internal" {
		t.Fatalf("unexpected preview root route: %+v", root)
	}
	if docs.Host != "feat-x-ab12.203-0-113-7.sslip.io" || docs.Path != "/docs" || docs.Serve != "dist" || docs.TLS != "internal" {
		t.Fatalf("unexpected preview docs route: %+v", docs)
	}
}

func TestWriteDeployManifestOverlaysRoutesAsParseableTOML(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
	dst := filepath.Join(root, "upload.toml")
	routes := map[string]config.Route{
		"prod.203-0-113-7.sslip.io": {Host: "prod.203-0-113-7.sslip.io", Process: "web", TLS: "internal"},
	}
	if err := writeDeployManifest(filepath.Join(root, ManifestFile), dst, routes); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestFile), data, 0644); err != nil {
		t.Fatal(err)
	}
	ctx, err := config.LoadAppContext(root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	route := ctx.Routes["prod.203-0-113-7.sslip.io"]
	if route.Process != "web" || route.TLS != "internal" {
		t.Fatalf("overlay route did not round-trip: %+v\n%s", route, string(data))
	}
}

func TestResolveDeployAddressDetectsStagedAndUnstagedDirtyState(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
	initCommittedGitApp(t, root, "main")

	if err := os.WriteFile(filepath.Join(root, "unstaged.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	addr, err := resolveDeployAddress(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !addr.Dirty {
		t.Fatal("unstaged file should mark worktree dirty")
	}

	runGit(t, root, "add", "unstaged.txt")
	addr, err = resolveDeployAddress(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !addr.Dirty {
		t.Fatal("staged file should mark worktree dirty")
	}
}

func TestEnforceProductionAncestryRejectsBehindProduction(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
	initCommittedGitApp(t, root, "main")
	first := gitHead(t, root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("new production"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "new production")
	deployed := gitHead(t, root)
	runGit(t, root, "reset", "--hard", first)

	ctx := &config.AppContext{AppName: "api", EnvName: "prod", Server: "deploy@example.com"}
	runner := &fakeSSHRunner{responses: map[string]string{
		serverAppStatusCommand("api", "prod", true): `{"app":"api","env":"prod","release":{"release":"` + deployed[:12] + `","base_commit":"` + deployed + `","source":"process"},"processes":[]}`,
	}}

	err := enforceProductionAncestry(root, runner, ctx, first)
	if !errcat.Is(err, errcat.CodeBehindProduction) || !strings.Contains(err.Error(), "next: git pull") {
		t.Fatalf("expected behind_production, got %v", err)
	}
}

func TestEnforceProductionAncestryAllowsFirstDeployAndAncestor(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
	initCommittedGitApp(t, root, "main")
	first := gitHead(t, root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("new production"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "new production")
	head := gitHead(t, root)

	ctx := &config.AppContext{AppName: "api", EnvName: "prod", Server: "deploy@example.com"}
	firstDeploy := &fakeSSHRunner{responses: map[string]string{
		serverAppStatusCommand("api", "prod", true): `{"app":"api","env":"prod","processes":[]}`,
	}}
	if err := enforceProductionAncestry(root, firstDeploy, ctx, head); err != nil {
		t.Fatalf("first deploy should skip ancestry check: %v", err)
	}

	ancestor := &fakeSSHRunner{responses: map[string]string{
		serverAppStatusCommand("api", "prod", true): `{"app":"api","env":"prod","release":{"release":"` + first[:12] + `","base_commit":"` + first + `","source":"process"},"processes":[]}`,
	}}
	if err := enforceProductionAncestry(root, ancestor, ctx, head); err != nil {
		t.Fatalf("ancestor deployed commit should pass: %v", err)
	}
}

func TestResolveReadPreviewEnvPropagatesUnknownBranchError(t *testing.T) {
	ctx := &config.AppContext{AppName: "api", EnvName: "prod", Server: "deploy@example.com"}
	command := serverAppPreviewResolveCommand("api", "feat/x")
	runner := &fakeSSHRunner{failures: map[string]string{
		command: errcat.New(errcat.CodeUnknownPreviewBranch, errcat.Fields{
			"branch":  "\"feat/x\"",
			"command": "git checkout feat/x && ship",
		}).JSONLine(),
	}}

	_, err := resolveReadPreviewEnv(runner, ctx, readAddress{PreviewBranch: "feat/x"})
	if err == nil {
		t.Fatal("expected unknown preview branch error")
	}
	want := "preview environment lookup failed\nno preview environment is mapped for branch \"feat/x\"\nnext: git checkout feat/x && ship"
	if !errcat.Is(err, errcat.CodeUnknownPreviewBranch) || err.Error() != want {
		t.Fatalf("unexpected error:\nwant: %q\n got: %q", want, err.Error())
	}
	if len(runner.commands) != 1 || runner.commands[0] != command {
		t.Fatalf("unexpected commands: %v", runner.commands)
	}
}

func TestCheckAndDeployShareDirtyWorktreeDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
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

func TestCommandRunnerUsesDefaultDeployKeyWhenPresent(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	defaultKey := filepath.Join(sshDir, "ship-deploy")
	if err := os.WriteFile(defaultKey, []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHIP_SSH_KEY", "")

	runner, err := NewCommandRunner()
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	assertSSHOptionSequence(t, runner.SshOptions, "-i", defaultKey)
	assertSSHOptionSequence(t, runner.SshOptions, "-o", "IdentitiesOnly=yes")
	if strings.Contains(strings.Join(runner.SshOptions, " "), "UserKnownHostsFile") {
		t.Fatalf("default key path should use normal known_hosts, got %v", runner.SshOptions)
	}
}

func TestCommandRunnerDoesNotForceMissingDefaultDeployKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHIP_SSH_KEY", "")

	runner, err := NewCommandRunner()
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	if strings.Contains(strings.Join(runner.SshOptions, " "), "ship-deploy") {
		t.Fatalf("missing default key should not be forced, got %v", runner.SshOptions)
	}
}

func TestCommandRunnerEnvKeyUsesNormalKnownHosts(t *testing.T) {
	t.Setenv("SHIP_SSH_KEY", "test-private-key")

	runner, err := NewCommandRunner()
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	assertSSHOptionSequence(t, runner.SshOptions, "-o", "IdentitiesOnly=yes")
	if strings.Contains(strings.Join(runner.SshOptions, " "), "UserKnownHostsFile") {
		t.Fatalf("env key should use normal known_hosts, got %v", runner.SshOptions)
	}
}

func TestCheckDiagnosticsExplainsMissingGitRepo(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())

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
box = "deploy@example.com"
probe = "/health"

[env]
DATABASE_URL = "@secret:DATABASE_URL"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = "web"
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
	if !strings.Contains(diags[0].Hint, "ship secret set DATABASE_URL") {
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
	writeClientManifest(t, appRoot, clientContainerManifest())
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
	writeClientManifest(t, root, clientContainerManifest())
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
	writeClientManifest(t, root, clientMixedManifest())
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "index.html"), []byte("generated"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init")
	runGit(t, root, "add", "Dockerfile", "ship.toml")
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
	writeClientManifest(t, root, clientMixedManifest())
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", ".env"), []byte("SECRET=bad"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init")
	runGit(t, root, "add", "Dockerfile", "ship.toml")
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
	writeClientManifest(t, appRoot, clientContainerManifest())
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
	for _, want := range []string{"Dockerfile", "ship.toml"} {
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

func initCommittedGitApp(t *testing.T, root, branch string) {
	t.Helper()
	runGit(t, root, "init")
	runGit(t, root, "checkout", "-B", branch)
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")
}

func gitHead(t *testing.T, root string) string {
	t.Helper()
	out := strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD"))
	if out == "" {
		t.Fatal("git rev-parse HEAD returned empty output")
	}
	return out
}

func runGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func assertSSHOptionSequence(t *testing.T, opts []string, first string, second string) {
	t.Helper()
	for i := 0; i < len(opts)-1; i++ {
		if opts[i] == first && opts[i+1] == second {
			return
		}
	}
	t.Fatalf("expected SSH option sequence %q %q in %v", first, second, opts)
}

func captureClientStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
	}()

	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestServerAppApplyCommandPutsTypedFlagsBeforePositional(t *testing.T) {
	plan := testLocalDeployPlan("abc1234", false)
	actor := testDeployIdentity()
	got := serverAppApplyCommand("api", "production", "/tmp/simple-vps-deploy/x.tar", "/tmp/simple-vps-deploy/x.toml", plan, actor, false)
	want := "sudo -n /usr/local/bin/ship server app apply --tarball /tmp/simple-vps-deploy/x.tar --manifest /tmp/simple-vps-deploy/x.toml --sha abc1234 --base-commit abc1234abc1234abc1234abc1234abc1234abc1234 --created-at 2026-05-30T14:30:12Z --ssh-key-comment fake-vps-smoke --git-author 'Smoke <smoke@example.com>' api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppApplyCommandSupportsRebuild(t *testing.T) {
	plan := testLocalDeployPlan("abc1234", true)
	actor := testDeployIdentity()
	got := serverAppApplyCommand("api", "production", "/tmp/simple-vps-deploy/x.tar", "/tmp/simple-vps-deploy/x.toml", plan, actor, true)
	want := "sudo -n /usr/local/bin/ship server app apply --rebuild --dirty --tarball /tmp/simple-vps-deploy/x.tar --manifest /tmp/simple-vps-deploy/x.toml --sha abc1234 --base-commit abc1234abc1234abc1234abc1234abc1234abc1234 --created-at 2026-05-30T14:30:12Z --ssh-key-comment fake-vps-smoke --git-author 'Smoke <smoke@example.com>' api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func testDeployIdentity() deployIdentityJSON {
	return deployIdentityJSON{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"}
}

func testLocalDeployPlan(release string, dirty bool) localDeployPlan {
	return localDeployPlan{
		Release:    release,
		BaseCommit: "abc1234abc1234abc1234abc1234abc1234abc1234",
		Dirty:      dirty,
		CreatedAt:  time.Date(2026, 5, 30, 14, 30, 12, 0, time.UTC),
	}
}

func TestServerCommandBuildersMatchSudoersShape(t *testing.T) {
	plan := testLocalDeployPlan("abc1234", true)
	actor := testDeployIdentity()
	commands := []struct {
		name    string
		command string
	}{
		{name: "doctor text", command: serverDoctorCommand(false)},
		{name: "doctor json", command: serverDoctorCommand(true)},
		{name: "setup env", command: serverAppSetupEnvCommand("api", "production")},
		{name: "preflight", command: serverAppPreflightCommand("api", "production", []string{"DATABASE_URL"})},
		{name: "preflight json", command: serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL"})},
		{name: "apply", command: serverAppApplyCommand("api", "production", "/tmp/simple-vps-deploy/x.tar", "/tmp/simple-vps-deploy/x.toml", plan, actor, true)},
		{name: "status text", command: serverAppStatusCommand("api", "production", false)},
		{name: "status json", command: serverAppStatusCommand("api", "production", true)},
		{name: "list text", command: serverAppListCommand(false)},
		{name: "list json", command: serverAppListCommand(true)},
		{name: "logs", command: serverAppLogsCommand("api", "production", "web", false, 50)},
		{name: "logs follow", command: serverAppLogsCommand("api", "production", "", true, 0)},
		{name: "exec pipes", command: serverAppExecCommand("api", "production", false, []string{"sh", "-c", "exit 7"})},
		{name: "exec tty", command: serverAppExecCommand("api", "production", true, []string{"env"})},
		{name: "rollback latest", command: serverAppRollbackCommand("api", "production", "", actor)},
		{name: "rollback release", command: serverAppRollbackCommand("api", "production", "abc1234", actor)},
		{name: "backup create", command: serverAppBackupCommand("api", "production", "", false)},
		{name: "backup json", command: serverAppBackupCommand("api", "production", "", true)},
		{name: "backup to", command: serverAppBackupCommand("api", "production", "/tmp/backups", false)},
		{name: "restore", command: serverAppRestoreCommand("api", "production", "backup-id", false)},
		{name: "restore dry run", command: serverAppRestoreCommand("api", "production", "backup-id", true)},
		{name: "destroy env", command: serverAppDestroyEnvCommand("api", "production", false)},
		{name: "destroy env purge", command: serverAppDestroyEnvCommand("api", "production", true)},
		{name: "preview resolve or create", command: serverAppPreviewResolveOrCreateCommand("api", "feat/x")},
		{name: "preview resolve", command: serverAppPreviewResolveCommand("api", "feat/x")},
		{name: "preview pin", command: serverAppPreviewPinCommand("api", "feat/x")},
		{name: "preview unpin", command: serverAppPreviewUnpinCommand("api", "feat/x")},
		{name: "secret set", command: serverAppSecretSetCommand("api", "production", "DATABASE_URL")},
		{name: "secret list", command: serverAppSecretListCommand("api", "production", false)},
		{name: "secret list json", command: serverAppSecretListCommand("api", "production", true)},
		{name: "secret rm", command: serverAppSecretRmCommand("api", "production", "DATABASE_URL")},
		{name: "why", command: serverAppWhyCommand("api", "production")},
	}

	for _, tt := range commands {
		t.Run(tt.name, func(t *testing.T) {
			assertServerCommandCoveredBySudoers(t, tt.command)
		})
	}
}

func assertServerCommandCoveredBySudoers(t *testing.T, command string) {
	t.Helper()
	const prefix = "sudo -n /usr/local/bin/ship server "
	if !strings.HasPrefix(command, prefix) {
		t.Fatalf("remote helper command must start exactly with %q:\n%s", prefix, command)
	}
	subcommand := strings.TrimPrefix(command, prefix)
	if !serverSubcommandCoveredBySudoers(subcommand) {
		t.Fatalf("remote helper command is outside sudoers grant patterns:\n%s", command)
	}
}

func serverSubcommandCoveredBySudoers(subcommand string) bool {
	return strings.HasPrefix(subcommand, "app ") ||
		subcommand == "status" ||
		strings.HasPrefix(subcommand, "status ") ||
		subcommand == "doctor" ||
		strings.HasPrefix(subcommand, "doctor ")
}

func TestServerAppSetupEnvCommand(t *testing.T) {
	got := serverAppSetupEnvCommand("api", "production")
	want := "sudo -n /usr/local/bin/ship server app setup-env api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppPreflightCommandIncludesRequiredSecrets(t *testing.T) {
	got := serverAppPreflightCommand("api", "production", []string{"DATABASE_URL", "API_KEY"})
	want := "sudo -n /usr/local/bin/ship server app preflight --secret DATABASE_URL --secret API_KEY api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL"})
	want = "sudo -n /usr/local/bin/ship server app preflight --json --secret DATABASE_URL api production"
	if got != want {
		t.Fatalf("unexpected json command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppListCommandSupportsJSON(t *testing.T) {
	got := serverAppListCommand(false)
	want := "sudo -n /usr/local/bin/ship server app list"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppListCommand(true)
	want = "sudo -n /usr/local/bin/ship server app list --json"
	if got != want {
		t.Fatalf("unexpected json command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppWhyCommandUsesJSON(t *testing.T) {
	got := serverAppWhyCommand("api", "prod")
	want := "sudo -n /usr/local/bin/ship server app why --json api prod"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppExecCommand(t *testing.T) {
	got := serverAppExecCommand("api", "prod", false, []string{"sh", "-c", "exit 7"})
	want := "sudo -n /usr/local/bin/ship server app exec api prod -- sh -c 'exit 7'"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppExecCommand("api", "prod", true, []string{"env"})
	want = "sudo -n /usr/local/bin/ship server app exec --tty api prod -- env"
	if got != want {
		t.Fatalf("unexpected tty command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppRollbackCommandSupportsRelease(t *testing.T) {
	actor := testDeployIdentity()
	got := serverAppRollbackCommand("api", "production", "", actor)
	want := "sudo -n /usr/local/bin/ship server app rollback --ssh-key-comment fake-vps-smoke --git-author 'Smoke <smoke@example.com>' api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppRollbackCommand("api", "production", "abc1234", actor)
	want = "sudo -n /usr/local/bin/ship server app rollback --ssh-key-comment fake-vps-smoke --git-author 'Smoke <smoke@example.com>' api production abc1234"
	if got != want {
		t.Fatalf("unexpected release command:\nwant: %s\n got: %s", want, got)
	}
}

func TestRenderWhyReleaseFailure(t *testing.T) {
	read := readContext{
		AppContext: &config.AppContext{ProductionBranch: "main"},
		Address:    readAddress{ProductionBranch: true},
	}
	entry := whyJournalEntry{
		Outcome:          "aborted_release",
		EndedAt:          "2026-07-07T10:00:01Z",
		PreviousRelease:  "aaa111",
		AttemptedRelease: "bbb222",
		FailingStep:      "release",
		StderrTail:       "fake release command failed",
		Identity:         testDeployIdentity(),
	}
	got := renderWhy(entry, read)
	want := "Deploy aborted for Production main at 2026-07-07T10:00:01Z.\n" +
		"attempted release: bbb222\n" +
		"previous release: aaa111\n" +
		"failing step: release\n" +
		"probable cause: release command exited non-zero before traffic switched.\n" +
		"stderr tail:\n" +
		"fake release command failed\n" +
		"traffic: old release aaa111 kept serving; no traffic was switched.\n" +
		"shipped by: Smoke <smoke@example.com> (ssh key: fake-vps-smoke)\n" +
		"next: ship\n"
	if got != want {
		t.Fatalf("unexpected why output:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestRenderWhyProbeFailure(t *testing.T) {
	read := readContext{
		AppContext: &config.AppContext{ProductionBranch: "main"},
		Address:    readAddress{ProductionBranch: true},
	}
	entry := whyJournalEntry{
		Outcome:          "aborted_probe",
		EndedAt:          "2026-07-07T10:02:01Z",
		PreviousRelease:  "aaa111",
		AttemptedRelease: "ccc333",
		FailingStep:      "probe",
		StderrTail:       "HTTP status 502: upstream web listens on 3000, probed 3999",
		Identity:         testDeployIdentity(),
		Probe:            &whyJournalProbe{Status: 502, BodySnippet: "upstream web listens on 3000, probed 3999"},
	}
	got := renderWhy(entry, read)
	want := "Deploy aborted for Production main at 2026-07-07T10:02:01Z.\n" +
		"attempted release: ccc333\n" +
		"previous release: aaa111\n" +
		"failing step: probe\n" +
		"probable cause: probe returned HTTP 502 with body: upstream web listens on 3000, probed 3999\n" +
		"stderr tail:\n" +
		"HTTP status 502: upstream web listens on 3000, probed 3999\n" +
		"traffic: old release aaa111 kept serving; failed probes never receive traffic with the current engine.\n" +
		"shipped by: Smoke <smoke@example.com> (ssh key: fake-vps-smoke)\n" +
		"next: ship\n"
	if got != want {
		t.Fatalf("unexpected why output:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestStatusFromAppListIncludesShippedBy(t *testing.T) {
	ctx := &config.AppContext{
		AppName:          "api",
		ProductionBranch: "main",
		Routes: map[string]config.Route{
			"api.example.com": {Host: "api.example.com", Process: "web"},
		},
	}
	raw := `{"apps":[{"app":"api","env":"prod","shipped_by":{"ssh_key_comment":"fake-vps-smoke","git_author":"Smoke <smoke@example.com>"},"processes":[{"process":"web","container":"api-web","state":"running","release":"abc1234","created_at":"2026-07-07T10:00:00Z"}]}]}`
	payload, err := statusFromAppList(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Envs) != 1 || payload.Envs[0].ShippedBy == nil {
		t.Fatalf("status missing shipped_by: %+v", payload)
	}
	if payload.Envs[0].ShippedBy.GitAuthor != "Smoke <smoke@example.com>" || payload.Envs[0].ShippedBy.SSHKeyComment != "fake-vps-smoke" {
		t.Fatalf("wrong shipped_by: %+v", payload.Envs[0].ShippedBy)
	}
	text := renderStatusSummary(payload)
	if !strings.Contains(text, `shipped_by="Smoke <smoke@example.com>"`) || !strings.Contains(text, `ssh_key="fake-vps-smoke"`) {
		t.Fatalf("human status missing attribution:\n%s", text)
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
			want: "sudo -n /usr/local/bin/ship server app backup create api production",
		},
		{
			name: "create json",
			got:  serverAppBackupCommand("api", "production", "", true),
			want: "sudo -n /usr/local/bin/ship server app backup create --json api production",
		},
		{
			name: "create to",
			got:  serverAppBackupCommand("api", "production", "/tmp/backups", false),
			want: "sudo -n /usr/local/bin/ship server app backup create --to /tmp/backups api production",
		},
		{
			name: "restore",
			got:  serverAppRestoreCommand("api", "production", "backup-id", false),
			want: "sudo -n /usr/local/bin/ship server app backup restore --from backup-id api production",
		},
		{
			name: "restore dry run",
			got:  serverAppRestoreCommand("api", "production", "backup-id", true),
			want: "sudo -n /usr/local/bin/ship server app backup restore --from backup-id --dry-run api production",
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
	want := "sudo -n /usr/local/bin/ship server app destroy-env api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppDestroyEnvCommand("api", "production", true)
	want = "sudo -n /usr/local/bin/ship server app destroy-env --purge api production"
	if got != want {
		t.Fatalf("unexpected purge command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppPreviewCommands(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "resolve or create",
			got:  serverAppPreviewResolveOrCreateCommand("api", "feat/x"),
			want: "sudo -n /usr/local/bin/ship server app preview resolve-or-create api feat/x",
		},
		{
			name: "resolve",
			got:  serverAppPreviewResolveCommand("api", "feat/x"),
			want: "sudo -n /usr/local/bin/ship server app preview resolve api feat/x",
		},
		{
			name: "pin",
			got:  serverAppPreviewPinCommand("api", "feat/x"),
			want: "sudo -n /usr/local/bin/ship server app preview pin api feat/x",
		},
		{
			name: "unpin",
			got:  serverAppPreviewUnpinCommand("api", "feat/x"),
			want: "sudo -n /usr/local/bin/ship server app preview unpin api feat/x",
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

func TestServerDoctorCommandSupportsJSON(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "doctor text",
			got:  serverDoctorCommand(false),
			want: "sudo -n /usr/local/bin/ship server doctor",
		},
		{
			name: "doctor json",
			got:  serverDoctorCommand(true),
			want: "sudo -n /usr/local/bin/ship server doctor --json",
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
	want := "sudo -n /usr/local/bin/ship server app secret list api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppSecretListCommand("api", "production", true)
	want = "sudo -n /usr/local/bin/ship server app secret list --json api production"
	if got != want {
		t.Fatalf("unexpected json command:\nwant: %s\n got: %s", want, got)
	}
}

type fakeSSHRunner struct {
	responses map[string]string
	failures  map[string]string
	sequences map[string][]fakeSSHResult
	commands  []string
}

type fakeSSHResult struct {
	stdout string
	stderr string
	code   int
	err    error
}

func (f *fakeSSHRunner) RunSSH(_ string, command string) (string, string, int, error) {
	f.commands = append(f.commands, command)
	if seq, ok := f.sequences[command]; ok && len(seq) > 0 {
		result := seq[0]
		f.sequences[command] = seq[1:]
		return result.stdout, result.stderr, result.code, result.err
	}
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
		"true":                        `ok`,
		"test -x /usr/local/bin/ship": "",
		"command -v rsync >/dev/null": "",
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
		"true":                        `ok`,
		"test -x /usr/local/bin/ship": "",
		"command -v rsync >/dev/null": "",
	}, failures: map[string]string{
		serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL"}): `{"app":"api","env":"production","healthy":false,"issues":[{"code":"secret_missing","message":"missing secret DATABASE_URL; run ` + "`" + `ship secret set DATABASE_URL` + "`" + `"}],"findings":["missing secret DATABASE_URL; run ` + "`" + `ship secret set DATABASE_URL` + "`" + `"]}`,
	}}

	err := deployRemotePreflight(runner, ctx)
	if !errcat.Is(err, errcat.CodeSecretMissing) || !strings.Contains(err.Error(), "missing secret DATABASE_URL") || !strings.Contains(err.Error(), "ship secret set DATABASE_URL") {
		t.Fatalf("expected missing secret hint, got %v", err)
	}
}

func TestEnsureRemoteEnvReadyPreparesMissingEnv(t *testing.T) {
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "production",
		Server:  "deploy@example.com",
	}
	preflightCmd := serverAppPreflightJSONCommand("api", "production", nil)
	runner := &fakeSSHRunner{
		responses: map[string]string{
			"true":                        `ok`,
			"test -x /usr/local/bin/ship": "",
			"command -v rsync >/dev/null": "",
			serverAppSetupEnvCommand("api", "production"): "App api (production) is ready",
		},
		sequences: map[string][]fakeSSHResult{
			preflightCmd: {
				{stdout: `{"app":"api","env":"production","healthy":false,"issues":[{"code":"env_missing","message":"app env is not prepared: missing /var/apps/api.production"}],"findings":["app env is not prepared: missing /var/apps/api.production"]}`, code: 1},
				{stdout: `{"app":"api","env":"production","healthy":true,"issues":[],"findings":[]}`, code: 0},
			},
		},
	}

	if err := ensureRemoteEnvReadyForDeploy(runner, ctx); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.commands, "\n")
	if !strings.Contains(joined, serverAppSetupEnvCommand("api", "production")) {
		t.Fatalf("expected deploy preparation to run setup-env, got:\n%s", joined)
	}
}

func TestEnsureRemoteEnvReadyDoesNotPrepareWhenSecretsAreMissing(t *testing.T) {
	ctx := &config.AppContext{
		AppName:    "api",
		EnvName:    "production",
		Server:     "deploy@example.com",
		SecretRefs: map[string]string{"DATABASE_URL": "DATABASE_URL"},
	}
	preflightCmd := serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL"})
	runner := &fakeSSHRunner{
		responses: map[string]string{
			"true":                        `ok`,
			"test -x /usr/local/bin/ship": "",
			"command -v rsync >/dev/null": "",
		},
		failures: map[string]string{
			preflightCmd: `{"app":"api","env":"production","healthy":false,"issues":[{"code":"env_missing","message":"app env is not prepared: missing /var/apps/api.production"},{"code":"secret_missing","message":"missing secret DATABASE_URL; run ` + "`" + `ship secret set DATABASE_URL` + "`" + `"}],"findings":["app env is not prepared: missing /var/apps/api.production","missing secret DATABASE_URL; run ` + "`" + `ship secret set DATABASE_URL` + "`" + `"]}`,
		},
	}

	err := ensureRemoteEnvReadyForDeploy(runner, ctx)
	if err == nil || !strings.Contains(err.Error(), "missing secret DATABASE_URL") {
		t.Fatalf("expected missing secret error, got %v", err)
	}
	joined := strings.Join(runner.commands, "\n")
	if strings.Contains(joined, "setup-env") {
		t.Fatalf("deploy should not mutate when required secrets are missing:\n%s", joined)
	}
}

func TestEnsureRemoteEnvReadyUsesPostPrepareBoundaryForSecondPreflightFailure(t *testing.T) {
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "production",
		Server:  "deploy@example.com",
	}
	preflightCmd := serverAppPreflightJSONCommand("api", "production", nil)
	runner := &fakeSSHRunner{
		responses: map[string]string{
			"true":                        `ok`,
			"test -x /usr/local/bin/ship": "",
			"command -v rsync >/dev/null": "",
			serverAppSetupEnvCommand("api", "production"): "App api (production) is ready",
		},
		sequences: map[string][]fakeSSHResult{
			preflightCmd: {
				{stdout: `{"app":"api","env":"production","healthy":false,"issues":[{"code":"env_missing","message":"app env is not prepared: missing /var/apps/api.production"}],"findings":["app env is not prepared: missing /var/apps/api.production"]}`, code: 1},
				{stdout: `not-json`, stderr: `broken preflight`, code: 1},
			},
		},
	}

	err := ensureRemoteEnvReadyForDeploy(runner, ctx)
	if err == nil {
		t.Fatal("expected second preflight failure")
	}
	if !strings.Contains(err.Error(), "after preparing the app environment") {
		t.Fatalf("expected post-prepare error boundary, got %v", err)
	}
	if strings.Contains(err.Error(), "No remote files, routes, or containers were changed.") {
		t.Fatalf("post-prepare failure must not claim no remote files changed, got %v", err)
	}
}

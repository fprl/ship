package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/knownhosts"
)

const clientAlicePublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/ ignored"

func writeClientManifest(t *testing.T, root string, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "ship.toml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func readClientManifest(t *testing.T, root string) *config.Manifest {
	t.Helper()
	manifest, err := config.ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestResolveMemberAddSourceCanonicalizesPublicKeyMaterial(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alice.pub", "cami.pem"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(clientAlicePublicKey+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			input, err := resolveMemberAddSource("203.0.113.7", path, "alice", "shipper")
			if err != nil {
				t.Fatal(err)
			}
			want := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/"
			if len(input.Keys) != 1 || input.Keys[0] != want {
				t.Fatalf("normalized key material = %+v, want %q", input.Keys, want)
			}
		})
	}
}

func TestResolveMemberAddSourceAcceptsOnlyDocumentedSourceShapes(t *testing.T) {
	for _, source := range []string{"alice", "github:alice", "http://github.com/alice.keys"} {
		t.Run(source, func(t *testing.T) {
			_, err := resolveMemberAddSource("203.0.113.7", source, "alice", "shipper")
			if !errcat.Is(err, errcat.CodeUsageError) || !strings.Contains(err.Error(), "HTTPS keys-URL") {
				t.Fatalf("error = %v, want HTTPS keys-URL usage error", err)
			}
		})
	}
}

func TestFetchHTTPSMemberKeysFollowsOnlyHTTPSAndRecordsFinalURL(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, clientAlicePublicKey+"\n")
	}))
	defer server.Close()
	oldTransport := memberURLTransport
	memberURLTransport = server.Client().Transport
	t.Cleanup(func() { memberURLTransport = oldTransport })

	input, err := fetchHTTPSMemberKeys(server.URL+"/start", "203.0.113.7", "alice", "shipper")
	if err != nil {
		t.Fatal(err)
	}
	if input.FinalURL != server.URL+"/final" {
		t.Fatalf("final URL = %q, want %q", input.FinalURL, server.URL+"/final")
	}
	if got, want := input.Keys, []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/"}; !slicesEqual(got, want) {
		t.Fatalf("canonical keys = %q, want %q", got, want)
	}
}

func TestFetchHTTPSMemberKeysRejectsInsecureRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/insecure", http.StatusFound)
	}))
	defer server.Close()
	oldTransport := memberURLTransport
	memberURLTransport = server.Client().Transport
	t.Cleanup(func() { memberURLTransport = oldTransport })

	_, err := fetchHTTPSMemberKeys(server.URL, "203.0.113.7", "alice", "shipper")
	if err == nil || !strings.Contains(err.Error(), "insecure HTTP redirect") {
		t.Fatalf("error = %v, want insecure redirect rejection", err)
	}
}

func TestFetchHTTPSMemberKeysBoundsResponseAndKeyCount(t *testing.T) {
	t.Run("response size", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", memberMaxResponseSize+1))
		}))
		defer server.Close()
		oldTransport := memberURLTransport
		memberURLTransport = server.Client().Transport
		t.Cleanup(func() { memberURLTransport = oldTransport })
		_, err := fetchHTTPSMemberKeys(server.URL, "203.0.113.7", "alice", "shipper")
		if !errcat.Is(err, errcat.CodeOperationFailed) || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v, want bounded response error", err)
		}
	})

	t.Run("key count", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat(clientAlicePublicKey+"\n", memberMaxKeyCount+1))
		}))
		defer server.Close()
		oldTransport := memberURLTransport
		memberURLTransport = server.Client().Transport
		t.Cleanup(func() { memberURLTransport = oldTransport })
		_, err := fetchHTTPSMemberKeys(server.URL, "203.0.113.7", "alice", "shipper")
		if !errcat.Is(err, errcat.CodeOperationFailed) || !strings.Contains(err.Error(), "more than") {
			t.Fatalf("error = %v, want bounded key count error", err)
		}
	})
}

func TestMemberAddPlanDigestUsesStableSortedMaterialAndAllPlanInputs(t *testing.T) {
	input := memberAddInput{
		FinalURL: serverURLForTest("alice.keys"),
		Keys: []string{
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/",
		},
	}
	plan := newMemberAddPlan("203.0.113.7", "https://github.com/alice.keys", input, "alice", "shipper")
	reordered := input
	reordered.Keys = append([]string(nil), input.Keys...)
	if plan.Digest != newMemberAddPlan("203.0.113.7", "https://github.com/alice.keys", reordered, "alice", "shipper").Digest {
		t.Fatal("digest changed when normalized key order stayed the same")
	}
	for name, changed := range map[string]memberAddPlan{
		"box":    func() memberAddPlan { p := plan; p.Box = "203.0.113.8"; return p }(),
		"source": func() memberAddPlan { p := plan; p.SourceURL = "https://example.com/alice.keys"; return p }(),
		"final":  func() memberAddPlan { p := plan; p.FinalURL = "https://example.com/alice.keys"; return p }(),
		"name":   func() memberAddPlan { p := plan; p.Name = "bob"; return p }(),
		"role":   func() memberAddPlan { p := plan; p.Role = "agent"; return p }(),
	} {
		if digestMemberAddPlan(changed) == plan.Digest {
			t.Fatalf("digest did not change when %s changed", name)
		}
	}
	rendered := renderMemberAddPlan(plan)
	for _, want := range []string{
		"final source: " + input.FinalURL,
		"name: alice",
		"role: shipper",
		"material: ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/",
		"fingerprint: SHA256:DUvOnIMvzMmJVSD+t9uB9yD7f8nYIQt2y1vGztKOWTg",
		"next: ship box member add https://github.com/alice.keys 203.0.113.7 --name alice --role shipper --confirm alice@sha256:" + plan.Digest,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("plan output missing %q:\n%s", want, rendered)
		}
	}
}

func TestParseMemberConfirmRequiresExactShape(t *testing.T) {
	for _, value := range []string{"alice", "alice@sha256:not-hex", "@sha256:"} {
		if _, err := parseMemberConfirm(value, "https://github.com/alice.keys", "203.0.113.7", "alice", "shipper"); !errcat.Is(err, errcat.CodeUsageError) || !strings.Contains(err.Error(), "<name>@sha256:<plan-digest>") {
			t.Fatalf("confirm %q error = %v, want shape usage error", value, err)
		}
	}
}

func TestRunBoxMemberAddRejectsConfirmForLiteralSource(t *testing.T) {
	err := runBoxMemberAdd("203.0.113.7", clientAlicePublicKey, "alice", "shipper", "alice@sha256:"+strings.Repeat("0", 64))
	if !errcat.Is(err, errcat.CodeUsageError) || !strings.Contains(err.Error(), "only valid with an HTTPS keys-URL") {
		t.Fatalf("literal confirm error = %v, want usage error", err)
	}
}

func serverURLForTest(path string) string {
	return "https://example.test/" + path
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestDecodeRemoteOutcome(t *testing.T) {
	codedJSON := errcat.New(errcat.CodeOperationFailed, errcat.Fields{"detail": "remote failed", "command": "ship retry"}).JSONLine()
	transport := errcat.New(errcat.CodeSSHUnreachable, errcat.Fields{"target": "example.com", "detail": "connection refused"})

	for _, tt := range []struct {
		name              string
		stdout            string
		stderr            string
		code              int
		err               error
		fallback          string
		wantDetail        string
		wantTransport     bool
		wantRemote        bool
		wantForwardStderr bool
	}{
		{name: "coded JSON on stdout", stdout: codedJSON, stderr: "diagnostic\\n", code: 1, wantRemote: true, wantForwardStderr: true},
		{name: "coded JSON on stderr", stderr: codedJSON, code: 1, wantRemote: true},
		{name: "strips repeated error prefixes", stderr: " Error: Error: failed ", code: 1, wantDetail: "failed"},
		{name: "stdout fallback", stdout: " Error: failed on stdout ", code: 1, wantDetail: "failed on stdout"},
		{name: "caller fallback", code: 1, fallback: "remote command failed", wantDetail: "remote command failed"},
		{name: "transport coded error wins", stdout: codedJSON, stderr: "diagnostic", code: 1, err: transport, wantTransport: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			outcome := decodeRemoteOutcome(tt.stdout, tt.stderr, tt.code, tt.err, tt.fallback)
			if (outcome.TransportCoded != nil) != tt.wantTransport {
				t.Fatalf("transport coded = %v, want %v", outcome.TransportCoded, tt.wantTransport)
			}
			if (outcome.RemoteCoded != nil) != tt.wantRemote {
				t.Fatalf("remote coded = %v, want %v", outcome.RemoteCoded, tt.wantRemote)
			}
			if outcome.Detail != tt.wantDetail {
				t.Fatalf("detail = %q, want %q", outcome.Detail, tt.wantDetail)
			}
			if outcome.ForwardStderr != tt.wantForwardStderr {
				t.Fatalf("forward stderr = %v, want %v", outcome.ForwardStderr, tt.wantForwardStderr)
			}
		})
	}
}

func TestRunSSHRequiredUsesCallerRemediation(t *testing.T) {
	runner := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
		"status": {{stderr: "host unavailable", code: 1}},
	}}
	_, err := runSSHRequired(runner, "example.com", "status", "status failed", "ship status")
	coded, ok := errcat.As(err)
	if !ok {
		t.Fatalf("expected errcat error, got %v", err)
	}
	if got := coded.Remediation(); got != "ship status" {
		t.Fatalf("remediation = %q, want ship status", got)
	}
}

func TestRemoteCodedRemediationUsesLiteralTargetWhenBoxAddressIsUnknown(t *testing.T) {
	coded := errcat.New(errcat.CodeMemberUnknown, errcat.Fields{"box": "<box>", "fingerprint": "SHA256:unknown"})
	err := sshResultError("203.0.113.44", coded.JSONLine(), "", 1, nil, "", "member failed", "ship box member ls 203.0.113.44")
	remote, ok := errcat.As(err)
	if !ok {
		t.Fatalf("error = %v, want coded remote error", err)
	}
	if got, want := remote.Remediation(), "ship box member add <https-url|key|path> 203.0.113.44 --name <name>"; got != want {
		t.Fatalf("remediation = %q, want %q", got, want)
	}
}

func TestRenderStatusSummaryWithApprovalsUsesApprovalLs(t *testing.T) {
	got := renderStatusSummaryWithApprovals(statusPayload{App: "api"}, 1, "203.0.113.7")
	want := "No live envs for api\n1 approvals pending — ship box approval ls 203.0.113.7\n"
	if got != want {
		t.Fatalf("status summary = %q, want %q", got, want)
	}
}

func TestResolveMemberAddSourceRejectsInvalidPublicKeys(t *testing.T) {
	t.Run("invalid line", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "invalid.pub")
		if err := os.WriteFile(path, []byte("ssh-ed25519\n"), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := resolveMemberAddSource("203.0.113.7", path, "alice", "shipper")
		coded, ok := errcat.As(err)
		if !ok || coded.Code() != errcat.CodeSSHPublicKeyInvalid {
			t.Fatalf("code = %v, want %s", err, errcat.CodeSSHPublicKeyInvalid)
		}
		if got, want := coded.Cause(), "public key line must contain key type and key body"; got != want {
			t.Fatalf("detail = %q, want %q", got, want)
		}
	})

	t.Run("garbage base64 body", func(t *testing.T) {
		_, err := resolveMemberAddSource("203.0.113.7", "ssh-ed25519 not-base64", "alice", "shipper")
		coded, ok := errcat.As(err)
		if !ok || coded.Code() != errcat.CodeSSHPublicKeyInvalid {
			t.Fatalf("code = %v, want %s", err, errcat.CodeSSHPublicKeyInvalid)
		}
		if got, want := coded.Cause(), "public key body is not valid base64"; got != want {
			t.Fatalf("detail = %q, want %q", got, want)
		}
	})
}

func writeClientDockerfile(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func clientContainerManifest() string {
	return `name = "api"
box = "example.com"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = "web"
`
}

func clientMixedManifest() string {
	return `name = "api"
box = "example.com"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = "web"
"api.example.com/docs" = { static = "dist" }
`
}

func TestDefaultAppNameUsesCurrentDirectoryBase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ship-local-demo")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}

	if got := defaultAppName(root); got != "ship-local-demo" {
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

func TestEnvNameForBranchRejectsUnmappableBranchName(t *testing.T) {
	_, err := envNameForBranch("日本語", "main")
	if !errcat.Is(err, errcat.CodeUnmappableBranchName) || !strings.Contains(err.Error(), "next: git branch -m <new-name>") {
		t.Fatalf("expected unmappable_branch_name with rename guidance, got %v", err)
	}
}

func TestEnvNameForBranchRejectsProductionPreviewCollision(t *testing.T) {
	_, err := envNameForBranch("production", "main")
	if !errcat.Is(err, errcat.CodeUnmappableBranchName) {
		t.Fatalf("expected production preview collision rejection, got %v", err)
	}
}

func TestResolveDeployAddressMapsBranchesToEnvs(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
	initCommittedGitApp(t, root, "main")
	manifest := readClientManifest(t, root)

	addr, err := resolveDeployAddressForManifest(root, "", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if addr.EnvName != "production" || !addr.ProductionBranch || addr.Branch != "main" {
		t.Fatalf("main should resolve to production branch, got %+v", addr)
	}

	runGit(t, root, "checkout", "-B", "feat/x")
	addr, err = resolveDeployAddressForManifest(root, "", manifest)
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
box = "example.com"
production_branch = "stable"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"api.example.com" = "web"
`)
	initCommittedGitApp(t, root, "main")
	manifest := readClientManifest(t, root)

	addr, err := resolveDeployAddressForManifest(root, "", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if addr.EnvName != "main" || addr.ProductionBranch {
		t.Fatalf("main should be a preview when production_branch=stable, got %+v", addr)
	}

	runGit(t, root, "checkout", "-B", "stable")
	addr, err = resolveDeployAddressForManifest(root, "", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if addr.EnvName != "production" || !addr.ProductionBranch {
		t.Fatalf("stable should resolve to production, got %+v", addr)
	}
}

func TestResolveDeployAddressDetachedBranchGate(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
	initCommittedGitApp(t, root, "main")
	manifest := readClientManifest(t, root)

	if _, err := resolveDeployAddressForManifest(root, "feat/x", manifest); !errcat.Is(err, errcat.CodeBranchFlagRequiresDetachedHead) {
		t.Fatalf("expected checked-out --branch rejection, got %v", err)
	}

	runGit(t, root, "checkout", "--detach")
	if _, err := resolveDeployAddressForManifest(root, "", manifest); !errcat.Is(err, errcat.CodeDetachedHeadRequiresBranch) {
		t.Fatalf("expected detached HEAD rejection, got %v", err)
	}

	addr, err := resolveDeployAddressForManifest(root, "feat/x", manifest)
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
	manifest := readClientManifest(t, root)

	_, err := resolveDeployAddressForManifest(root, "", manifest)
	if !errcat.Is(err, errcat.CodeNotAGitRepo) || !strings.Contains(err.Error(), "next:") {
		t.Fatalf("expected not_a_git_repo with next step, got %v", err)
	}
}

func TestWriteShipResultOutputContracts(t *testing.T) {
	result := ShipResult{
		URL:        "https://api.example.com",
		Env:        "production",
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
		AppContext: &config.AppContext{AppName: "api", EnvName: "production", ProductionBranch: "main"},
		Address:    readAddress{EnvName: "production", ProductionBranch: true},
		EnvName:    "production",
	}
	out := rewriteRollbackSummary("Rolled back api (production) from def456 to abc123\n  web          running\n", read)
	if !strings.Contains(out, "Rolled back Production main from def456 to abc123") {
		t.Fatalf("rollback summary leaked internal env:\n%s", out)
	}
	if !strings.Contains(out, "web") {
		t.Fatalf("rollback process lines should be preserved:\n%s", out)
	}
}

func TestDeploymentURLSynthesizesSSLIPWithoutRoutes(t *testing.T) {
	ctx := &config.AppContext{AppName: "api", EnvName: "production", Server: "203.0.113.7"}
	got := deploymentURL(ctx, "production")
	want := "https://api.203-0-113-7.sslip.io"
	if got != want {
		t.Fatalf("fallback URL = %q, want %q", got, want)
	}
}

func TestDeploymentURLPrefersRootWebRoute(t *testing.T) {
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "production",
		Server:  "example.com",
		Routes: map[string]config.Route{
			"api.example.com/docs": {Host: "api.example.com", Path: "/docs", Process: "web"},
			"api.example.com":      {Host: "api.example.com", Process: "web"},
		},
	}
	got := deploymentURL(ctx, "production")
	want := "https://api.example.com"
	if got != want {
		t.Fatalf("routed URL = %q, want %q", got, want)
	}
}

func TestPrepareDeployRoutesSynthesizesSSLIPRouteForRoutelessApp(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "production",
		Server:  "example.com",
		Processes: map[string]config.Process{
			"web": {Port: &port},
		},
	}
	plan, err := prepareDeployRoutes(ctx, "production", deployRouteOptions{BoxIP: "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RewritesManifest || !plan.NoConfiguredDomain {
		t.Fatalf("expected rewritten no-domain plan: %+v", plan)
	}
	route := plan.Context.Routes["api.203-0-113-7.sslip.io"]
	if route.Host != "api.203-0-113-7.sslip.io" || route.Process != "web" {
		t.Fatalf("unexpected synthesized route: %+v", route)
	}
}

func TestPrepareDeployRoutesRejectsMultipleProcessesWithoutWeb(t *testing.T) {
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "production",
		Processes: map[string]config.Process{
			"api":    {},
			"worker": {},
		},
	}
	_, err := prepareDeployRoutes(ctx, "production", deployRouteOptions{BoxIP: "203.0.113.7"})
	if !errcat.Is(err, errcat.CodeMultiProcessNoWebRoute) {
		t.Fatalf("expected multi-process/no-web error, got %v", err)
	}
}

func TestPrepareDeployRoutesCollapsesPreviewToSSLIPHost(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "feat-x-ab12",
		Server:  "example.com",
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
	root := plan.Context.Routes["api-feat-x-ab12.203-0-113-7.sslip.io"]
	docs := plan.Context.Routes["api-feat-x-ab12.203-0-113-7.sslip.io/docs"]
	if root.Host != "api-feat-x-ab12.203-0-113-7.sslip.io" || root.Process != "web" || root.TLS != "internal" {
		t.Fatalf("unexpected preview root route: %+v", root)
	}
	if docs.Host != "api-feat-x-ab12.203-0-113-7.sslip.io" || docs.Path != "/docs" || docs.Serve != "dist" || docs.TLS != "internal" {
		t.Fatalf("unexpected preview docs route: %+v", docs)
	}
}

func TestPrepareDeployRoutesUsesPreviewBaseAndDerivesAlias(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "feat-new-pricing-x7q2",
		Preview: config.Preview{Base: "preview.example.com", Aliases: true},
		Processes: map[string]config.Process{
			"web": {Port: &port},
		},
	}
	plan, err := prepareDeployRoutes(ctx, "feat-new-pricing-x7q2", deployRouteOptions{Preview: true, BoxIP: "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plan.Context.Routes["api-feat-new-pricing-x7q2.preview.example.com"].Host, "api-feat-new-pricing-x7q2.preview.example.com"; got != want {
		t.Fatalf("canonical preview host = %q, want %q", got, want)
	}
	if got, want := plan.PreviewAlias, "feat-new-pricing.preview.example.com"; got != want {
		t.Fatalf("preview alias = %q, want %q", got, want)
	}
	if got, want := deploymentURLForBoxIP(ctx, "feat-new-pricing-x7q2", "203.0.113.7"), "https://api-feat-new-pricing-x7q2.preview.example.com"; got != want {
		t.Fatalf("preview fallback URL = %q, want %q", got, want)
	}
}

func TestPrepareDeployRoutesKeepsProductionOnSSLIPWhenPreviewBaseExists(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		AppName: "api",
		Preview: config.Preview{Base: "preview.example.com", Aliases: true},
		Processes: map[string]config.Process{
			"web": {Port: &port},
		},
	}
	plan, err := prepareDeployRoutes(ctx, "production", deployRouteOptions{BoxIP: "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plan.Context.Routes["api.203-0-113-7.sslip.io"].Host, "api.203-0-113-7.sslip.io"; got != want {
		t.Fatalf("production host = %q, want %q", got, want)
	}
}

func TestWriteDeployManifestOverlaysRoutesAsParseableTOML(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
	dst := filepath.Join(root, "upload.toml")
	routes := map[string]config.Route{
		"api.203-0-113-7.sslip.io": {Host: "api.203-0-113-7.sslip.io", Process: "web", TLS: "internal"},
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
	ctx, err := config.LoadAppContext(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	route := ctx.Routes["api.203-0-113-7.sslip.io"]
	if route.Process != "web" || route.TLS != "" {
		t.Fatalf("overlay route did not round-trip: %+v\n%s", route, string(data))
	}
	if strings.Contains(string(data), "tls") {
		t.Fatalf("deploy manifest overlay must not write manifest tls:\n%s", string(data))
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

	ctx := &config.AppContext{AppName: "api", EnvName: "production", Server: "example.com"}
	runner := &fakeSSHRunner{responses: map[string]string{
		serverAppStatusCommand("api", "production"): `{"app":"api","env":"production","release":{"release":"` + deployed[:12] + `","base_commit":"` + deployed + `","source":"process"},"processes":[]}`,
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

	ctx := &config.AppContext{AppName: "api", EnvName: "production", Server: "example.com"}
	firstDeploy := &fakeSSHRunner{responses: map[string]string{
		serverAppStatusCommand("api", "production"): `{"app":"api","env":"production","processes":[]}`,
	}}
	if err := enforceProductionAncestry(root, firstDeploy, ctx, head); err != nil {
		t.Fatalf("first deploy should skip ancestry check: %v", err)
	}

	ancestor := &fakeSSHRunner{responses: map[string]string{
		serverAppStatusCommand("api", "production"): `{"app":"api","env":"production","release":{"release":"` + first[:12] + `","base_commit":"` + first + `","source":"process"},"processes":[]}`,
	}}
	if err := enforceProductionAncestry(root, ancestor, ctx, head); err != nil {
		t.Fatalf("ancestor deployed commit should pass: %v", err)
	}
}

func TestResolveReadPreviewEnvPropagatesUnknownBranchError(t *testing.T) {
	ctx := &config.AppContext{AppName: "api", EnvName: "production", Server: "example.com"}
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

func TestResolvePreviewEnvUsesRunnablePublicRemediations(t *testing.T) {
	ctx := &config.AppContext{AppName: "api", EnvName: "production", Server: "example.com"}
	readCommand := serverAppPreviewResolveCommand("api", "feat/x")
	deployCommand := serverAppPreviewResolveOrCreateCommand("api", "feat/x")

	t.Run("read resolution", func(t *testing.T) {
		runner := &fakeSSHRunner{failures: map[string]string{readCommand: "host unavailable"}}
		_, err := resolveReadPreviewEnv(runner, ctx, readAddress{PreviewBranch: "feat/x"})
		coded, ok := errcat.As(err)
		if !ok || coded.Remediation() != "ship status" {
			t.Fatalf("read remediation = %q for %v, want ship status", coded.Remediation(), err)
		}
	})

	t.Run("deploy resolution", func(t *testing.T) {
		runner := &fakeSSHRunner{failures: map[string]string{deployCommand: "host unavailable"}}
		_, err := resolveDeployPreviewEnv(runner, ctx, deployAddress{PreviewBranch: "feat/x"})
		coded, ok := errcat.As(err)
		if !ok || coded.Remediation() != "ship" {
			t.Fatalf("deploy remediation = %q for %v, want ship", coded.Remediation(), err)
		}
	})
}

func TestCommandRunnerWithoutEnvKeyPinsShipIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("USER", "runner-user")
	t.Setenv("SHIP_SSH_KEY", "")

	runner, err := NewCommandRunner()
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	assertSSHOptionSequence(t, runner.SshOptions, "-i", filepath.Join(home, ".ssh", "ship"))
	assertSSHOptionSequence(t, runner.SshOptions, "-o", "IdentitiesOnly=yes")
	assertSSHOptionSequence(t, runner.SshOptions, "-o", "UserKnownHostsFile="+filepath.Join(home, ".config", "ship", "known_hosts"))
	assertSSHOptionSequence(t, runner.SshOptions, "-o", "StrictHostKeyChecking=accept-new")
	assertSSHOptionSequence(t, runner.SshOptions, "-o", "HashKnownHosts=no")
	if !strings.Contains(runner.RsyncRemoteShell, filepath.Join(home, ".ssh", "ship")) {
		t.Fatalf("rsync remote shell should pin ship identity, got %q", runner.RsyncRemoteShell)
	}
	if !strings.Contains(runner.RsyncRemoteShell, "UserKnownHostsFile="+filepath.Join(home, ".config", "ship", "known_hosts")) {
		t.Fatalf("rsync remote shell should pin ship known_hosts, got %q", runner.RsyncRemoteShell)
	}
}

func TestCommandRunnerEnvKeyWinsOverShipIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("USER", "runner-user")
	t.Setenv("SHIP_SSH_KEY", generatePrivateKeyForClientTest(t))

	runner, err := NewCommandRunner()
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	assertSSHOptionSequence(t, runner.SshOptions, "-o", "IdentitiesOnly=yes")
	if strings.Contains(strings.Join(runner.SshOptions, " "), filepath.Join(home, ".ssh", "ship")) {
		t.Fatalf("env key should win over ~/.ssh/ship, got %v", runner.SshOptions)
	}
	assertSSHOptionSequence(t, runner.SshOptions, "-o", "UserKnownHostsFile="+filepath.Join(home, ".config", "ship", "known_hosts"))
	assertSSHOptionSequence(t, runner.SshOptions, "-o", "StrictHostKeyChecking=accept-new")
	assertSSHOptionSequence(t, runner.SshOptions, "-o", "HashKnownHosts=no")
	if runner.MemberFingerprint == "" {
		t.Fatal("env key should derive a member fingerprint")
	}
	if got := publicKeyComment(filepath.Join(runner.TempDir, "id.pub")); got != "runner-user" {
		t.Fatalf("env key public half comment = %q, want runner-user", got)
	}
}

func TestSSHHostKeyFailureMapsToErrcat(t *testing.T) {
	err := mapSSHTransportError(
		"203.0.113.7",
		"@@@@@@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@@@@@@\nHost key verification failed.",
		255,
		errors.New("exit status 255"),
	)
	if !errcat.Is(err, errcat.CodeHostKeyChanged) {
		t.Fatalf("error = %v, want %s", err, errcat.CodeHostKeyChanged)
	}
	want := "box host key changed\nSSH host key for 203.0.113.7 is unknown or changed; if the box was rebuilt, re-establish the pin (ship box forget 203.0.113.7 clears it); if not, investigate before trusting this host\nnext: ship box setup <ssh-target>"
	if err.Error() != want {
		t.Fatalf("host key error mismatch\nwant:\n%s\ngot:\n%s", want, err)
	}
}

func TestSSHHostKeyFailureDoesNotScanStdout(t *testing.T) {
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\necho 'the remote program reported an offending key'\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, _, _, err := (&CommandRunner{}).RunSSH("203.0.113.7", "false")
	if errcat.Is(err, errcat.CodeHostKeyChanged) {
		t.Fatalf("stdout text was misclassified as a host-key failure: %v", err)
	}
}

func TestReadBoxVersionPreservesTransportCodedError(t *testing.T) {
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\necho 'Host key verification failed.' >&2\nexit 255\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := readBoxVersion(&CommandRunner{}, "203.0.113.7")
	if !errcat.Is(err, errcat.CodeHostKeyChanged) {
		t.Fatalf("error = %v, want %s", err, errcat.CodeHostKeyChanged)
	}
}

func TestCmdBoxForgetRemovesShipKnownHost(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path, err := knownhosts.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	content := "203.0.113.7 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/\n" +
		"203.0.113.8 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICtppnbbz76teU3iU6BguTmo//WITtYN35e4gSER6UNt\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	out := captureClientStdout(t, func() { CmdBoxForget("203.0.113.7") })
	if out != "forgot box 203.0.113.7 (~/.config/ship/known_hosts)\n" {
		t.Fatalf("forget output = %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "203.0.113.7") || !strings.Contains(string(data), "203.0.113.8") {
		t.Fatalf("known_hosts after forget:\n%s", data)
	}

	out = captureClientStdout(t, func() { CmdBoxForget("203.0.113.7") })
	if out != "box 203.0.113.7 is not pinned (~/.config/ship/known_hosts)\n" {
		t.Fatalf("unknown forget output = %q", out)
	}
}

func TestCommandRunnerInjectsMemberFingerprintAfterServerNamespace(t *testing.T) {
	runner := &CommandRunner{MemberFingerprint: "SHA256:abc+/123"}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "app",
			in:   "sudo -n /usr/local/bin/ship server app apply api production",
			want: "sudo -n /usr/local/bin/ship server app --member-fingerprint SHA256:abc+/123 apply api production",
		},
		{
			name: "doctor",
			in:   "sudo -n /usr/local/bin/ship server doctor --json",
			want: "sudo -n /usr/local/bin/ship server doctor --member-fingerprint SHA256:abc+/123 --json",
		},
		{
			name: "approval",
			in:   "sudo -n /usr/local/bin/ship server approval ls --json",
			want: "sudo -n /usr/local/bin/ship server approval --member-fingerprint SHA256:abc+/123 ls --json",
		},
		{
			name: "env prefix",
			in:   "SHIP_ERROR_JSON=1 sudo -n /usr/local/bin/ship server app secret set api production KEY",
			want: "SHIP_ERROR_JSON=1 sudo -n /usr/local/bin/ship server app --member-fingerprint SHA256:abc+/123 secret set api production KEY",
		},
		{
			name: "non server",
			in:   "true",
			want: "true",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runner.withMemberFingerprint(tt.in); got != tt.want {
				t.Fatalf("withMemberFingerprint:\nwant: %s\n got: %s", tt.want, got)
			}
		})
	}
}

func generatePrivateKeyForClientTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "id")
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "runner-user", "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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

func TestBuildLocalDeployPlanRejectsTrackedDotenv(t *testing.T) {
	root := t.TempDir()
	writeClientDockerfile(t, root)
	writeClientManifest(t, root, clientContainerManifest())
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
	if errors := diags.errorMessages(); len(errors) != 1 || !strings.Contains(errors[0], ".env") {
		t.Fatalf("tracked dotenv must reject the deploy artifact, got %+v", diags)
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

func TestDeployDiagnosticsDockerfileMissingNamesBothPaths(t *testing.T) {
	err := deployDiagnosticsError(diagnostics{{Kind: diagnosticKindDockerfileMissing, Level: diagnosticError}})
	if !errcat.Is(err, errcat.CodeDockerfileMissing) {
		t.Fatalf("error = %v, want %s", err, errcat.CodeDockerfileMissing)
	}
	for _, want := range []string{
		"Dockerfile is missing",
		"the declared processes need a Dockerfile to build",
		"next: write a Dockerfile, or declare a [routes] static route in ship.toml",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%s", want, err)
		}
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

func captureClientStderr(t *testing.T, fn func()) string {
	t.Helper()
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = oldStderr
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
	got := serverAppApplyCommand("api", "production", "/tmp/ship-deploy/x.tar", "/tmp/ship-deploy/x.toml", plan, actor, false, "internal", "")
	want := "sudo -n /usr/local/bin/ship server app apply --tls internal --tarball /tmp/ship-deploy/x.tar --manifest /tmp/ship-deploy/x.toml --sha abc1234 --base-commit abc1234abc1234abc1234abc1234abc1234abc1234 --created-at 2026-05-30T14:30:12Z --ssh-key-comment fake-vps-smoke --git-author 'Smoke <smoke@example.com>' api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppApplyCommandSupportsRebuild(t *testing.T) {
	plan := testLocalDeployPlan("abc1234", true)
	actor := testDeployIdentity()
	got := serverAppApplyCommand("api", "production", "/tmp/ship-deploy/x.tar", "/tmp/ship-deploy/x.toml", plan, actor, true, "", "")
	want := "sudo -n /usr/local/bin/ship server app apply --rebuild --dirty --tarball /tmp/ship-deploy/x.tar --manifest /tmp/ship-deploy/x.toml --sha abc1234 --base-commit abc1234abc1234abc1234abc1234abc1234abc1234 --created-at 2026-05-30T14:30:12Z --ssh-key-comment fake-vps-smoke --git-author 'Smoke <smoke@example.com>' api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppApplyCommandPassesPreviewAlias(t *testing.T) {
	plan := testLocalDeployPlan("abc1234", false)
	got := serverAppApplyCommand("api", "feat-x-ab12", "/tmp/ship-deploy/x.tar", "/tmp/ship-deploy/x.toml", plan, testDeployIdentity(), false, "", "feat-x.preview.example.com")
	if !strings.Contains(got, "--preview-alias feat-x.preview.example.com") {
		t.Fatalf("apply command did not include preview alias: %s", got)
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
		{name: "doctor text", command: serverDoctorCommand("example.com", false)},
		{name: "doctor json", command: serverDoctorCommand("example.com", true)},
		{name: "setup env", command: serverAppSetupEnvCommand("api", "production")},
		{name: "preflight json", command: serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL"})},
		{name: "apply", command: serverAppApplyCommand("api", "production", "/tmp/ship-deploy/x.tar", "/tmp/ship-deploy/x.toml", plan, actor, true, "auto", "")},
		{name: "status json", command: serverAppStatusCommand("api", "production")},
		{name: "ls text", command: serverAppLsCommand(false)},
		{name: "ls json", command: serverAppLsCommand(true)},
		{name: "logs", command: serverAppLogsCommand("api", "production", "web", false, intPtr(50))},
		{name: "logs follow", command: serverAppLogsCommand("api", "production", "", true, intPtr(0))},
		{name: "exec pipes", command: serverAppExecCommand("api", "production", false, []string{"sh", "-c", "exit 7"})},
		{name: "exec tty", command: serverAppExecCommand("api", "production", true, []string{"env"})},
		{name: "rollback latest", command: serverAppRollbackCommand("api", "production", "", actor)},
		{name: "rollback release", command: serverAppRollbackCommand("api", "production", "abc1234", actor)},
		{name: "data save", command: serverAppDataSaveCommand("api", "production")},
		{name: "data restore", command: serverAppDataRestoreCommand("api", "production", "/tmp/ship-deploy/snapshot.data.tar.gz")},
		{name: "destroy app", command: serverAppDestroyCommand("api")},
		{name: "destroy env purge", command: serverAppDestroyEnvCommand("api", "production")},
		{name: "preview resolve or create", command: serverAppPreviewResolveOrCreateCommand("api", "feat/x")},
		{name: "preview resolve", command: serverAppPreviewResolveCommand("api", "feat/x")},
		{name: "preview pin", command: serverAppPreviewPinCommand("api", "feat/x")},
		{name: "preview unpin", command: serverAppPreviewUnpinCommand("api", "feat/x")},
		{name: "preview share", command: serverAppPreviewShareCommand("api", "feat-x-abcd", false)},
		{name: "preview share rotate", command: serverAppPreviewShareCommand("api", "feat-x-abcd", true)},
		{name: "data fork", command: serverAppDataForkCommand("api", "feat-x-abcd")},
		{name: "data reset", command: serverAppDataResetCommand("api", "feat-x-abcd")},
		{name: "secret set", command: serverAppSecretSetCommand("api", "production", "DATABASE_URL")},
		{name: "secret list", command: serverAppSecretListCommand("api", "production", false)},
		{name: "secret list json", command: serverAppSecretListCommand("api", "production", true)},
		{name: "secret rm", command: serverAppSecretRmCommand("api", "production", "DATABASE_URL")},
		{name: "why", command: serverAppWhyCommand("api", "production")},
		{name: "key add", command: serverKeyAddCommand("alice", "shipper")},
		{name: "key list text", command: serverKeyListCommand(false)},
		{name: "key list json", command: serverKeyListCommand(true)},
		{name: "key rm", command: serverKeyRmCommand("alice")},
		{name: "approval ls text", command: serverApprovalLsCommand(false)},
		{name: "approval ls json", command: serverApprovalLsCommand(true)},
		{name: "approval grant", command: serverApprovalGrantCommand("abc123xy")},
		{name: "box webhook get", command: serverBoxWebhookGetCommand()},
		{name: "box webhook set", command: serverBoxWebhookSetCommand("https://ntfy.example/ship")},
		{name: "box webhook clear", command: serverBoxWebhookClearCommand()},
		{name: "box config get", command: serverBoxConfigGetCommand()},
		{name: "box config set", command: serverBoxConfigSetCommand("webhook.url", "https://ntfy.example/ship")},
		{name: "box config unset", command: serverBoxConfigUnsetCommand("webhook.url")},
	}

	for _, tt := range commands {
		t.Run(tt.name, func(t *testing.T) {
			assertServerCommandCoveredBySudoers(t, tt.command)
		})
	}
}

func TestServerAppLogsCommandTail(t *testing.T) {
	tests := []struct {
		name   string
		follow bool
		tail   *int
		want   string
	}{
		{
			name: "unset omits tail flag",
			want: "sudo -n /usr/local/bin/ship server app logs api production",
		},
		{
			name:   "zero includes tail flag",
			follow: true,
			tail:   intPtr(0),
			want:   "sudo -n /usr/local/bin/ship server app logs --follow --tail=0 api production",
		},
		{
			name: "positive includes tail flag",
			tail: intPtr(50),
			want: "sudo -n /usr/local/bin/ship server app logs --tail=50 api production",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateLogsTail(tt.tail); err != nil {
				t.Fatalf("ValidateLogsTail() error = %v", err)
			}
			got := serverAppLogsCommand("api", "production", "", tt.follow, tt.tail)
			if got != tt.want {
				t.Fatalf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateLogsTailRejectsNegative(t *testing.T) {
	err := ValidateLogsTail(intPtr(-1))
	if !errcat.Is(err, errcat.CodeUsageError) {
		t.Fatalf("ValidateLogsTail(-1) error = %v, want usage error", err)
	}
	if got, want := err.Error(), "command usage failed\n--tail must be zero or greater\nnext: ship logs --tail 0"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func intPtr(v int) *int {
	return &v
}

func TestDeploySSHTargetAddsConstantDeployUser(t *testing.T) {
	if got := deploySSHTarget("203.0.113.7"); got != "deploy@203.0.113.7" {
		t.Fatalf("deploy ssh target = %q", got)
	}
	if got := deploySSHTarget("root@203.0.113.7"); got != "root@203.0.113.7" {
		t.Fatalf("explicit ssh target should not be rewritten, got %q", got)
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
		subcommand == "doctor" ||
		strings.HasPrefix(subcommand, "doctor ") ||
		strings.HasPrefix(subcommand, "key ") ||
		strings.HasPrefix(subcommand, "approval ") ||
		strings.HasPrefix(subcommand, "config ") ||
		strings.HasPrefix(subcommand, "webhook ")
}

func TestServerAppSetupEnvCommand(t *testing.T) {
	got := serverAppSetupEnvCommand("api", "production")
	want := "sudo -n /usr/local/bin/ship server app setup-env api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppPreflightJSONCommandIncludesRequiredSecrets(t *testing.T) {
	got := serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL", "API_KEY"})
	want := "sudo -n /usr/local/bin/ship server app preflight --json --secret DATABASE_URL --secret API_KEY api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppLsCommandSupportsJSON(t *testing.T) {
	got := serverAppLsCommand(false)
	want := "sudo -n /usr/local/bin/ship server app ls"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppLsCommand(true)
	want = "sudo -n /usr/local/bin/ship server app ls --json"
	if got != want {
		t.Fatalf("unexpected json command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerBoxStatusCommandUsesCompactVersionSummary(t *testing.T) {
	got := serverBoxStatusCommand()
	want := "sudo -n /usr/local/bin/ship server version --json --summary"
	if got != want {
		t.Fatalf("unexpected box status command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppWhyCommand(t *testing.T) {
	// The helper always emits journal JSON; the command carries no flag.
	got := serverAppWhyCommand("api", "production")
	want := "sudo -n /usr/local/bin/ship server app why api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}
}

func TestServerAppExecCommand(t *testing.T) {
	got := serverAppExecCommand("api", "production", false, []string{"sh", "-c", "exit 7"})
	want := "sudo -n /usr/local/bin/ship server app exec api production -- sh -c 'exit 7'"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
	}

	got = serverAppExecCommand("api", "production", true, []string{"env"})
	want = "sudo -n /usr/local/bin/ship server app exec --tty api production -- env"
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
		Outcome:          "failed",
		EndedAt:          "2026-07-07T10:00:01Z",
		PreviousRelease:  "aaa111",
		AttemptedRelease: "bbb222",
		FailingStep:      "release",
		StderrTail:       "fake release command failed",
		Identity:         testDeployIdentity(),
	}
	got := renderWhy(entry, read)
	want := "Deploy failed for Production main at 2026-07-07T10:00:01Z.\n" +
		"attempted release: bbb222\n" +
		"previous release: aaa111\n" +
		"failing step: release\n" +
		"probable cause: release command exited non-zero before traffic switched.\n" +
		"stderr tail:\n" +
		"fake release command failed\n" +
		"traffic: old release aaa111 kept serving; no traffic was switched.\n" +
		"shipped by: Smoke <smoke@example.com> (ssh key: fake-vps-smoke)\n" +
		"next: fix the release command in ship.toml, then ship\n"
	if got != want {
		t.Fatalf("unexpected why output:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestRenderWhyConvergedOutcome(t *testing.T) {
	read := readContext{
		AppContext: &config.AppContext{ProductionBranch: "main"},
		Address:    readAddress{ProductionBranch: true},
	}
	got := renderWhy(whyJournalEntry{
		Outcome:          "converged",
		EndedAt:          "2026-07-07T10:00:01Z",
		AttemptedRelease: "bbb222",
	}, read)
	for _, want := range []string{"Convergence completed for Production main", "release: bbb222", "traffic: release bbb222 is live", "next: ship status"} {
		if !strings.Contains(got, want) {
			t.Fatalf("why output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Deploy failed") {
		t.Fatalf("converged outcome fell through to failure rendering:\n%s", got)
	}
}

func TestRenderWhyCommittedDegradedPrescribesConverge(t *testing.T) {
	read := readContext{
		AppContext: &config.AppContext{ProductionBranch: "main"},
		Address:    readAddress{ProductionBranch: true},
	}
	got := renderWhy(whyJournalEntry{
		Outcome:          "committed_degraded",
		EndedAt:          "2026-07-07T10:00:01Z",
		AttemptedRelease: "bbb222",
	}, read)
	for _, want := range []string{
		"Deploy committed but degraded",
		"next: ship converge\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("why output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "next: ship\n") {
		t.Fatalf("degraded output fell through to generic remediation:\n%s", got)
	}
}

func TestRenderWhyApplyFailureDoesNotPrescribeReleaseCommand(t *testing.T) {
	read := readContext{
		AppContext: &config.AppContext{ProductionBranch: "main"},
		Address:    readAddress{ProductionBranch: true},
	}
	entry := whyJournalEntry{
		Outcome:          "failed",
		EndedAt:          "2026-07-07T10:00:01Z",
		PreviousRelease:  "aaa111",
		AttemptedRelease: "bbb222",
		FailingStep:      "apply",
		StderrTail:       "source tar is corrupt",
		Identity:         testDeployIdentity(),
	}
	got := renderWhy(entry, read)
	for _, want := range []string{"failing step: apply\n", "probable cause: deploy failed before traffic switched.\n", "source tar is corrupt\n", "next: ship\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("why output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fix the release command") {
		t.Fatalf("apply failure prescribed release command:\n%s", got)
	}
}

func TestRenderWhyProbeFailure(t *testing.T) {
	read := readContext{
		AppContext: &config.AppContext{ProductionBranch: "main"},
		Address:    readAddress{ProductionBranch: true},
	}
	entry := whyJournalEntry{
		Outcome:          "failed",
		EndedAt:          "2026-07-07T10:02:01Z",
		PreviousRelease:  "aaa111",
		AttemptedRelease: "ccc333",
		FailingStep:      "probe",
		StderrTail:       "HTTP status 502: upstream web listens on 3000, probed 3999",
		Identity:         testDeployIdentity(),
		Probe:            &whyJournalProbe{Status: 502, BodySnippet: "upstream web listens on 3000, probed 3999"},
	}
	got := renderWhy(entry, read)
	want := "Deploy failed for Production main at 2026-07-07T10:02:01Z.\n" +
		"attempted release: ccc333\n" +
		"previous release: aaa111\n" +
		"failing step: probe\n" +
		"probable cause: probe returned HTTP 502 with body: upstream web listens on 3000, probed 3999\n" +
		"stderr tail:\n" +
		"HTTP status 502: upstream web listens on 3000, probed 3999\n" +
		"traffic: old release aaa111 kept serving; failed probes never receive traffic with the current engine.\n" +
		"shipped by: Smoke <smoke@example.com> (ssh key: fake-vps-smoke)\n" +
		"next: fix the process port or probe path in ship.toml, then ship\n"
	if got != want {
		t.Fatalf("unexpected why output:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestFailDeployAfterRemoteDirPreservesSingleCodedErrorShape(t *testing.T) {
	cleaned := false
	_, err := failDeployAfterRemoteDir(func() { cleaned = true }, errcat.New(errcat.CodeProbeFailed, errcat.Fields{
		"detail": "health check failed for web",
	}))
	if !cleaned {
		t.Fatal("cleanup was not called")
	}
	if !errcat.Is(err, errcat.CodeProbeFailed) {
		t.Fatalf("expected probe_failed to survive cleanup, got %v", err)
	}
	text := err.Error()
	if count := strings.Count(text, "next:"); count != 1 {
		t.Fatalf("expected exactly one next line, got %d:\n%s", count, text)
	}
	if strings.Contains(text, "operation failed\noperation failed") {
		t.Fatalf("coded deploy error was double-wrapped:\n%s", text)
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
	raw := `{"apps":[{"app":"api","envs":[{"class":"production","branch":"main","url":"https://api.example.com","env":"production","current_release":"abc1234","health":"healthy","age_seconds":60,"expires_at":"","pinned":false,"dirty":false,"shipped_by":{"ssh_key_comment":"fake-vps-smoke","git_author":"Smoke <smoke@example.com>"},"processes":[{"process":"web","container":"api-web","state":"running","release":"abc1234","created_at":"2026-07-07T10:00:00Z"}]}]}]}`
	payload, err := statusFromAppList(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Envs) != 1 || payload.Envs[0].ShippedBy == nil {
		t.Fatalf("status missing shipped_by: %+v", payload)
	}
	if payload.Envs[0].Class != "production" {
		t.Fatalf("status class = %q, want production", payload.Envs[0].Class)
	}
	if payload.Envs[0].ShippedBy.GitAuthor != "Smoke <smoke@example.com>" || payload.Envs[0].ShippedBy.SSHKeyComment != "fake-vps-smoke" {
		t.Fatalf("wrong shipped_by: %+v", payload.Envs[0].ShippedBy)
	}
	text := renderStatusSummary(payload)
	if !strings.Contains(text, `shipped_by="Smoke <smoke@example.com>"`) || !strings.Contains(text, `ssh_key="fake-vps-smoke"`) {
		t.Fatalf("human status missing attribution:\n%s", text)
	}
}

func TestStatusFromAppListEmptyAppsUsesEmptyEnvsArray(t *testing.T) {
	payload, err := statusFromAppList(&config.AppContext{AppName: "api"}, `{"apps":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Envs == nil {
		t.Fatal("status envs should be a non-nil empty slice")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"app":"api","envs":[]}` {
		t.Fatalf("empty status JSON = %s", data)
	}
}

func TestSplitLogLinesEmptyIsEmptyArray(t *testing.T) {
	lines := splitLogLines("")
	if lines == nil || len(lines) != 0 {
		t.Fatalf("empty logs should be a non-nil empty slice, got %#v", lines)
	}
	payload := struct {
		Lines []string `json:"lines"`
	}{Lines: lines}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"lines":[]}` {
		t.Fatalf("empty logs JSON = %s, want lines: []", data)
	}
}

func TestServerAppDataSnapshotCommands(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "save",
			got:  serverAppDataSaveCommand("api", "production"),
			want: "sudo -n /usr/local/bin/ship server app data save api production",
		},
		{
			name: "restore",
			got:  serverAppDataRestoreCommand("api", "production", "/tmp/ship-deploy/snapshot.data.tar.gz"),
			want: "sudo -n /usr/local/bin/ship server app data restore --archive /tmp/ship-deploy/snapshot.data.tar.gz api production",
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
	got := serverAppDestroyEnvCommand("api", "production")
	want := "sudo -n /usr/local/bin/ship server app destroy-env --purge api production"
	if got != want {
		t.Fatalf("unexpected command:\nwant: %s\n got: %s", want, got)
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
		{
			name: "preview share",
			got:  serverAppPreviewShareCommand("api", "feat-x-abcd", false),
			want: "sudo -n /usr/local/bin/ship server app preview share api feat-x-abcd",
		},
		{
			name: "preview share rotate",
			got:  serverAppPreviewShareCommand("api", "feat-x-abcd", true),
			want: "sudo -n /usr/local/bin/ship server app preview share --rotate api feat-x-abcd",
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

func TestServerAppDataCommands(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "fork",
			got:  serverAppDataForkCommand("api", "feat-x-abcd"),
			want: "sudo -n /usr/local/bin/ship server app data fork api feat-x-abcd",
		},
		{
			name: "reset",
			got:  serverAppDataResetCommand("api", "feat-x-abcd"),
			want: "sudo -n /usr/local/bin/ship server app data reset api feat-x-abcd",
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

func TestRenderDataForkSummaryIncludesStableNotes(t *testing.T) {
	got := renderDataForkSummary("feature/data", dataForkSummary{
		Files: []dataForkFile{
			{Path: "uploads/avatar.txt", Size: 11},
			{Path: "app.db", Size: 8192, SQLite: true},
		},
		SQLiteFiles: 1,
	})
	for _, want := range []string{
		"Forked data for Preview feature/data\n",
		"  app.db 8192 bytes (sqlite)\n",
		"  uploads/avatar.txt 11 bytes\n",
		DataForkPIINote + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, DataForkNoSQLiteNote) {
		t.Fatalf("SQLite summary should not include no-SQLite note:\n%s", got)
	}

	noSQLite := renderDataForkSummary("feature/uploads", dataForkSummary{
		Files: []dataForkFile{{Path: "uploads/avatar.txt", Size: 1}},
	})
	if !strings.Contains(noSQLite, DataForkNoSQLiteNote+"\n") {
		t.Fatalf("no-SQLite summary missing note:\n%s", noSQLite)
	}
}

func TestPreviewShareRotateKeepsCapabilityWhenURLLookupFails(t *testing.T) {
	runner := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
		serverAppPreviewShareCommand("api", "preview", true): {{stdout: "new-capability\n"}},
		serverAppLsCommand(true):                             {{stderr: "status lookup failed", code: 1}},
	}}
	result, err := runPreviewShare(previewShareContext{
		AppContext: &config.AppContext{AppName: "api", Server: "example.com"},
		EnvName:    "preview",
		Runner:     runner,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Capability != "new-capability" || result.URLLookupErr == nil {
		t.Fatalf("result = %+v, want new capability and URL lookup failure", result)
	}
	stdout, stderr, err := renderPreviewShareOutput(result, true)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "new-capability\n" {
		t.Fatalf("stdout = %q, want capability", stdout)
	}
	if !strings.Contains(stderr, "warning: preview URL lookup failed: ") || !strings.Contains(stderr, "next: ship status\n") {
		t.Fatalf("stderr = %q", stderr)
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
			got:  serverDoctorCommand("example.com", false),
			want: "sudo -n /usr/local/bin/ship server doctor --box-target example.com",
		},
		{
			name: "doctor json",
			got:  serverDoctorCommand("example.com", true),
			want: "sudo -n /usr/local/bin/ship server doctor --box-target example.com --json",
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

func TestServerApprovalCommands(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "list text",
			got:  serverApprovalLsCommand(false),
			want: "sudo -n /usr/local/bin/ship server approval ls",
		},
		{
			name: "list json",
			got:  serverApprovalLsCommand(true),
			want: "sudo -n /usr/local/bin/ship server approval ls --json",
		},
		{
			name: "grant",
			got:  serverApprovalGrantCommand("abc123xy"),
			want: "sudo -n /usr/local/bin/ship server approval grant abc123xy",
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

func TestRunBoxWebhookJSON(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response string
		want     string
	}{
		{
			name:     "set URL",
			response: `{"config":{"webhook.url":{"value":"https://ntfy.example/ship","default":"","source":"set"}}}`,
			want:     "{\"url\":\"https://ntfy.example/ship\"}\n",
		},
		{
			name:     "unset URL",
			response: `{"config":{"webhook.url":{"value":"","default":"","source":"default"}}}`,
			want:     "{\"url\":\"\"}\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeSSHRunner{responses: map[string]string{serverBoxConfigGetCommand(): tt.response}}
			got, err := runBoxWebhook(runner, "example.com", "", false, true)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("JSON output = %q, want %q", got, tt.want)
			}
			if strings.Join(runner.commands, "\n") != serverBoxConfigGetCommand() {
				t.Fatalf("commands = %v, want box config get", runner.commands)
			}
		})
	}
}

func TestRunBoxWebhookRejectsJSONMutations(t *testing.T) {
	for _, tt := range []struct {
		name   string
		url    string
		remove bool
	}{
		{name: "set URL", url: "https://ntfy.example/ship"},
		{name: "remove", remove: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runBoxWebhook(&fakeSSHRunner{}, "example.com", tt.url, tt.remove, true)
			if !errcat.Is(err, errcat.CodeUsageError) {
				t.Fatalf("error = %v, want usage error", err)
			}
			for _, want := range []string{"--json is only valid when reading box webhook", "ship box webhook <box> --json"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error missing %q:\n%v", want, err)
				}
			}
		})
	}
}

func TestSuccessfulRemoteStderrIsForwarded(t *testing.T) {
	stderr := captureClientStderr(t, func() {
		runner := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
			"detail":   {{stdout: "detail output", stderr: "warning: detail\n"}},
			"required": {{stdout: "required output", stderr: "warning: required\n"}},
		}}
		if _, err := runSSHDetail(runner, "example.com", "detail", "ship detail"); err != nil {
			t.Fatal(err)
		}
		if _, err := runSSHRequired(runner, "example.com", "required", "required failed", "ship status"); err != nil {
			t.Fatal(err)
		}

		configRunner := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
			serverBoxConfigSetCommand("webhook.url", "https://ntfy.example/ship"): {{stdout: "configured\n", stderr: "warning: config journal\n"}},
		}}
		if _, err := runBoxConfigMutation(configRunner, "example.com", serverBoxConfigSetCommand("webhook.url", "https://ntfy.example/ship"), "ship box config example.com set webhook.url"); err != nil {
			t.Fatal(err)
		}

		webhookRunner := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
			serverBoxWebhookSetCommand("https://ntfy.example/ship"): {{stdout: "configured\n", stderr: "warning: webhook journal\n"}},
		}}
		if _, err := runBoxWebhook(webhookRunner, "example.com", "https://ntfy.example/ship", false, false); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"warning: detail\n", "warning: required\n", "warning: config journal\n", "warning: webhook journal\n"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
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

func TestDeployHostPreflight(t *testing.T) {
	ctx := &config.AppContext{Server: "example.com"}
	agentShellRefusal := errcat.WithCause(
		errcat.New(errcat.CodeOperationFailed, errcat.Fields{"detail": "shell command rejected"}),
		"agent_shell_refused: agent keys use the server API",
	).(*errcat.Error).JSONLine()
	transport := errcat.New(errcat.CodeHostKeyChanged, errcat.Fields{"box": "example.com"})

	for _, tt := range []struct {
		name     string
		result   fakeSSHResult
		wantCode errcat.Code
		wantNil  bool
	}{
		{name: "transport coded error is unchanged", result: fakeSSHResult{err: transport}, wantCode: errcat.CodeHostKeyChanged},
		{name: "agent shell refusal proceeds through server API", result: fakeSSHResult{stdout: agentShellRefusal, code: 1}, wantNil: true},
		{name: "missing ship marker", result: fakeSSHResult{stdout: "login banner\nship_preflight:no_ship\n", code: 1}, wantCode: errcat.CodeBoxNotInitialized},
		{name: "missing rsync marker", result: fakeSSHResult{stdout: "ship_preflight:no_rsync", code: 1}, wantCode: errcat.CodeBoxMissingTool},
		{name: "marker-like banner is not a marker", result: fakeSSHResult{stdout: "notice: ship_preflight:no_ship is reserved", code: 1}, wantCode: errcat.CodeSSHUnreachable},
		{name: "unmarked failure is unreachable", result: fakeSSHResult{stderr: "remote command failed", code: 1}, wantCode: errcat.CodeSSHUnreachable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
				deployHostPreflightCommand: {tt.result},
			}}

			err := deployHostPreflight(runner, ctx)
			if tt.wantNil {
				if err != nil {
					t.Fatalf("deployHostPreflight() error = %v, want nil", err)
				}
			} else {
				coded, ok := errcat.As(err)
				if !ok || coded.Code() != tt.wantCode {
					t.Fatalf("deployHostPreflight() error = %v, want %s", err, tt.wantCode)
				}
				if tt.wantCode == errcat.CodeHostKeyChanged && err != transport {
					t.Fatalf("transport coded error was replaced: got %v, want %v", err, transport)
				}
			}
			assertCommandCount(t, runner.commands, deployHostPreflightCommand, 1)
		})
	}

	t.Run("healthy box uses one compound probe", func(t *testing.T) {
		runner := &fakeSSHRunner{responses: map[string]string{deployHostPreflightCommand: ""}}
		if err := deployHostPreflight(runner, ctx); err != nil {
			t.Fatal(err)
		}
		if len(runner.commands) != 1 || runner.commands[0] != deployHostPreflightCommand {
			t.Fatalf("commands = %#v, want exactly [%q]", runner.commands, deployHostPreflightCommand)
		}
	})
}

func TestEnsureRemoteEnvReadyPreparesMissingEnv(t *testing.T) {
	ctx := &config.AppContext{
		AppName: "api",
		EnvName: "production",
		Server:  "example.com",
	}
	preflightCmd := serverAppPreflightJSONCommand("api", "production", nil)
	runner := &fakeSSHRunner{
		responses: map[string]string{
			deployHostPreflightCommand:                    "",
			serverAppSetupEnvCommand("api", "production"): "App api (production) is ready",
		},
		sequences: map[string][]fakeSSHResult{
			preflightCmd: {
				{stdout: `{"app":"api","env":"production","healthy":false,"issues":[{"code":"env_missing","message":"app env is not prepared: missing /var/apps/api.production"}]}`, code: 1},
				{stdout: `{"app":"api","env":"production","healthy":true,"issues":[]}`, code: 0},
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
	assertCommandCount(t, runner.commands, deployHostPreflightCommand, 1)
}

func assertCommandCount(t *testing.T, commands []string, command string, want int) {
	t.Helper()
	got := 0
	for _, item := range commands {
		if item == command {
			got++
		}
	}
	if got != want {
		t.Fatalf("command %q ran %d times, want %d\ncommands:\n%s", command, got, want, strings.Join(commands, "\n"))
	}
}

func TestEnsureRemoteEnvReadyDoesNotPrepareWhenSecretsAreMissing(t *testing.T) {
	ctx := &config.AppContext{
		AppName:    "api",
		EnvName:    "production",
		Server:     "example.com",
		SecretRefs: map[string]string{"DATABASE_URL": "DATABASE_URL"},
	}
	preflightCmd := serverAppPreflightJSONCommand("api", "production", []string{"DATABASE_URL"})
	runner := &fakeSSHRunner{
		responses: map[string]string{
			deployHostPreflightCommand: "",
		},
		failures: map[string]string{
			preflightCmd: `{"app":"api","env":"production","healthy":false,"issues":[{"code":"env_missing","message":"app env is not prepared: missing /var/apps/api.production"},{"code":"secret_missing","message":"missing secret DATABASE_URL; run ` + "`" + `ship secret set DATABASE_URL` + "`" + `"}]}`,
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
		Server:  "example.com",
	}
	preflightCmd := serverAppPreflightJSONCommand("api", "production", nil)
	runner := &fakeSSHRunner{
		responses: map[string]string{
			deployHostPreflightCommand:                    "",
			serverAppSetupEnvCommand("api", "production"): "App api (production) is ready",
		},
		sequences: map[string][]fakeSSHResult{
			preflightCmd: {
				{stdout: `{"app":"api","env":"production","healthy":false,"issues":[{"code":"env_missing","message":"app env is not prepared: missing /var/apps/api.production"}]}`, code: 1},
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

func TestEnsureRemoteEnvReadyPreservesCodedSecondPreflightFailure(t *testing.T) {
	ctx := &config.AppContext{AppName: "api", EnvName: "production", Server: "example.com"}
	preflightCmd := serverAppPreflightJSONCommand("api", "production", nil)
	transport := errcat.New(errcat.CodeHostKeyChanged, errcat.Fields{"box": "example.com"})
	runner := &fakeSSHRunner{
		responses: map[string]string{
			deployHostPreflightCommand:                    "",
			serverAppSetupEnvCommand("api", "production"): "App api (production) is ready",
		},
		sequences: map[string][]fakeSSHResult{
			preflightCmd: {
				{stdout: `{"app":"api","env":"production","healthy":false,"issues":[{"code":"env_missing","message":"environment missing"}]}`, code: 1},
				{err: transport},
			},
		},
	}

	err := ensureRemoteEnvReadyForDeploy(runner, ctx)
	if err != transport {
		t.Fatalf("second coded preflight error = %v, want original %v", err, transport)
	}
}

func TestFetchRemotePreflightReportRejectsWrongTargetAndCodedErrors(t *testing.T) {
	ctx := &config.AppContext{AppName: "api", EnvName: "production", Server: "example.com"}
	command := serverAppPreflightJSONCommand("api", "production", nil)

	t.Run("wrong app env", func(t *testing.T) {
		runner := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
			command: {{stdout: `{"app":"other","env":"preview","healthy":true,"issues":[]}`}},
		}}
		_, err := fetchRemotePreflightReport(runner, ctx)
		if !errcat.Is(err, errcat.CodeRemotePreflightFailed) || !strings.Contains(err.Error(), "wrong app/env") {
			t.Fatalf("error = %v, want remote_preflight_failed for wrong app/env", err)
		}
	})

	t.Run("transport coded error wins over parseable report", func(t *testing.T) {
		transport := errcat.New(errcat.CodeHostKeyChanged, errcat.Fields{"box": "example.com"})
		runner := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
			command: {{stdout: `{"app":"api","env":"production","healthy":true,"issues":[]}`, err: transport}},
		}}
		_, err := fetchRemotePreflightReport(runner, ctx)
		if err != transport {
			t.Fatalf("error = %v, want original transport error %v", err, transport)
		}
	})
}

func TestReadBoxVersionMapsPreUpdateBoxesToSetupRequired(t *testing.T) {
	version := serverVersionCommand(true)

	sudoDenied := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
		version: {{stderr: "sudo: a password is required", code: 1}},
	}}
	_, err := readBoxVersion(sudoDenied, "203.0.113.7")
	coded, ok := errcat.As(err)
	if !ok || coded.Code() != errcat.CodeBoxSetupRequired {
		t.Fatalf("sudo denial should map to box_setup_required, got %v", err)
	}
	if !strings.Contains(coded.Remediation(), "ship box setup 203.0.113.7") {
		t.Fatalf("remediation should name box setup, got %q", coded.Remediation())
	}
	if coded.Message() != "box is not set up for ship" {
		t.Fatalf("message = %q", coded.Message())
	}
	if coded.Cause() != "the ship helper (or its sudo rules) is missing or stale on this box" {
		t.Fatalf("cause = %q", coded.Cause())
	}

	usageError := &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
		version: {{stdout: `{"error":{"code":"usage_error","message":"command usage failed","cause":"unexpected argument version","remediation":"ship help"}}`, code: 2}},
	}}
	_, err = readBoxVersion(usageError, "203.0.113.7")
	coded, ok = errcat.As(err)
	if !ok || coded.Code() != errcat.CodeUsageError {
		t.Fatalf("a remote usage error should surface as usage_error, got %v", err)
	}

	healthy := &fakeSSHRunner{responses: map[string]string{
		version: `{"version":"v0.4.0","architecture":"x86_64"}`,
	}}
	payload, err := readBoxVersion(healthy, "203.0.113.7")
	if err != nil || payload.Version != "v0.4.0" {
		t.Fatalf("healthy probe failed: payload=%+v err=%v", payload, err)
	}
}

func TestReadBoxStatusDecodesFullVersionSummary(t *testing.T) {
	command := serverBoxStatusCommand()
	runner := &fakeSSHRunner{responses: map[string]string{
		command: `{"version":"v0.4.1","ship_version":"v0.4.0","disk":{"status":"ok","evidence":"/: used=10.0%"},"apps":[{"app":"api","env_count":2}],"members":{"total":3,"owners":1},"pending_approvals":1,"doctor":{"status":"degraded","recorded_at":"2026-07-14T08:00:00Z"}}`,
	}}

	payload, err := readBoxStatus(runner, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if payload.Disk.Evidence != "/: used=10.0%" || len(payload.Apps) != 1 || payload.Apps[0] != (boxStatusApp{App: "api", EnvCount: 2}) || payload.Members == nil || *payload.Members != (boxStatusMembers{Total: 3, Owners: 1}) || payload.PendingApprovals != 1 || payload.Doctor == nil || payload.Doctor.Status != "degraded" || payload.Doctor.RecordedAt != "2026-07-14T08:00:00Z" {
		t.Fatalf("summary = %+v", payload)
	}
	if payload.ShipVersion != "v0.4.0" {
		t.Fatalf("ship version = %q", payload.ShipVersion)
	}
	if len(runner.commands) != 1 || runner.commands[0] != command {
		t.Fatalf("box status commands = %v, want only %q", runner.commands, command)
	}
}

func TestReadBoxStatusRejectsVersionOnlyPayload(t *testing.T) {
	runner := &fakeSSHRunner{responses: map[string]string{
		serverBoxStatusCommand(): `{"version":"v0.4.1"}`,
	}}
	if _, err := readBoxStatus(runner, "203.0.113.7"); err == nil {
		t.Fatal("version-only payload should not decode as box status")
	}
}

func TestRenderBoxStatusIncludesDoctorAge(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	payload := boxStatusPayload{Doctor: &boxStatusDoctor{Status: "ok", RecordedAt: "2026-07-14T08:00:00Z"}}

	out := renderBoxStatus(payload, "203.0.113.7", now)
	if !strings.Contains(out, "doctor: ok (2h ago)\n") {
		t.Fatalf("doctor status = %q", out)
	}
}

func TestRenderBoxStatusShowsLastCompletedUpdate(t *testing.T) {
	out := renderBoxStatus(boxStatusPayload{ShipVersion: "v0.4.0"}, "203.0.113.7", time.Now())
	if !strings.Contains(out, "ship: v0.4.0\n") || strings.Contains(out, "last client") {
		t.Fatalf("version lines = %q", out)
	}
}

func TestRenderBoxStatusPrintsAppCount(t *testing.T) {
	payload := boxStatusPayload{Apps: []boxStatusApp{{App: "api", EnvCount: 2}, {App: "web", EnvCount: 1}}, Members: &boxStatusMembers{Total: 3, Owners: 1}}
	out := renderBoxStatus(payload, "203.0.113.7", time.Now())
	if !strings.Contains(out, "apps: 2 (3 envs)\n") {
		t.Fatalf("app count = %q", out)
	}
	if !strings.Contains(out, "members: 3 (1 owners)\n") {
		t.Fatalf("member count = %q", out)
	}
	if strings.Index(out, "apps:") > strings.Index(out, "members:") || strings.Index(out, "members:") > strings.Index(out, "pending approvals:") {
		t.Fatalf("member count should follow apps and precede approvals:\n%s", out)
	}
	if strings.Contains(out, "api:") || strings.Contains(out, "web:") {
		t.Fatalf("status should not include app table:\n%s", out)
	}
}

func TestRenderBoxStatusShowsDoctorNeverRun(t *testing.T) {
	out := renderBoxStatus(boxStatusPayload{}, "203.0.113.7", time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC))
	if !strings.Contains(out, "doctor: never run\n") {
		t.Fatalf("doctor status = %q", out)
	}
}

func TestBoxStatusPayloadJSONIncludesDoctor(t *testing.T) {
	payload := boxStatusPayload{Members: &boxStatusMembers{Total: 3, Owners: 1}, Doctor: &boxStatusDoctor{Status: "degraded", RecordedAt: "2026-07-14T08:00:00Z"}}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Members *boxStatusMembers `json:"members"`
		Doctor  *boxStatusDoctor  `json:"doctor"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Doctor == nil || *decoded.Doctor != *payload.Doctor {
		t.Fatalf("doctor JSON = %s", data)
	}
	if decoded.Members == nil || *decoded.Members != *payload.Members {
		t.Fatalf("members JSON = %s", data)
	}
}

func TestRenderBoxStatusShowsUnknownMembers(t *testing.T) {
	out := renderBoxStatus(boxStatusPayload{}, "203.0.113.7", time.Now())
	if !strings.Contains(out, "members: unknown\n") {
		t.Fatalf("unknown member count = %q", out)
	}
}

func TestClassifyBoxUpdateVersionSkew(t *testing.T) {
	t.Run("newer helper requires client upgrade", func(t *testing.T) {
		err := classifyBoxUpdate("v0.4.1", "v0.4.0", "203.0.113.7")
		coded, ok := errcat.As(err)
		if !ok || coded.Code() != errcat.CodeClientBehindHelper {
			t.Fatalf("newer helper should map to client_behind_helper, got %v", err)
		}
	})

	t.Run("different git-describe builds require box setup", func(t *testing.T) {
		err := classifyBoxUpdate("v0.3.0-17-gabc", "v0.3.0-18-gdef", "203.0.113.7")
		coded, ok := errcat.As(err)
		if !ok || coded.Code() != errcat.CodeBoxVersionAmbiguous {
			t.Fatalf("ambiguous builds should map to box_version_ambiguous, got %v", err)
		}
		if got := coded.Remediation(); got != "ship box setup 203.0.113.7" {
			t.Fatalf("remediation = %q", got)
		}
	})
}

func TestValidateBoxUpdateTargetRefusesDevelopmentBuild(t *testing.T) {
	err := validateBoxUpdateTarget("v0.4.0", "v0.4.1-3-gabcdef", "203.0.113.7")
	if !errcat.Is(err, errcat.CodeBoxVersionAmbiguous) {
		t.Fatalf("validateBoxUpdateTarget error = %v, want %s", err, errcat.CodeBoxVersionAmbiguous)
	}
}

func TestIsGitDescribeVersion(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "v0.4.0-5-gabc123", want: true},
		{value: "v0.4.0", want: false},
		{value: "v0.4.0-rc1", want: false},
		{value: "v0.4.0-rc1-2-gabc", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := isGitDescribeVersion(tt.value); got != tt.want {
				t.Fatalf("isGitDescribeVersion(%q) = %t, want %t", tt.value, got, tt.want)
			}
		})
	}
}

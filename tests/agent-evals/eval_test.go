package agentevals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fprl/simple-vps/internal/identity"
)

const (
	evalImage         = "simple-vps-agent-evals:local"
	evalToolCallLimit = 6
	productionEnv     = "prod"
)

type commandResult struct {
	stdout string
	stderr string
	err    error
}

func (r commandResult) combined() string {
	switch {
	case r.stdout != "" && r.stderr != "":
		return r.stdout + "\n" + r.stderr
	case r.stdout != "":
		return r.stdout
	default:
		return r.stderr
	}
}

func (r commandResult) exitCode() int {
	if r.err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(r.err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

type evalSuite struct {
	ctx        context.Context
	repoRoot   string
	image      string
	dockerfile string
	tmp        string
	hostBinDir string
	shipBin    string
	binDir     string
	linuxBin   string
}

func TestAgentEvalScenarios(t *testing.T) {
	if os.Getenv("SHIP_RUN_FAKE_VPS_SMOKE") != "1" {
		t.Skip("set SHIP_RUN_FAKE_VPS_SMOKE=1 to run Docker-backed agent evals")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	t.Cleanup(cancel)

	suite := newEvalSuite(t, ctx)
	suite.buildBinaries(t)
	suite.buildImage(t)

	runnerKind := "oracle"
	if os.Getenv("SHIP_EVAL_RUNNER") == "agent" || os.Getenv("SHIP_EVAL_AGENT_CMD") != "" {
		runnerKind = "agent"
	}
	if runnerKind == "agent" && strings.TrimSpace(os.Getenv("SHIP_EVAL_AGENT_CMD")) == "" {
		t.Fatal("SHIP_EVAL_AGENT_CMD is required when SHIP_EVAL_RUNNER=agent")
	}

	for _, scenario := range evalScenarios() {
		scenario := scenario
		t.Run(scenario.Name, func(t *testing.T) {
			env := suite.newCase(t)
			result := runScenario(t, env, scenario, runnerKind)
			if result.Outcome == "passed" {
				t.Logf("%s passed with %d tool calls", scenario.Name, result.ToolCalls)
				return
			}
			if runnerKind == "oracle" {
				t.Logf("ORACLE_FINDING %s after %d tool calls: %s", scenario.Name, result.ToolCalls, result.DeadEnd)
				return
			}
			t.Fatalf("%s failed with %s after %d tool calls: %s", scenario.Name, result.Outcome, result.ToolCalls, result.DeadEnd)
		})
	}
}

func newEvalSuite(t *testing.T, ctx context.Context) *evalSuite {
	t.Helper()
	repoRoot := repoRootForTest(t)
	tmp := t.TempDir()
	s := &evalSuite{
		ctx:        ctx,
		repoRoot:   repoRoot,
		image:      evalImage,
		dockerfile: filepath.Join(repoRoot, "tests/agent-evals/Dockerfile"),
		tmp:        tmp,
		hostBinDir: filepath.Join(tmp, "host-bin"),
		shipBin:    filepath.Join(tmp, "host-bin", "ship"),
		binDir:     filepath.Join(repoRoot, "tests/agent-evals/.fake-vps-bin"),
		linuxBin:   filepath.Join(repoRoot, "tests/agent-evals/.fake-vps-bin/ship-linux-amd64"),
	}
	t.Cleanup(func() {
		if os.Getenv("KEEP_FAKE_VPS") == "1" {
			t.Logf("keeping eval binary dir: %s", s.binDir)
			return
		}
		_ = os.RemoveAll(s.binDir)
	})
	return s
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func (s *evalSuite) buildBinaries(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(s.binDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.hostBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	goCmd := os.Getenv("GO")
	if goCmd == "" {
		goCmd = "go"
	}
	mustRunHost(t, s.ctx, s.repoRoot, nil, goCmd, "build", "-trimpath", "-o", s.shipBin, ".")
	mustRunHost(t, s.ctx, s.repoRoot, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, goCmd, "build", "-trimpath", "-ldflags=-s -w", "-o", s.linuxBin, ".")
}

func (s *evalSuite) buildImage(t *testing.T) {
	t.Helper()
	mustRunHost(t, s.ctx, s.repoRoot, nil, "docker", "build", "-f", s.dockerfile, "-t", s.image, s.repoRoot)
}

func mustRunHost(t *testing.T, ctx context.Context, dir string, extraEnv []string, name string, args ...string) string {
	t.Helper()
	result := runHost(ctx, dir, extraEnv, nil, name, args...)
	if result.err != nil {
		t.Fatalf("%s %s failed: %v\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func runHost(ctx context.Context, dir string, extraEnv []string, stdin []byte, name string, args ...string) commandResult {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

type evalCase struct {
	suite     *evalSuite
	tmp       string
	container string
	sshBinDir string
	docs      string
}

func (s *evalSuite) newCase(t *testing.T) *evalCase {
	t.Helper()
	e := &evalCase{
		suite: s,
		tmp:   t.TempDir(),
	}
	t.Cleanup(func() {
		if os.Getenv("KEEP_FAKE_VPS") == "1" {
			t.Logf("keeping fake VPS container: %s", e.container)
			t.Logf("keeping fake VPS temp dir: %s", e.tmp)
			return
		}
		if e.container != "" {
			_ = exec.CommandContext(context.Background(), "docker", "rm", "-f", e.container).Run()
		}
	})

	e.startContainer(t)
	e.configureSSH(t, "deploy")
	e.waitForSSH(t)
	e.ensureSmokeHostSeed(t)
	e.docs = e.mustRun(t, s.repoRoot, nil, nil, s.shipBin, "docs")
	if strings.TrimSpace(e.docs) == "" {
		t.Fatal("ship docs returned empty output")
	}
	return e
}

func (e *evalCase) startContainer(t *testing.T) {
	t.Helper()
	out := e.mustRun(t, e.suite.repoRoot, nil, nil, "docker", "run", "-d", "-p", "127.0.0.1::22", e.suite.image)
	e.container = strings.TrimSpace(out)
	if e.container == "" {
		t.Fatal("docker run returned empty container id")
	}
}

func (e *evalCase) configureSSH(t *testing.T, user string) {
	t.Helper()
	keyPath := filepath.Join(e.tmp, "id_ed25519")
	e.mustRun(t, e.suite.repoRoot, nil, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "agent-eval", "-f", keyPath)

	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	sshDir := "/home/" + user + "/.ssh"
	owner := user + ":" + user
	if user == "root" {
		sshDir = "/root/.ssh"
		owner = "root:root"
	}
	authorize := fmt.Sprintf("mkdir -p %[1]s && cat > %[1]s/authorized_keys && chown %[2]s %[1]s/authorized_keys && chmod 600 %[1]s/authorized_keys", sshDir, owner)
	e.mustRun(t, e.suite.repoRoot, nil, pub, "docker", "exec", "-i", e.container, "bash", "-lc", authorize)

	portOutput := strings.TrimSpace(e.mustRun(t, e.suite.repoRoot, nil, nil, "docker", "port", e.container, "22/tcp"))
	colon := strings.LastIndex(portOutput, ":")
	if colon == -1 || colon == len(portOutput)-1 {
		t.Fatalf("unexpected docker port output: %q", portOutput)
	}
	port := portOutput[colon+1:]

	homeSSH := filepath.Join(e.tmp, "home", ".ssh")
	if err := os.MkdirAll(homeSSH, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`Host fake-vps
  HostName 127.0.0.1
  Port %s
  User %s
  IdentityFile %s
  IdentitiesOnly yes
  BatchMode yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
`, port, user, keyPath)
	if err := os.WriteFile(filepath.Join(homeSSH, "config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	hostSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	hostSCP, err := exec.LookPath("scp")
	if err != nil {
		t.Fatal(err)
	}
	e.sshBinDir = filepath.Join(e.tmp, "bin")
	if err := os.MkdirAll(e.sshBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	sshWrapper := fmt.Sprintf("#!/usr/bin/env bash\nexec %q -F %q \"$@\"\n", hostSSH, filepath.Join(homeSSH, "config"))
	if err := os.WriteFile(filepath.Join(e.sshBinDir, "ssh"), []byte(sshWrapper), 0755); err != nil {
		t.Fatal(err)
	}
	scpWrapper := fmt.Sprintf("#!/usr/bin/env bash\nexec %q -F %q \"$@\"\n", hostSCP, filepath.Join(homeSSH, "config"))
	if err := os.WriteFile(filepath.Join(e.sshBinDir, "scp"), []byte(scpWrapper), 0755); err != nil {
		t.Fatal(err)
	}
}

func (e *evalCase) waitForSSH(t *testing.T) {
	t.Helper()
	var last commandResult
	for i := 0; i < 30; i++ {
		last = e.run(t, e.suite.repoRoot, nil, nil, e.sshBin(), "fake-vps", "true")
		if last.err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("fake VPS ssh did not become ready\nstdout:\n%s\nstderr:\n%s\nerr: %v", last.stdout, last.stderr, last.err)
}

func (e *evalCase) ensureSmokeHostSeed(t *testing.T) {
	t.Helper()
	e.dockerExec(t, `cat > /etc/simple-vps/host.json <<'EOF'
{"version":1,"desired":{"users":{"operator":"operator","deploy":"deploy"},"ingress":{"expose":"public","tunnel":"none"},"features":{},"packages":{"podman":{"source":"apt"},"rsync":{"source":"apt"},"caddy":{"source":"container"}}},"observed":{"packages":{},"ingress":{}},"meta":{}}
EOF`)
	e.dockerExec(t, "mkdir -p /etc/caddy/simple-vps /etc/caddy/conf.d /var/lib/caddy /etc/systemd/system")
	e.dockerExec(t, "mkdir -p /tmp/simple-vps-deploy && chmod 1777 /tmp/simple-vps-deploy")
	e.dockerExec(t, `cat > /etc/caddy/Caddyfile <<'EOF'
import simple-vps/*.caddy
import conf.d/*.caddy
EOF`)
	e.dockerExec(t, "podman network exists ingress || podman network create ingress")
	e.dockerExec(t, "if [ ! -f /run/fake-podman/containers/caddy.labels ]; then podman run -d --name caddy --network ingress --publish 80:80 -v /etc/caddy:/etc/caddy:Z docker.io/library/caddy:2-alpine; fi")
	e.dockerExec(t, "touch /etc/systemd/system/ship-preview-reaper.timer /etc/systemd/system/ship-doctor.timer")
	e.dockerExec(t, "systemctl enable ship-preview-reaper.timer >/dev/null && systemctl start ship-preview-reaper.timer >/dev/null")
}

func (e *evalCase) sshBin() string {
	if e.sshBinDir == "" {
		return "ssh"
	}
	return filepath.Join(e.sshBinDir, "ssh")
}

func (e *evalCase) dockerExec(t *testing.T, command string) string {
	t.Helper()
	result := e.run(t, e.suite.repoRoot, nil, nil, "docker", "exec", e.container, "bash", "-c", command)
	if result.err != nil {
		t.Fatalf("docker exec %q failed: %v\nstdout:\n%s\nstderr:\n%s", command, result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func (e *evalCase) mustRun(t *testing.T, dir string, extraEnv []string, stdin []byte, name string, args ...string) string {
	t.Helper()
	result := e.run(t, dir, extraEnv, stdin, name, args...)
	if result.err != nil {
		t.Fatalf("%s %s failed: %v\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func (e *evalCase) run(t *testing.T, dir string, extraEnv []string, stdin []byte, name string, args ...string) commandResult {
	t.Helper()
	cmd := exec.CommandContext(e.suite.ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = e.commandEnv(extraEnv)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func (e *evalCase) runShell(dir string, extraEnv []string, stdin []byte, command string) commandResult {
	cmd := exec.CommandContext(e.suite.ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = e.commandEnv(extraEnv)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func (e *evalCase) commandEnv(extra []string) []string {
	env := os.Environ()
	pathParts := []string{e.suite.hostBinDir}
	if e.sshBinDir != "" {
		pathParts = append([]string{e.sshBinDir}, pathParts...)
	}
	pathParts = append(pathParts, os.Getenv("PATH"))
	env = setEnv(env, "PATH", strings.Join(pathParts, string(os.PathListSeparator)))
	return append(env, extra...)
}

func setEnv(env []string, key string, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

type evalScenario struct {
	Name           string
	SetupSummary   string
	Goal           string
	CheckerSummary string
	RetryCommand   string
	FixGuidance    string
	FixCommand     string
	Setup          func(t *testing.T, e *evalCase) *evalProject
	Induce         func(t *testing.T, e *evalCase, p *evalProject) commandResult
	Check          func(e *evalCase, p *evalProject) error
}

type evalProject struct {
	Dir         string
	App         string
	Host        string
	Branch      string
	SecretKey   string
	SecretValue string
}

func evalScenarios() []evalScenario {
	return []evalScenario{
		{
			Name:           "missing-secret",
			SetupSummary:   "container app on main references @secret:api_token, but no secret is stored",
			Goal:           "make the deploy succeed; use eval-secret-api_token for api_token",
			CheckerSummary: "Production main is live, serves 200, and API_TOKEN is resolved to eval-secret-api_token",
			RetryCommand:   "ship",
			Setup:          setupMissingSecret,
			Induce:         induceShip,
			Check:          checkMissingSecretRecovered,
		},
		{
			Name:           "failing-release-command",
			SetupSummary:   "baseline deploy is live, then the next committed release command exits non-zero",
			Goal:           "make the deploy succeed",
			CheckerSummary: "Production main serves the current HEAD release with MARKER=next-marker",
			RetryCommand:   "ship",
			FixGuidance:    "fix the release command in ship.toml, then ship",
			FixCommand:     `python3 -c 'from pathlib import Path; p=Path("ship.toml"); s=p.read_text(); p.write_text(s.replace("simple-vps-fail-release", "touch /data/release-ok"))' && git add ship.toml && git commit -m 'fix release command'`,
			Setup:          setupFailingReleaseCommand,
			Induce:         induceShip,
			Check:          checkCurrentHeadProductionLive,
		},
		{
			Name:           "probe-failure-wrong-port",
			SetupSummary:   "baseline deploy is live, then the next committed manifest probes the wrong process port",
			Goal:           "make the deploy succeed",
			CheckerSummary: "Production main serves the current HEAD release",
			RetryCommand:   "ship",
			FixGuidance:    "fix the process port or probe path in ship.toml, then ship",
			FixCommand:     `python3 -c 'from pathlib import Path; p=Path("ship.toml"); s=p.read_text(); p.write_text(s.replace("port = 3999", "port = 3000"))' && git add ship.toml && git commit -m 'fix probe port'`,
			Setup:          setupProbeFailureWrongPort,
			Induce:         induceShip,
			Check:          checkCurrentHeadProductionLive,
		},
		{
			Name:           "missing-dockerfile",
			SetupSummary:   "container manifest declares a process but the repo has no Dockerfile",
			Goal:           "make the deploy succeed",
			CheckerSummary: "Production main serves the current HEAD release",
			RetryCommand:   "ship",
			Setup:          setupMissingDockerfile,
			Induce:         induceShip,
			Check:          checkCurrentHeadProductionLive,
		},
		{
			Name:           "expired-preview-referenced",
			SetupSummary:   "feature preview is deployed, forcibly expired, reaped, then referenced by pin",
			Goal:           "recreate the expired preview",
			CheckerSummary: "Preview feature/expired exists again and serves 200",
			RetryCommand:   "ship pin feature/expired",
			Setup:          setupExpiredPreviewReferenced,
			Induce:         inducePinExpiredPreview,
			Check:          checkExpiredPreviewRecreated,
		},
		{
			Name:           "dirty-branch-state",
			SetupSummary:   "production branch has an uncommitted worktree change",
			Goal:           "make the deploy succeed",
			CheckerSummary: "Production main serves the committed dirty-change release",
			RetryCommand:   "ship",
			Setup:          setupDirtyBranchState,
			Induce:         induceShip,
			Check:          checkCurrentHeadProductionLive,
		},
	}
}

func setupMissingSecret(t *testing.T, e *evalCase) *evalProject {
	app := newProjectDir(t, e, "missing-secret")
	writeSecretFixture(t, app, "evalsecret", "eval-secret.example.com")
	initGit(t, e, app)
	checkoutBranch(t, e, app, "main")
	return &evalProject{Dir: app, App: "evalsecret", Host: "eval-secret.example.com", Branch: "main", SecretKey: "api_token", SecretValue: "eval-secret-api_token"}
}

func setupFailingReleaseCommand(t *testing.T, e *evalCase) *evalProject {
	app := newProjectDir(t, e, "release-command")
	writeReleaseFixture(t, app, "touch /data/release-ok", "stable")
	initGit(t, e, app)
	checkoutBranch(t, e, app, "main")
	e.mustRun(t, app, nil, nil, e.suite.shipBin)

	manifestPath := filepath.Join(app, "ship.toml")
	manifest := mustRead(t, manifestPath)
	manifest = strings.Replace(manifest, `release = "touch /data/release-ok"`, `release = "simple-vps-fail-release"`, 1)
	manifest = strings.Replace(manifest, `MARKER = "stable"`, `MARKER = "next-marker"`, 1)
	mustWrite(t, manifestPath, manifest)
	mustWrite(t, filepath.Join(app, "README.md"), "failing release command\n")
	commitAll(t, e, app, "induce failing release command")
	return &evalProject{Dir: app, App: "evalrelease", Host: "eval-release.example.com", Branch: "main"}
}

func setupProbeFailureWrongPort(t *testing.T, e *evalCase) *evalProject {
	app := newProjectDir(t, e, "probe-failure")
	writeProbeFixture(t, app, 3000)
	initGit(t, e, app)
	checkoutBranch(t, e, app, "main")
	e.mustRun(t, app, nil, nil, e.suite.shipBin)

	manifestPath := filepath.Join(app, "ship.toml")
	manifest := mustRead(t, manifestPath)
	manifest = strings.Replace(manifest, "port = 3000", "port = 3999", 1)
	mustWrite(t, manifestPath, manifest)
	mustWrite(t, filepath.Join(app, "README.md"), "wrong probe port\n")
	commitAll(t, e, app, "induce probe failure")
	return &evalProject{Dir: app, App: "evalprobe", Host: "eval-probe.example.com", Branch: "main"}
}

func setupMissingDockerfile(t *testing.T, e *evalCase) *evalProject {
	app := newProjectDir(t, e, "missing-dockerfile")
	writeManifestOnlyContainerFixture(t, app, "evaldockerfile", "eval-dockerfile.example.com")
	initGit(t, e, app)
	checkoutBranch(t, e, app, "main")
	return &evalProject{Dir: app, App: "evaldockerfile", Host: "eval-dockerfile.example.com", Branch: "main"}
}

func setupExpiredPreviewReferenced(t *testing.T, e *evalCase) *evalProject {
	app := newProjectDir(t, e, "expired-preview")
	writeBasicContainerFixture(t, app, "evalpreview", "eval-preview.example.com")
	initGit(t, e, app)
	checkoutBranch(t, e, app, "main")
	e.mustRun(t, app, nil, nil, e.suite.shipBin)

	checkoutBranch(t, e, app, "feature/expired")
	mustWrite(t, filepath.Join(app, "README.md"), "expired preview\n")
	commitAll(t, e, app, "deploy preview")
	e.mustRun(t, app, nil, nil, e.suite.shipBin)
	previewEnv := previewEnvForBranch(t, e, "evalpreview", "feature/expired")
	forcePreviewExpired(t, e, "evalpreview", previewEnv)
	e.dockerExec(t, "/usr/local/bin/ship server env reap")
	checkoutBranch(t, e, app, "main")
	return &evalProject{Dir: app, App: "evalpreview", Host: "eval-preview.example.com", Branch: "feature/expired"}
}

func setupDirtyBranchState(t *testing.T, e *evalCase) *evalProject {
	app := newProjectDir(t, e, "dirty-branch")
	writeBasicContainerFixture(t, app, "evaldirty", "eval-dirty.example.com")
	initGit(t, e, app)
	checkoutBranch(t, e, app, "main")
	mustWrite(t, filepath.Join(app, "README.md"), "dirty change\n")
	return &evalProject{Dir: app, App: "evaldirty", Host: "eval-dirty.example.com", Branch: "main"}
}

func induceShip(t *testing.T, e *evalCase, p *evalProject) commandResult {
	t.Helper()
	return e.run(t, p.Dir, nil, nil, e.suite.shipBin)
}

func inducePinExpiredPreview(t *testing.T, e *evalCase, p *evalProject) commandResult {
	t.Helper()
	return e.run(t, p.Dir, nil, nil, e.suite.shipBin, "pin", p.Branch)
}

func newProjectDir(t *testing.T, e *evalCase, name string) string {
	t.Helper()
	dir := filepath.Join(e.tmp, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func initGit(t *testing.T, e *evalCase, app string) {
	t.Helper()
	e.mustRun(t, app, nil, nil, "git", "init", "-q")
	e.mustRun(t, app, nil, nil, "git", "config", "user.email", "eval@example.com")
	e.mustRun(t, app, nil, nil, "git", "config", "user.name", "Agent Eval")
	commitAll(t, e, app, "fixture")
}

func checkoutBranch(t *testing.T, e *evalCase, app string, branch string) {
	t.Helper()
	e.mustRun(t, app, nil, nil, "git", "checkout", "-B", branch)
}

func commitAll(t *testing.T, e *evalCase, app string, message string) {
	t.Helper()
	e.mustRun(t, app, nil, nil, "git", "add", ".")
	e.mustRun(t, app, nil, nil, "git", "commit", "-q", "-m", message)
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeDockerfile(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
}

func writeBasicContainerFixture(t *testing.T, app string, name string, host string) {
	t.Helper()
	writeDockerfile(t, app)
	writeManifestOnlyContainerFixture(t, app, name, host)
}

func writeManifestOnlyContainerFixture(t *testing.T, app string, name string, host string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "ship.toml"), fmt.Sprintf(`name = "%s"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"%s" = "web"
`, name, host))
}

func writeSecretFixture(t *testing.T, app string, name string, host string) {
	t.Helper()
	writeDockerfile(t, app)
	mustWrite(t, filepath.Join(app, "ship.toml"), fmt.Sprintf(`name = "%s"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[env]
API_TOKEN = "@secret:api_token"

[processes]
web = { port = 3000 }

[routes]
"%s" = "web"
`, name, host))
}

func writeReleaseFixture(t *testing.T, app string, release string, marker string) {
	t.Helper()
	writeDockerfile(t, app)
	mustWrite(t, filepath.Join(app, "ship.toml"), fmt.Sprintf(`name = "evalrelease"
box = "fake-vps"
production_branch = "main"
release = "%s"
probe = "/health"

[env]
MARKER = "%s"

[processes]
web = { port = 3000 }

[routes]
"eval-release.example.com" = "web"
`, release, marker))
}

func writeProbeFixture(t *testing.T, app string, port int) {
	t.Helper()
	writeDockerfile(t, app)
	mustWrite(t, filepath.Join(app, "ship.toml"), fmt.Sprintf(`name = "evalprobe"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[processes]
web = { port = %d, cmd = "simple-vps-listen-port=3000 sleep 3600" }

[routes]
"eval-probe.example.com" = "web"
`, port))
}

type scenarioRunResult struct {
	Outcome   string
	ToolCalls int
	DeadEnd   string
}

func runScenario(t *testing.T, e *evalCase, scenario evalScenario, runnerKind string) scenarioRunResult {
	t.Helper()
	tr := newTranscript(t, e.suite.repoRoot, runnerKind, scenario)
	defer tr.write(t)

	project := scenario.Setup(t, e)
	tr.header(project, scenario, e.docs)

	initial := scenario.Induce(t, e, project)
	tr.command("induced failure", 0, scenario.RetryCommand, initial)
	if initial.err == nil {
		result := scenarioRunResult{Outcome: "scenario_invalid", DeadEnd: "induced command succeeded; scenario did not produce a failure"}
		tr.outcome(result)
		t.Fatalf("%s: %s", scenario.Name, result.DeadEnd)
	}
	if err := scenario.Check(e, project); err == nil {
		result := scenarioRunResult{Outcome: "scenario_invalid", DeadEnd: "checker passed before recovery"}
		tr.outcome(result)
		t.Fatalf("%s: %s", scenario.Name, result.DeadEnd)
	}

	var result scenarioRunResult
	if runnerKind == "agent" {
		result = runAgent(t, e, scenario, project, initial, tr)
	} else {
		result = runOracle(e, scenario, project, initial, tr)
	}
	tr.outcome(result)
	return result
}

func runOracle(e *evalCase, scenario evalScenario, project *evalProject, initial commandResult, tr *transcript) scenarioRunResult {
	lastOutput := initial.combined()
	toolCalls := 0
	seenCommands := map[string]bool{}
	retryAfterRemediation := false

	for toolCalls < evalToolCallLimit {
		if err := scenario.Check(e, project); err == nil {
			return scenarioRunResult{Outcome: "passed", ToolCalls: toolCalls}
		}
		next, ok := extractNext(lastOutput)
		if !ok {
			return scenarioRunResult{Outcome: "dead_end", ToolCalls: toolCalls, DeadEnd: deadEndText("no next remediation in output", lastOutput)}
		}
		command, stdin, executable, reason := oracleCommand(next, lastOutput, project, scenario)
		if !executable {
			return scenarioRunResult{Outcome: "dead_end", ToolCalls: toolCalls, DeadEnd: deadEndText(reason, lastOutput)}
		}
		if seenCommands[command] && !(retryAfterRemediation && command == scenario.RetryCommand) {
			return scenarioRunResult{Outcome: "dead_end", ToolCalls: toolCalls, DeadEnd: deadEndText("remediation cycle repeated command: "+command, lastOutput)}
		}
		seenCommands[command] = true
		retryAfterRemediation = false

		result := e.runShell(project.Dir, nil, stdin, command)
		toolCalls++
		tr.command("oracle", toolCalls, command, result)
		if err := scenario.Check(e, project); err == nil {
			return scenarioRunResult{Outcome: "passed", ToolCalls: toolCalls}
		}
		lastOutput = result.combined()
		if result.err == nil {
			if _, ok := extractNext(lastOutput); !ok && scenario.RetryCommand != "" && command != scenario.RetryCommand {
				lastOutput = "next: " + scenario.RetryCommand
				retryAfterRemediation = true
			}
		}
	}
	return scenarioRunResult{Outcome: "max_tool_calls", ToolCalls: toolCalls, DeadEnd: "checker did not pass within 6 tool calls"}
}

func oracleCommand(next string, output string, project *evalProject, scenario evalScenario) (string, []byte, bool, string) {
	command := strings.TrimSpace(next)
	if command == "" {
		return "", nil, false, "empty next remediation"
	}
	if scenario.FixCommand != "" && command == scenario.FixGuidance {
		return scenario.FixCommand, nil, true, ""
	}
	if strings.Contains(command, "<message>") {
		command = strings.ReplaceAll(command, "<message>", "ship eval remediation")
	}
	if strings.Contains(command, "KEY") {
		key := missingSecretFromOutput(output)
		if key == "" {
			return "", nil, false, "next remediation contains KEY but output does not name a secret"
		}
		command = strings.ReplaceAll(command, "KEY", key)
	}
	if strings.Contains(command, "<") || strings.Contains(command, ">") {
		return "", nil, false, "next remediation contains an unresolved placeholder: " + command
	}
	if command == "fix ship.toml" || strings.HasPrefix(command, "fix ") {
		return "", nil, false, "next remediation is not an executable shell command: " + command
	}

	var stdin []byte
	if strings.HasPrefix(command, "ship secret set ") && !strings.Contains(command, "|") && !strings.Contains(command, "<") {
		key := secretKeyFromSetCommand(command)
		value := project.SecretValue
		if value == "" {
			value = "eval-secret-" + strings.ToLower(key)
		}
		stdin = []byte(value)
	}
	return command, stdin, true, ""
}

var missingSecretRe = regexp.MustCompile(`missing secret ([A-Za-z_][A-Za-z0-9_]*)`)

func missingSecretFromOutput(output string) string {
	match := missingSecretRe.FindStringSubmatch(output)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func secretKeyFromSetCommand(command string) string {
	fields := strings.Fields(command)
	for i, field := range fields {
		if field == "set" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return "secret"
}

func extractNext(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "next: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "next: ")), true
		}
	}
	return "", false
}

func deadEndText(reason string, output string) string {
	return reason + "\n--- dead-end output ---\n" + strings.TrimSpace(output)
}

func runAgent(t *testing.T, e *evalCase, scenario evalScenario, project *evalProject, initial commandResult, tr *transcript) scenarioRunResult {
	t.Helper()
	runner := agentRunner{
		CommandTemplate: os.Getenv("SHIP_EVAL_AGENT_CMD"),
		Mode:            os.Getenv("SHIP_EVAL_AGENT_MODE"),
	}
	if runner.Mode == "" {
		runner.Mode = "turn"
	}
	history := formatHistoryCommand("induced failure", scenario.RetryCommand, initial)
	toolCalls := 0

	switch runner.Mode {
	case "script":
		commands, err := runner.invoke(t, e, scenario, project, history, 1, tr)
		if err != nil {
			return scenarioRunResult{Outcome: "agent_error", ToolCalls: toolCalls, DeadEnd: err.Error()}
		}
		for _, command := range commands {
			if toolCalls >= evalToolCallLimit {
				break
			}
			result := e.runShell(project.Dir, nil, nil, command)
			toolCalls++
			tr.command("agent", toolCalls, command, result)
			history += "\n" + formatHistoryCommand(fmt.Sprintf("tool call %d", toolCalls), command, result)
			if err := scenario.Check(e, project); err == nil {
				return scenarioRunResult{Outcome: "passed", ToolCalls: toolCalls}
			}
		}
	case "turn":
		for toolCalls < evalToolCallLimit {
			commands, err := runner.invoke(t, e, scenario, project, history, toolCalls+1, tr)
			if err != nil {
				return scenarioRunResult{Outcome: "agent_error", ToolCalls: toolCalls, DeadEnd: err.Error()}
			}
			if len(commands) == 0 {
				return scenarioRunResult{Outcome: "agent_error", ToolCalls: toolCalls, DeadEnd: "agent emitted no shell commands"}
			}
			for _, command := range commands {
				if toolCalls >= evalToolCallLimit {
					break
				}
				result := e.runShell(project.Dir, nil, nil, command)
				toolCalls++
				tr.command("agent", toolCalls, command, result)
				history += "\n" + formatHistoryCommand(fmt.Sprintf("tool call %d", toolCalls), command, result)
				if err := scenario.Check(e, project); err == nil {
					return scenarioRunResult{Outcome: "passed", ToolCalls: toolCalls}
				}
			}
		}
	default:
		return scenarioRunResult{Outcome: "agent_error", ToolCalls: toolCalls, DeadEnd: "unknown SHIP_EVAL_AGENT_MODE " + runner.Mode}
	}

	return scenarioRunResult{Outcome: "max_tool_calls", ToolCalls: toolCalls, DeadEnd: "checker did not pass within 6 tool calls"}
}

type agentRunner struct {
	CommandTemplate string
	Mode            string
}

func (r agentRunner) invoke(t *testing.T, e *evalCase, scenario evalScenario, project *evalProject, history string, turn int, tr *transcript) ([]string, error) {
	t.Helper()
	prompt := renderAgentPrompt(scenario, project, e.docs, history)
	promptFile := filepath.Join(e.tmp, fmt.Sprintf("agent-prompt-%02d.md", turn))
	contextFile := filepath.Join(e.tmp, "ship-docs.md")
	lastOutputFile := filepath.Join(e.tmp, fmt.Sprintf("agent-last-output-%02d.txt", turn))
	if err := os.WriteFile(promptFile, []byte(prompt), 0644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(contextFile, []byte(e.docs), 0644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(lastOutputFile, []byte(history), 0644); err != nil {
		return nil, err
	}

	command := expandAgentTemplate(r.CommandTemplate, map[string]string{
		"prompt":           prompt,
		"prompt_file":      promptFile,
		"context_file":     contextFile,
		"goal":             scenario.Goal,
		"workdir":          project.Dir,
		"turn":             fmt.Sprintf("%d", turn),
		"last_output_file": lastOutputFile,
	})
	result := e.runShell(project.Dir, []string{"SHIP_EVAL_TURN=" + fmt.Sprintf("%d", turn)}, []byte(prompt), command)
	tr.agentInvocation(turn, command, result)
	if result.err != nil {
		return nil, fmt.Errorf("agent command failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	return parseAgentCommands(result.stdout), nil
}

func renderAgentPrompt(scenario evalScenario, project *evalProject, docs string, history string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", scenario.Goal)
	fmt.Fprintf(&b, "Working directory: %s\n", project.Dir)
	b.WriteString("The ship binary is on PATH and configured for the fake-vps box.\n")
	b.WriteString("Emit shell commands only. Each non-empty command line is one tool call. The limit is 6.\n\n")
	b.WriteString("Command history:\n")
	b.WriteString(history)
	b.WriteString("\n\nship docs:\n")
	b.WriteString(docs)
	return b.String()
}

func expandAgentTemplate(template string, values map[string]string) string {
	out := template
	for key, value := range values {
		replacement := shellQuote(value)
		if key == "turn" {
			replacement = value
		}
		out = strings.ReplaceAll(out, "{"+key+"}", replacement)
	}
	return out
}

func parseAgentCommands(output string) []string {
	var commands []string
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "```") {
			continue
		}
		line = strings.TrimPrefix(line, "$ ")
		if line != "" {
			commands = append(commands, line)
		}
	}
	return commands
}

func formatHistoryCommand(label string, command string, result commandResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n", label)
	fmt.Fprintf(&b, "$ %s\n", command)
	fmt.Fprintf(&b, "exit: %d\n", result.exitCode())
	b.WriteString("stdout:\n")
	b.WriteString(result.stdout)
	if !strings.HasSuffix(result.stdout, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("stderr:\n")
	b.WriteString(result.stderr)
	if !strings.HasSuffix(result.stderr, "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

type transcript struct {
	path string
	buf  strings.Builder
}

func newTranscript(t *testing.T, repoRoot string, runnerKind string, scenario evalScenario) *transcript {
	t.Helper()
	dir := filepath.Join(repoRoot, "tests/agent-evals/transcripts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%s_%s_%s.txt", time.Now().UTC().Format("20060102T150405.000000000Z"), runnerKind, sanitizeFilename(scenario.Name))
	return &transcript{path: filepath.Join(dir, name)}
}

func (tlog *transcript) header(project *evalProject, scenario evalScenario, docs string) {
	fmt.Fprintf(&tlog.buf, "# %s\n\n", filepath.Base(tlog.path))
	fmt.Fprintf(&tlog.buf, "project: %s\n", project.Dir)
	fmt.Fprintf(&tlog.buf, "app: %s\n", project.App)
	fmt.Fprintf(&tlog.buf, "setup: %s\n", scenario.SetupSummary)
	fmt.Fprintf(&tlog.buf, "goal: %s\n", scenario.Goal)
	fmt.Fprintf(&tlog.buf, "checker: %s\n", scenario.CheckerSummary)
	fmt.Fprintf(&tlog.buf, "ship docs lines: %d\n\n", strings.Count(docs, "\n"))
}

func (tlog *transcript) command(kind string, count int, command string, result commandResult) {
	fmt.Fprintf(&tlog.buf, "## %s", kind)
	if count > 0 {
		fmt.Fprintf(&tlog.buf, " %d", count)
	}
	tlog.buf.WriteString("\n\n")
	fmt.Fprintf(&tlog.buf, "$ %s\n", command)
	fmt.Fprintf(&tlog.buf, "exit: %d\n\n", result.exitCode())
	tlog.buf.WriteString("stdout:\n")
	tlog.buf.WriteString(result.stdout)
	if !strings.HasSuffix(result.stdout, "\n") {
		tlog.buf.WriteByte('\n')
	}
	tlog.buf.WriteString("\nstderr:\n")
	tlog.buf.WriteString(result.stderr)
	if !strings.HasSuffix(result.stderr, "\n") {
		tlog.buf.WriteByte('\n')
	}
	tlog.buf.WriteByte('\n')
}

func (tlog *transcript) agentInvocation(turn int, command string, result commandResult) {
	fmt.Fprintf(&tlog.buf, "## agent invocation %d\n\n", turn)
	fmt.Fprintf(&tlog.buf, "$ %s\n", command)
	fmt.Fprintf(&tlog.buf, "exit: %d\n\n", result.exitCode())
	tlog.buf.WriteString("agent stdout:\n")
	tlog.buf.WriteString(result.stdout)
	if !strings.HasSuffix(result.stdout, "\n") {
		tlog.buf.WriteByte('\n')
	}
	tlog.buf.WriteString("\nagent stderr:\n")
	tlog.buf.WriteString(result.stderr)
	if !strings.HasSuffix(result.stderr, "\n") {
		tlog.buf.WriteByte('\n')
	}
	tlog.buf.WriteByte('\n')
}

func (tlog *transcript) outcome(result scenarioRunResult) {
	tlog.buf.WriteString("## outcome\n\n")
	fmt.Fprintf(&tlog.buf, "outcome: %s\n", result.Outcome)
	fmt.Fprintf(&tlog.buf, "tool_calls: %d\n", result.ToolCalls)
	if result.DeadEnd != "" {
		tlog.buf.WriteString("dead_end:\n")
		tlog.buf.WriteString(result.DeadEnd)
		tlog.buf.WriteByte('\n')
	}
}

func (tlog *transcript) write(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(tlog.path, []byte(tlog.buf.String()), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	t.Logf("transcript: %s", tlog.path)
}

func sanitizeFilename(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func checkMissingSecretRecovered(e *evalCase, p *evalProject) error {
	if err := checkCurrentHeadProductionLive(e, p); err != nil {
		return err
	}
	envFile := e.runShell(e.suite.repoRoot, nil, nil, "docker exec "+shellQuote(e.container)+" bash -c "+shellQuote("cat "+identity.EnvFile(p.App, productionEnv)))
	if envFile.err != nil {
		return fmt.Errorf("read env file failed: %v\nstdout:%s\nstderr:%s", envFile.err, envFile.stdout, envFile.stderr)
	}
	want := "API_TOKEN=" + p.SecretValue + "\n"
	if !strings.Contains(envFile.stdout, want) {
		return fmt.Errorf("runtime env file missing %q; got:\n%s", want, envFile.stdout)
	}
	return nil
}

func checkCurrentHeadProductionLive(e *evalCase, p *evalProject) error {
	head := strings.TrimSpace(e.runShell(p.Dir, nil, nil, "git rev-parse --short=12 HEAD").stdout)
	if head == "" {
		return fmt.Errorf("could not read current HEAD")
	}
	status, err := readStatus(e, p)
	if err != nil {
		return err
	}
	env, ok := status.envByKind("Production")
	if !ok {
		return fmt.Errorf("status has no Production env: %+v", status.Envs)
	}
	if env.Release != head {
		return fmt.Errorf("Production release = %q, want current HEAD %q", env.Release, head)
	}
	if env.Health != "healthy" {
		return fmt.Errorf("Production health = %q, want healthy", env.Health)
	}
	if err := e.urlServes200(env.URL); err != nil {
		return err
	}
	return nil
}

func checkExpiredPreviewRecreated(e *evalCase, p *evalProject) error {
	status, err := readStatus(e, p)
	if err != nil {
		return err
	}
	env, ok := status.envByBranch(p.Branch)
	if !ok {
		return fmt.Errorf("status has no Preview branch %s: %+v", p.Branch, status.Envs)
	}
	if env.Kind != "Preview" {
		return fmt.Errorf("branch %s kind = %q, want Preview", p.Branch, env.Kind)
	}
	if env.Health != "healthy" {
		return fmt.Errorf("Preview health = %q, want healthy", env.Health)
	}
	if err := e.urlServes200(env.URL); err != nil {
		return err
	}
	return nil
}

type evalStatusPayload struct {
	App  string          `json:"app"`
	Envs []evalStatusEnv `json:"envs"`
}

type evalStatusEnv struct {
	Kind    string `json:"kind"`
	Branch  string `json:"branch"`
	URL     string `json:"url"`
	Env     string `json:"env"`
	Release string `json:"release"`
	Health  string `json:"health"`
}

func (p evalStatusPayload) envByKind(kind string) (evalStatusEnv, bool) {
	for _, env := range p.Envs {
		if env.Kind == kind {
			return env, true
		}
	}
	return evalStatusEnv{}, false
}

func (p evalStatusPayload) envByBranch(branch string) (evalStatusEnv, bool) {
	for _, env := range p.Envs {
		if env.Branch == branch {
			return env, true
		}
	}
	return evalStatusEnv{}, false
}

func readStatus(e *evalCase, p *evalProject) (evalStatusPayload, error) {
	result := e.runShell(p.Dir, nil, nil, "ship status --json")
	if result.err != nil {
		return evalStatusPayload{}, fmt.Errorf("ship status --json failed: %v\nstdout:%s\nstderr:%s", result.err, result.stdout, result.stderr)
	}
	var payload evalStatusPayload
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		return evalStatusPayload{}, fmt.Errorf("parse status JSON: %v\n%s", err, result.stdout)
	}
	if payload.App != p.App {
		return evalStatusPayload{}, fmt.Errorf("status app = %q, want %q", payload.App, p.App)
	}
	return payload, nil
}

func (e *evalCase) urlServes200(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse URL %q: %v", rawURL, err)
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	command := fmt.Sprintf("curl -fsS -o /dev/null -w '%%{http_code}' -H %s %s",
		shellQuote("Host: "+parsed.Hostname()),
		shellQuote("http://127.0.0.1"+path),
	)
	result := e.runShell(e.suite.repoRoot, nil, nil, "ssh fake-vps "+shellQuote(command))
	if result.err != nil {
		return fmt.Errorf("curl through fake Caddy failed: %v\nstdout:%s\nstderr:%s", result.err, result.stdout, result.stderr)
	}
	if got := strings.TrimSpace(result.stdout); got != "200" {
		return fmt.Errorf("%s served HTTP %s, want 200", rawURL, got)
	}
	return nil
}

func previewEnvForBranch(t *testing.T, e *evalCase, app string, branch string) string {
	t.Helper()
	command := "sudo -n /usr/local/bin/ship server app preview resolve " + app + " " + shellQuote(branch)
	out := e.mustRun(t, e.suite.repoRoot, nil, nil, e.sshBin(), "fake-vps", command)
	return strings.TrimSpace(out)
}

func forcePreviewExpired(t *testing.T, e *evalCase, app string, env string) {
	t.Helper()
	path := identity.IdentityFile(app, env)
	e.dockerExec(t, fmt.Sprintf(`python3 - <<'PY'
import json
path = %q
with open(path) as f:
    data = json.load(f)
data["preview"]["pinned"] = False
data["preview"]["expires_at"] = "2000-01-01T00:00:00Z"
with open(path, "w") as f:
    json.dump(data, f)
    f.write("\n")
PY`, path))
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`).MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

package fakevps

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	h "github.com/fprl/ship/tests/harness"
)

// Shared harness for the fake-VPS smoke tests. Owns binary build,
// Docker image build, container lifecycle, SSH wiring, and the small
// set of assert/run helpers each test file uses. No actual test
// functions live here.

const fakeVPSImage = "ship-fake-vps:local"

type smokeEnv struct {
	ctx        context.Context
	repoRoot   string
	image      string
	dockerfile string
	tmp        string
	shipHome   string
	binDir     string
	goBin      string
	linuxBin   string
	container  string
	pathPrefix string
}

type commandResult struct {
	stdout string
	stderr string
	err    error
}

func newSmokeEnv(t *testing.T, ctx context.Context) *smokeEnv {
	t.Helper()
	return newSmokeEnvWithImage(t, ctx, fakeVPSImage, "")
}

func newSmokeEnvWithImage(t *testing.T, ctx context.Context, image string, dockerfile string) *smokeEnv {
	t.Helper()
	repoRoot := h.RepoRootForTest(t)
	if dockerfile == "" {
		dockerfile = filepath.Join(repoRoot, "tests/fake-vps/Dockerfile")
	}
	tmp := t.TempDir()
	env := &smokeEnv{
		ctx:        ctx,
		repoRoot:   repoRoot,
		image:      image,
		dockerfile: dockerfile,
		tmp:        tmp,
		shipHome:   filepath.Join(tmp, "ship-home"),
		binDir:     filepath.Join(repoRoot, ".fake-vps-bin"),
		goBin:      filepath.Join(tmp, "ship"),
		linuxBin:   filepath.Join(repoRoot, ".fake-vps-bin", "ship-linux-amd64"),
	}
	t.Cleanup(func() {
		if os.Getenv("KEEP_FAKE_VPS") == "1" {
			t.Logf("keeping fake VPS container: %s", env.container)
			t.Logf("keeping fake VPS temp dir: %s", tmp)
			t.Logf("keeping fake VPS binary dir: %s", env.binDir)
			return
		}
		if env.container != "" {
			_ = exec.CommandContext(context.Background(), "docker", "rm", "-f", env.container).Run()
		}
		_ = os.RemoveAll(env.binDir)
	})
	return env
}

func (e *smokeEnv) buildBinaries(t *testing.T) {
	t.Helper()
	h.BuildBinaries(t, e.ctx, e.repoRoot, e.binDir, e.goBin, e.linuxBin)
}

func (e *smokeEnv) buildImage(t *testing.T) {
	t.Helper()
	h.BuildImage(t, e.ctx, e.repoRoot, e.dockerfile, e.image)
}

func (e *smokeEnv) startContainer(t *testing.T) {
	t.Helper()
	e.container = h.StartContainer(t, e.ctx, e.repoRoot, e.image)
}

func (e *smokeEnv) configureSSH(t *testing.T, user string) {
	t.Helper()
	e.pathPrefix = h.ConfigureSSH(t, e.ctx, e.repoRoot, e.tmp, e.container, user, "fake-vps-smoke")
}

func (e *smokeEnv) waitForSSH(t *testing.T) {
	t.Helper()
	h.WaitForSSH(t, e.ctx, e.repoRoot, e.sshBin())
}

func (e *smokeEnv) commitFixture(t *testing.T, appDir string) {
	t.Helper()
	e.mustRun(t, appDir, nil, "git", "init", "-q")
	e.mustRun(t, appDir, nil, "git", "config", "user.email", "smoke@example.com")
	e.mustRun(t, appDir, nil, "git", "config", "user.name", "Smoke")
	e.mustRun(t, appDir, nil, "git", "add", ".")
	e.mustRun(t, appDir, nil, "git", "commit", "-q", "-m", "fixture")
}

func (e *smokeEnv) ship(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	result := e.runShip(t, dir, stdin, args...)
	if result.err != nil {
		t.Fatalf("ship %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func (e *smokeEnv) runShip(t *testing.T, dir string, stdin []byte, args ...string) commandResult {
	t.Helper()
	return e.runCommand(t, dir, nil, stdin, e.goBin, args...)
}

func (e *smokeEnv) ssh(t *testing.T, command string) string {
	t.Helper()
	result := e.run(t, e.repoRoot, nil, e.sshBin(), "fake-vps", command)
	if result.err != nil {
		t.Fatalf("ssh fake-vps %q failed: %v\nstdout:\n%s\nstderr:\n%s", command, result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

// dockerExec runs a shell command inside the fake VPS container as
// root via `docker exec`. The smoke's normal SSH session lands as the
// `deploy` user, which only has passwordless sudo for
// /usr/local/bin/ship; fixture setup that has to call `podman`
// directly (creating the ingress network, starting the Caddy
// container, seeding /etc/caddy/Caddyfile) goes through this instead.
func (e *smokeEnv) dockerExec(t *testing.T, command string) string {
	t.Helper()
	result := e.run(t, e.repoRoot, nil, "docker", "exec", e.container, "bash", "-c", command)
	if result.err != nil {
		t.Fatalf("docker exec %q failed: %v\nstdout:\n%s\nstderr:\n%s", command, result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func (e *smokeEnv) assertRemoteBody(t *testing.T, command string, expected string) {
	t.Helper()
	got := strings.TrimSuffix(e.ssh(t, command), "\n")
	if got != expected {
		t.Fatalf("%s returned %q, want %q", command, got, expected)
	}
}

func (e *smokeEnv) urlBody(t *testing.T, rawURL, path string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL %q: %v", rawURL, err)
	}
	command := "curl -fsS -H " + h.ShellQuote("Host: "+parsed.Hostname()) + " " + h.ShellQuote("http://127.0.0.1"+path)
	return e.ssh(t, command)
}

func (e *smokeEnv) sshBin() string {
	if e.pathPrefix == "" {
		return "ssh"
	}
	return filepath.Join(e.pathPrefix, "ssh")
}

func (e *smokeEnv) configureSSHWithKey(t *testing.T, keyPath string) string {
	t.Helper()
	portOutput := strings.TrimSpace(e.mustRun(t, e.repoRoot, nil, "docker", "port", e.container, "22/tcp"))
	colon := strings.LastIndex(portOutput, ":")
	if colon == -1 || colon == len(portOutput)-1 {
		t.Fatalf("unexpected docker port output: %q", portOutput)
	}
	port := portOutput[colon+1:]

	homeSSH := filepath.Join(e.tmp, "teammate-home", ".ssh")
	if err := os.MkdirAll(homeSSH, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`Host fake-vps
  HostName 127.0.0.1
  Port %s
  User deploy
  IdentityFile %s
  IdentitiesOnly yes
  BatchMode yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
`, port, keyPath)
	if err := os.WriteFile(filepath.Join(homeSSH, "config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(e.tmp, "teammate-bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssh", "scp"} {
		hostBin, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		wrapper := fmt.Sprintf("#!/usr/bin/env bash\nexec %q -F %q \"$@\"\n", hostBin, filepath.Join(homeSSH, "config"))
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(wrapper), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return binDir
}

func (e *smokeEnv) mustRun(t *testing.T, dir string, extraEnv []string, name string, args ...string) string {
	t.Helper()
	result := e.run(t, dir, extraEnv, name, args...)
	if result.err != nil {
		t.Fatalf("%s %s failed: %v\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func (e *smokeEnv) mustRunWithStdin(t *testing.T, dir string, extraEnv []string, stdin []byte, name string, args ...string) string {
	t.Helper()
	result := e.runCommand(t, dir, extraEnv, stdin, name, args...)
	if result.err != nil {
		t.Fatalf("%s %s failed: %v\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func (e *smokeEnv) run(t *testing.T, dir string, extraEnv []string, name string, args ...string) commandResult {
	t.Helper()
	return e.runCommand(t, dir, extraEnv, nil, name, args...)
}

func (e *smokeEnv) runCommand(t *testing.T, dir string, extraEnv []string, stdin []byte, name string, args ...string) commandResult {
	t.Helper()
	cmd := exec.CommandContext(e.ctx, name, args...)
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

func (e *smokeEnv) commandEnv(extra []string) []string {
	env := os.Environ()
	env = h.SetEnv(env, "SHIP_HELPER_DIR", e.binDir)
	env = h.SetEnv(env, "HOME", e.shipHome)
	env = h.SetEnv(env, "USER", "fake-vps-smoke")
	env = h.SetEnv(env, "GIT_CONFIG_COUNT", "1")
	env = h.SetEnv(env, "GIT_CONFIG_KEY_0", "user.name")
	env = h.SetEnv(env, "GIT_CONFIG_VALUE_0", "fake-vps-smoke")
	if e.pathPrefix != "" {
		env = h.SetEnv(env, "PATH", e.pathPrefix+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	for _, item := range extra {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			env = append(env, item)
			continue
		}
		env = h.SetEnv(env, key, value)
	}
	return env
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}

func assertContains(t *testing.T, got string, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected output to contain %q\noutput:\n%s", want, got)
	}
}

func assertNotContains(t *testing.T, got string, want string) {
	t.Helper()
	if strings.Contains(got, want) {
		t.Fatalf("expected output not to contain %q\noutput:\n%s", want, got)
	}
}

func assertContainsInOrder(t *testing.T, got string, wants ...string) {
	t.Helper()
	offset := 0
	for _, want := range wants {
		index := strings.Index(got[offset:], want)
		if index < 0 {
			t.Fatalf("expected output to contain %q after byte %d\noutput:\n%s", want, offset, got)
		}
		offset += index + len(want)
	}
}

func fingerprintFromMemberMutation(t *testing.T, output string) string {
	t.Helper()
	output = strings.TrimSpace(output)
	_, tail, ok := strings.Cut(output, ", ")
	if !ok {
		t.Fatalf("member mutation output missing role/fingerprint tuple: %q", output)
	}
	fingerprint := strings.TrimSuffix(tail, ")")
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		t.Fatalf("member mutation output missing SHA256 fingerprint: %q", output)
	}
	return fingerprint
}

func assertEqual(t *testing.T, got string, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

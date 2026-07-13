package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
)

type CommandResult struct {
	Stdout string
	Stderr string
	Err    error
}

func RepoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	repoRoot, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return repoRoot
}

func BuildBinaries(t *testing.T, ctx context.Context, repoRoot, binDir, hostBin, linuxBin string) {
	t.Helper()
	if err := os.RemoveAll(binDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hostBin), 0755); err != nil {
		t.Fatal(err)
	}
	goCmd := os.Getenv("GO")
	if goCmd == "" {
		goCmd = "go"
	}
	MustRun(t, ctx, repoRoot, nil, nil, goCmd, "build", "-trimpath", "-o", hostBin, ".")
	linuxDir := filepath.Dir(linuxBin)
	MustRun(t, ctx, repoRoot, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, nil, goCmd, "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(linuxDir, "ship-linux-amd64"), ".")
	MustRun(t, ctx, repoRoot, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64"}, nil, goCmd, "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(linuxDir, "ship-linux-arm64"), ".")
}

func BuildImage(t *testing.T, ctx context.Context, repoRoot, dockerfile, image string) {
	t.Helper()
	MustRun(t, ctx, repoRoot, nil, nil, "docker", "build", "-f", dockerfile, "-t", image, repoRoot)
}

func StartContainer(t *testing.T, ctx context.Context, repoRoot, image string) string {
	t.Helper()
	out := MustRun(t, ctx, repoRoot, nil, nil, "docker", "run", "-d", "-p", "127.0.0.1::22", image)
	container := strings.TrimSpace(out)
	if container == "" {
		t.Fatal("docker run returned empty container id")
	}
	return container
}

func ConfigureSSH(t *testing.T, ctx context.Context, repoRoot, tmp, container, user, comment string) string {
	t.Helper()
	keyPath := filepath.Join(tmp, "id_ed25519")
	MustRun(t, ctx, repoRoot, nil, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", keyPath)

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
	MustRun(t, ctx, repoRoot, nil, pub, "docker", "exec", "-i", container, "bash", "-lc", authorize)

	portOutput := strings.TrimSpace(MustRun(t, ctx, repoRoot, nil, nil, "docker", "port", container, "22/tcp"))
	colon := strings.LastIndex(portOutput, ":")
	if colon == -1 || colon == len(portOutput)-1 {
		t.Fatalf("unexpected docker port output: %q", portOutput)
	}
	port := portOutput[colon+1:]

	homeSSH := filepath.Join(tmp, "home", ".ssh")
	if err := os.MkdirAll(homeSSH, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`Host fake-vps
  HostName 127.0.0.1
  Port %s
  HostKeyAlias fake-vps
  User %s
  IdentityFile %s
  IdentitiesOnly yes
  BatchMode yes
  LogLevel ERROR
`, port, user, keyPath)
	if err := os.WriteFile(filepath.Join(homeSSH, "config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssh", "scp"} {
		hostBin, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		wrapper := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

known_hosts=0
strict_host_key_checking=0
for arg in "$@"; do
  case "$arg" in
    UserKnownHostsFile=*) known_hosts=1 ;;
    StrictHostKeyChecking=*) strict_host_key_checking=1 ;;
  esac
done

args=(-F %q)
if (( ! known_hosts )); then
  args+=(-o UserKnownHostsFile=/dev/null)
fi
if (( ! strict_host_key_checking )); then
  args+=(-o StrictHostKeyChecking=no)
fi
exec %q "${args[@]}" "$@"
`, filepath.Join(homeSSH, "config"), hostBin)
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(wrapper), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return binDir
}

func WaitForSSH(t *testing.T, ctx context.Context, repoRoot, sshBin string) {
	t.Helper()
	var last CommandResult
	for i := 0; i < 30; i++ {
		last = Run(ctx, repoRoot, nil, nil, sshBin, "fake-vps", "true")
		if last.Err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("fake VPS ssh did not become ready\nstdout:\n%s\nstderr:\n%s\nerr: %v", last.Stdout, last.Stderr, last.Err)
}

func MustRun(t *testing.T, ctx context.Context, dir string, extraEnv []string, stdin []byte, name string, args ...string) string {
	t.Helper()
	result := Run(ctx, dir, extraEnv, stdin, name, args...)
	if result.Err != nil {
		t.Fatalf("%s %s failed: %v\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), result.Err, result.Stdout, result.Stderr)
	}
	return result.Stdout
}

func Run(ctx context.Context, dir string, extraEnv []string, stdin []byte, name string, args ...string) CommandResult {
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
	return CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
}

func SetEnv(env []string, key string, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func PreviewEnvForBranch(t *testing.T, ssh func(string) string, app, branch string) string {
	t.Helper()
	out := ssh("sudo -n /usr/local/bin/ship server app preview resolve " + app + " " + ShellQuote(branch))
	return strings.TrimSpace(out)
}

func ForcePreviewExpired(t *testing.T, dockerExec func(string) string, app, env string) {
	t.Helper()
	SetPreviewExpiry(t, dockerExec, app, env, "2000-01-01T00:00:00Z")
}

func SetPreviewExpiry(t *testing.T, dockerExec func(string) string, app, env, expiresAt string) {
	t.Helper()
	path := identity.IdentityFile(app, env)
	dockerExec(fmt.Sprintf(`python3 - <<'PY'
import json
path = %q
with open(path) as f:
    data = json.load(f)
data["preview"]["pinned"] = False
data["preview"]["expires_at"] = %q
with open(path, "w") as f:
    json.dump(data, f)
    f.write("\n")
PY`, path, expiresAt))
}

func AssertURLServes200(t *testing.T, ssh func(string) string, rawURL string) {
	t.Helper()
	if err := URLServes200(func(command string) CommandResult {
		return CommandResult{Stdout: ssh(command)}
	}, rawURL); err != nil {
		t.Fatal(err)
	}
}

func URLServes200(runShell func(command string) CommandResult, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse URL %q: %v", rawURL, err)
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	headers := "-H " + ShellQuote("Host: "+parsed.Hostname())
	if token := parsed.Query().Get("ship"); token != "" {
		headers += " -H " + ShellQuote("x-ship-capability: "+token)
	}
	command := fmt.Sprintf("curl -fsS -o /dev/null -w '%%{http_code}' %s %s",
		headers,
		ShellQuote("http://127.0.0.1"+path),
	)
	result := runShell(command)
	if result.Err != nil {
		return fmt.Errorf("curl through fake Caddy failed: %v\nstdout:%s\nstderr:%s", result.Err, result.Stdout, result.Stderr)
	}
	if got := strings.TrimSpace(result.Stdout); got != "200" {
		return fmt.Errorf("%s served HTTP %s, want 200", rawURL, got)
	}
	return nil
}

func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func SeedHostJSON() string {
	data, err := json.Marshal(store.HostFile{
		Version: store.CurrentVersion,
		Desired: store.HostDesired{
			Users:    store.HostUsers{Operator: "operator", Deploy: "deploy"},
			Ingress:  store.HostIngressDesired{Expose: store.ExposePublic},
			Features: store.HostFeatures{},
			Packages: map[string]store.DesiredPackage{
				"podman": {Source: "apt"},
				"rsync":  {Source: "apt"},
				"caddy":  {Source: "container"},
			},
		},
		Observed: store.HostObserved{
			Packages: map[string]store.ObservedPackage{},
			Ingress:  store.HostIngressObserved{},
		},
		Meta: store.HostMeta{},
	})
	if err != nil {
		panic(err)
	}
	return string(data) + "\n"
}

package fakevps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const fakeVPSImage = "simple-vps-fake-vps:local"

type smokeEnv struct {
	ctx        context.Context
	repoRoot   string
	image      string
	dockerfile string
	tmp        string
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

func TestSmoke(t *testing.T) {
	if os.Getenv("SIMPLE_VPS_RUN_FAKE_VPS_SMOKE") != "1" {
		t.Skip("set SIMPLE_VPS_RUN_FAKE_VPS_SMOKE=1 to run Docker-backed fake VPS smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)

	env := newSmokeEnv(t, ctx)
	env.buildBinaries(t)
	env.buildImage(t)
	env.startContainer(t)
	env.configureSSH(t, "deploy")
	env.waitForSSH(t)

	t.Run("node app deploys routes secrets rollbacks and rejects unhealthy release", env.testNodeAppLifecycle)
	t.Run("bun app installs dependencies and serves through route", env.testBunApp)
	t.Run("static app publishes static route", env.testStaticApp)
	t.Run("build output app preserves includes and destroy removes routes", env.testBuildOutputApp)
	t.Run("install false bundle deploys without source and purge removes app", env.testInstallFalseBundle)
}

func newSmokeEnv(t *testing.T, ctx context.Context) *smokeEnv {
	t.Helper()
	return newSmokeEnvWithImage(t, ctx, fakeVPSImage, "")
}

func newSmokeEnvWithImage(t *testing.T, ctx context.Context, image string, dockerfile string) *smokeEnv {
	t.Helper()
	repoRoot := repoRootForTest(t)
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
		binDir:     filepath.Join(repoRoot, ".fake-vps-bin"),
		goBin:      filepath.Join(tmp, "simple-vps"),
		linuxBin:   filepath.Join(repoRoot, ".fake-vps-bin", "simple-vps-linux-amd64"),
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

func repoRootForTest(t *testing.T) string {
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

func (e *smokeEnv) buildBinaries(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(e.binDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.binDir, 0755); err != nil {
		t.Fatal(err)
	}
	goCmd := os.Getenv("GO")
	if goCmd == "" {
		goCmd = "go"
	}
	e.mustRun(t, e.repoRoot, nil, goCmd, "build", "-trimpath", "-o", e.goBin, ".")
	e.mustRun(t, e.repoRoot, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, goCmd, "build", "-trimpath", "-ldflags=-s -w", "-o", e.linuxBin, ".")
}

func (e *smokeEnv) buildImage(t *testing.T) {
	t.Helper()
	e.mustRun(t, e.repoRoot, nil, "docker", "build", "-f", e.dockerfile, "-t", e.image, e.repoRoot)
}

func (e *smokeEnv) startContainer(t *testing.T) {
	t.Helper()
	out := e.mustRun(t, e.repoRoot, nil, "docker", "run", "-d", "-p", "127.0.0.1::22", e.image)
	e.container = strings.TrimSpace(out)
	if e.container == "" {
		t.Fatal("docker run returned empty container id")
	}
}

func (e *smokeEnv) configureSSH(t *testing.T, user string) {
	t.Helper()
	keyPath := filepath.Join(e.tmp, "id_ed25519")
	e.mustRun(t, e.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath)

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
	e.mustRunWithStdin(t, e.repoRoot, nil, pub, "docker", "exec", "-i", e.container, "bash", "-lc", authorize)

	portOutput := strings.TrimSpace(e.mustRun(t, e.repoRoot, nil, "docker", "port", e.container, "22/tcp"))
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
	binDir := filepath.Join(e.tmp, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	wrapper := fmt.Sprintf("#!/usr/bin/env bash\nexec %q -F %q \"$@\"\n", hostSSH, filepath.Join(homeSSH, "config"))
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	hostSCP, err := exec.LookPath("scp")
	if err != nil {
		t.Fatal(err)
	}
	scpWrapper := fmt.Sprintf("#!/usr/bin/env bash\nexec %q -F %q \"$@\"\n", hostSCP, filepath.Join(homeSSH, "config"))
	if err := os.WriteFile(filepath.Join(binDir, "scp"), []byte(scpWrapper), 0755); err != nil {
		t.Fatal(err)
	}
	e.pathPrefix = binDir
}

func (e *smokeEnv) waitForSSH(t *testing.T) {
	t.Helper()
	var last commandResult
	for i := 0; i < 30; i++ {
		last = e.run(t, e.repoRoot, nil, e.sshBin(), "fake-vps", "true")
		if last.err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("fake VPS ssh did not become ready\nstdout:\n%s\nstderr:\n%s\nerr: %v", last.stdout, last.stderr, last.err)
}

func (e *smokeEnv) testNodeAppLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "mode-a")
	mustMkdir(t, app)
	writeNodePackage(t, app, "api")
	writeServer(t, app, "mode-a")
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "api"

[env.production]
server = "fake-vps"
runtime = "node"

[services.web]
command = "node server.js"
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
`)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil, "setup", "production")
	e.simpleVPS(t, app, nil, "deploy", "production")

	firstCurrent := strings.TrimSpace(e.ssh(t, "readlink -f /var/apps/api/current"))
	e.ssh(t, "test -L /var/apps/api/current")
	e.ssh(t, "test -L /var/apps/api/current/db")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/health", "ok")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/", "mode-a")
	routes := e.ssh(t, "sudo simple-vps server route list --json")
	assertContains(t, routes, `"host": "api.example.com"`)
	assertContains(t, routes, `"service": "web"`)
	assertContains(t, e.simpleVPS(t, app, nil, "status", "production"), "service web: active")
	assertContains(t, e.simpleVPS(t, app, nil, "logs", "production", "web"), "server:mode-a")

	mustWrite(t, filepath.Join(app, "production.env"), "API_KEY=from-env\n")
	e.simpleVPS(t, app, nil, "env", "push", "production", "production.env")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/secret", "")
	e.simpleVPS(t, app, nil, "restart", "production", "web")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/secret", "from-env")

	e.simpleVPS(t, app, []byte("from-secret\n"), "secret", "put", "production", "API_KEY")
	secretList := e.simpleVPS(t, app, nil, "secret", "list", "production")
	assertContains(t, secretList, "API_KEY")
	assertNotContains(t, secretList, "from-secret")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/secret", "from-env")
	e.simpleVPS(t, app, nil, "restart", "production", "web")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/secret", "from-secret")
	e.simpleVPS(t, app, nil, "secret", "rm", "production", "API_KEY")
	e.simpleVPS(t, app, nil, "restart", "production", "web")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/secret", "")
	mustRemove(t, filepath.Join(app, "production.env"))

	writeServer(t, app, "mode-a-v2")
	e.mustRun(t, app, nil, "git", "add", "server.js")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "second fixture")
	e.simpleVPS(t, app, nil, "deploy", "production")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/", "mode-a-v2")
	e.simpleVPS(t, app, nil, "rollback", "production")
	assertEqual(t, strings.TrimSpace(e.ssh(t, "readlink -f /var/apps/api/current")), firstCurrent)
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/", "mode-a")

	writeUnhealthyServer(t, app, "mode-a-bad")
	e.mustRun(t, app, nil, "git", "add", "server.js")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "bad fixture")
	result := e.runSimpleVPS(t, app, nil, "deploy", "production")
	if result.err == nil {
		t.Fatal("unhealthy deploy unexpectedly passed")
	}
	assertEqual(t, strings.TrimSpace(e.ssh(t, "readlink -f /var/apps/api/current")), firstCurrent)
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3000/", "mode-a")
}

func (e *smokeEnv) testBunApp(t *testing.T) {
	app := filepath.Join(e.tmp, "hono-api")
	mustMkdir(t, app)
	writeHonoBunApp(t, e, app)
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "hono-api"

[env.production]
server = "fake-vps"
runtime = "bun"

[services.web]
command = "bun run src/server.ts"
port = 3003
healthcheck = "/health"

[routes.app]
host = "hono.example.com"
type = "proxy"
service = "web"
`)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil, "setup", "production")
	e.simpleVPS(t, app, nil, "deploy", "production")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3003/health", "ok")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3003/", "hono-api")
	e.ssh(t, "test -d /var/apps/hono-api/current/node_modules/hono")
	e.assertRouteBody(t, "hono.example.com", "/", "hono-api")
}

func (e *smokeEnv) testStaticApp(t *testing.T) {
	app := filepath.Join(e.tmp, "static-site")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "index.html"), `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <title>Static Smoke</title>
  </head>
  <body>
    <h1>static-site</h1>
  </body>
</html>
`)
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "static-site"

[env.production]
server = "fake-vps"
runtime = "static"

[routes.site]
host = "static.example.com"
type = "static"
`)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil, "setup", "production")
	e.simpleVPS(t, app, nil, "deploy", "production")
	e.ssh(t, "grep -q '<h1>static-site</h1>' /var/apps/static-site/current/index.html")
	e.assertRouteContains(t, "static.example.com", "/", "<h1>static-site</h1>")
}

func (e *smokeEnv) testBuildOutputApp(t *testing.T) {
	app := filepath.Join(e.tmp, "mode-b")
	mustMkdir(t, filepath.Join(app, "public"))
	writeNodePackage(t, app, "web")
	writeServer(t, app, "mode-b")
	mustWrite(t, filepath.Join(app, "public", "asset.txt"), "asset\n")
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "web"

[build]
command = "mkdir -p dist && cp server.js dist/server.js"
output = "dist"
include = ["public"]

[env.production]
server = "fake-vps"
runtime = "node"

[services.web]
command = "node server.js"
port = 3001
healthcheck = "/health"

[routes.app]
host = "web.example.com"
type = "proxy"
service = "web"
`)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil, "setup", "production")
	e.simpleVPS(t, app, nil, "deploy", "production")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3001/health", "ok")
	e.ssh(t, "grep -q '^asset$' /var/apps/web/current/public/asset.txt")
	e.ssh(t, "test -f /var/apps/web/current/package-lock.json")
	e.ssh(t, "test ! -e /var/apps/web/current/simple-vps.toml")
	assertContains(t, e.ssh(t, "sudo simple-vps server route list --json"), `"host": "web.example.com"`)
	e.simpleVPS(t, app, nil, "destroy", "production", "--yes")
	e.ssh(t, "test -d /var/apps/web/shared")
	e.ssh(t, "test -d /var/apps/web/releases")
	e.ssh(t, "test ! -e /var/apps/web/current")
	assertNotContains(t, e.ssh(t, "sudo simple-vps server route list --json"), `"app": "web"`)
}

func (e *smokeEnv) testInstallFalseBundle(t *testing.T) {
	app := filepath.Join(e.tmp, "mode-c")
	mustMkdir(t, app)
	writeServer(t, app, "mode-c")
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "bundle"

[build]
command = "mkdir -p dist && cp server.js dist/server.js"
output = "dist"
install = false

[env.production]
server = "fake-vps"
runtime = "node"

[services.web]
command = "node server.js"
port = 3002
healthcheck = "/health"

[routes.app]
host = "bundle.example.com"
type = "proxy"
service = "web"
`)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil, "setup", "production")
	e.simpleVPS(t, app, nil, "deploy", "production")
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:3002/health", "ok")
	e.ssh(t, "test ! -e /var/apps/bundle/current/package.json")
	e.ssh(t, "test ! -e /var/apps/bundle/current/simple-vps.toml")
	assertContains(t, e.ssh(t, "sudo simple-vps server route list --json"), `"host": "bundle.example.com"`)
	e.simpleVPS(t, app, nil, "destroy", "production", "--purge", "--yes", "--confirm", "bundle")
	e.ssh(t, "test ! -e /var/apps/bundle")
	assertNotContains(t, e.ssh(t, "sudo simple-vps server route list --json"), `"app": "bundle"`)
}

func (e *smokeEnv) commitFixture(t *testing.T, appDir string) {
	t.Helper()
	e.mustRun(t, appDir, nil, "git", "init", "-q")
	e.mustRun(t, appDir, nil, "git", "config", "user.email", "smoke@example.com")
	e.mustRun(t, appDir, nil, "git", "config", "user.name", "Smoke")
	e.mustRun(t, appDir, nil, "git", "add", ".")
	e.mustRun(t, appDir, nil, "git", "commit", "-q", "-m", "fixture")
}

func (e *smokeEnv) simpleVPS(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	result := e.runSimpleVPS(t, dir, stdin, args...)
	if result.err != nil {
		t.Fatalf("simple-vps %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func (e *smokeEnv) runSimpleVPS(t *testing.T, dir string, stdin []byte, args ...string) commandResult {
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

func (e *smokeEnv) assertRemoteBody(t *testing.T, command string, expected string) {
	t.Helper()
	got := strings.TrimSuffix(e.ssh(t, command), "\n")
	if got != expected {
		t.Fatalf("%s returned %q, want %q", command, got, expected)
	}
}

func (e *smokeEnv) assertRouteBody(t *testing.T, host string, path string, expected string) {
	t.Helper()
	e.assertRoute(t, host, path, func(body string) bool { return body == expected }, fmt.Sprintf("equal %q", expected))
}

func (e *smokeEnv) assertRouteContains(t *testing.T, host string, path string, expected string) {
	t.Helper()
	e.assertRoute(t, host, path, func(body string) bool { return strings.Contains(body, expected) }, fmt.Sprintf("contain %q", expected))
}

func (e *smokeEnv) assertRoute(t *testing.T, host string, path string, accept func(string) bool, expectation string) {
	t.Helper()
	command := fmt.Sprintf("curl -fsS -H 'Host: %s' 'http://127.0.0.1:8080%s'", host, path)
	var last commandResult
	for i := 0; i < 20; i++ {
		last = e.run(t, e.repoRoot, nil, e.sshBin(), "fake-vps", command)
		if last.err == nil && accept(last.stdout) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("route %s%s did not %s\nlast stdout:\n%s\nlast stderr:\n%s\nlast err: %v", host, path, expectation, last.stdout, last.stderr, last.err)
}

func (e *smokeEnv) sshBin() string {
	if e.pathPrefix == "" {
		return "ssh"
	}
	return filepath.Join(e.pathPrefix, "ssh")
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
	if e.pathPrefix != "" {
		env = setEnv(env, "PATH", e.pathPrefix+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
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

func writeNodePackage(t *testing.T, appDir string, name string) {
	t.Helper()
	mustWrite(t, filepath.Join(appDir, "package.json"), fmt.Sprintf(`{
  "name": %q,
  "version": "1.0.0",
  "scripts": {
    "start": "node server.js"
  }
}
`, name))
	mustWrite(t, filepath.Join(appDir, "package-lock.json"), fmt.Sprintf(`{
  "name": %q,
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": %q,
      "version": "1.0.0"
    }
  }
}
`, name, name))
}

func writeServer(t *testing.T, appDir string, body string) {
	t.Helper()
	mustWrite(t, filepath.Join(appDir, "server.js"), fmt.Sprintf(`const http = require("http");
const port = Number(process.env.PORT || 3000);
console.log("server:%s");
http.createServer((req, res) => {
  if (req.url === "/health") {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("ok");
    return;
  }
  if (req.url === "/secret") {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end(process.env.API_KEY || "");
    return;
  }
  res.writeHead(200, { "content-type": "text/plain" });
  res.end(%q);
}).listen(port, "127.0.0.1");
`, body, body))
}

func writeUnhealthyServer(t *testing.T, appDir string, body string) {
	t.Helper()
	mustWrite(t, filepath.Join(appDir, "server.js"), fmt.Sprintf(`const http = require("http");
const port = Number(process.env.PORT || 3000);
console.log("server:%s");
http.createServer((req, res) => {
  if (req.url === "/health") {
    res.writeHead(500, { "content-type": "text/plain" });
    res.end("bad");
    return;
  }
  res.writeHead(200, { "content-type": "text/plain" });
  res.end(%q);
}).listen(port, "127.0.0.1");
`, body, body))
}

func writeHonoBunApp(t *testing.T, env *smokeEnv, appDir string) {
	t.Helper()
	mustMkdir(t, filepath.Join(appDir, "src"))
	mustWrite(t, filepath.Join(appDir, "package.json"), `{
  "name": "hono-api",
  "version": "1.0.0",
  "type": "module",
  "scripts": {
    "start": "bun run src/server.ts"
  },
  "dependencies": {
    "hono": "4.12.19"
  }
}
`)
	mustWrite(t, filepath.Join(appDir, "src", "server.ts"), `import { Hono } from "hono";

const app = new Hono();

app.get("/", (c) => c.text("hono-api"));
app.get("/health", (c) => c.text("ok"));
app.get("/env", (c) => c.text(Bun.env.GREETING ?? ""));

Bun.serve({
  hostname: "127.0.0.1",
  port: Number(Bun.env.PORT || 3003),
  fetch: app.fetch,
});

console.log("server:hono-api");
`)
	env.mustRun(t, appDir, nil, "bun", "install", "--lockfile-only")
	mustRemoveAll(t, filepath.Join(appDir, "node_modules"))
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

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func mustRemoveAll(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

func assertContains(t *testing.T, got string, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected output to contain %q\noutput:\n%s", want, got)
	}
}

func assertNotContains(t *testing.T, got string, forbidden string) {
	t.Helper()
	if strings.Contains(got, forbidden) {
		t.Fatalf("expected output not to contain %q\noutput:\n%s", forbidden, got)
	}
}

func assertEqual(t *testing.T, got string, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

package fakevps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestContainerSmoke exercises the new container-deploy lifecycle (ADR-0005
// + ADR-0006 Cut 2) end-to-end against the fake-vps fixture:
//
//   - `simple-vps setup production` calls `server app setup-env`, which
//     creates the per-env Linux user, on-disk layout under
//     /var/apps/<app>/<env>/, and the per-(app, env) Podman network.
//   - `simple-vps deploy production` tars the working tree, uploads the
//     manifest, calls `server app apply`, which runs `podman build` +
//     `podman run` (initial §7 hardening subset) without any host-port
//     publish — the app container joins both the per-(app, env) network
//     and the shared `ingress` network. The helper writes a per-app
//     Caddyfile fragment that reverse-proxies via container DNS, then
//     reloads Caddy via `podman exec caddy caddy reload`.
//   - End-to-end: `curl -H 'Host: api.example.com' http://127.0.0.1/health`
//     reaches the app container through the fake Caddy proxy.
func TestContainerSmoke(t *testing.T) {
	if os.Getenv("SIMPLE_VPS_RUN_FAKE_VPS_SMOKE") != "1" {
		t.Skip("set SIMPLE_VPS_RUN_FAKE_VPS_SMOKE=1 to run Docker-backed fake VPS smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	env := newSmokeEnv(t, ctx)
	env.buildBinaries(t)
	env.buildImage(t)
	env.startContainer(t)
	env.configureSSH(t, "deploy")
	env.waitForSSH(t)

	t.Run("container app reaches setup + deploy + caddy proxy", env.testContainerAppLifecycle)
	t.Run("@secret refs resolve through put/list/rm into the runtime env", env.testSecretLifecycle)
	t.Run("status + logs surface deployed services without SSHing in", env.testStatusAndLogs)
}

func (e *smokeEnv) testContainerAppLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	mustMkdir(t, app)
	writeContainerFixture(t, app)
	// Deploy needs a git tree (release id = git short SHA). Commit to
	// stay on the canonical clean-tree path.
	e.commitFixture(t, app)

	// The shared `ingress` Podman network and the host-side Caddy
	// container would normally come from `simple-vps host install`.
	// The smoke skips the installer, so seed both here the same way
	// the provisioner does. These need root and the deploy user only
	// has passwordless sudo for /usr/local/bin/simple-vps — use
	// docker exec instead.
	e.dockerExec(t, "mkdir -p /etc/caddy/simple-vps /etc/caddy/conf.d /var/lib/caddy")
	e.dockerExec(t, `cat > /etc/caddy/Caddyfile <<'EOF'
import simple-vps/*.caddy
import conf.d/*.caddy
EOF`)
	e.dockerExec(t, "podman network create ingress")
	e.dockerExec(t, "podman run -d --name caddy --network ingress --publish 80:80 -v /etc/caddy:/etc/caddy:Z docker.io/library/caddy:2-alpine")

	// 1. Setup creates the per-env user, paths, and per-(app, env) network.
	e.simpleVPS(t, app, nil, "setup", "production")

	e.ssh(t, "getent passwd app-api-production >/dev/null")
	e.ssh(t, "test -d /var/apps/api/production/shared")
	e.ssh(t, "test -f /run/fake-podman/networks/app-api-production")

	// 2. Deploy on a clean tree.
	e.simpleVPS(t, app, nil, "deploy", "production")

	// 3. fake-podman should have logged build + run for the web service.
	commandsLog := e.ssh(t, "cat /run/fake-podman/commands.log")
	assertContains(t, commandsLog, "podman build")
	assertContains(t, commandsLog, "podman run")
	assertContains(t, commandsLog, "--name app-api-production-web")
	assertContains(t, commandsLog, "--user ") // numeric uid:gid
	assertContains(t, commandsLog, "--read-only")
	assertContains(t, commandsLog, "--tmpfs /tmp:size=64m,mode=1777")
	assertContains(t, commandsLog, "--cap-drop ALL")
	assertContains(t, commandsLog, "--security-opt no-new-privileges")
	assertContains(t, commandsLog, "--network app-api-production")
	assertContains(t, commandsLog, "--network ingress")

	// 4. App container must NOT carry the host-port label (that path is
	// gone with Caddy-in-container) and the run line must NOT carry a
	// --publish (no host loopback ingress).
	labels := e.ssh(t, "cat /run/fake-podman/containers/app-api-production-web.labels")
	assertContains(t, labels, "app=api")
	assertContains(t, labels, "env=production")
	assertContains(t, labels, "service=web")
	if strings.Contains(labels, "simple_vps_host_port=") {
		t.Fatalf("simple_vps_host_port label leaked into Caddy-in-container flow:\n%s", labels)
	}
	if strings.Contains(commandsLog, "--publish 127.0.0.1:") {
		t.Fatalf("app container still publishes a host loopback port; Caddy-in-container should drop this:\n%s", commandsLog)
	}

	// 5. Caddy fragment should reverse-proxy via container DNS, not
	// 127.0.0.1.
	fragment := e.ssh(t, "cat /etc/caddy/conf.d/simple-vps-api-production.caddy")
	assertContains(t, fragment, `"api.example.com" {`)
	assertContains(t, fragment, "reverse_proxy http://app-api-production-web:3000")
	if strings.Contains(fragment, "127.0.0.1") {
		t.Fatalf("Caddy fragment still uses host loopback; should be container DNS:\n%s", fragment)
	}

	// 6. Helper should reload Caddy by execing into the container.
	assertContains(t, commandsLog, "podman exec caddy caddy reload --config /etc/caddy/Caddyfile")

	// 7. End-to-end: curl through the fake Caddy with Host header reaches
	// the app container. This is the assertion the host-port path could
	// never make honestly — it proves the actual routing path the user
	// sees in production works.
	e.assertRemoteBody(t, "curl -fsS -H 'Host: api.example.com' http://127.0.0.1/health", "ok")

	// 8. A second deploy on the same source must produce a byte-identical
	// fragment (no churn from non-deterministic upstreams or ports).
	firstFragment := fragment
	e.simpleVPS(t, app, nil, "deploy", "production")
	secondFragment := e.ssh(t, "cat /etc/caddy/conf.d/simple-vps-api-production.caddy")
	if firstFragment != secondFragment {
		t.Fatalf("expected stable fragment across deploys, got:\nfirst:\n%s\nsecond:\n%s", firstFragment, secondFragment)
	}
}

// testSecretLifecycle covers the @secret:KEY resolution path end-to-end
// against the helper-side store under /etc/simple-vps/secrets/:
//
//  1. setup the app/env baseline
//  2. `simple-vps secret put` over SSH-stdin (value never on argv)
//  3. `simple-vps secret list` shows the key (NOT the value)
//  4. deploy a manifest that references @secret:db_url
//  5. the on-host env file contains the literal env values AND the
//     resolved secret, with mode 0600 owned by the per-env user
//  6. `simple-vps secret rm` removes the key
//  7. a subsequent deploy with the ref still present fails fast at
//     `app apply` with the unresolved-ref error
func (e *smokeEnv) testSecretLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "container-secrets")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "sec"

[env.production]
server = "fake-vps"

[env.production.env]
LOG_LEVEL = "info"
DATABASE_URL = "@secret:db_url"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "sec.example.com"
type = "proxy"
service = "web"
`)
	e.commitFixture(t, app)
	e.simpleVPS(t, app, nil, "setup", "production")

	// 1. put: value over stdin, never argv. The fake-VPS harness uses
	// docker exec ssh under the hood and the helper reads from its
	// own stdin in `secret put`.
	e.simpleVPS(t, app, []byte("postgres://verybadidea"), "secret", "put", "production", "db_url")

	// 2. file lands at the expected path, root-owned, mode 0600,
	// containing the value verbatim (no trailing newline — the client
	// trims one if present). Read as root via dockerExec because the
	// deploy user only has passwordless sudo for /usr/local/bin/simple-vps.
	listing := strings.TrimSpace(e.dockerExec(t, "ls -l /etc/simple-vps/secrets/sec/production"))
	if !strings.Contains(listing, " db_url") {
		t.Fatalf("expected db_url in /etc/simple-vps/secrets/sec/production listing:\n%s", listing)
	}
	if !strings.Contains(listing, "-rw-------") {
		t.Fatalf("secret file is not mode 0600:\n%s", listing)
	}
	body := strings.TrimSuffix(e.dockerExec(t, "cat /etc/simple-vps/secrets/sec/production/db_url"), "\n")
	if body != "postgres://verybadidea" {
		t.Fatalf("secret value didn't round-trip:\nwant: postgres://verybadidea\n got: %q", body)
	}

	// 3. list shows the key — NEVER the value.
	listing = e.simpleVPS(t, app, nil, "secret", "list", "production")
	if !strings.Contains(listing, "db_url") {
		t.Fatalf("secret list missing db_url:\n%s", listing)
	}
	if strings.Contains(listing, "postgres://") {
		t.Fatalf("secret list leaked the value:\n%s", listing)
	}

	// 4. deploy: helper resolves @secret:db_url into the env file
	// next to the literal LOG_LEVEL.
	e.simpleVPS(t, app, nil, "deploy", "production")
	envFile := e.dockerExec(t, "cat /var/apps/sec/production/shared/.env")
	if !strings.Contains(envFile, "LOG_LEVEL=info\n") {
		t.Fatalf("env file missing literal LOG_LEVEL:\n%s", envFile)
	}
	if !strings.Contains(envFile, "DATABASE_URL=postgres://verybadidea\n") {
		t.Fatalf("env file missing resolved DATABASE_URL:\n%s", envFile)
	}

	// 5. env file mode + ownership: 0600 owned by the per-env user.
	envStat := strings.TrimSpace(e.dockerExec(t, "stat -c '%a %U' /var/apps/sec/production/shared/.env"))
	if envStat != "600 app-sec-production" {
		t.Fatalf("env file perms wrong: %q (want `600 app-sec-production`)", envStat)
	}

	// 6. rm removes the key.
	e.simpleVPS(t, app, nil, "secret", "rm", "production", "db_url")
	if missing := strings.TrimSpace(e.dockerExec(t, "ls /etc/simple-vps/secrets/sec/production")); missing != "" {
		t.Fatalf("expected empty secret dir after rm, got:\n%s", missing)
	}

	// 7. next deploy with the ref still in the manifest fails fast.
	result := e.runSimpleVPS(t, app, nil, "deploy", "production")
	if result.err == nil {
		t.Fatal("expected deploy to fail with unresolved @secret reference")
	}
	if !strings.Contains(result.stderr+result.stdout, "DATABASE_URL") || !strings.Contains(result.stderr+result.stdout, "db_url") {
		t.Fatalf("unresolved-ref error must name the env var and the secret key, got:\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
	}
}

// testStatusAndLogs covers the read-only operator surface introduced
// in PR #38. Assumes the earlier subtests have already deployed the
// `api` container app and left its `web` service running.
func (e *smokeEnv) testStatusAndLogs(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")

	// Text status surfaces the web service, its container, and the
	// simple_vps_release label baked in by `app apply`.
	text := e.simpleVPS(t, app, nil, "status", "production")
	assertContains(t, text, "api (production)")
	assertContains(t, text, "web")
	assertContains(t, text, "running")
	assertContains(t, text, "release=")
	if strings.Contains(text, "no services running") {
		t.Fatalf("status reported `no services running` after a successful deploy:\n%s", text)
	}

	// JSON status carries the same data in a structured shape.
	// Parse it back to prove the contract — text-mode regressions
	// might still slip through a substring check.
	rawJSON := e.simpleVPS(t, app, nil, "status", "--json", "production")
	var payload struct {
		App      string `json:"app"`
		Env      string `json:"env"`
		Services []struct {
			Service   string `json:"service"`
			Container string `json:"container"`
			State     string `json:"state"`
			Release   string `json:"release"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		t.Fatalf("status --json output not parseable as JSON: %v\nraw:\n%s", err, rawJSON)
	}
	if payload.App != "api" || payload.Env != "production" {
		t.Fatalf("status --json mis-identifies the app: %+v", payload)
	}
	if len(payload.Services) != 1 || payload.Services[0].Service != "web" {
		t.Fatalf("expected one service `web`, got: %+v", payload.Services)
	}
	if payload.Services[0].Container != "app-api-production-web" {
		t.Fatalf("unexpected container name: %+v", payload.Services[0])
	}
	if payload.Services[0].Release == "" {
		t.Fatalf("status --json missing simple_vps_release label: %+v", payload.Services[0])
	}

	// Logs reaches `podman logs` on the right container and prints
	// the deterministic stub line.
	logs := e.simpleVPS(t, app, nil, "logs", "production", "web")
	assertContains(t, logs, "fake podman logs for app-api-production-web")

	// Service argument is required when... actually our fixture only
	// has one service, so omitting it should work too. Cover that.
	logsNoSvc := e.simpleVPS(t, app, nil, "logs", "production")
	assertContains(t, logsNoSvc, "fake podman logs for app-api-production-web")

	// Unknown service errors clearly.
	missing := e.runSimpleVPS(t, app, nil, "logs", "production", "nope")
	if missing.err == nil {
		t.Fatal("expected logs to fail when service is unknown")
	}
	if !strings.Contains(missing.stderr, "nope") {
		t.Fatalf("error should name the missing service, got: %s", missing.stderr)
	}
}

func writeContainerFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "api"

[env.production]
server = "fake-vps"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
`)
}

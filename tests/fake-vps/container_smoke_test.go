package fakevps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestContainerSmoke exercises the new container-deploy lifecycle (ADR-0005
// + ADR-0006) end-to-end against the fake-vps fixture:
//
//   - `simple-vps setup production` calls `server app setup-env`, which
//     creates the per-env Linux user, on-disk layout under
//     /var/apps/<app>/<env>/, and the per-(app, env) Podman network.
//   - `simple-vps deploy production` tars the working tree, uploads the
//     manifest, calls `server app apply`, which runs `podman build` +
//     `podman run` (with the initial §7 hardening subset), writes a
//     per-app Caddyfile fragment, validates the full Caddyfile, and
//     reloads Caddy.
//
// The fake-podman script records every command and starts a tiny HTTP
// listener on each allocated host port so the helper's healthcheck
// succeeds.
//
// Caddy-proxy assertions (curl through Caddy reaches the container) are
// out of scope for this slice — the fake-systemctl reload still drives
// the legacy routes.json proxy, not the new conf.d fragments. Those land
// alongside the Caddy-in-container conversion (ADR-0006 Cut 2).
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

	t.Run("container app reaches setup + deploy", env.testContainerAppLifecycle)
}

func (e *smokeEnv) testContainerAppLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	mustMkdir(t, app)
	writeContainerFixture(t, app)
	// Deploy needs a git tree (release id = git short SHA). The
	// `--dirty` flag still goes through git rev-parse today; using a
	// real commit keeps the smoke aligned with the canonical path.
	e.commitFixture(t, app)

	// 1. Setup must create the per-env user, paths, and per-(app, env)
	// Podman network. (The shared `ingress` network is normally created
	// at host install time; fake-podman doesn't validate network
	// existence in `run`, so the smoke proceeds without it. A real-
	// Podman run would need the provisioner to have created it first.)
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
	assertContains(t, commandsLog, "--user ") // numeric uid:gid is present
	assertContains(t, commandsLog, "--read-only")
	assertContains(t, commandsLog, "--tmpfs /tmp:size=64m")
	assertContains(t, commandsLog, "--cap-drop ALL")
	assertContains(t, commandsLog, "--security-opt no-new-privileges")
	assertContains(t, commandsLog, "--network app-api-production")
	assertContains(t, commandsLog, "--network ingress")

	// 4. The container record must carry the host-port label so the next
	// deploy reuses the allocation.
	labels := e.ssh(t, "cat /run/fake-podman/containers/app-api-production-web.labels")
	assertContains(t, labels, "app=api")
	assertContains(t, labels, "env=production")
	assertContains(t, labels, "service=web")
	assertContains(t, labels, "simple_vps_host_port=")

	// 5. The per-app Caddy fragment must be written and contain the
	// allocated host port (NOT the in-container service port).
	fragment := e.ssh(t, "cat /etc/caddy/conf.d/simple-vps-api-production.caddy")
	assertContains(t, fragment, `"api.example.com" {`)
	assertContains(t, fragment, "reverse_proxy 127.0.0.1:33")
	if strings.Contains(fragment, "reverse_proxy 127.0.0.1:3000") {
		t.Fatalf("Caddy fragment leaked the in-container port:\n%s", fragment)
	}

	// 6. Container listener should respond on the allocated port (proves
	// the fake-podman started the HTTP stub and the helper's healthcheck
	// would have succeeded).
	hostPort := strings.TrimSpace(e.ssh(t, "cat /run/fake-podman/containers/app-api-production-web.labels | sed -n 's/^simple_vps_host_port=//p'"))
	if hostPort == "" {
		t.Fatal("expected simple_vps_host_port label on container")
	}
	e.assertRemoteBody(t, "curl -fsS http://127.0.0.1:"+hostPort+"/health", "ok")

	// 7. A second deploy on the same source must reuse the same host
	// port (label discovery), so the Caddy fragment doesn't churn.
	firstFragment := fragment
	e.simpleVPS(t, app, nil, "deploy", "production")
	secondFragment := e.ssh(t, "cat /etc/caddy/conf.d/simple-vps-api-production.caddy")
	if firstFragment != secondFragment {
		t.Fatalf("expected port reuse across deploys, fragment changed:\nfirst:\n%s\nsecond:\n%s", firstFragment, secondFragment)
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

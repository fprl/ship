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

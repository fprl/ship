package fakevps

import (
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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/names"
	h "github.com/fprl/ship/tests/harness"
)

const productionEnv = "production"

func TestFakeCaddyRejectsUnsupportedCommand(t *testing.T) {
	cmd := exec.Command(filepath.Join(h.RepoRootForTest(t), "tests", "fake-vps", "fake-caddy"), "reload")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unsupported fake-caddy command succeeded: %s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("unsupported fake-caddy exit = %v, want code 2; output: %s", err, output)
	}
}

const (
	webhookEventDeployAborted     = "deploy_aborted"
	webhookEventDeployRecovered   = "deploy_recovered"
	webhookEventPreviewReaped     = "preview_reaped"
	webhookEventDoctorDegraded    = "doctor_degraded"
	webhookEventApprovalRequested = "approval_requested"
)

// TestContainerSmoke exercises the new container-deploy lifecycle (ADR-0005
// + ADR-0006 Cut 2) end-to-end against the fake-vps fixture:
//
//   - bare `ship` prepares the app env on first deploy, tars the
//     working tree, uploads the manifest, calls
//     `server app apply`, which runs `podman build` + `podman run`
//     (§7 hardening subset) without any host-port publish — the app
//     container joins both the per-(app, env) network and the shared
//     `ingress` network. The helper writes a per-app Caddyfile fragment
//     that reverse-proxies via container DNS, then reloads Caddy via
//     `podman exec caddy caddy reload`.
//   - End-to-end: `curl -H 'Host: api.example.com' http://127.0.0.1/health`
//     reaches the app container through the fake Caddy proxy.
func TestContainerSmoke(t *testing.T) {
	if os.Getenv("SHIP_RUN_FAKE_VPS_SMOKE") != "1" {
		t.Skip("set SHIP_RUN_FAKE_VPS_SMOKE=1 to run Docker-backed fake VPS smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)

	env := newSmokeEnv(t, ctx)
	env.buildBinaries(t)
	env.buildImage(t)
	env.startContainer(t)
	env.configureSSH(t, "deploy")
	env.waitForSSH(t)

	t.Run("webhook events", env.testWebhookEvents)
	t.Run("container app reaches setup + deploy + caddy proxy", env.testContainerAppLifecycle)
	t.Run("real Caddy validation rejects malformed fragments", env.testCaddyValidationRejectsMalformedFragment)
	t.Run("daily verbs pin unknown host and refuse changed host", env.testDailyVerbHostKeyTOFU)
	t.Run("phase 1 acceptance init ship branch and sslip", env.testPhase1AcceptanceAndZeroDNS)
	t.Run("member add ls rm manages deploy access", env.testMemberAccess)
	t.Run("agent role production ship approval flow", env.testAgentRoleApprovalFlow)
	t.Run("data fork and reset", env.testDataForks)
	t.Run("release command failure leaves old traffic unchanged", env.testReleaseCommandFailure)
	t.Run("probe failure explains old traffic kept serving", env.testProbeFailureWhy)
	t.Run("caddy switch failure restores runtime state", env.testCaddySwitchFailureRollback)
	t.Run("branch env resolution and production guards", env.testBranchEnvironmentGuards)
	t.Run("preview lifecycle mapping pin and reap", env.testPreviewLifecycle)
	t.Run("preview protection uses one capability", env.testPreviewProtection)
	t.Run("container rollback runs an older image release", env.testRollback)
	t.Run("proactive release image pruning", env.testImagePruning)
	t.Run("deploy removes processes dropped from the manifest", env.testRemovedProcessReconciliation)
	t.Run("concurrent deploys of the same app env serialize", env.testConcurrentDeploys)
	t.Run("static-only app deploys and restores without containers", env.testStaticOnlyAppLifecycle)
	t.Run("mixed container and static routes deploy as one release", env.testMixedContainerStaticLifecycle)
	t.Run("@secret refs resolve through set/list/rm into the runtime env", env.testSecretLifecycle)
	t.Run("preview secret scoping isolation", env.testPreviewSecretScoping)
	t.Run("secret bulk import merge replace and preview scope", env.testSecretBulkImport)
	t.Run("preview env overlay applies before secret resolution", env.testPreviewEnvOverlay)
	t.Run("preview custom base aliases serve with the same capability", env.testPreviewBaseAliases)
	t.Run("exec runs one-off commands in the release environment", env.testExec)
	t.Run("status + logs surface deployed processes without SSHing in", env.testStatusAndLogs)
	t.Run("box status and update converge helper version", env.testBoxStatusAndUpdate)
	t.Run("box app rm destroys an app and its environments", env.testBoxAppRm)
	t.Run("rm tears down one app environment", env.testDestroy)
}

func (e *smokeEnv) testCaddyValidationRejectsMalformedFragment(t *testing.T) {
	const fragment = "/etc/caddy/conf.d/malformed.caddy"
	e.dockerExec(t, "cat > "+fragment+" <<'EOF'\n\"malformed.example.com\" {\n\troute {\n\t\t@ship_capability_query query \"ship=tok\"\n\t}\nEOF")
	t.Cleanup(func() { e.dockerExec(t, "rm -f "+fragment) })

	result := e.run(t, e.repoRoot, nil, "docker", "exec", e.container, "podman", "exec", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")
	if result.err == nil {
		t.Fatalf("malformed Caddy fragment passed validation:\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
}

func (e *smokeEnv) testBoxStatusAndUpdate(t *testing.T) {
	e.ensureSmokeHostSeed(t)
	const oldVersion = "v0.4.0"
	const currentVersion = "v0.4.1"
	const newerVersion = "v0.4.2"
	clientBinary := e.buildStampedShip(t, "ship-client-current", "", currentVersion)
	currentHelper := e.buildStampedShip(t, "ship-helper-current", "linux", currentVersion)
	oldHelper := e.buildStampedShip(t, "ship-helper-old", "linux", oldVersion)
	newerHelper := e.buildStampedShip(t, "ship-helper-newer", "linux", newerVersion)
	e.startReleaseFixture(t, currentHelper)
	recordApp := filepath.Join(e.tmp, "box-version-api")
	mustMkdir(t, recordApp)
	writeBoxVersionFixture(t, recordApp)
	e.commitFixture(t, recordApp)
	e.mustRun(t, e.repoRoot, nil, "docker", "cp", oldHelper, e.container+":/usr/local/bin/ship")

	clientEnv := []string{"SHIP_LINUX_HELPER=" + currentHelper}
	recorded := e.runCommand(t, recordApp, clientEnv, nil, clientBinary, "--tls", "internal")
	if recorded.err != nil {
		t.Fatalf("client deploy to record version failed: %v\nstdout:\n%s\nstderr:\n%s", recorded.err, recorded.stdout, recorded.stderr)
	}
	status := e.runCommand(t, e.repoRoot, clientEnv, nil, clientBinary, "box", "status", "fake-vps")
	if status.err != nil {
		t.Fatalf("stale helper status failed: %v\nstdout:\n%s\nstderr:\n%s", status.err, status.stdout, status.stderr)
	}
	assertContains(t, status.stdout, "helper: "+oldVersion)
	assertContains(t, status.stdout, "last client: "+currentVersion)
	// The smoke container is shared across subtests, so the box carries
	// however many apps earlier subtests deployed — assert the count-line
	// shape, not a count.
	if !regexp.MustCompile(`(?m)^apps: \d+( \(\d+ envs\))?$`).MatchString(status.stdout) {
		t.Fatalf("box status should print an app count line:\n%s", status.stdout)
	}
	if !regexp.MustCompile(`(?m)^members: \d+ \(\d+ owners\)$`).MatchString(status.stdout) {
		t.Fatalf("box status should print a member count line:\n%s", status.stdout)
	}
	if strings.Contains(status.stdout, "versionapi:") {
		t.Fatalf("box status should print an app count, not an app table:\n%s", status.stdout)
	}
	assertContains(t, status.stdout, "next: ship box update fake-vps")
	doctor := e.runCommand(t, e.repoRoot, clientEnv, nil, clientBinary, "box", "doctor", "fake-vps", "--json")
	if doctor.err == nil {
		t.Fatal("doctor should degrade for stale helper")
	}
	assertHelperVersionCheck(t, doctor.stdout, "degraded")

	updated := e.runCommand(t, e.repoRoot, clientEnv, nil, clientBinary, "box", "update", "fake-vps")
	if updated.err != nil {
		t.Fatalf("box update failed: %v\nstdout:\n%s\nstderr:\n%s", updated.err, updated.stdout, updated.stderr)
	}
	assertContains(t, updated.stdout, "box updated: "+currentVersion)
	clean := e.runCommand(t, e.repoRoot, clientEnv, nil, clientBinary, "box", "status", "fake-vps", "--json")
	if clean.err != nil {
		t.Fatalf("clean status failed: %v\nstdout:\n%s\nstderr:\n%s", clean.err, clean.stdout, clean.stderr)
	}
	assertContains(t, clean.stdout, `"helper_version": "`+currentVersion+`"`)
	assertContains(t, clean.stdout, `"update_available": false`)
	assertContains(t, clean.stdout, `"members": {`)
	// Assert the helper_version CHECK recovered — not whole-box green:
	// in the shared smoke container earlier subtests leave unrelated
	// degraded checks (uncertified example.com routes), so doctor's
	// exit code is order-dependent here.
	doctor = e.runCommand(t, e.repoRoot, clientEnv, nil, clientBinary, "box", "doctor", "fake-vps", "--json")
	assertHelperVersionCheck(t, doctor.stdout, "ok")
	assertContains(t, e.dockerExec(t, "cat /var/lib/ship/updates-journal.jsonl"), `"version":"`+currentVersion+`"`)

	noOp := e.runCommand(t, e.repoRoot, clientEnv, nil, clientBinary, "box", "update", "fake-vps")
	if noOp.err != nil {
		t.Fatalf("second box update failed: %v\nstdout:\n%s\nstderr:\n%s", noOp.err, noOp.stdout, noOp.stderr)
	}
	if noOp.stdout != "box update: already current\n" {
		t.Fatalf("second update = %q, want exact no-op", noOp.stdout)
	}

	beforeTamperedUpdate := strings.TrimSpace(e.dockerExec(t, "sha256sum /usr/local/bin/ship"))
	e.dockerExec(t, "printf tampered-helper > /tmp/ship-release-fixture/ship-linux-"+runtime.GOARCH)
	tamperedClient := e.buildStampedShip(t, "ship-client-tampered", "", newerVersion)
	tampered := e.runCommand(t, e.repoRoot, clientEnv, nil, tamperedClient, "box", "update", "fake-vps")
	if tampered.err == nil {
		t.Fatal("box update accepted a release artifact with a mismatched checksum")
	}
	assertContains(t, tampered.stdout+tampered.stderr, "checksum mismatch")
	afterTamperedUpdate := strings.TrimSpace(e.dockerExec(t, "sha256sum /usr/local/bin/ship"))
	if afterTamperedUpdate != beforeTamperedUpdate {
		t.Fatalf("checksum mismatch changed installed helper:\nbefore %s\nafter  %s", beforeTamperedUpdate, afterTamperedUpdate)
	}
	assertNotContains(t, e.dockerExec(t, "cat /var/lib/ship/updates-journal.jsonl"), `"version":"`+newerVersion+`"`)

	e.mustRun(t, e.repoRoot, nil, "docker", "cp", newerHelper, e.container+":/usr/local/bin/ship")
	behindEnv := append([]string{"SHIP_ERROR_JSON=1"}, clientEnv...)
	behind := e.runCommand(t, e.repoRoot, behindEnv, nil, clientBinary, "box", "update", "fake-vps")
	if behind.err == nil {
		t.Fatal("older client must not downgrade newer helper")
	}
	assertContains(t, behind.stdout+behind.stderr, "client_behind_helper")
}

func (e *smokeEnv) startReleaseFixture(t *testing.T, helper string) {
	t.Helper()
	name := "ship-linux-" + runtime.GOARCH
	e.dockerExec(t, "rm -rf /tmp/ship-release-fixture && mkdir -p /tmp/ship-release-fixture")
	e.mustRun(t, e.repoRoot, nil, "docker", "cp", helper, e.container+":/tmp/ship-release-fixture/"+name)
	e.dockerExec(t, "cd /tmp/ship-release-fixture && sha256sum "+name+" > SHA256SUMS")
	e.dockerExec(t, `openssl req -x509 -newkey rsa:2048 -nodes -keyout /tmp/ship-release-fixture/key.pem -out /tmp/ship-release-fixture/cert.pem -days 1 -subj '/CN=github.com' -addext 'subjectAltName=DNS:github.com' >/dev/null 2>&1
cp /tmp/ship-release-fixture/cert.pem /usr/local/share/ca-certificates/ship-release-fixture.crt
update-ca-certificates >/dev/null
printf '127.0.0.1 github.com\n' >> /etc/hosts
cat > /tmp/ship-release-fixture/server.py <<'PY'
import http.server
import os
import ssl

root = '/tmp/ship-release-fixture'
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        return
    def do_GET(self):
        name = 'SHA256SUMS' if self.path.endswith('/SHA256SUMS') else os.path.basename(self.path)
        path = os.path.join(root, name)
        if not os.path.isfile(path):
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header('Content-Length', str(os.path.getsize(path)))
        self.end_headers()
        with open(path, 'rb') as f:
            self.wfile.write(f.read())

server = http.server.ThreadingHTTPServer(('127.0.0.1', 443), Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(certfile=root + '/cert.pem', keyfile=root + '/key.pem')
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
PY
python3 /tmp/ship-release-fixture/server.py >/tmp/ship-release-fixture/server.log 2>&1 &
echo $! > /tmp/ship-release-fixture/server.pid
for i in $(seq 1 50); do
  if curl -fsS https://github.com/fprl/ship/releases/download/v0.4.1/SHA256SUMS >/dev/null 2>&1; then exit 0; fi
  sleep 0.1
done
cat /tmp/ship-release-fixture/server.log >&2
exit 1`)
	t.Cleanup(func() {
		e.dockerExec(t, `if [ -f /tmp/ship-release-fixture/server.pid ]; then kill "$(cat /tmp/ship-release-fixture/server.pid)" 2>/dev/null || true; fi
rm -f /usr/local/share/ca-certificates/ship-release-fixture.crt
update-ca-certificates >/dev/null || true`)
	})
}

func (e *smokeEnv) buildStampedShip(t *testing.T, name, goos, stampedVersion string) string {
	t.Helper()
	path := filepath.Join(e.tmp, name)
	// Caches are isolated per-run inside t.TempDir; -modcacherw keeps the
	// module cache writable so TempDir cleanup can remove it.
	env := []string{"CGO_ENABLED=0", "GOFLAGS=-modcacherw",
		"GOCACHE=" + filepath.Join(e.tmp, "go-cache"),
		"GOMODCACHE=" + filepath.Join(e.tmp, "go-mod-cache")}
	if goos != "" {
		// The container runs the host's architecture (Docker Desktop);
		// a hardcoded amd64 binary lands under qemu emulation on arm64
		// hosts, which corrupts Go runtime behavior in impossible ways.
		env = append(env, "GOOS="+goos, "GOARCH="+runtime.GOARCH)
	}
	e.mustRun(t, e.repoRoot, env, "go", "build", "-ldflags", "-X github.com/fprl/ship/internal/version.Version="+stampedVersion, "-o", path, ".")
	return path
}

func (e *smokeEnv) assertShipSudoersMatchesRealHelperShape(t *testing.T) {
	t.Helper()
	e.ssh(t, "sudo -n /usr/local/bin/ship server app ls --json >/dev/null")

	result := e.run(t, e.repoRoot, nil, e.sshBin(), "fake-vps", "sudo -n env SHIP_ERROR_JSON=1 /usr/local/bin/ship server app ls --json")
	if result.err == nil {
		t.Fatalf("sudo accepted env-prefixed helper command; stdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if strings.Contains(result.stdout, `"apps"`) {
		t.Fatalf("env-prefixed helper command reached ship server app ls; stdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
}

func (e *smokeEnv) ensureSmokeHostSeed(t *testing.T) {
	t.Helper()
	e.dockerExec(t, "mkdir -p /etc/ship /var/lib/ship /run/ship /etc/caddy/conf.d /var/lib/caddy /etc/systemd/system")
	e.dockerExec(t, "printf '%s\\n' '{\"version\":1,\"members\":{}}' > /etc/ship/members.json")
	e.dockerExec(t, "printf '%s\\n' '{\"version\":1,\"values\":{\"box.address\":\"fake-vps\"}}' > /etc/ship/box-config.json")
	e.dockerExec(t, "mkdir -p /etc/ship/secrets && chmod 0700 /etc/ship/secrets && chown root:root /etc/ship/secrets")
	e.dockerExec(t, "mkdir -p /tmp/ship-deploy && chmod 1777 /tmp/ship-deploy")
	e.dockerExec(t, `cat > /etc/caddy/Caddyfile <<'EOF'
import conf.d/*.caddy
EOF`)
	e.dockerExec(t, "podman network exists ingress || podman network create ingress")
	e.dockerExec(t, "if [ ! -f /run/fake-podman/containers/caddy.labels ]; then podman run -d --name caddy --network ingress --publish 80:80 -v /etc/caddy:/etc/caddy:Z docker.io/library/caddy:2-alpine; fi")
	e.dockerExec(t, "touch /etc/systemd/system/ship-preview-reaper.timer /etc/systemd/system/ship-doctor.timer")
	e.dockerExec(t, "systemctl enable ship-preview-reaper.timer >/dev/null && systemctl start ship-preview-reaper.timer >/dev/null")
}

func (e *smokeEnv) testWebhookEvents(t *testing.T) {
	e.ensureSmokeHostSeed(t)
	sink := e.startWebhookSink(t)
	t.Cleanup(func() {
		e.dockerExec(t, "systemctl enable ship-preview-reaper.timer >/dev/null && systemctl start ship-preview-reaper.timer >/dev/null")
	})

	app := filepath.Join(e.tmp, "webhook-api")
	mustMkdir(t, app)
	secretValue := "webhook-secret-value"
	writeWebhookFixture(t, app, sink.URL("/app-one"))
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")

	unset := e.runShip(t, app, nil, "box", "webhook", "fake-vps")
	if unset.err != nil {
		t.Fatalf("box webhook unset read failed: %v\nstdout:\n%s\nstderr:\n%s", unset.err, unset.stdout, unset.stderr)
	}
	assertContains(t, unset.stderr, "box webhook is unset")
	assertContains(t, unset.stderr, "next: ship box webhook fake-vps <url>")
	if unset.stdout != "" {
		t.Fatalf("unset box webhook should leave stdout empty, got %q", unset.stdout)
	}
	freshConfig := e.ship(t, app, nil, "box", "config", "fake-vps", "--json")
	assertContains(t, freshConfig, `"webhook.url"`)
	assertContains(t, freshConfig, `"source": "default"`)
	set := e.ship(t, app, nil, "box", "config", "fake-vps", "set", "webhook.url", sink.URL("/box"))
	assertContains(t, set, "box config set webhook.url")
	if got := e.ship(t, app, nil, "box", "webhook", "fake-vps"); got != sink.URL("/box")+"\n" {
		t.Fatalf("box webhook read = %q, want %q", got, sink.URL("/box")+"\n")
	}
	if got := e.ship(t, app, nil, "box", "config", "fake-vps", "--json"); !strings.Contains(got, `"source": "set"`) || !strings.Contains(got, sink.URL("/box")) {
		t.Fatalf("box config set = %q", got)
	}
	cleared := e.ship(t, app, nil, "box", "webhook", "fake-vps", "--rm")
	assertContains(t, cleared, "box webhook cleared")
	if got := e.ship(t, app, nil, "box", "config", "fake-vps", "--json"); !strings.Contains(got, `"source": "default"`) {
		t.Fatalf("box config unset = %q", got)
	}
	e.ship(t, app, nil, "box", "webhook", "fake-vps", sink.URL("/box"))

	secondApp := filepath.Join(e.tmp, "webhook-second-api")
	mustMkdir(t, secondApp)
	writeWebhookSecondFixture(t, secondApp, sink.URL("/app-two"))
	e.commitFixture(t, secondApp)
	e.mustRun(t, secondApp, nil, "git", "checkout", "-B", "main")
	e.ship(t, secondApp, nil)

	e.ship(t, app, []byte(secretValue), "secret", "set", "API_TOKEN")
	e.ship(t, app, []byte(secretValue), "secret", "set", "API_TOKEN", "--preview")
	e.ship(t, app, nil)

	manifestPath := filepath.Join(app, "ship.toml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	failingManifest := strings.Replace(string(manifest), `release = "touch /data/release-ok"`, `release = "ship-fail-release"`, 1)
	if failingManifest == string(manifest) {
		t.Fatal("webhook fixture did not contain the success release command")
	}
	mustWrite(t, manifestPath, failingManifest)
	mustWrite(t, filepath.Join(app, "README.md"), "webhook failed release\n")
	e.commitFixture(t, app)
	failedRelease := gitRelease(t, e, app)
	failed := e.runShip(t, app, nil, "--tls", "internal")
	if failed.err == nil {
		t.Fatal("deploy with failing release command should fail")
	}
	aborted := sink.waitForEvent(t, webhookEventDeployAborted)
	assertWebhookSmokeField(t, aborted, "_sink_path", "/app-one")
	assertWebhookSmokeField(t, aborted, "release", failedRelease)
	assertWebhookSmokeNested(t, aborted, "why.outcome", "aborted_release")
	assertContains(t, webhookSmokeNestedString(t, aborted, "why.stderr_tail"), "fake release command failed")
	assertWebhookSmokeNested(t, aborted, "remediation.command", "ship")
	assertWebhookSmokeNested(t, aborted, "remediation.journal.failing_step", "release")

	fixedManifest := strings.Replace(failingManifest, `release = "ship-fail-release"`, `release = "touch /data/release-ok"`, 1)
	mustWrite(t, manifestPath, fixedManifest)
	mustWrite(t, filepath.Join(app, "README.md"), "webhook recovered release\n")
	e.commitFixture(t, app)
	recoveredRelease := gitRelease(t, e, app)
	e.ship(t, app, nil, "--tls", "internal")
	recovered := sink.waitForEvent(t, webhookEventDeployRecovered)
	assertWebhookSmokeField(t, recovered, "release", recoveredRelease)
	assertWebhookSmokeNested(t, recovered, "why.previous_failure.attempted_release", failedRelease)
	assertWebhookSmokeNested(t, recovered, "why.current.outcome", "deployed")
	assertWebhookSmokeNested(t, recovered, "remediation.command", "ship status")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/webhook")
	e.ship(t, app, nil, "--tls", "internal")
	previewEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "webhookapi", "feature/webhook")
	h.ForcePreviewExpired(t, func(command string) string { return e.dockerExec(t, command) }, "webhookapi", previewEnv)
	e.dockerExec(t, "/usr/local/bin/ship server env reap")
	reaped := sink.waitForEvent(t, webhookEventPreviewReaped)
	assertWebhookSmokeField(t, reaped, "env", "Preview feature/webhook")
	assertWebhookSmokeNested(t, reaped, "why.branch", "feature/webhook")
	assertWebhookSmokeNested(t, reaped, "remediation.command", "git checkout feature/webhook && ship")

	// Establish a doctor baseline, then drop the events it fired: the
	// first-ever record has no prior state, so every already-degraded
	// check (e.g. tls_certs for internal-cert routes) counts as newly
	// degraded. Clearing the sink isolates the reaper_timer transition
	// we actually induce next.
	e.dockerExec(t, "/usr/local/bin/ship server doctor record")
	e.dockerExec(t, "rm -f "+sink.eventsPath)
	e.dockerExec(t, "systemctl stop ship-preview-reaper.timer")
	e.dockerExec(t, "/usr/local/bin/ship server doctor record")
	doctor := sink.waitForEvent(t, webhookEventDoctorDegraded)
	assertWebhookSmokeField(t, doctor, "_sink_path", "/box")
	if box, _ := doctor["box"].(string); box == "" {
		t.Fatalf("doctor_degraded missing box host: %s", prettySmokeJSON(t, doctor))
	}
	assertWebhookSmokeNested(t, doctor, "why.id", "reaper_timer")
	assertContains(t, webhookSmokeNestedString(t, doctor, "why.evidence"), "active=inactive")
	assertContains(t, webhookSmokeNestedString(t, doctor, "remediation.command"), "systemctl start ship-preview-reaper.timer")
	if countWebhookSmokeEvents(sink.rawEvents(t), webhookEventDoctorDegraded) != 1 {
		t.Fatalf("doctor_degraded should POST once to the box webhook:\n%s", sink.rawEvents(t))
	}

	slowManifest := strings.Replace(fixedManifest, sink.URL("/app-one"), sink.URL("/slow?token=webhook-url-secret"), 1)
	if slowManifest == fixedManifest {
		t.Fatal("webhook fixture did not contain the app webhook URL")
	}
	slowFailing := strings.Replace(slowManifest, `release = "touch /data/release-ok"`, `release = "ship-fail-release"`, 1)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")
	mustWrite(t, manifestPath, slowFailing)
	mustWrite(t, filepath.Join(app, "README.md"), "slow webhook failed release\n")
	e.commitFixture(t, app)
	if result := e.runShip(t, app, nil, "--tls", "internal"); result.err == nil {
		t.Fatal("deploy with failing release command should fail")
	}
	slowFixed := strings.Replace(slowFailing, `release = "ship-fail-release"`, `release = "touch /data/release-ok"`, 1)
	mustWrite(t, manifestPath, slowFixed)
	mustWrite(t, filepath.Join(app, "README.md"), "slow webhook recovered release\n")
	e.commitFixture(t, app)
	start := time.Now()
	result := e.runShip(t, app, nil, "--tls", "internal")
	elapsed := time.Since(start)
	if result.err != nil {
		t.Fatalf("deploy should succeed even when webhook times out: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	if elapsed > 7*time.Second {
		t.Fatalf("deploy was delayed beyond webhook timeout budget: %s\nstdout:\n%s\nstderr:\n%s", elapsed, result.stdout, result.stderr)
	}
	if strings.Contains(result.stdout+result.stderr, "webhook-url-secret") || strings.Contains(result.stdout+result.stderr, sink.URL("/slow")) {
		t.Fatalf("webhook timeout leaked URL/token\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}

	rawEvents := sink.rawEvents(t)
	if strings.Contains(rawEvents, secretValue) {
		t.Fatalf("webhook payload leaked secret value:\n%s", rawEvents)
	}
}

func (e *smokeEnv) testContainerAppLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	mustMkdir(t, app)
	writeContainerFixture(t, app)
	// Deploy needs a git tree (release id = git short SHA). Commit to
	// stay on the canonical clean-tree path.
	e.commitFixture(t, app)

	e.assertShipSudoersMatchesRealHelperShape(t)

	// The shared `ingress` Podman network and the host-side Caddy
	// container would normally come from `ship box setup`.
	// The smoke skips the installer, so seed both here the same way
	// the provisioner does. These need root and the deploy user only
	// has passwordless sudo for /usr/local/bin/ship — use
	// docker exec instead.
	e.ensureSmokeHostSeed(t)

	// 1. Deploy on a clean tree. First deploy prepares the per-env user,
	// paths, identity, and per-(app, env) network before the release starts.
	firstDeploy := e.runShip(t, app, nil)
	if firstDeploy.err != nil {
		t.Fatalf("ship failed: %v\nstdout:\n%s\nstderr:\n%s", firstDeploy.err, firstDeploy.stdout, firstDeploy.stderr)
	}
	if firstDeploy.stdout != "https://api.example.com\n" {
		t.Fatalf("deploy stdout = %q, want only deployment URL", firstDeploy.stdout)
	}
	for _, want := range []string{"preflight ", "build ", "release ", "probe ok", "live"} {
		assertContains(t, firstDeploy.stderr, want)
	}

	e.ssh(t, "getent passwd "+identity.SystemUser("api", productionEnv)+" >/dev/null")
	e.ssh(t, "test -d "+identity.DataDir("api", productionEnv))
	e.ssh(t, "test -d "+identity.ReleaseDir("api", productionEnv))
	e.ssh(t, "test -f "+identity.IdentityFile("api", productionEnv))
	e.ssh(t, "test -f /run/fake-podman/networks/"+identity.Network("api", productionEnv))
	releaseDirStat := strings.TrimSpace(e.dockerExec(t, "stat -c '%a %U' "+identity.ReleaseDir("api", productionEnv)))
	if releaseDirStat != "755 root" {
		t.Fatalf("release dir ownership = %q, want `755 root`", releaseDirStat)
	}

	release := gitRelease(t, e, app)
	webContainer := identity.ContainerName("api", productionEnv, "web", release)
	releaseManifest := e.ssh(t, "cat "+identity.ReleaseManifestFile("api", productionEnv, release))
	assertContains(t, releaseManifest, "port = 3000")
	releaseMetadata := e.ssh(t, "cat "+identity.ReleaseMetadataFile("api", productionEnv, release))
	assertContains(t, releaseMetadata, `"release": "`+release+`"`)
	assertContains(t, releaseMetadata, `"dirty": false`)
	assertContains(t, releaseMetadata, `"base_commit":`)

	// 3. fake-podman should have logged build + run for the web process.
	commandsLog := e.ssh(t, "cat /run/fake-podman/commands.log")
	assertContains(t, commandsLog, "podman build")
	assertContains(t, commandsLog, "podman run")
	assertContains(t, commandsLog, "--name "+webContainer)
	assertContains(t, commandsLog, "--user ") // numeric uid:gid
	assertContains(t, commandsLog, "--read-only")
	assertContains(t, commandsLog, "--tmpfs /tmp:size=64m,mode=1777")
	assertContains(t, commandsLog, "--cap-drop ALL")
	assertContains(t, commandsLog, "--security-opt no-new-privileges")
	assertContains(t, commandsLog, "--memory 512m")
	assertContains(t, commandsLog, "--cpus 0.5")
	assertContains(t, commandsLog, "--network "+identity.Network("api", productionEnv))
	assertContains(t, commandsLog, "--network ingress")

	// 4. App container must NOT carry the host-port label (that path is
	// gone with Caddy-in-container) and the run line must NOT carry a
	// --publish (no host loopback ingress).
	labels := e.ssh(t, "cat /run/fake-podman/containers/"+webContainer+".labels")
	assertContains(t, labels, "ship.app=api")
	assertContains(t, labels, "ship.env="+productionEnv)
	assertContains(t, labels, "ship.process=web")
	assertContains(t, labels, "ship.release="+release)
	if strings.Contains(commandsLog, "--publish 127.0.0.1:") {
		t.Fatalf("app container still publishes a host loopback port; Caddy-in-container should drop this:\n%s", commandsLog)
	}

	// 5. Caddy fragment should reverse-proxy via container DNS, not
	// 127.0.0.1.
	fragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("api", productionEnv))
	assertContains(t, fragment, `"api.example.com" {`)
	assertContains(t, fragment, "reverse_proxy http://"+webContainer+":3000")
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
	processEnv := e.urlBody(t, "https://api.example.com", "/ship-env")
	for _, want := range []string{
		"SHIP_URL=https://api.example.com\n",
		"SHIP_BRANCH=main\n",
		"SHIP_ENV=production\n",
		"SHIP_RELEASE=" + release + "\n",
	} {
		assertContains(t, processEnv, want)
	}

	// 8. A second deploy on the same source must start a replacement
	// container before Caddy moves traffic, instead of removing the routed
	// container name up front.
	firstFragment := fragment
	commandsBeforeRedeploy := e.ssh(t, "cat /run/fake-podman/commands.log")
	e.ship(t, app, nil)
	secondFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("api", productionEnv))
	if firstFragment == secondFragment {
		t.Fatalf("expected same-release redeploy to route to a replacement container:\n%s", secondFragment)
	}
	secondWebContainer := currentWebContainer(t, e, app)
	if secondWebContainer == webContainer {
		t.Fatalf("expected same-release redeploy to replace %s", webContainer)
	}
	assertContains(t, secondFragment, "reverse_proxy http://"+secondWebContainer+":3000")
	e.dockerExec(t, "test ! -e /run/fake-podman/containers/"+webContainer+".labels")
	commandsAfterRedeploy := e.ssh(t, "cat /run/fake-podman/commands.log")
	redeployCommands := strings.TrimPrefix(commandsAfterRedeploy, commandsBeforeRedeploy)
	assertContainsInOrder(t, redeployCommands,
		"podman run -d --name "+secondWebContainer,
		"podman exec caddy caddy reload --config /etc/caddy/Caddyfile",
		"podman rm -f "+webContainer,
	)

	// 9. Explicit rebuild refreshes mutable base images and bypasses
	// Podman's build cache.
	e.ship(t, app, nil, "--rebuild")
	commandsLog = e.ssh(t, "cat /run/fake-podman/commands.log")
	assertContains(t, commandsLog, "podman build --no-cache --pull=always")

}

func (e *smokeEnv) testDailyVerbHostKeyTOFU(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	freshHome := filepath.Join(e.tmp, "daily-tofu-home")
	copyShipIdentityForHome(t, e.shipHome, freshHome)
	knownHosts := filepath.Join(freshHome, ".config", "ship", "known_hosts")

	first := e.runCommand(t, app, []string{"HOME=" + freshHome, "XDG_CONFIG_HOME="}, nil, e.goBin, "status")
	if first.err != nil {
		t.Fatalf("daily status should pin unknown host and succeed: %v\nstdout:\n%s\nstderr:\n%s", first.err, first.stdout, first.stderr)
	}
	assertContains(t, first.stdout, "Production main")
	pinned := readFile(t, knownHosts)
	assertContains(t, pinned, "fake-vps ")

	second := e.runCommand(t, app, []string{"HOME=" + freshHome, "XDG_CONFIG_HOME="}, nil, e.goBin, "status")
	if second.err != nil {
		t.Fatalf("daily status should verify existing host pin: %v\nstdout:\n%s\nstderr:\n%s", second.err, second.stdout, second.stderr)
	}
	assertContains(t, second.stdout, "Production main")

	wrongKey := strings.Join(strings.Fields(alicePublicKeyForFreshInstallTest())[:2], " ")
	if err := os.WriteFile(knownHosts, []byte("fake-vps "+wrongKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := e.runCommand(t, app, []string{"HOME=" + freshHome, "XDG_CONFIG_HOME="}, nil, e.goBin, "status")
	if changed.err == nil {
		t.Fatalf("daily status should refuse changed host key\nstdout:\n%s\nstderr:\n%s", changed.stdout, changed.stderr)
	}
	output := changed.stdout + changed.stderr
	assertContains(t, output, "box host key changed")
	assertContains(t, output, "SSH host key for fake-vps is unknown or changed")
	assertContains(t, output, "next: ship box setup <ssh-target>")
}

func (e *smokeEnv) testPhase1AcceptanceAndZeroDNS(t *testing.T) {
	app := filepath.Join(e.tmp, "phase1-init")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "phaseone"
box = "fake-vps"

[processes]
web = {}

[routes]
"phaseone.example.com" = "web"
`)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")

	prod := e.runShip(t, app, nil)
	if prod.err != nil {
		t.Fatalf("ship from main failed: %v\nstdout:\n%s\nstderr:\n%s", prod.err, prod.stdout, prod.stderr)
	}
	prodURL := assertOnlyURL(t, prod.stdout)
	prodParsed, err := url.Parse(prodURL)
	if err != nil || !strings.HasPrefix(prodParsed.Hostname(), "phaseone.") {
		t.Fatalf("production URL should use the app label, got %q (parse err %v)", prodURL, err)
	}
	h.AssertURLServes200(t, func(command string) string { return e.ssh(t, command) }, prodURL)

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/phase1")
	mustWrite(t, filepath.Join(app, "README.md"), "feature phase1\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "feature phase1")
	preview := e.runShip(t, app, nil)
	if preview.err != nil {
		t.Fatalf("feature branch ship failed: %v\nstdout:\n%s\nstderr:\n%s", preview.err, preview.stdout, preview.stderr)
	}
	previewURL := assertOnlyURL(t, preview.stdout)
	if previewURL == prodURL {
		t.Fatalf("feature branch URL should be distinct from Production URL: %s", previewURL)
	}
	h.AssertURLServes200(t, func(command string) string { return e.ssh(t, command) }, previewURL)
	previewParsed, err := url.Parse(previewURL)
	if err != nil || !strings.HasPrefix(previewParsed.Hostname(), "phaseone-feature-phase1-") {
		t.Fatalf("preview URL should use an app-first label, got %q (parse err %v)", previewURL, err)
	}
	previewRelease := gitRelease(t, e, app)
	previewEnv := e.urlBody(t, previewURL, "/ship-env")
	// SHIP_URL is the env's own clean https URL; the capability token rides
	// only the deploy-stdout URL, never the process env.
	cleanPreviewURL := previewURL
	if i := strings.IndexByte(cleanPreviewURL, '?'); i >= 0 {
		cleanPreviewURL = cleanPreviewURL[:i]
	}
	for _, want := range []string{
		"SHIP_URL=" + cleanPreviewURL + "\n",
		"SHIP_BRANCH=feature/phase1\n",
		"SHIP_ENV=preview\n",
		"SHIP_RELEASE=" + previewRelease + "\n",
	} {
		assertContains(t, previewEnv, want)
	}

	zero := filepath.Join(e.tmp, "zero-dns")
	mustMkdir(t, zero)
	writeZeroDNSFixture(t, zero)
	e.commitFixture(t, zero)
	e.mustRun(t, zero, nil, "git", "checkout", "-B", "main")
	result := e.runShip(t, zero, nil)
	if result.err != nil {
		t.Fatalf("zero-DNS ship failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	zeroURL := assertOnlyURL(t, result.stdout)
	if strings.Contains(zeroURL, "ship://") {
		t.Fatalf("zero-DNS ship printed removed fallback URL: %s", zeroURL)
	}
	parsed, err := url.Parse(zeroURL)
	if err != nil {
		t.Fatalf("parse zero-DNS URL: %v", err)
	}
	if !strings.HasSuffix(parsed.Hostname(), ".sslip.io") {
		t.Fatalf("zero-DNS URL should use sslip.io, got %s", zeroURL)
	}
	if !strings.HasPrefix(parsed.Hostname(), "zerodns.") {
		t.Fatalf("zero-DNS production URL should use the app label, got %s", zeroURL)
	}
	h.AssertURLServes200(t, func(command string) string { return e.ssh(t, command) }, zeroURL)
	ip := sslipIPFromHost(parsed.Hostname())
	wantLast := "next: add DNS A <your-domain> → " + ip + " and add it under [routes]"
	if gotLast := lastNonEmptyLine(result.stderr); gotLast != wantLast {
		t.Fatalf("zero-DNS final stderr line = %q, want %q\nstderr:\n%s", gotLast, wantLast, result.stderr)
	}
}

func (e *smokeEnv) testReleaseCommandFailure(t *testing.T) {
	app := filepath.Join(e.tmp, "release-fail")
	mustMkdir(t, app)
	writeReleaseFailFixture(t, app)
	e.commitFixture(t, app)

	noWhy := e.runShip(t, app, nil, "why")
	if noWhy.err == nil {
		t.Fatal("why before any deploy should fail")
	}
	assertContains(t, noWhy.stdout+noWhy.stderr, "deploy journal lookup failed")
	assertContains(t, noWhy.stdout+noWhy.stderr, "no deploys recorded")
	assertContains(t, noWhy.stdout+noWhy.stderr, "next: ship")

	secretValue := "releasefail-secret-token"
	e.ship(t, app, []byte(secretValue), "secret", "set", "API_TOKEN")
	e.ship(t, app, nil)
	e.dockerExec(t, "test -f "+identity.DataDir("releasefail", productionEnv)+"/release-ok")
	stableEnv := e.dockerExec(t, "cat "+identity.EnvFile("releasefail", productionEnv))
	assertContains(t, stableEnv, "MARKER=stable")
	statusJSON := e.ship(t, app, nil, "status", "--json")
	if strings.Contains(statusJSON, `"process":"release"`) || strings.Contains(statusJSON, `"process": "release"`) {
		t.Fatalf("release command container polluted status:\n%s", statusJSON)
	}
	stableFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("releasefail", productionEnv))
	stableContainer := currentWebContainer(t, e, app)

	manifestPath := filepath.Join(app, "ship.toml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	failingManifest := strings.Replace(string(manifest), `release = "touch /data/release-ok"`, `release = "ship-fail-release"`, 1)
	failingManifest = strings.Replace(failingManifest, `MARKER = "stable"`, `MARKER = "next-marker"`, 1)
	if failingManifest == string(manifest) {
		t.Fatal("release failure fixture did not contain the success release command")
	}
	mustWrite(t, manifestPath, failingManifest)
	e.commitFixture(t, app)
	failedRelease := gitRelease(t, e, app)
	failed := e.runShip(t, app, nil)
	if failed.err == nil {
		t.Fatal("deploy with failing release command should fail")
	}
	assertContains(t, failed.stdout+failed.stderr, "release command")
	assertContains(t, failed.stdout+failed.stderr, "failed before traffic switch")
	fragmentAfterFailure := e.ssh(t, "cat "+identity.CaddyFragmentFile("releasefail", productionEnv))
	if fragmentAfterFailure != stableFragment {
		t.Fatalf("failing release command changed traffic:\nbefore:\n%s\nafter:\n%s", stableFragment, fragmentAfterFailure)
	}
	e.dockerExec(t, "test -e /run/fake-podman/containers/"+stableContainer+".labels")
	e.dockerExec(t, "test ! -e /tmp/ship-deploy/releasefail-"+productionEnv+"-"+failedRelease)
	envAfterFailure := e.dockerExec(t, "cat "+identity.EnvFile("releasefail", productionEnv))
	if envAfterFailure != stableEnv {
		t.Fatalf("failing release command changed runtime env:\nbefore:\n%s\nafter:\n%s", stableEnv, envAfterFailure)
	}

	why := e.ship(t, app, nil, "why")
	assertContains(t, why, "Deploy aborted for Production main")
	assertContains(t, why, "failing step: release")
	assertContains(t, why, "probable cause: release command exited non-zero before traffic switched.")
	assertContains(t, why, "fake release command failed")
	assertContains(t, why, "old release ")
	assertContains(t, why, "kept serving; no traffic was switched.")
	assertContains(t, why, "shipped by: Smoke <smoke@example.com> (ssh key: fake-vps-smoke)")
	assertContains(t, why, "next: fix the release command in ship.toml, then ship")

	var whyJSON smokeWhyEntry
	rawWhyJSON := e.ship(t, app, nil, "why", "--json")
	if err := json.Unmarshal([]byte(rawWhyJSON), &whyJSON); err != nil {
		t.Fatalf("why --json output not parseable as JSON: %v\nraw:\n%s", err, rawWhyJSON)
	}
	if whyJSON.Outcome != "aborted_release" || whyJSON.FailingStep != "release" || whyJSON.AttemptedRelease != failedRelease {
		t.Fatalf("unexpected release failure journal entry: %+v", whyJSON)
	}
	if whyJSON.Identity.GitAuthor != "Smoke <smoke@example.com>" || whyJSON.Identity.SSHKeyComment != "fake-vps-smoke" {
		t.Fatalf("why --json missing attribution: %+v", whyJSON.Identity)
	}
	journal := e.ssh(t, "cat "+identity.DeployJournalFile("releasefail", productionEnv))
	if strings.Contains(journal, secretValue) {
		t.Fatalf("deploy journal leaked secret value:\n%s", journal)
	}
}

func (e *smokeEnv) testProbeFailureWhy(t *testing.T) {
	app := filepath.Join(e.tmp, "probe-fail")
	mustMkdir(t, app)
	writeProbeFailFixture(t, app)
	e.commitFixture(t, app)

	secretValue := "probe-log-secret-token"
	e.ship(t, app, []byte(secretValue), "secret", "set", "LEAK_ON_PROBE_LOG")
	e.ship(t, app, nil)
	stableFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("probefail", productionEnv))
	stableContainer := currentWebContainer(t, e, app)

	manifestPath := filepath.Join(app, "ship.toml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	failingManifest := strings.Replace(string(manifest), "port = 3000", "port = 3999", 1)
	if failingManifest == string(manifest) {
		t.Fatal("probe failure fixture did not contain port = 3000")
	}
	mustWrite(t, manifestPath, failingManifest)
	mustWrite(t, filepath.Join(app, "README.md"), "probe failure\n")
	e.commitFixture(t, app)
	failedRelease := gitRelease(t, e, app)
	failed := e.runShip(t, app, nil)
	if failed.err == nil {
		t.Fatal("deploy with failing probe should fail")
	}
	assertContains(t, failed.stdout+failed.stderr, "health check failed")
	assertContains(t, failed.stdout+failed.stderr, "HTTP status 502")
	assertContains(t, failed.stderr, "[redacted]")
	if strings.Contains(failed.stdout+failed.stderr, secretValue) {
		t.Fatalf("probe failure output leaked secret value\nstdout:\n%s\nstderr:\n%s", failed.stdout, failed.stderr)
	}
	fragmentAfterFailure := e.ssh(t, "cat "+identity.CaddyFragmentFile("probefail", productionEnv))
	if fragmentAfterFailure != stableFragment {
		t.Fatalf("failing probe changed traffic:\nbefore:\n%s\nafter:\n%s", stableFragment, fragmentAfterFailure)
	}
	e.dockerExec(t, "test -e /run/fake-podman/containers/"+stableContainer+".labels")
	e.dockerExec(t, "test ! -e /run/fake-podman/containers/"+identity.ContainerName("probefail", productionEnv, "web", failedRelease)+".labels")

	why := e.ship(t, app, nil, "why")
	assertContains(t, why, "Deploy aborted for Production main")
	assertContains(t, why, "failing step: probe")
	assertContains(t, why, "probable cause: probe returned HTTP 502")
	assertContains(t, why, "HTTP status 502: upstream ")
	assertContains(t, why, "old release ")
	assertContains(t, why, "kept serving; failed probes never receive traffic with the current engine.")
	assertContains(t, why, "shipped by: Smoke <smoke@example.com> (ssh key: fake-vps-smoke)")
	assertContains(t, why, "next: fix the process port or probe path in ship.toml, then ship")

	var whyJSON smokeWhyEntry
	rawWhyJSON := e.ship(t, app, nil, "why", "--json")
	if err := json.Unmarshal([]byte(rawWhyJSON), &whyJSON); err != nil {
		t.Fatalf("why --json output not parseable as JSON: %v\nraw:\n%s", err, rawWhyJSON)
	}
	if whyJSON.Outcome != "aborted_probe" || whyJSON.FailingStep != "probe" || whyJSON.AttemptedRelease != failedRelease {
		t.Fatalf("unexpected probe failure journal entry: %+v", whyJSON)
	}
	if whyJSON.Probe == nil || whyJSON.Probe.Status != 502 {
		t.Fatalf("probe journal missing HTTP status: %+v", whyJSON.Probe)
	}
}

func (e *smokeEnv) testCaddySwitchFailureRollback(t *testing.T) {
	app := filepath.Join(e.tmp, "caddy-fail")
	mustMkdir(t, app)
	writeCaddyFailFixture(t, app)
	e.commitFixture(t, app)

	e.ship(t, app, nil)
	stableWorker := currentProcessContainer(t, e, app, "worker")
	stableEnv := e.dockerExec(t, "cat "+identity.EnvFile("caddyfail", productionEnv))
	stableManifest := e.dockerExec(t, "cat "+identity.ManifestFile("caddyfail", productionEnv))
	stableFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("caddyfail", productionEnv))
	stableStaticCurrent := e.dockerExec(t, "readlink "+filepath.Join(identity.StaticDir("caddyfail", productionEnv), "current"))
	e.dockerExec(t, "test -f /run/fake-podman/listeners/"+stableWorker+".pid")

	manifestPath := filepath.Join(app, "ship.toml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	nextManifest := strings.Replace(string(manifest), `MARKER = "stable"`, `MARKER = "failed"`, 1)
	nextManifest = strings.Replace(nextManifest, `path = "/docs"`, `path = "/docs-v2"`, 1)
	if nextManifest == string(manifest) {
		t.Fatal("test fixture did not contain stable marker")
	}
	mustWrite(t, manifestPath, nextManifest)
	mustWrite(t, filepath.Join(app, "docs-dist", "index.html"), "docs failed\n")
	mustWrite(t, filepath.Join(app, "README.md"), "trigger caddy failure\n")
	e.commitFixture(t, app)
	failedRelease := gitRelease(t, e, app)
	e.dockerExec(t, "touch /run/fake-podman/fail-caddy-reload")
	defer e.dockerExec(t, "rm -f /run/fake-podman/fail-caddy-reload")

	failed := e.runShip(t, app, nil)
	if failed.err == nil {
		t.Fatal("deploy with failing Caddy reload should fail")
	}
	assertContains(t, failed.stdout+failed.stderr, "caddy reload")

	if got := e.dockerExec(t, "cat "+identity.EnvFile("caddyfail", productionEnv)); got != stableEnv {
		t.Fatalf("failing Caddy reload changed runtime env:\nbefore:\n%s\nafter:\n%s", stableEnv, got)
	}
	if got := e.dockerExec(t, "cat "+identity.ManifestFile("caddyfail", productionEnv)); got != stableManifest {
		t.Fatalf("failing Caddy reload changed current manifest:\nbefore:\n%s\nafter:\n%s", stableManifest, got)
	}
	if got := e.ssh(t, "cat "+identity.CaddyFragmentFile("caddyfail", productionEnv)); got != stableFragment {
		t.Fatalf("failing Caddy reload changed traffic:\nbefore:\n%s\nafter:\n%s", stableFragment, got)
	}
	if got := e.dockerExec(t, "readlink "+filepath.Join(identity.StaticDir("caddyfail", productionEnv), "current")); got != stableStaticCurrent {
		t.Fatalf("failing Caddy reload changed static current:\nbefore: %s\nafter: %s", stableStaticCurrent, got)
	}
	e.dockerExec(t, "test -f /run/fake-podman/listeners/"+stableWorker+".pid")
	e.dockerExec(t, "test ! -e /run/fake-podman/containers/"+identity.ContainerName("caddyfail", productionEnv, "web", failedRelease)+".labels")
	e.dockerExec(t, "test ! -e /run/fake-podman/containers/"+identity.ContainerName("caddyfail", productionEnv, "worker", failedRelease)+".labels")
}

func (e *smokeEnv) testBranchEnvironmentGuards(t *testing.T) {
	app := filepath.Join(e.tmp, "branch-api")
	mustMkdir(t, app)
	writeBranchFixture(t, app)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "stable")
	baseCommit := strings.TrimSpace(e.mustRun(t, app, nil, "git", "rev-parse", "HEAD"))
	baseShort := gitRelease(t, e, app)

	e.ship(t, app, nil)
	e.ssh(t, "test -f "+identity.IdentityFile("branchapi", "production"))

	mustWrite(t, filepath.Join(app, "dirty.txt"), "dirty deploy payload")
	rejected := e.runShip(t, app, nil)
	if rejected.err == nil {
		t.Fatal("production branch deploy should reject a dirty worktree")
	}
	if rejected.stdout != "" {
		t.Fatalf("dirty_worktree human error should not write stdout, got:\n%s", rejected.stdout)
	}
	wantDirty := "Production ship failed\nproduction branch \"stable\" has uncommitted changes\nnext: git add . && git commit -m \"<message>\"\n"
	if rejected.stderr != wantDirty {
		t.Fatalf("dirty_worktree shape mismatch\nwant:\n%s\ngot:\n%s", wantDirty, rejected.stderr)
	}
	if err := os.Remove(filepath.Join(app, "dirty.txt")); err != nil {
		t.Fatal(err)
	}

	mustWrite(t, filepath.Join(app, "README.md"), "new production\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "new production")
	e.ship(t, app, nil)
	e.mustRun(t, app, nil, "git", "reset", "--hard", baseCommit)
	behind := e.runShip(t, app, nil)
	if behind.err == nil {
		t.Fatal("production deploy from behind checkout should fail")
	}
	assertContains(t, behind.stdout+behind.stderr, "Production ship failed")
	assertContains(t, behind.stdout+behind.stderr, "deployed commit")
	assertContains(t, behind.stdout+behind.stderr, "next: git pull")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feat/x")
	mustWrite(t, filepath.Join(app, "preview-dirty.txt"), "dirty preview payload")
	e.ship(t, app, nil)
	featEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "branchapi", "feat/x")
	assertPreviewEnvName(t, featEnv, "feat-x")
	e.ssh(t, "test -f "+identity.IdentityFile("branchapi", featEnv))
	status := statusEnvByBranch(t, e, app, "feat/x")
	if !status.Dirty {
		t.Fatalf("expected dirty release in status: %+v", status)
	}
	if !strings.HasPrefix(status.Release, baseShort+"-dirty-") {
		t.Fatalf("dirty release id %q should start with %s-dirty-", status.Release, baseShort)
	}
	releaseMetadata := e.ssh(t, "cat "+identity.ReleaseMetadataFile("branchapi", featEnv, status.Release))
	assertContains(t, releaseMetadata, `"dirty": true`)
	assertContains(t, releaseMetadata, `"base_commit": "`+baseCommit+`"`)
	textStatus := e.ship(t, app, nil, "status")
	assertContains(t, textStatus, "Preview feat/x")
	assertContains(t, textStatus, "(dirty")
	if strings.Contains(stripURLs(textStatus), featEnv) {
		t.Fatalf("human status leaked internal preview env outside URLs:\n%s", textStatus)
	}

	checkedOutBranchFlag := e.runShip(t, app, nil, "--branch", "feat/x")
	if checkedOutBranchFlag.err == nil {
		t.Fatal("deploy --branch should fail while a branch is checked out")
	}
	assertContains(t, checkedOutBranchFlag.stdout+checkedOutBranchFlag.stderr, "branch resolution failed")
	assertContains(t, checkedOutBranchFlag.stdout+checkedOutBranchFlag.stderr, "--branch is only accepted")

	e.mustRun(t, app, nil, "git", "checkout", "--detach")
	detachedWithoutBranch := e.runShip(t, app, nil)
	if detachedWithoutBranch.err == nil {
		t.Fatal("detached HEAD deploy without --branch should fail")
	}
	assertContains(t, detachedWithoutBranch.stdout+detachedWithoutBranch.stderr, "branch resolution failed")
	assertContains(t, detachedWithoutBranch.stdout+detachedWithoutBranch.stderr, "HEAD is detached")
	e.ship(t, app, nil, "--branch", "feat/x")
	if again := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "branchapi", "feat/x"); again != featEnv {
		t.Fatalf("re-ship should keep preview env stable: first=%s second=%s", featEnv, again)
	}

	e.mustRun(t, app, nil, "git", "checkout", "-B", "mañana/Über")
	e.ship(t, app, nil)
	accentEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "branchapi", "mañana/Über")
	assertPreviewEnvName(t, accentEnv, "ma-ana-ber")
	e.ssh(t, "test -f "+identity.IdentityFile("branchapi", accentEnv))

	longBranch := "feature/abcdefghijklmnopqrstuvwxyz0123456789"
	e.mustRun(t, app, nil, "git", "checkout", "-B", longBranch)
	e.ship(t, app, nil)
	longEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "branchapi", longBranch)
	assertPreviewEnvName(t, longEnv, "feature-abcdefghijklmnopqrst")
	e.ssh(t, "test -f "+identity.IdentityFile("branchapi", longEnv))

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feat/login", "main")
	e.ship(t, app, nil)
	slashEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "branchapi", "feat/login")
	assertPreviewEnvName(t, slashEnv, "feat-login")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feat.login", "main")
	e.ship(t, app, nil)
	dotEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "branchapi", "feat.login")
	assertPreviewEnvName(t, dotEnv, "feat-login")
	if slashEnv == dotEnv {
		t.Fatalf("raw branches with colliding sanitized names should get distinct envs: %s", slashEnv)
	}
}

func (e *smokeEnv) testPreviewLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "preview-api")
	mustMkdir(t, app)
	writePreviewLifecycleFixture(t, app)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")

	e.ship(t, app, nil)
	e.ssh(t, "test -f "+identity.IdentityFile("previewapi", "production"))

	unknown := e.runShip(t, app, nil, "preview", "pin", "ghost/branch")
	if unknown.err == nil {
		t.Fatal("pin for an unmapped preview branch should fail")
	}
	assertContains(t, unknown.stdout+unknown.stderr, "preview environment lookup failed")
	assertContains(t, unknown.stdout+unknown.stderr, "no preview environment is mapped")
	assertContains(t, unknown.stdout+unknown.stderr, "next: git checkout ghost/branch && ship")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/lifecycle")
	e.ship(t, app, nil)
	previewEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "previewapi", "feature/lifecycle")
	assertPreviewEnvName(t, previewEnv, "feature-lifecycle")
	firstIdentity := readPreviewIdentity(t, e, "previewapi", previewEnv)
	if firstIdentity.Preview == nil || firstIdentity.Preview.ExpiresAt == nil {
		t.Fatalf("unexpected first preview identity: %+v", firstIdentity)
	}
	if firstIdentity.Preview.Branch != "feature/lifecycle" || names.PreviewSanitizedBranch(firstIdentity.Preview.Branch) != names.PreviewBranchSlug(firstIdentity.Env) {
		t.Fatalf("preview mapping has inconsistent branch/environment derivation: %+v", firstIdentity.Preview)
	}
	h.SetPreviewExpiry(t, func(command string) string { return e.dockerExec(t, command) }, "previewapi", previewEnv, "2000-01-01T00:00:00Z")
	firstExpiry := parseRemoteTime(t, "2000-01-01T00:00:00Z")

	e.ship(t, app, []byte("throwaway"), "secret", "set", "cleanup_key")
	mustWrite(t, filepath.Join(app, "README.md"), "second preview ship\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "second preview")
	e.ship(t, app, nil)
	if again := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "previewapi", "feature/lifecycle"); again != previewEnv {
		t.Fatalf("same branch should keep suffix stable: first=%s second=%s", previewEnv, again)
	}
	refreshed := readPreviewIdentity(t, e, "previewapi", previewEnv)
	refreshedExpiry := parseRemoteTime(t, *refreshed.Preview.ExpiresAt)
	if !refreshedExpiry.After(firstExpiry) {
		t.Fatalf("expiry should refresh on re-ship: before=%s after=%s", firstExpiry, refreshedExpiry)
	}

	e.ship(t, app, nil, "preview", "pin", "feature/lifecycle")
	pinned := readPreviewIdentity(t, e, "previewapi", previewEnv)
	if pinned.Preview == nil || pinned.Preview.ExpiresAt != nil {
		t.Fatalf("pin should clear expiry: %+v", pinned.Preview)
	}
	e.dockerExec(t, "/usr/local/bin/ship server env reap")
	e.dockerExec(t, "test -d "+identity.EnvRoot("previewapi", previewEnv))

	e.ship(t, app, nil, "preview", "unpin", "feature/lifecycle")
	unpinned := readPreviewIdentity(t, e, "previewapi", previewEnv)
	if unpinned.Preview == nil || unpinned.Preview.ExpiresAt == nil {
		t.Fatalf("unpin should restore expiry: %+v", unpinned.Preview)
	}
	appList := appListPayloadForBox(t, e, app)
	prodAppListEnv := appListEnvByAppClassBranch(t, appList, "previewapi", "production", "main")
	if prodAppListEnv.Env != productionEnv || prodAppListEnv.URL != "https://preview.example.com" || prodAppListEnv.CurrentRelease == "" || prodAppListEnv.Health != "healthy" || prodAppListEnv.ExpiresAt != "" || prodAppListEnv.Pinned || prodAppListEnv.ShippedBy == nil {
		t.Fatalf("production app ls summary missing fields: %+v", prodAppListEnv)
	}
	previewAppListEnv := appListEnvByAppClassBranch(t, appList, "previewapi", "preview", "feature/lifecycle")
	if previewAppListEnv.Env != previewEnv || !strings.Contains(previewAppListEnv.URL, "previewapi-"+previewEnv+".") || previewAppListEnv.CurrentRelease == "" || previewAppListEnv.Health != "healthy" || previewAppListEnv.ExpiresAt == "" || previewAppListEnv.Pinned || previewAppListEnv.ShippedBy == nil {
		t.Fatalf("preview app ls summary missing fields: %+v", previewAppListEnv)
	}
	h.ForcePreviewExpired(t, func(command string) string { return e.dockerExec(t, command) }, "previewapi", previewEnv)
	reapOutput := e.dockerExec(t, "/usr/local/bin/ship server env reap")
	assertContains(t, reapOutput, "Reaped preview previewapi ("+previewEnv+") branch=feature/lifecycle")
	e.dockerExec(t, "test ! -e "+identity.EnvRoot("previewapi", previewEnv))
	e.dockerExec(t, "test ! -e /etc/ship/secrets/previewapi/"+previewEnv)
	e.dockerExec(t, "test -e "+identity.EnvRoot("previewapi", "production"))
}

func (e *smokeEnv) testPreviewProtection(t *testing.T) {
	app := filepath.Join(e.tmp, "preview-protection")
	mustMkdir(t, app)
	writePreviewProtectionFixture(t, app)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")
	e.ship(t, app, nil)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: guard.example.com' http://127.0.0.1/health", "ok")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/protected")
	mustWrite(t, filepath.Join(app, "README.md"), "protected preview\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "protected preview")
	previewURL := assertOnlyURL(t, e.ship(t, app, nil))
	previewParsed, err := url.Parse(previewURL)
	if err != nil || previewParsed.Hostname() == "" {
		t.Fatalf("preview URL = %q, parse err = %v", previewURL, err)
	}
	host := previewParsed.Hostname()
	token := previewParsed.Query().Get("ship")
	if token == "" {
		t.Fatalf("preview URL missing capability token: %q", previewURL)
	}
	cleanPath := previewParsed.EscapedPath()
	if cleanPath == "" {
		cleanPath = "/"
	}
	status := func(args string) string {
		return strings.TrimSpace(e.ssh(t, "curl -sS -o /dev/null -w '%{http_code}' -H 'Host: "+host+"' "+args+" http://127.0.0.1"+cleanPath))
	}
	if got := status(""); got != "401" {
		t.Fatalf("anonymous protected preview status = %s, want 401", got)
	}

	capabilityHeaders := e.ssh(t, "curl -sS -D - -o /dev/null -H 'Host: "+host+"' "+h.ShellQuote("http://127.0.0.1"+previewParsed.RequestURI()))
	assertContains(t, capabilityHeaders, "302 Found")
	assertContains(t, capabilityHeaders, "Set-Cookie: ship="+token+"; Path=/; HttpOnly; Secure")
	assertContains(t, capabilityHeaders, "Location: "+cleanPath)
	if got := status("-H 'Cookie: ship=" + token + "'"); got != "200" {
		t.Fatalf("capability URL cookie redirect status = %s, want 200", got)
	}
	if got := status("-H 'x-ship-capability: " + token + "'"); got != "200" {
		t.Fatalf("capability header status = %s, want 200", got)
	}
	headers := e.ssh(t, "curl -fsS -H 'Host: "+host+"' -H 'x-ship-capability: "+token+"' -H 'Authorization: Bearer app-token' -H 'Cookie: first=one; ship="+token+"; last=two' http://127.0.0.1/request-headers")
	assertContains(t, headers, "authorization=Bearer app-token\n")
	assertContains(t, headers, "x-ship-capability=\n")
	assertContains(t, headers, "cookie=first=one; last=two\n")
	if strings.Contains(headers, token) {
		t.Fatalf("protected preview app received its capability token:\n%s", headers)
	}

	if again := assertOnlyURL(t, e.ship(t, app, nil, "preview", "share")); again != previewURL {
		t.Fatalf("preview share URL = %q, want %q", again, previewURL)
	}
	previewStatus := statusEnvByBranch(t, e, app, "feature/protected")
	if previewStatus.CapabilityURL != previewURL {
		t.Fatalf("status capability_url = %q, want %q", previewStatus.CapabilityURL, previewURL)
	}

	rotatedURL := assertOnlyURL(t, e.ship(t, app, nil, "preview", "share", "--rotate"))
	rotatedParsed, err := url.Parse(rotatedURL)
	if err != nil {
		t.Fatalf("rotated preview URL = %q, parse err = %v", rotatedURL, err)
	}
	rotatedToken := rotatedParsed.Query().Get("ship")
	if rotatedToken == "" || rotatedToken == token {
		t.Fatalf("rotated capability token = %q, old=%q", rotatedToken, token)
	}
	if got := strings.TrimSpace(e.ssh(t, "curl -sS -o /dev/null -w '%{http_code}' -H 'Host: "+host+"' "+h.ShellQuote("http://127.0.0.1"+previewParsed.RequestURI()))); got != "401" {
		t.Fatalf("old capability URL after rotation status = %s, want 401", got)
	}
	if got := status("-H 'x-ship-capability: " + token + "'"); got != "401" {
		t.Fatalf("old capability header after rotation status = %s, want 401", got)
	}
	if got := status("-H 'Cookie: ship=" + token + "'"); got != "401" {
		t.Fatalf("old capability cookie after rotation status = %s, want 401", got)
	}
	rotatedHeaders := e.ssh(t, "curl -sS -D - -o /dev/null -H 'Host: "+host+"' "+h.ShellQuote("http://127.0.0.1"+rotatedParsed.RequestURI()))
	assertContains(t, rotatedHeaders, "302 Found")
	if got := status("-H 'Cookie: ship=" + rotatedToken + "'"); got != "200" {
		t.Fatalf("new capability URL cookie redirect status = %s, want 200", got)
	}
	if previewStatus = statusEnvByBranch(t, e, app, "feature/protected"); previewStatus.CapabilityURL != rotatedURL {
		t.Fatalf("rotated status capability_url = %q, want %q", previewStatus.CapabilityURL, rotatedURL)
	}
	// The successful protected deploy proves its process-port probe bypassed
	// Caddy. Production remains public after capability issuance and rotation.
	e.assertRemoteBody(t, "curl -fsS -H 'Host: guard.example.com' http://127.0.0.1/health", "ok")

	e.mustRun(t, app, nil, "git", "checkout", "main")
	for _, args := range [][]string{{"preview", "share"}, {"preview", "share", "--rotate"}} {
		result := e.runCommand(t, app, []string{"SHIP_ERROR_JSON=1"}, nil, e.goBin, args...)
		if result.err == nil {
			t.Fatalf("production %s unexpectedly succeeded", strings.Join(args, " "))
		}
		assertContains(t, result.stdout+result.stderr, "share_on_production")
	}
	e.mustRun(t, app, nil, "git", "checkout", "feature/protected")

	agentKeyPath := filepath.Join(e.tmp, "preview-protection-agent")
	e.mustRun(t, e.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "preview-protection-agent", "-f", agentKeyPath)
	e.ship(t, app, nil, "box", "member", "add", agentKeyPath+".pub", "--name", "preview-protection-agent", "--role", "agent")
	agentKey, err := os.ReadFile(agentKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	ownerPrefix := e.pathPrefix
	agentPrefix := e.configureSSHWithKey(t, agentKeyPath)
	t.Cleanup(func() { e.pathPrefix = ownerPrefix })
	e.pathPrefix = agentPrefix
	agentRotate := e.runCommand(t, app, []string{"SHIP_SSH_KEY=" + string(agentKey), "SHIP_ERROR_JSON=1"}, nil, e.goBin, "preview", "share", "--rotate")
	if agentRotate.err == nil {
		t.Fatal("agent preview share rotation should require approval")
	}
	agentRotateText := agentRotate.stdout + agentRotate.stderr
	assertContains(t, agentRotateText, "approval_required")
	approvalID := approvalIDFromOutput(t, agentRotateText)
	e.pathPrefix = ownerPrefix
	e.ship(t, app, nil, "box", "approval", "grant", approvalID)
	e.pathPrefix = agentPrefix
	agentRetry := e.runCommand(t, app, []string{"SHIP_SSH_KEY=" + string(agentKey)}, nil, e.goBin, "preview", "share", "--rotate")
	if agentRetry.err != nil {
		t.Fatalf("approved agent preview share rotation should succeed: %v\nstdout:\n%s\nstderr:\n%s", agentRetry.err, agentRetry.stdout, agentRetry.stderr)
	}
	agentURL := assertOnlyURL(t, agentRetry.stdout)
	if agentURL == rotatedURL {
		t.Fatalf("approved agent rotation reused capability URL %q", agentURL)
	}
	e.pathPrefix = ownerPrefix

	protectedEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "guardapi", "feature/protected")
	capabilityFile := "/etc/ship/secrets/guardapi/" + protectedEnv + "/capability-token"
	agentParsed, err := url.Parse(agentURL)
	if err != nil {
		t.Fatalf("agent preview URL = %q, parse err = %v", agentURL, err)
	}
	agentToken := agentParsed.Query().Get("ship")
	e.dockerExec(t, "test -f "+capabilityFile)
	h.ForcePreviewExpired(t, func(command string) string { return e.dockerExec(t, command) }, "guardapi", protectedEnv)
	e.dockerExec(t, "/usr/local/bin/ship server env reap")
	e.dockerExec(t, "test ! -e "+identity.EnvRoot("guardapi", protectedEnv))
	e.dockerExec(t, "test ! -e "+capabilityFile)
	// Recreate the branch to obtain a live protected vhost after reap; its
	// new capability must reject the reaped preview's old token.
	recreatedURL := assertOnlyURL(t, e.ship(t, app, nil))
	recreatedParsed, err := url.Parse(recreatedURL)
	if err != nil || recreatedParsed.Hostname() == "" {
		t.Fatalf("recreated preview URL = %q, parse err = %v", recreatedURL, err)
	}
	if got := strings.TrimSpace(e.ssh(t, "curl -sS -o /dev/null -w '%{http_code}' -H 'Host: "+recreatedParsed.Hostname()+"' -H 'x-ship-capability: "+agentToken+"' http://127.0.0.1"+cleanPath)); got != "401" {
		t.Fatalf("reaped capability status = %s, want 401", got)
	}
}

func (e *smokeEnv) testPreviewBaseAliases(t *testing.T) {
	app := filepath.Join(e.tmp, "preview-base-alias")
	mustMkdir(t, app)
	writePreviewBaseAliasFixture(t, app)
	e.commitFixture(t, app)
	e.ship(t, app, nil)

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/base-alias")
	mustWrite(t, filepath.Join(app, "README.md"), "custom preview base alias\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "custom preview base alias")
	previewURL := assertOnlyURL(t, e.ship(t, app, nil))
	parsed, err := url.Parse(previewURL)
	if err != nil || parsed.Hostname() == "" {
		t.Fatalf("preview URL = %q, parse err = %v", previewURL, err)
	}
	if !strings.HasSuffix(parsed.Hostname(), ".preview.example.com") || !strings.HasPrefix(parsed.Hostname(), "basealias-feature-base-alias-") {
		t.Fatalf("canonical preview host = %q, want app-first custom-base host", parsed.Hostname())
	}
	token := parsed.Query().Get("ship")
	if token == "" {
		t.Fatalf("preview URL missing capability token: %q", previewURL)
	}
	if got := strings.TrimSpace(e.urlBody(t, previewURL, "/health")); got != "ok" {
		t.Fatalf("canonical preview body = %q, want ok", got)
	}

	const alias = "feature-base-alias.preview.example.com"
	if got := strings.TrimSpace(e.ssh(t, "curl -fsS -H "+h.ShellQuote("Host: "+alias)+" -H "+h.ShellQuote("x-ship-capability: "+token)+" http://127.0.0.1/health")); got != "ok" {
		t.Fatalf("preview alias body = %q, want ok", got)
	}
	if got := strings.TrimSpace(e.ssh(t, "curl -sS -o /dev/null -w '%{http_code}' -H "+h.ShellQuote("Host: "+alias)+" http://127.0.0.1/health")); got != "401" {
		t.Fatalf("anonymous preview alias status = %s, want 401", got)
	}
	rotatedURL := assertOnlyURL(t, e.ship(t, app, nil, "preview", "share", "--rotate"))
	rotated, err := url.Parse(rotatedURL)
	if err != nil || rotated.Query().Get("ship") == "" {
		t.Fatalf("rotated preview URL = %q, parse err = %v", rotatedURL, err)
	}
	if got := strings.TrimSpace(e.ssh(t, "curl -fsS -H "+h.ShellQuote("Host: "+alias)+" -H "+h.ShellQuote("x-ship-capability: "+rotated.Query().Get("ship"))+" http://127.0.0.1/health")); got != "ok" {
		t.Fatalf("preview alias body after capability rotation = %q, want ok", got)
	}

	manifestPath := filepath.Join(app, "ship.toml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, manifestPath, strings.Replace(string(manifest), "aliases = true", "aliases = false", 1))
	e.mustRun(t, app, nil, "git", "add", "ship.toml")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "disable preview alias")
	e.ship(t, app, nil)
	if got := strings.TrimSpace(e.ssh(t, "curl -sS -o /dev/null -w '%{http_code}' -H "+h.ShellQuote("Host: "+alias)+" http://127.0.0.1/health")); got == "200" {
		t.Fatalf("alias stayed routable after aliases=false deploy")
	}
}

func (e *smokeEnv) testMemberAccess(t *testing.T) {
	e.ensureSmokeHostSeed(t)
	app := filepath.Join(e.tmp, "key-api")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "keyapi"
box = "fake-vps"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"key.example.com" = "web"
`)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")

	keyPath := filepath.Join(e.tmp, "teammate")
	keyComment := filepath.Base(keyPath)
	e.mustRun(t, e.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", keyComment, "-f", keyPath)

	out := e.ship(t, app, nil, "box", "member", "add", keyPath+".pub", "--name", keyComment)
	if !strings.HasPrefix(strings.TrimSpace(out), "member added: "+keyComment+" (shipper, SHA256:") {
		t.Fatalf("unexpected member add output: %q", out)
	}
	fingerprint := fingerprintFromMemberMutation(t, out)
	again := e.ship(t, app, nil, "box", "member", "add", keyPath+".pub", "--name", keyComment)
	assertContains(t, again, "member "+keyComment+" already authorized (shipper, "+fingerprint+")")
	authorized := e.dockerExec(t, "cat /home/deploy/.ssh/authorized_keys")
	assertContains(t, authorized, keyComment)

	list := e.ship(t, app, nil, "box", "member", "ls")
	assertContains(t, list, "NAME ROLE KEY-ID TYPE CURRENT")
	assertContains(t, list, keyComment)
	assertContains(t, list, "SHA256:")
	assertContains(t, list, "ssh-ed25519")
	assertContains(t, list, "CURRENT")

	var members struct {
		Members []struct {
			Name string `json:"name"`
			Role string `json:"role"`
			Keys []struct {
				ID          string `json:"id"`
				Fingerprint string `json:"fingerprint"`
				Type        string `json:"type"`
				Current     bool   `json:"current"`
			} `json:"keys"`
		} `json:"members"`
	}
	if err := json.Unmarshal([]byte(e.ship(t, app, nil, "box", "member", "ls", "--json")), &members); err != nil {
		t.Fatal(err)
	}
	foundMember := false
	foundCurrentOwnerKey := false
	for _, member := range members.Members {
		if member.Name == keyComment && member.Role == "shipper" {
			for _, key := range member.Keys {
				// The owner runs this ls, so the teammate's key must
				// NOT carry the current-connection marker.
				if key.Type == "ssh-ed25519" && key.Fingerprint == fingerprint && !key.Current {
					foundMember = true
				}
			}
		}
		if member.Role == "owner" {
			for _, key := range member.Keys {
				if key.Current {
					foundCurrentOwnerKey = true
				}
			}
		}
	}
	if !foundMember {
		t.Fatalf("member ls --json missing added member: %+v", members.Members)
	}
	if !foundCurrentOwnerKey {
		t.Fatalf("member ls --json missing CURRENT marker on the connecting owner key: %+v", members.Members)
	}

	rename := keyComment + "-renamed"
	e.ship(t, app, nil, "box", "member", "rename", keyComment, rename)
	assertContains(t, e.ship(t, app, nil, "box", "member", "ls"), rename)
	e.ship(t, app, nil, "box", "member", "role", rename, "owner")
	e.ship(t, app, nil, "box", "member", "role", rename, "shipper")
	rotationKeyPath := filepath.Join(e.tmp, "rotation-key")
	e.mustRun(t, e.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "rotation-key", "-f", rotationKeyPath)
	rotationResult := e.runShip(t, app, nil, "box", "member", "add", rotationKeyPath+".pub", "--name", rename)
	if rotationResult.err != nil {
		t.Fatalf("rotation member add failed: %v\nstdout:\n%s\nstderr:\n%s", rotationResult.err, rotationResult.stdout, rotationResult.stderr)
	}
	assertContains(t, rotationResult.stdout, "member added: "+rename)
	// The rotation guidance is progress/next-step text, so it rides
	// stderr per the output contract; the retire command with --key is
	// part of that guidance.
	assertContains(t, rotationResult.stderr, "rotation: verify a fresh connection with the new key")
	rotationOutput := rotationResult.stderr
	oldShortID := ""
	for _, line := range strings.Split(rotationOutput, "\n") {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "--key" {
				oldShortID = fields[i+1]
			}
		}
	}
	if !strings.HasPrefix(oldShortID, "SHA256:") {
		t.Fatalf("rotation output missing printed SHA256 short key id: %q", rotationOutput)
	}
	assertContains(t, rotationOutput, "--key "+oldShortID)

	unknown := e.runShip(t, app, nil, "box", "member", "rm", "ghost")
	if unknown.err == nil {
		t.Fatal("member rm unknown should fail")
	}
	assertContains(t, unknown.stderr, "member rm failed")
	assertContains(t, unknown.stderr, "current members: fake-vps-smoke, "+rename)

	teammatePrefix := e.configureSSHWithKey(t, keyPath)
	oldPrefix := e.pathPrefix
	t.Cleanup(func() { e.pathPrefix = oldPrefix })
	e.pathPrefix = teammatePrefix
	teammateKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	teammateEnv := []string{"SHIP_SSH_KEY=" + string(teammateKey)}

	teammateDeploy := e.runCommand(t, app, teammateEnv, nil, e.goBin)
	if teammateDeploy.err != nil {
		t.Fatalf("ship with added key should succeed: %v\nstdout:\n%s\nstderr:\n%s", teammateDeploy.err, teammateDeploy.stdout, teammateDeploy.stderr)
	}
	url := strings.TrimSpace(teammateDeploy.stdout)
	e.pathPrefix = oldPrefix
	h.AssertURLServes200(t, func(command string) string { return e.ssh(t, command) }, url)
	status := statusEnvByClass(t, e, app, "production")
	if status.ShippedBy == nil || status.ShippedBy.SSHKeyComment != keyComment {
		t.Fatalf("ship with added key should attribute the teammate key, got %+v", status.ShippedBy)
	}

	rmOut := e.ship(t, app, nil, "box", "member", "rm", rename, "--key", oldShortID)
	assertContains(t, rmOut, "removed 1 SSH key for "+rename)
	e.pathPrefix = teammatePrefix
	revoked := e.runCommand(t, app, teammateEnv, nil, e.goBin)
	if revoked.err == nil {
		t.Fatal("ship with removed member key should fail")
	}
	e.pathPrefix = oldPrefix

	guard := e.runShip(t, app, nil, "box", "member", "rm", "fake-vps-smoke")
	if guard.err == nil {
		t.Fatal("member rm should refuse to remove the last key")
	}
	assertContains(t, guard.stderr, "member mutation refused")
	assertContains(t, guard.stderr, "no effective owner key")
}

func (e *smokeEnv) testAgentRoleApprovalFlow(t *testing.T) {
	e.ensureSmokeHostSeed(t)
	sink := e.startWebhookSink(t)

	app := filepath.Join(e.tmp, "role-approval")
	mustMkdir(t, app)
	writeRoleApprovalFixture(t, app, sink.URL("/hook"))
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")
	e.ship(t, app, nil)
	e.ship(t, app, nil, "box", "webhook", "fake-vps", sink.URL("/box"))

	agentKeyPath := filepath.Join(e.tmp, "agent-role")
	e.mustRun(t, e.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "agent-role", "-f", agentKeyPath)
	added := e.ship(t, app, nil, "box", "member", "add", agentKeyPath+".pub", "--name", "agent-role", "--role", "agent")
	agentFingerprint := fingerprintFromMemberMutation(t, added)
	assertContains(t, added, "member added: agent-role (agent, "+agentFingerprint+")")
	authorized := e.dockerExec(t, "cat /home/deploy/.ssh/authorized_keys")
	assertContains(t, authorized, `command="/usr/local/bin/ship server agent-shell --member-fingerprint `+agentFingerprint+`",restrict ssh-ed25519`)
	assertContains(t, authorized, "fake-vps-smoke")
	ownerLine := e.dockerExec(t, "grep 'fake-vps-smoke' /home/deploy/.ssh/authorized_keys")
	assertNotContains(t, ownerLine, "agent-shell")

	agentKey, err := os.ReadFile(agentKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	agentEnv := []string{"SHIP_SSH_KEY=" + string(agentKey)}
	ownerPrefix := e.pathPrefix
	agentPrefix := e.configureSSHWithKey(t, agentKeyPath)
	setAgent := func() { e.pathPrefix = agentPrefix }
	setOwner := func() { e.pathPrefix = ownerPrefix }
	t.Cleanup(setOwner)

	shipperKeyPath := filepath.Join(e.tmp, "shipper-role")
	e.mustRun(t, e.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "shipper-role", "-f", shipperKeyPath)
	e.ship(t, app, nil, "box", "member", "add", shipperKeyPath+".pub", "--name", "shipper-role", "--role", "shipper")
	shipperKey, err := os.ReadFile(shipperKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	shipperEnv := []string{"SHIP_SSH_KEY=" + string(shipperKey)}
	shipperPrefix := e.configureSSHWithKey(t, shipperKeyPath)
	setShipper := func() { e.pathPrefix = shipperPrefix }
	setShipper()
	configDenied := e.runCommand(t, app, shipperEnv, nil, e.goBin, "box", "config", "fake-vps", "set", "webhook.url", sink.URL("/shipper"))
	if configDenied.err == nil {
		t.Fatal("shipper box config set should require approval")
	}
	configID := approvalIDFromOutput(t, configDenied.stdout+configDenied.stderr)
	configRequested := sink.waitForEvent(t, webhookEventApprovalRequested)
	assertWebhookSmokeField(t, configRequested, "_sink_path", "/box")
	assertWebhookSmokeNested(t, configRequested, "why.target.summary", "set box config webhook.url")
	selfApprove := e.runCommand(t, app, shipperEnv, nil, e.goBin, "box", "approval", "grant", configID)
	if selfApprove.err == nil {
		t.Fatal("shipper must not self-approve an owner-gated request")
	}
	assertContains(t, selfApprove.stdout+selfApprove.stderr, "requests cannot be self-approved")
	assertContains(t, selfApprove.stdout+selfApprove.stderr, "another owner")
	setOwner()
	e.ship(t, app, nil, "box", "approval", "grant", configID)
	setShipper()
	if retry := e.runCommand(t, app, shipperEnv, nil, e.goBin, "box", "config", "fake-vps", "set", "webhook.url", sink.URL("/shipper")); retry.err != nil {
		t.Fatalf("approved shipper box config retry should succeed: %v\nstdout:\n%s\nstderr:\n%s", retry.err, retry.stdout, retry.stderr)
	}
	setOwner()
	e.ship(t, app, nil, "box", "config", "fake-vps", "set", "webhook.url", sink.URL("/box"))

	// Clear consumed events so the next approval_requested wait sees the
	// agent's box-webhook request, not the earlier shipper box-config one.
	e.dockerExec(t, "rm -f "+sink.eventsPath)
	setAgent()
	webhookDenied := e.runCommand(t, app, agentEnv, nil, e.goBin, "box", "webhook", "fake-vps", sink.URL("/moved"))
	if webhookDenied.err == nil {
		t.Fatal("agent box webhook set should require approval")
	}
	webhookID := approvalIDFromOutput(t, webhookDenied.stdout+webhookDenied.stderr)
	webhookRequested := sink.waitForEvent(t, webhookEventApprovalRequested)
	assertWebhookSmokeField(t, webhookRequested, "_sink_path", "/box")
	assertWebhookSmokeNested(t, webhookRequested, "why.target.summary", "box webhook set")
	setShipper()
	shipperApprove := e.runCommand(t, app, shipperEnv, nil, e.goBin, "box", "approval", "grant", webhookID)
	if shipperApprove.err == nil {
		t.Fatal("shipper must not grant an owner-gated request")
	}
	assertContains(t, shipperApprove.stdout+shipperApprove.stderr, "request requires owner")
	assertContains(t, shipperApprove.stdout+shipperApprove.stderr, "ask an owner")
	setOwner()
	e.ship(t, app, nil, "box", "approval", "grant", webhookID)
	setAgent()
	if retry := e.runCommand(t, app, agentEnv, nil, e.goBin, "box", "webhook", "fake-vps", sink.URL("/moved")); retry.err != nil {
		t.Fatalf("approved agent box webhook retry should succeed: %v\nstdout:\n%s\nstderr:\n%s", retry.err, retry.stdout, retry.stderr)
	}
	setOwner()
	e.ship(t, app, nil, "box", "webhook", "fake-vps", sink.URL("/box"))
	e.dockerExec(t, "rm -f "+sink.eventsPath)
	setAgent()
	interactive := e.run(t, e.repoRoot, nil, e.sshBin(), "fake-vps")
	if interactive.err == nil {
		t.Fatal("agent key should not open an interactive ssh session")
	}
	assertContains(t, interactive.stdout+interactive.stderr, "agent_shell_refused")

	arbitrary := e.run(t, e.repoRoot, nil, e.sshBin(), "fake-vps", "ls")
	if arbitrary.err == nil {
		t.Fatal("agent key should not run arbitrary ssh commands")
	}
	assertContains(t, arbitrary.stdout+arbitrary.stderr, "agent_shell_refused")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/agent")
	mustWrite(t, filepath.Join(app, "README.md"), "agent preview ship\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "agent preview")
	firstPreviewRelease := gitRelease(t, e, app)
	setAgent()
	preview := e.runCommand(t, app, agentEnv, nil, e.goBin)
	if preview.err != nil {
		t.Fatalf("agent preview ship should be allowed: %v\nstdout:\n%s\nstderr:\n%s", preview.err, preview.stdout, preview.stderr)
	}
	previewEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "roleapi", "feature/agent")

	mustWrite(t, filepath.Join(app, "README.md"), "agent second preview ship\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "agent preview second")
	secondPreview := e.runCommand(t, app, agentEnv, nil, e.goBin)
	if secondPreview.err != nil {
		t.Fatalf("agent second preview ship should be allowed: %v\nstdout:\n%s\nstderr:\n%s", secondPreview.err, secondPreview.stdout, secondPreview.stderr)
	}
	lyingRollback := e.run(t, e.repoRoot, nil, e.sshBin(), "fake-vps",
		"sudo -n /usr/local/bin/ship server app --member-fingerprint "+e.ownerFingerprint+
			" rollback --ssh-key-comment liar --git-author liar roleapi "+previewEnv+" "+firstPreviewRelease)
	if lyingRollback.err != nil {
		t.Fatalf("agent helper call with lying fingerprint should be pinned and allowed: %v\nstdout:\n%s\nstderr:\n%s", lyingRollback.err, lyingRollback.stdout, lyingRollback.stderr)
	}
	setOwner()
	previewJournal := e.ssh(t, "cat "+identity.DeployJournalFile("roleapi", previewEnv))
	assertJournalLineContains(t, previewJournal, `"outcome":"rolled_back"`, `"name":"agent-role"`, `"role":"agent"`)

	e.mustRun(t, app, nil, "git", "checkout", "main")
	mustWrite(t, filepath.Join(app, "README.md"), "agent production ship needs approval\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "agent production approval")
	prodRelease := gitRelease(t, e, app)
	setAgent()
	denied := e.runCommand(t, app, agentEnv, nil, e.goBin)
	if denied.err == nil {
		t.Fatal("agent production ship should require approval")
	}
	deniedText := denied.stdout + denied.stderr
	assertContains(t, deniedText, "approval required for ship app=roleapi env=production class=production release="+prodRelease)
	assertContains(t, deniedText, "agent-role (agent) requested ship app=roleapi env=production class=production release="+prodRelease)
	id := approvalIDFromOutput(t, deniedText)

	requested := sink.waitForEvent(t, webhookEventApprovalRequested)
	assertWebhookSmokeField(t, requested, "_sink_path", "/box")
	assertWebhookSmokeNested(t, requested, "remediation.command", "ship box approval grant "+id+" fake-vps")

	setOwner()
	approved := e.ship(t, app, nil, "box", "approval", "grant", id)
	assertContains(t, approved, "approved "+id+" for agent-role (ship app=roleapi env=production class=production release="+prodRelease+")")

	setAgent()
	retry := e.runCommand(t, app, agentEnv, nil, e.goBin)
	if retry.err != nil {
		t.Fatalf("approved agent production retry should succeed: %v\nstdout:\n%s\nstderr:\n%s", retry.err, retry.stdout, retry.stderr)
	}

	second := e.runCommand(t, app, agentEnv, nil, e.goBin)
	if second.err == nil {
		t.Fatal("consumed approval should not authorize a second production ship")
	}
	secondText := second.stdout + second.stderr
	secondID := approvalIDFromOutput(t, secondText)
	if secondID == id {
		t.Fatalf("second approval reused consumed id %s\noutput:\n%s", id, secondText)
	}

	agentApprove := e.runCommand(t, app, agentEnv, nil, e.goBin, "box", "approval", "grant", secondID)
	if agentApprove.err == nil {
		t.Fatal("agent approving should be hard-denied")
	}
	assertContains(t, agentApprove.stdout+agentApprove.stderr, "operation denied")
	assertContains(t, agentApprove.stdout+agentApprove.stderr, "requests cannot be self-approved")
	assertNotContains(t, agentApprove.stdout+agentApprove.stderr, "approval required")

	setOwner()
	status := e.ship(t, app, nil, "status")
	assertContains(t, status, "1 approvals pending — ship box approval ls fake-vps")

	listing := e.ship(t, app, nil, "box", "approval", "ls")
	assertContains(t, listing, "ID MEMBER REQUEST EXPIRES")
	assertContains(t, listing, secondID+" agent-role ship app=roleapi env=production class=production release="+prodRelease+" ")

	setAgent()
	const updateHelperVersion = "v0.4.0"
	const updateClientVersion = "v0.4.1"
	oldHelper := e.buildStampedShip(t, "agent-update-helper-old", "linux", updateHelperVersion)
	updateClient := e.buildStampedShip(t, "agent-update-client", "", updateClientVersion)
	e.mustRun(t, e.repoRoot, nil, "docker", "cp", oldHelper, e.container+":/usr/local/bin/ship")
	updateDenied := e.runCommand(t, app, agentEnv, nil, updateClient, "box", "update", "fake-vps")
	if updateDenied.err == nil {
		t.Fatal("agent box update should require approval")
	}
	assertContains(t, updateDenied.stdout+updateDenied.stderr, "approval required for update box")
	e.mustRun(t, e.repoRoot, nil, "docker", "cp", e.linuxBin, e.container+":/usr/local/bin/ship")

	setOwner()
	journal := e.ssh(t, "cat "+identity.DeployJournalFile("roleapi", productionEnv))
	assertContains(t, journal, `"name":"agent-role"`)
	assertContains(t, journal, `"role":"agent"`)
}

func (e *smokeEnv) testDataForks(t *testing.T) {
	e.ensureSmokeHostSeed(t)
	app := filepath.Join(e.tmp, "data-api")
	mustMkdir(t, app)
	writeDataForkFixture(t, app, "dataapi", "data.example.com")
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")
	e.ship(t, app, nil)
	e.ship(t, app, nil, "exec", "sqlite3", "/data/app.db", "CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO items(name) VALUES ('one'), ('two'), ('three');")
	e.ship(t, app, nil, "exec", "sh", "-c", "mkdir -p /data/uploads && printf prod-upload > /data/uploads/file.txt")
	prodHash := remoteDataHash(t, e, "dataapi", productionEnv)

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/data")
	mustWrite(t, filepath.Join(app, "README.md"), "data preview\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "data preview")
	previewURL := assertOnlyURL(t, e.ship(t, app, nil))
	previewEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "dataapi", "feature/data")

	forkOut := e.runShip(t, app, nil, "data", "fork")
	if forkOut.err != nil {
		t.Fatalf("data fork failed: %v\nstdout:\n%s\nstderr:\n%s", forkOut.err, forkOut.stdout, forkOut.stderr)
	}
	if forkOut.stdout != previewURL+"\n" {
		t.Fatalf("data fork stdout = %q, want only preview URL", forkOut.stdout)
	}
	for _, want := range []string{
		"Forked data for Preview feature/data\n",
		"app.db ",
		"(sqlite)",
		"uploads/file.txt",
		"note: Production data, including any PII, now exists in this less-guarded Preview.\n",
	} {
		assertContains(t, forkOut.stderr, want)
	}
	if got := strings.TrimSpace(e.urlBody(t, previewURL, "/data-count")); got != "3" {
		t.Fatalf("preview row count after fork = %q, want 3", got)
	}
	if got := strings.TrimSpace(e.urlBody(t, previewURL, "/upload-file")); got != "prod-upload" {
		t.Fatalf("preview upload after fork = %q", got)
	}
	if got := remoteDataHash(t, e, "dataapi", productionEnv); got != prodHash {
		t.Fatalf("prod data changed after fork:\nbefore:\n%s\nafter:\n%s", prodHash, got)
	}

	snapshot := strings.TrimSpace(e.ship(t, app, nil, "data", "save"))
	t.Cleanup(func() { _ = os.Remove(snapshot) })
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("data save did not create local snapshot %q: %v", snapshot, err)
	}
	assertContains(t, e.ship(t, app, nil, "data", "ls"), filepath.Base(snapshot))
	e.dockerExec(t, "test -z \"$(find "+identity.EnvRoot("dataapi", previewEnv)+" -maxdepth 1 -name '.data-save-*' -print)\"")
	e.ship(t, app, nil, "exec", "sqlite3", "/data/app.db", "INSERT INTO items(name) VALUES ('after-save');")
	if got := strings.TrimSpace(e.urlBody(t, previewURL, "/data-count")); got != "4" {
		t.Fatalf("preview row count after write = %q, want 4", got)
	}
	e.ship(t, app, nil, "data", "restore", snapshot)
	if got := strings.TrimSpace(e.urlBody(t, previewURL, "/data-count")); got != "3" {
		t.Fatalf("preview row count after restore = %q, want 3", got)
	}
	badSnapshot := filepath.Join(e.tmp, "corrupt.data.tar.gz")
	if err := os.WriteFile(badSnapshot, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	badRestore := e.runCommand(t, app, []string{"SHIP_ERROR_JSON=1"}, nil, e.goBin, "data", "restore", badSnapshot)
	if badRestore.err == nil {
		t.Fatal("corrupt snapshot restore should fail")
	}
	assertContains(t, badRestore.stdout+badRestore.stderr, "data_snapshot_invalid")
	if got := strings.TrimSpace(e.urlBody(t, previewURL, "/data-count")); got != "3" {
		t.Fatalf("corrupt restore changed data: %q", got)
	}

	e.ship(t, app, nil, "exec", "sqlite3", "/data/app.db", "INSERT INTO items(name) VALUES ('preview-only');")
	if got := strings.TrimSpace(e.urlBody(t, previewURL, "/data-count")); got != "4" {
		t.Fatalf("preview write row count = %q, want 4", got)
	}
	e.ship(t, app, nil, "data", "fork")
	if got := strings.TrimSpace(e.urlBody(t, previewURL, "/data-count")); got != "3" {
		t.Fatalf("preview row count after refresh = %q, want 3", got)
	}
	if got := remoteDataHash(t, e, "dataapi", productionEnv); got != prodHash {
		t.Fatalf("prod data changed after refresh:\nbefore:\n%s\nafter:\n%s", prodHash, got)
	}

	resetOut := e.runShip(t, app, nil, "data", "reset")
	if resetOut.err != nil {
		t.Fatalf("data reset failed: %v\nstdout:\n%s\nstderr:\n%s", resetOut.err, resetOut.stdout, resetOut.stderr)
	}
	if resetOut.stdout != previewURL+"\n" {
		t.Fatalf("data reset stdout = %q, want only preview URL", resetOut.stdout)
	}
	assertContains(t, resetOut.stderr, "Reset data for Preview feature/data\n")
	if got := strings.TrimSpace(e.urlBody(t, previewURL, "/data-count")); got != "missing" {
		t.Fatalf("preview row count after data reset = %q, want missing", got)
	}
	e.dockerExec(t, "test ! -e "+identity.DataDir("dataapi", previewEnv)+"/app.db")
	h.AssertURLServes200(t, func(command string) string { return e.ssh(t, command) }, previewURL)

	e.mustRun(t, app, nil, "git", "checkout", "main")
	prodRestore := e.runShip(t, app, nil, "data", "restore", snapshot)
	if prodRestore.err == nil {
		t.Fatal("production data restore without --confirm should fail")
	}
	assertContains(t, prodRestore.stdout+prodRestore.stderr, "Production restore requires --confirm dataapi")
	onProd := e.runShip(t, app, nil, "data", "fork")
	if onProd.err == nil {
		t.Fatal("data fork on production branch should fail")
	}
	assertContains(t, onProd.stdout+onProd.stderr, "data command refused on Production")
	assertContains(t, onProd.stdout+onProd.stderr, "branch \"main\" maps to Production; data commands target Preview branches only")
	assertContains(t, onProd.stdout+onProd.stderr, "next: git checkout <preview-branch>")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/not-shipped")
	noPreview := e.runShip(t, app, nil, "data", "fork")
	if noPreview.err == nil {
		t.Fatal("data fork without preview env should fail")
	}
	assertContains(t, noPreview.stdout+noPreview.stderr, "preview environment lookup failed")
	assertContains(t, noPreview.stdout+noPreview.stderr, "no Preview environment exists for branch \"feature/not-shipped\"")
	assertContains(t, noPreview.stdout+noPreview.stderr, "next: ship")

	e.mustRun(t, app, nil, "git", "checkout", "feature/data")
	agentKeyPath := filepath.Join(e.tmp, "data-agent")
	e.mustRun(t, e.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "data-agent", "-f", agentKeyPath)
	added := e.ship(t, app, nil, "box", "member", "add", agentKeyPath+".pub", "--name", "data-agent", "--role", "agent")
	assertContains(t, added, "member added: data-agent (agent, ")
	agentKey, err := os.ReadFile(agentKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	agentEnv := []string{"SHIP_SSH_KEY=" + string(agentKey)}
	ownerPrefix := e.pathPrefix
	agentPrefix := e.configureSSHWithKey(t, agentKeyPath)
	t.Cleanup(func() { e.pathPrefix = ownerPrefix })
	e.pathPrefix = agentPrefix
	denied := e.runCommand(t, app, agentEnv, nil, e.goBin, "data", "fork")
	if denied.err == nil {
		t.Fatal("agent data fork should require approval")
	}
	deniedText := denied.stdout + denied.stderr
	assertContains(t, deniedText, "approval required for fork app=dataapi env="+previewEnv+" class=preview data=fork from=production")
	deniedSave := e.runCommand(t, app, agentEnv, nil, e.goBin, "data", "save")
	if deniedSave.err == nil {
		t.Fatal("agent data save should require approval")
	}
	assertContains(t, deniedSave.stdout+deniedSave.stderr, "approval required for save app=dataapi env="+previewEnv+" class=preview data=save")
	deniedRestore := e.runCommand(t, app, agentEnv, nil, e.goBin, "data", "restore", snapshot)
	if deniedRestore.err == nil {
		t.Fatal("agent data restore should require approval")
	}
	assertContains(t, deniedRestore.stdout+deniedRestore.stderr, "approval required for restore app=dataapi env="+previewEnv+" class=preview data=restore")
	approvalID := approvalIDFromOutput(t, deniedText)
	e.pathPrefix = ownerPrefix
	approved := e.ship(t, app, nil, "box", "approval", "grant", approvalID)
	assertContains(t, approved, "approved "+approvalID+" for data-agent")
	e.pathPrefix = agentPrefix
	retry := e.runCommand(t, app, agentEnv, nil, e.goBin, "data", "fork")
	if retry.err != nil {
		t.Fatalf("approved agent data fork should succeed: %v\nstdout:\n%s\nstderr:\n%s", retry.err, retry.stdout, retry.stderr)
	}
	e.pathPrefix = ownerPrefix

	uploadsOnly := filepath.Join(e.tmp, "uploads-only")
	mustMkdir(t, uploadsOnly)
	writeDataForkFixture(t, uploadsOnly, "uploadsonly", "uploads-only.example.com")
	e.commitFixture(t, uploadsOnly)
	e.mustRun(t, uploadsOnly, nil, "git", "checkout", "-B", "main")
	e.ship(t, uploadsOnly, nil)
	e.ship(t, uploadsOnly, nil, "exec", "sh", "-c", "mkdir -p /data/uploads && printf only-upload > /data/uploads/file.txt")
	e.mustRun(t, uploadsOnly, nil, "git", "checkout", "-B", "feature/uploads")
	mustWrite(t, filepath.Join(uploadsOnly, "README.md"), "uploads preview\n")
	e.mustRun(t, uploadsOnly, nil, "git", "add", ".")
	e.mustRun(t, uploadsOnly, nil, "git", "commit", "-q", "-m", "uploads preview")
	uploadsURL := assertOnlyURL(t, e.ship(t, uploadsOnly, nil))
	noSQLiteOut := e.runShip(t, uploadsOnly, nil, "data", "fork")
	if noSQLiteOut.err != nil {
		t.Fatalf("uploads-only data fork failed: %v\nstdout:\n%s\nstderr:\n%s", noSQLiteOut.err, noSQLiteOut.stdout, noSQLiteOut.stderr)
	}
	if noSQLiteOut.stdout != uploadsURL+"\n" {
		t.Fatalf("uploads-only data fork stdout = %q, want only preview URL", noSQLiteOut.stdout)
	}
	assertContains(t, noSQLiteOut.stderr, "note: No SQLite files found; copied non-database files from /data only.\n")
	if got := strings.TrimSpace(e.urlBody(t, uploadsURL, "/upload-file")); got != "only-upload" {
		t.Fatalf("uploads-only forked file = %q, want only-upload", got)
	}
}

func (e *smokeEnv) testBoxAppRm(t *testing.T) {
	app := filepath.Join(e.tmp, "box-rm-api")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "boxrmapi"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"boxrm.example.com" = "web"
`)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")
	e.ship(t, app, []byte("prod-cleanup"), "secret", "set", "cleanup_key")
	e.ship(t, app, nil)

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/box-rm")
	e.ship(t, app, []byte("preview-cleanup"), "secret", "set", "cleanup_key", "--branch", "feature/box-rm")
	e.ship(t, app, nil)
	previewEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "boxrmapi", "feature/box-rm")
	e.dockerExec(t, "test -f /etc/ship/secrets/boxrmapi/"+previewEnv+"/capability-token")

	missingConfirm := e.runShip(t, e.repoRoot, nil, "box", "app", "rm", "boxrmapi", "fake-vps")
	if missingConfirm.err == nil {
		t.Fatal("box app rm without confirmation should fail")
	}
	assertContains(t, missingConfirm.stdout+missingConfirm.stderr, "box app rm requires --confirm boxrmapi")

	out := e.ship(t, e.repoRoot, nil, "box", "app", "rm", "boxrmapi", "fake-vps", "--confirm", "boxrmapi")
	assertContains(t, out, "Destroying boxrmapi (2 envs)")
	assertContains(t, out, "Destroyed boxrmapi ("+productionEnv+")")
	assertContains(t, out, "Destroyed boxrmapi ("+previewEnv+")")
	assertContains(t, out, "secrets: purged")

	for _, env := range []string{productionEnv, previewEnv} {
		e.dockerExec(t, "test ! -e "+identity.EnvRoot("boxrmapi", env))
		e.dockerExec(t, "test ! -e /etc/ship/secrets/boxrmapi/"+env)
		e.dockerExec(t, "test ! -e "+identity.CaddyFragmentFile("boxrmapi", env))
	}
	// box app rm must purge the Preview capability credential with its environment.
	e.dockerExec(t, "test ! -e /etc/ship/secrets/boxrmapi/"+previewEnv+"/capability-token")
	e.dockerExec(t, "test ! -e /etc/ship/secrets/boxrmapi")
}

func (e *smokeEnv) testRollback(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	oldRelease := strings.TrimSpace(e.mustRun(t, app, nil, "git", "rev-parse", "--short=12", "HEAD"))

	manifestPath := filepath.Join(app, "ship.toml")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	nextManifest := strings.Replace(string(manifestBytes), "port = 3000", "port = 3333", 1)
	if nextManifest == string(manifestBytes) {
		t.Fatal("test fixture did not contain port = 3000")
	}
	mustWrite(t, manifestPath, nextManifest)
	mustWrite(t, filepath.Join(app, "README.md"), "second release\n")
	e.commitFixture(t, app)
	newRelease := strings.TrimSpace(e.mustRun(t, app, nil, "git", "rev-parse", "--short=12", "HEAD"))
	if newRelease == oldRelease {
		t.Fatal("expected fixture commit to produce a new release")
	}
	e.ship(t, app, nil)
	newFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("api", productionEnv))
	assertContains(t, newFragment, ":"+"3333")

	prodStatus := statusEnvByClass(t, e, app, "production")
	rollbackText := e.ship(t, app, nil, "rollback")
	assertContains(t, rollbackText, "Rolled back Production "+prodStatus.Branch+" from "+newRelease+" to "+oldRelease)
	assertContains(t, rollbackText, "web")

	status := statusEnvByClass(t, e, app, "production")
	if len(status.Processes) != 1 || status.Processes[0].Process != "web" || status.Processes[0].Release != oldRelease {
		t.Fatalf("status did not report rolled-back release %s: %+v", oldRelease, status.Processes)
	}
	rolledBackFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("api", productionEnv))
	assertContains(t, rolledBackFragment, identity.ContainerName("api", productionEnv, "web", oldRelease)+":3000")
	if strings.Contains(rolledBackFragment, ":3333") {
		t.Fatalf("rollback should restore the old manifest route shape, got:\n%s", rolledBackFragment)
	}
	appliedManifest := e.ssh(t, "cat "+identity.ManifestFile("api", productionEnv))
	assertContains(t, appliedManifest, "port = 3000")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: api.example.com' http://127.0.0.1/health", "ok")

	why := e.ship(t, app, nil, "why")
	assertContains(t, why, "Rollback completed for Production "+prodStatus.Branch)
	assertContains(t, why, "release: "+oldRelease+" (from "+newRelease+")")
	assertContains(t, why, "traffic: release "+oldRelease+" is live.")
	assertContains(t, why, "shipped by: Smoke <smoke@example.com> (ssh key: fake-vps-smoke)")
	var whyJSON smokeWhyEntry
	rawWhyJSON := e.ship(t, app, nil, "why", "--json")
	if err := json.Unmarshal([]byte(rawWhyJSON), &whyJSON); err != nil {
		t.Fatalf("why --json output not parseable as JSON: %v\nraw:\n%s", err, rawWhyJSON)
	}
	if whyJSON.Outcome != "rolled_back" || whyJSON.PreviousRelease != newRelease || whyJSON.AttemptedRelease != oldRelease {
		t.Fatalf("unexpected rollback journal entry: %+v", whyJSON)
	}
}

func (e *smokeEnv) testImagePruning(t *testing.T) {
	e.ensureSmokeHostSeed(t)
	app := filepath.Join(e.tmp, "image-prune-api")
	mustMkdir(t, app)
	writeImagePruneFixture(t, app)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")

	prodReleases := []string{gitRelease(t, e, app)}
	e.ship(t, app, nil)
	for i := 2; i <= 7; i++ {
		prodReleases = append(prodReleases, commitImagePruneChange(t, e, app, fmt.Sprintf("prod release %d", i)))
		e.ship(t, app, nil)
	}
	assertFakeImageReleases(t, e, "imgprune", productionEnv, prodReleases[2:]...)
	assertFakeImageAbsent(t, e, "imgprune", productionEnv, prodReleases[0])
	assertFakeImageAbsent(t, e, "imgprune", productionEnv, prodReleases[1])
	status := statusEnvByClass(t, e, app, "production")
	if status.Release != prodReleases[len(prodReleases)-1] {
		t.Fatalf("prod live release = %s, want %s", status.Release, prodReleases[len(prodReleases)-1])
	}

	rollbackTarget := prodReleases[3]
	e.ship(t, app, nil, "rollback", rollbackTarget)
	nextRelease := commitImagePruneChange(t, e, app, "prod release 8 after rollback")
	e.ship(t, app, nil)
	assertFakeImagePresent(t, e, "imgprune", productionEnv, rollbackTarget)
	assertFakeImageReleases(t, e, "imgprune", productionEnv,
		rollbackTarget,
		prodReleases[4],
		prodReleases[5],
		prodReleases[6],
		nextRelease,
	)

	e.dockerExec(t, "touch /run/fake-podman/fail-rmi")
	pruneFailureRelease := commitImagePruneChange(t, e, app, "prod release 9 with prune failure")
	pruneFailure := e.runShip(t, app, nil)
	e.dockerExec(t, "rm -f /run/fake-podman/fail-rmi")
	if pruneFailure.err != nil {
		t.Fatalf("deploy should succeed when image pruning fails: %v\nstdout:\n%s\nstderr:\n%s", pruneFailure.err, pruneFailure.stdout, pruneFailure.stderr)
	}
	assertContains(t, pruneFailure.stdout, "https://img-prune.example.com")
	assertFakeImagePresent(t, e, "imgprune", productionEnv, pruneFailureRelease)

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/image-pruning")
	previewReleases := []string{gitRelease(t, e, app)}
	e.ship(t, app, nil)
	previewEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "imgprune", "feature/image-pruning")
	for i := 2; i <= 4; i++ {
		previewReleases = append(previewReleases, commitImagePruneChange(t, e, app, fmt.Sprintf("preview release %d", i)))
		e.ship(t, app, nil)
	}
	assertFakeImageReleases(t, e, "imgprune", previewEnv, previewReleases[2:]...)
	assertFakeImageAbsent(t, e, "imgprune", previewEnv, previewReleases[0])
	assertFakeImageAbsent(t, e, "imgprune", previewEnv, previewReleases[1])

	h.ForcePreviewExpired(t, func(command string) string { return e.dockerExec(t, command) }, "imgprune", previewEnv)
	e.dockerExec(t, "/usr/local/bin/ship server env reap")
	assertFakeImageReleases(t, e, "imgprune", previewEnv)
	e.dockerExec(t, "test ! -e "+identity.EnvRoot("imgprune", previewEnv))
	e.dockerExec(t, "test ! -e /etc/ship/secrets/imgprune/"+previewEnv)
}

func (e *smokeEnv) testConcurrentDeploys(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	start := make(chan struct{})
	results := make(chan commandResult, 2)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- e.runShip(t, app, nil)
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent deploy failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
		}
		assertEqual(t, result.stdout, "https://api.example.com\n")
	}

	status := e.ship(t, app, nil, "status")
	assertContains(t, status, "Production ")
	assertContains(t, status, "health=healthy")
}

func (e *smokeEnv) testRemovedProcessReconciliation(t *testing.T) {
	app := filepath.Join(e.tmp, "prune-api")
	mustMkdir(t, app)
	writePruneFixture(t, app)
	e.commitFixture(t, app)

	e.ship(t, app, nil)
	oldRelease := gitRelease(t, e, app)
	oldWorker := identity.ContainerName("prune", productionEnv, "worker", oldRelease)
	e.dockerExec(t, "test -f /run/fake-podman/containers/"+oldWorker+".labels")

	manifestPath := filepath.Join(app, "ship.toml")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	nextManifest := strings.Replace(string(manifestBytes), `worker = "sleep 3600"
`, "", 1)
	if nextManifest == string(manifestBytes) {
		t.Fatal("test fixture did not contain worker process")
	}
	mustWrite(t, manifestPath, nextManifest)
	mustWrite(t, filepath.Join(app, "README.md"), "worker removed\n")
	e.commitFixture(t, app)
	e.ship(t, app, nil)

	e.dockerExec(t, "test ! -f /run/fake-podman/containers/"+oldWorker+".labels")
	statusJSON := e.ship(t, app, nil, "status", "--json")
	if strings.Contains(statusJSON, `"process": "worker"`) {
		t.Fatalf("removed worker still appears in status:\n%s", statusJSON)
	}
}

func (e *smokeEnv) testStaticOnlyAppLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "static-site")
	mustMkdir(t, app)
	writeStaticFixture(t, app)
	e.commitFixture(t, app)

	e.ship(t, app, nil)
	oldRelease := currentStaticReleaseFor(t, e, "site", productionEnv)
	staticReleaseManifest := e.ssh(t, "cat "+identity.ReleaseManifestFile("site", productionEnv, oldRelease))
	assertContains(t, staticReleaseManifest, `static = "dist"`)

	status := e.ship(t, app, nil, "status")
	assertContains(t, status, "Production ")
	assertContains(t, status, "release="+oldRelease)
	assertContains(t, status, "health=healthy")

	rawListJSON := e.ship(t, app, nil, "box", "app", "ls", "--json")
	var listPayload struct {
		Apps []struct {
			App  string `json:"app"`
			Envs []struct {
				Class          string `json:"class"`
				Branch         string `json:"branch"`
				Env            string `json:"env"`
				URL            string `json:"url"`
				CurrentRelease string `json:"current_release"`
				Health         string `json:"health"`
				Processes      []struct {
					Process string `json:"process"`
				} `json:"processes"`
			} `json:"envs"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(rawListJSON), &listPayload); err != nil {
		t.Fatalf("app ls --json output not parseable as JSON: %v\nraw:\n%s", err, rawListJSON)
	}
	foundStatic := false
	for _, listed := range listPayload.Apps {
		if listed.App != "site" {
			continue
		}
		for _, env := range listed.Envs {
			if env.Class == "production" && env.Env == productionEnv && env.CurrentRelease == oldRelease && env.Health == "healthy" && len(env.Processes) == 0 && env.URL == "https://static.example.com" {
				foundStatic = true
			}
		}
	}
	if !foundStatic {
		t.Fatalf("app ls --json missing static-only site env:\n%+v", listPayload.Apps)
	}

	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-ok")

	mustWrite(t, filepath.Join(app, "dist", "index.html"), "static-v2")
	e.commitFixture(t, app)
	e.ship(t, app, nil)
	newRelease := currentStaticReleaseFor(t, e, "site", productionEnv)
	if newRelease == oldRelease {
		t.Fatal("expected static fixture deploy to produce a new release")
	}
	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-v2")

	rawRollback := e.ship(t, app, nil, "rollback")
	prodStatus := statusEnvByClass(t, e, app, "production")
	assertContains(t, rawRollback, "Rolled back Production "+prodStatus.Branch+" from "+newRelease+" to "+oldRelease)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-ok")

}

func (e *smokeEnv) testMixedContainerStaticLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "mixed-api")
	mustMkdir(t, app)
	writeMixedFixture(t, app)
	e.commitFixture(t, app)

	e.ship(t, app, nil)
	oldRelease := currentStaticReleaseFor(t, e, "mix", productionEnv)
	oldWeb := identity.ContainerName("mix", productionEnv, "web", oldRelease)
	fragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", productionEnv))
	assertContains(t, fragment, "reverse_proxy http://"+oldWeb+":3000")
	assertContains(t, fragment, `root * "`+staticRouteRoot("mix", productionEnv, oldRelease, "mixed.example.com/docs")+`"`)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/health", "ok")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v1")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs/", "docs-v1")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs-v2", "ok")

	mustWrite(t, filepath.Join(app, "docs-dist", "index.html"), "docs-v2")
	mustWrite(t, filepath.Join(app, "README.md"), "docs v2\n")
	e.commitFixture(t, app)
	e.ship(t, app, nil)
	newRelease := currentStaticReleaseFor(t, e, "mix", productionEnv)
	if newRelease == oldRelease {
		t.Fatal("expected mixed fixture deploy to produce a new release")
	}
	newWeb := identity.ContainerName("mix", productionEnv, "web", newRelease)
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", productionEnv))
	assertContains(t, fragment, "reverse_proxy http://"+newWeb+":3000")
	assertContains(t, fragment, `root * "`+staticRouteRoot("mix", productionEnv, newRelease, "mixed.example.com/docs")+`"`)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/health", "ok")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v2")

	e.ship(t, app, nil, "rollback")
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", productionEnv))
	assertContains(t, fragment, "reverse_proxy http://"+oldWeb+":3000")
	assertContains(t, fragment, `root * "`+staticRouteRoot("mix", productionEnv, oldRelease, "mixed.example.com/docs")+`"`)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v1")

	manifestPath := filepath.Join(app, "ship.toml")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	nextManifest := strings.Replace(string(manifestBytes), `"mixed.example.com/docs" = { static = "docs-dist" }
`, "", 1)
	if nextManifest == string(manifestBytes) {
		t.Fatal("test fixture did not contain docs route")
	}
	mustWrite(t, manifestPath, nextManifest)
	mustWrite(t, filepath.Join(app, "README.md"), "docs route removed\n")
	e.commitFixture(t, app)
	e.ship(t, app, nil)
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", productionEnv))
	if strings.Contains(fragment, "docs-dist") || strings.Contains(fragment, "/docs") {
		t.Fatalf("removed static route still appears in Caddy fragment:\n%s", fragment)
	}
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "ok")
}

// testSecretLifecycle covers bare @secret resolution end-to-end
// against the helper-side store under /etc/ship/secrets/:
//
//  1. setup the app/env baseline
//  2. `ship secret set` over SSH-stdin (value never on argv)
//  3. `ship secret ls` shows the key (NOT the value)
//  4. deploy a manifest that references @secret
//  5. the on-host env file contains the literal env values AND the
//     resolved secret, with mode 0600 owned by the per-env user
//  6. `ship secret rm` removes the key
//  7. a subsequent deploy with the ref still present fails fast at
//     `app apply` with the unresolved-ref error
func (e *smokeEnv) testSecretLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "container-secrets")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "sec"
box = "fake-vps"
probe = "/health"

[env]
LOG_LEVEL = "info"
DATABASE_URL = "@secret"

[processes]
web = { port = 3000 }

[routes]
"sec.example.com" = "web"
`)
	e.commitFixture(t, app)

	// 1. set: value over stdin, never argv. The fake-VPS harness uses
	// docker exec ssh under the hood and the helper reads from its
	// own stdin in `secret set`.
	e.ship(t, app, []byte("postgres://verybadidea"), "secret", "set", "DATABASE_URL")

	// 2. file lands at the expected path, root-owned, mode 0600,
	// containing the value verbatim (no trailing newline — the client
	// trims one if present). Read as root via dockerExec because the
	// deploy user only has passwordless sudo for /usr/local/bin/ship.
	secretDir := "/etc/ship/secrets/sec/" + productionEnv
	listing := strings.TrimSpace(e.dockerExec(t, "ls -l "+secretDir))
	if !strings.Contains(listing, " DATABASE_URL") {
		t.Fatalf("expected DATABASE_URL in %s listing:\n%s", secretDir, listing)
	}
	if !strings.Contains(listing, "-rw-------") {
		t.Fatalf("secret file is not mode 0600:\n%s", listing)
	}
	body := strings.TrimSuffix(e.dockerExec(t, "cat "+secretDir+"/DATABASE_URL"), "\n")
	if body != "postgres://verybadidea" {
		t.Fatalf("secret value didn't round-trip:\nwant: postgres://verybadidea\n got: %q", body)
	}

	// 3. list shows the key — NEVER the value.
	listing = e.ship(t, app, nil, "secret", "ls")
	if !strings.Contains(listing, "DATABASE_URL") {
		t.Fatalf("secret list missing DATABASE_URL:\n%s", listing)
	}
	if strings.Contains(listing, "postgres://") {
		t.Fatalf("secret list leaked the value:\n%s", listing)
	}
	rawSecretJSON := e.ship(t, app, nil, "secret", "ls", "--json")
	var secretPayload struct {
		App  string   `json:"app"`
		Env  string   `json:"env"`
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(rawSecretJSON), &secretPayload); err != nil {
		t.Fatalf("secret list --json output not parseable as JSON: %v\nraw:\n%s", err, rawSecretJSON)
	}
	if secretPayload.App != "sec" || secretPayload.Env != productionEnv || len(secretPayload.Keys) != 1 || secretPayload.Keys[0] != "DATABASE_URL" {
		t.Fatalf("unexpected secret list --json payload: %+v", secretPayload)
	}
	if strings.Contains(rawSecretJSON, "postgres://") {
		t.Fatalf("secret list --json leaked the value:\n%s", rawSecretJSON)
	}

	// 4. deploy: helper resolves DATABASE_URL from bare @secret into the env file
	// next to the literal LOG_LEVEL.
	e.ship(t, app, nil)
	envFile := e.dockerExec(t, "cat "+identity.EnvFile("sec", productionEnv))
	if !strings.Contains(envFile, "LOG_LEVEL=info\n") {
		t.Fatalf("env file missing literal LOG_LEVEL:\n%s", envFile)
	}
	if !strings.Contains(envFile, "DATABASE_URL=postgres://verybadidea\n") {
		t.Fatalf("env file missing resolved DATABASE_URL:\n%s", envFile)
	}

	// 5. env file mode + ownership: 0600 owned by the per-env user.
	envStat := strings.TrimSpace(e.dockerExec(t, "stat -c '%a %U' "+identity.EnvFile("sec", productionEnv)))
	wantOwner := identity.SystemUser("sec", productionEnv)
	if envStat != "600 "+wantOwner {
		t.Fatalf("env file perms wrong: %q (want `600 %s`)", envStat, wantOwner)
	}

	// 6. rm removes the key.
	e.ship(t, app, nil, "secret", "rm", "DATABASE_URL")
	if missing := strings.TrimSpace(e.dockerExec(t, "ls "+secretDir)); missing != "" {
		t.Fatalf("expected empty secret dir after rm, got:\n%s", missing)
	}

	// 7. next deploy with the ref still in the manifest fails fast.
	result := e.runShip(t, app, nil)
	if result.err == nil {
		t.Fatal("expected deploy to fail with unresolved @secret reference")
	}
	if !strings.Contains(result.stderr+result.stdout, "missing secret DATABASE_URL") ||
		!strings.Contains(result.stderr+result.stdout, "ship secret set DATABASE_URL") {
		t.Fatalf("preflight error must name the missing secret and set command, got:\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
	}
}

func (e *smokeEnv) testPreviewSecretScoping(t *testing.T) {
	app := filepath.Join(e.tmp, "preview-secret-scope")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "scope"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[env]
API_TOKEN = "@secret"

[processes]
web = { port = 3000 }

[routes]
"scope.example.com" = "web"
`)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")

	e.ship(t, app, []byte("prod-token"), "secret", "set", "API_TOKEN")
	e.ship(t, app, nil)
	prodEnv := e.dockerExec(t, "cat "+identity.EnvFile("scope", productionEnv))
	assertContains(t, prodEnv, "API_TOKEN=prod-token\n")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/secrets")
	failed := e.runShip(t, app, nil, "--json")
	if failed.err == nil {
		t.Fatal("preview deploy should fail when only the Production secret exists")
	}
	wantSecretJSON := "{\"error\":{\"code\":\"secret_missing\",\"message\":\"deploy is missing a required secret\",\"cause\":\"missing secret API_TOKEN for Preview branch \\\"feature/secrets\\\"\",\"remediation\":\"ship secret set API_TOKEN [--preview|--branch <name>]\"}}\n"
	if failed.stdout != wantSecretJSON {
		t.Fatalf("secret_missing json shape mismatch\nwant:\n%s\ngot:\n%s\nstderr:\n%s", wantSecretJSON, failed.stdout, failed.stderr)
	}
	previewEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "scope", "feature/secrets")
	if leaked := e.dockerExec(t, "test ! -f "+identity.EnvFile("scope", previewEnv)+" || cat "+identity.EnvFile("scope", previewEnv)); strings.Contains(leaked, "prod-token") {
		t.Fatalf("preview env received Production secret value:\n%s", leaked)
	}

	prodEnvBeforeBranchSet := prodEnv
	e.ship(t, app, []byte("branch-token"), "secret", "set", "API_TOKEN")
	if prodEnvAfterBranchSet := e.dockerExec(t, "cat "+identity.EnvFile("scope", productionEnv)); prodEnvAfterBranchSet != prodEnvBeforeBranchSet {
		t.Fatalf("bare secret set on a Preview branch changed Production:\nbefore:\n%s\nafter:\n%s", prodEnvBeforeBranchSet, prodEnvAfterBranchSet)
	}
	if branchList := e.ship(t, app, nil, "secret", "ls"); !strings.Contains(branchList, "API_TOKEN") {
		t.Fatalf("bare secret ls should use the current Preview branch, got:\n%s", branchList)
	}
	e.ship(t, app, nil)
	envFile := e.dockerExec(t, "cat "+identity.EnvFile("scope", previewEnv))
	assertContains(t, envFile, "API_TOKEN=branch-token\n")

	e.ship(t, app, []byte("shared-preview-token"), "secret", "set", "API_TOKEN", "--preview")
	previewList := e.ship(t, app, nil, "secret", "ls", "--preview")
	assertContains(t, previewList, "API_TOKEN")
	e.ship(t, app, nil)
	envFile = e.dockerExec(t, "cat "+identity.EnvFile("scope", previewEnv))
	assertContains(t, envFile, "API_TOKEN=branch-token\n")
	if strings.Contains(envFile, "prod-token") {
		t.Fatalf("preview env leaked Production secret value:\n%s", envFile)
	}

	e.ship(t, app, []byte("branch-token"), "secret", "set", "API_TOKEN", "--branch", "feature/secrets")
	branchJSON := e.ship(t, app, nil, "secret", "ls", "--json", "--branch", "feature/secrets")
	var branchPayload struct {
		App  string   `json:"app"`
		Env  string   `json:"env"`
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(branchJSON), &branchPayload); err != nil {
		t.Fatalf("branch secret list JSON invalid: %v\n%s", err, branchJSON)
	}
	if branchPayload.App != "scope" || branchPayload.Env != previewEnv || len(branchPayload.Keys) != 1 || branchPayload.Keys[0] != "API_TOKEN" {
		t.Fatalf("unexpected branch secret JSON: %+v", branchPayload)
	}
	e.ship(t, app, nil)
	envFile = e.dockerExec(t, "cat "+identity.EnvFile("scope", previewEnv))
	assertContains(t, envFile, "API_TOKEN=branch-token\n")
	if strings.Contains(envFile, "shared-preview-token") || strings.Contains(envFile, "prod-token") {
		t.Fatalf("branch secret should win over shared preview and prod:\n%s", envFile)
	}

	e.ship(t, app, nil, "secret", "rm", "API_TOKEN")
	e.ship(t, app, nil)
	envFile = e.dockerExec(t, "cat "+identity.EnvFile("scope", previewEnv))
	assertContains(t, envFile, "API_TOKEN=shared-preview-token\n")
	if strings.Contains(envFile, "branch-token") || strings.Contains(envFile, "prod-token") {
		t.Fatalf("branch rm should fall back to shared preview only:\n%s", envFile)
	}
}

func (e *smokeEnv) testSecretBulkImport(t *testing.T) {
	e.ensureSmokeHostSeed(t)

	app := filepath.Join(e.tmp, "secret-bulk-import")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "bulksec"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[env]
DATABASE_URL = "@secret"
API_KEY = "@secret"

[env.preview]
DATABASE_URL = "sqlite://preview"
PREVIEW_TOKEN = "@secret"

[processes]
web = { port = 3000 }

[routes]
"bulksec.example.com" = "web"
`)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")

	e.ship(t, app, []byte("legacy-secret"), "secret", "set", "LEGACY")
	bulkEnv := filepath.Join(e.tmp, "bulk-import.env")
	mustWrite(t, bulkEnv, `
# full-line comment ignored
export DATABASE_URL="postgres://bulk"
API_KEY='bulk-api'
`)
	merge := e.runShip(t, app, nil, "secret", "set", "--from", bulkEnv)
	if merge.err != nil {
		t.Fatalf("bulk merge failed: %v\nstdout:\n%s\nstderr:\n%s", merge.err, merge.stdout, merge.stderr)
	}
	if merge.stdout != "" {
		t.Fatalf("bulk merge stdout should be empty, got:\n%s", merge.stdout)
	}
	assertContains(t, merge.stderr, "set: API_KEY, DATABASE_URL")
	assertContains(t, merge.stderr, "set 2, removed 0")
	for _, leaked := range []string{"postgres://bulk", "bulk-api", "legacy-secret"} {
		if strings.Contains(merge.stderr, leaked) {
			t.Fatalf("bulk merge leaked secret value %q:\n%s", leaked, merge.stderr)
		}
	}
	listing := e.ship(t, app, nil, "secret", "ls")
	for _, want := range []string{"API_KEY", "DATABASE_URL", "LEGACY"} {
		assertContains(t, listing, want)
	}

	e.ship(t, app, nil)
	prodEnv := e.dockerExec(t, "cat "+identity.EnvFile("bulksec", productionEnv))
	assertContains(t, prodEnv, "DATABASE_URL=postgres://bulk\n")
	assertContains(t, prodEnv, "API_KEY=bulk-api\n")

	replaceEnv := filepath.Join(e.tmp, "bulk-replace.env")
	mustWrite(t, replaceEnv, `
DATABASE_URL=postgres://replacement
NEW_KEY=new-secret
`)
	replace := e.runShip(t, app, nil, "secret", "set", "--from", replaceEnv, "--replace")
	if replace.err != nil {
		t.Fatalf("bulk replace failed: %v\nstdout:\n%s\nstderr:\n%s", replace.err, replace.stdout, replace.stderr)
	}
	if replace.stdout != "" {
		t.Fatalf("bulk replace stdout should be empty, got:\n%s", replace.stdout)
	}
	assertContains(t, replace.stderr, "set: DATABASE_URL, NEW_KEY")
	assertContains(t, replace.stderr, "removed: API_KEY, LEGACY")
	assertContains(t, replace.stderr, "set 2, removed 2")
	if strings.Contains(replace.stderr, "postgres://replacement") || strings.Contains(replace.stderr, "new-secret") {
		t.Fatalf("bulk replace leaked a secret value:\n%s", replace.stderr)
	}
	listing = e.ship(t, app, nil, "secret", "ls")
	for _, want := range []string{"DATABASE_URL", "NEW_KEY"} {
		assertContains(t, listing, want)
	}
	for _, removed := range []string{"API_KEY", "LEGACY"} {
		if strings.Contains(listing, removed) {
			t.Fatalf("bulk replace did not remove %s:\n%s", removed, listing)
		}
	}

	badEnv := filepath.Join(e.tmp, "bad-bulk.env")
	mustWrite(t, badEnv, "GOOD=should-not-write\nbroken line\n")
	bad := e.runShip(t, app, nil, "secret", "set", "--from", badEnv)
	if bad.err == nil {
		t.Fatal("malformed dotenv import should fail")
	}
	assertContains(t, bad.stderr+bad.stdout, "bad-bulk.env:2: expected KEY=VALUE")
	if strings.Contains(bad.stderr+bad.stdout, "should-not-write") {
		t.Fatalf("malformed dotenv error leaked a value:\nstdout:\n%s\nstderr:\n%s", bad.stdout, bad.stderr)
	}
	listing = e.ship(t, app, nil, "secret", "ls")
	if strings.Contains(listing, "GOOD") {
		t.Fatalf("malformed dotenv import partially wrote GOOD:\n%s", listing)
	}

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/bulk")
	previewEnvPath := filepath.Join(e.tmp, "preview-bulk.env")
	mustWrite(t, previewEnvPath, `
API_KEY=preview-api
PREVIEW_TOKEN='preview-token'
`)
	previewImport := e.runShip(t, app, nil, "secret", "set", "--from", previewEnvPath, "--preview")
	if previewImport.err != nil {
		t.Fatalf("preview bulk import failed: %v\nstdout:\n%s\nstderr:\n%s", previewImport.err, previewImport.stdout, previewImport.stderr)
	}
	if previewImport.stdout != "" {
		t.Fatalf("preview bulk import stdout should be empty, got:\n%s", previewImport.stdout)
	}
	e.ship(t, app, nil, "--tls", "internal")
	previewEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "bulksec", "feature/bulk")
	previewEnvFile := e.dockerExec(t, "cat "+identity.EnvFile("bulksec", previewEnv))
	assertContains(t, previewEnvFile, "DATABASE_URL=sqlite://preview\n")
	assertContains(t, previewEnvFile, "API_KEY=preview-api\n")
	assertContains(t, previewEnvFile, "PREVIEW_TOKEN=preview-token\n")
	if strings.Contains(previewEnvFile, "postgres://replacement") || strings.Contains(previewEnvFile, "new-secret") {
		t.Fatalf("preview deploy resolved Production secrets:\n%s", previewEnvFile)
	}
}

func (e *smokeEnv) testPreviewEnvOverlay(t *testing.T) {
	e.ensureSmokeHostSeed(t)

	app := filepath.Join(e.tmp, "preview-env-overlay")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "overlay"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[env]
LOG_LEVEL = "info"
BASE_ONLY = "kept"
PROD_TOKEN = "@secret"

[env.preview]
LOG_LEVEL = "debug"
PREVIEW_ONLY = "yes"
API_TOKEN = "@secret"

[processes]
web = { port = 3000 }

[routes]
"overlay.example.com" = "web"
`)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")

	e.ship(t, app, []byte("prod-base-token"), "secret", "set", "PROD_TOKEN")
	e.ship(t, app, []byte("prod-leak-token"), "secret", "set", "API_TOKEN")
	e.ship(t, app, []byte("shared-preview-prod-token"), "secret", "set", "PROD_TOKEN", "--preview")
	e.ship(t, app, []byte("shared-preview-token"), "secret", "set", "API_TOKEN", "--preview")
	e.ship(t, app, nil)
	prodEnv := e.dockerExec(t, "cat "+identity.EnvFile("overlay", productionEnv))
	assertContains(t, prodEnv, "LOG_LEVEL=info\n")
	assertContains(t, prodEnv, "BASE_ONLY=kept\n")
	assertContains(t, prodEnv, "PROD_TOKEN=prod-base-token\n")
	if strings.Contains(prodEnv, "PREVIEW_ONLY=") || strings.Contains(prodEnv, "shared-preview-token") {
		t.Fatalf("Production env should ignore [env.preview]:\n%s", prodEnv)
	}

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/env-overlay")
	e.ship(t, app, nil)
	previewEnv := h.PreviewEnvForBranch(t, func(command string) string { return e.ssh(t, command) }, "overlay", "feature/env-overlay")
	previewEnvFile := e.dockerExec(t, "cat "+identity.EnvFile("overlay", previewEnv))
	assertContains(t, previewEnvFile, "LOG_LEVEL=debug\n")
	assertContains(t, previewEnvFile, "BASE_ONLY=kept\n")
	assertContains(t, previewEnvFile, "PREVIEW_ONLY=yes\n")
	assertContains(t, previewEnvFile, "API_TOKEN=shared-preview-token\n")
	if strings.Contains(previewEnvFile, "prod-base-token") || strings.Contains(previewEnvFile, "prod-leak-token") {
		t.Fatalf("Preview env should resolve overlay @secret through Preview scopes only:\n%s", previewEnvFile)
	}
}

func (e *smokeEnv) testExec(t *testing.T) {
	app := filepath.Join(e.tmp, "exec-api")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "execapi"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[env]
LOG_LEVEL = "info"
DATABASE_URL = "@secret"

[processes]
web = { port = 3000 }

[routes]
"exec.example.com" = "web"
`)
	e.commitFixture(t, app)
	e.mustRun(t, app, nil, "git", "checkout", "-B", "main")

	noRelease := e.runShip(t, app, nil, "exec", "env")
	if noRelease.err == nil {
		t.Fatal("exec before first deploy should fail")
	}
	if noRelease.stdout != "" || !strings.Contains(noRelease.stderr, `"code":"no_deploys"`) || !strings.Contains(noRelease.stderr, `"remediation":"ship"`) {
		t.Fatalf("exec no-release guard should be coded on stderr\nstdout:%s\nstderr:%s", noRelease.stdout, noRelease.stderr)
	}

	e.ship(t, app, []byte("postgres://exec-secret"), "secret", "set", "DATABASE_URL")
	e.ship(t, app, nil)
	release := gitRelease(t, e, app)

	envOut := e.ship(t, app, nil, "exec", "env")
	for _, want := range []string{
		"LOG_LEVEL=info\n",
		"DATABASE_URL=postgres://exec-secret\n",
		"SHIP_URL=https://exec.example.com\n",
		"SHIP_BRANCH=main\n",
		"SHIP_ENV=production\n",
		"SHIP_RELEASE=" + release + "\n",
	} {
		assertContains(t, envOut, want)
	}

	failed := e.runShip(t, app, nil, "exec", "sh", "-c", "exit 7")
	var exitErr *exec.ExitError
	if !errors.As(failed.err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("exec should propagate command exit 7, got err=%v stdout=%s stderr=%s", failed.err, failed.stdout, failed.stderr)
	}

	e.ship(t, app, nil, "exec", "sh", "-c", "printf exec-data > /data/exec.txt")
	got := strings.TrimSpace(e.ship(t, app, nil, "exec", "cat", "/data/exec.txt"))
	if got != "exec-data" {
		t.Fatalf("exec /data round-trip = %q, want exec-data", got)
	}
}

// testStatusAndLogs covers the read-only operator surface. It assumes
// the earlier subtests have already deployed the `api` container app
// and left its `web` process running.
func (e *smokeEnv) testStatusAndLogs(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")

	// Text status surfaces the web process, its container, and the
	// release label baked in by `app apply`.
	text := e.ship(t, app, nil, "status")
	assertContains(t, text, "Production ")
	assertContains(t, text, "release=")
	assertContains(t, text, "health=healthy")
	assertContains(t, text, `shipped_by="Smoke <smoke@example.com>"`)
	assertContains(t, text, `ssh_key="fake-vps-smoke"`)
	if strings.Contains(text, "No live envs") {
		t.Fatalf("status reported no live envs after a successful deploy:\n%s", text)
	}

	// JSON status carries the same data in a structured shape.
	// Parse it back to prove the contract — text-mode regressions
	// might still slip through a substring check.
	payload := statusPayloadForApp(t, e, app)
	env := statusEnvByClassFromPayload(t, payload, "production")
	if payload.App != "api" || env.Env != productionEnv {
		t.Fatalf("status --json mis-identifies the app: %+v", payload)
	}
	if len(env.Processes) != 1 || env.Processes[0].Process != "web" {
		t.Fatalf("expected one process `web`, got: %+v", env.Processes)
	}
	if env.Processes[0].Container == "" || !strings.Contains(env.Processes[0].Container, "-web-") {
		t.Fatalf("unexpected container name: %+v", env.Processes[0])
	}
	if env.Processes[0].Release == "" {
		t.Fatalf("status --json missing release label: %+v", env.Processes[0])
	}
	if env.ShippedBy == nil || env.ShippedBy.GitAuthor != "Smoke <smoke@example.com>" || env.ShippedBy.SSHKeyComment != "fake-vps-smoke" {
		t.Fatalf("status --json missing shipped_by: %+v", env.ShippedBy)
	}

	// Host-level app listing is sourced from Podman labels instead
	// of the removed apps.json/routes.json registries.
	rawListJSON := e.ship(t, app, nil, "box", "app", "ls", "--json")
	legacyApps := e.runShip(t, app, nil, "box", "ls")
	if legacyApps.err == nil || !strings.Contains(legacyApps.stderr+legacyApps.stdout, "ship box app ls") {
		t.Fatalf("box ls should fail with box app ls remediation:\nstdout:\n%s\nstderr:\n%s", legacyApps.stdout, legacyApps.stderr)
	}
	var listPayload struct {
		Apps []struct {
			App  string `json:"app"`
			Envs []struct {
				Class          string `json:"class"`
				Branch         string `json:"branch"`
				Env            string `json:"env"`
				URL            string `json:"url"`
				CurrentRelease string `json:"current_release"`
				Health         string `json:"health"`
				AgeSeconds     int64  `json:"age_seconds"`
				ExpiresAt      string `json:"expires_at"`
				Pinned         bool   `json:"pinned"`
				ShippedBy      *struct {
					SSHKeyComment string `json:"ssh_key_comment"`
					GitAuthor     string `json:"git_author"`
				} `json:"shipped_by"`
				Processes []struct {
					Process string `json:"process"`
					State   string `json:"state"`
				} `json:"processes"`
			} `json:"envs"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(rawListJSON), &listPayload); err != nil {
		t.Fatalf("app ls --json output not parseable as JSON: %v\nraw:\n%s", err, rawListJSON)
	}
	if len(listPayload.Apps) == 0 {
		t.Fatalf("app ls --json returned no apps after deploy:\n%s", rawListJSON)
	}
	found := false
	for _, listed := range listPayload.Apps {
		if listed.App != "api" {
			continue
		}
		for _, env := range listed.Envs {
			if env.Class == "production" &&
				env.Branch == "main" &&
				env.Env == productionEnv &&
				env.URL == "https://api.example.com" &&
				env.CurrentRelease != "" &&
				env.Health == "healthy" &&
				env.ExpiresAt == "" &&
				!env.Pinned &&
				env.ShippedBy != nil &&
				env.ShippedBy.SSHKeyComment == "fake-vps-smoke" &&
				len(env.Processes) == 1 &&
				env.Processes[0].Process == "web" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("app ls --json missing api/production/web process:\n%+v", listPayload.Apps)
	}

	// A container with no stdout/stderr is not a silent success.
	emptyLogs := e.runShip(t, app, nil, "logs", "web", "--tail", "20")
	if emptyLogs.err != nil {
		t.Fatalf("empty logs should exit 0: %v\nstdout:\n%s\nstderr:\n%s", emptyLogs.err, emptyLogs.stdout, emptyLogs.stderr)
	}
	if emptyLogs.stdout != "" || !strings.Contains(emptyLogs.stderr, "no log lines yet") {
		t.Fatalf("empty logs should be explicit on stderr\nstdout:\n%s\nstderr:\n%s", emptyLogs.stdout, emptyLogs.stderr)
	}

	emptyLogsJSON := e.runShip(t, app, nil, "logs", "web", "--tail", "20", "--json")
	if emptyLogsJSON.err != nil {
		t.Fatalf("empty logs --json should exit 0: %v\nstdout:\n%s\nstderr:\n%s", emptyLogsJSON.err, emptyLogsJSON.stdout, emptyLogsJSON.stderr)
	}
	var logsPayload struct {
		App     string   `json:"app"`
		Env     string   `json:"env"`
		Process string   `json:"process"`
		Lines   []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(emptyLogsJSON.stdout), &logsPayload); err != nil {
		t.Fatalf("empty logs --json output not parseable as JSON: %v\nraw:\n%s", err, emptyLogsJSON.stdout)
	}
	if logsPayload.App != "api" || logsPayload.Env != productionEnv || logsPayload.Process != "web" || logsPayload.Lines == nil || len(logsPayload.Lines) != 0 {
		t.Fatalf("unexpected empty logs --json payload: %+v", logsPayload)
	}
	if !strings.Contains(emptyLogsJSON.stderr, "no log lines yet") {
		t.Fatalf("empty logs --json should keep stdout JSON and stderr hint\nstdout:\n%s\nstderr:\n%s", emptyLogsJSON.stdout, emptyLogsJSON.stderr)
	}

	// Logs reaches `podman logs` on the right container and prints
	// actual captured process output rather than a synthetic fake line.
	e.assertRemoteBody(t, "curl -fsS -H 'Host: api.example.com' http://127.0.0.1/health", "ok")
	logs := e.ship(t, app, nil, "logs", "web", "--tail", "1")
	assertContains(t, logs, "GET /health")
	if strings.Contains(logs, "fake podman logs") {
		t.Fatalf("logs should not be synthetic:\n%s", logs)
	}

	logsJSON := e.ship(t, app, nil, "logs", "web", "--tail", "1", "--json")
	if err := json.Unmarshal([]byte(logsJSON), &logsPayload); err != nil {
		t.Fatalf("logs --json output not parseable as JSON: %v\nraw:\n%s", err, logsJSON)
	}
	if logsPayload.App != "api" || logsPayload.Env != productionEnv || logsPayload.Process != "web" || len(logsPayload.Lines) != 1 || !strings.Contains(logsPayload.Lines[0], "GET /health") {
		t.Fatalf("unexpected logs --json payload: %+v", logsPayload)
	}

	// Process argument is optional when exactly one process exists.
	logsNoSvc := e.ship(t, app, nil, "logs", "--tail", "1")
	assertContains(t, logsNoSvc, "GET /health")

	// Unknown process errors clearly.
	missing := e.runShip(t, app, nil, "logs", "nope")
	if missing.err == nil {
		t.Fatal("expected logs to fail when process is unknown")
	}
	if !strings.Contains(missing.stderr, "nope") {
		t.Fatalf("error should name the missing process, got: %s", missing.stderr)
	}
}

// testDestroy covers the public `ship rm` wrapper and the privileged
// `server app destroy-env` teardown path. It intentionally runs after
// status/logs because it removes the container-api fixture from the
// fake VPS.
func (e *smokeEnv) testDestroy(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	prodStatus := statusEnvByClass(t, e, app, "production")

	// Client safety gate: no accidental teardown without either
	// --confirm <app>.
	missingConfirm := e.runShip(t, app, nil, "rm", prodStatus.Branch)
	if missingConfirm.err == nil {
		t.Fatal("expected rm without confirmation to fail")
	}
	if !strings.Contains(missingConfirm.stderr+missingConfirm.stdout, "--confirm api") {
		t.Fatalf("confirmation error should name the app, got:\nstdout: %s\nstderr: %s", missingConfirm.stdout, missingConfirm.stderr)
	}

	// Give --purge something observable to remove.
	e.ship(t, app, []byte("throwaway"), "secret", "set", "cleanup_key")
	currentContainer := currentWebContainer(t, e, app)

	out := e.ship(t, app, nil, "rm", prodStatus.Branch, "--confirm", "api")
	assertContains(t, out, "Removed Production "+prodStatus.Branch)

	commandsLog := e.ssh(t, "cat /run/fake-podman/commands.log")
	assertContains(t, commandsLog, "podman rm -f "+currentContainer)
	assertContains(t, commandsLog, "podman network rm "+identity.Network("api", productionEnv))
	assertContains(t, commandsLog, "podman exec caddy caddy reload --config /etc/caddy/Caddyfile")

	e.dockerExec(t, "test ! -e /run/fake-podman/containers/"+currentContainer+".labels")
	e.dockerExec(t, "test ! -e /run/fake-podman/networks/"+identity.Network("api", productionEnv))
	e.dockerExec(t, "test ! -e "+identity.CaddyFragmentFile("api", productionEnv))
	e.dockerExec(t, "test ! -e "+identity.EnvRoot("api", productionEnv))
	e.dockerExec(t, "test ! -e /etc/ship/secrets/api/"+productionEnv)
	e.dockerExec(t, "! getent passwd "+identity.SystemUser("api", productionEnv)+" >/dev/null")

	status := e.ship(t, app, nil, "status")
	assertContains(t, status, "No live envs for api")

	// Fake Caddy re-reads conf.d on every request, so route removal is
	// visible immediately after the reload.
	e.ssh(t, "if curl -fsS -H 'Host: api.example.com' http://127.0.0.1/health; then exit 1; fi")

	// Idempotence: a second destroy should be a no-op, not an error.
	again := e.ship(t, app, nil, "rm", prodStatus.Branch, "--confirm", "api")
	assertContains(t, again, "Removed Production "+prodStatus.Branch)
}

func writeContainerFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "api"
box = "fake-vps"
probe = "/health"

[processes]
web = { port = 3000, resources = { memory = "512m", cpus = 0.5 } }

[routes]
"api.example.com" = "web"
	`)
}

func assertHelperVersionCheck(t *testing.T, doctorJSON string, wantStatus string) {
	t.Helper()
	var checks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(doctorJSON)), &checks); err != nil {
		t.Fatalf("doctor output is not a JSON check list: %v\noutput:\n%s", err, doctorJSON)
	}
	for _, check := range checks {
		if check.ID == "helper_version" {
			if check.Status != wantStatus {
				t.Fatalf("helper_version status = %q, want %q\noutput:\n%s", check.Status, wantStatus, doctorJSON)
			}
			return
		}
	}
	t.Fatalf("no helper_version check in doctor output:\n%s", doctorJSON)
}

func writeBoxVersionFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "versionapi"
box = "fake-vps"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"version.example.com" = "web"
`)
}

func writeBranchFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "branchapi"
box = "fake-vps"
production_branch = "stable"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"branch.example.com" = "web"
`)
}

func writePreviewLifecycleFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "previewapi"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"preview.example.com" = "web"
`)
}

func writePreviewProtectionFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "guardapi"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"guard.example.com" = "web"
`)
}

func writePreviewBaseAliasFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "basealias"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[preview]
base = "preview.example.com"
aliases = true

[processes]
web = { port = 3000 }

[routes]
"basealias.example.com" = "web"
`)
}

func writeZeroDNSFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "zerodns"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[processes]
web = { port = 3000 }
`)
}

func writeReleaseFailFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "releasefail"
box = "fake-vps"
release = "touch /data/release-ok"
probe = "/health"

[env]
MARKER = "stable"
API_TOKEN = "@secret"

[processes]
web = { port = 3000 }

[routes]
"release-fail.example.com" = "web"
	`)
}

func writeWebhookFixture(t *testing.T, app string, webhookURL string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "webhookapi"
box = "fake-vps"
production_branch = "main"
release = "touch /data/release-ok"
probe = "/health"
webhook = "`+webhookURL+`"

[env]
API_TOKEN = "@secret"

[processes]
web = { port = 3000 }

[routes]
"webhook.example.com" = "web"
`)
}

func writeRoleApprovalFixture(t *testing.T, app string, webhookURL string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "roleapi"
box = "fake-vps"
production_branch = "main"
probe = "/health"
webhook = "`+webhookURL+`"

[processes]
web = { port = 3000 }

[routes]
"role.example.com" = "web"
`)
}

// writeWebhookSecondFixture is the webhook test's own second app: it exists
// only to prove box events fire once across two apps on one box. It must
// NOT reuse another subtest's app name (e.g. roleapi) — a leftover production
// deploy would poison that subtest's own first ship.
func writeWebhookSecondFixture(t *testing.T, app string, webhookURL string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "webhooksecond"
box = "fake-vps"
production_branch = "main"
probe = "/health"
webhook = "`+webhookURL+`"

[processes]
web = { port = 3000 }

[routes]
"webhook-second.example.com" = "web"
`)
}

func writeDataForkFixture(t *testing.T, app, name, host string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "`+name+`"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"`+host+`" = "web"
`)
}

func writeProbeFailFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "probefail"
box = "fake-vps"
probe = "/health"

[env]
LEAK_ON_PROBE_LOG = "@secret"

[processes]
web = { port = 3000, cmd = "ship-listen-port=3000 sleep 3600" }

[routes]
"probe-fail.example.com" = "web"
`)
}

func writeCaddyFailFixture(t *testing.T, app string) {
	t.Helper()
	mustMkdir(t, filepath.Join(app, "docs-dist"))
	mustWrite(t, filepath.Join(app, "docs-dist", "index.html"), "docs stable\n")
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "caddyfail"
box = "fake-vps"
probe = "/health"

[env]
MARKER = "stable"

[processes]
web = { port = 3000 }
worker = "sleep 3600"

[routes]
"caddy-fail.example.com" = "web"
"caddy-fail.example.com/docs" = { static = "docs-dist" }
`)
}

func writePruneFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "prune"
box = "fake-vps"
probe = "/health"

[processes]
web = { port = 3000 }
worker = "sleep 3600"

[routes]
"prune.example.com" = "web"
`)
}

func writeImagePruneFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "imgprune"
box = "fake-vps"
production_branch = "main"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"img-prune.example.com" = "web"
`)
}

func writeStaticFixture(t *testing.T, app string) {
	t.Helper()
	mustMkdir(t, filepath.Join(app, "dist"))
	mustWrite(t, filepath.Join(app, "dist", "index.html"), "static-ok")
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "site"
box = "fake-vps"

[routes]
"static.example.com" = { static = "dist" }
`)
}

func writeMixedFixture(t *testing.T, app string) {
	t.Helper()
	mustMkdir(t, filepath.Join(app, "docs-dist"))
	mustWrite(t, filepath.Join(app, "docs-dist", "index.html"), "docs-v1")
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "mix"
box = "fake-vps"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"mixed.example.com/docs" = { static = "docs-dist" }
"mixed.example.com" = "web"
`)
}

func gitRelease(t *testing.T, e *smokeEnv, app string) string {
	t.Helper()
	return strings.TrimSpace(e.mustRun(t, app, nil, "git", "rev-parse", "--short=12", "HEAD"))
}

func commitImagePruneChange(t *testing.T, e *smokeEnv, app string, message string) string {
	t.Helper()
	mustWrite(t, filepath.Join(app, "README.md"), message+"\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", message)
	return gitRelease(t, e, app)
}

func fakeImageReleases(t *testing.T, e *smokeEnv, app, env string) []string {
	t.Helper()
	raw := e.dockerExec(t, "podman images --format json")
	var images []struct {
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal([]byte(raw), &images); err != nil {
		t.Fatalf("podman images JSON not parseable: %v\nraw:\n%s", err, raw)
	}
	var releases []string
	for _, image := range images {
		labels := image.Labels
		if labels["ship.app"] != app || labels["ship.env"] != env || labels["ship.infra_id"] != identity.InfraID(app, env) {
			continue
		}
		if labels["ship.release"] != "" {
			releases = append(releases, labels["ship.release"])
		}
	}
	sort.Strings(releases)
	return releases
}

func assertFakeImageReleases(t *testing.T, e *smokeEnv, app, env string, want ...string) {
	t.Helper()
	got := fakeImageReleases(t, e, app, env)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("images for %s/%s:\nwant: %v\n got: %v", app, env, want, got)
	}
}

func assertFakeImagePresent(t *testing.T, e *smokeEnv, app, env, release string) {
	t.Helper()
	for _, got := range fakeImageReleases(t, e, app, env) {
		if got == release {
			return
		}
	}
	t.Fatalf("image %s/%s:%s not found; images=%v", app, env, release, fakeImageReleases(t, e, app, env))
}

func assertFakeImageAbsent(t *testing.T, e *smokeEnv, app, env, release string) {
	t.Helper()
	for _, got := range fakeImageReleases(t, e, app, env) {
		if got == release {
			t.Fatalf("image %s/%s:%s should have been pruned; images=%v", app, env, release, fakeImageReleases(t, e, app, env))
		}
	}
}

func assertOnlyURL(t *testing.T, stdout string) string {
	t.Helper()
	if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("ship stdout should be exactly one URL line, got %q", stdout)
	}
	raw := strings.TrimSpace(stdout)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		t.Fatalf("ship stdout is not an https URL: %q", stdout)
	}
	return raw
}

func sslipIPFromHost(host string) string {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) < 4 {
		return ""
	}
	return strings.ReplaceAll(labels[len(labels)-3], "-", ".")
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

func remoteDataHash(t *testing.T, e *smokeEnv, app, env string) string {
	t.Helper()
	dir := identity.DataDir(app, env)
	return e.dockerExec(t, "cd "+dir+" && find . -type f -print | sort | xargs sha256sum")
}

func approvalIDFromOutput(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		command, ok := strings.CutPrefix(line, "next: ship box approval grant ")
		if !ok {
			continue
		}
		fields := strings.Fields(command)
		if len(fields) != 2 || fields[0] == "" || fields[1] != "fake-vps" {
			t.Fatalf("approval line must carry id and resolved box:\n%s", output)
		}
		return fields[0]
	}
	for _, line := range strings.Split(output, "\n") {
		var payload struct {
			Error struct {
				Remediation string `json:"remediation"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line), &payload) != nil {
			continue
		}
		if command, ok := strings.CutPrefix(payload.Error.Remediation, "ship box approval grant "); ok {
			fields := strings.Fields(command)
			if len(fields) == 2 && fields[0] != "" && fields[1] == "fake-vps" {
				return fields[0]
			}
		}
	}
	t.Fatalf("approval output missing remediation:\n%s", output)
	return ""
}

func assertJournalLineContains(t *testing.T, journal string, marker string, wants ...string) {
	t.Helper()
	for _, line := range strings.Split(journal, "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		for _, want := range wants {
			if !strings.Contains(line, want) {
				t.Fatalf("journal line containing %q missing %q:\n%s", marker, want, line)
			}
		}
		return
	}
	t.Fatalf("journal missing line containing %q:\n%s", marker, journal)
}

func stripURLs(text string) string {
	fields := strings.Fields(text)
	for i, field := range fields {
		if strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "http://") {
			fields[i] = "<url>"
		}
	}
	return strings.Join(fields, " ")
}

func staticRouteRoot(app, env, release, routeKey string) string {
	return filepath.Join(identity.StaticDir(app, env), "releases", release, config.RouteStorageName(routeKey))
}

func currentStaticReleaseFor(t *testing.T, e *smokeEnv, app, env string) string {
	t.Helper()
	return strings.TrimSpace(e.ssh(t, "basename $(readlink "+identity.StaticDir(app, env)+"/current)"))
}

func currentWebContainer(t *testing.T, e *smokeEnv, app string) string {
	t.Helper()
	return currentProcessContainer(t, e, app, "web")
}

func currentProcessContainer(t *testing.T, e *smokeEnv, app string, process string) string {
	t.Helper()
	env := statusEnvByClass(t, e, app, "production")
	for _, proc := range env.Processes {
		if proc.Process == process {
			return proc.Container
		}
	}
	t.Fatalf("status --json missing %s process: %+v", process, env.Processes)
	return ""
}

type smokeStatusPayload struct {
	App  string           `json:"app"`
	Envs []smokeStatusEnv `json:"envs"`
}

type smokeStatusEnv struct {
	Class         string `json:"class"`
	Branch        string `json:"branch"`
	URL           string `json:"url"`
	CapabilityURL string `json:"capability_url"`
	Env           string `json:"env"`
	Release       string `json:"release"`
	Health        string `json:"health"`
	ExpiresAt     string `json:"expiresAt"`
	Pinned        bool   `json:"pinned"`
	Dirty         bool   `json:"dirty"`
	ShippedBy     *struct {
		SSHKeyComment string `json:"ssh_key_comment"`
		GitAuthor     string `json:"git_author"`
	} `json:"shipped_by"`
	Processes []struct {
		Process   string `json:"process"`
		Container string `json:"container"`
		State     string `json:"state"`
		Release   string `json:"release"`
	} `json:"processes"`
}

type smokeAppListPayload struct {
	Apps []smokeAppListApp `json:"apps"`
}

type smokeAppListApp struct {
	App  string            `json:"app"`
	Envs []smokeAppListEnv `json:"envs"`
}

type smokeAppListEnv struct {
	Class          string `json:"class"`
	Branch         string `json:"branch"`
	URL            string `json:"url"`
	Env            string `json:"env"`
	CurrentRelease string `json:"current_release"`
	Health         string `json:"health"`
	AgeSeconds     int64  `json:"age_seconds"`
	ExpiresAt      string `json:"expires_at"`
	Pinned         bool   `json:"pinned"`
	Dirty          bool   `json:"dirty"`
	ShippedBy      *struct {
		SSHKeyComment string `json:"ssh_key_comment"`
		GitAuthor     string `json:"git_author"`
	} `json:"shipped_by"`
	Processes []struct {
		Process string `json:"process"`
		State   string `json:"state"`
		Release string `json:"release"`
	} `json:"processes"`
}

type smokeWhyEntry struct {
	Outcome          string `json:"outcome"`
	PreviousRelease  string `json:"previous_release"`
	AttemptedRelease string `json:"attempted_release"`
	FailingStep      string `json:"failing_step"`
	StderrTail       string `json:"stderr_tail"`
	Identity         struct {
		SSHKeyComment string `json:"ssh_key_comment"`
		GitAuthor     string `json:"git_author"`
	} `json:"identity"`
	Probe *struct {
		Status      int    `json:"status"`
		BodySnippet string `json:"body_snippet"`
	} `json:"probe"`
}

func statusPayloadForApp(t *testing.T, e *smokeEnv, app string) smokeStatusPayload {
	t.Helper()
	rawJSON := e.ship(t, app, nil, "status", "--json")
	var payload smokeStatusPayload
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		t.Fatalf("status --json output not parseable as JSON: %v\nraw:\n%s", err, rawJSON)
	}
	return payload
}

func statusEnvByClass(t *testing.T, e *smokeEnv, app string, class string) smokeStatusEnv {
	t.Helper()
	return statusEnvByClassFromPayload(t, statusPayloadForApp(t, e, app), class)
}

func statusEnvByClassFromPayload(t *testing.T, payload smokeStatusPayload, class string) smokeStatusEnv {
	t.Helper()
	for _, env := range payload.Envs {
		if env.Class == class {
			return env
		}
	}
	t.Fatalf("status missing %s env: %+v", class, payload.Envs)
	return smokeStatusEnv{}
}

func statusEnvByBranch(t *testing.T, e *smokeEnv, app string, branch string) smokeStatusEnv {
	t.Helper()
	payload := statusPayloadForApp(t, e, app)
	for _, env := range payload.Envs {
		if env.Branch == branch {
			return env
		}
	}
	t.Fatalf("status missing branch %s: %+v", branch, payload.Envs)
	return smokeStatusEnv{}
}

func appListPayloadForBox(t *testing.T, e *smokeEnv, app string) smokeAppListPayload {
	t.Helper()
	rawJSON := e.ship(t, app, nil, "box", "app", "ls", "--json")
	var payload smokeAppListPayload
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		t.Fatalf("box app ls --json output not parseable as JSON: %v\nraw:\n%s", err, rawJSON)
	}
	return payload
}

func appListEnvByAppClassBranch(t *testing.T, payload smokeAppListPayload, app, class, branch string) smokeAppListEnv {
	t.Helper()
	for _, listed := range payload.Apps {
		if listed.App != app {
			continue
		}
		for _, env := range listed.Envs {
			if env.Class == class && env.Branch == branch {
				return env
			}
		}
	}
	t.Fatalf("app ls missing %s %s %s: %+v", app, class, branch, payload.Apps)
	return smokeAppListEnv{}
}

type previewIdentityPayload struct {
	Version int `json:"version"`
	App     string
	Env     string
	Preview *struct {
		Branch     string  `json:"branch"`
		LastShipAt string  `json:"last_ship_at"`
		ExpiresAt  *string `json:"expires_at"`
	} `json:"preview"`
}

type smokeWebhookSink struct {
	env        *smokeEnv
	eventsPath string
	port       string
}

func (e *smokeEnv) startWebhookSink(t *testing.T) *smokeWebhookSink {
	t.Helper()
	sink := &smokeWebhookSink{
		env:        e,
		eventsPath: "/tmp/ship-webhook-events.jsonl",
		port:       "18081",
	}
	e.dockerExec(t, `rm -f /tmp/ship-webhook-events.jsonl /tmp/ship-webhook-sink.pid /tmp/ship-webhook-sink.log /tmp/ship-webhook-sink.py`)
	e.dockerExec(t, `cat > /tmp/ship-webhook-sink.py <<'PY'
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

events_path = sys.argv[1]
port = int(sys.argv[2])

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        return

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(b"ok")

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        try:
            import json
            payload = json.loads(body)
            payload["_sink_path"] = self.path
            body = json.dumps(payload, separators=(",", ":")).encode()
        except Exception:
            pass
        with open(events_path, "ab") as f:
            f.write(body + b"\n")
        if self.path.startswith("/slow"):
            time.sleep(5)
        try:
            self.send_response(204)
            self.end_headers()
        except BrokenPipeError:
            pass

server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
server.serve_forever()
PY
python3 /tmp/ship-webhook-sink.py /tmp/ship-webhook-events.jsonl 18081 >/tmp/ship-webhook-sink.log 2>&1 &
echo $! >/tmp/ship-webhook-sink.pid`)
	t.Cleanup(func() {
		e.dockerExec(t, `if [ -f /tmp/ship-webhook-sink.pid ]; then kill "$(cat /tmp/ship-webhook-sink.pid)" 2>/dev/null || true; fi`)
	})
	e.dockerExec(t, `for i in $(seq 1 50); do
  if curl -fsS http://127.0.0.1:18081/ready >/dev/null 2>&1; then
    exit 0
  fi
  sleep 0.1
done
cat /tmp/ship-webhook-sink.log >&2
exit 1`)
	return sink
}

func (s *smokeWebhookSink) URL(path string) string {
	return "http://127.0.0.1:" + s.port + path
}

func (s *smokeWebhookSink) rawEvents(t *testing.T) string {
	t.Helper()
	return s.env.dockerExec(t, "if [ -f "+h.ShellQuote(s.eventsPath)+" ]; then cat "+h.ShellQuote(s.eventsPath)+"; fi")
}

func (s *smokeWebhookSink) waitForEvent(t *testing.T, event string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastRaw string
	for time.Now().Before(deadline) {
		lastRaw = s.rawEvents(t)
		for _, line := range strings.Split(strings.TrimSpace(lastRaw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				t.Fatalf("webhook sink captured invalid JSON: %v\nraw:\n%s", err, line)
			}
			if got, _ := payload["event"].(string); got == event {
				return payload
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for webhook event %s\ncaptured:\n%s", event, lastRaw)
	return nil
}

func countWebhookSmokeEvents(raw, event string) int {
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var payload map[string]any
		if json.Unmarshal([]byte(line), &payload) == nil && payload["event"] == event {
			count++
		}
	}
	return count
}

func assertWebhookSmokeField(t *testing.T, payload map[string]any, field, want string) {
	t.Helper()
	if got, _ := payload[field].(string); got != want {
		t.Fatalf("webhook field %s = %q, want %q\npayload:\n%s", field, got, want, prettySmokeJSON(t, payload))
	}
}

func assertWebhookSmokeNested(t *testing.T, payload map[string]any, path, want string) {
	t.Helper()
	if got := webhookSmokeNestedString(t, payload, path); got != want {
		t.Fatalf("webhook field %s = %q, want %q\npayload:\n%s", path, got, want, prettySmokeJSON(t, payload))
	}
}

func webhookSmokeNestedString(t *testing.T, payload map[string]any, path string) string {
	t.Helper()
	var current any = payload
	for _, part := range strings.Split(path, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("webhook field %s is not an object at %s\npayload:\n%s", path, part, prettySmokeJSON(t, payload))
		}
		current = obj[part]
	}
	got, ok := current.(string)
	if !ok {
		t.Fatalf("webhook field %s is not a string\npayload:\n%s", path, prettySmokeJSON(t, payload))
	}
	return got
}

func prettySmokeJSON(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertPreviewEnvName(t *testing.T, env, sanitizedBranch string) {
	t.Helper()
	prefix := sanitizedBranch + "-"
	if !strings.HasPrefix(env, prefix) || len(strings.TrimPrefix(env, prefix)) != 4 {
		t.Fatalf("preview env %q should be %s plus a 4-char suffix", env, prefix)
	}
	for _, r := range strings.TrimPrefix(env, prefix) {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Fatalf("preview suffix in %q is not [a-z0-9]", env)
		}
	}
}

func readPreviewIdentity(t *testing.T, e *smokeEnv, app, env string) previewIdentityPayload {
	t.Helper()
	raw := e.ssh(t, "cat "+identity.IdentityFile(app, env))
	var payload previewIdentityPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("preview identity is not JSON: %v\n%s", err, raw)
	}
	return payload
}

func parseRemoteTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("parse remote time %q: %v", raw, err)
	}
	return parsed
}

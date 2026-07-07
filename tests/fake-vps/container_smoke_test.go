package fakevps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/identity"
)

const productionEnv = "prod"

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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	env := newSmokeEnv(t, ctx)
	env.buildBinaries(t)
	env.buildImage(t)
	env.startContainer(t)
	env.configureSSH(t, "deploy")
	env.waitForSSH(t)

	t.Run("container app reaches setup + deploy + caddy proxy", env.testContainerAppLifecycle)
	t.Run("release command failure leaves old traffic unchanged", env.testReleaseCommandFailure)
	t.Run("caddy switch failure restores runtime state", env.testCaddySwitchFailureRollback)
	t.Run("branch env resolution and production guards", env.testBranchEnvironmentGuards)
	t.Run("preview lifecycle mapping pin and reap", env.testPreviewLifecycle)
	t.Run("container rollback runs an older image release", env.testRollback)
	t.Run("backup and restore round-trip app state", env.testBackupRestore)
	t.Run("deploy removes processes dropped from the manifest", env.testRemovedProcessReconciliation)
	t.Run("concurrent deploys of the same app env serialize", env.testConcurrentDeploys)
	t.Run("static-only app deploys and restores without containers", env.testStaticOnlyAppLifecycle)
	t.Run("mixed container and static routes deploy as one release", env.testMixedContainerStaticLifecycle)
	t.Run("@secret refs resolve through set/list/rm into the runtime env", env.testSecretLifecycle)
	t.Run("status + logs surface deployed processes without SSHing in", env.testStatusAndLogs)
	t.Run("rm tears down one app environment", env.testDestroy)
}

func (e *smokeEnv) testContainerAppLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	mustMkdir(t, app)
	writeContainerFixture(t, app)
	// Deploy needs a git tree (release id = git short SHA). Commit to
	// stay on the canonical clean-tree path.
	e.commitFixture(t, app)

	// The shared `ingress` Podman network and the host-side Caddy
	// container would normally come from `ship box init`.
	// The smoke skips the installer, so seed both here the same way
	// the provisioner does. These need root and the deploy user only
	// has passwordless sudo for /usr/local/bin/ship — use
	// docker exec instead.
	e.dockerExec(t, `cat > /etc/simple-vps/host.json <<'EOF'
{"version":1,"desired":{"users":{"operator":"operator","deploy":"deploy"},"ingress":{"expose":"public","tunnel":"none"},"features":{},"packages":{"podman":{"source":"apt"},"rsync":{"source":"apt"},"caddy":{"source":"container"}}},"observed":{"packages":{},"ingress":{}},"meta":{}}
EOF`)
	e.dockerExec(t, "mkdir -p /etc/caddy/simple-vps /etc/caddy/conf.d /var/lib/caddy")
	e.dockerExec(t, "mkdir -p /tmp/simple-vps-deploy && chmod 1777 /tmp/simple-vps-deploy")
	e.dockerExec(t, `cat > /etc/caddy/Caddyfile <<'EOF'
import simple-vps/*.caddy
import conf.d/*.caddy
EOF`)
	e.dockerExec(t, "podman network create ingress")
	e.dockerExec(t, "podman run -d --name caddy --network ingress --publish 80:80 -v /etc/caddy:/etc/caddy:Z docker.io/library/caddy:2-alpine")

	// 1. Deploy on a clean tree. First deploy prepares the per-env user,
	// paths, identity, and per-(app, env) network before the release starts.
	firstDeploy := e.runSimpleVPS(t, app, nil)
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
	assertContains(t, labels, "simple-vps.app=api")
	assertContains(t, labels, "simple-vps.env="+productionEnv)
	assertContains(t, labels, "simple-vps.process=web")
	assertContains(t, labels, "simple-vps.release="+release)
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

	// 8. A second deploy on the same source must start a replacement
	// container before Caddy moves traffic, instead of removing the routed
	// container name up front.
	firstFragment := fragment
	commandsBeforeRedeploy := e.ssh(t, "cat /run/fake-podman/commands.log")
	e.simpleVPS(t, app, nil)
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
	e.simpleVPS(t, app, nil, "--rebuild")
	commandsLog = e.ssh(t, "cat /run/fake-podman/commands.log")
	assertContains(t, commandsLog, "podman build --no-cache --pull=always")

}

func (e *smokeEnv) testReleaseCommandFailure(t *testing.T) {
	app := filepath.Join(e.tmp, "release-fail")
	mustMkdir(t, app)
	writeReleaseFailFixture(t, app)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil)
	e.dockerExec(t, "test -f "+identity.DataDir("releasefail", productionEnv)+"/release-ok")
	stableEnv := e.dockerExec(t, "cat "+identity.EnvFile("releasefail", productionEnv))
	assertContains(t, stableEnv, "MARKER=stable")
	statusJSON := e.simpleVPS(t, app, nil, "status", "--json")
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
	failingManifest := strings.Replace(string(manifest), `release = "touch /data/release-ok"`, `release = "simple-vps-fail-release"`, 1)
	failingManifest = strings.Replace(failingManifest, `MARKER = "stable"`, `MARKER = "failed"`, 1)
	if failingManifest == string(manifest) {
		t.Fatal("release failure fixture did not contain the success release command")
	}
	mustWrite(t, manifestPath, failingManifest)
	e.commitFixture(t, app)
	failedRelease := gitRelease(t, e, app)
	failed := e.runSimpleVPS(t, app, nil)
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
	e.dockerExec(t, "test ! -e /tmp/simple-vps-deploy/releasefail-"+productionEnv+"-"+failedRelease)
	envAfterFailure := e.dockerExec(t, "cat "+identity.EnvFile("releasefail", productionEnv))
	if envAfterFailure != stableEnv {
		t.Fatalf("failing release command changed runtime env:\nbefore:\n%s\nafter:\n%s", stableEnv, envAfterFailure)
	}
}

func (e *smokeEnv) testCaddySwitchFailureRollback(t *testing.T) {
	app := filepath.Join(e.tmp, "caddy-fail")
	mustMkdir(t, app)
	writeCaddyFailFixture(t, app)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil)
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

	failed := e.runSimpleVPS(t, app, nil)
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

	e.simpleVPS(t, app, nil)
	e.ssh(t, "test -f "+identity.IdentityFile("branchapi", "prod"))

	mustWrite(t, filepath.Join(app, "dirty.txt"), "dirty deploy payload")
	rejected := e.runSimpleVPS(t, app, nil)
	if rejected.err == nil {
		t.Fatal("production branch deploy should reject a dirty worktree")
	}
	assertContains(t, rejected.stdout+rejected.stderr, "dirty_worktree")
	assertContains(t, rejected.stdout+rejected.stderr, "next: git commit")
	if err := os.Remove(filepath.Join(app, "dirty.txt")); err != nil {
		t.Fatal(err)
	}

	mustWrite(t, filepath.Join(app, "README.md"), "new production\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "new production")
	e.simpleVPS(t, app, nil)
	e.mustRun(t, app, nil, "git", "reset", "--hard", baseCommit)
	behind := e.runSimpleVPS(t, app, nil)
	if behind.err == nil {
		t.Fatal("production deploy from behind checkout should fail")
	}
	assertContains(t, behind.stdout+behind.stderr, "behind_production")
	assertContains(t, behind.stdout+behind.stderr, "next: git pull")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feat/x")
	mustWrite(t, filepath.Join(app, "preview-dirty.txt"), "dirty preview payload")
	e.simpleVPS(t, app, nil)
	featEnv := previewEnvForBranch(t, e, "branchapi", "feat/x")
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
	textStatus := e.simpleVPS(t, app, nil, "status")
	assertContains(t, textStatus, "Preview feat/x")
	assertContains(t, textStatus, "(dirty")

	checkedOutBranchFlag := e.runSimpleVPS(t, app, nil, "--branch", "feat/x")
	if checkedOutBranchFlag.err == nil {
		t.Fatal("deploy --branch should fail while a branch is checked out")
	}
	assertContains(t, checkedOutBranchFlag.stdout+checkedOutBranchFlag.stderr, "branch_flag_requires_detached_head")

	e.mustRun(t, app, nil, "git", "checkout", "--detach")
	detachedWithoutBranch := e.runSimpleVPS(t, app, nil)
	if detachedWithoutBranch.err == nil {
		t.Fatal("detached HEAD deploy without --branch should fail")
	}
	assertContains(t, detachedWithoutBranch.stdout+detachedWithoutBranch.stderr, "detached_head_requires_branch")
	e.simpleVPS(t, app, nil, "--branch", "feat/x")
	if again := previewEnvForBranch(t, e, "branchapi", "feat/x"); again != featEnv {
		t.Fatalf("re-ship should keep preview env stable: first=%s second=%s", featEnv, again)
	}

	e.mustRun(t, app, nil, "git", "checkout", "-B", "mañana/Über")
	e.simpleVPS(t, app, nil)
	accentEnv := previewEnvForBranch(t, e, "branchapi", "mañana/Über")
	assertPreviewEnvName(t, accentEnv, "ma-ana-ber")
	e.ssh(t, "test -f "+identity.IdentityFile("branchapi", accentEnv))

	longBranch := "feature/abcdefghijklmnopqrstuvwxyz0123456789"
	e.mustRun(t, app, nil, "git", "checkout", "-B", longBranch)
	e.simpleVPS(t, app, nil)
	longEnv := previewEnvForBranch(t, e, "branchapi", longBranch)
	assertPreviewEnvName(t, longEnv, "feature-abcdefghijklmnopqrst")
	e.ssh(t, "test -f "+identity.IdentityFile("branchapi", longEnv))

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feat/login", "main")
	e.simpleVPS(t, app, nil)
	slashEnv := previewEnvForBranch(t, e, "branchapi", "feat/login")
	assertPreviewEnvName(t, slashEnv, "feat-login")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feat.login", "main")
	e.simpleVPS(t, app, nil)
	dotEnv := previewEnvForBranch(t, e, "branchapi", "feat.login")
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

	e.simpleVPS(t, app, nil)
	e.ssh(t, "test -f "+identity.IdentityFile("previewapi", "prod"))

	unknown := e.runSimpleVPS(t, app, nil, "pin", "ghost/branch")
	if unknown.err == nil {
		t.Fatal("pin for an unmapped preview branch should fail")
	}
	assertContains(t, unknown.stdout+unknown.stderr, "unknown_preview_branch")
	assertContains(t, unknown.stdout+unknown.stderr, "next: ship")

	e.mustRun(t, app, nil, "git", "checkout", "-B", "feature/lifecycle")
	e.simpleVPS(t, app, nil)
	previewEnv := previewEnvForBranch(t, e, "previewapi", "feature/lifecycle")
	assertPreviewEnvName(t, previewEnv, "feature-lifecycle")
	firstIdentity := readPreviewIdentity(t, e, "previewapi", previewEnv)
	if firstIdentity.Preview == nil || firstIdentity.Preview.Pinned {
		t.Fatalf("unexpected first preview identity: %+v", firstIdentity)
	}
	if firstIdentity.Preview.Branch != "feature/lifecycle" || firstIdentity.Preview.SanitizedBranch != "feature-lifecycle" {
		t.Fatalf("preview mapping should store raw and sanitized branch names: %+v", firstIdentity.Preview)
	}
	firstExpiry := parseRemoteTime(t, *firstIdentity.Preview.ExpiresAt)

	e.simpleVPS(t, app, []byte("throwaway"), "secret", "set", "cleanup_key")
	time.Sleep(time.Second)
	mustWrite(t, filepath.Join(app, "README.md"), "second preview ship\n")
	e.mustRun(t, app, nil, "git", "add", ".")
	e.mustRun(t, app, nil, "git", "commit", "-q", "-m", "second preview")
	e.simpleVPS(t, app, nil)
	if again := previewEnvForBranch(t, e, "previewapi", "feature/lifecycle"); again != previewEnv {
		t.Fatalf("same branch should keep suffix stable: first=%s second=%s", previewEnv, again)
	}
	refreshed := readPreviewIdentity(t, e, "previewapi", previewEnv)
	refreshedExpiry := parseRemoteTime(t, *refreshed.Preview.ExpiresAt)
	if !refreshedExpiry.After(firstExpiry) {
		t.Fatalf("expiry should refresh on re-ship: before=%s after=%s", firstExpiry, refreshedExpiry)
	}

	e.simpleVPS(t, app, nil, "pin", "feature/lifecycle")
	pinned := readPreviewIdentity(t, e, "previewapi", previewEnv)
	if pinned.Preview == nil || !pinned.Preview.Pinned || pinned.Preview.ExpiresAt != nil {
		t.Fatalf("pin should clear expiry: %+v", pinned.Preview)
	}
	e.dockerExec(t, "/usr/local/bin/ship server env reap")
	e.dockerExec(t, "test -d "+identity.EnvRoot("previewapi", previewEnv))

	e.simpleVPS(t, app, nil, "unpin", "feature/lifecycle")
	unpinned := readPreviewIdentity(t, e, "previewapi", previewEnv)
	if unpinned.Preview == nil || unpinned.Preview.Pinned || unpinned.Preview.ExpiresAt == nil {
		t.Fatalf("unpin should restore expiry: %+v", unpinned.Preview)
	}
	forcePreviewExpired(t, e, "previewapi", previewEnv)
	reapOutput := e.dockerExec(t, "/usr/local/bin/ship server env reap")
	assertContains(t, reapOutput, "Reaped preview previewapi ("+previewEnv+") branch=feature/lifecycle")
	e.dockerExec(t, "test ! -e "+identity.EnvRoot("previewapi", previewEnv))
	e.dockerExec(t, "test ! -e /etc/simple-vps/secrets/previewapi/"+previewEnv)
	e.dockerExec(t, "test -e "+identity.EnvRoot("previewapi", "prod"))
}

func (e *smokeEnv) testBackupRestore(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	e.dockerExec(t, "printf 'durable-state' > "+identity.DataDir("api", productionEnv)+"/data.txt")

	prodStatus := statusEnvByKind(t, e, app, "Production")
	backupRelease := prodStatus.Release
	backupID := backupIDFromSaveOutput(t, e.simpleVPS(t, app, nil, "save"))

	mustWrite(t, filepath.Join(app, "README.md"), "post-backup release\n")
	e.commitFixture(t, app)
	newRelease := gitRelease(t, e, app)
	if newRelease == backupRelease {
		t.Fatal("expected fixture commit to produce a new release")
	}
	e.simpleVPS(t, app, nil)
	newContainer := identity.ContainerName("api", productionEnv, "web", newRelease)
	e.dockerExec(t, "test -f /run/fake-podman/containers/"+newContainer+".labels")

	e.simpleVPS(t, app, nil, "restore", "--from", backupID)
	e.dockerExec(t, "test ! -f /run/fake-podman/containers/"+newContainer+".labels")
	statusJSON := e.simpleVPS(t, app, nil, "status", "--json")
	assertContains(t, statusJSON, `"release": "`+backupRelease+`"`)

	e.simpleVPS(t, app, nil, "rm", prodStatus.Branch, "--confirm", "api")
	if exists := e.run(t, e.repoRoot, nil, "docker", "exec", e.container, "bash", "-c", "test -e "+identity.DataDir("api", productionEnv)+"/data.txt"); exists.err == nil {
		t.Fatal("destroy should remove data before restore")
	}

	e.simpleVPS(t, app, nil, "restore", "--from", backupID)
	got := strings.TrimSpace(e.dockerExec(t, "cat "+identity.DataDir("api", productionEnv)+"/data.txt"))
	if got != "durable-state" {
		t.Fatalf("restored data = %q, want durable-state", got)
	}
	envRootStat := strings.TrimSpace(e.dockerExec(t, "stat -c '%a %U' "+identity.EnvRoot("api", productionEnv)))
	if envRootStat != "755 root" {
		t.Fatalf("restored env root ownership = %q, want `755 root`", envRootStat)
	}
	dataStat := strings.TrimSpace(e.dockerExec(t, "stat -c '%a %U' "+identity.DataDir("api", productionEnv)))
	if dataStat != "2775 "+identity.SystemUser("api", productionEnv) {
		t.Fatalf("restored data ownership = %q, want `2775 %s`", dataStat, identity.SystemUser("api", productionEnv))
	}
	status := e.simpleVPS(t, app, nil, "status")
	assertContains(t, status, "Production "+prodStatus.Branch)
	assertContains(t, status, "health=healthy")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: api.example.com' http://127.0.0.1/health", "ok")
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
	e.simpleVPS(t, app, nil)
	newFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("api", productionEnv))
	assertContains(t, newFragment, ":"+"3333")

	prodStatus := statusEnvByKind(t, e, app, "Production")
	rollbackText := e.simpleVPS(t, app, nil, "rollback")
	assertContains(t, rollbackText, "Rolled back Production "+prodStatus.Branch+" from "+newRelease+" to "+oldRelease)
	assertContains(t, rollbackText, "web")

	status := statusEnvByKind(t, e, app, "Production")
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
			results <- e.runSimpleVPS(t, app, nil)
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

	status := e.simpleVPS(t, app, nil, "status")
	assertContains(t, status, "Production ")
	assertContains(t, status, "health=healthy")
}

func (e *smokeEnv) testRemovedProcessReconciliation(t *testing.T) {
	app := filepath.Join(e.tmp, "prune-api")
	mustMkdir(t, app)
	writePruneFixture(t, app)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil)
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
	e.simpleVPS(t, app, nil)

	e.dockerExec(t, "test ! -f /run/fake-podman/containers/"+oldWorker+".labels")
	statusJSON := e.simpleVPS(t, app, nil, "status", "--json")
	if strings.Contains(statusJSON, `"process": "worker"`) {
		t.Fatalf("removed worker still appears in status:\n%s", statusJSON)
	}
}

func (e *smokeEnv) testStaticOnlyAppLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "static-site")
	mustMkdir(t, app)
	writeStaticFixture(t, app)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil)
	oldRelease := currentStaticReleaseFor(t, e, "site", productionEnv)
	staticReleaseManifest := e.ssh(t, "cat "+identity.ReleaseManifestFile("site", productionEnv, oldRelease))
	assertContains(t, staticReleaseManifest, `static = "dist"`)

	status := e.simpleVPS(t, app, nil, "status")
	assertContains(t, status, "Production ")
	assertContains(t, status, "release="+oldRelease)
	assertContains(t, status, "health=healthy")

	rawListJSON := e.simpleVPS(t, app, nil, "box", "ls", "--json")
	var listPayload struct {
		Apps []struct {
			App       string `json:"app"`
			Env       string `json:"env"`
			Processes []struct {
				Process string `json:"process"`
			} `json:"processes"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(rawListJSON), &listPayload); err != nil {
		t.Fatalf("app list --json output not parseable as JSON: %v\nraw:\n%s", err, rawListJSON)
	}
	foundStatic := false
	for _, listed := range listPayload.Apps {
		if listed.App == "site" && listed.Env == productionEnv && len(listed.Processes) == 0 {
			foundStatic = true
		}
	}
	if !foundStatic {
		t.Fatalf("app list --json missing static-only site env:\n%+v", listPayload.Apps)
	}

	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-ok")

	mustWrite(t, filepath.Join(app, "dist", "index.html"), "static-v2")
	e.commitFixture(t, app)
	e.simpleVPS(t, app, nil)
	newRelease := currentStaticReleaseFor(t, e, "site", productionEnv)
	if newRelease == oldRelease {
		t.Fatal("expected static fixture deploy to produce a new release")
	}
	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-v2")

	rawRollback := e.simpleVPS(t, app, nil, "rollback")
	prodStatus := statusEnvByKind(t, e, app, "Production")
	assertContains(t, rawRollback, "Rolled back Production "+prodStatus.Branch+" from "+newRelease+" to "+oldRelease)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-ok")

	backupID := backupIDFromSaveOutput(t, e.simpleVPS(t, app, nil, "save"))

	e.simpleVPS(t, app, nil, "rm", prodStatus.Branch, "--confirm", "site")
	e.simpleVPS(t, app, nil, "restore", "--from", backupID)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-ok")
}

func (e *smokeEnv) testMixedContainerStaticLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "mixed-api")
	mustMkdir(t, app)
	writeMixedFixture(t, app)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil)
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
	e.simpleVPS(t, app, nil)
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

	e.simpleVPS(t, app, nil, "rollback")
	prodStatus := statusEnvByKind(t, e, app, "Production")
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", productionEnv))
	assertContains(t, fragment, "reverse_proxy http://"+oldWeb+":3000")
	assertContains(t, fragment, `root * "`+staticRouteRoot("mix", productionEnv, oldRelease, "mixed.example.com/docs")+`"`)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v1")

	backupID := backupIDFromSaveOutput(t, e.simpleVPS(t, app, nil, "save"))
	e.simpleVPS(t, app, nil)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v2")
	e.simpleVPS(t, app, nil, "restore", "--from", backupID)
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", productionEnv))
	assertContains(t, fragment, "reverse_proxy http://"+oldWeb+":3000")
	assertContains(t, fragment, `root * "`+staticRouteRoot("mix", productionEnv, oldRelease, "mixed.example.com/docs")+`"`)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v1")

	e.simpleVPS(t, app, nil, "rm", prodStatus.Branch, "--confirm", "mix")
	e.simpleVPS(t, app, nil, "restore", "--from", backupID)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/health", "ok")
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
	e.simpleVPS(t, app, nil)
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", productionEnv))
	if strings.Contains(fragment, "docs-dist") || strings.Contains(fragment, "/docs") {
		t.Fatalf("removed static route still appears in Caddy fragment:\n%s", fragment)
	}
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "ok")
}

// testSecretLifecycle covers the @secret:KEY resolution path end-to-end
// against the helper-side store under /etc/simple-vps/secrets/:
//
//  1. setup the app/env baseline
//  2. `ship secret set` over SSH-stdin (value never on argv)
//  3. `ship secret ls` shows the key (NOT the value)
//  4. deploy a manifest that references @secret:db_url
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
DATABASE_URL = "@secret:db_url"

[processes]
web = { port = 3000 }

[routes]
"sec.example.com" = "web"
`)
	e.commitFixture(t, app)

	// 1. set: value over stdin, never argv. The fake-VPS harness uses
	// docker exec ssh under the hood and the helper reads from its
	// own stdin in `secret set`.
	e.simpleVPS(t, app, []byte("postgres://verybadidea"), "secret", "set", "db_url")

	// 2. file lands at the expected path, root-owned, mode 0600,
	// containing the value verbatim (no trailing newline — the client
	// trims one if present). Read as root via dockerExec because the
	// deploy user only has passwordless sudo for /usr/local/bin/ship.
	secretDir := "/etc/simple-vps/secrets/sec/" + productionEnv
	listing := strings.TrimSpace(e.dockerExec(t, "ls -l "+secretDir))
	if !strings.Contains(listing, " db_url") {
		t.Fatalf("expected db_url in %s listing:\n%s", secretDir, listing)
	}
	if !strings.Contains(listing, "-rw-------") {
		t.Fatalf("secret file is not mode 0600:\n%s", listing)
	}
	body := strings.TrimSuffix(e.dockerExec(t, "cat "+secretDir+"/db_url"), "\n")
	if body != "postgres://verybadidea" {
		t.Fatalf("secret value didn't round-trip:\nwant: postgres://verybadidea\n got: %q", body)
	}

	// 3. list shows the key — NEVER the value.
	listing = e.simpleVPS(t, app, nil, "secret", "ls")
	if !strings.Contains(listing, "db_url") {
		t.Fatalf("secret list missing db_url:\n%s", listing)
	}
	if strings.Contains(listing, "postgres://") {
		t.Fatalf("secret list leaked the value:\n%s", listing)
	}
	rawSecretJSON := e.simpleVPS(t, app, nil, "secret", "ls", "--json")
	var secretPayload struct {
		App  string   `json:"app"`
		Env  string   `json:"env"`
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(rawSecretJSON), &secretPayload); err != nil {
		t.Fatalf("secret list --json output not parseable as JSON: %v\nraw:\n%s", err, rawSecretJSON)
	}
	if secretPayload.App != "sec" || secretPayload.Env != productionEnv || len(secretPayload.Keys) != 1 || secretPayload.Keys[0] != "db_url" {
		t.Fatalf("unexpected secret list --json payload: %+v", secretPayload)
	}
	if strings.Contains(rawSecretJSON, "postgres://") {
		t.Fatalf("secret list --json leaked the value:\n%s", rawSecretJSON)
	}

	// 4. deploy: helper resolves @secret:db_url into the env file
	// next to the literal LOG_LEVEL.
	e.simpleVPS(t, app, nil)
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
	e.simpleVPS(t, app, nil, "secret", "rm", "db_url")
	if missing := strings.TrimSpace(e.dockerExec(t, "ls "+secretDir)); missing != "" {
		t.Fatalf("expected empty secret dir after rm, got:\n%s", missing)
	}

	// 7. next deploy with the ref still in the manifest fails fast.
	result := e.runSimpleVPS(t, app, nil)
	if result.err == nil {
		t.Fatal("expected deploy to fail with unresolved @secret reference")
	}
	if !strings.Contains(result.stderr+result.stdout, "missing secret db_url") ||
		!strings.Contains(result.stderr+result.stdout, "ship secret set db_url") {
		t.Fatalf("preflight error must name the missing secret and set command, got:\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
	}
}

// testStatusAndLogs covers the read-only operator surface. It assumes
// the earlier subtests have already deployed the `api` container app
// and left its `web` process running.
func (e *smokeEnv) testStatusAndLogs(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")

	// Text status surfaces the web process, its container, and the
	// release label baked in by `app apply`.
	text := e.simpleVPS(t, app, nil, "status")
	assertContains(t, text, "Production ")
	assertContains(t, text, "release=")
	assertContains(t, text, "health=healthy")
	if strings.Contains(text, "No live envs") {
		t.Fatalf("status reported no live envs after a successful deploy:\n%s", text)
	}

	// JSON status carries the same data in a structured shape.
	// Parse it back to prove the contract — text-mode regressions
	// might still slip through a substring check.
	payload := statusPayloadForApp(t, e, app)
	env := statusEnvByKindFromPayload(t, payload, "Production")
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

	// Host-level app listing is sourced from Podman labels instead
	// of the removed apps.json/routes.json registries.
	rawListJSON := e.simpleVPS(t, app, nil, "box", "ls", "--json")
	var listPayload struct {
		Apps []struct {
			App       string `json:"app"`
			Env       string `json:"env"`
			Processes []struct {
				Process string `json:"process"`
				State   string `json:"state"`
			} `json:"processes"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(rawListJSON), &listPayload); err != nil {
		t.Fatalf("app list --json output not parseable as JSON: %v\nraw:\n%s", err, rawListJSON)
	}
	if len(listPayload.Apps) == 0 {
		t.Fatalf("app list --json returned no apps after deploy:\n%s", rawListJSON)
	}
	found := false
	for _, listed := range listPayload.Apps {
		if listed.App == "api" && listed.Env == productionEnv && len(listed.Processes) == 1 && listed.Processes[0].Process == "web" {
			found = true
		}
	}
	if !found {
		t.Fatalf("app list --json missing api/production/web process:\n%+v", listPayload.Apps)
	}

	// Logs reaches `podman logs` on the right container and prints
	// the deterministic stub line.
	logs := e.simpleVPS(t, app, nil, "logs", "web")
	assertContains(t, logs, "fake podman logs for "+env.Processes[0].Container)

	logsJSON := e.simpleVPS(t, app, nil, "logs", "web", "--json")
	var logsPayload struct {
		App     string   `json:"app"`
		Env     string   `json:"env"`
		Process string   `json:"process"`
		Lines   []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(logsJSON), &logsPayload); err != nil {
		t.Fatalf("logs --json output not parseable as JSON: %v\nraw:\n%s", err, logsJSON)
	}
	if logsPayload.App != "api" || logsPayload.Env != productionEnv || logsPayload.Process != "web" || len(logsPayload.Lines) == 0 {
		t.Fatalf("unexpected logs --json payload: %+v", logsPayload)
	}

	// Process argument is optional when exactly one process exists.
	logsNoSvc := e.simpleVPS(t, app, nil, "logs")
	assertContains(t, logsNoSvc, "fake podman logs for "+env.Processes[0].Container)

	// Unknown process errors clearly.
	missing := e.runSimpleVPS(t, app, nil, "logs", "nope")
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
	prodStatus := statusEnvByKind(t, e, app, "Production")

	// Client safety gate: no accidental teardown without either
	// --confirm <app>.
	missingConfirm := e.runSimpleVPS(t, app, nil, "rm", prodStatus.Branch)
	if missingConfirm.err == nil {
		t.Fatal("expected rm without confirmation to fail")
	}
	if !strings.Contains(missingConfirm.stderr+missingConfirm.stdout, "--confirm api") {
		t.Fatalf("confirmation error should name the app, got:\nstdout: %s\nstderr: %s", missingConfirm.stdout, missingConfirm.stderr)
	}

	// Give --purge something observable to remove.
	e.simpleVPS(t, app, []byte("throwaway"), "secret", "set", "cleanup_key")
	currentContainer := currentWebContainer(t, e, app)

	out := e.simpleVPS(t, app, nil, "rm", prodStatus.Branch, "--confirm", "api")
	assertContains(t, out, "Removed Production "+prodStatus.Branch)

	commandsLog := e.ssh(t, "cat /run/fake-podman/commands.log")
	assertContains(t, commandsLog, "podman rm -f "+currentContainer)
	assertContains(t, commandsLog, "podman network rm "+identity.Network("api", productionEnv))
	assertContains(t, commandsLog, "podman exec caddy caddy reload --config /etc/caddy/Caddyfile")

	e.dockerExec(t, "test ! -e /run/fake-podman/containers/"+currentContainer+".labels")
	e.dockerExec(t, "test ! -e /run/fake-podman/networks/"+identity.Network("api", productionEnv))
	e.dockerExec(t, "test ! -e "+identity.CaddyFragmentFile("api", productionEnv))
	e.dockerExec(t, "test ! -e "+identity.EnvRoot("api", productionEnv))
	e.dockerExec(t, "test ! -e /etc/simple-vps/secrets/api/"+productionEnv)
	e.dockerExec(t, "! getent passwd "+identity.SystemUser("api", productionEnv)+" >/dev/null")

	status := e.simpleVPS(t, app, nil, "status")
	assertContains(t, status, "No live envs for api")

	// Fake Caddy re-reads conf.d on every request, so route removal is
	// visible immediately after the reload.
	e.ssh(t, "if curl -fsS -H 'Host: api.example.com' http://127.0.0.1/health; then exit 1; fi")

	// Idempotence: a second destroy should be a no-op, not an error.
	again := e.simpleVPS(t, app, nil, "rm", prodStatus.Branch, "--confirm", "api")
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

func writeDirtyFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "dirtyapi"
box = "fake-vps"
probe = "/health"

[processes]
web = { port = 3000 }

[routes]
"dirty.example.com" = "web"
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

[processes]
web = { port = 3000 }

[routes]
"release-fail.example.com" = "web"
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
	env := statusEnvByKind(t, e, app, "Production")
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
	Kind      string `json:"kind"`
	Branch    string `json:"branch"`
	URL       string `json:"url"`
	Env       string `json:"env"`
	Release   string `json:"release"`
	Health    string `json:"health"`
	ExpiresAt string `json:"expiresAt"`
	Pinned    bool   `json:"pinned"`
	Dirty     bool   `json:"dirty"`
	Processes []struct {
		Process   string `json:"process"`
		Container string `json:"container"`
		State     string `json:"state"`
		Release   string `json:"release"`
	} `json:"processes"`
}

func statusPayloadForApp(t *testing.T, e *smokeEnv, app string) smokeStatusPayload {
	t.Helper()
	rawJSON := e.simpleVPS(t, app, nil, "status", "--json")
	var payload smokeStatusPayload
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		t.Fatalf("status --json output not parseable as JSON: %v\nraw:\n%s", err, rawJSON)
	}
	return payload
}

func statusEnvByKind(t *testing.T, e *smokeEnv, app string, kind string) smokeStatusEnv {
	t.Helper()
	return statusEnvByKindFromPayload(t, statusPayloadForApp(t, e, app), kind)
}

func statusEnvByKindFromPayload(t *testing.T, payload smokeStatusPayload, kind string) smokeStatusEnv {
	t.Helper()
	for _, env := range payload.Envs {
		if env.Kind == kind {
			return env
		}
	}
	t.Fatalf("status missing %s env: %+v", kind, payload.Envs)
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

func backupIDFromSaveOutput(t *testing.T, output string) string {
	t.Helper()
	fields := strings.Fields(output)
	if len(fields) < 3 || fields[0] != "Created" || fields[1] != "backup" {
		t.Fatalf("unexpected save output: %q", output)
	}
	name := filepath.Base(fields[2])
	id := strings.TrimSuffix(name, ".tar")
	if id == "" || id == name {
		t.Fatalf("save output did not contain a backup tar path: %q", output)
	}
	return id
}

type previewIdentityPayload struct {
	Version int `json:"version"`
	App     string
	Env     string
	Preview *struct {
		Branch          string  `json:"branch"`
		SanitizedBranch string  `json:"sanitized_branch"`
		Env             string  `json:"env"`
		Suffix          string  `json:"suffix"`
		LastShipAt      string  `json:"last_ship_at"`
		ExpiresAt       *string `json:"expires_at"`
		Pinned          bool    `json:"pinned"`
	} `json:"preview"`
}

func previewEnvForBranch(t *testing.T, e *smokeEnv, app, branch string) string {
	t.Helper()
	out := e.ssh(t, "sudo -n /usr/local/bin/ship server app preview resolve "+app+" "+shellQuote(branch))
	return strings.TrimSpace(out)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
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

func forcePreviewExpired(t *testing.T, e *smokeEnv, app, env string) {
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

package fakevps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fprl/simple-vps/internal/identity"
)

// TestContainerSmoke exercises the new container-deploy lifecycle (ADR-0005
// + ADR-0006 Cut 2) end-to-end against the fake-vps fixture:
//
//   - `simple-vps setup --env production` calls `server app setup-env`, which
//     creates the per-env Linux user, on-disk layout under
//     /var/apps/<app>.<env>/, and the per-(app, env) Podman network.
//   - `simple-vps deploy --env production` tars the working tree, uploads the
//     manifest, calls `server app apply`, which runs `podman build` +
//     `podman run` (§7 hardening subset) without any host-port
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
	t.Run("release command failure leaves old traffic unchanged", env.testReleaseCommandFailure)
	t.Run("dirty deploy records base commit and status", env.testDirtyDeployStatus)
	t.Run("container rollback runs an older image release", env.testRollback)
	t.Run("backup and restore round-trip app state", env.testBackupRestore)
	t.Run("deploy removes processes dropped from the manifest", env.testRemovedProcessReconciliation)
	t.Run("concurrent deploys of the same app env serialize", env.testConcurrentDeploys)
	t.Run("static-only app deploys and restores without containers", env.testStaticOnlyAppLifecycle)
	t.Run("mixed container and static routes deploy as one release", env.testMixedContainerStaticLifecycle)
	t.Run("@secret refs resolve through set/list/rm into the runtime env", env.testSecretLifecycle)
	t.Run("status + logs surface deployed processes without SSHing in", env.testStatusAndLogs)
	t.Run("restart bounces containers in place via podman restart", env.testRestart)
	t.Run("destroy tears down one app environment", env.testDestroy)
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
	e.dockerExec(t, `cat > /etc/simple-vps/host.json <<'EOF'
{"version":1,"desired":{"users":{"operator":"operator","deploy":"deploy"},"ingress":{"expose":"public","tunnel":"none"},"features":{},"packages":{"podman":{"source":"apt"},"rsync":{"source":"apt"},"caddy":{"source":"container"}}},"observed":{"packages":{},"ingress":{}},"meta":{}}
EOF`)
	e.dockerExec(t, "mkdir -p /etc/caddy/simple-vps /etc/caddy/conf.d /var/lib/caddy")
	e.dockerExec(t, `cat > /etc/caddy/Caddyfile <<'EOF'
import simple-vps/*.caddy
import conf.d/*.caddy
EOF`)
	e.dockerExec(t, "podman network create ingress")
	e.dockerExec(t, "podman run -d --name caddy --network ingress --publish 80:80 -v /etc/caddy:/etc/caddy:Z docker.io/library/caddy:2-alpine")

	// 1. Setup creates the per-env user, paths, and per-(app, env) network.
	e.simpleVPS(t, app, nil, "setup", "--env", "production")

	e.ssh(t, "getent passwd "+identity.SystemUser("api", "production")+" >/dev/null")
	e.ssh(t, "test -d "+identity.DataDir("api", "production"))
	e.ssh(t, "test -d "+identity.ReleaseDir("api", "production"))
	e.ssh(t, "test -f "+identity.IdentityFile("api", "production"))
	e.ssh(t, "test -f /run/fake-podman/networks/"+identity.Network("api", "production"))
	releaseDirStat := strings.TrimSpace(e.dockerExec(t, "stat -c '%a %U' "+identity.ReleaseDir("api", "production")))
	if releaseDirStat != "755 root" {
		t.Fatalf("release dir ownership = %q, want `755 root`", releaseDirStat)
	}

	// 2. Deploy on a clean tree.
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	release := gitRelease(t, e, app)
	webContainer := identity.ContainerName("api", "production", "web", release)
	releaseManifest := e.ssh(t, "cat "+identity.ReleaseManifestFile("api", "production", release))
	assertContains(t, releaseManifest, "port = 3000")
	releaseMetadata := e.ssh(t, "cat "+identity.ReleaseMetadataFile("api", "production", release))
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
	assertContains(t, commandsLog, "--network "+identity.Network("api", "production"))
	assertContains(t, commandsLog, "--network ingress")

	// 4. App container must NOT carry the host-port label (that path is
	// gone with Caddy-in-container) and the run line must NOT carry a
	// --publish (no host loopback ingress).
	labels := e.ssh(t, "cat /run/fake-podman/containers/"+webContainer+".labels")
	assertContains(t, labels, "simple-vps.app=api")
	assertContains(t, labels, "simple-vps.env=production")
	assertContains(t, labels, "simple-vps.process=web")
	assertContains(t, labels, "simple-vps.release="+release)
	if strings.Contains(commandsLog, "--publish 127.0.0.1:") {
		t.Fatalf("app container still publishes a host loopback port; Caddy-in-container should drop this:\n%s", commandsLog)
	}

	// 5. Caddy fragment should reverse-proxy via container DNS, not
	// 127.0.0.1.
	fragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("api", "production"))
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
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	secondFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("api", "production"))
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
	e.simpleVPS(t, app, nil, "deploy", "--env", "production", "--rebuild")
	commandsLog = e.ssh(t, "cat /run/fake-podman/commands.log")
	assertContains(t, commandsLog, "podman build --no-cache --pull=always")

}

func (e *smokeEnv) testReleaseCommandFailure(t *testing.T) {
	app := filepath.Join(e.tmp, "release-fail")
	mustMkdir(t, app)
	writeReleaseFailFixture(t, app)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil, "setup", "--env", "production")
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	e.dockerExec(t, "test -f "+identity.DataDir("releasefail", "production")+"/release-ok")
	stableEnv := e.dockerExec(t, "cat "+identity.EnvFile("releasefail", "production"))
	assertContains(t, stableEnv, "MARKER=stable")
	statusJSON := e.simpleVPS(t, app, nil, "status", "--json", "--env", "production")
	if strings.Contains(statusJSON, `"process":"release"`) || strings.Contains(statusJSON, `"process": "release"`) {
		t.Fatalf("release command container polluted status:\n%s", statusJSON)
	}
	stableFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("releasefail", "production"))
	stableContainer := currentWebContainer(t, e, app)

	manifestPath := filepath.Join(app, "simple-vps.toml")
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
	failed := e.runSimpleVPS(t, app, nil, "deploy", "--env", "production")
	if failed.err == nil {
		t.Fatal("deploy with failing release command should fail")
	}
	assertContains(t, failed.stdout+failed.stderr, "release command")
	assertContains(t, failed.stdout+failed.stderr, "failed before traffic switch")
	fragmentAfterFailure := e.ssh(t, "cat "+identity.CaddyFragmentFile("releasefail", "production"))
	if fragmentAfterFailure != stableFragment {
		t.Fatalf("failing release command changed traffic:\nbefore:\n%s\nafter:\n%s", stableFragment, fragmentAfterFailure)
	}
	e.dockerExec(t, "test -e /run/fake-podman/containers/"+stableContainer+".labels")
	e.dockerExec(t, "test ! -e /tmp/simple-vps-deploy/releasefail-production-"+failedRelease)
	envAfterFailure := e.dockerExec(t, "cat "+identity.EnvFile("releasefail", "production"))
	if envAfterFailure != stableEnv {
		t.Fatalf("failing release command changed runtime env:\nbefore:\n%s\nafter:\n%s", stableEnv, envAfterFailure)
	}
}

func (e *smokeEnv) testDirtyDeployStatus(t *testing.T) {
	app := filepath.Join(e.tmp, "dirty-api")
	mustMkdir(t, app)
	writeDirtyFixture(t, app)
	e.commitFixture(t, app)
	baseCommit := strings.TrimSpace(e.mustRun(t, app, nil, "git", "rev-parse", "HEAD"))
	baseShort := gitRelease(t, e, app)

	e.simpleVPS(t, app, nil, "setup", "--env", "production")
	mustWrite(t, filepath.Join(app, "dirty.txt"), "dirty deploy payload")
	rejected := e.runSimpleVPS(t, app, nil, "deploy", "--env", "production")
	if rejected.err == nil {
		t.Fatal("deploy without --dirty should reject a dirty worktree")
	}
	assertContains(t, rejected.stdout+rejected.stderr, "working tree is dirty")

	e.simpleVPS(t, app, nil, "deploy", "--env", "production", "--dirty")
	rawStatus := e.simpleVPS(t, app, nil, "status", "--json", "--env", "production")
	var status struct {
		Release struct {
			Release    string `json:"release"`
			Dirty      bool   `json:"dirty"`
			BaseCommit string `json:"base_commit"`
		} `json:"release"`
	}
	if err := json.Unmarshal([]byte(rawStatus), &status); err != nil {
		t.Fatalf("status --json output not parseable as JSON: %v\nraw:\n%s", err, rawStatus)
	}
	if !status.Release.Dirty {
		t.Fatalf("expected dirty release in status: %+v", status.Release)
	}
	if status.Release.BaseCommit != baseCommit {
		t.Fatalf("dirty base commit = %q, want %q", status.Release.BaseCommit, baseCommit)
	}
	if !strings.HasPrefix(status.Release.Release, baseShort+"-dirty-") {
		t.Fatalf("dirty release id %q should start with %s-dirty-", status.Release.Release, baseShort)
	}
	releaseMetadata := e.ssh(t, "cat "+identity.ReleaseMetadataFile("dirtyapi", "production", status.Release.Release))
	assertContains(t, releaseMetadata, `"dirty": true`)
	assertContains(t, releaseMetadata, `"base_commit": "`+baseCommit+`"`)
	textStatus := e.simpleVPS(t, app, nil, "status", "--env", "production")
	assertContains(t, textStatus, "(dirty")
}

func (e *smokeEnv) testBackupRestore(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	e.dockerExec(t, "printf 'durable-state' > "+identity.DataDir("api", "production")+"/data.txt")

	rawCreate := e.simpleVPS(t, app, nil, "backup", "create", "--json", "--env", "production")
	var created struct {
		Backup struct {
			ID      string `json:"id"`
			Release string `json:"release"`
		} `json:"backup"`
	}
	if err := json.Unmarshal([]byte(rawCreate), &created); err != nil {
		t.Fatalf("backup create --json output not parseable as JSON: %v\nraw:\n%s", err, rawCreate)
	}
	if created.Backup.ID == "" {
		t.Fatalf("expected created backup id, got %+v", created.Backup)
	}
	rawList := e.simpleVPS(t, app, nil, "backup", "list", "--json", "--env", "production")
	var list struct {
		Backups []struct {
			ID      string `json:"id"`
			Release string `json:"release"`
		} `json:"backups"`
	}
	if err := json.Unmarshal([]byte(rawList), &list); err != nil {
		t.Fatalf("backup list --json output not parseable as JSON: %v\nraw:\n%s", err, rawList)
	}
	if len(list.Backups) == 0 || list.Backups[0].ID == "" {
		t.Fatalf("expected at least one backup, got %+v", list.Backups)
	}
	backupID := created.Backup.ID
	backupRelease := created.Backup.Release

	mustWrite(t, filepath.Join(app, "README.md"), "post-backup release\n")
	e.commitFixture(t, app)
	newRelease := gitRelease(t, e, app)
	if newRelease == backupRelease {
		t.Fatal("expected fixture commit to produce a new release")
	}
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	newContainer := identity.ContainerName("api", "production", "web", newRelease)
	e.dockerExec(t, "test -f /run/fake-podman/containers/"+newContainer+".labels")

	e.simpleVPS(t, app, nil, "restore", "--from", backupID, "--env", "production")
	e.dockerExec(t, "test ! -f /run/fake-podman/containers/"+newContainer+".labels")
	statusJSON := e.simpleVPS(t, app, nil, "status", "--json", "--env", "production")
	assertContains(t, statusJSON, `"release": "`+backupRelease+`"`)

	e.simpleVPS(t, app, nil, "destroy", "--env", "production", "--yes")
	if exists := e.run(t, e.repoRoot, nil, "docker", "exec", e.container, "bash", "-c", "test -e "+identity.DataDir("api", "production")+"/data.txt"); exists.err == nil {
		t.Fatal("destroy should remove data before restore")
	}

	e.simpleVPS(t, app, nil, "restore", "--from", backupID, "--env", "production")
	got := strings.TrimSpace(e.dockerExec(t, "cat "+identity.DataDir("api", "production")+"/data.txt"))
	if got != "durable-state" {
		t.Fatalf("restored data = %q, want durable-state", got)
	}
	envRootStat := strings.TrimSpace(e.dockerExec(t, "stat -c '%a %U' "+identity.EnvRoot("api", "production")))
	if envRootStat != "755 root" {
		t.Fatalf("restored env root ownership = %q, want `755 root`", envRootStat)
	}
	dataStat := strings.TrimSpace(e.dockerExec(t, "stat -c '%a %U' "+identity.DataDir("api", "production")))
	if dataStat != "2775 "+identity.SystemUser("api", "production") {
		t.Fatalf("restored data ownership = %q, want `2775 %s`", dataStat, identity.SystemUser("api", "production"))
	}
	status := e.simpleVPS(t, app, nil, "status", "--env", "production")
	assertContains(t, status, "web")
	assertContains(t, status, "running")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: api.example.com' http://127.0.0.1/health", "ok")
}

func (e *smokeEnv) testRollback(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")
	oldRelease := strings.TrimSpace(e.mustRun(t, app, nil, "git", "rev-parse", "--short=12", "HEAD"))

	manifestPath := filepath.Join(app, "simple-vps.toml")
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
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	newFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("api", "production"))
	assertContains(t, newFragment, ":"+"3333")

	rollbackText := e.simpleVPS(t, app, nil, "rollback", "--env", "production")
	assertContains(t, rollbackText, "Rolled back api (production) from "+newRelease+" to "+oldRelease)
	assertContains(t, rollbackText, "web")

	statusJSON := e.simpleVPS(t, app, nil, "status", "--json", "--env", "production")
	var status struct {
		Processes []struct {
			Process string `json:"process"`
			Release string `json:"release"`
		} `json:"processes"`
	}
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("status after rollback not parseable as JSON: %v\nraw:\n%s", err, statusJSON)
	}
	if len(status.Processes) != 1 || status.Processes[0].Process != "web" || status.Processes[0].Release != oldRelease {
		t.Fatalf("status did not report rolled-back release %s: %+v", oldRelease, status.Processes)
	}
	rolledBackFragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("api", "production"))
	assertContains(t, rolledBackFragment, identity.ContainerName("api", "production", "web", oldRelease)+":3000")
	if strings.Contains(rolledBackFragment, ":3333") {
		t.Fatalf("rollback should restore the old manifest route shape, got:\n%s", rolledBackFragment)
	}
	appliedManifest := e.ssh(t, "cat "+identity.ManifestFile("api", "production"))
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
			results <- e.runSimpleVPS(t, app, nil, "deploy", "--env", "production")
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent deploy failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
		}
		assertContains(t, result.stdout, "Deployed api (production)")
	}

	status := e.simpleVPS(t, app, nil, "status", "--env", "production")
	assertContains(t, status, "web")
	assertContains(t, status, "running")
}

func (e *smokeEnv) testRemovedProcessReconciliation(t *testing.T) {
	app := filepath.Join(e.tmp, "prune-api")
	mustMkdir(t, app)
	writePruneFixture(t, app)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil, "setup", "--env", "production")
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	oldRelease := gitRelease(t, e, app)
	oldWorker := identity.ContainerName("prune", "production", "worker", oldRelease)
	e.dockerExec(t, "test -f /run/fake-podman/containers/"+oldWorker+".labels")

	manifestPath := filepath.Join(app, "simple-vps.toml")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	nextManifest := strings.Replace(string(manifestBytes), `
[processes.worker]
command = "sleep 3600"
`, "", 1)
	if nextManifest == string(manifestBytes) {
		t.Fatal("test fixture did not contain worker process")
	}
	mustWrite(t, manifestPath, nextManifest)
	mustWrite(t, filepath.Join(app, "README.md"), "worker removed\n")
	e.commitFixture(t, app)
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")

	e.dockerExec(t, "test ! -f /run/fake-podman/containers/"+oldWorker+".labels")
	statusJSON := e.simpleVPS(t, app, nil, "status", "--json", "--env", "production")
	if strings.Contains(statusJSON, `"process": "worker"`) {
		t.Fatalf("removed worker still appears in status:\n%s", statusJSON)
	}
}

func (e *smokeEnv) testStaticOnlyAppLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "static-site")
	mustMkdir(t, app)
	writeStaticFixture(t, app)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil, "setup", "--env", "production")
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	oldRelease := currentStaticReleaseFor(t, e, "site", "production")
	staticReleaseManifest := e.ssh(t, "cat "+identity.ReleaseManifestFile("site", "production", oldRelease))
	assertContains(t, staticReleaseManifest, `serve = "dist"`)

	status := e.simpleVPS(t, app, nil, "status", "--env", "production")
	assertContains(t, status, "site (production)")
	assertContains(t, status, "static")
	assertContains(t, status, "release="+oldRelease)

	rawListJSON := e.simpleVPS(t, app, nil, "app", "list", "--server", "fake-vps", "--json")
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
		if listed.App == "site" && listed.Env == "production" && len(listed.Processes) == 0 {
			foundStatic = true
		}
	}
	if !foundStatic {
		t.Fatalf("app list --json missing static-only site env:\n%+v", listPayload.Apps)
	}

	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-ok")

	mustWrite(t, filepath.Join(app, "dist", "index.html"), "static-v2")
	e.commitFixture(t, app)
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	newRelease := currentStaticReleaseFor(t, e, "site", "production")
	if newRelease == oldRelease {
		t.Fatal("expected static fixture deploy to produce a new release")
	}
	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-v2")

	rawRollback := e.simpleVPS(t, app, nil, "rollback", "--env", "production")
	assertContains(t, rawRollback, "Rolled back site (production) from "+newRelease+" to "+oldRelease)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-ok")

	e.simpleVPS(t, app, nil, "backup", "create", "--env", "production")
	rawBackups := e.simpleVPS(t, app, nil, "backup", "list", "--json", "--env", "production")
	var backups struct {
		Backups []struct {
			ID string `json:"id"`
		} `json:"backups"`
	}
	if err := json.Unmarshal([]byte(rawBackups), &backups); err != nil {
		t.Fatalf("backup list --json output not parseable as JSON: %v\nraw:\n%s", err, rawBackups)
	}
	if len(backups.Backups) == 0 || backups.Backups[0].ID == "" {
		t.Fatalf("expected static backup, got %+v", backups.Backups)
	}
	backupID := backups.Backups[0].ID

	e.simpleVPS(t, app, nil, "destroy", "--env", "production", "--confirm", "site")
	e.simpleVPS(t, app, nil, "restore", "--from", backupID, "--env", "production")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: static.example.com' http://127.0.0.1/", "static-ok")
}

func (e *smokeEnv) testMixedContainerStaticLifecycle(t *testing.T) {
	app := filepath.Join(e.tmp, "mixed-api")
	mustMkdir(t, app)
	writeMixedFixture(t, app)
	e.commitFixture(t, app)

	e.simpleVPS(t, app, nil, "setup", "--env", "production")
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	oldRelease := currentStaticReleaseFor(t, e, "mix", "production")
	oldWeb := identity.ContainerName("mix", "production", "web", oldRelease)
	fragment := e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", "production"))
	assertContains(t, fragment, "reverse_proxy http://"+oldWeb+":3000")
	assertContains(t, fragment, `root * "`+filepath.Join(identity.StaticDir("mix", "production"), "releases", oldRelease, "docs")+`"`)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/health", "ok")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v1")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs/", "docs-v1")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs-v2", "ok")

	mustWrite(t, filepath.Join(app, "docs-dist", "index.html"), "docs-v2")
	mustWrite(t, filepath.Join(app, "README.md"), "docs v2\n")
	e.commitFixture(t, app)
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	newRelease := currentStaticReleaseFor(t, e, "mix", "production")
	if newRelease == oldRelease {
		t.Fatal("expected mixed fixture deploy to produce a new release")
	}
	newWeb := identity.ContainerName("mix", "production", "web", newRelease)
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", "production"))
	assertContains(t, fragment, "reverse_proxy http://"+newWeb+":3000")
	assertContains(t, fragment, `root * "`+filepath.Join(identity.StaticDir("mix", "production"), "releases", newRelease, "docs")+`"`)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/health", "ok")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v2")

	e.simpleVPS(t, app, nil, "rollback", "--env", "production")
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", "production"))
	assertContains(t, fragment, "reverse_proxy http://"+oldWeb+":3000")
	assertContains(t, fragment, `root * "`+filepath.Join(identity.StaticDir("mix", "production"), "releases", oldRelease, "docs")+`"`)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v1")

	e.simpleVPS(t, app, nil, "backup", "create", "--env", "production")
	rawBackups := e.simpleVPS(t, app, nil, "backup", "list", "--json", "--env", "production")
	var backups struct {
		Backups []struct {
			ID string `json:"id"`
		} `json:"backups"`
	}
	if err := json.Unmarshal([]byte(rawBackups), &backups); err != nil {
		t.Fatalf("mixed backup list --json output not parseable as JSON: %v\nraw:\n%s", err, rawBackups)
	}
	if len(backups.Backups) == 0 || backups.Backups[0].ID == "" {
		t.Fatalf("expected mixed backup, got %+v", backups.Backups)
	}
	backupID := backups.Backups[0].ID
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v2")
	e.simpleVPS(t, app, nil, "restore", "--from", backupID, "--env", "production")
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", "production"))
	assertContains(t, fragment, "reverse_proxy http://"+oldWeb+":3000")
	assertContains(t, fragment, `root * "`+filepath.Join(identity.StaticDir("mix", "production"), "releases", oldRelease, "docs")+`"`)
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v1")

	e.simpleVPS(t, app, nil, "destroy", "--env", "production", "--confirm", "mix")
	e.simpleVPS(t, app, nil, "restore", "--from", backupID, "--env", "production")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/health", "ok")
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "docs-v1")

	manifestPath := filepath.Join(app, "simple-vps.toml")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	nextManifest := strings.Replace(string(manifestBytes), `
[routes.docs]
host = "mixed.example.com"
path = "/docs"
serve = "docs-dist"
`, "", 1)
	if nextManifest == string(manifestBytes) {
		t.Fatal("test fixture did not contain docs route")
	}
	mustWrite(t, manifestPath, nextManifest)
	mustWrite(t, filepath.Join(app, "README.md"), "docs route removed\n")
	e.commitFixture(t, app)
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	fragment = e.ssh(t, "cat "+identity.CaddyFragmentFile("mix", "production"))
	if strings.Contains(fragment, "docs-dist") || strings.Contains(fragment, "/docs") {
		t.Fatalf("removed static route still appears in Caddy fragment:\n%s", fragment)
	}
	e.assertRemoteBody(t, "curl -fsS -H 'Host: mixed.example.com' http://127.0.0.1/docs", "ok")
}

// testSecretLifecycle covers the @secret:KEY resolution path end-to-end
// against the helper-side store under /etc/simple-vps/secrets/:
//
//  1. setup the app/env baseline
//  2. `simple-vps secret set` over SSH-stdin (value never on argv)
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

[vars]
LOG_LEVEL = "info"
DATABASE_URL = "@secret:db_url"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "sec.example.com"
process = "web"
`)
	e.commitFixture(t, app)
	e.simpleVPS(t, app, nil, "setup", "--env", "production")

	// 1. set: value over stdin, never argv. The fake-VPS harness uses
	// docker exec ssh under the hood and the helper reads from its
	// own stdin in `secret set`.
	e.simpleVPS(t, app, []byte("postgres://verybadidea"), "secret", "set", "db_url", "--env", "production")

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
	listing = e.simpleVPS(t, app, nil, "secret", "list", "--env", "production")
	if !strings.Contains(listing, "db_url") {
		t.Fatalf("secret list missing db_url:\n%s", listing)
	}
	if strings.Contains(listing, "postgres://") {
		t.Fatalf("secret list leaked the value:\n%s", listing)
	}
	rawSecretJSON := e.simpleVPS(t, app, nil, "secret", "list", "--json", "--env", "production")
	var secretPayload struct {
		App  string   `json:"app"`
		Env  string   `json:"env"`
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(rawSecretJSON), &secretPayload); err != nil {
		t.Fatalf("secret list --json output not parseable as JSON: %v\nraw:\n%s", err, rawSecretJSON)
	}
	if secretPayload.App != "sec" || secretPayload.Env != "production" || len(secretPayload.Keys) != 1 || secretPayload.Keys[0] != "db_url" {
		t.Fatalf("unexpected secret list --json payload: %+v", secretPayload)
	}
	if strings.Contains(rawSecretJSON, "postgres://") {
		t.Fatalf("secret list --json leaked the value:\n%s", rawSecretJSON)
	}

	// 4. deploy: helper resolves @secret:db_url into the env file
	// next to the literal LOG_LEVEL.
	e.simpleVPS(t, app, nil, "deploy", "--env", "production")
	envFile := e.dockerExec(t, "cat "+identity.EnvFile("sec", "production"))
	if !strings.Contains(envFile, "LOG_LEVEL=info\n") {
		t.Fatalf("env file missing literal LOG_LEVEL:\n%s", envFile)
	}
	if !strings.Contains(envFile, "DATABASE_URL=postgres://verybadidea\n") {
		t.Fatalf("env file missing resolved DATABASE_URL:\n%s", envFile)
	}

	// 5. env file mode + ownership: 0600 owned by the per-env user.
	envStat := strings.TrimSpace(e.dockerExec(t, "stat -c '%a %U' "+identity.EnvFile("sec", "production")))
	wantOwner := identity.SystemUser("sec", "production")
	if envStat != "600 "+wantOwner {
		t.Fatalf("env file perms wrong: %q (want `600 %s`)", envStat, wantOwner)
	}

	// 6. rm removes the key.
	e.simpleVPS(t, app, nil, "secret", "rm", "db_url", "--env", "production")
	if missing := strings.TrimSpace(e.dockerExec(t, "ls /etc/simple-vps/secrets/sec/production")); missing != "" {
		t.Fatalf("expected empty secret dir after rm, got:\n%s", missing)
	}

	// 7. next deploy with the ref still in the manifest fails fast.
	result := e.runSimpleVPS(t, app, nil, "deploy", "--env", "production")
	if result.err == nil {
		t.Fatal("expected deploy to fail with unresolved @secret reference")
	}
	if !strings.Contains(result.stderr+result.stdout, "missing secret db_url") ||
		!strings.Contains(result.stderr+result.stdout, "simple-vps secret set db_url --env production") {
		t.Fatalf("preflight error must name the missing secret and set command, got:\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
	}
}

// testStatusAndLogs covers the read-only operator surface introduced
// in PR #38. Assumes the earlier subtests have already deployed the
// `api` container app and left its `web` process running.
func (e *smokeEnv) testStatusAndLogs(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")

	// Text status surfaces the web process, its container, and the
	// release label baked in by `app apply`.
	text := e.simpleVPS(t, app, nil, "status", "--env", "production")
	assertContains(t, text, "api (production)")
	assertContains(t, text, "web")
	assertContains(t, text, "running")
	assertContains(t, text, "release=")
	if strings.Contains(text, "no processes running") {
		t.Fatalf("status reported `no processes running` after a successful deploy:\n%s", text)
	}

	// JSON status carries the same data in a structured shape.
	// Parse it back to prove the contract — text-mode regressions
	// might still slip through a substring check.
	rawJSON := e.simpleVPS(t, app, nil, "status", "--json", "--env", "production")
	var payload struct {
		App       string `json:"app"`
		Env       string `json:"env"`
		Processes []struct {
			Process   string `json:"process"`
			Container string `json:"container"`
			State     string `json:"state"`
			Release   string `json:"release"`
		} `json:"processes"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		t.Fatalf("status --json output not parseable as JSON: %v\nraw:\n%s", err, rawJSON)
	}
	if payload.App != "api" || payload.Env != "production" {
		t.Fatalf("status --json mis-identifies the app: %+v", payload)
	}
	if len(payload.Processes) != 1 || payload.Processes[0].Process != "web" {
		t.Fatalf("expected one process `web`, got: %+v", payload.Processes)
	}
	if payload.Processes[0].Container == "" || !strings.Contains(payload.Processes[0].Container, "-web-") {
		t.Fatalf("unexpected container name: %+v", payload.Processes[0])
	}
	if payload.Processes[0].Release == "" {
		t.Fatalf("status --json missing release label: %+v", payload.Processes[0])
	}

	// Host-level app listing is sourced from Podman labels instead
	// of the removed apps.json/routes.json registries.
	rawListJSON := e.simpleVPS(t, app, nil, "app", "list", "--server", "fake-vps", "--json")
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
		if listed.App == "api" && listed.Env == "production" && len(listed.Processes) == 1 && listed.Processes[0].Process == "web" {
			found = true
		}
	}
	if !found {
		t.Fatalf("app list --json missing api/production/web process:\n%+v", listPayload.Apps)
	}

	// Logs reaches `podman logs` on the right container and prints
	// the deterministic stub line.
	logs := e.simpleVPS(t, app, nil, "logs", "web", "--env", "production")
	assertContains(t, logs, "fake podman logs for "+payload.Processes[0].Container)

	// Process argument is optional when exactly one process exists.
	logsNoSvc := e.simpleVPS(t, app, nil, "logs", "--env", "production")
	assertContains(t, logsNoSvc, "fake podman logs for "+payload.Processes[0].Container)

	// Unknown process errors clearly.
	missing := e.runSimpleVPS(t, app, nil, "logs", "nope", "--env", "production")
	if missing.err == nil {
		t.Fatal("expected logs to fail when process is unknown")
	}
	if !strings.Contains(missing.stderr, "nope") {
		t.Fatalf("error should name the missing process, got: %s", missing.stderr)
	}
}

// testRestart covers `simple-vps restart` against the live container
// flow. Assumes the earlier `testContainerAppLifecycle` subtest has
// already deployed the `api` container app and left its `web`
// process running on the fake VPS.
func (e *smokeEnv) testRestart(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")

	// Whole-env restart text output names the env and the bounced
	// process, with the post-restart state.
	text := e.simpleVPS(t, app, nil, "restart", "--env", "production")
	assertContains(t, text, "api (production)")
	assertContains(t, text, "web")
	assertContains(t, text, "restarted (running)")

	// fake-podman should have logged the actual `podman restart`. This
	// is the assertion that proves the helper used the in-place
	// primitive (not an `rm`/`run` cycle that would force a re-build
	// or lose container state).
	currentContainer := currentWebContainer(t, e, app)
	commandsLog := e.ssh(t, "cat /run/fake-podman/commands.log")
	assertContains(t, commandsLog, "podman restart "+currentContainer)

	// The end-to-end Caddy route should still work after the bounce —
	// the container kept its labels, network membership, and listener
	// port; Caddy didn't need a reload.
	e.assertRemoteBody(t, "curl -fsS -H 'Host: api.example.com' http://127.0.0.1/health", "ok")

	// Process-scoped restart hits just the named process.
	svcOut := e.simpleVPS(t, app, nil, "restart", "web", "--env", "production")
	assertContains(t, svcOut, "web")
	assertContains(t, svcOut, "restarted (running)")

	// Unknown process errors with a clear message that names the
	// missing one.
	missing := e.runSimpleVPS(t, app, nil, "restart", "nope", "--env", "production")
	if missing.err == nil {
		t.Fatal("expected restart to fail for unknown process")
	}
	if !strings.Contains(missing.stderr+missing.stdout, "nope") {
		t.Fatalf("error should name the missing process, got:\nstdout: %s\nstderr: %s", missing.stdout, missing.stderr)
	}
}

// testDestroy covers the public `simple-vps destroy` wrapper and the
// privileged `server app destroy-env` teardown path. It intentionally
// runs after status/logs/restart because it removes the
// container-api fixture from the fake VPS.
func (e *smokeEnv) testDestroy(t *testing.T) {
	app := filepath.Join(e.tmp, "container-api")

	// Client safety gate: no accidental teardown without either
	// --confirm <app> or --yes.
	missingConfirm := e.runSimpleVPS(t, app, nil, "destroy", "--env", "production")
	if missingConfirm.err == nil {
		t.Fatal("expected destroy without confirmation to fail")
	}
	if !strings.Contains(missingConfirm.stderr+missingConfirm.stdout, "--confirm api") {
		t.Fatalf("confirmation error should name the app, got:\nstdout: %s\nstderr: %s", missingConfirm.stdout, missingConfirm.stderr)
	}

	// Give --purge something observable to remove.
	e.simpleVPS(t, app, []byte("throwaway"), "secret", "set", "cleanup_key", "--env", "production")
	currentContainer := currentWebContainer(t, e, app)

	out := e.simpleVPS(t, app, nil, "destroy", "--env", "production", "--confirm", "api", "--purge")
	assertContains(t, out, "Destroyed api (production)")
	assertContains(t, out, "containers: 1 removed")
	assertContains(t, out, "route: removed")
	assertContains(t, out, "secrets: purged")

	commandsLog := e.ssh(t, "cat /run/fake-podman/commands.log")
	assertContains(t, commandsLog, "podman rm -f "+currentContainer)
	assertContains(t, commandsLog, "podman network rm "+identity.Network("api", "production"))
	assertContains(t, commandsLog, "podman exec caddy caddy reload --config /etc/caddy/Caddyfile")

	e.dockerExec(t, "test ! -e /run/fake-podman/containers/"+currentContainer+".labels")
	e.dockerExec(t, "test ! -e /run/fake-podman/networks/"+identity.Network("api", "production"))
	e.dockerExec(t, "test ! -e "+identity.CaddyFragmentFile("api", "production"))
	e.dockerExec(t, "test ! -e "+identity.EnvRoot("api", "production"))
	e.dockerExec(t, "test ! -e /etc/simple-vps/secrets/api/production")
	e.dockerExec(t, "! getent passwd "+identity.SystemUser("api", "production")+" >/dev/null")

	status := e.simpleVPS(t, app, nil, "status", "--env", "production")
	assertContains(t, status, "no processes running")

	// Fake Caddy re-reads conf.d on every request, so route removal is
	// visible immediately after the reload.
	e.ssh(t, "if curl -fsS -H 'Host: api.example.com' http://127.0.0.1/health; then exit 1; fi")

	// Idempotence: a second destroy should be a no-op, not an error.
	again := e.simpleVPS(t, app, nil, "destroy", "--env", "production", "--yes")
	assertContains(t, again, "containers: none")
	assertContains(t, again, "route: none")
}

func writeContainerFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "api"

[env.production]
server = "fake-vps"

[processes.web]
port = 3000
health = "/health"
resources = { memory = "512m", cpus = 0.5 }

[routes.app]
host = "api.example.com"
process = "web"
	`)
}

func writeDirtyFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "dirtyapi"

[env.production]
server = "fake-vps"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "dirty.example.com"
process = "web"
`)
}

func writeReleaseFailFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "releasefail"

[env.production]
server = "fake-vps"

[vars]
MARKER = "stable"

[deploy]
release = "touch /data/release-ok"

[processes.web]
port = 3000
health = "/health"

[routes.app]
host = "release-fail.example.com"
process = "web"
`)
}

func writePruneFixture(t *testing.T, app string) {
	t.Helper()
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "prune"

[env.production]
server = "fake-vps"

[processes.web]
port = 3000
health = "/health"

[processes.worker]
command = "sleep 3600"

[routes.app]
host = "prune.example.com"
process = "web"
`)
}

func writeStaticFixture(t *testing.T, app string) {
	t.Helper()
	mustMkdir(t, filepath.Join(app, "dist"))
	mustWrite(t, filepath.Join(app, "dist", "index.html"), "static-ok")
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "site"

[env.production]
server = "fake-vps"

[routes.home]
host = "static.example.com"
serve = "dist"
`)
}

func writeMixedFixture(t *testing.T, app string) {
	t.Helper()
	mustMkdir(t, filepath.Join(app, "docs-dist"))
	mustWrite(t, filepath.Join(app, "docs-dist", "index.html"), "docs-v1")
	mustWrite(t, filepath.Join(app, "Dockerfile"), `FROM alpine
CMD ["/bin/sh", "-c", "sleep 3600"]
`)
	mustWrite(t, filepath.Join(app, "simple-vps.toml"), `name = "mix"

[env.production]
server = "fake-vps"

[processes.web]
port = 3000
health = "/health"

[routes.docs]
host = "mixed.example.com"
path = "/docs"
serve = "docs-dist"

[routes.app]
host = "mixed.example.com"
process = "web"
`)
}

func gitRelease(t *testing.T, e *smokeEnv, app string) string {
	t.Helper()
	return strings.TrimSpace(e.mustRun(t, app, nil, "git", "rev-parse", "--short=12", "HEAD"))
}

func currentStaticReleaseFor(t *testing.T, e *smokeEnv, app, env string) string {
	t.Helper()
	return strings.TrimSpace(e.ssh(t, "basename $(readlink "+identity.StaticDir(app, env)+"/current)"))
}

func currentWebContainer(t *testing.T, e *smokeEnv, app string) string {
	t.Helper()
	rawJSON := e.simpleVPS(t, app, nil, "status", "--json", "--env", "production")
	var payload struct {
		Processes []struct {
			Process   string `json:"process"`
			Container string `json:"container"`
		} `json:"processes"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		t.Fatalf("status --json output not parseable as JSON: %v\nraw:\n%s", err, rawJSON)
	}
	for _, proc := range payload.Processes {
		if proc.Process == "web" {
			return proc.Container
		}
	}
	t.Fatalf("status --json missing web process:\n%s", rawJSON)
	return ""
}

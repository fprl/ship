package fakevps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	h "github.com/fprl/ship/tests/harness"
)

const freshHostImage = "ship-fresh-host:local"

func TestFreshHostInstall(t *testing.T) {
	if os.Getenv("SHIP_RUN_FAKE_VPS_SMOKE") != "1" {
		t.Skip("set SHIP_RUN_FAKE_VPS_SMOKE=1 to run Docker-backed fake VPS smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)

	env := newSmokeEnvWithImage(t, ctx, freshHostImage, filepath.Join(h.RepoRootForTest(t), "tests/fake-vps/Dockerfile.install"))
	env.buildBinaries(t)
	env.buildImage(t)
	env.startContainer(t)
	env.configureSSH(t, "root")
	env.waitForSSH(t)

	env.assertHostDoctorNotInstalled(t)

	decoyKeyPath := filepath.Join(env.tmp, "decoy-bootstrap")
	env.mustRun(t, env.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "decoy-bootstrap", "-f", decoyKeyPath)
	decoyKey := strings.TrimSpace(readFile(t, decoyKeyPath+".pub"))
	env.ssh(t, "printf %s "+h.ShellQuote(decoyKey+"\n")+" >> /root/.ssh/authorized_keys")

	first, firstOutput := env.installHost(t, "")
	if first <= 0 {
		t.Fatalf("first install changed %d operations, want > 0", first)
	}
	assertContainsInOrder(t, firstOutput,
		"identity: fake-vps-smoke (created ~/.ssh/ship)",
		"connected as root (bootstrap)",
		"ingress: public 80/443 (--ingress ...)",
		"admin: SSH keys only (--admin tailscale)",
		"ship installer starting",
		"Running in remote mode against root@fake-vps",
		"Provisioning complete",
		"member added: fake-vps-smoke (SHA256:",
	)

	env.assertFreshHostInstalled(t)
	identityKey := env.shipIdentityPublicKey(t)
	env.assertDeployAuthorizedKeys(t, identityKey)
	env.assertOperatorAuthorizedKeys(t, identityKey)
	env.assertDeployAuthorizedKeysDoesNotContain(t, decoyKey)
	env.assertSetupMemberVisible(t)
	env.assertHostDoctorHealthy(t)

	env.ssh(t, "mkdir -p /var/apps/api.production/data && printf sentinel > /var/apps/api.production/data/sentinel && chmod 600 /var/apps/api.production/data/sentinel")
	second, _ := env.installHost(t, "")
	if second != 0 {
		t.Fatalf("second install changed %d operations, want 0", second)
	}
	assertEqual(t, strings.TrimSpace(env.ssh(t, "cat /var/apps/api.production/data/sentinel")), "sentinel")
	assertEqual(t, strings.TrimSpace(env.ssh(t, "stat -c '%a' /var/apps/api.production/data/sentinel")), "600")

	explicitKeyPath := filepath.Join(env.tmp, "explicit-deploy")
	env.mustRun(t, env.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "explicit-deploy", "-f", explicitKeyPath)
	_, _ = env.installHost(t, explicitKeyPath+".pub")
	explicitKey := strings.TrimSpace(readFile(t, explicitKeyPath+".pub"))
	env.assertDeployAuthorizedKeys(t, explicitKey)
	env.assertOperatorAuthorizedKeys(t, identityKey)

	env.assertDoctorRecordingDeltaForStoppedReaper(t)
}

func TestFreshHostInstallEmptyAuthorizedKeysNoFlag(t *testing.T) {
	if os.Getenv("SHIP_RUN_FAKE_VPS_SMOKE") != "1" {
		t.Skip("set SHIP_RUN_FAKE_VPS_SMOKE=1 to run Docker-backed fake VPS smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)

	env := newSmokeEnvWithImage(t, ctx, freshHostImage, filepath.Join(h.RepoRootForTest(t), "tests/fake-vps/Dockerfile.install"))
	env.buildBinaries(t)
	env.buildImage(t)
	env.startContainer(t)
	env.configureSSH(t, "root")
	env.allowSSHWithEmptyRootAuthorizedKeys(t)
	env.waitForSSH(t)

	result := env.runShip(t, env.repoRoot, nil,
		"box", "setup", "fake-vps",
		"--mode", "remote",
		"--bootstrap-user", "root",
		"--timezone", "Europe/Madrid",
		"--locale", "en_US.UTF-8",
		"--no-tailscale",
		"--no-cloudflare-tunnel",
		"--no-litestream",
	)
	if result.err == nil {
		t.Fatalf("ship box setup should fail with empty bootstrap authorized_keys\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	output := result.stdout + result.stderr
	assertContains(t, output, "bootstrap SSH key is missing")
	assertContains(t, output, "provider gave a password")
	assertContains(t, output, "next: ssh-copy-id -i ~/.ssh/ship.pub root@fake-vps")
	assertContains(t, output, "identity: fake-vps-smoke (created ~/.ssh/ship)")
	assertNotContains(t, output, "member added:")
	if _, err := os.Stat(filepath.Join(env.shipHome, ".ssh", "ship.pub")); err != nil {
		t.Fatalf("ship identity should exist before password remediation: %v", err)
	}
}

func (e *smokeEnv) installHost(t *testing.T, deployPublicKeyFile string) (int, string) {
	t.Helper()
	args := []string{
		"box", "setup", "root@fake-vps",
		"--mode", "remote",
		"--timezone", "Europe/Madrid",
		"--locale", "en_US.UTF-8",
		"--no-tailscale",
		"--no-cloudflare-tunnel",
		"--no-litestream",
	}
	if deployPublicKeyFile != "" {
		args = append(args, "--deploy-ssh-public-key-file", deployPublicKeyFile)
	}
	result := e.runShip(t, e.repoRoot, nil, args...)
	// Always log the install output. When a downstream assertion fails
	// (e.g., last_apply.status != "ok") the install transcript is the
	// only place that names the failing op; `t.Logf` is printed when
	// the test fails and is invisible on a green run.
	t.Logf("install stdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	if result.err != nil {
		t.Fatalf("ship box setup failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	assertContains(t, result.stdout, "Provisioning complete")
	// Read operations-changed from /etc/ship/host.json on the
	// VPS. The trailing `Apply ... changed N operations` stdout line
	// can be dropped by SSH pipe-close races on some Linux/Docker
	// runners; host.json is the authoritative record (ADR-0002 §2).
	return changedOperationsFromHostState(t, e), result.stdout
}

func (e *smokeEnv) assertFreshHostInstalled(t *testing.T) {
	t.Helper()
	e.ssh(t, "test -x /usr/local/bin/ship")
	e.ssh(t, "getent passwd operator >/dev/null")
	e.ssh(t, "getent passwd deploy >/dev/null")
	assertContains(t, e.ssh(t, "id -nG operator"), "sudo")
	e.ssh(t, "grep -q 'operator ALL=(ALL) NOPASSWD:ALL' /etc/sudoers.d/operator")
	e.ssh(t, "grep -Fq 'deploy ALL=(root) NOPASSWD: /usr/local/bin/ship server app *, /usr/local/bin/ship server doctor, /usr/local/bin/ship server doctor *, /usr/local/bin/ship server key *' /etc/sudoers.d/ship")
	e.ssh(t, "grep -q 'fake-vps-smoke' /home/operator/.ssh/authorized_keys")
	e.ssh(t, "grep -q 'fake-vps-smoke' /home/deploy/.ssh/authorized_keys")
	e.ssh(t, "test -d /etc/ship/backups && test -d /etc/ship/providers && test -d /etc/ship/secrets")
	assertEqual(t, strings.TrimSpace(e.ssh(t, "stat -c '%a' /etc/ship/secrets")), "700")
	assertEqual(t, strings.TrimSpace(e.ssh(t, "stat -c '%a' /tmp/ship-deploy")), "1777")
	e.ssh(t, "grep -Fq '# BEGIN ship podman bridges' /etc/ufw/before.rules")
	e.ssh(t, "test -f /etc/containers/registries.conf.d/00-ship.conf")
	e.ssh(t, "grep -Fq '# Managed by ship.' /etc/containers/registries.conf.d/00-ship.conf")
	e.ssh(t, "grep -Fq '# Managed by ship.' /etc/caddy/Caddyfile")

	hostState := e.ssh(t, "cat /etc/ship/host.json")
	var state struct {
		Version int `json:"version"`
		Desired struct {
			Users struct {
				Operator string `json:"operator"`
				Deploy   string `json:"deploy"`
			} `json:"users"`
			Ingress struct {
				Expose string `json:"expose"`
				Tunnel string `json:"tunnel"`
			} `json:"ingress"`
			Features struct {
				Docker     bool `json:"docker"`
				Litestream bool `json:"litestream"`
			} `json:"features"`
		} `json:"desired"`
		Meta struct {
			LastApply struct {
				Status            string `json:"status"`
				OperationsChanged int    `json:"operations_changed"`
			} `json:"last_apply"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(hostState), &state); err != nil {
		t.Fatalf("decode host.json: %v\n%s", err, hostState)
	}
	if state.Version != 1 {
		t.Fatalf("host.json version = %d, want 1", state.Version)
	}
	if state.Desired.Users.Operator != "operator" || state.Desired.Users.Deploy != "deploy" {
		t.Fatalf("unexpected desired users: %+v", state.Desired.Users)
	}
	if state.Desired.Ingress.Expose != "public" || state.Desired.Ingress.Tunnel != "none" {
		t.Fatalf("unexpected ingress: %+v", state.Desired.Ingress)
	}
	if state.Desired.Features.Docker || state.Desired.Features.Litestream {
		t.Fatalf("unexpected enabled features: %+v", state.Desired.Features)
	}
	if state.Meta.LastApply.Status != "ok" || state.Meta.LastApply.OperationsChanged <= 0 {
		t.Fatalf("unexpected last apply meta: %+v", state.Meta.LastApply)
	}

	assertContains(t, e.ssh(t, "cat /run/ship-fresh-host/systemctl.log"), "restart ssh.service")
	systemctlLog := e.ssh(t, "cat /run/ship-fresh-host/systemctl.log")
	assertContains(t, systemctlLog, "start fail2ban.service")
	assertContains(t, systemctlLog, "start caddy.service")
	assertContains(t, systemctlLog, "start ship-preview-reaper.timer")
	assertContains(t, systemctlLog, "enable ship-preview-reaper.timer")
	assertContains(t, systemctlLog, "start ship-doctor.timer")
	assertContains(t, systemctlLog, "enable ship-doctor.timer")
	e.ssh(t, "grep -Fq 'ExecStart=/usr/local/bin/ship server env reap' /etc/systemd/system/ship-preview-reaper.service")
	e.ssh(t, "grep -Fq 'OnUnitActiveSec=1h' /etc/systemd/system/ship-preview-reaper.timer")
	e.ssh(t, "grep -Fq 'ExecStart=/usr/local/bin/ship server doctor record' /etc/systemd/system/ship-doctor.service")
	e.ssh(t, "grep -Fq 'OnUnitActiveSec=24h' /etc/systemd/system/ship-doctor.timer")

	ufwLog := e.ssh(t, "cat /run/ship-fresh-host/ufw.log")
	for _, want := range []string{
		"default deny incoming",
		"default allow outgoing",
		"allow 22/tcp",
		"allow 41641/udp",
		"allow 80/tcp",
		"allow 443/tcp",
		"--force enable",
	} {
		assertContains(t, ufwLog, want)
	}
	assertEqual(t, strings.TrimSpace(e.ssh(t, "cat /run/ship-fresh-host/timezone")), "Europe/Madrid")
	assertEqual(t, strings.TrimSpace(e.ssh(t, "cat /run/ship-fresh-host/locale")), "en_US.UTF-8")
}

func (e *smokeEnv) assertDeployAuthorizedKeys(t *testing.T, want string) {
	t.Helper()
	got := strings.TrimSpace(e.ssh(t, "cat /home/deploy/.ssh/authorized_keys"))
	assertEqual(t, got, strings.TrimSpace(want))
}

func (e *smokeEnv) assertOperatorAuthorizedKeys(t *testing.T, want string) {
	t.Helper()
	got := strings.TrimSpace(e.ssh(t, "cat /home/operator/.ssh/authorized_keys"))
	assertEqual(t, got, strings.TrimSpace(want))
}

func (e *smokeEnv) assertDeployAuthorizedKeysDoesNotContain(t *testing.T, unwanted string) {
	t.Helper()
	got := strings.TrimSpace(e.ssh(t, "cat /home/deploy/.ssh/authorized_keys"))
	if strings.Contains(got, strings.TrimSpace(unwanted)) {
		t.Fatalf("deploy authorized_keys contains bootstrap decoy\nwanted absent:\n%s\nactual:\n%s", unwanted, got)
	}
}

func (e *smokeEnv) shipIdentityPublicKey(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(readFile(t, filepath.Join(e.shipHome, ".ssh", "ship.pub")))
}

func (e *smokeEnv) assertSetupMemberVisible(t *testing.T) {
	t.Helper()
	app := filepath.Join(e.tmp, "member-list")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), "FROM alpine\nCMD [\"sleep\", \"3600\"]\n")
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "memberlist"
box = "fake-vps"

[processes]
web = { port = 3000 }
`)
	list := e.ship(t, app, nil, "member", "ls")
	assertContains(t, list, "fake-vps-smoke ssh-ed25519 SHA256:")
}

func (e *smokeEnv) allowSSHWithEmptyRootAuthorizedKeys(t *testing.T) {
	t.Helper()
	e.dockerExec(t, "cp /root/.ssh/authorized_keys /root/.ssh/bootstrap_authorized_keys && : > /root/.ssh/authorized_keys && if grep -Eq '^#?AuthorizedKeysFile' /etc/ssh/sshd_config; then sed -ri 's|^#?AuthorizedKeysFile.*|AuthorizedKeysFile .ssh/bootstrap_authorized_keys .ssh/authorized_keys|' /etc/ssh/sshd_config; else printf '\\nAuthorizedKeysFile .ssh/bootstrap_authorized_keys .ssh/authorized_keys\\n' >> /etc/ssh/sshd_config; fi && pkill -HUP sshd")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (e *smokeEnv) assertHostDoctorNotInstalled(t *testing.T) {
	t.Helper()
	doctorEnv := []string{"HOME=" + filepath.Join(e.tmp, "preinstall-doctor-home")}
	result := e.runCommand(t, e.repoRoot, doctorEnv, nil, e.goBin, "box", "doctor", "fake-vps")
	if result.err == nil {
		t.Fatalf("box doctor passed before install\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	output := result.stdout + result.stderr
	assertContains(t, output, "failed to run doctor")
	assertContains(t, output, "host is not installed")

	jsonResult := e.runCommand(t, e.repoRoot, doctorEnv, nil, e.goBin, "box", "doctor", "fake-vps", "--json")
	if jsonResult.err == nil {
		t.Fatalf("box doctor --json passed before install\nstdout:\n%s\nstderr:\n%s", jsonResult.stdout, jsonResult.stderr)
	}
	var checks []doctorCheck
	if err := json.Unmarshal([]byte(jsonResult.stdout), &checks); err != nil {
		t.Fatalf("box doctor --json output not parseable as JSON: %v\nstdout:\n%s\nstderr:\n%s", err, jsonResult.stdout, jsonResult.stderr)
	}
	hostState := findDoctorCheck(t, checks, "host_state")
	if hostState.Status != "failed" || !strings.Contains(hostState.Evidence, "host is not installed") {
		t.Fatalf("unexpected degraded doctor payload: %+v", checks)
	}
}

func (e *smokeEnv) assertHostDoctorHealthy(t *testing.T) {
	t.Helper()
	output := e.ship(t, e.repoRoot, nil, "box", "doctor", "fake-vps")
	for _, want := range []string{
		"host_state ok -",
		"service_health ok -",
		"sudoers_identity ok -",
		"disk_space ok -",
		"tls_certs ok -",
		"reaper_timer ok -",
		"deploy_journals ok -",
	} {
		assertContains(t, output, want)
	}

	rawDoctorJSON := e.ship(t, e.repoRoot, nil, "box", "doctor", "fake-vps", "--json")
	var doctorPayload []doctorCheck
	if err := json.Unmarshal([]byte(rawDoctorJSON), &doctorPayload); err != nil {
		t.Fatalf("box doctor --json output not parseable as JSON: %v\nraw:\n%s", err, rawDoctorJSON)
	}
	for _, id := range []string{"host_state", "service_health", "sudoers_identity", "disk_space", "tls_certs", "reaper_timer", "deploy_journals"} {
		check := findDoctorCheck(t, doctorPayload, id)
		if check.Status != "ok" || check.Evidence == "" || check.Remediation == "" {
			t.Fatalf("unexpected healthy doctor check %s: %+v", id, check)
		}
	}
}

func (e *smokeEnv) assertDoctorRecordingDeltaForStoppedReaper(t *testing.T) {
	t.Helper()
	e.ssh(t, "/usr/local/bin/ship server doctor record")
	state := readDoctorState(t, e)
	if len(state.Delta) != 0 {
		t.Fatalf("healthy baseline doctor record should have empty delta: %+v", state.Delta)
	}

	e.ssh(t, "systemctl stop ship-preview-reaper.timer")
	result := e.runShip(t, e.repoRoot, nil, "box", "doctor", "fake-vps", "--json")
	if result.err == nil {
		t.Fatalf("box doctor should fail after stopping reaper timer\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	var checks []doctorCheck
	if err := json.Unmarshal([]byte(result.stdout), &checks); err != nil {
		t.Fatalf("doctor degraded JSON not parseable: %v\nstdout:\n%s\nstderr:\n%s", err, result.stdout, result.stderr)
	}
	reaper := findDoctorCheck(t, checks, "reaper_timer")
	if reaper.Status != "degraded" || !strings.Contains(reaper.Evidence, "active=inactive") {
		t.Fatalf("unexpected reaper degraded check: %+v", reaper)
	}

	e.ssh(t, "/usr/local/bin/ship server doctor record")
	state = readDoctorState(t, e)
	if len(state.Delta) != 1 || state.Delta[0].ID != "reaper_timer" || state.Delta[0].Status != "degraded" {
		t.Fatalf("expected newly degraded reaper delta, got: %+v", state.Delta)
	}
	if findDoctorCheck(t, state.Checks, "reaper_timer").Status != "degraded" {
		t.Fatalf("recorded checks did not reflect degraded reaper: %+v", state.Checks)
	}

	e.ssh(t, "/usr/local/bin/ship server doctor record")
	state = readDoctorState(t, e)
	if len(state.Delta) != 0 {
		t.Fatalf("second degraded doctor record should have empty delta: %+v", state.Delta)
	}
}

type doctorCheck struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
	Remediation string `json:"remediation"`
}

type doctorStateFile struct {
	Version    int           `json:"version"`
	RecordedAt string        `json:"recorded_at"`
	Checks     []doctorCheck `json:"checks"`
	Delta      []doctorCheck `json:"delta"`
}

func readDoctorState(t *testing.T, e *smokeEnv) doctorStateFile {
	t.Helper()
	raw := e.ssh(t, "cat /etc/ship/doctor.json")
	var state doctorStateFile
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("decode doctor.json: %v\n%s", err, raw)
	}
	if state.Version != 1 || state.RecordedAt == "" || state.Checks == nil || state.Delta == nil {
		t.Fatalf("unexpected doctor.json shape: %+v", state)
	}
	return state
}

func findDoctorCheck(t *testing.T, checks []doctorCheck, id string) doctorCheck {
	t.Helper()
	for _, check := range checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("doctor check %q not found in %+v", id, checks)
	return doctorCheck{}
}

// changedOperationsFromHostState reads /etc/ship/host.json from
// the fake VPS and returns meta.last_apply.operations_changed. Reading
// the file directly avoids the SSH pipe-close race that drops trailing
// inner-installer stdout on some Linux/Docker runners.
func changedOperationsFromHostState(t *testing.T, e *smokeEnv) int {
	t.Helper()
	raw := e.ssh(t, "cat /etc/ship/host.json")
	var state struct {
		Meta struct {
			LastApply struct {
				OperationsChanged int `json:"operations_changed"`
			} `json:"last_apply"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("decode host.json: %v\n%s", err, raw)
	}
	return state.Meta.LastApply.OperationsChanged
}

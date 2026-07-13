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
		"identity: fake-vps-smoke (~/.ssh/ship)",
		"connected as root (bootstrap)",
		"ingress: public 80/443",
		"admin: SSH keys only",
		"Using ship Linux helper binary",
		"running provisioner on target",
		"provisioning ",
		"member added: fake-vps-smoke (owner, SHA256:",
		"pinned box fake-vps (~/.config/ship/known_hosts)",
		"box ready",
		"next: ship box doctor fake-vps",
	)
	for _, notWant := range []string{
		"ship installer starting",
		"Operator user:",
		"Deploy user:",
		"Docker: false",
		"deploy@fake-vps",
	} {
		assertNotContains(t, firstOutput, notWant)
	}
	shipKnownHosts := readFile(t, filepath.Join(env.shipHome, ".config", "ship", "known_hosts"))
	assertContains(t, shipKnownHosts, "fake-vps ")
	if _, err := os.Stat(filepath.Join(env.shipHome, ".ssh", "known_hosts")); !os.IsNotExist(err) {
		t.Fatalf("box setup must not touch ~/.ssh/known_hosts, stat err=%v", err)
	}

	env.assertFreshHostInstalled(t)
	identityKey := env.shipIdentityPublicKey(t)
	env.assertDeployAuthorizedKeys(t, identityKey)
	env.assertOperatorAuthorizedKeys(t, identityKey)
	env.assertDeployAuthorizedKeysDoesNotContain(t, decoyKey)
	env.assertSetupMemberVisible(t)
	env.assertHostDoctorHealthy(t)
	env.assertWrongHostKeyRefuses(t)

	env.ssh(t, "mkdir -p /var/apps/api.production/data && printf sentinel > /var/apps/api.production/data/sentinel && chmod 600 /var/apps/api.production/data/sentinel")
	second, secondOutput := env.installHost(t, "")
	if second != 0 {
		t.Fatalf("second install changed %d operations, want 0", second)
	}
	assertContains(t, secondOutput, "member fake-vps-smoke already authorized")
	env.assertDeployAuthorizedKeys(t, identityKey)
	env.assertDeployAuthorizedKeysLineCount(t, identityKey, 1)
	assertEqual(t, strings.TrimSpace(env.ssh(t, "cat /var/apps/api.production/data/sentinel")), "sentinel")
	assertEqual(t, strings.TrimSpace(env.ssh(t, "stat -c '%a' /var/apps/api.production/data/sentinel")), "600")

	memberApp := env.memberListApp(t, "setup-preserve-members")
	teammateKeyPath := filepath.Join(env.tmp, "setup-preserved-member")
	teammateComment := filepath.Base(teammateKeyPath)
	env.mustRun(t, env.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", teammateComment, "-f", teammateKeyPath)
	addedTeammate := env.ship(t, memberApp, nil, "member", "add", teammateKeyPath+".pub")
	if !strings.HasPrefix(strings.TrimSpace(addedTeammate), "member added: "+teammateComment+" (shipper, SHA256:") {
		t.Fatalf("unexpected member add output: %q", addedTeammate)
	}
	teammateFingerprint := fingerprintFromMemberMutation(t, addedTeammate)
	teammateKey := strings.TrimSpace(readFile(t, teammateKeyPath+".pub"))
	_, preserveOutput := env.installHost(t, "")
	assertContains(t, preserveOutput, "member fake-vps-smoke already authorized")
	preservedMembers := env.ship(t, memberApp, nil, "member", "ls")
	assertContains(t, preservedMembers, "fake-vps-smoke owner ssh-ed25519 SHA256:")
	assertContains(t, preservedMembers, teammateComment+" shipper ssh-ed25519 "+teammateFingerprint)
	env.assertDeployAuthorizedKeysLineCount(t, identityKey, 1)
	env.assertDeployAuthorizedKeysLineCount(t, teammateKey, 1)

	explicitKeyPath := filepath.Join(env.tmp, "explicit-deploy")
	env.mustRun(t, env.repoRoot, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "explicit-deploy", "-f", explicitKeyPath)
	_, _ = env.installHost(t, explicitKeyPath+".pub")
	explicitKey := strings.TrimSpace(readFile(t, explicitKeyPath+".pub"))
	env.assertDeployAuthorizedKeysContains(t, identityKey)
	env.assertDeployAuthorizedKeysContains(t, teammateKey)
	env.assertDeployAuthorizedKeysContains(t, explicitKey)
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
	)
	if result.err == nil {
		t.Fatalf("ship box setup should fail with empty bootstrap authorized_keys\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	output := result.stdout + result.stderr
	assertContains(t, output, "bootstrap SSH key is missing")
	assertContains(t, output, "provider gave a password")
	assertContains(t, output, "next: ssh-copy-id -i ~/.ssh/ship.pub root@fake-vps")
	assertContains(t, output, "identity: fake-vps-smoke (~/.ssh/ship)")
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
	if result.stdout != "" {
		t.Fatalf("box setup narration should be on stderr, got stdout:\n%s", result.stdout)
	}
	assertContains(t, result.stderr, "box ready")
	return changedOperationsFromHostState(t, e), result.stderr
}

func (e *smokeEnv) assertFreshHostInstalled(t *testing.T) {
	t.Helper()
	e.ssh(t, "test -x /usr/local/bin/ship")
	e.ssh(t, "getent passwd operator >/dev/null")
	e.ssh(t, "getent passwd deploy >/dev/null")
	assertContains(t, e.ssh(t, "id -nG operator"), "sudo")
	e.ssh(t, "grep -q 'operator ALL=(ALL) NOPASSWD:ALL' /etc/sudoers.d/operator")
	e.ssh(t, "grep -Fq 'deploy ALL=(root) NOPASSWD: /usr/local/bin/ship server app *, /usr/local/bin/ship server doctor, /usr/local/bin/ship server doctor *, /usr/local/bin/ship server key *, /usr/local/bin/ship server approval *, /usr/local/bin/ship server config *, /usr/local/bin/ship server notify *, /usr/local/bin/ship server version, /usr/local/bin/ship server version *, /usr/local/bin/ship server update *' /etc/sudoers.d/ship")
	e.ssh(t, "grep -q 'fake-vps-smoke' /home/operator/.ssh/authorized_keys")
	e.ssh(t, "grep -q 'fake-vps-smoke' /home/deploy/.ssh/authorized_keys")
	e.ssh(t, "test -d /etc/ship/providers && test -d /etc/ship/secrets && test ! -e /etc/ship/backups")
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
			} `json:"ingress"`
			Features struct {
				Docker bool `json:"docker"`
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
	if state.Desired.Ingress.Expose != "public" {
		t.Fatalf("unexpected ingress: %+v", state.Desired.Ingress)
	}
	if state.Desired.Features.Docker {
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
	assertEqual(t, strings.TrimSpace(e.ssh(t, "timedatectl show --property=Timezone --value")), "UTC")
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

func (e *smokeEnv) assertDeployAuthorizedKeysContains(t *testing.T, want string) {
	t.Helper()
	got := strings.TrimSpace(e.ssh(t, "cat /home/deploy/.ssh/authorized_keys"))
	if !strings.Contains(got, strings.TrimSpace(want)) {
		t.Fatalf("deploy authorized_keys missing key\nwanted present:\n%s\nactual:\n%s", want, got)
	}
}

func (e *smokeEnv) assertDeployAuthorizedKeysLineCount(t *testing.T, want string, count int) {
	t.Helper()
	got := strings.TrimSpace(e.ssh(t, "cat /home/deploy/.ssh/authorized_keys"))
	actual := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == strings.TrimSpace(want) {
			actual++
		}
	}
	if actual != count {
		t.Fatalf("deploy authorized_keys line count = %d, want %d\nline:\n%s\nactual:\n%s", actual, count, want, got)
	}
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
	app := e.memberListApp(t, "member-list")
	list := e.ship(t, app, nil, "member", "ls")
	assertContains(t, list, "fake-vps-smoke owner ssh-ed25519 SHA256:")
}

func (e *smokeEnv) memberListApp(t *testing.T, name string) string {
	t.Helper()
	app := filepath.Join(e.tmp, name)
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "Dockerfile"), "FROM alpine\nCMD [\"sleep\", \"3600\"]\n")
	mustWrite(t, filepath.Join(app, "ship.toml"), `name = "memberlist"
box = "fake-vps"

[processes]
web = { port = 3000 }
`)
	return app
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
	assertContains(t, output, "failed to run doctor:")
	assertContains(t, output, "Permission denied")
	assertContains(t, output, "next: ship box doctor fake-vps")
}

func (e *smokeEnv) assertWrongHostKeyRefuses(t *testing.T) {
	t.Helper()
	home := filepath.Join(e.tmp, "wrong-host-key-home")
	copyShipIdentityForHome(t, e.shipHome, home)
	shipConfig := filepath.Join(home, ".config", "ship")
	if err := os.MkdirAll(shipConfig, 0700); err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(shipConfig, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte(readFile(t, filepath.Join(e.shipHome, ".config", "ship", "known_hosts"))), 0600); err != nil {
		t.Fatal(err)
	}
	clean := e.runCommand(t, e.repoRoot, []string{"HOME=" + home}, nil, e.goBin, "box", "doctor", "fake-vps")
	if clean.err != nil {
		t.Fatalf("box doctor should pass with the real host key\nstdout:\n%s\nstderr:\n%s", clean.stdout, clean.stderr)
	}
	wrongKey := strings.Join(strings.Fields(alicePublicKeyForFreshInstallTest())[:2], " ")
	if err := os.WriteFile(knownHosts, []byte("fake-vps "+wrongKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := e.runCommand(t, e.repoRoot, []string{"HOME=" + home}, nil, e.goBin, "box", "doctor", "fake-vps")
	if result.err == nil {
		t.Fatalf("box doctor should fail with wrong host key\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	output := result.stdout + result.stderr
	assertContains(t, output, "box host key changed")
	assertContains(t, output, "SSH host key for fake-vps is unknown or changed")
	assertContains(t, output, "next: ship box setup <ssh-target>")
	assertNotContains(t, output, "StrictHostKeyChecking=accept-new")
}

func alicePublicKeyForFreshInstallTest() string {
	return "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/ alice"
}

func (e *smokeEnv) assertHostDoctorHealthy(t *testing.T) {
	t.Helper()
	output := e.ship(t, e.repoRoot, nil, "box", "doctor", "fake-vps")
	for _, want := range []string{
		"host_state ok -",
		"service_health ok -",
		"sudoers_identity ok -",
		"host_tools ok -",
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
	for _, id := range []string{"host_state", "service_health", "sudoers_identity", "host_tools", "disk_space", "tls_certs", "reaper_timer", "deploy_journals"} {
		check := findDoctorCheck(t, doctorPayload, id)
		if check.Status != "ok" || check.Evidence == "" || check.Remediation == "" {
			t.Fatalf("unexpected healthy doctor check %s: %+v", id, check)
		}
	}
}

func (e *smokeEnv) assertDoctorRecordingDeltaForStoppedReaper(t *testing.T) {
	t.Helper()
	e.dockerExec(t, "/usr/local/bin/ship server doctor record")
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

	e.dockerExec(t, "/usr/local/bin/ship server doctor record")
	state = readDoctorState(t, e)
	if len(state.Delta) != 1 || state.Delta[0].ID != "reaper_timer" || state.Delta[0].Status != "degraded" {
		t.Fatalf("expected newly degraded reaper delta, got: %+v", state.Delta)
	}
	if findDoctorCheck(t, state.Checks, "reaper_timer").Status != "degraded" {
		t.Fatalf("recorded checks did not reflect degraded reaper: %+v", state.Checks)
	}

	e.dockerExec(t, "/usr/local/bin/ship server doctor record")
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

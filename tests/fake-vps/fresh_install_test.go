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

const freshHostImage = "simple-vps-fresh-host:local"

func TestFreshHostInstall(t *testing.T) {
	if os.Getenv("SHIP_RUN_FAKE_VPS_SMOKE") != "1" {
		t.Skip("set SHIP_RUN_FAKE_VPS_SMOKE=1 to run Docker-backed fake VPS smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)

	env := newSmokeEnvWithImage(t, ctx, freshHostImage, filepath.Join(repoRootForTest(t), "tests/fake-vps/Dockerfile.install"))
	env.buildBinaries(t)
	env.buildImage(t)
	env.startContainer(t)
	env.configureSSH(t, "root")
	env.waitForSSH(t)

	env.assertHostDoctorNotInstalled(t)

	keyPath := filepath.Join(env.tmp, "operator.pub")
	mustWrite(t, keyPath, "ssh-ed25519 AAAAoperator test-operator\n")

	first := env.installHost(t, keyPath)
	if first <= 0 {
		t.Fatalf("first install changed %d operations, want > 0", first)
	}

	env.assertFreshHostInstalled(t)
	env.assertHostDoctorHealthy(t)

	env.ssh(t, "mkdir -p /var/apps/api.production/data && printf sentinel > /var/apps/api.production/data/sentinel && chmod 600 /var/apps/api.production/data/sentinel")
	second := env.installHost(t, keyPath)
	if second != 0 {
		t.Fatalf("second install changed %d operations, want 0", second)
	}
	assertEqual(t, strings.TrimSpace(env.ssh(t, "cat /var/apps/api.production/data/sentinel")), "sentinel")
	assertEqual(t, strings.TrimSpace(env.ssh(t, "stat -c '%a' /var/apps/api.production/data/sentinel")), "600")
}

func (e *smokeEnv) installHost(t *testing.T, publicKeyFile string) int {
	t.Helper()
	result := e.runSimpleVPS(t, e.repoRoot, nil,
		"host", "install",
		"--mode", "remote",
		"--host", "fake-vps",
		"--bootstrap-user", "root",
		"--operator-ssh-public-key-file", publicKeyFile,
		"--shared-key",
		"--timezone", "Europe/Madrid",
		"--locale", "en_US.UTF-8",
		"--no-tailscale",
		"--no-cloudflare-tunnel",
		"--no-litestream",
	)
	// Always log the install output. When a downstream assertion fails
	// (e.g., last_apply.status != "ok") the install transcript is the
	// only place that names the failing op; `t.Logf` is printed when
	// the test fails and is invisible on a green run.
	t.Logf("install stdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	if result.err != nil {
		t.Fatalf("ship host install failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	assertContains(t, result.stdout, "Provisioning complete")
	// Read operations-changed from /etc/simple-vps/host.json on the
	// VPS. The trailing `Apply ... changed N operations` stdout line
	// can be dropped by SSH pipe-close races on some Linux/Docker
	// runners; host.json is the authoritative record (ADR-0002 §2).
	return changedOperationsFromHostState(t, e)
}

func (e *smokeEnv) assertFreshHostInstalled(t *testing.T) {
	t.Helper()
	e.ssh(t, "test -x /usr/local/bin/ship")
	e.ssh(t, "getent passwd operator >/dev/null")
	e.ssh(t, "getent passwd deploy >/dev/null")
	assertContains(t, e.ssh(t, "id -nG operator"), "sudo")
	e.ssh(t, "grep -q 'operator ALL=(ALL) NOPASSWD:ALL' /etc/sudoers.d/operator")
	e.ssh(t, "grep -Fq 'deploy ALL=(root) NOPASSWD: /usr/local/bin/ship server app *, /usr/local/bin/ship server status, /usr/local/bin/ship server status *, /usr/local/bin/ship server doctor, /usr/local/bin/ship server doctor *' /etc/sudoers.d/ship")
	e.ssh(t, "grep -q 'ssh-ed25519 AAAAoperator test-operator' /home/operator/.ssh/authorized_keys")
	e.ssh(t, "grep -q 'ssh-ed25519 AAAAoperator test-operator' /home/deploy/.ssh/authorized_keys")

	hostState := e.ssh(t, "cat /etc/simple-vps/host.json")
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

	assertContains(t, e.ssh(t, "cat /run/simple-vps-fresh-host/systemctl.log"), "restart ssh.service")
	systemctlLog := e.ssh(t, "cat /run/simple-vps-fresh-host/systemctl.log")
	assertContains(t, systemctlLog, "start fail2ban.service")
	assertContains(t, systemctlLog, "start caddy.service")

	ufwLog := e.ssh(t, "cat /run/simple-vps-fresh-host/ufw.log")
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
	assertEqual(t, strings.TrimSpace(e.ssh(t, "cat /run/simple-vps-fresh-host/timezone")), "Europe/Madrid")
	assertEqual(t, strings.TrimSpace(e.ssh(t, "cat /run/simple-vps-fresh-host/locale")), "en_US.UTF-8")
}

func (e *smokeEnv) assertHostDoctorNotInstalled(t *testing.T) {
	t.Helper()
	result := e.runSimpleVPS(t, e.repoRoot, nil, "host", "doctor", "--server", "fake-vps")
	if result.err == nil {
		t.Fatalf("host doctor passed before install\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	output := result.stdout + result.stderr
	assertContains(t, output, "failed to run doctor")
	assertContains(t, output, "host is not installed")

	jsonResult := e.runSimpleVPS(t, e.repoRoot, nil, "host", "doctor", "--json", "--server", "fake-vps")
	if jsonResult.err == nil {
		t.Fatalf("host doctor --json passed before install\nstdout:\n%s\nstderr:\n%s", jsonResult.stdout, jsonResult.stderr)
	}
	var payload struct {
		State struct {
			Status   string   `json:"status"`
			Findings []string `json:"findings"`
		} `json:"state"`
		Healthy bool `json:"healthy"`
	}
	if err := json.Unmarshal([]byte(jsonResult.stdout), &payload); err != nil {
		t.Fatalf("host doctor --json output not parseable as JSON: %v\nstdout:\n%s\nstderr:\n%s", err, jsonResult.stdout, jsonResult.stderr)
	}
	if payload.Healthy || payload.State.Status != "degraded" || len(payload.State.Findings) == 0 {
		t.Fatalf("unexpected degraded doctor payload: %+v", payload)
	}
}

func (e *smokeEnv) assertHostDoctorHealthy(t *testing.T) {
	t.Helper()
	output := e.simpleVPS(t, e.repoRoot, nil, "host", "doctor", "--server", "fake-vps")
	assertContains(t, output, "ship doctor")
	assertContains(t, output, "state: healthy")
	assertContains(t, output, "services: healthy")
	assertContains(t, output, "identity: healthy")

	rawDoctorJSON := e.simpleVPS(t, e.repoRoot, nil, "host", "doctor", "--json", "--server", "fake-vps")
	var doctorPayload struct {
		State struct {
			Status   string   `json:"status"`
			Findings []string `json:"findings"`
		} `json:"state"`
		Services struct {
			Status   string   `json:"status"`
			Findings []string `json:"findings"`
		} `json:"services"`
		Identity struct {
			Status   string   `json:"status"`
			Findings []string `json:"findings"`
		} `json:"identity"`
		Healthy bool `json:"healthy"`
	}
	if err := json.Unmarshal([]byte(rawDoctorJSON), &doctorPayload); err != nil {
		t.Fatalf("host doctor --json output not parseable as JSON: %v\nraw:\n%s", err, rawDoctorJSON)
	}
	if !doctorPayload.Healthy || doctorPayload.State.Status != "healthy" || doctorPayload.Services.Status != "healthy" || doctorPayload.Identity.Status != "healthy" {
		t.Fatalf("unexpected healthy doctor payload: %+v", doctorPayload)
	}

	rawStatusJSON := e.simpleVPS(t, e.repoRoot, nil, "host", "status", "--json", "--server", "fake-vps")
	var statusPayload struct {
		State struct {
			Status    string `json:"status"`
			Installed bool   `json:"installed"`
			Path      string `json:"path"`
		} `json:"state"`
		Services map[string]string `json:"services"`
		Tools    map[string]string `json:"tools"`
	}
	if err := json.Unmarshal([]byte(rawStatusJSON), &statusPayload); err != nil {
		t.Fatalf("host status --json output not parseable as JSON: %v\nraw:\n%s", err, rawStatusJSON)
	}
	if !statusPayload.State.Installed || statusPayload.State.Status != "installed" || statusPayload.State.Path != "/etc/simple-vps/host.json" {
		t.Fatalf("unexpected host status payload: %+v", statusPayload)
	}
	if statusPayload.Services["caddy"] == "" || statusPayload.Tools["podman"] == "" {
		t.Fatalf("host status --json missing service/tool data: %+v", statusPayload)
	}
}

// changedOperationsFromHostState reads /etc/simple-vps/host.json from
// the fake VPS and returns meta.last_apply.operations_changed. Reading
// the file directly avoids the SSH pipe-close race that drops trailing
// inner-installer stdout on some Linux/Docker runners.
func changedOperationsFromHostState(t *testing.T, e *smokeEnv) int {
	t.Helper()
	raw := e.ssh(t, "cat /etc/simple-vps/host.json")
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

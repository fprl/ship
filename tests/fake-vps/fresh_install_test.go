package fakevps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const freshHostImage = "simple-vps-fresh-host:local"

func TestFreshHostInstall(t *testing.T) {
	if os.Getenv("SIMPLE_VPS_RUN_FAKE_VPS_SMOKE") != "1" {
		t.Skip("set SIMPLE_VPS_RUN_FAKE_VPS_SMOKE=1 to run Docker-backed fake VPS smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)

	env := newSmokeEnvWithImage(t, ctx, freshHostImage, filepath.Join(repoRootForTest(t), "tests/fake-vps/Dockerfile.install"))
	env.buildBinaries(t)
	env.buildImage(t)
	env.startContainer(t)
	env.configureSSH(t, "root")
	env.waitForSSH(t)

	keyPath := filepath.Join(env.tmp, "operator.pub")
	mustWrite(t, keyPath, "ssh-ed25519 AAAAoperator test-operator\n")

	first := env.installHost(t, keyPath)
	if first <= 0 {
		t.Fatalf("first install changed %d operations, want > 0", first)
	}

	env.assertFreshHostInstalled(t)

	second := env.installHost(t, keyPath)
	if second != 0 {
		t.Fatalf("second install changed %d operations, want 0", second)
	}
}

func (e *smokeEnv) installHost(t *testing.T, publicKeyFile string) int {
	t.Helper()
	output := e.simpleVPS(t, e.repoRoot, nil,
		"host", "install",
		"--mode", "remote",
		"--host", "fake-vps",
		"--bootstrap-user", "root",
		"--ssh-public-key-file", publicKeyFile,
		"--shared-key",
		"--timezone", "Europe/Madrid",
		"--locale", "en_US.UTF-8",
		"--no-tailscale",
		"--no-cloudflare-tunnel",
		"--no-litestream",
	)
	assertContains(t, output, "Provisioning complete")
	return changedOperations(t, output)
}

func (e *smokeEnv) assertFreshHostInstalled(t *testing.T) {
	t.Helper()
	e.ssh(t, "test -x /usr/local/bin/simple-vps")
	e.ssh(t, "getent passwd operator >/dev/null")
	e.ssh(t, "getent passwd deploy >/dev/null")
	assertContains(t, e.ssh(t, "id -nG operator"), "sudo")
	e.ssh(t, "grep -q 'operator ALL=(ALL) NOPASSWD:ALL' /etc/sudoers.d/operator")
	e.ssh(t, "grep -q 'deploy ALL=(root) NOPASSWD: /usr/local/bin/simple-vps' /etc/sudoers.d/simple-vps")
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

func changedOperations(t *testing.T, output string) int {
	t.Helper()
	match := regexp.MustCompile(`changed ([0-9]+) operations`).FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("install output did not include changed operation count:\n%s", output)
	}
	n, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

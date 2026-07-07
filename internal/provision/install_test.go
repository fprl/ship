package provision

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fprl/simple-vps/internal/provision/host"
	"github.com/fprl/simple-vps/internal/store"
)

func TestRunInstallWritesHonestChangedCount(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}

	runner := &installFakeRunner{files: map[string]host.FileState{}}
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	summary, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		Timezone:              "UTC",
		Locale:                "en_US.UTF-8",
		Tailscale:             false,
		CloudflareTunnel:      false,
		InstallLitestream:     false,
		StateRoot:             root,
		HelperBinaryPath:      helper,
		ApplyID:               "apply-test",
		Now:                   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ApplyID != "apply-test" {
		t.Fatalf("unexpected apply id: %s", summary.ApplyID)
	}
	if summary.OperationsChanged == 0 {
		t.Fatal("expected install to report changed operations")
	}

	loaded, err := (store.Store{Root: root}).ReadHost()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.LastApply == nil {
		t.Fatal("expected last_apply metadata")
	}
	if loaded.Meta.LastApply.OperationsChanged != summary.OperationsChanged {
		t.Fatalf("metadata count %d did not match summary count %d", loaded.Meta.LastApply.OperationsChanged, summary.OperationsChanged)
	}
	if loaded.Meta.LastApply.Status != "ok" {
		t.Fatalf("unexpected apply status: %s", loaded.Meta.LastApply.Status)
	}
	if _, ok := runner.files["/etc/systemd/system/ssh.service"]; ok {
		t.Fatal("install must not overwrite the packaged ssh.service unit")
	}
	if !runner.ranCommand("install", "-d -o root -g root -m 700 /etc/simple-vps/secrets") {
		t.Fatal("expected /etc/simple-vps/secrets to be created mode 0700")
	}
}

func TestRunInstallDoesNotRestartSSHWhenConfigAlreadyConverged(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{
		"/etc/ssh/sshd_config": {
			Content: []byte(strings.Join([]string{
				"PermitRootLogin prohibit-password",
				"PasswordAuthentication no",
				"PubkeyAuthentication yes",
				"X11Forwarding no",
				"MaxAuthTries 3",
				"",
			}, "\n")),
			Owner: "root",
			Group: "root",
			Mode:  0644,
		},
	}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		Tailscale:             false,
		CloudflareTunnel:      false,
		InstallLitestream:     false,
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if command.Program == "systemctl" && strings.Join(command.Args, " ") == "restart ssh.service" {
			t.Fatalf("ssh restart should be gated on sshd_config drift, commands: %+v", runner.commands)
		}
	}
}

func TestRunInstallSkipsPinnedLitestream(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{
		files: map[string]host.FileState{},
		commandResults: map[string]host.CommandResult{
			"dpkg-query -W -f=${Version} litestream": {Stdout: []byte(litestreamVersion + "\n")},
		},
	}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		Tailscale:             false,
		CloudflareTunnel:      false,
		InstallLitestream:     true,
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		joined := command.Program + " " + strings.Join(command.Args, " ")
		if strings.Contains(joined, "litestream-"+litestreamVersion) && (command.Program == "curl" || command.Program == "apt-get") {
			t.Fatalf("pinned Litestream should not be downloaded or reinstalled, command: %+v", command)
		}
	}
}

func TestRunInstallRejectsLitestreamChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		Tailscale:             false,
		CloudflareTunnel:      false,
		InstallLitestream:     true,
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err == nil {
		t.Fatal("expected Litestream checksum mismatch to fail")
	}
	if !strings.Contains(err.Error(), "litestream checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got: %v", err)
	}
	for _, command := range runner.commands {
		joined := command.Program + " " + strings.Join(command.Args, " ")
		if command.Program == "apt-get" && strings.Contains(joined, ".deb") {
			t.Fatalf("mismatched Litestream artifact should not be installed, command: %+v", command)
		}
	}
}

func TestRunInstallInstallsLitestreamAfterChecksumMatch(t *testing.T) {
	arch := litestreamArch(runtime.GOARCH)
	if arch == "" {
		t.Skipf("unsupported test architecture: %s", runtime.GOARCH)
	}
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	deb := fmt.Sprintf("/tmp/simple-vps-litestream.TEST/litestream-%s-linux-%s.deb", litestreamVersion, arch)
	runner := &installFakeRunner{
		files: map[string]host.FileState{},
		commandResults: map[string]host.CommandResult{
			"sha256sum " + deb: {Stdout: []byte(litestreamSHA256(arch) + "  " + deb + "\n")},
		},
	}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		Tailscale:             false,
		CloudflareTunnel:      false,
		InstallLitestream:     true,
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	checkIndex := -1
	installIndex := -1
	for i, command := range runner.commands {
		joined := command.Program + " " + strings.Join(command.Args, " ")
		if joined == "sha256sum "+deb {
			checkIndex = i
		}
		if joined == "apt-get install -y "+deb {
			installIndex = i
		}
	}
	if checkIndex == -1 {
		t.Fatalf("expected Litestream checksum verification, commands: %+v", runner.commands)
	}
	if installIndex == -1 {
		t.Fatalf("expected Litestream deb install, commands: %+v", runner.commands)
	}
	if checkIndex > installIndex {
		t.Fatalf("Litestream deb was installed before checksum verification, commands: %+v", runner.commands)
	}
}

func TestRequireFileSHA256RejectsMalformedOutput(t *testing.T) {
	runner := &installFakeRunner{
		files: map[string]host.FileState{},
		commandResults: map[string]host.CommandResult{
			"sha256sum /tmp/artifact.deb": {Stdout: []byte("not-a-sha\n")},
		},
	}

	err := requireFileSHA256(host.Apply{Context: context.Background(), Runner: runner}, "artifact", "/tmp/artifact.deb", strings.Repeat("a", 64))
	if err == nil {
		t.Fatal("expected malformed checksum output to fail")
	}
	if !strings.Contains(err.Error(), "checksum output malformed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloudflareTunnelGuardReadsADRProviderState(t *testing.T) {
	runner := &installFakeRunner{files: map[string]host.FileState{
		"/etc/cloudflared/tunnel-token": {
			Content: []byte("token-test\n"),
			Owner:   "root",
			Group:   "cloudflared",
			Mode:    0640,
		},
		"/etc/simple-vps/providers/cloudflare.json": {
			Content: []byte(`{"version":1,"account_id":"account-test","tunnel_id":"tunnel-test","tunnel_name":"simple-vps-test","routes":{}}`),
			Owner:   "root",
			Group:   "root",
			Mode:    0600,
		},
	}}

	ready, err := cloudflareTunnelAlreadyConfigured(host.Apply{
		Context: context.Background(),
		Runner:  runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Fatal("expected Cloudflare tunnel guard to read providers/cloudflare.json")
	}
}

func TestRunInstallDoesNotRestartConvergedCloudflaredService(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	unit := cloudflaredUnit("/usr/bin/cloudflared tunnel --no-autoupdate run --token-file /etc/cloudflared/tunnel-token")
	runner := &installFakeRunner{files: map[string]host.FileState{
		"/etc/cloudflared/tunnel-token": {
			Content: []byte("token-test\n"),
			Owner:   "root",
			Group:   "cloudflared",
			Mode:    0640,
		},
		"/etc/systemd/system/cloudflared.service": {
			Content: []byte(unit),
			Owner:   "root",
			Group:   "root",
			Mode:    0644,
		},
	}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		Tailscale:             false,
		CloudflareTunnel:      true,
		CloudflareTunnelToken: "token-test",
		InstallLitestream:     false,
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if command.Program == "systemctl" && strings.Join(command.Args, " ") == "restart cloudflared.service" {
			t.Fatalf("cloudflared restart should be gated on unit/token drift, commands: %+v", runner.commands)
		}
	}
}

func TestRunInstallRestartsCloudflaredWhenTokenChanges(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	unit := cloudflaredUnit("/usr/bin/cloudflared tunnel --no-autoupdate run --token-file /etc/cloudflared/tunnel-token")
	runner := &installFakeRunner{files: map[string]host.FileState{
		"/etc/cloudflared/tunnel-token": {
			Content: []byte("old-token\n"),
			Owner:   "root",
			Group:   "cloudflared",
			Mode:    0640,
		},
		"/etc/systemd/system/cloudflared.service": {
			Content: []byte(unit),
			Owner:   "root",
			Group:   "root",
			Mode:    0644,
		},
	}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		Tailscale:             false,
		CloudflareTunnel:      true,
		CloudflareTunnelToken: "new-token",
		InstallLitestream:     false,
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runner.ranCommand("systemctl", "restart cloudflared.service") {
		t.Fatalf("expected cloudflared restart after token drift, commands: %+v", runner.commands)
	}
}

func TestRunInstallStartsInactiveConvergedCloudflaredService(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	unit := cloudflaredUnit("/usr/bin/cloudflared tunnel --no-autoupdate run --token-file /etc/cloudflared/tunnel-token")
	runner := &installFakeRunner{
		files: map[string]host.FileState{
			"/etc/cloudflared/tunnel-token": {
				Content: []byte("token-test\n"),
				Owner:   "root",
				Group:   "cloudflared",
				Mode:    0640,
			},
			"/etc/systemd/system/cloudflared.service": {
				Content: []byte(unit),
				Owner:   "root",
				Group:   "root",
				Mode:    0644,
			},
		},
		commandResults: map[string]host.CommandResult{
			"systemctl is-active --quiet cloudflared.service": {ExitCode: 3},
		},
	}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		Tailscale:             false,
		CloudflareTunnel:      true,
		CloudflareTunnelToken: "token-test",
		InstallLitestream:     false,
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.ranCommand("systemctl", "restart cloudflared.service") {
		t.Fatalf("did not expect restart for inactive converged service, commands: %+v", runner.commands)
	}
	if !runner.ranCommand("systemctl", "start cloudflared.service") {
		t.Fatalf("expected start for inactive converged service, commands: %+v", runner.commands)
	}
}

func TestRunInstallUsesHostUbuntuCodenameForDockerAndTailscaleRepos(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{
		"/etc/os-release": {
			Content: []byte("ID=ubuntu\nVERSION_CODENAME=jammy\n"),
			Owner:   "root",
			Group:   "root",
			Mode:    0644,
		},
	}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		Tailscale:             true,
		CloudflareTunnel:      false,
		InstallDocker:         true,
		InstallLitestream:     false,
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}
	dockerSource := string(runner.files["/etc/apt/sources.list.d/docker.list"].Content)
	tailscaleSource := string(runner.files["/etc/apt/sources.list.d/tailscale.list"].Content)
	if !strings.Contains(dockerSource, " jammy stable") {
		t.Fatalf("docker repo did not use host codename:\n%s", dockerSource)
	}
	if !strings.Contains(tailscaleSource, " jammy main") {
		t.Fatalf("tailscale repo did not use host codename:\n%s", tailscaleSource)
	}
	if !runner.ranCommand("curl", "-fsSL https://pkgs.tailscale.com/stable/ubuntu/jammy.noarmor.gpg -o /tmp/simple-vps-tailscale-apt.TEST/key") {
		t.Fatalf("tailscale key URL did not use host codename, commands: %+v", runner.commands)
	}
}

func TestOSReleaseValue(t *testing.T) {
	tests := []struct {
		name    string
		content string
		key     string
		want    string
	}{
		{
			name:    "plain",
			content: "VERSION_CODENAME=jammy\n",
			key:     "VERSION_CODENAME",
			want:    "jammy",
		},
		{
			name:    "double quoted",
			content: "VERSION_CODENAME=\"noble\"\n",
			key:     "VERSION_CODENAME",
			want:    "noble",
		},
		{
			name:    "single quoted crlf",
			content: "VERSION_CODENAME='oracular'\r\n",
			key:     "VERSION_CODENAME",
			want:    "oracular",
		},
		{
			name:    "ignores comments",
			content: "# VERSION_CODENAME=wrong\nUBUNTU_CODENAME=plucky\n",
			key:     "UBUNTU_CODENAME",
			want:    "plucky",
		},
		{
			name:    "missing",
			content: "ID=ubuntu\n",
			key:     "VERSION_CODENAME",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := osReleaseValue([]byte(tt.content), tt.key); got != tt.want {
				t.Fatalf("osReleaseValue() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Podman provisioner coverage (ADR-0005 cutover items 23, 24; ADR-0006 Cut 2) ---

func TestRunInstallInstallsPodmanFromUbuntuUniverse(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !runner.ranCommand("apt-get", "install -y podman") {
		t.Fatalf("expected podman to be installed via apt-get, commands: %+v", runner.commands)
	}

	loaded, err := (store.Store{Root: root}).ReadHost()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Desired.Packages["podman"]
	if !ok {
		t.Fatalf("expected podman in desired packages, got %+v", loaded.Desired.Packages)
	}
	if got.Source != "ubuntu" {
		t.Fatalf("expected podman source=ubuntu, got %+v", got)
	}
}

func TestRunInstallCreatesIngressNetworkWhenAbsent(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{
		files: map[string]host.FileState{},
		commandResults: map[string]host.CommandResult{
			"podman network exists ingress": {ExitCode: 1},
		},
	}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !runner.ranCommand("podman", "network create ingress") {
		t.Fatalf("expected ingress network to be created, commands: %+v", runner.commands)
	}
}

func TestRunInstallCreatesDeployTmpDirWithStickyMode(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Mode 1777 = sticky world-writable. The deploy user needs to drop
	// files there, but other local users must not delete them mid-deploy.
	if !runner.ranCommand("install", "-d -o root -g root -m 1777 /tmp/simple-vps-deploy") {
		t.Fatalf("expected /tmp/simple-vps-deploy to be created with mode 1777, commands: %+v", runner.commands)
	}
}

func TestRunInstallWritesCaddyContainerSystemdUnit(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	unit, ok := runner.files["/etc/systemd/system/caddy.service"]
	if !ok {
		t.Fatal("expected caddy.service unit to be installed")
	}
	if !runner.ranCommand("install", "-d -o root -g root -m 755 /var/apps") {
		t.Fatal("expected /var/apps to be created before caddy.service starts")
	}
	content := string(unit.Content)
	for _, want := range []string{
		"podman run --rm --name caddy",
		"--network ingress",
		"--publish 80:80",
		"--publish 443:443",
		"-v /etc/caddy:/etc/caddy:Z",
		"docker.io/library/caddy:2-alpine",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("caddy.service missing %q\nunit:\n%s", want, content)
		}
	}
	// Post-cutover: Caddy is no longer an apt package. Make sure the
	// provisioner didn't accidentally re-add either the third-party
	// Caddy apt repo or `apt install caddy`.
	if runner.ranCommand("apt-get", "install -y caddy") {
		t.Fatal("apt install caddy ran; provisioner should manage Caddy via podman")
	}
	for _, cmd := range runner.commands {
		if cmd.Program == "curl" && strings.Contains(strings.Join(cmd.Args, " "), "caddy") {
			t.Fatalf("Caddy apt-repo key fetch ran; provisioner should manage Caddy via podman: %+v", cmd)
		}
	}
}

func TestRunInstallWritesPreviewReaperAndDoctorTimers(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	service, ok := runner.files["/etc/systemd/system/ship-preview-reaper.service"]
	if !ok {
		t.Fatal("expected preview reaper service")
	}
	if !strings.Contains(string(service.Content), "ExecStart=/usr/local/bin/ship server env reap") {
		t.Fatalf("unexpected reaper service:\n%s", service.Content)
	}
	timer, ok := runner.files["/etc/systemd/system/ship-preview-reaper.timer"]
	if !ok {
		t.Fatal("expected preview reaper timer")
	}
	timerContent := string(timer.Content)
	for _, want := range []string{"OnBootSec=15min", "OnUnitActiveSec=1h", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(timerContent, want) {
			t.Fatalf("timer missing %q:\n%s", want, timerContent)
		}
	}

	doctorService, ok := runner.files["/etc/systemd/system/ship-doctor.service"]
	if !ok {
		t.Fatal("expected doctor service")
	}
	if !strings.Contains(string(doctorService.Content), "ExecStart=/usr/local/bin/ship server doctor record") {
		t.Fatalf("unexpected doctor service:\n%s", doctorService.Content)
	}
	doctorTimer, ok := runner.files["/etc/systemd/system/ship-doctor.timer"]
	if !ok {
		t.Fatal("expected doctor timer")
	}
	doctorTimerContent := string(doctorTimer.Content)
	for _, want := range []string{"OnBootSec=30min", "OnUnitActiveSec=24h", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(doctorTimerContent, want) {
			t.Fatalf("doctor timer missing %q:\n%s", want, doctorTimerContent)
		}
	}
}

func TestEnsureSystemdUnitEnabledRunsEnableWhenDisabled(t *testing.T) {
	runner := &installFakeRunner{
		files: map[string]host.FileState{},
		commandResults: map[string]host.CommandResult{
			"systemctl is-enabled --quiet ship-preview-reaper.timer": {ExitCode: 1},
		},
	}
	changed, err := ensureSystemdUnitEnabled(host.Apply{Context: context.Background(), Runner: runner}, "ship-preview-reaper.timer")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected disabled timer to be enabled")
	}
	if !runner.ranCommand("systemctl", "enable ship-preview-reaper.timer") {
		t.Fatalf("expected systemctl enable, commands: %+v", runner.commands)
	}
}

func TestCaddyUnitIngressModes(t *testing.T) {
	public := caddyUnit("public")
	if !strings.Contains(public, "--publish 80:80") || !strings.Contains(public, "--publish 443:443") {
		t.Fatalf("public ingress should publish 80/443:\n%s", public)
	}

	cloudflare := caddyUnit("cloudflare")
	if !strings.Contains(cloudflare, "--publish 127.0.0.1:8080:80") {
		t.Fatalf("cloudflare ingress should publish loopback 8080:\n%s", cloudflare)
	}
	if strings.Contains(cloudflare, "--publish 80:80") || strings.Contains(cloudflare, "--publish 443:443") {
		t.Fatalf("cloudflare ingress should not publish public 80/443:\n%s", cloudflare)
	}

	private := caddyUnit("private")
	if strings.Contains(private, "--publish ") {
		t.Fatalf("private ingress should not publish host ports:\n%s", private)
	}
}

// --- Podman host baseline: UFW podman+ rules + registries.conf.d ---
// (real-box smoke findings 3 + 4)

const ubuntuBeforeRules = `#
# rules.before
#
# Rules that should be run before the ufw command line added rules. Custom
# rules should be added to one of these chains:
#   ufw-before-input
#   ufw-before-output
#   ufw-before-forward
#

# Don't delete these required lines, otherwise there will be errors
*filter
:ufw-before-input - [0:0]
:ufw-before-output - [0:0]
:ufw-before-forward - [0:0]
:ufw-not-local - [0:0]
# End required lines

# allow all on loopback
-A ufw-before-input -i lo -j ACCEPT
-A ufw-before-output -o lo -j ACCEPT

COMMIT
`

func TestInjectPodmanUfwBlockFreshInsert(t *testing.T) {
	next, changed, err := injectPodmanUfwBlock(ubuntuBeforeRules, podmanUfwBlock())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change on fresh insert")
	}
	if !strings.Contains(next, "# BEGIN simple-vps podman bridges\n") {
		t.Fatalf("missing BEGIN marker:\n%s", next)
	}
	if !strings.Contains(next, "\n# END simple-vps podman bridges\n") {
		t.Fatalf("missing END marker:\n%s", next)
	}
	if !strings.Contains(next, "-A ufw-before-input -i podman+ -j ACCEPT\n") {
		t.Fatalf("missing input ACCEPT line:\n%s", next)
	}
	if !strings.Contains(next, "-A ufw-before-forward -i podman+ -j ACCEPT\n") {
		t.Fatalf("missing forward-in ACCEPT line:\n%s", next)
	}
	if !strings.Contains(next, "-A ufw-before-forward -o podman+ -j ACCEPT\n") {
		t.Fatalf("missing forward-out ACCEPT line:\n%s", next)
	}
	// Block must land AFTER the anchor (chain declarations) and
	// BEFORE COMMIT, otherwise the rules don't take effect.
	anchorIdx := strings.Index(next, "# End required lines")
	beginIdx := strings.Index(next, "# BEGIN simple-vps podman bridges")
	commitIdx := strings.Index(next, "\nCOMMIT")
	if !(anchorIdx < beginIdx && beginIdx < commitIdx) {
		t.Fatalf("block in wrong position: anchor=%d begin=%d commit=%d\n%s", anchorIdx, beginIdx, commitIdx, next)
	}
}

func TestInjectPodmanUfwBlockIsIdempotent(t *testing.T) {
	once, _, err := injectPodmanUfwBlock(ubuntuBeforeRules, podmanUfwBlock())
	if err != nil {
		t.Fatal(err)
	}
	twice, changed, err := injectPodmanUfwBlock(once, podmanUfwBlock())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second injection must be a no-op")
	}
	if twice != once {
		t.Fatalf("second injection mutated content:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
}

func TestInjectPodmanUfwBlockReplacesExistingBlock(t *testing.T) {
	stale := strings.Replace(
		ubuntuBeforeRules,
		"# End required lines\n",
		"# End required lines\n\n# BEGIN simple-vps podman bridges\n-A ufw-before-input -i podman+ -j REJECT\n# END simple-vps podman bridges\n\n",
		1,
	)
	next, changed, err := injectPodmanUfwBlock(stale, podmanUfwBlock())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change when replacing a stale block")
	}
	if strings.Contains(next, "-j REJECT") {
		t.Fatalf("stale REJECT rule survived replacement:\n%s", next)
	}
	if !strings.Contains(next, "-A ufw-before-input -i podman+ -j ACCEPT") {
		t.Fatalf("canonical ACCEPT rule missing after replace:\n%s", next)
	}
	// Exactly one BEGIN/END pair after replacement.
	if strings.Count(next, "# BEGIN simple-vps podman bridges") != 1 {
		t.Fatalf("expected exactly one BEGIN marker:\n%s", next)
	}
	if strings.Count(next, "# END simple-vps podman bridges") != 1 {
		t.Fatalf("expected exactly one END marker:\n%s", next)
	}
}

func TestInjectPodmanUfwBlockPreservesUserContent(t *testing.T) {
	customized := strings.Replace(
		ubuntuBeforeRules,
		"-A ufw-before-input -i lo -j ACCEPT\n",
		"-A ufw-before-input -i lo -j ACCEPT\n# user: allow vpn\n-A ufw-before-input -p udp --dport 51820 -j ACCEPT\n",
		1,
	)
	next, _, err := injectPodmanUfwBlock(customized, podmanUfwBlock())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "# user: allow vpn") {
		t.Fatalf("dropped user comment:\n%s", next)
	}
	if !strings.Contains(next, "-A ufw-before-input -p udp --dport 51820 -j ACCEPT") {
		t.Fatalf("dropped user rule:\n%s", next)
	}
}

func TestInjectPodmanUfwBlockRejectsHalfMarker(t *testing.T) {
	half := strings.Replace(
		ubuntuBeforeRules,
		"# End required lines\n",
		"# End required lines\n\n# BEGIN simple-vps podman bridges\n# but no END here\n",
		1,
	)
	if _, _, err := injectPodmanUfwBlock(half, podmanUfwBlock()); err == nil {
		t.Fatal("expected error on half-present marker pair")
	}
}

func TestInjectPodmanUfwBlockRejectsMissingAnchor(t *testing.T) {
	noAnchor := strings.ReplaceAll(ubuntuBeforeRules, "# End required lines\n", "")
	if _, _, err := injectPodmanUfwBlock(noAnchor, podmanUfwBlock()); err == nil {
		t.Fatal("expected error when the anchor line is absent")
	}
}

func TestRunInstallWritesPodmanHostBaseline(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{
		files: map[string]host.FileState{
			"/etc/ufw/before.rules": {
				Content: []byte(ubuntuBeforeRules),
				Owner:   "root", Group: "root", Mode: 0640,
			},
			"/etc/default/ufw": {
				Content: []byte("DEFAULT_FORWARD_POLICY=\"DROP\"\nIPV6=yes\n"),
				Owner:   "root", Group: "root", Mode: 0644,
			},
		},
	}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. before.rules has our marker block inserted after the anchor.
	updated, ok := runner.files["/etc/ufw/before.rules"]
	if !ok {
		t.Fatal("expected /etc/ufw/before.rules to be written")
	}
	if !strings.Contains(string(updated.Content), "-A ufw-before-input -i podman+ -j ACCEPT") {
		t.Fatalf("before.rules missing input ACCEPT:\n%s", updated.Content)
	}
	if !strings.Contains(string(updated.Content), "-A ufw-before-forward -i podman+ -j ACCEPT") ||
		!strings.Contains(string(updated.Content), "-A ufw-before-forward -o podman+ -j ACCEPT") {
		t.Fatalf("before.rules missing forward ACCEPT pair:\n%s", updated.Content)
	}
	if !strings.Contains(string(updated.Content), "# allow all on loopback") {
		t.Fatalf("before.rules lost the user/distro content below the anchor:\n%s", updated.Content)
	}

	// 2. default/ufw flipped to ACCEPT, IPV6 line preserved.
	policy, ok := runner.files["/etc/default/ufw"]
	if !ok {
		t.Fatal("expected /etc/default/ufw to be written")
	}
	if !strings.Contains(string(policy.Content), `DEFAULT_FORWARD_POLICY="ACCEPT"`) {
		t.Fatalf("default/ufw did not flip forward policy:\n%s", policy.Content)
	}
	if !strings.Contains(string(policy.Content), "IPV6=yes") {
		t.Fatalf("default/ufw lost unrelated lines:\n%s", policy.Content)
	}

	// 3. registries drop-in written.
	reg, ok := runner.files["/etc/containers/registries.conf.d/00-simple-vps.conf"]
	if !ok {
		t.Fatal("expected /etc/containers/registries.conf.d/00-simple-vps.conf to be written")
	}
	if !strings.Contains(string(reg.Content), `unqualified-search-registries = ["docker.io"]`) {
		t.Fatalf("registries drop-in missing the docker.io entry:\n%s", reg.Content)
	}

	// 4. `ufw reload` was invoked at least once.
	if !runner.ranCommand("ufw", "reload") {
		t.Fatalf("expected `ufw reload` after editing UFW config, commands: %+v", runner.commands)
	}
}

func TestRunInstallSkipsIngressNetworkCreationWhenPresent(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "simple-vps")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	// Default fake runner returns ExitCode 0 for unknown commands, so
	// "podman network exists ingress" reports "exists" (exit 0).
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorUser:          "operator",
		DeployUser:            "deploy",
		OperatorSSHPublicKeys: []string{"ssh-ed25519 AAAAoperator test"},
		DeploySSHPublicKeys:   []string{"ssh-ed25519 AAAAdeploy test"},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	if runner.ranCommand("podman", "network create ingress") {
		t.Fatalf("ingress network create should be skipped when present, commands: %+v", runner.commands)
	}
}

func TestUbuntuCodenameFallsBackToNoble(t *testing.T) {
	runner := &installFakeRunner{files: map[string]host.FileState{}}
	got, err := ubuntuCodename(host.Apply{Context: context.Background(), Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if got != "noble" {
		t.Fatalf("ubuntuCodename() = %q, want noble", got)
	}
}

type installFakeRunner struct {
	files          map[string]host.FileState
	commands       []host.Command
	commandResults map[string]host.CommandResult
}

func (r *installFakeRunner) ReadFile(_ context.Context, path string) (host.FileState, error) {
	if file, ok := r.files[path]; ok {
		return file, nil
	}
	// Pretend the essential package install seeded the standard
	// Ubuntu config files. The fake doesn't actually run apt, so
	// tests that exercise ops which read those files (e.g.
	// addPodmanHostBaseline reading /etc/ufw/before.rules) would
	// otherwise hit ErrNotExist on a "successful" essentials step.
	if defaults, ok := installFakeDefaults[path]; ok {
		return defaults, nil
	}
	return host.FileState{}, host.ErrNotExist
}

// installFakeDefaults mirrors what `apt-get install -y ufw` etc.
// drop on a fresh Ubuntu 24.04 box. Add entries here when a new op
// needs to read a file the install assumes is already present.
var installFakeDefaults = map[string]host.FileState{
	"/etc/ufw/before.rules": {
		Content: []byte(ubuntuBeforeRules),
		Owner:   "root", Group: "root", Mode: 0640,
	},
	"/etc/default/ufw": {
		Content: []byte("IPV6=yes\nDEFAULT_INPUT_POLICY=\"DROP\"\nDEFAULT_OUTPUT_POLICY=\"ACCEPT\"\nDEFAULT_FORWARD_POLICY=\"DROP\"\n"),
		Owner:   "root", Group: "root", Mode: 0644,
	},
}

func (r *installFakeRunner) WriteFile(_ context.Context, file host.File) error {
	r.files[file.Path] = host.FileState{
		Content: append([]byte(nil), file.Content...),
		Owner:   file.Owner,
		Group:   file.Group,
		Mode:    file.Mode,
	}
	return nil
}

func (r *installFakeRunner) Validate(_ context.Context, _ host.Validation) error {
	return nil
}

func (r *installFakeRunner) Run(_ context.Context, command host.Command) (host.CommandResult, error) {
	r.commands = append(r.commands, command)
	if result, ok := r.commandResults[installCommandKey(command)]; ok {
		return result, nil
	}
	switch command.Program {
	case "stat":
		return host.CommandResult{ExitCode: 1}, nil
	case "dpkg-query":
		return host.CommandResult{ExitCode: 1}, nil
	case "getent":
		return host.CommandResult{ExitCode: 1}, nil
	case "id":
		if len(command.Args) > 0 && command.Args[0] == "-nG" {
			return host.CommandResult{Stdout: []byte(command.Args[1] + "\n")}, nil
		}
		return host.CommandResult{ExitCode: 1}, nil
	case "timedatectl":
		if strings.Contains(strings.Join(command.Args, " "), "show") {
			return host.CommandResult{Stdout: []byte("UTC\n")}, nil
		}
	case "localectl":
		return host.CommandResult{Stdout: []byte("System Locale: LANG=en_US.UTF-8\n")}, nil
	case "gpg":
		return r.runGPG(command)
	case "curl":
		return r.runCurl(command), nil
	case "sha256sum":
		return r.runSHA256Sum(command), nil
	case "mktemp":
		if len(command.Args) == 2 && command.Args[0] == "-d" {
			return host.CommandResult{Stdout: []byte(strings.TrimSuffix(command.Args[1], ".XXXXXX") + ".TEST\n")}, nil
		}
	}
	return host.CommandResult{}, nil
}

func (r *installFakeRunner) runCurl(command host.Command) host.CommandResult {
	output := ""
	url := ""
	for i := 0; i < len(command.Args); i++ {
		arg := command.Args[i]
		switch {
		case arg == "-o" && i+1 < len(command.Args):
			output = command.Args[i+1]
			i++
		case strings.HasPrefix(arg, "-"):
		default:
			url = arg
		}
	}
	if output != "" {
		r.files[output] = host.FileState{
			Content: []byte("fake curl content for " + url + "\n"),
			Owner:   "root",
			Group:   "root",
			Mode:    0644,
		}
	}
	return host.CommandResult{}
}

func (r *installFakeRunner) runSHA256Sum(command host.Command) host.CommandResult {
	if len(command.Args) != 1 {
		return host.CommandResult{ExitCode: 1, Stderr: []byte("unsupported fake sha256sum command")}
	}
	path := command.Args[0]
	file, ok := r.files[path]
	if !ok {
		return host.CommandResult{ExitCode: 1, Stderr: []byte("missing fake file")}
	}
	sum := sha256.Sum256(file.Content)
	return host.CommandResult{Stdout: []byte(fmt.Sprintf("%x  %s\n", sum, path))}
}

func (r *installFakeRunner) runGPG(command host.Command) (host.CommandResult, error) {
	joined := strings.Join(command.Args, " ")
	if strings.Contains(joined, "--dearmor") {
		return host.CommandResult{}, nil
	}
	if !strings.Contains(joined, "--show-keys") {
		return host.CommandResult{ExitCode: 1, Stderr: []byte("unsupported fake gpg command")}, nil
	}
	path := command.Args[len(command.Args)-1]
	switch {
	case strings.Contains(path, "caddy"):
		return host.CommandResult{Stdout: []byte(gpgFingerprintOutput(caddyAptKeyFingerprint))}, nil
	case strings.Contains(path, "docker"):
		return host.CommandResult{Stdout: []byte(gpgFingerprintOutput(dockerAptKeyFingerprint))}, nil
	case strings.Contains(path, "tailscale"):
		return host.CommandResult{Stdout: []byte(gpgFingerprintOutput(tailscaleAptKeyFingerprint))}, nil
	case strings.Contains(path, "cloudflare"):
		return host.CommandResult{Stdout: []byte(gpgFingerprintOutput(cloudflareAptKeyFingerprint))}, nil
	}
	return host.CommandResult{ExitCode: 1, Stderr: []byte("unknown fake gpg key")}, nil
}

func installCommandKey(command host.Command) string {
	return command.Program + " " + strings.Join(command.Args, " ")
}

func gpgFingerprintOutput(fingerprint string) string {
	return "pub:::::::::\nfpr:::::::::" + fingerprint + ":\n"
}

func (r *installFakeRunner) ranCommand(program string, args string) bool {
	for _, command := range r.commands {
		if command.Program == program && strings.Join(command.Args, " ") == args {
			return true
		}
	}
	return false
}

var _ host.Runner = (*installFakeRunner)(nil)

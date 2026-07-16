package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/provision/host"
)

func TestRunInstallWritesAndEnablesStage5Units(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{
		files: map[string]host.FileState{},
		commandResults: map[string]host.CommandResult{
			"systemctl is-enabled --quiet caddy.service":             {ExitCode: 1},
			"systemctl is-enabled --quiet ship-preview-reaper.timer": {ExitCode: 1},
			"systemctl is-enabled --quiet ship-doctor.timer":         {ExitCode: 1},
			"systemctl is-enabled --quiet ship-boot-converge.service": {ExitCode: 1},
			"systemctl is-enabled --quiet ship-gc.timer":              {ExitCode: 1},
		},
	}
	if _, err := RunInstall(context.Background(), runner, InstallOptions{
		DeploySSHPublicKeys: []string{deployTestPublicKey},
		StateRoot:           root,
		HelperBinaryPath:    helper,
	}); err != nil {
		t.Fatal(err)
	}
	boot, ok := runner.files["/etc/systemd/system/ship-boot-converge.service"]
	if !ok || !strings.Contains(string(boot.Content), "After=network-online.target podman.socket podman.service") || !strings.Contains(string(boot.Content), "ExecStart=/usr/local/bin/ship server converge-boot") || !strings.Contains(string(boot.Content), "WantedBy=multi-user.target") {
		t.Fatalf("boot unit=%+v", boot)
	}
	timer, ok := runner.files["/etc/systemd/system/ship-gc.timer"]
	if !ok || !strings.Contains(string(timer.Content), "OnUnitActiveSec=6h") || !strings.Contains(string(timer.Content), "Persistent=true") {
		t.Fatalf("GC timer=%+v", timer)
	}
	if !runner.ranCommand("systemctl", "enable ship-boot-converge.service") || !runner.ranCommand("systemctl", "enable ship-gc.timer") {
		t.Fatalf("stage5 units were not enabled: %+v", runner.commands)
	}
}


package provision

import (
	"context"
	"os"
	"path/filepath"
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

type installFakeRunner struct {
	files          map[string]host.FileState
	commands       []host.Command
	commandResults map[string]host.CommandResult
}

func (r *installFakeRunner) ReadFile(_ context.Context, path string) (host.FileState, error) {
	file, ok := r.files[path]
	if !ok {
		return host.FileState{}, host.ErrNotExist
	}
	return file, nil
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
	}
	return host.CommandResult{}, nil
}

func installCommandKey(command host.Command) string {
	return command.Program + " " + strings.Join(command.Args, " ")
}

var _ host.Runner = (*installFakeRunner)(nil)

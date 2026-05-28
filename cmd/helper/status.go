package helper

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/fprl/simple-vps/internal/host"
	"github.com/fprl/simple-vps/internal/store"
	"github.com/fprl/simple-vps/internal/utils"
)

type statusCmd struct{}

func (statusCmd) Run() error {
	CmdStatus()
	return nil
}

func toolStatus(tool string) string {
	_, err := exec.LookPath(tool)
	if err != nil {
		return "missing"
	}
	cmd := exec.Command(tool, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("installed (version check failed: exit %s)", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) > 0 && lines[0] != "" {
		return fmt.Sprintf("installed (%s)", lines[0])
	}
	return "installed"
}

func CmdStatus() {
	lines, err := statusStateLines(store.Default())
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Println("Simple VPS")
	for _, line := range lines {
		fmt.Println(line)
	}
	fmt.Println("services:")
	for _, service := range []string{"tailscaled", "cloudflared", "caddy"} {
		fmt.Printf("  %s: %s\n", service, host.SystemServiceStatus(service))
	}
	// Post-cutover host footprint per ADR-0005 §14: the helper itself
	// (on PATH, implied), Podman, Caddy, rsync.
	fmt.Println("tools:")
	for _, tool := range []string{"podman", "caddy", "rsync"} {
		fmt.Printf("  %s: %s\n", tool, toolStatus(tool))
	}
}

// statusStateLines reports the host-install state only. Per-app/per-env
// counts intentionally do not appear: the legacy apps.json / routes.json
// registers aren't written by the container deploy flow, so any count
// derived from them would be a lie. Live (app, env) inventory belongs in
// a future `status` rewrite sourced from `podman ps` labels.
func statusStateLines(stateStore store.Store) ([]string, error) {
	installed, err := stateStore.HostInstalled()
	if err != nil {
		return nil, err
	}
	if installed {
		return []string{fmt.Sprintf("state: installed (%s)", stateStore.HostPath())}, nil
	}
	return []string{fmt.Sprintf("state: not installed (missing %s)", stateStore.HostPath())}, nil
}

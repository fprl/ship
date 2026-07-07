package helper

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/fprl/simple-vps/internal/host"
	"github.com/fprl/simple-vps/internal/store"
	"github.com/fprl/simple-vps/internal/utils"
)

type statusCmd struct {
	JSON bool `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c statusCmd) Run() error {
	CmdStatus(c.JSON)
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

func CmdStatus(jsonFlag bool) {
	report, err := hostStatusReportFor(store.Default(), host.SystemServiceStatus, toolStatus)
	if err != nil {
		utils.DieError(err, 1)
	}
	if jsonFlag {
		buf, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(buf))
		return
	}
	fmt.Print(renderHostStatusText(report))
}

type hostStatusReport struct {
	State    hostStateStatus   `json:"state"`
	Services map[string]string `json:"services"`
	Tools    map[string]string `json:"tools"`
}

type hostStateStatus struct {
	Status    string `json:"status"`
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
}

func hostStatusReportFor(stateStore store.Store, serviceStatus func(string) string, toolStatus func(string) string) (hostStatusReport, error) {
	installed, err := stateStore.HostInstalled()
	if err != nil {
		return hostStatusReport{}, err
	}

	stateStatus := "not_installed"
	if installed {
		stateStatus = "installed"
	}

	report := hostStatusReport{
		State: hostStateStatus{
			Status:    stateStatus,
			Installed: installed,
			Path:      stateStore.HostPath(),
		},
		Services: map[string]string{},
		Tools:    map[string]string{},
	}
	for _, service := range []string{"tailscaled", "cloudflared", "caddy"} {
		report.Services[service] = serviceStatus(service)
	}
	// The helper itself is implied by this command running. Caddy is
	// supervised as a container service, so the host only needs Podman
	// and rsync on PATH for deploy operations.
	for _, tool := range []string{"podman", "rsync"} {
		report.Tools[tool] = toolStatus(tool)
	}
	return report, nil
}

func renderHostStatusText(report hostStatusReport) string {
	var b strings.Builder
	b.WriteString("ship\n")
	for _, line := range hostStatusStateLines(report.State) {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("services:\n")
	for _, service := range []string{"tailscaled", "cloudflared", "caddy"} {
		fmt.Fprintf(&b, "  %s: %s\n", service, report.Services[service])
	}
	b.WriteString("tools:\n")
	for _, tool := range []string{"podman", "rsync"} {
		fmt.Fprintf(&b, "  %s: %s\n", tool, report.Tools[tool])
	}
	return b.String()
}

func hostStatusStateLines(state hostStateStatus) []string {
	if state.Installed {
		return []string{fmt.Sprintf("state: installed (%s)", state.Path)}
	}
	return []string{fmt.Sprintf("state: not installed (missing %s)", state.Path)}
}

// statusStateLines reports the host-install state only. App/env inventory
// belongs to `ship app list`, which merges env identity files with
// live Podman labels.
func statusStateLines(stateStore store.Store) ([]string, error) {
	installed, err := stateStore.HostInstalled()
	if err != nil {
		return nil, err
	}
	status := "not_installed"
	if installed {
		status = "installed"
	}
	return hostStatusStateLines(hostStateStatus{
		Status:    status,
		Installed: installed,
		Path:      stateStore.HostPath(),
	}), nil
}

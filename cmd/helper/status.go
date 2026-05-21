package helper

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/fprl/simple-vps/internal/store"
	"github.com/fprl/simple-vps/internal/systemd"
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
		fmt.Printf("  %s: %s\n", service, systemd.SystemServiceStatus(service))
	}
	fmt.Println("tools:")
	for _, tool := range []string{"litestream", "node", "bun", "pnpm"} {
		fmt.Printf("  %s: %s\n", tool, toolStatus(tool))
	}
}

func statusStateLines(stateStore store.Store) ([]string, error) {
	installed, err := stateStore.HostInstalled()
	if err != nil {
		return nil, err
	}
	var lines []string
	if installed {
		lines = append(lines, fmt.Sprintf("state: installed (%s)", stateStore.HostPath()))
	} else {
		lines = append(lines, fmt.Sprintf("state: not installed (missing %s)", stateStore.HostPath()))
	}
	apps, err := stateStore.ReadApps()
	if err != nil {
		return nil, err
	}
	routes, err := stateStore.ReadRoutes()
	if err != nil {
		return nil, err
	}
	lines = append(lines, fmt.Sprintf("apps: %d", len(apps.Apps)))
	lines = append(lines, fmt.Sprintf("routes: %d", len(routes.Routes)))
	return lines, nil
}

func CmdRoutes(jsonFlag bool) {
	routes, err := store.Default().ReadRoutes()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if jsonFlag {
		type routesWrap struct {
			Routes []store.Route `json:"routes"`
		}
		data, err := json.MarshalIndent(routesWrap{Routes: routes.Routes}, "", "  ")
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		fmt.Println(string(data))
		return
	}
	if len(routes.Routes) == 0 {
		fmt.Println("No routes configured.")
		return
	}

	hostWidth := len("HOST")
	typeWidth := len("TYPE")
	targetWidth := len("TARGET")

	for _, r := range routes.Routes {
		if len(r.Host) > hostWidth {
			hostWidth = len(r.Host)
		}
		if len(r.Type) > typeWidth {
			typeWidth = len(r.Type)
		}
		target := getRouteTarget(r)
		if len(target) > targetWidth {
			targetWidth = len(target)
		}
	}

	headerFormat := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  APP\n", hostWidth, typeWidth, targetWidth)
	rowFormat := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", hostWidth, typeWidth, targetWidth)

	fmt.Printf(headerFormat, "HOST", "TYPE", "TARGET")
	for _, r := range routes.Routes {
		fmt.Printf(rowFormat, r.Host, r.Type, getRouteTarget(r), r.App)
	}
}

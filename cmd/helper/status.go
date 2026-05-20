package helper

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/fprl/simple-vps/internal/state"
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
	s, err := state.LoadState()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Println("Simple VPS")
	fmt.Printf("state: %s\n", state.StatePath())
	fmt.Printf("routes: %d\n", len(s.Routes))
	fmt.Println("services:")
	for _, service := range []string{"tailscaled", "cloudflared", "caddy"} {
		fmt.Printf("  %s: %s\n", service, systemd.SystemServiceStatus(service))
	}
	fmt.Println("tools:")
	for _, tool := range []string{"litestream", "node", "bun", "pnpm"} {
		fmt.Printf("  %s: %s\n", tool, toolStatus(tool))
	}
}

func CmdRoutes(jsonFlag bool) {
	s, err := state.LoadState()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if jsonFlag {
		type routesWrap struct {
			Routes []state.StateRoute `json:"routes"`
		}
		data, err := json.MarshalIndent(routesWrap{Routes: s.Routes}, "", "  ")
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		fmt.Println(string(data))
		return
	}
	if len(s.Routes) == 0 {
		fmt.Println("No routes configured.")
		return
	}

	hostWidth := len("HOST")
	typeWidth := len("TYPE")
	targetWidth := len("TARGET")

	for _, r := range s.Routes {
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
	for _, r := range s.Routes {
		fmt.Printf(rowFormat, r.Host, r.Type, getRouteTarget(r), r.App)
	}
}

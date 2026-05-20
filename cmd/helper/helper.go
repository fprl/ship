package helper

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/fprl/simple-vps/internal/caddy"
	"github.com/fprl/simple-vps/internal/cloudflare"
	"github.com/fprl/simple-vps/internal/state"
	"github.com/fprl/simple-vps/internal/systemd"
	"github.com/fprl/simple-vps/internal/utils"
)

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
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

func getRouteTarget(r state.StateRoute) string {
	switch r.Type {
	case "proxy":
		if r.Port != nil {
			return fmt.Sprintf("127.0.0.1:%d", *r.Port)
		}
	case "static":
		return r.Root
	case "redirect":
		return r.To
	}
	return ""
}

func parseHeaderArgs(headers []string) (map[string]string, error) {
	res := make(map[string]string)
	for _, h := range headers {
		if !strings.Contains(h, ":") {
			return nil, fmt.Errorf("invalid header %q; expected 'Name: value'", h)
		}
		parts := strings.SplitN(h, ":", 2)
		norm, err := state.NormalizeHeaders(map[string]string{parts[0]: parts[1]})
		if err != nil {
			return nil, err
		}
		for k, v := range norm {
			res[k] = v
		}
	}
	return res, nil
}

func upsertRoute(route *state.StateRoute, force bool) {
	s, err := state.LoadState()
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	prevRoutes := make([]state.StateRoute, len(s.Routes))
	copy(prevRoutes, s.Routes)

	idx := state.RouteIndex(s.Routes, route.Host)
	if idx != -1 {
		existing := s.Routes[idx]
		// Compare routes
		existingJSON, _ := json.Marshal(existing)
		routeJSON, _ := json.Marshal(route)
		if string(existingJSON) == string(routeJSON) {
			fmt.Printf("%s already has the requested %s route\n", route.Host, route.Type)
			return
		}
		if !force {
			utils.Die(fmt.Sprintf("%s already has a %s route; use --force to replace it", route.Host, existing.Type), 1)
		}
		s.Routes[idx] = *route
	} else {
		s.Routes = append(s.Routes, *route)
	}

	err = state.WriteState(s)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	_, err = caddy.ApplyCaddyfile(s, force)
	if err != nil {
		// Rollback state
		s.Routes = prevRoutes
		_ = state.WriteState(s)
		utils.Die(err.Error(), 1)
	}

	fmt.Printf("Routed %s (%s) -> %s\n", route.Host, route.Type, getRouteTarget(*route))
}

func removeRoute(host string, force bool) {
	s, err := state.LoadState()
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	prevRoutes := make([]state.StateRoute, len(s.Routes))
	copy(prevRoutes, s.Routes)

	idx := state.RouteIndex(s.Routes, host)
	if idx == -1 {
		utils.Die(fmt.Sprintf("%s is not routed", host), 1)
	}

	s.Routes = append(s.Routes[:idx], s.Routes[idx+1:]...)

	err = state.WriteState(s)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	_, err = caddy.ApplyCaddyfile(s, force)
	if err != nil {
		// Rollback state
		s.Routes = prevRoutes
		_ = state.WriteState(s)
		utils.Die(err.Error(), 1)
	}

	fmt.Printf("Removed route for %s\n", host)
}

func removeRoutesByApp(app string, force bool) {
	s, err := state.LoadState()
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	normApp, err := state.NormalizeApp(app)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	prevRoutes := make([]state.StateRoute, len(s.Routes))
	copy(prevRoutes, s.Routes)

	var nextRoutes []state.StateRoute
	for _, r := range s.Routes {
		if r.App != normApp {
			nextRoutes = append(nextRoutes, r)
		}
	}

	removedCount := len(s.Routes) - len(nextRoutes)
	if removedCount == 0 {
		fmt.Printf("No routes found for app %s\n", normApp)
		return
	}

	s.Routes = nextRoutes
	err = state.WriteState(s)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	_, err = caddy.ApplyCaddyfile(s, force)
	if err != nil {
		// Rollback state
		s.Routes = prevRoutes
		_ = state.WriteState(s)
		utils.Die(err.Error(), 1)
	}

	plural := "route"
	if removedCount > 1 {
		plural = "routes"
	}
	fmt.Printf("Removed %d %s for app %s\n", removedCount, plural, normApp)
}

func Run(command string, args []string) {
	systemd.RequireRoot()

	switch command {
	case "status":
		CmdStatus()

	case "doctor":
		CmdDoctor()

	case "routes":
		routesFlags := flag.NewFlagSet("routes", flag.ExitOnError)
		jsonFlag := routesFlags.Bool("json", false, "Output as JSON")
		_ = routesFlags.Parse(args)
		CmdRoutes(*jsonFlag)

	case "route":
		if len(args) < 1 {
			utils.Die("route requires a subcommand: list, proxy, static, redirect, remove", 1)
		}
		routeCmd := args[0]
		routeArgs := args[1:]

		switch routeCmd {
		case "list":
			listFlags := flag.NewFlagSet("route list", flag.ExitOnError)
			jsonFlag := listFlags.Bool("json", false, "Output as JSON")
			_ = listFlags.Parse(routeArgs)
			CmdRoutes(*jsonFlag)

		case "proxy":
			proxyFlags := flag.NewFlagSet("route proxy", flag.ExitOnError)
			portFlag := proxyFlags.String("port", "", "Local app port")
			appFlag := proxyFlags.String("app", "", "App name")
			forceFlag := proxyFlags.Bool("force", false, "Force replace existing route")
			var headerFlags stringSlice
			proxyFlags.Var(&headerFlags, "header", "Custom header Name: value")
			_ = proxyFlags.Parse(routeArgs)

			if proxyFlags.NArg() < 1 {
				utils.Die("route proxy requires host argument", 1)
			}
			host := proxyFlags.Arg(0)
			if *portFlag == "" {
				utils.Die("route proxy requires --port", 1)
			}

			portInt, err := strconv.Atoi(*portFlag)
			if err != nil {
				utils.Die(fmt.Sprintf("invalid port: %s", *portFlag), 1)
			}

			hdrMap, err := parseHeaderArgs(headerFlags)
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			route, err := state.NormalizeRoute(state.StateRoute{
				Host:    host,
				Type:    "proxy",
				Port:    &portInt,
				App:     *appFlag,
				Headers: hdrMap,
			})
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			upsertRoute(route, *forceFlag)

		case "static":
			staticFlags := flag.NewFlagSet("route static", flag.ExitOnError)
			rootFlag := staticFlags.String("root", "", "Static directory path")
			appFlag := staticFlags.String("app", "", "App name")
			forceFlag := staticFlags.Bool("force", false, "Force replace existing route")
			var headerFlags stringSlice
			staticFlags.Var(&headerFlags, "header", "Custom header Name: value")
			_ = staticFlags.Parse(routeArgs)

			if staticFlags.NArg() < 1 {
				utils.Die("route static requires host argument", 1)
			}
			host := staticFlags.Arg(0)
			if *rootFlag == "" {
				utils.Die("route static requires --root", 1)
			}

			hdrMap, err := parseHeaderArgs(headerFlags)
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			route, err := state.NormalizeRoute(state.StateRoute{
				Host:    host,
				Type:    "static",
				Root:    *rootFlag,
				App:     *appFlag,
				Headers: hdrMap,
			})
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			upsertRoute(route, *forceFlag)

		case "redirect":
			redirFlags := flag.NewFlagSet("route redirect", flag.ExitOnError)
			toFlag := redirFlags.String("to", "", "Target URL")
			appFlag := redirFlags.String("app", "", "App name")
			forceFlag := redirFlags.Bool("force", false, "Force replace existing route")
			_ = redirFlags.Parse(routeArgs)

			if redirFlags.NArg() < 1 {
				utils.Die("route redirect requires host argument", 1)
			}
			host := redirFlags.Arg(0)
			if *toFlag == "" {
				utils.Die("route redirect requires --to", 1)
			}

			route, err := state.NormalizeRoute(state.StateRoute{
				Host: host,
				Type: "redirect",
				To:   *toFlag,
				App:  *appFlag,
			})
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			upsertRoute(route, *forceFlag)

		case "remove":
			removeFlags := flag.NewFlagSet("route remove", flag.ExitOnError)
			appFlag := removeFlags.String("app", "", "App name")
			forceFlag := removeFlags.Bool("force", false, "Force operation")
			_ = removeFlags.Parse(routeArgs)

			hasHost := removeFlags.NArg() > 0
			hasApp := *appFlag != ""

			if hasHost == hasApp {
				utils.Die("provide exactly one of host or --app", 1)
			}

			if hasApp {
				removeRoutesByApp(*appFlag, *forceFlag)
			} else {
				host, err := state.NormalizeHost(removeFlags.Arg(0))
				if err != nil {
					utils.Die(err.Error(), 1)
				}
				removeRoute(host, *forceFlag)
			}

		default:
			utils.Die(fmt.Sprintf("unknown route command: %s", routeCmd), 1)
		}

	case "publish":
		pubFlags := flag.NewFlagSet("publish", flag.ExitOnError)
		hostFlag := pubFlags.String("host", "", "Host to route")
		portFlag := pubFlags.String("port", "", "Port to route to")
		forceFlag := pubFlags.Bool("force", false, "Force routing")
		_ = pubFlags.Parse(args)

		if *hostFlag == "" || *portFlag == "" {
			utils.Die("publish requires --host and --port", 1)
		}

		portInt, err := strconv.Atoi(*portFlag)
		if err != nil {
			utils.Die(fmt.Sprintf("invalid port: %s", *portFlag), 1)
		}

		route, err := state.NormalizeRoute(state.StateRoute{
			Host: *hostFlag,
			Type: "proxy",
			Port: &portInt,
		})
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		upsertRoute(route, *forceFlag)

	case "unpublish":
		unpubFlags := flag.NewFlagSet("unpublish", flag.ExitOnError)
		hostFlag := unpubFlags.String("host", "", "Host to unpublish")
		forceFlag := unpubFlags.Bool("force", false, "Force unpublish")
		_ = unpubFlags.Parse(args)

		if *hostFlag == "" {
			utils.Die("unpublish requires --host", 1)
		}
		host, err := state.NormalizeHost(*hostFlag)
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		removeRoute(host, *forceFlag)

	case "generate-caddy":
		genFlags := flag.NewFlagSet("generate-caddy", flag.ExitOnError)
		forceFlag := genFlags.Bool("force", false, "Force generation")
		_ = genFlags.Parse(args)

		s, err := state.LoadState()
		if err != nil {
			utils.Die(err.Error(), 1)
		}

		changed, err := caddy.ApplyCaddyfile(s, *forceFlag)
		if err != nil {
			utils.Die(err.Error(), 1)
		}

		if changed {
			fmt.Printf("Generated %s\n", caddy.CaddyfilePath())
		} else {
			fmt.Printf("%s already up to date\n", caddy.CaddyfilePath())
		}

	case "cloudflare":
		if len(args) < 1 {
			utils.Die("cloudflare requires a subcommand: setup-tunnel, publish, remove", 1)
		}
		cfCmd := args[0]
		cfArgs := args[1:]

		switch cfCmd {
		case "setup-tunnel":
			setupFlags := flag.NewFlagSet("cloudflare setup-tunnel", flag.ExitOnError)
			tokenFile := setupFlags.String("token-file", cloudflare.CloudflareApiTokenPath(), "Path to API token")
			accountId := setupFlags.String("account-id", "", "Cloudflare account ID")
			nameFlag := setupFlags.String("name", "", "Tunnel name")
			_ = setupFlags.Parse(cfArgs)

			if *nameFlag == "" {
				utils.Die("cloudflare setup-tunnel requires --name", 1)
			}

			token, err := cloudflare.ReadCloudflareApiToken(*tokenFile)
			if err != nil || token == "" {
				utils.Die(fmt.Sprintf("Cloudflare API token not found: %s", *tokenFile), 1)
			}

			accId, err := cloudflare.CloudflareAccountId(token, *accountId)
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			tunnelId, err := cloudflare.EnsureCloudflareTunnel(token, accId, *nameFlag)
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			q := url.Values{}
			res, err := cloudflare.CloudflareApiRequest(token, "GET", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/token", accId, tunnelId), nil, q)
			if err != nil {
				utils.Die("Cloudflare API did not return a tunnel token", 1)
			}
			var tunnelToken string
			if err := json.Unmarshal(res, &tunnelToken); err != nil || tunnelToken == "" {
				// sometimes result is a plain string in RawMessage
				tunnelToken = strings.Trim(string(res), "\"")
			}
			if tunnelToken == "" {
				utils.Die("Cloudflare API did not return a tunnel token", 1)
			}

			err = state.AtomicWrite(cloudflare.CloudflaredTunnelTokenPath(), []byte(tunnelToken+"\n"), 0640)
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			// Try chowning to root:cloudflared best-effort
			_ = exec.Command("chown", "root:cloudflared", cloudflare.CloudflaredTunnelTokenPath()).Run()

			cfState, err := state.LoadCloudflareState()
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			cfState.AccountId = accId
			cfState.TunnelId = tunnelId
			cfState.TunnelName = *nameFlag

			err = state.WriteCloudflareState(cfState)
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			fmt.Printf("Cloudflare tunnel ready: %s (%s)\n", *nameFlag, tunnelId)

		case "publish":
			pubFlags := flag.NewFlagSet("cloudflare publish", flag.ExitOnError)
			appFlag := pubFlags.String("app", "", "App name")
			_ = pubFlags.Parse(cfArgs)

			if pubFlags.NArg() < 1 {
				utils.Die("cloudflare publish requires host argument", 1)
			}
			host := pubFlags.Arg(0)
			if *appFlag == "" {
				utils.Die("cloudflare publish requires --app", 1)
			}

			ingress, err := cloudflare.NewCloudflareIngress()
			if err != nil {
				if errors.Is(err, cloudflare.ErrNotConfigured) {
					fmt.Println(strings.Join([]string{
						"Cloudflare API publishing is not configured; configure this hostname in Cloudflare:",
						fmt.Sprintf("  public hostname: %s", host),
						"  service: http://127.0.0.1:8080",
						"Local Caddy route publishing will continue.",
					}, "\n"))
					return
				}
				utils.Die(err.Error(), 1)
			}

			msg, err := ingress.Publish(host, *appFlag)
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Println(msg)

		case "remove":
			remFlags := flag.NewFlagSet("cloudflare remove", flag.ExitOnError)
			appFlag := remFlags.String("app", "", "App name")
			_ = remFlags.Parse(cfArgs)

			hasHost := remFlags.NArg() > 0
			hasApp := *appFlag != ""

			if hasHost == hasApp {
				utils.Die("provide exactly one of host or --app", 1)
			}

			ingress, err := cloudflare.NewCloudflareIngress()
			if err != nil {
				if errors.Is(err, cloudflare.ErrNotConfigured) {
					fmt.Println("Cloudflare API publishing is not configured; no Cloudflare routes to remove.")
					return
				}
				utils.Die(err.Error(), 1)
			}

			var hostArg string
			if hasHost {
				hostArg = remFlags.Arg(0)
			}

			removed, err := ingress.Remove(hostArg, *appFlag)
			if err != nil {
				utils.Die(err.Error(), 1)
			}

			if len(removed) == 0 {
				fmt.Println("No Cloudflare routes matched")
				return
			}

			for _, h := range removed {
				fmt.Printf("Removed Cloudflare route: %s\n", h)
			}

		default:
			utils.Die(fmt.Sprintf("unknown cloudflare command: %s", cfCmd), 1)
		}

	case "app":
		if len(args) < 1 {
			utils.Die("app requires a subcommand: create, destroy, read-env, install-env, install-unit, uninstall-unit, daemon-reload, service, run-as", 1)
		}
		appCmd := args[0]
		appArgs := args[1:]

		switch appCmd {
		case "create":
			if len(appArgs) < 1 {
				utils.Die("app create requires name argument", 1)
			}
			name := appArgs[0]
			err := systemd.AppCreate(name)
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Printf("App %s is ready at %s\n", name, systemd.AppPath(name))

		case "destroy":
			if len(appArgs) < 1 {
				utils.Die("app destroy requires name argument", 1)
			}
			name := appArgs[0]
			err := systemd.AppDestroy(name)
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Printf("Removed app %s\n", name)

		case "read-env":
			if len(appArgs) < 1 {
				utils.Die("app read-env requires name argument", 1)
			}
			name := appArgs[0]
			content, err := systemd.AppReadEnv(name)
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Print(content)

		case "install-env":
			if len(appArgs) < 2 {
				utils.Die("app install-env requires name and path_to_env_file arguments", 1)
			}
			name := appArgs[0]
			envPath := appArgs[1]
			err := systemd.AppInstallEnv(name, envPath)
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Printf("Installed env for %s\n", name)

		case "install-unit":
			if len(appArgs) < 3 {
				utils.Die("app install-unit requires name, service, and path_to_unit_file arguments", 1)
			}
			name := appArgs[0]
			service := appArgs[1]
			unitPath := appArgs[2]
			err := systemd.AppInstallUnit(name, service, unitPath)
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Printf("Installed %s\n", systemd.ServiceUnitName(name, service))

		case "uninstall-unit":
			if len(appArgs) < 2 {
				utils.Die("app uninstall-unit requires name and service arguments", 1)
			}
			name := appArgs[0]
			service := appArgs[1]
			err := systemd.AppUninstallUnit(name, service)
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Printf("Removed %s\n", systemd.ServiceUnitName(name, service))

		case "daemon-reload":
			err := systemd.AppDaemonReload()
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Println("Reloaded systemd")

		case "service":
			if len(appArgs) < 3 {
				utils.Die("app service requires action, name, and service arguments", 1)
			}
			action := appArgs[0]
			name := appArgs[1]
			service := appArgs[2]
			output, err := systemd.AppServiceAction(action, name, service)
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					fmt.Print(output)
					os.Exit(exitErr.ExitCode())
				}
				utils.Die(err.Error(), 1)
			}
			if output != "" {
				fmt.Print(output)
			}

		case "run-as":
			if len(appArgs) < 1 {
				utils.Die("app run-as requires name argument", 1)
			}
			name := appArgs[0]
			remArgs := appArgs[1:]

			var cwdVal string
			if len(remArgs) >= 2 && remArgs[0] == "--cwd" {
				cwdVal = remArgs[1]
				remArgs = remArgs[2:]
			}

			if len(remArgs) == 0 {
				utils.Die("missing command to run", 1)
			}

			err := systemd.AppRunAs(name, cwdVal, remArgs)
			if err != nil {
				utils.Die(err.Error(), 1)
			}

		default:
			utils.Die(fmt.Sprintf("unknown app command: %s", appCmd), 1)
		}

	default:
		utils.Die(fmt.Sprintf("unknown command: %s", command), 1)
	}
}

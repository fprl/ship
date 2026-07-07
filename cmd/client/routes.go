package client

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/errcat"
	"github.com/pelletier/go-toml/v2"
)

type deployRouteOptions struct {
	Preview bool
	TLS     string
	BoxIP   string
}

type deployRoutePlan struct {
	Context            *config.AppContext
	RewritesManifest   bool
	NoConfiguredDomain bool
}

func prepareDeployRoutes(ctx *config.AppContext, envName string, opts deployRouteOptions) (deployRoutePlan, error) {
	tlsMode := strings.TrimSpace(opts.TLS)
	if tlsMode == "auto" {
		tlsMode = ""
	}

	hadConfiguredRoutes := hasConfiguredRouteHost(ctx.Routes)
	routes := cloneRoutes(ctx.Routes)
	rewritten := false
	noConfiguredDomain := false

	if opts.Preview {
		if hadConfiguredRoutes {
			collapsed, err := previewCollapsedRoutes(ctx.Routes, envName, opts.BoxIP)
			if err != nil {
				return deployRoutePlan{}, err
			}
			routes = collapsed
			rewritten = true
		} else {
			synth, err := synthesizedRoutes(ctx, envName, opts.BoxIP)
			if err != nil {
				return deployRoutePlan{}, err
			}
			routes = synth
			rewritten = true
			noConfiguredDomain = true
		}
	} else if !hadConfiguredRoutes {
		synth, err := synthesizedRoutes(ctx, envName, opts.BoxIP)
		if err != nil {
			return deployRoutePlan{}, err
		}
		routes = synth
		rewritten = true
		noConfiguredDomain = true
	}

	if tlsMode != "" {
		routes = routesWithTLS(routes, tlsMode)
		rewritten = true
	}

	return deployRoutePlan{
		Context:            cloneAppContextWithRoutes(ctx, routes),
		RewritesManifest:   rewritten,
		NoConfiguredDomain: noConfiguredDomain,
	}, nil
}

func hasConfiguredRouteHost(routes map[string]config.Route) bool {
	for _, route := range routes {
		if route.Host != "" {
			return true
		}
	}
	return false
}

func synthesizedRoutes(ctx *config.AppContext, envName, boxIP string) (map[string]config.Route, error) {
	process, err := synthesizedRouteProcess(ctx.Processes)
	if err != nil {
		return nil, err
	}
	host := sslipHost(envName, boxIP)
	return map[string]config.Route{
		host: {Host: host, Process: process},
	}, nil
}

func synthesizedRouteProcess(processes map[string]config.Process) (string, error) {
	if _, ok := processes["web"]; ok {
		return "web", nil
	}
	if len(processes) == 1 {
		for name := range processes {
			return name, nil
		}
	}
	if len(processes) > 1 {
		return "", errcat.New(errcat.CodeMultiProcessNoWebRoute, nil)
	}
	return "", errcat.New(errcat.CodeManifestInvalid, errcat.Fields{
		"details": "manifest must declare a routed process when [routes] is empty",
		"command": "fix ship.toml",
	})
}

func previewCollapsedRoutes(routes map[string]config.Route, envName, boxIP string) (map[string]config.Route, error) {
	defaultHost := defaultRouteHost(routes)
	if defaultHost == "" {
		synthHost := sslipHost(envName, boxIP)
		return nil, fmt.Errorf("preview routes cannot be collapsed because [routes] has no non-redirect default host; add a process or static route for %s", synthHost)
	}
	// v1 previews always use sslip because the manifest has no wildcard-base knob.
	// TODO(§3): add an explicit wildcard-base config field and use it here.
	previewHost := sslipHost(envName, boxIP)
	out := map[string]config.Route{}
	for _, name := range sortedRouteNames(routes) {
		route := routes[name]
		if route.Host != defaultHost || route.Redirect != "" {
			continue
		}
		route.Host = previewHost
		key := route.Host + route.Path
		out[key] = route
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("preview routes cannot be collapsed because default host %s has only redirects", defaultHost)
	}
	return out, nil
}

func defaultRouteHost(routes map[string]config.Route) string {
	type candidate struct {
		rank int
		host string
	}
	var candidates []candidate
	for _, route := range routes {
		if route.Host == "" || route.Redirect != "" {
			continue
		}
		rank := 3
		switch {
		case route.Process == "web" && route.Path == "":
			rank = 0
		case route.Path == "":
			rank = 1
		case route.Process == "web":
			rank = 2
		}
		candidates = append(candidates, candidate{rank: rank, host: route.Host})
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].host < candidates[j].host
	})
	return candidates[0].host
}

func routesWithTLS(routes map[string]config.Route, tlsMode string) map[string]config.Route {
	out := make(map[string]config.Route, len(routes))
	for name, route := range routes {
		route.TLS = tlsMode
		out[name] = route
	}
	return out
}

func cloneRoutes(routes map[string]config.Route) map[string]config.Route {
	out := make(map[string]config.Route, len(routes))
	for name, route := range routes {
		out[name] = route
	}
	return out
}

func cloneAppContextWithRoutes(ctx *config.AppContext, routes map[string]config.Route) *config.AppContext {
	next := *ctx
	next.Routes = routes
	next.HasStaticRoutes = false
	for _, route := range routes {
		if route.Serve != "" {
			next.HasStaticRoutes = true
			break
		}
	}
	return &next
}

func writeDeployManifest(src, dst string, routes map[string]config.Route) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse manifest for route overlay: %v", err)
	}
	raw["routes"] = tomlRoutes(routes)
	encoded, err := toml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encode manifest route overlay: %v", err)
	}
	return os.WriteFile(dst, encoded, 0644)
}

func tomlRoutes(routes map[string]config.Route) map[string]any {
	out := map[string]any{}
	for _, name := range sortedRouteNames(routes) {
		route := routes[name]
		key := route.Host + route.Path
		switch {
		case route.Process != "" && route.TLS == "":
			out[key] = route.Process
		case route.Process != "":
			out[key] = map[string]any{"process": route.Process, "tls": route.TLS}
		case route.Serve != "":
			entry := map[string]any{"static": route.Serve}
			if route.TLS != "" {
				entry["tls"] = route.TLS
			}
			out[key] = entry
		case route.Redirect != "":
			entry := map[string]any{"redirect": route.Redirect}
			if route.TLS != "" {
				entry["tls"] = route.TLS
			}
			out[key] = entry
		}
	}
	return out
}

func sortedRouteNames(routes map[string]config.Route) []string {
	names := make([]string, 0, len(routes))
	for name := range routes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sslipHost(envName, boxIP string) string {
	if boxIP == "" {
		boxIP = "127.0.0.1"
	}
	return envName + "." + strings.ReplaceAll(boxIP, ".", "-") + ".sslip.io"
}

func resolveBoxIPv4(runner sshRunner, server string) string {
	host := boxHost(server)
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	if ips, err := net.LookupIP(host); err == nil {
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	command := "hostname -I 2>/dev/null | tr ' ' '\\n' | sed -n '/^[0-9][0-9.]*$/p' | head -n1"
	if out, err := runSSHDetail(runner, server, command); err == nil {
		for _, field := range strings.Fields(out) {
			if ip := net.ParseIP(field); ip != nil {
				if v4 := ip.To4(); v4 != nil {
					return v4.String()
				}
			}
		}
	}
	return "127.0.0.1"
}

func warnRouteDNSPreflight(ctx *config.AppContext, boxIP string) {
	if boxIP == "" {
		return
	}
	seen := map[string]bool{}
	for _, name := range sortedRouteNames(ctx.Routes) {
		host := ctx.Routes[name].Host
		if host == "" || strings.HasSuffix(host, ".sslip.io") || seen[host] {
			continue
		}
		seen[host] = true
		if hostResolvesToIPv4(host, boxIP) {
			continue
		}
		fmt.Fprintf(os.Stderr, "warning: A %s → %s\n", host, boxIP)
	}
}

func hostResolvesToIPv4(host, want string) bool {
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil && v4.String() == want {
			return true
		}
	}
	return false
}

func prodNoDomainNextLine(boxIP string) string {
	if boxIP == "" {
		boxIP = "127.0.0.1"
	}
	return fmt.Sprintf("next: add DNS A <your-domain> → %s and add it under [routes]", boxIP)
}

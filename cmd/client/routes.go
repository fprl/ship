package client

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/pelletier/go-toml/v2"
)

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
		case route.Process != "":
			out[key] = route.Process
		case route.Serve != "":
			entry := map[string]any{"static": route.Serve}
			out[key] = entry
		case route.Redirect != "":
			entry := map[string]any{"redirect": route.Redirect}
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
	if runner == nil {
		return "127.0.0.1"
	}
	command := "hostname -I 2>/dev/null | tr ' ' '\\n' | sed -n '/^[0-9][0-9.]*$/p' | head -n1"
	if out, err := runSSHDetail(runner, server, command, "ship box doctor "+server); err == nil {
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

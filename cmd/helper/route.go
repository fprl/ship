package helper

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fprl/simple-vps/internal/caddy"
	"github.com/fprl/simple-vps/internal/state"
	"github.com/fprl/simple-vps/internal/utils"
)

type routeCmd struct {
	List     routeListCmd     `cmd:"" help:"List configured routes."`
	Proxy    routeProxyCmd    `cmd:"" help:"Create or replace a proxy route."`
	Static   routeStaticCmd   `cmd:"" help:"Create or replace a static route."`
	Redirect routeRedirectCmd `cmd:"" help:"Create or replace a redirect route."`
	Remove   routeRemoveCmd   `cmd:"" help:"Remove routes by host or app."`
}

type routeListCmd struct {
	JSON bool `name:"json" help:"Output JSON."`
}

func (c routeListCmd) Run() error {
	CmdRoutes(c.JSON)
	return nil
}

type routesCompatCmd struct {
	JSON bool `name:"json" help:"Output JSON."`
}

func (c routesCompatCmd) Run() error {
	CmdRoutes(c.JSON)
	return nil
}

type routeProxyCmd struct {
	Host    string   `arg:"" help:"Route hostname."`
	Port    int      `required:"" help:"Local app port."`
	App     string   `help:"App name."`
	Force   bool     `help:"Force replace existing route."`
	Headers []string `name:"header" help:"Custom header Name: value."`
}

func (c routeProxyCmd) Run() error {
	hdrMap, err := parseHeaderArgs(c.Headers)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	route, err := state.NormalizeRoute(state.StateRoute{
		Host:    c.Host,
		Type:    "proxy",
		Port:    &c.Port,
		App:     c.App,
		Headers: hdrMap,
	})
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	upsertRoute(route, c.Force)
	return nil
}

type routeStaticCmd struct {
	Host    string   `arg:"" help:"Route hostname."`
	Root    string   `required:"" help:"Static directory path."`
	App     string   `help:"App name."`
	Force   bool     `help:"Force replace existing route."`
	Headers []string `name:"header" help:"Custom header Name: value."`
}

func (c routeStaticCmd) Run() error {
	hdrMap, err := parseHeaderArgs(c.Headers)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	route, err := state.NormalizeRoute(state.StateRoute{
		Host:    c.Host,
		Type:    "static",
		Root:    c.Root,
		App:     c.App,
		Headers: hdrMap,
	})
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	upsertRoute(route, c.Force)
	return nil
}

type routeRedirectCmd struct {
	Host  string `arg:"" help:"Route hostname."`
	To    string `required:"" help:"Target URL."`
	App   string `help:"App name."`
	Force bool   `help:"Force replace existing route."`
}

func (c routeRedirectCmd) Run() error {
	route, err := state.NormalizeRoute(state.StateRoute{
		Host: c.Host,
		Type: "redirect",
		To:   c.To,
		App:  c.App,
	})
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	upsertRoute(route, c.Force)
	return nil
}

type routeRemoveCmd struct {
	Host  string `arg:"" optional:"" help:"Route hostname."`
	App   string `help:"App name."`
	Force bool   `help:"Force operation."`
}

func (c routeRemoveCmd) Run() error {
	hasHost := c.Host != ""
	hasApp := c.App != ""

	if hasHost == hasApp {
		utils.Die("provide exactly one of host or --app", 1)
	}

	if hasApp {
		removeRoutesByApp(c.App, c.Force)
		return nil
	}

	host, err := state.NormalizeHost(c.Host)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	removeRoute(host, c.Force)
	return nil
}

type publishCompatCmd struct {
	Host  string `required:"" help:"Host to route."`
	Port  int    `required:"" help:"Port to route to."`
	Force bool   `help:"Force routing."`
}

func (c publishCompatCmd) Run() error {
	route, err := state.NormalizeRoute(state.StateRoute{
		Host: c.Host,
		Type: "proxy",
		Port: &c.Port,
	})
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	upsertRoute(route, c.Force)
	return nil
}

type unpublishCompatCmd struct {
	Host  string `required:"" help:"Host to unpublish."`
	Force bool   `help:"Force unpublish."`
}

func (c unpublishCompatCmd) Run() error {
	host, err := state.NormalizeHost(c.Host)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	removeRoute(host, c.Force)
	return nil
}

type generateCaddyCmd struct {
	Force bool `help:"Force generation."`
}

func (c generateCaddyCmd) Run() error {
	s, err := state.LoadState()
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	changed, err := caddy.ApplyCaddyfile(s, c.Force)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	if changed {
		fmt.Printf("Generated %s\n", caddy.CaddyfilePath())
	} else {
		fmt.Printf("%s already up to date\n", caddy.CaddyfilePath())
	}
	return nil
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

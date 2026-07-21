// Package addressing owns Ship's route plan and primary deployment URL.
package addressing

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/names"
)

type Options struct {
	Preview bool
	TLS     string
	BoxIP   string
}

type Plan struct {
	Context            *config.AppContext
	RewritesManifest   bool
	NoConfiguredDomain bool
	PreviewAlias       string
	PrimaryURL         string
}

func PlanRoutes(ctx *config.AppContext, env string, opts Options) (Plan, error) {
	tlsMode := strings.TrimSpace(opts.TLS)
	if tlsMode == "auto" {
		tlsMode = ""
	}
	hadConfiguredRoutes := hasHost(ctx.Routes)
	routes := cloneRoutes(ctx.Routes)
	rewritten, noDomain := false, false
	if opts.Preview {
		base := PreviewBase(ctx, opts.BoxIP)
		var err error
		if hadConfiguredRoutes {
			routes, err = collapsePreview(ctx, env, base)
		} else {
			routes, err = synthesize(ctx, env, base)
			noDomain = true
		}
		if err != nil {
			return Plan{}, err
		}
		rewritten = true
	} else if !hadConfiguredRoutes {
		var err error
		routes, err = synthesize(ctx, env, SSLIPBase(opts.BoxIP))
		if err != nil {
			return Plan{}, err
		}
		rewritten, noDomain = true, true
	}
	if tlsMode != "" {
		for name, route := range routes {
			route.TLS = tlsMode
			routes[name] = route
		}
		rewritten = true
	}
	next := cloneContext(ctx, routes)
	primary, _ := PrimaryURL(routes, true)
	plan := Plan{Context: next, RewritesManifest: rewritten, NoConfiguredDomain: noDomain, PrimaryURL: primary}
	if opts.Preview && ctx.Preview.Aliases {
		plan.PreviewAlias = names.PreviewBranchSlug(env) + "." + PreviewBase(ctx, opts.BoxIP)
	}
	return plan, nil
}

// PrimaryURL deterministically selects the URL used by CLI output and the
// SHIP_URL runtime variable. Redirects may be excluded when selecting the
// default host that preview routes collapse onto.
func PrimaryURL(routes map[string]config.Route, includeRedirects bool) (string, bool) {
	type candidate struct {
		rank int
		url  string
	}
	var candidates []candidate
	for _, route := range routes {
		if route.Host == "" || (!includeRedirects && route.Redirect != "") {
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
		candidates = append(candidates, candidate{rank: rank, url: "https://" + route.Host + route.Path})
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].url < candidates[j].url
	})
	return candidates[0].url, true
}

func URL(ctx *config.AppContext, env, boxIP string) string {
	if primary, ok := PrimaryURL(ctx.Routes, true); ok {
		return primary
	}
	base := SSLIPBase(boxIP)
	if env != names.ProductionEnvName {
		base = PreviewBase(ctx, boxIP)
	}
	return "https://" + SynthesizedHost(ctx.AppName, env, base)
}

func SSLIPBase(boxIP string) string {
	if boxIP == "" {
		boxIP = "127.0.0.1"
	}
	return strings.ReplaceAll(boxIP, ".", "-") + ".sslip.io"
}

func PreviewBase(ctx *config.AppContext, boxIP string) string {
	if ctx.Preview.Base != "" {
		return ctx.Preview.Base
	}
	return SSLIPBase(boxIP)
}

func SynthesizedHost(app, env, base string) string {
	return names.SynthesizedHostLabel(app, env) + "." + base
}

func collapsePreview(ctx *config.AppContext, env, base string) (map[string]config.Route, error) {
	primary, ok := PrimaryURL(ctx.Routes, false)
	if !ok {
		host := SynthesizedHost(ctx.AppName, env, base)
		return nil, invalid(fmt.Sprintf("preview routes cannot be collapsed because [routes] has no non-redirect default host; add a process or static route for %s", host))
	}
	defaultHost := strings.SplitN(strings.TrimPrefix(primary, "https://"), "/", 2)[0]
	previewHost := SynthesizedHost(ctx.AppName, env, base)
	out := map[string]config.Route{}
	for _, name := range sortedNames(ctx.Routes) {
		route := ctx.Routes[name]
		if route.Host != defaultHost || route.Redirect != "" {
			continue
		}
		route.Host = previewHost
		out[route.Host+route.Path] = route
	}
	if len(out) == 0 {
		return nil, invalid(fmt.Sprintf("preview routes cannot be collapsed because default host %s has only redirects", defaultHost))
	}
	return out, nil
}

func synthesize(ctx *config.AppContext, env, base string) (map[string]config.Route, error) {
	process := ""
	if _, ok := ctx.Processes["web"]; ok {
		process = "web"
	} else if len(ctx.Processes) == 1 {
		for name := range ctx.Processes {
			process = name
		}
	} else if len(ctx.Processes) > 1 {
		return nil, errcat.New(errcat.CodeMultiProcessNoWebRoute, nil)
	} else {
		return nil, errcat.New(errcat.CodeManifestInvalid, errcat.Fields{
			"details": "manifest must declare a routed process when [routes] is empty",
			"command": "add a [routes] entry or a web process to ship.toml, then ship",
		})
	}
	host := SynthesizedHost(ctx.AppName, env, base)
	return map[string]config.Route{host: {Host: host, Process: process}}, nil
}

func invalid(detail string) error {
	return errcat.New(errcat.CodeManifestInvalid, errcat.Fields{"details": detail, "command": "fix the [routes] default host in ship.toml, then ship"})
}

func hasHost(routes map[string]config.Route) bool {
	for _, route := range routes {
		if route.Host != "" {
			return true
		}
	}
	return false
}

func cloneRoutes(routes map[string]config.Route) map[string]config.Route {
	out := make(map[string]config.Route, len(routes))
	for name, route := range routes {
		out[name] = route
	}
	return out
}

func cloneContext(ctx *config.AppContext, routes map[string]config.Route) *config.AppContext {
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

func sortedNames(routes map[string]config.Route) []string {
	out := make([]string, 0, len(routes))
	for name := range routes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

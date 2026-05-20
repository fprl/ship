package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var (
	AppRe        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	ServiceRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	HeaderNameRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
	SystemUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}\$?$`)
)

type Build struct {
	Command string   `toml:"command"`
	Output  string   `toml:"output"`
	Include []string `toml:"include"`
	Install *bool    `toml:"install"`
}

type Service struct {
	Command            string `toml:"command"`
	Port               *int   `toml:"port"`
	Healthcheck        string `toml:"healthcheck"`
	HealthcheckStatus  *int   `toml:"healthcheck_status"`
	HealthcheckTimeout *int   `toml:"healthcheck_timeout"`
}

type Route struct {
	Host    string   `toml:"host"`
	Type    string   `toml:"type"`
	Service string   `toml:"service"`
	Root    string   `toml:"root"`
	To      string   `toml:"to"`
	Headers []string `toml:"headers"`
}

type EnvBlock struct {
	Server       string             `toml:"server"`
	Runtime      string             `toml:"runtime"`
	Path         *string            `toml:"path"`
	KeepReleases *int               `toml:"keep_releases"`
	Build        *Build             `toml:"build"`
	Services     map[string]Service `toml:"services"`
	Routes       map[string]Route   `toml:"routes"`
}

type Manifest struct {
	Name     string              `toml:"name"`
	Build    *Build              `toml:"build"`
	Services map[string]Service  `toml:"services"`
	Routes   map[string]Route    `toml:"routes"`
	Env      map[string]EnvBlock `toml:"env"`
}

type AppContext struct {
	AppName   string
	EnvName   string
	Server    string
	AppRoot   string
	Runtime   string
	Build     *Build
	Services  map[string]Service
	Routes    map[string]Route
	Lockfiles []string
}

// Validation helpers

func ValidateHost(host string) bool {
	host = strings.ToLower(host)
	host = strings.TrimSuffix(host, ".")
	if len(host) < 1 || len(host) > 253 {
		return false
	}
	parts := strings.Split(host, ".")
	for _, part := range parts {
		if len(part) < 1 || len(part) > 63 {
			return false
		}
		if part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, ch := range part {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return false
			}
		}
	}
	return true
}

func ValidateSshTarget(target string) bool {
	if strings.HasPrefix(target, "-") {
		return false
	}
	if !strings.Contains(target, "@") {
		return ValidateHost(target)
	}
	parts := strings.SplitN(target, "@", 2)
	user := parts[0]
	host := parts[1]
	return SystemUserRe.MatchString(user) && ValidateHost(host)
}

func ReadManifest(root string) (*Manifest, error) {
	path := filepath.Join(root, "simple-vps.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("simple-vps.toml not found")
	}
	var manifest Manifest
	err = toml.Unmarshal(data, &manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to parse simple-vps.toml: %w", err)
	}
	return &manifest, nil
}

func GetLockfiles(root string) []string {
	var found []string
	known := []string{"bun.lock", "bun.lockb", "pnpm-lock.yaml", "package-lock.json", "yarn.lock"}
	for _, file := range known {
		if _, err := os.Stat(filepath.Join(root, file)); err == nil {
			found = append(found, file)
		}
	}
	return found
}

func CheckManifest(root string, envName string) ([]string, []string, error) {
	manifest, err := ReadManifest(root)
	if err != nil {
		return nil, nil, err
	}

	var errors []string
	var warnings []string

	if manifest.Name == "" {
		errors = append(errors, "name is required")
	} else if !AppRe.MatchString(manifest.Name) {
		errors = append(errors, "name must match ^[a-z][a-z0-9-]{1,40}$")
	}

	if len(manifest.Env) == 0 {
		errors = append(errors, "at least one [env.<name>] block is required")
		return errors, warnings, nil
	}

	var envNames []string
	for k := range manifest.Env {
		envNames = append(envNames, k)
	}

	if envName != "" {
		if _, ok := manifest.Env[envName]; !ok {
			errors = append(errors, fmt.Sprintf("env not found: %s", envName))
			return errors, warnings, nil
		}
	}

	selectedEnvNames := envNames
	if envName != "" {
		selectedEnvNames = []string{envName}
	}

	locks := GetLockfiles(root)
	if len(locks) > 1 {
		errors = append(errors, fmt.Sprintf("multiple lockfiles found: %s", strings.Join(locks, ", ")))
	}

	for _, selected := range selectedEnvNames {
		envBlock := manifest.Env[selected]
		if !ServiceRe.MatchString(selected) {
			errors = append(errors, fmt.Sprintf("invalid env name: %s", selected))
		}

		if envBlock.Server == "" {
			errors = append(errors, fmt.Sprintf("[env.%s].server is required", selected))
		} else if !ValidateSshTarget(envBlock.Server) {
			errors = append(errors, fmt.Sprintf("[env.%s].server must be an SSH target like deploy@example.com", selected))
		}

		if envBlock.Path != nil {
			p := *envBlock.Path
			if p == "" {
				errors = append(errors, fmt.Sprintf("[env.%s].path must be a non-empty string when present", selected))
			} else if !strings.HasPrefix(p, "/") {
				errors = append(errors, fmt.Sprintf("[env.%s].path must be absolute", selected))
			} else if manifest.Name == "" || !AppRe.MatchString(manifest.Name) {
				errors = append(errors, fmt.Sprintf("[env.%s].path requires a valid top-level name", selected))
			} else {
				expected := "/var/apps/" + manifest.Name
				if p != expected {
					errors = append(errors, fmt.Sprintf("[env.%s].path must be %s", selected, expected))
				}
			}
		}

		if envBlock.Runtime == "" {
			errors = append(errors, fmt.Sprintf("[env.%s].runtime is required", selected))
		} else if envBlock.Runtime != "bun" && envBlock.Runtime != "node" && envBlock.Runtime != "static" {
			errors = append(errors, fmt.Sprintf("[env.%s].runtime must be bun, node, or static", selected))
		}

		if envBlock.KeepReleases != nil {
			if *envBlock.KeepReleases < 1 {
				errors = append(errors, fmt.Sprintf("[env.%s].keep_releases must be a positive integer", selected))
			}
		}

		// Merge and validate build
		mergedBuild := mergeBuild(manifest.Build, envBlock.Build)
		validateBuild(mergedBuild, root, selected, &errors)

		// Merge and validate services
		mergedServices := mergeServices(manifest.Services, envBlock.Services)
		validateServices(mergedServices, envBlock.Runtime, selected, &errors)

		// Merge and validate routes
		mergedRoutes := mergeRoutes(manifest.Routes, envBlock.Routes)
		validateRoutes(mergedRoutes, mergedServices, selected, &errors)

		// Check lockfile necessity
		if isInstallNeeded(envBlock.Runtime, mergedBuild) && len(locks) == 0 {
			errors = append(errors, fmt.Sprintf("no lockfile found for env %s", selected))
		}
	}

	return errors, warnings, nil
}

func LoadAppContext(root string, envName string) (*AppContext, error) {
	manifest, err := ReadManifest(root)
	if err != nil {
		return nil, err
	}
	errors, _, err := CheckManifest(root, envName)
	if err != nil {
		return nil, err
	}
	if len(errors) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errors, "\n"))
	}

	envBlock, ok := manifest.Env[envName]
	if !ok {
		return nil, fmt.Errorf("env not found: %s", envName)
	}

	appRoot := "/var/apps/" + manifest.Name
	if envBlock.Path != nil && *envBlock.Path != "" {
		appRoot = *envBlock.Path
	}

	return &AppContext{
		AppName:   manifest.Name,
		EnvName:   envName,
		Server:    envBlock.Server,
		AppRoot:   appRoot,
		Runtime:   envBlock.Runtime,
		Build:     mergeBuild(manifest.Build, envBlock.Build),
		Services:  mergeServices(manifest.Services, envBlock.Services),
		Routes:    mergeRoutes(manifest.Routes, envBlock.Routes),
		Lockfiles: GetLockfiles(root),
	}, nil
}

// Merge helpers

func mergeBuild(base *Build, override *Build) *Build {
	if base == nil && override == nil {
		return nil
	}
	res := &Build{}
	if base != nil {
		res.Command = base.Command
		res.Output = base.Output
		res.Include = append([]string(nil), base.Include...)
		res.Install = base.Install
	}
	if override != nil {
		if override.Command != "" {
			res.Command = override.Command
		}
		if override.Output != "" {
			res.Output = override.Output
		}
		if len(override.Include) > 0 {
			res.Include = append([]string(nil), override.Include...)
		}
		if override.Install != nil {
			res.Install = override.Install
		}
	}
	return res
}

func mergeServices(base map[string]Service, override map[string]Service) map[string]Service {
	res := make(map[string]Service)
	for k, v := range base {
		res[k] = v
	}
	for k, v := range override {
		existing, ok := res[k]
		if !ok {
			res[k] = v
			continue
		}
		if v.Command != "" {
			existing.Command = v.Command
		}
		if v.Port != nil {
			existing.Port = v.Port
		}
		if v.Healthcheck != "" {
			existing.Healthcheck = v.Healthcheck
		}
		if v.HealthcheckStatus != nil {
			existing.HealthcheckStatus = v.HealthcheckStatus
		}
		if v.HealthcheckTimeout != nil {
			existing.HealthcheckTimeout = v.HealthcheckTimeout
		}
		res[k] = existing
	}
	return res
}

func mergeRoutes(base map[string]Route, override map[string]Route) map[string]Route {
	res := make(map[string]Route)
	for k, v := range base {
		res[k] = v
	}
	for k, v := range override {
		existing, ok := res[k]
		if !ok {
			res[k] = v
			continue
		}
		if v.Host != "" {
			existing.Host = v.Host
		}
		if v.Type != "" {
			existing.Type = v.Type
		}
		if v.Service != "" {
			existing.Service = v.Service
		}
		if v.Root != "" {
			existing.Root = v.Root
		}
		if v.To != "" {
			existing.To = v.To
		}
		if len(v.Headers) > 0 {
			existing.Headers = append([]string(nil), v.Headers...)
		}
		res[k] = existing
	}
	return res
}

func validateRelativePath(path string, label string, errors *[]string) {
	if path == "" {
		*errors = append(*errors, fmt.Sprintf("%s must be a non-empty string", label))
		return
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.ContainsAny(path, "*?[]{}") {
		*errors = append(*errors, fmt.Sprintf("%s must be a relative path without '..' or globs", label))
	}
}

func validateBuild(build *Build, root string, env string, errors *[]string) {
	if build == nil {
		return
	}
	if build.Command == "" {
		*errors = append(*errors, "[build].command is required when [build] is present")
	}
	validateRelativePath(build.Output, "[build].output", errors)

	for i, entry := range build.Include {
		label := fmt.Sprintf("[build].include[%d]", i)
		validateRelativePath(entry, label, errors)
		if entry != "" && !strings.HasPrefix(entry, "/") && !strings.Contains(entry, "..") {
			if _, err := os.Stat(filepath.Join(root, entry)); err != nil {
				*errors = append(*errors, fmt.Sprintf("%s does not exist: %s", label, entry))
			}
		}
	}
}

func validateServices(services map[string]Service, runtime string, env string, errors *[]string) {
	ports := make(map[int]string)
	if runtime == "static" && len(services) > 0 {
		*errors = append(*errors, `runtime = "static" cannot declare services`)
	}

	reserved := map[string]bool{"current": true, "releases": true, "shared": true}

	for name, svc := range services {
		if !ServiceRe.MatchString(name) {
			*errors = append(*errors, fmt.Sprintf("invalid service name: %s", name))
		}
		if reserved[name] {
			*errors = append(*errors, fmt.Sprintf("reserved service name: %s", name))
		}
		if svc.Command == "" {
			*errors = append(*errors, fmt.Sprintf("[services.%s].command is required", name))
		}
		if svc.Port != nil {
			port := *svc.Port
			if port < 1 || port > 65535 {
				*errors = append(*errors, fmt.Sprintf("[services.%s].port must be an integer in [1, 65535]", name))
			} else if existing, ok := ports[port]; ok {
				*errors = append(*errors, fmt.Sprintf("[services.%s].port duplicates [services.%s].port", name, existing))
			} else {
				ports[port] = name
			}
			if svc.Healthcheck == "" {
				*errors = append(*errors, fmt.Sprintf("[services.%s].healthcheck is required when port is set", name))
			}
		}
		if svc.HealthcheckTimeout != nil && *svc.HealthcheckTimeout <= 0 {
			*errors = append(*errors, fmt.Sprintf("[services.%s].healthcheck_timeout must be positive", name))
		}
		if svc.HealthcheckStatus != nil {
			status := *svc.HealthcheckStatus
			if status < 100 || status > 599 {
				*errors = append(*errors, fmt.Sprintf("[services.%s].healthcheck_status must be an HTTP status code", name))
			}
		}
	}
}

func validateRoutes(routes map[string]Route, services map[string]Service, env string, errors *[]string) {
	for name, route := range routes {
		if !ServiceRe.MatchString(name) {
			*errors = append(*errors, fmt.Sprintf("invalid route name: %s", name))
		}
		if route.Host == "" {
			*errors = append(*errors, fmt.Sprintf("[routes.%s].host is required", name))
		} else if !ValidateHost(route.Host) {
			*errors = append(*errors, fmt.Sprintf("[routes.%s].host is invalid", name))
		}

		if route.Type == "" {
			*errors = append(*errors, fmt.Sprintf("[routes.%s].type is required", name))
		} else if route.Type != "proxy" && route.Type != "static" && route.Type != "redirect" {
			*errors = append(*errors, fmt.Sprintf("[routes.%s].type must be proxy, static, or redirect", name))
		}

		if route.Type == "proxy" {
			if route.Service == "" {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].service is required for proxy routes", name))
			} else if svc, ok := services[route.Service]; !ok {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].service references unknown service: %s", name, route.Service))
			} else if svc.Port == nil {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].service must reference a service with a port", name))
			}
		}

		if route.Type == "static" && route.Root != "" {
			*errors = append(*errors, fmt.Sprintf("[routes.%s].root is not configurable in v1", name))
		}

		if route.Type == "redirect" {
			if route.To == "" {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].to is required for redirect routes", name))
			} else if !strings.HasPrefix(route.To, "http://") && !strings.HasPrefix(route.To, "https://") {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].to must start with http:// or https://", name))
			}
		}
	}
}

func isInstallNeeded(runtime string, build *Build) bool {
	if runtime == "static" {
		return false
	}
	if build != nil && build.Install != nil {
		return *build.Install
	}
	return true
}

package config

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/names"
	"github.com/pelletier/go-toml/v2"
)

const (
	ShapeContainer = "container"
	ShapeStatic    = "static"
)

var (
	AppRe        = names.AppRe
	EnvRe        = names.EnvRe
	ProcessRe    = names.ProcessRe
	SystemUserRe = names.SystemUserRe
	EnvKeyRe     = names.EnvKeyRe
)

const secretPrefix = "@secret:"

type Resources struct {
	Memory *string  `toml:"memory"`
	CPUs   *float64 `toml:"cpus"`
}

type Process struct {
	Command   string    `toml:"command"`
	Port      *int      `toml:"port"`
	Health    string    `toml:"health"`
	Resources Resources `toml:"resources"`
}

type Route struct {
	Host     string `toml:"host"`
	Path     string `toml:"path"`
	Process  string `toml:"process"`
	Serve    string `toml:"serve"`
	Redirect string `toml:"redirect"`
	// TLS controls Caddy's automatic-HTTPS behavior for this route:
	//   - ""         — same as "auto"
	//   - "auto"     — emit nothing; Caddy provisions Let's Encrypt
	//   - "internal" — emit `tls internal`; self-signed cert for
	//                  private DNS, dev, and smoke boxes
	//
	// "off" is intentionally not supported. Reject anything else at
	// check time so typos never silently downgrade HTTPS behavior.
	TLS string `toml:"tls"`
}

type DeployConfig struct {
	Release string `toml:"release"`
}

type EnvBlock struct {
	Server    string             `toml:"server"`
	Processes map[string]Process `toml:"processes"`
	Routes    map[string]Route   `toml:"routes"`
	Vars      map[string]any     `toml:"vars"`
	Deploy    DeployConfig       `toml:"deploy"`
}

type Manifest struct {
	Name      string              `toml:"name"`
	Processes map[string]Process  `toml:"processes"`
	Routes    map[string]Route    `toml:"routes"`
	Vars      map[string]any      `toml:"vars"`
	Deploy    DeployConfig        `toml:"deploy"`
	Env       map[string]EnvBlock `toml:"env"`
}

type AppContext struct {
	AppName         string
	EnvName         string
	Server          string
	Shape           string
	NeedsImage      bool
	HasStaticRoutes bool
	Dockerfile      string
	Processes       map[string]Process
	Routes          map[string]Route
	Deploy          DeployConfig
	// Vars holds resolved non-secret env values for this env.
	Vars map[string]string
	// SecretRefs maps env-var key -> secret key name. The helper resolves
	// these against the per-(app, env, key) secret store before deploy.
	SecretRefs map[string]string
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
	// Strict decoding: removed fields (`runtime`, `[build]`, `[services]`,
	// `[env.*.env]`, `tmpfs`, route `type`, etc.) fail at
	// check time instead of silently becoming no-ops.
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("failed to parse simple-vps.toml: %s", strictErrorMessage(err))
	}
	return &manifest, nil
}

func strictErrorMessage(err error) string {
	var missing *toml.StrictMissingError
	if !errors.As(err, &missing) || len(missing.Errors) == 0 {
		return err.Error()
	}
	var msgs []string
	for _, decErr := range missing.Errors {
		key := strings.Join([]string(decErr.Key()), ".")
		row, col := decErr.Position()
		if key == "" {
			msgs = append(msgs, fmt.Sprintf("unknown field at line %d:%d", row, col))
			continue
		}
		msgs = append(msgs, fmt.Sprintf("unknown field %q at line %d:%d", key, row, col))
	}
	return strings.Join(msgs, "; ")
}

func detectShape(root string, processes map[string]Process, routes map[string]Route) (string, string) {
	hasDockerfile := false
	if _, err := os.Stat(filepath.Join(root, "Dockerfile")); err == nil {
		hasDockerfile = true
	}

	hasProcesses := len(processes) > 0
	if !hasProcesses && len(routes) == 0 {
		return "", "manifest must declare at least one [processes.<name>] block or route"
	}

	if hasProcesses || hasProcessRoutes(routes) {
		if !hasDockerfile {
			return "", "manifest declares processes but is missing a Dockerfile"
		}
		return ShapeContainer, ""
	}

	if hasServeRoutes(routes) || len(routes) > 0 {
		return ShapeStatic, ""
	}

	return "", "manifest must declare at least one [processes.<name>] block or route"
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
		errors = append(errors, "name must match "+names.AppPattern)
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

	for _, selected := range selectedEnvNames {
		envBlock := manifest.Env[selected]
		if !EnvRe.MatchString(selected) {
			errors = append(errors, fmt.Sprintf("invalid env name: %s", selected))
		}

		if envBlock.Server == "" {
			errors = append(errors, fmt.Sprintf("[env.%s].server is required", selected))
		} else if !ValidateSshTarget(envBlock.Server) {
			errors = append(errors, fmt.Sprintf("[env.%s].server must be an SSH target like deploy@example.com", selected))
		}

		mergedVars := mergeVars(manifest.Vars, envBlock.Vars)
		validateVarsBlock(mergedVars, selected, &errors)

		mergedProcesses := mergeProcesses(manifest.Processes, envBlock.Processes)
		validateProcesses(mergedProcesses, &errors)

		mergedRoutes := mergeRoutes(manifest.Routes, envBlock.Routes)
		validateRoutes(root, mergedRoutes, mergedProcesses, &errors)

		mergedDeploy := mergeDeploy(manifest.Deploy, envBlock.Deploy)
		shape, shapeErr := detectShape(root, mergedProcesses, mergedRoutes)
		if shapeErr != "" {
			errors = append(errors, shapeErr)
		}
		if shape == ShapeStatic && mergedDeploy.Release != "" {
			errors = append(errors, "[deploy].release is only supported for container apps")
		}
		if shape == ShapeStatic && len(mergedVars) > 0 {
			errors = append(errors, "[vars] is only supported for container apps")
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

	processes := mergeProcesses(manifest.Processes, envBlock.Processes)
	routes := mergeRoutes(manifest.Routes, envBlock.Routes)
	shape, _ := detectShape(root, processes, routes)
	dockerfile := ""
	if shape == ShapeContainer {
		dockerfile = filepath.Join(root, "Dockerfile")
	}

	vars, secretRefs := splitVarsBlock(mergeVars(manifest.Vars, envBlock.Vars))

	return &AppContext{
		AppName:         manifest.Name,
		EnvName:         envName,
		Server:          envBlock.Server,
		Shape:           shape,
		NeedsImage:      shape == ShapeContainer,
		HasStaticRoutes: hasServeRoutes(routes),
		Dockerfile:      dockerfile,
		Processes:       processes,
		Routes:          routes,
		Deploy:          mergeDeploy(manifest.Deploy, envBlock.Deploy),
		Vars:            vars,
		SecretRefs:      secretRefs,
	}, nil
}

func hasProcessRoutes(routes map[string]Route) bool {
	for _, route := range routes {
		if route.Process != "" {
			return true
		}
	}
	return false
}

func hasServeRoutes(routes map[string]Route) bool {
	for _, route := range routes {
		if route.Serve != "" {
			return true
		}
	}
	return false
}

func splitVarsBlock(vars map[string]any) (map[string]string, map[string]string) {
	literals := make(map[string]string)
	refs := make(map[string]string)
	for k, v := range vars {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(s, secretPrefix) {
			key := strings.TrimPrefix(s, secretPrefix)
			if EnvKeyRe.MatchString(key) {
				refs[k] = key
				continue
			}
		}
		literals[k] = s
	}
	return literals, refs
}

func validateVarsBlock(vars map[string]any, envName string, errors *[]string) {
	for key, raw := range vars {
		label := fmt.Sprintf("[env.%s.vars].%s", envName, key)
		if !EnvKeyRe.MatchString(key) {
			*errors = append(*errors, fmt.Sprintf("%s key must match ^[A-Za-z_][A-Za-z0-9_]*$", label))
			continue
		}
		switch v := raw.(type) {
		case string:
			if strings.HasPrefix(v, secretPrefix) {
				ref := strings.TrimPrefix(v, secretPrefix)
				if !EnvKeyRe.MatchString(ref) {
					*errors = append(*errors, fmt.Sprintf("%s value starts with reserved prefix '@secret:', use the secret store instead", label))
				}
			}
		case bool:
			*errors = append(*errors, fmt.Sprintf("%s must be a string; if you want %q, write it as a quoted string", label, fmt.Sprintf("%t", v)))
		case int64:
			*errors = append(*errors, fmt.Sprintf("%s must be a string; if you want %q, write it as a quoted string", label, fmt.Sprintf("%d", v)))
		case float64:
			*errors = append(*errors, fmt.Sprintf("%s must be a string; if you want %q, write it as a quoted string", label, fmt.Sprintf("%v", v)))
		default:
			*errors = append(*errors, fmt.Sprintf("%s must be a string; arrays and tables are not supported", label))
		}
	}
}

func mergeVars(base map[string]any, override map[string]any) map[string]any {
	res := make(map[string]any)
	for k, v := range base {
		res[k] = v
	}
	for k, v := range override {
		res[k] = v
	}
	return res
}

func mergeProcesses(base map[string]Process, override map[string]Process) map[string]Process {
	res := make(map[string]Process)
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
		if v.Health != "" {
			existing.Health = v.Health
		}
		if v.Resources.Memory != nil {
			existing.Resources.Memory = v.Resources.Memory
		}
		if v.Resources.CPUs != nil {
			existing.Resources.CPUs = v.Resources.CPUs
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
		if v.Path != "" {
			existing.Path = v.Path
		}
		if v.Process != "" {
			existing.Process = v.Process
			existing.Serve = ""
			existing.Redirect = ""
		}
		if v.Serve != "" {
			existing.Serve = v.Serve
			existing.Process = ""
			existing.Redirect = ""
		}
		if v.Redirect != "" {
			existing.Redirect = v.Redirect
			existing.Process = ""
			existing.Serve = ""
		}
		if v.TLS != "" {
			existing.TLS = v.TLS
		}
		res[k] = existing
	}
	return res
}

func mergeDeploy(base DeployConfig, override DeployConfig) DeployConfig {
	if override.Release != "" {
		base.Release = override.Release
	}
	return base
}

func validateProcesses(processes map[string]Process, errors *[]string) {
	ports := make(map[int]string)
	reserved := map[string]bool{"data": true, "runtime": true, "static": true}

	for name, proc := range processes {
		if !ProcessRe.MatchString(name) {
			*errors = append(*errors, fmt.Sprintf("invalid process name: %s", name))
		}
		if reserved[name] {
			*errors = append(*errors, fmt.Sprintf("reserved process name: %s", name))
		}
		if proc.Port != nil {
			port := *proc.Port
			if port < 1 || port > 65535 {
				*errors = append(*errors, fmt.Sprintf("[processes.%s].port must be an integer in [1, 65535]", name))
			} else if existing, ok := ports[port]; ok {
				*errors = append(*errors, fmt.Sprintf("[processes.%s].port duplicates [processes.%s].port", name, existing))
			} else {
				ports[port] = name
			}
			if proc.Health == "" {
				*errors = append(*errors, fmt.Sprintf("[processes.%s].health is required when port is set", name))
			}
		}
		validateProcessResources(name, proc.Resources, errors)
	}
}

var byteSizeRe = regexp.MustCompile(`^[1-9][0-9]*(k|m|g)$`)

func validateProcessResources(processName string, res Resources, errors *[]string) {
	if res.Memory != nil && !byteSizeRe.MatchString(*res.Memory) {
		*errors = append(*errors, fmt.Sprintf("[processes.%s].resources.memory %q must match ^[1-9][0-9]*(k|m|g)$", processName, *res.Memory))
	}
	if res.CPUs != nil && (*res.CPUs <= 0 || math.IsNaN(*res.CPUs) || math.IsInf(*res.CPUs, 0)) {
		*errors = append(*errors, fmt.Sprintf("[processes.%s].resources.cpus must be positive", processName))
	}
}

func validateRoutes(root string, routes map[string]Route, processes map[string]Process, errors *[]string) {
	seenHostPaths := map[string]string{}
	hostTLS := map[string]string{}
	for _, name := range sortedRouteNames(routes) {
		route := routes[name]
		if !ProcessRe.MatchString(name) {
			*errors = append(*errors, fmt.Sprintf("invalid route name: %s", name))
		}
		if route.Host == "" {
			*errors = append(*errors, fmt.Sprintf("[routes.%s].host is required", name))
		} else if !ValidateHost(route.Host) {
			*errors = append(*errors, fmt.Sprintf("[routes.%s].host is invalid", name))
		} else {
			hostPathKey := route.Host + "\x00" + route.Path
			if existing := seenHostPaths[hostPathKey]; existing != "" {
				*errors = append(*errors, fmt.Sprintf("[routes.%s] conflicts with [routes.%s]: host/path already used", name, existing))
			} else {
				seenHostPaths[hostPathKey] = name
			}
			tlsMode := route.TLS
			if tlsMode == "" {
				tlsMode = "auto"
			}
			if existing := hostTLS[route.Host]; existing != "" && existing != tlsMode {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].tls conflicts with another route for host %s", name, route.Host))
			} else {
				hostTLS[route.Host] = tlsMode
			}
		}
		validateRoutePath(name, route.Path, errors)

		targets := 0
		if route.Process != "" {
			targets++
		}
		if route.Serve != "" {
			targets++
		}
		if route.Redirect != "" {
			targets++
		}
		if targets != 1 {
			*errors = append(*errors, fmt.Sprintf("[routes.%s] must set exactly one of process, serve, or redirect", name))
		}

		if route.Process != "" {
			if proc, ok := processes[route.Process]; !ok {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].process references unknown process: %s", name, route.Process))
			} else if proc.Port == nil {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].process must reference a process with a port", name))
			}
		}

		if route.Serve != "" {
			validateServeDir(root, name, route.Serve, errors)
		}

		if route.Redirect != "" {
			if !strings.HasPrefix(route.Redirect, "http://") && !strings.HasPrefix(route.Redirect, "https://") {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].redirect must start with http:// or https://", name))
			}
		}

		switch route.TLS {
		case "", "auto", "internal":
			// OK
		default:
			*errors = append(*errors, fmt.Sprintf(`[routes.%s].tls must be "auto" or "internal"`, name))
		}
	}
}

func sortedRouteNames(routes map[string]Route) []string {
	names := make([]string, 0, len(routes))
	for name := range routes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateRoutePath(routeName, path string, errors *[]string) {
	if path == "" {
		return
	}
	label := fmt.Sprintf("[routes.%s].path", routeName)
	if !strings.HasPrefix(path, "/") {
		*errors = append(*errors, label+" must start with /")
		return
	}
	if path == "/" {
		*errors = append(*errors, label+" must be omitted for the host root")
		return
	}
	if strings.Contains(path, "..") {
		*errors = append(*errors, label+` must not contain ".."`)
		return
	}
	if path != "/" && strings.HasSuffix(path, "/") {
		*errors = append(*errors, label+" must not have a trailing slash")
		return
	}
	if strings.ContainsAny(path, " \t\r\n") {
		*errors = append(*errors, label+" must not contain whitespace")
		return
	}
	if strings.ContainsAny(path, "*?[]{}#") {
		*errors = append(*errors, label+" must not contain Caddy matcher syntax")
		return
	}
}

func validateServeDir(root, routeName, dir string, errors *[]string) {
	label := fmt.Sprintf("[routes.%s].serve", routeName)
	if filepath.IsAbs(dir) {
		*errors = append(*errors, label+" must be relative to the app root")
		return
	}
	clean := filepath.Clean(dir)
	if clean == "." || strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+".."+string(filepath.Separator)) {
		*errors = append(*errors, label+` must not contain ".."`)
		return
	}
	info, err := os.Stat(filepath.Join(root, dir))
	if err != nil {
		*errors = append(*errors, fmt.Sprintf("%s directory %q does not exist", label, dir))
		return
	}
	if !info.IsDir() {
		*errors = append(*errors, fmt.Sprintf("%s %q must be a directory", label, dir))
	}
}

package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/fprl/ship/internal/names"
	"github.com/pelletier/go-toml/v2"
)

const (
	ShapeContainer = "container"
	ShapeStatic    = "static"
)

const DockerfileMissingDetail = "manifest declares processes but is missing a Dockerfile"

var (
	AppRe        = names.AppRe
	EnvRe        = names.EnvRe
	ProcessRe    = names.ProcessRe
	SystemUserRe = names.SystemUserRe
	EnvKeyRe     = names.EnvKeyRe
)

const (
	secretBare   = "@secret"
	secretPrefix = "@secret:"
)

type Resources struct {
	Memory *string  `toml:"memory"`
	CPUs   *float64 `toml:"cpus"`
}

type Process struct {
	Command   string    `toml:"cmd"`
	Port      *int      `toml:"port"`
	Preview   bool      `toml:"preview"`
	Resources Resources `toml:"resources"`
}

type Route struct {
	Host     string `toml:"-"`
	Path     string `toml:"-"`
	Process  string `toml:"-"`
	Serve    string `toml:"-"`
	Redirect string `toml:"-"`
	// TLS is helper-side deploy state set by the client --tls flag:
	//   - ""         — same as "auto"
	//   - "auto"     — emit nothing; Caddy provisions Let's Encrypt
	//   - "internal" — emit `tls internal`; self-signed cert for
	//                  private DNS, dev, and smoke boxes
	//
	// It is not part of the public MANIFEST schema.
	TLS string `toml:"-"`
}

type Manifest struct {
	Name             string             `toml:"name"`
	Box              string             `toml:"box"`
	ProductionBranch string             `toml:"production_branch"`
	Processes        map[string]Process `toml:"processes"`
	Routes           map[string]Route   `toml:"routes"`
	Env              map[string]any     `toml:"env"`
	Release          string             `toml:"release"`
	Probe            string             `toml:"probe"`
	Notify           string             `toml:"notify"`
}

type rawManifest struct {
	Name             string         `toml:"name"`
	Box              string         `toml:"box"`
	ProductionBranch string         `toml:"production_branch"`
	Processes        map[string]any `toml:"processes"`
	Routes           map[string]any `toml:"routes"`
	Env              map[string]any `toml:"env"`
	Release          string         `toml:"release"`
	Probe            string         `toml:"probe"`
	Notify           string         `toml:"notify"`
}

type AppContext struct {
	AppName          string
	EnvName          string
	Server           string
	ProductionBranch string
	Shape            string
	NeedsImage       bool
	HasStaticRoutes  bool
	Dockerfile       string
	Processes        map[string]Process
	Routes           map[string]Route
	Release          string
	Probe            string
	Notify           string
	// Vars holds resolved non-secret env values for this env.
	Vars map[string]string
	// SecretRefs maps env-var key -> secret key name. The helper resolves
	// these against the per-(app, env, key) secret store before deploy.
	SecretRefs map[string]string
}

type ManifestError struct {
	Details []string
}

func (e *ManifestError) Error() string {
	return strings.Join(e.Details, "\n")
}

func manifestError(details ...string) error {
	return &ManifestError{Details: append([]string(nil), details...)}
}

func ManifestErrorDetails(err error) ([]string, bool) {
	var manifestErr *ManifestError
	if !errors.As(err, &manifestErr) {
		return nil, false
	}
	return append([]string(nil), manifestErr.Details...), true
}

func (p *Process) UnmarshalTOML(value any) error {
	*p = Process{Preview: true}
	switch v := value.(type) {
	case string:
		p.Command = v
		return nil
	case map[string]any:
		for key, raw := range v {
			switch key {
			case "cmd":
				s, ok := raw.(string)
				if !ok {
					return fmt.Errorf("[processes.<name>].cmd must be a string")
				}
				p.Command = s
			case "port":
				port, err := tomlInt(raw)
				if err != nil {
					return fmt.Errorf("[processes.<name>].port must be an integer")
				}
				p.Port = &port
			case "preview":
				b, ok := raw.(bool)
				if !ok {
					return fmt.Errorf("[processes.<name>].preview must be a boolean")
				}
				p.Preview = b
			case "resources":
				res, err := parseResources(raw)
				if err != nil {
					return err
				}
				p.Resources = res
			case "health":
				return fmt.Errorf("[processes.<name>].health is not supported; use top-level probe")
			case "command":
				return fmt.Errorf("[processes.<name>].command is not supported; use cmd")
			default:
				return fmt.Errorf("unknown process field %q", key)
			}
		}
		return nil
	default:
		return fmt.Errorf("[processes.<name>] must be a string command or a table")
	}
}

func qualifyProcessError(name string, err error) error {
	return errors.New(strings.ReplaceAll(err.Error(), "[processes.<name>]", fmt.Sprintf("[processes.%s]", name)))
}

func parseResources(raw any) (Resources, error) {
	table, ok := raw.(map[string]any)
	if !ok {
		return Resources{}, fmt.Errorf("[processes.<name>].resources must be a table")
	}
	var res Resources
	for key, value := range table {
		switch key {
		case "memory":
			s, ok := value.(string)
			if !ok {
				return Resources{}, fmt.Errorf("[processes.<name>].resources.memory must be a string")
			}
			res.Memory = &s
		case "cpus":
			switch v := value.(type) {
			case float64:
				res.CPUs = &v
			case int64:
				f := float64(v)
				res.CPUs = &f
			default:
				return Resources{}, fmt.Errorf("[processes.<name>].resources.cpus must be a number")
			}
		default:
			return Resources{}, fmt.Errorf("unknown process resources field %q", key)
		}
	}
	return res, nil
}

func tomlInt(raw any) (int, error) {
	switch v := raw.(type) {
	case int64:
		return int(v), nil
	case int:
		return v, nil
	default:
		return 0, fmt.Errorf("not an integer")
	}
}

func (r *Route) UnmarshalTOML(value any) error {
	*r = Route{}
	switch v := value.(type) {
	case string:
		r.Process = v
		return nil
	case map[string]any:
		targets := 0
		for key, raw := range v {
			switch key {
			case "process":
				s, ok := raw.(string)
				if !ok {
					return fmt.Errorf("[routes.<host/path>].process must be a string")
				}
				r.Process = s
				targets++
			case "static":
				s, ok := raw.(string)
				if !ok {
					return fmt.Errorf("[routes.<host/path>].static must be a string")
				}
				r.Serve = s
				targets++
			case "redirect":
				s, ok := raw.(string)
				if !ok {
					return fmt.Errorf("[routes.<host/path>].redirect must be a string")
				}
				r.Redirect = s
				targets++
			default:
				return fmt.Errorf("unknown route target field %q", key)
			}
		}
		if targets != 1 {
			return fmt.Errorf("[routes.<host/path>] must set exactly one of process, static, or redirect")
		}
		return nil
	default:
		return fmt.Errorf("[routes.<host/path>] must be a process string or a target table")
	}
}

func qualifyRouteError(name string, err error) error {
	return errors.New(strings.ReplaceAll(err.Error(), "[routes.<host/path>]", routeLabel(name)))
}

func parseProcessMap(raw map[string]any) (map[string]Process, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]Process, len(raw))
	for name, value := range raw {
		var proc Process
		if err := proc.UnmarshalTOML(value); err != nil {
			return nil, qualifyProcessError(name, err)
		}
		out[name] = proc
	}
	return out, nil
}

func parseRouteMap(raw map[string]any) (map[string]Route, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]Route, len(raw))
	for name, value := range raw {
		var route Route
		if err := route.UnmarshalTOML(value); err != nil {
			return nil, qualifyRouteError(name, err)
		}
		out[name] = route
	}
	return out, nil
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
	path := filepath.Join(root, "ship.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, manifestError("ship.toml not found")
	}
	var raw rawManifest
	// Strict decoding: removed fields (`runtime`, `[build]`, `[services]`,
	// `[env.*.env]`, `tmpfs`, route `type`, etc.) fail at
	// check time instead of silently becoming no-ops.
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, manifestError(fmt.Sprintf("failed to parse ship.toml: %s", strictErrorMessage(err)))
	}
	processes, err := parseProcessMap(raw.Processes)
	if err != nil {
		return nil, manifestError(fmt.Sprintf("failed to parse ship.toml: %s", err))
	}
	routes, err := parseRouteMap(raw.Routes)
	if err != nil {
		return nil, manifestError(fmt.Sprintf("failed to parse ship.toml: %s", err))
	}
	manifest := Manifest{
		Name:             raw.Name,
		Box:              raw.Box,
		ProductionBranch: raw.ProductionBranch,
		Processes:        processes,
		Routes:           hydrateRouteKeys(routes),
		Env:              raw.Env,
		Release:          raw.Release,
		Probe:            raw.Probe,
		Notify:           raw.Notify,
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

func hydrateRouteKeys(routes map[string]Route) map[string]Route {
	if len(routes) == 0 {
		return routes
	}
	out := make(map[string]Route, len(routes))
	for key, route := range routes {
		host, path := splitRouteKey(key)
		route.Host = canonicalHost(host)
		route.Path = path
		out[key] = route
	}
	return out
}

func splitRouteKey(key string) (string, string) {
	host, rawPath, found := strings.Cut(key, "/")
	if !found {
		return key, ""
	}
	return host, "/" + rawPath
}

func canonicalHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func routeLabel(routeName string) string {
	return fmt.Sprintf("[routes.%q]", routeName)
}

func RouteStorageName(routeKey string) string {
	if routeKey == "" {
		routeKey = "route"
	}
	var b strings.Builder
	prevDash := false
	changed := false
	for _, r := range routeKey {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if valid {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		changed = true
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "route"
		changed = true
	}
	if !changed {
		return name
	}
	sum := sha256.Sum256([]byte(routeKey))
	return name + "-" + hex.EncodeToString(sum[:])[:8]
}

func defaultProductionBranch(root, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		return configured
	}
	if gitBranchExists(root, "main") {
		return "main"
	}
	if gitBranchExists(root, "master") {
		return "master"
	}
	return "main"
}

func gitBranchExists(root, branch string) bool {
	gitDir := filepath.Join(root, ".git")
	if info, err := os.Stat(filepath.Join(gitDir, "refs", "heads", branch)); err == nil && !info.IsDir() {
		return true
	}
	packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return false
	}
	needle := "refs/heads/" + branch
	for _, line := range strings.Split(string(packed), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		if strings.HasSuffix(line, " "+needle) {
			return true
		}
	}
	return false
}

func applyProcessPortDefaults(root string, processes map[string]Process, routes map[string]Route) map[string]Process {
	if len(processes) == 0 {
		return processes
	}
	out := make(map[string]Process, len(processes))
	for name, proc := range processes {
		out[name] = proc
	}
	defaultPort := dockerfileDefaultPort(root)
	for _, route := range routes {
		if route.Process == "" {
			continue
		}
		proc, ok := out[route.Process]
		if !ok || proc.Port != nil {
			continue
		}
		port := defaultPort
		proc.Port = &port
		out[route.Process] = proc
	}
	return out
}

func dockerfileDefaultPort(root string) int {
	ports := exposedDockerfilePorts(filepath.Join(root, "Dockerfile"))
	if len(ports) == 1 {
		for port := range ports {
			return port
		}
	}
	return 3000
}

func exposedDockerfilePorts(path string) map[int]bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	ports := map[int]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = stripDockerfileComment(line)
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "EXPOSE") {
			continue
		}
		for _, field := range fields[1:] {
			portToken, _, _ := strings.Cut(field, "/")
			port, err := strconv.Atoi(portToken)
			if err != nil || port < 1 || port > 65535 {
				continue
			}
			ports[port] = true
		}
	}
	return ports
}

func stripDockerfileComment(line string) string {
	if idx := strings.IndexByte(line, '#'); idx >= 0 {
		return line[:idx]
	}
	return line
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
			return "", DockerfileMissingDetail
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
	return CheckLoadedManifest(root, envName, manifest)
}

func CheckLoadedManifest(root string, envName string, manifest *Manifest) ([]string, []string, error) {
	var errors []string
	var warnings []string

	if manifest.Name == "" {
		errors = append(errors, "name is required")
	} else if !AppRe.MatchString(manifest.Name) {
		errors = append(errors, "name must match "+names.AppPattern)
	}

	if manifest.Box == "" {
		errors = append(errors, "box is required")
	} else if !ValidateSshTarget(manifest.Box) {
		errors = append(errors, "box must be an SSH target like deploy@example.com")
	}

	if manifest.ProductionBranch != "" && !validateProductionBranch(manifest.ProductionBranch) {
		errors = append(errors, "production_branch must be a valid git branch name")
	}

	if envName != "" && !EnvRe.MatchString(envName) {
		errors = append(errors, fmt.Sprintf("invalid env name: %s", envName))
	}

	validateVarsBlock(manifest.Env, &errors)
	validateProbe(manifest.Probe, &errors)
	validateNotify(manifest.Notify, &errors)

	routes := manifest.Routes
	processes := applyProcessPortDefaults(root, manifest.Processes, routes)
	validateProcesses(processes, &errors)
	validateRoutes(root, routes, processes, &errors)

	shape, shapeErr := detectShape(root, processes, routes)
	if shapeErr != "" {
		errors = append(errors, shapeErr)
	}
	if shape == ShapeStatic && manifest.Release != "" {
		errors = append(errors, "release is only supported for container apps")
	}
	if shape == ShapeStatic && len(manifest.Env) > 0 {
		errors = append(errors, "[env] is only supported for container apps")
	}

	return errors, warnings, nil
}

func LoadAppContext(root string, envName string) (*AppContext, error) {
	manifest, err := ReadManifest(root)
	if err != nil {
		return nil, err
	}
	return LoadAppContextFromManifest(root, envName, manifest)
}

func LoadAppContextFromManifest(root string, envName string, manifest *Manifest) (*AppContext, error) {
	errors, _, err := CheckLoadedManifest(root, envName, manifest)
	if err != nil {
		return nil, err
	}
	if len(errors) > 0 {
		return nil, manifestError(errors...)
	}

	routes := manifest.Routes
	processes := applyProcessPortDefaults(root, manifest.Processes, routes)
	shape, _ := detectShape(root, processes, routes)
	dockerfile := ""
	if shape == ShapeContainer {
		dockerfile = filepath.Join(root, "Dockerfile")
	}

	vars, secretRefs := splitVarsBlock(manifest.Env)

	return &AppContext{
		AppName:          manifest.Name,
		EnvName:          envName,
		Server:           manifest.Box,
		ProductionBranch: defaultProductionBranch(root, manifest.ProductionBranch),
		Shape:            shape,
		NeedsImage:       shape == ShapeContainer,
		HasStaticRoutes:  hasServeRoutes(routes),
		Dockerfile:       dockerfile,
		Processes:        processes,
		Routes:           routes,
		Release:          manifest.Release,
		Probe:            manifest.Probe,
		Notify:           manifest.Notify,
		Vars:             vars,
		SecretRefs:       secretRefs,
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
		if s == secretBare {
			refs[k] = k
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

func validateVarsBlock(vars map[string]any, errors *[]string) {
	for key, raw := range vars {
		label := fmt.Sprintf("[env].%s", key)
		if !EnvKeyRe.MatchString(key) {
			*errors = append(*errors, fmt.Sprintf("%s key must match ^[A-Za-z_][A-Za-z0-9_]*$", label))
			continue
		}
		switch v := raw.(type) {
		case string:
			if v == secretBare {
				continue
			}
			if strings.HasPrefix(v, secretPrefix) {
				ref := strings.TrimPrefix(v, secretPrefix)
				if !EnvKeyRe.MatchString(ref) {
					*errors = append(*errors, fmt.Sprintf("%s value starts with reserved prefix '@secret:', use a valid secret key", label))
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

func validateProductionBranch(branch string) bool {
	return names.ValidGitBranch(branch)
}

func validateProbe(probe string, errors *[]string) {
	if probe == "" {
		return
	}
	if !strings.HasPrefix(probe, "/") {
		*errors = append(*errors, "probe must start with /")
		return
	}
	if strings.ContainsAny(probe, " \t\r\n") {
		*errors = append(*errors, "probe must not contain whitespace")
	}
}

func validateNotify(raw string, errors *[]string) {
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		*errors = append(*errors, "notify must be a valid URL")
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		*errors = append(*errors, "notify must use http or https")
	}
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
		label := routeLabel(name)
		if route.Host == "" {
			*errors = append(*errors, fmt.Sprintf("%s host is required", label))
		} else if !ValidateHost(route.Host) {
			*errors = append(*errors, fmt.Sprintf("%s host is invalid", label))
		} else {
			hostPathKey := route.Host + "\x00" + route.Path
			if existing := seenHostPaths[hostPathKey]; existing != "" {
				*errors = append(*errors, fmt.Sprintf("%s conflicts with %s: host/path already used", label, routeLabel(existing)))
			} else {
				seenHostPaths[hostPathKey] = name
			}
			tlsMode := route.TLS
			if tlsMode == "" {
				tlsMode = "auto"
			}
			if existing := hostTLS[route.Host]; existing != "" && existing != tlsMode {
				*errors = append(*errors, fmt.Sprintf("%s tls conflicts with another route for host %s", label, route.Host))
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
			*errors = append(*errors, fmt.Sprintf("%s must set exactly one target", label))
		}

		if route.Process != "" {
			if proc, ok := processes[route.Process]; !ok {
				*errors = append(*errors, fmt.Sprintf("%s references unknown process: %s", label, route.Process))
			} else if proc.Port == nil {
				*errors = append(*errors, fmt.Sprintf("%s must reference a process with a port", label))
			}
		}

		if route.Serve != "" {
			validateServeDir(root, name, route.Serve, errors)
		}

		if route.Redirect != "" {
			if !ValidateHost(route.Redirect) {
				*errors = append(*errors, fmt.Sprintf("%s redirect must be a hostname", label))
			}
		}

		switch route.TLS {
		case "", "auto", "internal":
			// OK
		default:
			*errors = append(*errors, fmt.Sprintf(`%s tls must be "auto" or "internal"`, label))
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
	label := routeLabel(routeName) + " path"
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
	label := routeLabel(routeName) + ".static"
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
		return
	}
	rooted := filepath.Join(root, dir)
	if err := filepath.WalkDir(rooted, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		*errors = append(*errors, fmt.Sprintf("%s must not contain symlink %q", label, filepath.ToSlash(rel)))
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}); err != nil {
		*errors = append(*errors, fmt.Sprintf("%s scan failed: %v", label, err))
	}
}

package config

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fprl/simple-vps/internal/names"
	"github.com/pelletier/go-toml/v2"
)

const ShapeContainer = "container"

var (
	AppRe        = names.AppRe
	EnvRe        = names.EnvRe
	ServiceRe    = names.ServiceRe
	SystemUserRe = names.SystemUserRe
	EnvKeyRe     = names.EnvKeyRe
)

const secretPrefix = "@secret:"

type Service struct {
	Command            string `toml:"command"`
	Port               *int   `toml:"port"`
	Healthcheck        string `toml:"healthcheck"`
	HealthcheckStatus  *int   `toml:"healthcheck_status"`
	HealthcheckTimeout *int   `toml:"healthcheck_timeout"`
	// Memory and CPUs are per-service Podman resource limits from the
	// §7 security floor. Memory uses the same compact size grammar as
	// tmpfs (`512m`, `1g`). CPUs is a positive numeric quota (`0.5`,
	// `1`, `2.25`) and renders directly to Podman's --cpus flag.
	Memory *string  `toml:"memory"`
	CPUs   *float64 `toml:"cpus"`
	// NetBindService is the only manifest-expressible Linux capability.
	// It maps to --cap-add=NET_BIND_SERVICE for images that need to bind
	// privileged ports inside the container after --cap-drop=ALL.
	NetBindService *bool `toml:"net_bind_service"`
	// Tmpfs declares additional writable tmpfs mounts for stock
	// images that need scratch beyond the always-on `/tmp:64m` from
	// the §7 security floor (e.g., nginx wants `/var/cache/nginx`
	// and `/var/run`). Keys are absolute paths; values are Podman
	// size strings (`64m`, `1g`, ...). Validation in `validateServiceTmpfs`
	// enforces a denylist of dangerous targets and the size grammar.
	Tmpfs map[string]string `toml:"tmpfs"`
}

type Route struct {
	Host    string `toml:"host"`
	Type    string `toml:"type"`
	Service string `toml:"service"`
	To      string `toml:"to"`
	// TLS controls Caddy's automatic-HTTPS behavior for this route:
	//   - ""        — same as "auto"
	//   - "auto"    — emit nothing; Caddy provisions Let's Encrypt
	//   - "internal" — emit `tls internal`; self-signed cert (private
	//                  DNS, no public ACME, dev/test boxes)
	// "off" is intentionally not yet supported. Reject anything else
	// at check time.
	TLS string `toml:"tls"`
}

type EnvBlock struct {
	Server   string             `toml:"server"`
	Services map[string]Service `toml:"services"`
	Routes   map[string]Route   `toml:"routes"`
	// Env is the [env.<env>.env] block. Values must be strings (or whole-value
	// @secret:KEY references); non-string TOML values are rejected at check
	// time. Captured as any so we can produce precise type errors.
	Env map[string]any `toml:"env"`
}

type Manifest struct {
	Name     string              `toml:"name"`
	Services map[string]Service  `toml:"services"`
	Routes   map[string]Route    `toml:"routes"`
	Env      map[string]EnvBlock `toml:"env"`
}

type AppContext struct {
	AppName    string
	EnvName    string
	Server     string
	AppRoot    string
	Shape      string
	Dockerfile string
	Services   map[string]Service
	Routes     map[string]Route
	// Env holds resolved non-secret env values for this env.
	Env map[string]string
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
	// Strict decoding: any field outside the schema (legacy `runtime`,
	// `[build]`, `keep_releases`, or a typo) becomes a check-time error
	// rather than silently passing as a no-op. Pre-user repo, no compat
	// window — stale config that looks honored but does nothing is
	// worse than a clear "unknown field" error.
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("failed to parse simple-vps.toml: %s", strictErrorMessage(err))
	}
	return &manifest, nil
}

// strictErrorMessage turns go-toml/v2's terse "strict mode: fields in the
// document are missing in the target struct" wrapper into something a user
// can actually act on — the offending field names plus their positions.
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

func detectShape(root string) (string, string) {
	if _, err := os.Stat(filepath.Join(root, "Dockerfile")); err == nil {
		return ShapeContainer, ""
	}
	return "", "manifest is missing a Dockerfile; container apps are the only shipped app shape"
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

	_, shapeErr := detectShape(root)
	if shapeErr != "" {
		errors = append(errors, shapeErr)
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

		validateEnvBlock(envBlock.Env, selected, &errors)

		mergedServices := mergeServices(manifest.Services, envBlock.Services)
		validateServices(mergedServices, selected, &errors)

		mergedRoutes := mergeRoutes(manifest.Routes, envBlock.Routes)
		validateRoutes(mergedRoutes, mergedServices, selected, &errors)
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

	shape, _ := detectShape(root)
	dockerfile := filepath.Join(root, "Dockerfile")

	appRoot := fmt.Sprintf("/var/apps/%s/%s", manifest.Name, envName)

	envVals, secretRefs := splitEnvBlock(envBlock.Env)

	return &AppContext{
		AppName:    manifest.Name,
		EnvName:    envName,
		Server:     envBlock.Server,
		AppRoot:    appRoot,
		Shape:      shape,
		Dockerfile: dockerfile,
		Services:   mergeServices(manifest.Services, envBlock.Services),
		Routes:     mergeRoutes(manifest.Routes, envBlock.Routes),
		Env:        envVals,
		SecretRefs: secretRefs,
	}, nil
}

// splitEnvBlock walks the validated [env.<env>.env] block and produces a
// (literals, secretRefs) pair. Validation has already rejected non-strings
// and malformed @secret: prefixes, so this only runs on a well-formed map.
func splitEnvBlock(env map[string]any) (map[string]string, map[string]string) {
	literals := make(map[string]string)
	refs := make(map[string]string)
	for k, v := range env {
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

// validateEnvBlock enforces the [env.<env>.env] rules from ADR-0005:
// values must be strings; non-string TOML types are rejected with a clear
// fix-it hint; @secret:KEY whole-value references must have a valid env-var
// key after the prefix; any other value starting with @secret: is rejected
// as a reserved-prefix violation.
func validateEnvBlock(env map[string]any, envName string, errors *[]string) {
	for key, raw := range env {
		label := fmt.Sprintf("[env.%s.env].%s", envName, key)
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

// Merge helpers

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
		if v.Memory != nil {
			existing.Memory = v.Memory
		}
		if v.CPUs != nil {
			existing.CPUs = v.CPUs
		}
		if v.NetBindService != nil {
			existing.NetBindService = v.NetBindService
		}
		if len(v.Tmpfs) > 0 {
			// Env-level tmpfs fully replaces the base map rather than
			// per-key merging — keeps the semantics simple ("the env
			// decides scratch space") and avoids subtle "I declared
			// /var/cache here but it survives from the base block" bugs.
			merged := make(map[string]string, len(v.Tmpfs))
			for k2, v2 := range v.Tmpfs {
				merged[k2] = v2
			}
			existing.Tmpfs = merged
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
		if v.To != "" {
			existing.To = v.To
		}
		if v.TLS != "" {
			existing.TLS = v.TLS
		}
		res[k] = existing
	}
	return res
}

func validateServices(services map[string]Service, env string, errors *[]string) {
	ports := make(map[int]string)

	reserved := map[string]bool{"current": true, "releases": true, "shared": true}

	for name, svc := range services {
		if !ServiceRe.MatchString(name) {
			*errors = append(*errors, fmt.Sprintf("invalid service name: %s", name))
		}
		if reserved[name] {
			*errors = append(*errors, fmt.Sprintf("reserved service name: %s", name))
		}
		// Command is optional for container apps (Dockerfile CMD covers it);
		// per-service command overrides the image CMD (ADR-0005 Section 13).
		_ = svc.Command
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
		validateServiceResourceCaps(name, svc, errors)
		validateServiceTmpfs(name, svc.Tmpfs, errors)
	}
}

// byteSizeRe matches the compact Podman size grammar we accept for
// service memory and tmpfs limits: a positive integer followed by a
// unit (k/m/g). Strict lowercase keeps the manifest unambiguous;
// mixed-case or extra units (Ki, MiB, etc.) are rejected even if
// Podman happens to accept them.
var byteSizeRe = regexp.MustCompile(`^[1-9][0-9]*(k|m|g)$`)

func validateServiceResourceCaps(serviceName string, svc Service, errors *[]string) {
	if svc.Memory != nil && !byteSizeRe.MatchString(*svc.Memory) {
		*errors = append(*errors, fmt.Sprintf("[services.%s].memory %q must match ^[1-9][0-9]*(k|m|g)$", serviceName, *svc.Memory))
	}
	if svc.CPUs != nil && (*svc.CPUs <= 0 || math.IsNaN(*svc.CPUs) || math.IsInf(*svc.CPUs, 0)) {
		*errors = append(*errors, fmt.Sprintf("[services.%s].cpus must be positive", serviceName))
	}
}

// tmpfsReservedPaths refuses mounts that would either break the
// container or shadow critical config the entrypoint relies on:
//   - /                          rootfs over rootfs = broken container
//   - /etc, /proc, /sys, /dev    hides /etc/passwd, /proc, /sys, /dev
//   - /tmp                       reserved for the §7 security floor's
//     always-on `--tmpfs /tmp:size=64m`;
//     manifest can't override or duplicate
var tmpfsReservedPaths = map[string]bool{
	"/":     true,
	"/etc":  true,
	"/proc": true,
	"/sys":  true,
	"/dev":  true,
	"/tmp":  true,
}

func validateServiceTmpfs(serviceName string, tmpfs map[string]string, errors *[]string) {
	for path, size := range tmpfs {
		label := fmt.Sprintf("[services.%s.tmpfs].%q", serviceName, path)
		if !strings.HasPrefix(path, "/") {
			*errors = append(*errors, label+" must be an absolute path")
			continue
		}
		if strings.Contains(path, "..") {
			*errors = append(*errors, label+` must not contain ".."`)
			continue
		}
		// Reject trailing slashes so the rendered Podman flag is
		// canonical (`/var/cache/nginx`, never `/var/cache/nginx/`)
		// and the reserved-path check matches by literal equality.
		if path != "/" && strings.HasSuffix(path, "/") {
			*errors = append(*errors, label+" must not have a trailing slash")
			continue
		}
		if tmpfsReservedPaths[path] {
			*errors = append(*errors, fmt.Sprintf("[services.%s.tmpfs].%q is reserved and cannot be a tmpfs mount", serviceName, path))
			continue
		}
		if !byteSizeRe.MatchString(size) {
			*errors = append(*errors, fmt.Sprintf("[services.%s.tmpfs].%q size %q must match ^[1-9][0-9]*(k|m|g)$", serviceName, path, size))
			continue
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
		} else if route.Type != "proxy" && route.Type != "redirect" {
			*errors = append(*errors, fmt.Sprintf("[routes.%s].type must be proxy or redirect", name))
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

		if route.Type == "redirect" {
			if route.To == "" {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].to is required for redirect routes", name))
			} else if !strings.HasPrefix(route.To, "http://") && !strings.HasPrefix(route.To, "https://") {
				*errors = append(*errors, fmt.Sprintf("[routes.%s].to must start with http:// or https://", name))
			}
		}

		switch route.TLS {
		case "", "auto", "internal":
			// OK
		default:
			// `off` has a clean Caddyfile shape (`http://host { ... }`)
			// but is deferred until a real user asks. Reject loudly so
			// a typo doesn't quietly downgrade an HTTPS route to HTTP.
			*errors = append(*errors, fmt.Sprintf(`[routes.%s].tls must be "auto" or "internal"`, name))
		}
	}
}

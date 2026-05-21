package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	AppRe        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	ServiceRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	SystemUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}\$?$`)
	HeaderNameRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
)

func newHostFile() *HostFile {
	file := HostFile{Version: CurrentVersion}
	normalizeHostFile(&file)
	return &file
}

func normalizeHostFile(file *HostFile) {
	file.Version = CurrentVersion
	normalizeHostDesired(&file.Desired)
	normalizeHostObserved(&file.Observed)
}

func normalizeHostDesired(desired *HostDesired) {
	if desired.Packages == nil {
		desired.Packages = map[string]DesiredPackage{}
	}
	if desired.Features.Runtimes == nil {
		desired.Features.Runtimes = []string{}
	}
	sort.Strings(desired.Features.Runtimes)
}

func normalizeHostObserved(observed *HostObserved) {
	if observed.Packages == nil {
		observed.Packages = map[string]ObservedPackage{}
	}
}

func normalizeAppsFile(file *AppsFile) {
	file.Version = CurrentVersion
	if file.Apps == nil {
		file.Apps = map[string]App{}
	}
	for name, app := range file.Apps {
		if app.Services == nil {
			app.Services = []string{}
		}
		sort.Strings(app.Services)
		file.Apps[name] = app
	}
}

func normalizeRoutesFile(file *RoutesFile) error {
	file.Version = CurrentVersion
	if file.Routes == nil {
		file.Routes = []Route{}
	}
	for i, route := range file.Routes {
		normalized, err := NormalizeRoute(route)
		if err != nil {
			return fmt.Errorf("invalid route entry: %w", err)
		}
		file.Routes[i] = *normalized
	}
	sort.Slice(file.Routes, func(i, j int) bool {
		left := file.Routes[i]
		right := file.Routes[j]
		if left.Host != right.Host {
			return left.Host < right.Host
		}
		if left.App != right.App {
			return left.App < right.App
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.Service != right.Service {
			return left.Service < right.Service
		}
		leftPort := 0
		if left.Port != nil {
			leftPort = *left.Port
		}
		rightPort := 0
		if right.Port != nil {
			rightPort = *right.Port
		}
		if leftPort != rightPort {
			return leftPort < rightPort
		}
		if left.Root != right.Root {
			return left.Root < right.Root
		}
		return left.To < right.To
	})
	return nil
}

func NormalizeHost(value string) (string, error) {
	host := strings.TrimSpace(value)
	host = strings.ToLower(host)
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", errors.New("host cannot be empty")
	}
	if !ValidateHost(host) {
		return "", fmt.Errorf("invalid host: %s", value)
	}
	return host, nil
}

func ValidateHost(host string) bool {
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

func NormalizePort(value any) (int, error) {
	var port int
	switch val := value.(type) {
	case int:
		port = val
	case float64:
		port = int(val)
	case string:
		p, err := strconv.Atoi(val)
		if err != nil {
			return 0, fmt.Errorf("invalid port: %v", value)
		}
		port = p
	default:
		return 0, fmt.Errorf("invalid port: %v", value)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port: %d", port)
	}
	return port, nil
}

func NormalizeApp(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	app, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid app name: %v", value)
	}
	app = strings.TrimSpace(app)
	if app == "" {
		return "", nil
	}
	if !AppRe.MatchString(app) {
		return "", fmt.Errorf("invalid app name: %s", app)
	}
	return app, nil
}

func NormalizeRequiredApp(value string) (string, error) {
	normalized, err := NormalizeApp(value)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", errors.New("app name is required")
	}
	return normalized, nil
}

func AppRoot() string {
	if p := os.Getenv("SIMPLE_VPS_APP_ROOT"); p != "" {
		return p
	}
	return "/var/apps"
}

func AppPath(name string) string {
	return filepath.Join(AppRoot(), name)
}

func NormalizeService(value string) (string, error) {
	service := strings.TrimSpace(value)
	if !ServiceRe.MatchString(service) {
		return "", fmt.Errorf("invalid service name: %s", value)
	}
	if service == "current" || service == "releases" || service == "shared" {
		return "", fmt.Errorf("reserved service name: %s", service)
	}
	return service, nil
}

func NormalizeRoot(value string, app string) (string, error) {
	root := strings.TrimSpace(value)
	if root == "" {
		return "", errors.New("root cannot be empty")
	}
	if strings.ContainsAny(root, "\n\r") {
		return "", errors.New("root cannot contain newlines")
	}
	if !strings.HasPrefix(root, "/") {
		return "", errors.New("static route root must be an absolute path")
	}
	normalized := strings.TrimSuffix(root, "/")
	if normalized == "" {
		normalized = "/"
	}
	if app != "" {
		appDir := "/var/apps"
		if p := os.Getenv("SIMPLE_VPS_APP_ROOT"); p != "" {
			appDir = p
		}
		base := filepath.Join(appDir, app)
		normClean := filepath.Clean(normalized)
		baseClean := filepath.Clean(base)
		rel, err := filepath.Rel(baseClean, normClean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return "", fmt.Errorf("static route root for app %s must be under %s", app, baseClean)
		}
	}
	return normalized, nil
}

func NormalizeRedirectTarget(value string) (string, error) {
	target := strings.TrimSpace(value)
	if target == "" {
		return "", errors.New("redirect target cannot be empty")
	}
	if strings.ContainsAny(target, "\n\r \t") {
		return "", errors.New("redirect target cannot contain whitespace")
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return "", errors.New("redirect target must start with http:// or https://")
	}
	return target, nil
}

func NormalizeHeaders(value map[string]string) (map[string]string, error) {
	if len(value) == 0 {
		return nil, nil
	}
	headers := make(map[string]string)
	for rawName, rawValue := range value {
		name := strings.TrimSpace(rawName)
		val := strings.TrimSpace(rawValue)
		if !HeaderNameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid header name: %s", rawName)
		}
		if strings.ContainsAny(val, "\n\r") {
			return nil, fmt.Errorf("invalid header value for %s: newlines are not allowed", name)
		}
		headers[name] = val
	}
	return headers, nil
}

func NormalizeRoute(route Route) (*Route, error) {
	host, err := NormalizeHost(route.Host)
	if err != nil {
		return nil, err
	}
	routeType := strings.ToLower(strings.TrimSpace(route.Type))
	if routeType == "" && route.Port != nil {
		routeType = "proxy"
	}
	app, err := NormalizeApp(route.App)
	if err != nil {
		return nil, err
	}
	service := ""
	if strings.TrimSpace(route.Service) != "" {
		service, err = NormalizeService(route.Service)
		if err != nil {
			return nil, err
		}
	}
	headers, err := NormalizeHeaders(route.Headers)
	if err != nil {
		return nil, err
	}

	normalized := &Route{Host: host, Type: routeType, App: app, Service: service, Headers: headers}
	switch routeType {
	case "proxy":
		if route.Port == nil {
			return nil, fmt.Errorf("port is required for proxy route %s", host)
		}
		port, err := NormalizePort(*route.Port)
		if err != nil {
			return nil, err
		}
		normalized.Port = &port
	case "static":
		root, err := NormalizeRoot(route.Root, app)
		if err != nil {
			return nil, err
		}
		normalized.Root = root
	case "redirect":
		to, err := NormalizeRedirectTarget(route.To)
		if err != nil {
			return nil, err
		}
		normalized.To = to
		normalized.Headers = nil
	default:
		return nil, fmt.Errorf("invalid route type for %s: %s", host, routeType)
	}
	return normalized, nil
}

func RouteIndex(routes []Route, host string) int {
	for i, route := range routes {
		if route.Host == host {
			return i
		}
	}
	return -1
}

func validateHostDesired(desired HostDesired) error {
	if strings.TrimSpace(desired.Users.Operator) == "" {
		return fmt.Errorf("users.operator is required")
	}
	if strings.TrimSpace(desired.Users.Deploy) == "" {
		return fmt.Errorf("users.deploy is required")
	}
	switch desired.Ingress.Expose {
	case ExposePublic, ExposePrivate:
	default:
		return fmt.Errorf("ingress.expose must be public or private")
	}
	switch desired.Ingress.Tunnel {
	case TunnelNone, TunnelCloudflare, TunnelTailscaleFunnel:
	default:
		return fmt.Errorf("ingress.tunnel must be none, cloudflare, or tailscale-funnel")
	}
	for _, runtime := range desired.Features.Runtimes {
		if strings.TrimSpace(runtime) == "" {
			return fmt.Errorf("features.runtimes cannot contain empty values")
		}
	}
	for name, pkg := range desired.Packages {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("packages cannot contain empty names")
		}
		if strings.TrimSpace(pkg.Source) == "" {
			return fmt.Errorf("packages.%s.source is required", name)
		}
	}
	return nil
}

func validateVersion(scope string, version int) error {
	if version == 0 {
		return fmt.Errorf("%s version is required", scope)
	}
	if version > CurrentVersion {
		return fmt.Errorf("unsupported %s version %d", scope, version)
	}
	return nil
}

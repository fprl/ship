package state

import (
	"encoding/json"
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

type StateRoute struct {
	Host    string            `json:"host"`
	Type    string            `json:"type"`
	App     string            `json:"app,omitempty"`
	Port    *int              `json:"port,omitempty"`
	Root    string            `json:"root,omitempty"`
	To      string            `json:"to,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type HostState struct {
	Version int          `json:"version"`
	Routes  []StateRoute `json:"routes"`
}

type CloudflareRouteState struct {
	App         string `json:"app"`
	ZoneId      string `json:"zone_id"`
	DnsRecordId string `json:"dns_record_id"`
}

type CloudflareState struct {
	Version    int                             `json:"version"`
	AccountId  string                          `json:"account_id,omitempty"`
	TunnelId   string                          `json:"tunnel_id,omitempty"`
	TunnelName string                          `json:"tunnel_name,omitempty"`
	Routes     map[string]CloudflareRouteState `json:"routes"`
}

// Env vars and default paths
func StatePath() string {
	if p := os.Getenv("SIMPLE_VPS_STATE_PATH"); p != "" {
		return p
	}
	return "/etc/simple-vps/state.json"
}

func CloudflareStatePath() string {
	if p := os.Getenv("SIMPLE_VPS_CLOUDFLARE_STATE_PATH"); p != "" {
		return p
	}
	return "/etc/simple-vps/cloudflare.json"
}

// Load and Save helpers

func LoadState() (*HostState, error) {
	path := StatePath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &HostState{Version: 2, Routes: []StateRoute{}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state HostState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("invalid state file: %w", err)
	}
	if state.Version > 2 {
		return nil, fmt.Errorf("unsupported state version %d", state.Version)
	}
	if state.Routes == nil {
		state.Routes = []StateRoute{}
	}
	// Normalize all routes
	for i, r := range state.Routes {
		norm, err := NormalizeRoute(r)
		if err != nil {
			return nil, fmt.Errorf("invalid route entry: %w", err)
		}
		state.Routes[i] = *norm
	}
	state.Version = 2
	SortRoutes(state.Routes)
	return &state, nil
}

func WriteState(state *HostState) error {
	state.Version = 2
	SortRoutes(state.Routes)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(StatePath(), append(data, '\n'), 0644)
}

func LoadCloudflareState() (*CloudflareState, error) {
	path := CloudflareStatePath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &CloudflareState{Version: 1, Routes: make(map[string]CloudflareRouteState)}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state CloudflareState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("invalid Cloudflare state file: %w", err)
	}
	if state.Routes == nil {
		state.Routes = make(map[string]CloudflareRouteState)
	}
	state.Version = 1
	return &state, nil
}

func WriteCloudflareState(state *CloudflareState) error {
	state.Version = 1
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(CloudflareStatePath(), append(data, '\n'), 0600)
}

func AtomicWrite(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(dir, fmt.Sprintf(".%s.", filepath.Base(path)))
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer func() {
		if _, err := os.Stat(tmpName); err == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(content); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Normalizations

func NormalizeHost(value string) (string, error) {
	host := strings.TrimSpace(value)
	host = strings.ToLower(host)
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", errors.New("host cannot be empty")
	}
	// ValidateHost without lookarounds (matches manual function)
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

func NormalizePort(value interface{}) (int, error) {
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

func NormalizeApp(value interface{}) (string, error) {
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
		// App Root relative check
		appDir := "/var/apps"
		if p := os.Getenv("SIMPLE_VPS_APP_ROOT"); p != "" {
			appDir = p
		}
		base := filepath.Join(appDir, app)
		// Clean paths
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

func NormalizeRoute(r StateRoute) (*StateRoute, error) {
	host, err := NormalizeHost(r.Host)
	if err != nil {
		return nil, err
	}
	routeType := strings.TrimSpace(r.Type)
	routeType = strings.ToLower(routeType)
	if routeType == "" && r.Port != nil {
		routeType = "proxy"
	}
	app, err := NormalizeApp(r.App)
	if err != nil {
		return nil, err
	}
	headers, err := NormalizeHeaders(r.Headers)
	if err != nil {
		return nil, err
	}

	norm := &StateRoute{Host: host, Type: routeType, App: app, Headers: headers}
	switch routeType {
	case "proxy":
		if r.Port == nil {
			return nil, fmt.Errorf("port is required for proxy route %s", host)
		}
		p, err := NormalizePort(*r.Port)
		if err != nil {
			return nil, err
		}
		norm.Port = &p
	case "static":
		root, err := NormalizeRoot(r.Root, app)
		if err != nil {
			return nil, err
		}
		norm.Root = root
	case "redirect":
		to, err := NormalizeRedirectTarget(r.To)
		if err != nil {
			return nil, err
		}
		norm.To = to
		norm.Headers = nil // redirects do not support custom headers
	default:
		return nil, fmt.Errorf("invalid route type for %s: %s", host, routeType)
	}
	return norm, nil
}

func SortRoutes(routes []StateRoute) {
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Host < routes[j].Host
	})
}

func RouteIndex(routes []StateRoute, host string) int {
	for i, r := range routes {
		if r.Host == host {
			return i
		}
	}
	return -1
}

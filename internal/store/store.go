package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Store struct {
	Root string
}
type hostFileRaw struct {
	Version  int             `json:"version"`
	Desired  json.RawMessage `json:"desired"`
	Observed HostObserved    `json:"observed"`
	Meta     HostMeta        `json:"meta"`
}

func DefaultRoot() string {
	if root := os.Getenv("SIMPLE_VPS_STATE_DIR"); root != "" {
		return root
	}
	return "/etc/simple-vps"
}

func Default() Store {
	return Store{Root: DefaultRoot()}
}

func (s Store) root() string {
	if s.Root != "" {
		return s.Root
	}
	return DefaultRoot()
}

func (s Store) HostPath() string {
	return filepath.Join(s.root(), "host.json")
}

func (s Store) AppsPath() string {
	return filepath.Join(s.root(), "apps.json")
}

func (s Store) RoutesPath() string {
	return filepath.Join(s.root(), "routes.json")
}

func (s Store) CloudflarePath() string {
	return filepath.Join(s.root(), "providers", "cloudflare.json")
}

func (s Store) SecretsDir() string {
	return filepath.Join(s.root(), "secrets")
}

func (s Store) HostInstalled() (bool, error) {
	if _, err := os.Stat(s.HostPath()); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ReadHost returns os.IsNotExist(err) when host.json has not been created yet.
func (s Store) ReadHost() (*HostFile, error) {
	var file HostFile
	if err := readJSON(s.HostPath(), &file); err != nil {
		return nil, err
	}
	if err := validateVersion("host.json", file.Version); err != nil {
		return nil, err
	}
	normalizeHostFile(&file)
	if err := validateHostDesired(file.Desired); err != nil {
		return nil, fmt.Errorf("invalid host.json desired: %w", err)
	}
	return &file, nil
}

func (s Store) WriteHostDesired(desired HostDesired) error {
	normalizeHostDesired(&desired)
	if err := validateHostDesired(desired); err != nil {
		return fmt.Errorf("invalid host desired: %w", err)
	}

	file, err := s.readHostForDesiredWrite()
	if err != nil {
		return err
	}
	file.Desired = desired
	normalizeHostFile(&file)
	return writeHostFile(s.HostPath(), file)
}

func (s Store) WriteHostState(observed HostObserved, meta HostMeta) error {
	file, err := s.readHostRaw()
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("host.json is required before writing host state")
		}
		return err
	}

	var desired HostDesired
	if err := json.Unmarshal(file.Desired, &desired); err != nil {
		return fmt.Errorf("invalid host.json desired: %w", err)
	}
	normalizeHostDesired(&desired)
	if err := validateHostDesired(desired); err != nil {
		return fmt.Errorf("invalid host.json desired: %w", err)
	}

	normalizeHostObserved(&observed)
	return writeHostState(s.HostPath(), file.Desired, observed, meta)
}

func (s Store) ReadApps() (*AppsFile, error) {
	var file AppsFile
	if err := readJSON(s.AppsPath(), &file); err != nil {
		if os.IsNotExist(err) {
			return &AppsFile{Version: CurrentVersion, Apps: map[string]App{}}, nil
		}
		return nil, err
	}
	if err := validateVersion("apps.json", file.Version); err != nil {
		return nil, err
	}
	file.Version = CurrentVersion
	if file.Apps == nil {
		file.Apps = map[string]App{}
	}
	return &file, nil
}

func (s Store) WriteApps(file AppsFile) error {
	if err := validateVersion("apps.json", file.Version); err != nil {
		return err
	}
	normalizeAppsFile(&file)
	return writeJSON(s.AppsPath(), file, 0644)
}

func (s Store) RegisterApp(name string, path string) error {
	name, err := NormalizeRequiredApp(name)
	if err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = AppPath(name)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("app path must be absolute: %s", path)
	}
	apps, err := s.ReadApps()
	if err != nil {
		return err
	}
	app := apps.Apps[name]
	app.Path = filepath.Clean(path)
	apps.Apps[name] = app
	return s.WriteApps(*apps)
}

func (s Store) UnregisterApp(name string) error {
	name, err := NormalizeRequiredApp(name)
	if err != nil {
		return err
	}
	apps, err := s.ReadApps()
	if err != nil {
		return err
	}
	delete(apps.Apps, name)
	return s.WriteApps(*apps)
}

func (s Store) RegisterAppService(name string, service string) error {
	name, err := NormalizeRequiredApp(name)
	if err != nil {
		return err
	}
	service, err = NormalizeService(service)
	if err != nil {
		return err
	}
	apps, err := s.ReadApps()
	if err != nil {
		return err
	}
	app := apps.Apps[name]
	if app.Path == "" {
		app.Path = AppPath(name)
	}
	for _, existing := range app.Services {
		if existing == service {
			apps.Apps[name] = app
			return s.WriteApps(*apps)
		}
	}
	app.Services = append(app.Services, service)
	apps.Apps[name] = app
	return s.WriteApps(*apps)
}

func (s Store) UnregisterAppService(name string, service string) error {
	name, err := NormalizeRequiredApp(name)
	if err != nil {
		return err
	}
	service, err = NormalizeService(service)
	if err != nil {
		return err
	}
	apps, err := s.ReadApps()
	if err != nil {
		return err
	}
	app, ok := apps.Apps[name]
	if !ok {
		return nil
	}
	var next []string
	for _, existing := range app.Services {
		if existing != service {
			next = append(next, existing)
		}
	}
	app.Services = next
	apps.Apps[name] = app
	return s.WriteApps(*apps)
}

func (s Store) ReadRoutes() (*RoutesFile, error) {
	var file RoutesFile
	if err := readJSON(s.RoutesPath(), &file); err != nil {
		if os.IsNotExist(err) {
			return &RoutesFile{Version: CurrentVersion, Routes: []Route{}}, nil
		}
		return nil, err
	}
	if err := validateVersion("routes.json", file.Version); err != nil {
		return nil, err
	}
	if err := normalizeRoutesFile(&file); err != nil {
		return nil, err
	}
	return &file, nil
}

func (s Store) WriteRoutes(file RoutesFile) error {
	if err := validateVersion("routes.json", file.Version); err != nil {
		return err
	}
	if err := normalizeRoutesFile(&file); err != nil {
		return err
	}
	return writeJSON(s.RoutesPath(), file, 0644)
}

func (s Store) ReadCloudflare() (*CloudflareFile, error) {
	var file CloudflareFile
	if err := readJSON(s.CloudflarePath(), &file); err != nil {
		if os.IsNotExist(err) {
			return &CloudflareFile{Version: CurrentVersion, Routes: map[string]CloudflareRoute{}}, nil
		}
		return nil, err
	}
	if err := validateVersion("providers/cloudflare.json", file.Version); err != nil {
		return nil, err
	}
	file.Version = CurrentVersion
	if file.Routes == nil {
		file.Routes = map[string]CloudflareRoute{}
	}
	return &file, nil
}

func (s Store) WriteCloudflare(file CloudflareFile) error {
	if err := validateVersion("providers/cloudflare.json", file.Version); err != nil {
		return err
	}
	file.Version = CurrentVersion
	if file.Routes == nil {
		file.Routes = map[string]CloudflareRoute{}
	}
	return writeJSON(s.CloudflarePath(), file, 0600)
}
func (s Store) readHostForDesiredWrite() (HostFile, error) {
	var file HostFile
	if err := readJSON(s.HostPath(), &file); err != nil {
		if os.IsNotExist(err) {
			file = *newHostFile()
			return file, nil
		}
		return HostFile{}, err
	}
	if err := validateVersion("host.json", file.Version); err != nil {
		return HostFile{}, err
	}
	normalizeHostFile(&file)
	return file, nil
}

func (s Store) readHostRaw() (hostFileRaw, error) {
	var file hostFileRaw
	if err := readJSON(s.HostPath(), &file); err != nil {
		return hostFileRaw{}, err
	}
	if err := validateVersion("host.json", file.Version); err != nil {
		return hostFileRaw{}, err
	}
	if len(bytes.TrimSpace(file.Desired)) == 0 {
		return hostFileRaw{}, fmt.Errorf("host.json desired is required")
	}
	return file, nil
}

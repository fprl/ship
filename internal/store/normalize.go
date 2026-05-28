package store

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Exported regexes used outside the package:
//   - AppRe and NormalizeApp are consumed by `internal/cloudflare` for
//     per-route app-name validation.
//   - SystemUserRe is consumed by `internal/host` (the host-side
//     primitives package) for SUDO_USER validation.
//
// ServiceRe and HeaderNameRe used to back the legacy route/service
// normalizers; both went away with the apps.json/routes.json gut in
// PR #39 and the regexes followed.
var (
	AppRe        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	SystemUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}\$?$`)
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

// NormalizeApp keeps a tiny "is this a valid app name?" surface for
// the Cloudflare per-route call site. Treats nil and empty string as
// "no app set"; non-empty values must match AppRe.
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

func validateHostDesired(desired HostDesired) error {
	if strings.TrimSpace(desired.Users.Operator) == "" {
		return errors.New("users.operator is required")
	}
	if strings.TrimSpace(desired.Users.Deploy) == "" {
		return errors.New("users.deploy is required")
	}
	switch desired.Ingress.Expose {
	case ExposePublic, ExposePrivate:
	default:
		return errors.New("ingress.expose must be public or private")
	}
	switch desired.Ingress.Tunnel {
	case TunnelNone, TunnelCloudflare, TunnelTailscaleFunnel:
	default:
		return errors.New("ingress.tunnel must be none, cloudflare, or tailscale-funnel")
	}
	for _, runtime := range desired.Features.Runtimes {
		if strings.TrimSpace(runtime) == "" {
			return errors.New("features.runtimes cannot contain empty values")
		}
	}
	for name, pkg := range desired.Packages {
		if strings.TrimSpace(name) == "" {
			return errors.New("packages cannot contain empty names")
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

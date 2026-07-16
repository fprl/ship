package helper

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
)

func currentStaticRelease(app, env string) (string, error) {
	pointer, err := readActive(app, env)
	if err != nil {
		return "", fmt.Errorf("active static release not found; deploy before rollback or backup")
	}
	release := pointer.Release
	if err := validateRelease(release); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(identity.StaticDir(app, env), "releases", release)); err != nil {
		return "", fmt.Errorf("static release %s not found: %v", release, err)
	}
	return release, nil
}

func verifyStaticRelease(app, env, release string, routes map[string]config.Route) error {
	if err := validateRelease(release); err != nil {
		return err
	}
	for _, routeName := range sortedKeys(routes) {
		route := routes[routeName]
		if route.Serve == "" {
			continue
		}
		storageKey := routeName
		if route.StorageKey != "" {
			storageKey = route.StorageKey
		}
		path := filepath.Join(identity.StaticDir(app, env), "releases", release, config.RouteStorageName(storageKey))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("static release %s missing route %s: %v", release, routeName, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("static release %s route %s is not a directory", release, routeName)
		}
	}
	return nil
}

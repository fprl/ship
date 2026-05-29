package helper

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/identity"
)

type staticCurrentSnapshot struct {
	Target  string
	Existed bool
}

func snapshotStaticCurrent(app, env string) (staticCurrentSnapshot, error) {
	path := filepath.Join(identity.StaticDir(app, env), "current")
	return snapshotStaticCurrentAt(path)
}

func snapshotStaticCurrentAt(path string) (staticCurrentSnapshot, error) {
	target, err := os.Readlink(path)
	if err == nil {
		return staticCurrentSnapshot{Target: target, Existed: true}, nil
	}
	if os.IsNotExist(err) {
		return staticCurrentSnapshot{}, nil
	}
	return staticCurrentSnapshot{}, err
}

func restoreStaticCurrent(app, env string, snapshot staticCurrentSnapshot) error {
	path := filepath.Join(identity.StaticDir(app, env), "current")
	return restoreStaticCurrentAt(path, snapshot)
}

func restoreStaticCurrentAt(path string, snapshot staticCurrentSnapshot) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if snapshot.Existed {
		return os.Symlink(snapshot.Target, path)
	}
	return nil
}

func clearStaticCurrent(app, env string) error {
	path := filepath.Join(identity.StaticDir(app, env), "current")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func currentStaticRelease(app, env string) (string, error) {
	current := filepath.Join(identity.StaticDir(app, env), "current")
	target, err := os.Readlink(current)
	if err != nil {
		return "", fmt.Errorf("static current release not found; deploy before rollback or backup")
	}
	release := filepath.Base(target)
	if release == "." || release == string(os.PathSeparator) || release == "" {
		return "", fmt.Errorf("static current release target is invalid: %s", target)
	}
	if err := validateRelease(release); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(identity.StaticDir(app, env), "releases", release)); err != nil {
		return "", fmt.Errorf("static release %s not found: %v", release, err)
	}
	return release, nil
}

func staticReleases(app, env string) ([]imageRelease, error) {
	return staticReleasesAt(filepath.Join(identity.StaticDir(app, env), "releases"))
}

func staticReleasesAt(releasesDir string) ([]imageRelease, error) {
	entries, err := os.ReadDir(releasesDir)
	if err != nil {
		return nil, fmt.Errorf("static releases not found; deploy before rollback")
	}
	type releaseDir struct {
		name    string
		modTime int64
	}
	var dirs []releaseDir
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := validateRelease(entry.Name()); err != nil {
			return nil, err
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, releaseDir{name: entry.Name(), modTime: info.ModTime().UnixNano()})
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].modTime != dirs[j].modTime {
			return dirs[i].modTime > dirs[j].modTime
		}
		return dirs[i].name > dirs[j].name
	})
	out := make([]imageRelease, 0, len(dirs))
	for _, dir := range dirs {
		out = append(out, imageRelease{Release: dir.name})
	}
	return out, nil
}

func activateStaticRelease(app, env, release string) error {
	if err := validateRelease(release); err != nil {
		return err
	}
	staticDir := identity.StaticDir(app, env)
	releaseDir := filepath.Join(staticDir, "releases", release)
	if info, err := os.Stat(releaseDir); err != nil {
		return fmt.Errorf("static release %s not found: %v", release, err)
	} else if !info.IsDir() {
		return fmt.Errorf("static release %s is not a directory", release)
	}
	current := filepath.Join(staticDir, "current")
	if err := os.Remove(current); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(releaseDir, current); err != nil {
		return fmt.Errorf("update static current symlink: %v", err)
	}
	return nil
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
		path := filepath.Join(identity.StaticDir(app, env), "releases", release, routeName)
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

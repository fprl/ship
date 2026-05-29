package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/utils"
)

// appRollbackCmd swaps one (app, env) back to an older local release. The
// release artifact supplies the image/static tree, and the release manifest
// snapshot supplies the process and route shape.
type appRollbackCmd struct {
	App     string `arg:"" help:"App name."`
	Env     string `arg:"" help:"Env name."`
	Release string `arg:"" optional:"" help:"Release to run. Omitted = previous local release."`
	JSON    bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c appRollbackCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	if c.Release != "" {
		if err := validateRelease(c.Release); err != nil {
			utils.Die(err.Error(), 1)
		}
	}
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked()
	})
	return nil
}

func (c appRollbackCmd) runLocked() {
	currentApp, cleanup, err := loadAppliedAppContext(c.App, c.Env)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer cleanup()
	var result rollbackPayload
	switch currentApp.Shape {
	case config.ShapeContainer:
		result, err = c.rollbackContainer()
	case config.ShapeStatic:
		result, err = c.rollbackStatic()
	default:
		err = fmt.Errorf("unsupported app shape %q", currentApp.Shape)
	}
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if c.JSON {
		buf, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		fmt.Println(string(buf))
		return
	}
	fmt.Print(renderRollbackText(result))
}

func (c appRollbackCmd) rollbackContainer() (rollbackPayload, error) {
	containers, err := podmanPSContainers(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	current, err := currentRelease(runningProcesses(containersToProcesses(containers)))
	if err != nil && c.Release == "" {
		return rollbackPayload{}, err
	}
	images, err := podmanImages(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	images = releasesWithManifestSnapshots(c.App, c.Env, images)
	target, err := selectRollbackRelease(images, current, c.Release)
	if err != nil {
		return rollbackPayload{}, err
	}
	app, cleanup, err := loadReleaseAppContext(c.App, c.Env, target.Release)
	if err != nil {
		return rollbackPayload{}, err
	}
	defer cleanup()
	if app.Shape != config.ShapeContainer {
		return rollbackPayload{}, fmt.Errorf("release %s is %s, not container", target.Release, app.Shape)
	}
	envSnapshot, err := snapshotEnvFile(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	resolved, err := resolveEnv(c.App, c.Env, app.Vars, app.SecretRefs)
	if err != nil {
		return rollbackPayload{}, err
	}
	if err := writeEnvFile(c.App, c.Env, resolved); err != nil {
		return rollbackPayload{}, err
	}
	userID, groupID, err := hostUserIDs(identity.SystemUser(c.App, c.Env))
	if err != nil {
		return rollbackPayload{}, err
	}
	imageTag := identity.ImageTag(c.App, c.Env, target.Release)
	var started []string
	cleanupStarted := func() {
		removeContainers(started)
	}
	for _, procName := range sortedKeys(app.Processes) {
		proc := app.Processes[procName]
		if proc.Port == nil {
			for _, old := range processContainers(containers, procName, target.Release) {
				_, _ = utils.RunChecked("podman", []string{"rm", "-f", old}, "")
			}
		}
		if err := startProcess(c.App, c.Env, procName, proc, imageTag, userID, groupID, target.Release); err != nil {
			cleanupStarted()
			_ = restoreEnvFile(c.App, c.Env, envSnapshot)
			return rollbackPayload{}, err
		}
		started = append(started, identity.ContainerName(c.App, c.Env, procName, target.Release))
	}
	caddyPath := caddyfilePath(c.App, c.Env)
	prevFragment, prevExisted, err := snapshotCaddyFragment(caddyPath)
	if err != nil {
		cleanupStarted()
		_ = restoreEnvFile(c.App, c.Env, envSnapshot)
		return rollbackPayload{}, fmt.Errorf("snapshot existing fragment: %v", err)
	}
	if err := writeAppCaddyfile(c.App, c.Env, app, target.Release); err != nil {
		cleanupStarted()
		_ = restoreEnvFile(c.App, c.Env, envSnapshot)
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		return rollbackPayload{}, err
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}, ""); err != nil {
		cleanupStarted()
		_ = restoreEnvFile(c.App, c.Env, envSnapshot)
		if restoreErr := restoreCaddyFragment(caddyPath, prevFragment, prevExisted); restoreErr != nil {
			return rollbackPayload{}, fmt.Errorf("caddy validate rejected the rollback fragment AND restore failed (manual fix required at %s): %v (restore: %v)", caddyPath, err, restoreErr)
		}
		return rollbackPayload{}, fmt.Errorf("caddy validate after rollback: %v", err)
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, ""); err != nil {
		cleanupStarted()
		_ = restoreEnvFile(c.App, c.Env, envSnapshot)
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		return rollbackPayload{}, fmt.Errorf("caddy reload after rollback: %v", err)
	}
	if err := persistCurrentManifestFromRelease(c.App, c.Env, target.Release); err != nil {
		return rollbackPayload{}, err
	}
	removeContainers(containerNamesExceptRelease(containers, target.Release))

	return rollbackPayload{
		App:       c.App,
		Env:       c.Env,
		Previous:  current,
		Release:   target.Release,
		Processes: processNames(app.Processes),
	}, nil
}

func (c appRollbackCmd) rollbackStatic() (rollbackPayload, error) {
	current, err := currentStaticRelease(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	releases, err := staticReleases(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	releases = releasesWithManifestSnapshots(c.App, c.Env, releases)
	target, err := selectRollbackRelease(releases, current, c.Release)
	if err != nil {
		return rollbackPayload{}, err
	}
	app, cleanup, err := loadReleaseAppContext(c.App, c.Env, target.Release)
	if err != nil {
		return rollbackPayload{}, err
	}
	defer cleanup()
	if app.Shape != config.ShapeStatic {
		return rollbackPayload{}, fmt.Errorf("release %s is %s, not static", target.Release, app.Shape)
	}

	staticSnapshot, err := snapshotStaticCurrent(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	caddyPath := caddyfilePath(c.App, c.Env)
	prevFragment, prevExisted, err := snapshotCaddyFragment(caddyPath)
	if err != nil {
		return rollbackPayload{}, fmt.Errorf("snapshot existing fragment: %v", err)
	}
	if err := activateStaticRelease(c.App, c.Env, target.Release); err != nil {
		return rollbackPayload{}, err
	}
	if err := writeAppCaddyfile(c.App, c.Env, app, target.Release); err != nil {
		_ = restoreStaticCurrent(c.App, c.Env, staticSnapshot)
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		return rollbackPayload{}, err
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}, ""); err != nil {
		_ = restoreStaticCurrent(c.App, c.Env, staticSnapshot)
		if restoreErr := restoreCaddyFragment(caddyPath, prevFragment, prevExisted); restoreErr != nil {
			return rollbackPayload{}, fmt.Errorf("caddy validate rejected the rollback fragment AND restore failed (manual fix required at %s): %v (restore: %v)", caddyPath, err, restoreErr)
		}
		return rollbackPayload{}, fmt.Errorf("caddy validate after rollback: %v", err)
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, ""); err != nil {
		_ = restoreStaticCurrent(c.App, c.Env, staticSnapshot)
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		return rollbackPayload{}, fmt.Errorf("caddy reload after rollback: %v", err)
	}
	if err := persistCurrentManifestFromRelease(c.App, c.Env, target.Release); err != nil {
		return rollbackPayload{}, err
	}

	return rollbackPayload{App: c.App, Env: c.Env, Previous: current, Release: target.Release, Processes: []string{}}, nil
}

type rollbackPayload struct {
	App       string   `json:"app"`
	Env       string   `json:"env"`
	Previous  string   `json:"previous"`
	Release   string   `json:"release"`
	Processes []string `json:"processes"`
}

type imageRelease struct {
	Release string
	Image   string
}

type imageEntry struct {
	Repository string            `json:"Repository"`
	Tag        string            `json:"Tag"`
	Names      []string          `json:"Names"`
	Labels     map[string]string `json:"Labels"`
}

func podmanImages(app, env string) ([]imageRelease, error) {
	out, err := utils.RunChecked("podman", []string{"images", "--format", "json"}, "")
	if err != nil {
		return nil, fmt.Errorf("podman images: %v", err)
	}
	out = []byte(strings.TrimSpace(string(out)))
	if len(out) == 0 {
		return nil, nil
	}
	var entries []imageEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse podman images json: %v", err)
	}
	return imageReleasesFromEntries(app, env, entries), nil
}

func imageReleasesFromEntries(app, env string, entries []imageEntry) []imageRelease {
	var releases []imageRelease
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Labels["simple-vps.app"] != app || e.Labels["simple-vps.env"] != env {
			continue
		}
		if e.Labels["simple-vps.infra_id"] != identity.InfraID(app, env) {
			continue
		}
		release := e.Labels["simple-vps.release"]
		if release == "" {
			release = e.Tag
		}
		if release == "" || release == "<none>" || seen[release] {
			continue
		}
		if err := validateRelease(release); err != nil {
			continue
		}
		seen[release] = true
		releases = append(releases, imageRelease{Release: release, Image: identity.ImageTag(app, env, release)})
	}
	return releases
}

func currentRelease(processes []processStatus) (string, error) {
	if len(processes) == 0 {
		return "", fmt.Errorf("no processes running; deploy before rollback")
	}
	current := processes[0].Release
	if current == "" {
		return "", fmt.Errorf("running processes do not expose a release label; cannot choose rollback target")
	}
	for _, proc := range processes[1:] {
		if proc.Release != current {
			return "", fmt.Errorf("running processes are on different releases; pass an explicit release")
		}
	}
	return current, nil
}

func selectRollbackRelease(images []imageRelease, current, requested string) (imageRelease, error) {
	if requested != "" {
		for _, img := range images {
			if img.Release == requested {
				if requested == current {
					return imageRelease{}, fmt.Errorf("%s is already running", requested)
				}
				return img, nil
			}
		}
		return imageRelease{}, fmt.Errorf("release %s is not available locally", requested)
	}
	for _, img := range images {
		if img.Release != current {
			return img, nil
		}
	}
	return imageRelease{}, fmt.Errorf("no previous release available locally")
}

func releasesWithManifestSnapshots(app, env string, releases []imageRelease) []imageRelease {
	type releaseWithSnapshot struct {
		imageRelease
		modTime int64
	}
	var withSnapshots []releaseWithSnapshot
	for _, release := range releases {
		if err := validateRelease(release.Release); err != nil {
			continue
		}
		info, err := os.Stat(identity.ReleaseManifestFile(app, env, release.Release))
		if err != nil || info.IsDir() {
			continue
		}
		withSnapshots = append(withSnapshots, releaseWithSnapshot{
			imageRelease: release,
			modTime:      info.ModTime().UnixNano(),
		})
	}
	sort.Slice(withSnapshots, func(i, j int) bool {
		if withSnapshots[i].modTime != withSnapshots[j].modTime {
			return withSnapshots[i].modTime > withSnapshots[j].modTime
		}
		return withSnapshots[i].Release > withSnapshots[j].Release
	})
	out := make([]imageRelease, 0, len(withSnapshots))
	for _, release := range withSnapshots {
		out = append(out, release.imageRelease)
	}
	return out
}

func loadAppliedAppContext(app, env string) (*config.AppContext, func(), error) {
	return loadAppContextFromManifest(app, env, identity.ManifestFile(app, env), "deploy once before rollback")
}

func loadReleaseAppContext(app, env, release string) (*config.AppContext, func(), error) {
	if err := validateRelease(release); err != nil {
		return nil, func() {}, err
	}
	return loadAppContextFromManifest(app, env, identity.ReleaseManifestFile(app, env, release), "release manifest snapshot is missing")
}

func loadAppContextFromManifest(app, env, manifestPath, missingHint string) (*config.AppContext, func(), error) {
	if _, err := os.Stat(manifestPath); err != nil {
		return nil, func() {}, fmt.Errorf("applied manifest not found at %s; %s", manifestPath, missingHint)
	}
	tmp, err := os.MkdirTemp("", "simple-vps-rollback-manifest-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "simple-vps.toml"), data, 0644); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if err := createStaticServePlaceholders(tmp, env); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	ctx, err := config.LoadAppContext(tmp, env)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if ctx.AppName != app {
		cleanup()
		return nil, func() {}, fmt.Errorf("applied manifest names app %s, expected %s", ctx.AppName, app)
	}
	return ctx, cleanup, nil
}

func persistCurrentManifestFromRelease(app, env, release string) error {
	if err := validateRelease(release); err != nil {
		return err
	}
	data, err := os.ReadFile(identity.ReleaseManifestFile(app, env, release))
	if err != nil {
		return fmt.Errorf("read release manifest snapshot: %v", err)
	}
	current := identity.ManifestFile(app, env)
	if err := os.WriteFile(current, data, 0644); err != nil {
		return fmt.Errorf("write applied manifest: %v", err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", current}, ""); err != nil {
		return fmt.Errorf("chown applied manifest: %v", err)
	}
	return nil
}

type envFileSnapshot struct {
	Data    []byte
	Existed bool
}

func snapshotEnvFile(app, env string) (envFileSnapshot, error) {
	path := identity.EnvFile(app, env)
	data, err := os.ReadFile(path)
	if err == nil {
		return envFileSnapshot{Data: data, Existed: true}, nil
	}
	if os.IsNotExist(err) {
		return envFileSnapshot{}, nil
	}
	return envFileSnapshot{}, err
}

func restoreEnvFile(app, env string, snapshot envFileSnapshot) error {
	path := identity.EnvFile(app, env)
	if !snapshot.Existed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.WriteFile(path, snapshot.Data, 0600); err != nil {
		return err
	}
	user := identity.SystemUser(app, env)
	if _, err := utils.RunChecked("chown", []string{user + ":" + user, path}, ""); err != nil {
		return fmt.Errorf("chown env file: %v", err)
	}
	return nil
}

func containerNamesExceptRelease(entries []containerEntry, release string) []string {
	var names []string
	for _, e := range entries {
		if e.Labels["simple-vps.process"] == "release" {
			continue
		}
		if e.Labels["simple-vps.release"] == release {
			continue
		}
		if len(e.Names) > 0 {
			names = append(names, e.Names[0])
		}
	}
	return names
}

func createStaticServePlaceholders(root, env string) error {
	manifest, err := config.ReadManifest(root)
	if err != nil {
		return err
	}
	create := func(routes map[string]config.Route) error {
		for _, route := range routes {
			if route.Serve == "" {
				continue
			}
			if err := os.MkdirAll(filepath.Join(root, route.Serve), 0755); err != nil {
				return err
			}
		}
		return nil
	}
	if err := create(manifest.Routes); err != nil {
		return err
	}
	if block, ok := manifest.Env[env]; ok {
		if err := create(block.Routes); err != nil {
			return err
		}
	}
	return nil
}

func processNames(processes map[string]config.Process) []string {
	return sortedKeys(processes)
}

func renderRollbackText(payload rollbackPayload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Rolled back %s (%s) from %s to %s\n", payload.App, payload.Env, payload.Previous, payload.Release)
	for _, proc := range payload.Processes {
		fmt.Fprintf(&b, "  %-12s running\n", proc)
	}
	return b.String()
}

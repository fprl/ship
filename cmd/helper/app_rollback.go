package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

// appRollbackCmd swaps one (app, env) back to an older local release. The
// release artifact supplies the image/static tree, and the release manifest
// snapshot supplies the process and route shape.
type appRollbackCmd struct {
	App           string `arg:"" help:"App name."`
	Env           string `arg:"" help:"Env name."`
	Release       string `arg:"" optional:"" help:"Release to run. Omitted = previous local release."`
	SSHKeyComment string `name:"ssh-key-comment" help:"SSH public key comment for the deploying key."`
	GitAuthor     string `name:"git-author" help:"Git author configured by the deploying client."`
}

func (c appRollbackCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if c.Release != "" {
		if err := validateRelease(c.Release); err != nil {
			utils.DieError(err, 1)
		}
	}
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked()
	})
	return nil
}

func (c appRollbackCmd) runLocked() {
	startedAt := time.Now().UTC()
	currentApp, cleanup, err := loadAppliedAppContext(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer cleanup()
	result, err := c.rollbackRelease(currentApp)
	if err != nil {
		utils.DieError(err, 1)
	}
	if err := appendDeployJournalEntry(c.App, c.Env, deployJournalEntry{
		SchemaVersion:    deployJournalSchemaVersion,
		App:              c.App,
		Env:              c.Env,
		Outcome:          "rolled_back",
		StartedAt:        startedAt.Format(time.RFC3339Nano),
		EndedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		PreviousRelease:  result.Previous,
		AttemptedRelease: result.Release,
		Identity:         c.actor(),
	}, nil); err != nil {
		utils.DieError(err, 1)
	}
	fmt.Print(renderRollbackText(result))
}

func (c appRollbackCmd) actor() deployIdentity {
	return deployActor(c.SSHKeyComment, c.GitAuthor)
}

func (c appRollbackCmd) rollbackRelease(currentApp *config.AppContext) (rollbackPayload, error) {
	current, err := activeRelease(c.App, c.Env, currentApp)
	if err != nil && c.Release == "" {
		return rollbackPayload{}, err
	}
	releases, err := availableRollbackReleases(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	target, err := selectRollbackRelease(releases, current, c.Release)
	if err != nil {
		return rollbackPayload{}, err
	}
	app, cleanup, err := loadReleaseAppContext(c.App, c.Env, target.Release)
	if err != nil {
		return rollbackPayload{}, err
	}
	defer cleanup()
	if err := verifyReleaseArtifacts(c.App, c.Env, target.Release, app); err != nil {
		return rollbackPayload{}, err
	}
	return c.rollbackToTarget(current, target.Release, app)
}

func activeRelease(app, env string, ctx *config.AppContext) (string, error) {
	if ctx.NeedsImage {
		containers, err := podmanPSContainers(app, env)
		if err != nil {
			return "", err
		}
		return currentRelease(runningProcesses(containersToProcesses(containers)))
	}
	if ctx.HasStaticRoutes {
		return currentStaticRelease(app, env)
	}
	return "", fmt.Errorf("no active release found")
}

func (c appRollbackCmd) rollbackToTarget(current, targetRelease string, app *config.AppContext) (rollbackPayload, error) {
	containers, err := podmanPSContainers(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	envSnapshot, err := snapshotEnvFile(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	staticSnapshot, err := snapshotStaticCurrent(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	var started []string
	cleanupStarted := func() {
		removeContainers(started)
	}

	if app.HasStaticRoutes {
		if err := activateStaticRelease(c.App, c.Env, targetRelease); err != nil {
			return rollbackPayload{}, err
		}
	} else if staticSnapshot.Existed {
		if err := clearStaticCurrent(c.App, c.Env); err != nil {
			return rollbackPayload{}, err
		}
	}

	if app.NeedsImage {
		startedResult, err := startReleaseProcesses(startReleaseProcessesParams{
			App:     c.App,
			Env:     c.Env,
			Release: targetRelease,
			Context: app,
			BeforeProcess: func(procName string, proc config.Process) error {
				if proc.Port != nil {
					return nil
				}
				for _, old := range processContainers(containers, procName, targetRelease) {
					_, _ = utils.RunChecked("podman", []string{"rm", "-f", old}, "")
				}
				return nil
			},
		})
		started = append(started, startedResult.Started...)
		if err != nil {
			cleanupStarted()
			_ = restoreEnvFile(c.App, c.Env, envSnapshot)
			_ = restoreStaticCurrent(c.App, c.Env, staticSnapshot)
			return rollbackPayload{}, err
		}
	}
	caddyPath := caddyfilePath(c.App, c.Env)
	prevFragment, prevExisted, err := snapshotCaddyFragment(caddyPath)
	if err != nil {
		cleanupStarted()
		_ = restoreEnvFile(c.App, c.Env, envSnapshot)
		_ = restoreStaticCurrent(c.App, c.Env, staticSnapshot)
		return rollbackPayload{}, fmt.Errorf("snapshot existing fragment: %v", err)
	}
	if err := writeAppCaddyfile(c.App, c.Env, app, targetRelease); err != nil {
		cleanupStarted()
		_ = restoreEnvFile(c.App, c.Env, envSnapshot)
		_ = restoreStaticCurrent(c.App, c.Env, staticSnapshot)
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		return rollbackPayload{}, err
	}
	if err := reloadCaddyOrRestore(caddyPath, prevFragment, prevExisted); err != nil {
		cleanupStarted()
		_ = restoreEnvFile(c.App, c.Env, envSnapshot)
		_ = restoreStaticCurrent(c.App, c.Env, staticSnapshot)
		var caddyErr caddyReloadStageError
		if errors.As(err, &caddyErr) {
			switch {
			case caddyErr.Stage == "validate" && caddyErr.RestoreErr != nil:
				return rollbackPayload{}, fmt.Errorf("caddy validate rejected the rollback fragment AND restore failed (manual fix required at %s): %v (restore: %v)", caddyPath, caddyErr.Err, caddyErr.RestoreErr)
			case caddyErr.Stage == "validate":
				return rollbackPayload{}, fmt.Errorf("caddy validate after rollback: %v", caddyErr.Err)
			case caddyErr.Stage == "reload":
				return rollbackPayload{}, fmt.Errorf("caddy reload after rollback: %v", caddyErr.Err)
			}
		}
		return rollbackPayload{}, err
	}
	if err := persistCurrentManifestFromRelease(c.App, c.Env, targetRelease); err != nil {
		return rollbackPayload{}, err
	}
	if app.NeedsImage {
		removeContainers(containerNamesExceptRelease(containers, targetRelease))
	} else {
		removeContainers(appContainerNames(containers))
	}

	return rollbackPayload{
		App:       c.App,
		Env:       c.Env,
		Previous:  current,
		Release:   targetRelease,
		Processes: processNames(app.Processes),
	}, nil
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
		if e.Labels["ship.app"] != app || e.Labels["ship.env"] != env {
			continue
		}
		if e.Labels["ship.infra_id"] != identity.InfraID(app, env) {
			continue
		}
		release := e.Labels["ship.release"]
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

func availableRollbackReleases(app, env string) ([]imageRelease, error) {
	releases, err := releaseSnapshots(app, env)
	if err != nil {
		return nil, err
	}
	images, err := podmanImages(app, env)
	if err != nil {
		return nil, err
	}
	imageByRelease := map[string]bool{}
	for _, image := range images {
		imageByRelease[image.Release] = true
	}
	var available []imageRelease
	for _, release := range releases {
		ctx, cleanup, err := loadReleaseAppContext(app, env, release.Release)
		if err != nil {
			continue
		}
		err = verifyReleaseArtifactsWithImages(app, env, release.Release, ctx, imageByRelease)
		cleanup()
		if err != nil {
			continue
		}
		available = append(available, release)
	}
	return available, nil
}

func releaseSnapshots(app, env string) ([]imageRelease, error) {
	releaseDir := identity.ReleaseDir(app, env)
	entries, err := os.ReadDir(releaseDir)
	if err != nil {
		return nil, fmt.Errorf("release manifest snapshots not found; deploy before rollback")
	}
	type releaseWithSnapshot struct {
		release string
		modTime int64
	}
	var withSnapshots []releaseWithSnapshot
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		release := entry.Name()
		if err := validateRelease(release); err != nil {
			continue
		}
		info, err := os.Stat(identity.ReleaseManifestFile(app, env, release))
		if err != nil || info.IsDir() {
			continue
		}
		if _, err := readReleaseMetadata(app, env, release); err != nil {
			return nil, err
		}
		withSnapshots = append(withSnapshots, releaseWithSnapshot{
			release: release,
			modTime: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(withSnapshots, func(i, j int) bool {
		if withSnapshots[i].modTime != withSnapshots[j].modTime {
			return withSnapshots[i].modTime > withSnapshots[j].modTime
		}
		return withSnapshots[i].release > withSnapshots[j].release
	})
	out := make([]imageRelease, 0, len(withSnapshots))
	for _, release := range withSnapshots {
		out = append(out, imageRelease{Release: release.release, Image: identity.ImageTag(app, env, release.release)})
	}
	return out, nil
}

func verifyReleaseArtifacts(app, env, release string, ctx *config.AppContext) error {
	images, err := podmanImages(app, env)
	if err != nil {
		return err
	}
	imageByRelease := map[string]bool{}
	for _, image := range images {
		imageByRelease[image.Release] = true
	}
	return verifyReleaseArtifactsWithImages(app, env, release, ctx, imageByRelease)
}

func verifyReleaseArtifactsWithImages(app, env, release string, ctx *config.AppContext, imageByRelease map[string]bool) error {
	if ctx.NeedsImage && !imageByRelease[release] {
		return fmt.Errorf("release %s image is not available locally", release)
	}
	if ctx.HasStaticRoutes {
		if err := verifyStaticRelease(app, env, release, ctx.Routes); err != nil {
			return err
		}
	}
	return nil
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
	tmp, err := os.MkdirTemp("", "ship-rollback-manifest-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "ship.toml"), data, 0644); err != nil {
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
	if err := store.AtomicWrite(current, data, 0644); err != nil {
		return fmt.Errorf("write applied manifest: %v", err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", current}, ""); err != nil {
		return fmt.Errorf("chown applied manifest: %v", err)
	}
	return nil
}

func snapshotCurrentManifest(app, env string) (fileSnapshot, error) {
	return snapshotFile(identity.ManifestFile(app, env))
}

func restoreCurrentManifest(app, env string, snapshot fileSnapshot) error {
	path := identity.ManifestFile(app, env)
	if err := restoreFile(path, snapshot, 0644); err != nil {
		return err
	}
	if !snapshot.Existed {
		return nil
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", path}, ""); err != nil {
		return fmt.Errorf("chown restored manifest: %v", err)
	}
	return nil
}

type fileSnapshot struct {
	Data    []byte
	Existed bool
}

func snapshotFile(path string) (fileSnapshot, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return fileSnapshot{Data: data, Existed: true}, nil
	}
	if os.IsNotExist(err) {
		return fileSnapshot{}, nil
	}
	return fileSnapshot{}, err
}

func restoreFile(path string, snapshot fileSnapshot, mode os.FileMode) error {
	if !snapshot.Existed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.WriteFile(path, snapshot.Data, mode); err != nil {
		return err
	}
	return nil
}

func snapshotEnvFile(app, env string) (fileSnapshot, error) {
	return snapshotFile(identity.EnvFile(app, env))
}

func restoreEnvFile(app, env string, snapshot fileSnapshot) error {
	path := identity.EnvFile(app, env)
	if err := restoreFile(path, snapshot, 0600); err != nil {
		return err
	}
	if !snapshot.Existed {
		return nil
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
		if isEphemeralProcess(e.Labels["ship.process"]) {
			continue
		}
		if e.Labels["ship.release"] == release {
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

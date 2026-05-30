package helper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/host"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/secrets"
	"github.com/fprl/simple-vps/internal/utils"
)

// appApplyCmd is the per-env deploy primitive. Given a
// streamed source tarball and a manifest, it:
//
//  1. Validates the manifest.
//  2. Resolves vars/secrets into runtime/.env for container apps.
//  3. Builds the image or snapshots static assets.
//  4. Starts new versioned process containers and verifies web health.
//  5. Synthesizes a Caddyfile fragment, validates, reloads, and only
//     then removes old routed containers.
type appApplyCmd struct {
	App        string `arg:"" help:"App name."`
	Env        string `arg:"" help:"Env name."`
	Tarball    string `name:"tarball" required:"" help:"Path to the streamed source tarball."`
	Manifest   string `name:"manifest" required:"" help:"Path to the uploaded simple-vps.toml."`
	SHA        string `name:"sha" required:"" help:"Release identifier."`
	Dirty      bool   `name:"dirty" help:"Mark this release as built from a dirty worktree snapshot."`
	BaseCommit string `name:"base-commit" required:"" help:"Git commit the release is based on."`
	CreatedAt  string `name:"created-at" required:"" help:"Release creation time in RFC3339."`
	Rebuild    bool   `name:"rebuild" help:"Pass --no-cache --pull=always to podman build."`
}

type applyReleaseResult struct {
	containersToRemove []string
	startedContainers  []string
	staticSnapshot     *staticCurrentSnapshot
	staticReleaseDir   string
	staticReleaseNew   bool
}

func (c appApplyCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := validateRelease(c.SHA); err != nil {
		utils.Die(err.Error(), 1)
	}
	if _, err := c.releaseMetadata(); err != nil {
		utils.Die(err.Error(), 1)
	}
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked()
	})
	return nil
}

func (c appApplyCmd) runLocked() {
	if err := c.runLockedE(); err != nil {
		utils.Die(err.Error(), 1)
	}
}

func (c appApplyCmd) runLockedE() error {
	ctxDir, err := c.prepareApplyContext()
	if err != nil {
		return err
	}
	defer os.RemoveAll(ctxDir)

	app, err := c.loadApplyContext(ctxDir)
	if err != nil {
		return err
	}

	meta, err := c.releaseMetadata()
	if err != nil {
		return err
	}
	if err := persistReleaseSnapshot(c.App, c.Env, c.SHA, filepath.Join(ctxDir, "simple-vps.toml"), meta); err != nil {
		return err
	}
	releaseSnapshotActive := false
	defer func() {
		if !releaseSnapshotActive {
			_ = removeReleaseSnapshot(c.App, c.Env, c.SHA)
		}
	}()

	result, err := c.applyRelease(ctxDir, app)
	if err != nil {
		return err
	}

	if err := c.switchTraffic(app, result); err != nil {
		result.cleanupFailed(c.App, c.Env)
		return err
	}
	releaseSnapshotActive = true
	if err := persistCurrentManifestFromRelease(c.App, c.Env, c.SHA); err != nil {
		return err
	}
	removeContainers(result.containersToRemove)

	fmt.Printf("Deployed %s (%s) at %s\n", c.App, c.Env, c.SHA)
	return nil
}

func (c appApplyCmd) releaseMetadata() (releaseMetadata, error) {
	return newReleaseMetadata(c.SHA, c.Dirty, c.BaseCommit, c.CreatedAt)
}

func (c appApplyCmd) prepareApplyContext() (string, error) {
	// host.ValidateDeployTmpSource resolves symlinks, ensures the
	// path is a regular file under the deploy tmp root, and (if invoked
	// via sudo) verifies the file is owned by the deploying user — so a
	// malicious local user can't leave a file behind for the helper to
	// pick up.
	tarball, err := host.ValidateDeployTmpSource(c.Tarball)
	if err != nil {
		return "", err
	}
	manifestPath, err := host.ValidateDeployTmpSource(c.Manifest)
	if err != nil {
		return "", err
	}

	// Manifest sits in a temp dir created by the client; CheckManifest
	// reads the rest of the working tree it expects (Dockerfile) from
	// the SAME directory. So we extract the tarball alongside the
	// uploaded manifest into a context dir and run the validator there.
	ctxDir, err := os.MkdirTemp(host.DeployTmpDir(), "ctx-")
	if err != nil {
		return "", err
	}

	if _, err := utils.RunChecked("tar", []string{"-xf", tarball, "-C", ctxDir}, ""); err != nil {
		_ = os.RemoveAll(ctxDir)
		return "", fmt.Errorf("extract tarball: %v", err)
	}
	// The uploaded manifest is authoritative — overwrite any manifest
	// that might have been in the tarball.
	if _, err := utils.RunChecked("install", []string{"-m", "0644", manifestPath, filepath.Join(ctxDir, "simple-vps.toml")}, ""); err != nil {
		_ = os.RemoveAll(ctxDir)
		return "", fmt.Errorf("install manifest: %v", err)
	}
	return ctxDir, nil
}

func (c appApplyCmd) loadApplyContext(ctxDir string) (*config.AppContext, error) {
	checkErrors, _, err := config.CheckManifest(ctxDir, c.Env)
	if err != nil {
		return nil, err
	}
	if len(checkErrors) > 0 {
		return nil, fmt.Errorf("manifest invalid: %s", strings.Join(checkErrors, "; "))
	}
	app, err := config.LoadAppContext(ctxDir, c.Env)
	if err != nil {
		return nil, err
	}
	if app.AppName != c.App {
		return nil, fmt.Errorf("uploaded manifest names app %s, expected %s", app.AppName, c.App)
	}
	if err := writeEnvIdentity(c.App, c.Env); err != nil {
		return nil, err
	}
	return app, nil
}

func (c appApplyCmd) applyRelease(ctxDir string, app *config.AppContext) (applyReleaseResult, error) {
	var result applyReleaseResult
	success := false
	defer func() {
		if !success {
			result.cleanupFailed(c.App, c.Env)
		}
	}()

	if app.HasStaticRoutes {
		snapshot, err := snapshotStaticCurrent(c.App, c.Env)
		if err != nil {
			return applyReleaseResult{}, fmt.Errorf("snapshot static current: %v", err)
		}
		result.staticSnapshot = &snapshot
		releaseDir, isNew, err := c.applyStatic(ctxDir, app)
		result.staticReleaseDir = releaseDir
		result.staticReleaseNew = isNew
		if err != nil {
			return applyReleaseResult{}, err
		}
	} else if snapshot, err := snapshotStaticCurrent(c.App, c.Env); err == nil && snapshot.Existed {
		result.staticSnapshot = &snapshot
		if err := clearStaticCurrent(c.App, c.Env); err != nil {
			return applyReleaseResult{}, err
		}
	} else if err != nil {
		return applyReleaseResult{}, fmt.Errorf("snapshot static current: %v", err)
	}

	existing, err := podmanPSContainers(c.App, c.Env)
	if err != nil {
		return applyReleaseResult{}, err
	}
	if app.NeedsImage {
		containerResult, err := c.applyContainer(ctxDir, app, existing)
		if err != nil {
			return applyReleaseResult{}, err
		}
		result.containersToRemove = containerResult.containersToRemove
		result.startedContainers = containerResult.startedContainers
	} else {
		result.containersToRemove = appContainerNames(existing)
	}

	success = true
	return result, nil
}

func (c appApplyCmd) switchTraffic(app *config.AppContext, result applyReleaseResult) error {
	// 6. Write the per-app Caddyfile fragment (`reverse_proxy
	// http://<container>:<process-port>`), validate the full Caddyfile
	// inside the Caddy container, then reload Caddy in place. The
	// fragment lives under `/etc/caddy/conf.d/` which the main Caddyfile
	// imports; we never `caddy reload --config <fragment>` because that
	// would *replace* the active config with just this app.
	//
	// Snapshot the previous fragment first: if validate rejects the new
	// one we restore the old. A previously-healthy app would otherwise
	// lose its route on the next reload from anywhere.
	caddyPath := caddyfilePath(c.App, c.Env)
	prevFragment, prevExisted, err := snapshotCaddyFragment(caddyPath)
	if err != nil {
		return fmt.Errorf("snapshot existing fragment: %v", err)
	}
	if err := writeAppCaddyfile(c.App, c.Env, app, c.SHA); err != nil {
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		if result.staticSnapshot != nil {
			_ = restoreStaticCurrent(c.App, c.Env, *result.staticSnapshot)
		}
		return err
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}, ""); err != nil {
		if restoreErr := restoreCaddyFragment(caddyPath, prevFragment, prevExisted); restoreErr != nil {
			return fmt.Errorf("caddy validate rejected the fragment AND restore failed (manual fix required at %s): %v (restore: %v)", caddyPath, err, restoreErr)
		}
		if result.staticSnapshot != nil {
			_ = restoreStaticCurrent(c.App, c.Env, *result.staticSnapshot)
		}
		return fmt.Errorf("caddy validate rejected the fragment, restored previous: %v", err)
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, ""); err != nil {
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		if result.staticSnapshot != nil {
			_ = restoreStaticCurrent(c.App, c.Env, *result.staticSnapshot)
		}
		return fmt.Errorf("caddy reload: %v", err)
	}
	return nil
}

func removeContainers(names []string) {
	for _, name := range names {
		_, _ = utils.RunChecked("podman", []string{"rm", "-f", name}, "")
	}
}

func (r applyReleaseResult) cleanupFailed(app, env string) {
	removeContainers(r.startedContainers)
	if r.staticSnapshot != nil {
		_ = restoreStaticCurrent(app, env, *r.staticSnapshot)
	}
	if r.staticReleaseNew && r.staticReleaseDir != "" {
		_ = os.RemoveAll(r.staticReleaseDir)
	}
}

func persistReleaseSnapshot(app, env, release, manifestPath string, meta releaseMetadata) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read applied manifest: %v", err)
	}
	if err := writeManifestSnapshot(app, env, release, data); err != nil {
		return err
	}
	if err := writeReleaseMetadata(app, env, meta); err != nil {
		return err
	}
	return nil
}

func removeReleaseSnapshot(app, env, release string) error {
	if err := validateRelease(release); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(identity.ReleaseDir(app, env), release))
}

func writeManifestSnapshot(app, env, release string, data []byte) error {
	if err := validateRelease(release); err != nil {
		return err
	}
	dst := identity.ReleaseManifestFile(app, env, release)
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("mkdir release manifest dir: %v", err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return fmt.Errorf("write release manifest: %v", err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", dst}, ""); err != nil {
		return fmt.Errorf("chown release manifest: %v", err)
	}
	return nil
}

type containerApplyResult struct {
	containersToRemove []string
	startedContainers  []string
}

func (c appApplyCmd) applyContainer(ctxDir string, app *config.AppContext, existing []containerEntry) (containerApplyResult, error) {
	if len(app.Processes) == 0 {
		return containerApplyResult{}, fmt.Errorf("manifest must declare at least one [processes.<name>] block")
	}
	resolved, err := resolveEnv(c.App, c.Env, app.Vars, app.SecretRefs)
	if err != nil {
		return containerApplyResult{}, err
	}
	if err := writeEnvFile(c.App, c.Env, resolved); err != nil {
		return containerApplyResult{}, err
	}

	userID, groupID, err := hostUserIDs(identity.SystemUser(c.App, c.Env))
	if err != nil {
		return containerApplyResult{}, err
	}

	imageTag := identity.ImageTag(c.App, c.Env, c.SHA)
	buildArgs := podmanBuildArgs(c.App, c.Env, imageTag, c.SHA, filepath.Join(ctxDir, "Dockerfile"), ctxDir, c.Rebuild)
	if _, err := utils.RunChecked("podman", buildArgs, ""); err != nil {
		return containerApplyResult{}, fmt.Errorf("podman build: %v", err)
	}

	if app.Deploy.Release != "" {
		if err := runReleaseCommand(c.App, c.Env, app.Deploy.Release, imageTag, userID, groupID, c.SHA); err != nil {
			return containerApplyResult{}, err
		}
	}

	var started []string
	containersToRemove := containersForRemovedProcesses(existing, app.Processes)
	for _, processName := range sortedKeys(app.Processes) {
		proc := app.Processes[processName]
		if proc.Port == nil {
			for _, old := range processContainers(existing, processName, "") {
				_, _ = utils.RunChecked("podman", []string{"rm", "-f", old}, "")
			}
		}
		containerName := identity.ContainerName(c.App, c.Env, processName, c.SHA)
		started = append(started, containerName)
		if err := startProcess(c.App, c.Env, processName, proc, imageTag, userID, groupID, c.SHA); err != nil {
			removeContainers(started)
			return containerApplyResult{}, err
		}
		if proc.Port != nil {
			containersToRemove = append(containersToRemove, processContainers(existing, processName, c.SHA)...)
		}
	}
	return containerApplyResult{
		containersToRemove: uniqueContainerNames(containersToRemove),
		startedContainers:  uniqueContainerNames(started),
	}, nil
}

func containersForRemovedProcesses(entries []containerEntry, next map[string]config.Process) []string {
	var names []string
	for _, e := range entries {
		process := e.Labels["simple-vps.process"]
		if process == "" || process == "release" {
			continue
		}
		if _, ok := next[process]; ok {
			continue
		}
		if len(e.Names) > 0 {
			names = append(names, e.Names[0])
		}
	}
	return uniqueContainerNames(names)
}

func appContainerNames(entries []containerEntry) []string {
	var names []string
	for _, e := range entries {
		process := e.Labels["simple-vps.process"]
		if process == "" || process == "release" {
			continue
		}
		if len(e.Names) > 0 {
			names = append(names, e.Names[0])
		}
	}
	return uniqueContainerNames(names)
}

func uniqueContainerNames(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (c appApplyCmd) applyStatic(ctxDir string, app *config.AppContext) (string, bool, error) {
	if err := validateRelease(c.SHA); err != nil {
		return "", false, err
	}
	staticDir := identity.StaticDir(c.App, c.Env)
	releaseDir := filepath.Join(identity.StaticDir(c.App, c.Env), "releases", c.SHA)
	if err := os.MkdirAll(filepath.Dir(releaseDir), 0755); err != nil {
		return "", false, err
	}
	if _, err := os.Stat(releaseDir); err == nil {
		if _, manifestErr := os.Stat(identity.ReleaseManifestFile(c.App, c.Env, c.SHA)); manifestErr == nil {
			if err := verifyStaticRelease(c.App, c.Env, c.SHA, app.Routes); err != nil {
				return "", false, err
			}
			if err := activateStaticRelease(c.App, c.Env, c.SHA); err != nil {
				return "", false, err
			}
			return releaseDir, false, nil
		}
		if err := os.RemoveAll(releaseDir); err != nil {
			return "", false, err
		}
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	stageDir := filepath.Join(staticDir, ".staging-"+c.SHA)
	if err := os.RemoveAll(stageDir); err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		return "", false, err
	}
	staged := false
	defer func() {
		if !staged {
			_ = os.RemoveAll(stageDir)
		}
	}()
	for _, routeName := range sortedKeys(app.Routes) {
		route := app.Routes[routeName]
		if route.Serve == "" {
			continue
		}
		src := filepath.Join(ctxDir, route.Serve)
		dst := filepath.Join(stageDir, routeName)
		if err := os.MkdirAll(dst, 0755); err != nil {
			return "", false, err
		}
		if _, err := utils.RunChecked("cp", []string{"-a", src + "/.", dst + "/"}, ""); err != nil {
			return "", false, fmt.Errorf("copy static route %s: %v", routeName, err)
		}
	}
	if err := os.Rename(stageDir, releaseDir); err != nil {
		return "", false, fmt.Errorf("publish static release: %v", err)
	}
	staged = true
	if err := activateStaticRelease(c.App, c.Env, c.SHA); err != nil {
		return releaseDir, true, err
	}
	return releaseDir, true, nil
}

func renderEnvFile(vals map[string]string) string {
	var lines []string
	for _, k := range sortedKeys(vals) {
		lines = append(lines, fmt.Sprintf("%s=%s", k, vals[k]))
	}
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	return content
}

// resolveEnv merges literal manifest env values with the runtime
// values pulled from the per-(app, env, key) secret store. A missing
// secret fails the whole resolution — no half-applied env file
// reaches the container, and no manifest-vs-store conflict is
// silently chosen for the user.
//
// Manifest literals and secret refs are guaranteed disjoint by
// `config.splitEnvBlock` (a value either *is* a secret ref or is a
// literal; never both). Returning a fresh map keeps the caller's
// `app.Vars` intact for any future reuse.
func resolveEnv(app, env string, literals map[string]string, refs map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(literals)+len(refs))
	for k, v := range literals {
		out[k] = v
	}
	// Sorted for deterministic error messages when multiple refs miss.
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var missing []string
	for _, envKey := range keys {
		secretKey := refs[envKey]
		val, err := secrets.Get(app, env, secretKey)
		if errors.Is(err, secrets.ErrNotFound) {
			missing = append(missing, fmt.Sprintf("%s (looks up @secret:%s)", envKey, secretKey))
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read secret %s: %w", secretKey, err)
		}
		out[envKey] = string(val)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("unresolved @secret references: %s — run `simple-vps secret set <key> --env %s` for each", strings.Join(missing, ", "), env)
	}
	return out, nil
}

func writeEnvFile(app, env string, vals map[string]string) error {
	path := identity.EnvFile(app, env)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderEnvFile(vals)), 0600); err != nil {
		return err
	}
	user := identity.SystemUser(app, env)
	if _, err := utils.RunChecked("chown", []string{user + ":" + user, path}, ""); err != nil {
		return fmt.Errorf("chown env file: %v", err)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

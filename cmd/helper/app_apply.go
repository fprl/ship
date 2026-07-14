package helper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
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
	App           string `arg:"" help:"App name."`
	Env           string `arg:"" help:"Env name."`
	Tarball       string `name:"tarball" required:"" help:"Path to the streamed source tarball."`
	Manifest      string `name:"manifest" required:"" help:"Path to the uploaded ship.toml."`
	SHA           string `name:"sha" required:"" help:"Release identifier."`
	Dirty         bool   `name:"dirty" help:"Mark this release as built from a dirty worktree snapshot."`
	BaseCommit    string `name:"base-commit" required:"" help:"Git commit the release is based on."`
	CreatedAt     string `name:"created-at" required:"" help:"Release creation time in RFC3339."`
	Rebuild       bool   `name:"rebuild" help:"Pass --no-cache --pull=always to podman build."`
	TLS           string `name:"tls" enum:"auto,internal" default:"auto" hidden:"" help:"TLS mode stamped by the client for this deploy."`
	PreviewAlias  string `name:"preview-alias" hidden:"" help:"Preview branch alias host derived by the client."`
	SSHKeyComment string `name:"ssh-key-comment" help:"SSH public key comment for the deploying key."`
	GitAuthor     string `name:"git-author" help:"Git author configured by the deploying client."`
	ClientVersion string `name:"client-version" hidden:"" help:"Client version contacting the helper."`
}

type applyReleaseResult struct {
	containersToRemove []string
	startedContainers  []string
	stoppedContainers  []string
	processNames       map[string]string
	staticSnapshot     *staticCurrentSnapshot
	staticReleaseDir   string
	staticReleaseNew   bool
}

var (
	appendSanitizedDeployJournal = appendSanitizedDeployJournalEntry
	bestEffortPruneAfterDeploy   = bestEffortPruneReleaseImagesAfterDeploy
)

func (c appApplyCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if err := validateRelease(c.SHA); err != nil {
		utils.DieError(err, 1)
	}
	if _, err := c.releaseMetadata(); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbShip, authTargetForAppEnv(c.App, c.Env, "ship", "release="+c.SHA))
	if err := c.recordClientVersion(); err != nil {
		utils.DieError(err, 1)
	}
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked()
	})
	return nil
}

func (c appApplyCmd) recordClientVersion() error {
	clientVersion := strings.TrimSpace(c.ClientVersion)
	if clientVersion == "" {
		return nil
	}
	stateStore := store.Default()
	hostFile, err := stateStore.ReadHost()
	if err != nil {
		return err
	}
	seen := strings.TrimSpace(hostFile.Meta.LastClientVersion)
	cmp, ok := version.Compare(clientVersion, seen)
	if seen != "" && (!ok || cmp <= 0) {
		return nil
	}
	hostFile.Meta.LastClientVersion = clientVersion
	return stateStore.WriteHostState(hostFile.Observed, hostFile.Meta)
}

func (c appApplyCmd) runLocked() {
	if err := c.runLockedE(); err != nil {
		utils.DieError(applyExitError(err), 1)
	}
}

func applyExitError(err error) error {
	var stepErr *journalStepError
	if !errors.As(err, &stepErr) {
		return err
	}
	switch stepErr.Step {
	case "probe":
		return errcat.New(errcat.CodeProbeFailed, errcat.Fields{"detail": stepErr.Err.Error()})
	case "release":
		return errcat.New(errcat.CodeReleaseCommandFailed, errcat.Fields{"detail": stepErr.Err.Error()})
	default:
		return err
	}
}

func (c appApplyCmd) recordDeployFailure(app *config.AppContext, previousRelease string, startedAt time.Time, err error) error {
	entry, scrubValues := deployJournalFailureEntry(c.App, c.Env, previousRelease, c.SHA, c.actor(), startedAt, err)
	entry = sanitizeDeployJournalEntry(c.App, c.Env, entry, scrubValues)
	if appendErr := appendSanitizedDeployJournal(c.App, c.Env, entry); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; run ship box doctor\n", appendErr)
	}
	if app != nil && isAbortedJournalOutcome(entry.Outcome) {
		webhookDeployAborted(app.Webhook, app, entry, time.Now().UTC())
	}
	return err
}

func (c appApplyCmd) runLockedE() (err error) {
	startedAt := time.Now().UTC()
	previousRelease := currentActiveReleaseBestEffort(c.App, c.Env)
	var app *config.AppContext
	defer func() {
		if err == nil {
			return
		}
		err = c.recordDeployFailure(app, previousRelease, startedAt, err)
	}()

	ctxDir, err := c.prepareApplyContext()
	if err != nil {
		return err
	}
	defer os.RemoveAll(ctxDir)

	app, err = c.loadApplyContext(ctxDir)
	if err != nil {
		return err
	}
	if err := attachPreviewProtection(c.App, c.Env, app); err != nil {
		return err
	}
	applyRouteTLS(app, c.TLS)

	var envSnapshot *fileSnapshot
	if app.NeedsImage {
		snapshot, err := snapshotEnvFile(c.App, c.Env)
		if err != nil {
			return fmt.Errorf("snapshot runtime env file: %v", err)
		}
		envSnapshot = &snapshot
	}
	manifestSnapshot, err := snapshotCurrentManifest(c.App, c.Env)
	if err != nil {
		return fmt.Errorf("snapshot current manifest: %v", err)
	}
	deployCommitted := false
	defer func() {
		if deployCommitted {
			return
		}
		if envSnapshot != nil {
			_ = restoreEnvFile(c.App, c.Env, *envSnapshot)
		}
		_ = restoreCurrentManifest(c.App, c.Env, manifestSnapshot)
	}()

	meta, err := c.releaseMetadata()
	if err != nil {
		return err
	}
	if err := persistReleaseSnapshot(c.App, c.Env, c.SHA, filepath.Join(ctxDir, "ship.toml"), meta); err != nil {
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

	if err := persistCurrentManifestFromRelease(c.App, c.Env, c.SHA); err != nil {
		result.cleanupFailed(c.App, c.Env)
		return err
	}
	if err := refreshPreviewShip(c.App, c.Env, time.Now().UTC()); err != nil {
		result.cleanupFailed(c.App, c.Env)
		return err
	}
	if err := c.switchTraffic(app, result); err != nil {
		result.cleanupFailed(c.App, c.Env)
		return err
	}
	releaseSnapshotActive = true
	deployCommitted = true
	return c.completeCommittedDeploy(app, previousRelease, startedAt, result)
}

func (c appApplyCmd) completeCommittedDeploy(app *config.AppContext, previousRelease string, startedAt time.Time, result applyReleaseResult) error {
	removeContainers(result.containersToRemove)
	previousJournal, previousJournalErr := readLatestDeployJournalEntry(c.App, c.Env)
	entry := sanitizeDeployJournalEntry(c.App, c.Env, deployJournalEntry{
		SchemaVersion:    deployJournalSchemaVersion,
		App:              c.App,
		Env:              c.Env,
		Outcome:          "deployed",
		StartedAt:        startedAt.Format(time.RFC3339Nano),
		EndedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		PreviousRelease:  previousRelease,
		AttemptedRelease: c.SHA,
		Identity:         c.actor(),
		Member:           currentServerMemberForJournal(),
	}, nil)
	entry.ImagePrune = bestEffortPruneAfterDeploy(c.App, c.Env, c.SHA, entry)
	if err := appendSanitizedDeployJournal(c.App, c.Env, entry); err != nil {
		fmt.Fprintf(os.Stderr, "warning: deployed but failed to write deploy journal: %v; run ship box doctor\n", err)
	}
	if previousJournalErr == nil && isAbortedJournalOutcome(previousJournal.Outcome) {
		webhookDeployRecovered(app.Webhook, app, previousJournal, entry, time.Now().UTC())
	}

	fmt.Printf("Deployed %s (%s) at %s\n", c.App, c.Env, c.SHA)
	return nil
}

func applyRouteTLS(app *config.AppContext, tlsMode string) {
	if tlsMode == "" || tlsMode == "auto" {
		return
	}
	for name, route := range app.Routes {
		route.TLS = tlsMode
		app.Routes[name] = route
	}
}

func (c appApplyCmd) actor() deployIdentity {
	return deployActor(c.SSHKeyComment, c.GitAuthor)
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
	if _, err := utils.RunChecked("install", []string{"-m", "0644", manifestPath, filepath.Join(ctxDir, "ship.toml")}, ""); err != nil {
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
		result.stoppedContainers = containerResult.stoppedContainers
		result.processNames = containerResult.processNames
	} else {
		result.containersToRemove = appContainerNames(existing)
	}

	success = true
	return result, nil
}

func (c appApplyCmd) switchTraffic(app *config.AppContext, result applyReleaseResult) error {
	if c.PreviewAlias != "" {
		if c.Env == productionEnvName {
			return fmt.Errorf("production deploys cannot set a preview alias")
		}
		if !app.Preview.Aliases {
			return fmt.Errorf("preview alias was supplied but [preview].aliases is false")
		}
		expected, ok := previewAliasForContext(c.App, c.Env, app)
		if !ok {
			return fmt.Errorf("preview alias was supplied but no canonical preview host was rendered")
		}
		if c.PreviewAlias != expected {
			return fmt.Errorf("preview alias %q does not match derived alias %q", c.PreviewAlias, expected)
		}
		if err := addConfiguredPreviewAlias(c.App, c.Env, app); err != nil {
			return err
		}
	}

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
	if err := writeAppCaddyfileWithProcessNames(c.App, c.Env, app, c.SHA, result.processNames); err != nil {
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		if result.staticSnapshot != nil {
			_ = restoreStaticCurrent(c.App, c.Env, *result.staticSnapshot)
		}
		return err
	}
	if err := reloadCaddyOrRestore(caddyPath, prevFragment, prevExisted); err != nil {
		if result.staticSnapshot != nil {
			_ = restoreStaticCurrent(c.App, c.Env, *result.staticSnapshot)
		}
		return caddyStageActionError(err, "deploy", caddyPath)
	}
	return nil
}

func removeContainers(names []string) {
	for _, name := range names {
		_, _ = utils.RunChecked("podman", []string{"rm", "-f", name}, "")
	}
}

func stopContainers(names []string) ([]string, error) {
	var stopped []string
	for _, name := range names {
		if _, err := utils.RunChecked("podman", []string{"stop", name}, ""); err != nil {
			return stopped, fmt.Errorf("stop %s: %v", name, err)
		}
		stopped = append(stopped, name)
	}
	return stopped, nil
}

func startContainers(names []string) error {
	var failed []string
	for _, name := range names {
		if _, err := utils.RunChecked("podman", []string{"start", name}, ""); err != nil {
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("restart containers %s", strings.Join(failed, ", "))
	}
	return nil
}

// warnOnRestartFailure restarts containers after an operation stops them. A
// restart failure must not hide the original operation outcome, but the
// operator still needs a visible recovery path.
func warnOnRestartFailure(stopped []string) {
	if err := startContainers(stopped); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; run ship box doctor\n", err)
	}
}

func (r applyReleaseResult) cleanupFailed(app, env string) {
	removeContainers(r.startedContainers)
	warnOnRestartFailure(r.stoppedContainers)
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
	if err := store.AtomicWrite(dst, data, 0644); err != nil {
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
	stoppedContainers  []string
	processNames       map[string]string
}

func (c appApplyCmd) applyContainer(ctxDir string, app *config.AppContext, existing []containerEntry) (containerApplyResult, error) {
	var stopped []string
	containersToRemove := containersForRemovedProcesses(existing, app.Processes)
	startedAt := time.Now().UTC().Format("20060102t150405000000000z")
	started, err := startReleaseProcesses(startReleaseProcessesParams{
		App:     c.App,
		Env:     c.Env,
		Release: c.SHA,
		Context: app,
		BeforeStart: func(runtime processStartRuntime) error {
			buildArgs := podmanBuildArgs(c.App, c.Env, runtime.ImageTag, c.SHA, filepath.Join(ctxDir, "Dockerfile"), ctxDir, c.Rebuild)
			if _, err := utils.RunChecked("podman", buildArgs, ""); err != nil {
				return newJournalStepError("build", fmt.Errorf("podman build: %w", err), runtime.ScrubValues, nil)
			}
			if app.Release != "" {
				if err := runReleaseCommand(c.App, c.Env, app.Release, runtime.ImageTag, runtime.UserID, runtime.GroupID, c.SHA); err != nil {
					return newJournalStepError("release", err, runtime.ScrubValues, nil)
				}
			}
			return nil
		},
		BeforeProcess: func(processName string, proc config.Process) error {
			if proc.Port != nil {
				return nil
			}
			old := processContainers(existing, processName, "")
			stoppedNow, err := stopContainers(old)
			if err != nil {
				warnOnRestartFailure(uniqueContainerNames(append(stopped, stoppedNow...)))
				return err
			}
			stopped = append(stopped, stoppedNow...)
			containersToRemove = append(containersToRemove, old...)
			return nil
		},
		ContainerName: func(processName string, proc config.Process) string {
			return nextProcessContainerName(existing, c.App, c.Env, processName, c.SHA, startedAt)
		},
	})
	if err != nil {
		var stepErr *journalStepError
		if errors.As(err, &stepErr) {
			return containerApplyResult{}, err
		}
		var startErr processStartError
		if errors.As(err, &startErr) {
			warnOnRestartFailure(stopped)
			step := "apply"
			var probe *journalProbe
			var probeErr *probeFailureError
			if strings.Contains(startErr.Err.Error(), "health check failed") {
				step = "probe"
			}
			if errors.As(startErr.Err, &probeErr) {
				step = "probe"
				probe = &journalProbe{Status: probeErr.Status, BodySnippet: probeErr.BodySnippet}
			}
			return containerApplyResult{}, newJournalStepError(step, startErr.Err, started.ScrubValues, probe)
		}
		return containerApplyResult{}, err
	}
	for _, processName := range sortedKeys(app.Processes) {
		if app.Processes[processName].Port != nil {
			containersToRemove = append(containersToRemove, processContainers(existing, processName, "")...)
		}
	}
	return containerApplyResult{
		containersToRemove: uniqueContainerNames(containersToRemove),
		startedContainers:  uniqueContainerNames(started.Started),
		stoppedContainers:  uniqueContainerNames(stopped),
		processNames:       started.ProcessName,
	}, nil
}

func nextProcessContainerName(entries []containerEntry, app, env, processName, release, instance string) string {
	base := identity.ContainerName(app, env, processName, release)
	for _, e := range entries {
		for _, name := range e.Names {
			if name == base {
				return identity.ContainerInstanceName(app, env, processName, release, instance)
			}
		}
	}
	return base
}

func routedProcessNames(routes map[string]config.Route) map[string]bool {
	out := map[string]bool{}
	for _, route := range routes {
		if route.Process != "" {
			out[route.Process] = true
		}
	}
	return out
}

func processProbe(routed map[string]bool, processName string, probe string) string {
	if routed[processName] {
		return probe
	}
	return ""
}

func containersForRemovedProcesses(entries []containerEntry, next map[string]config.Process) []string {
	var names []string
	for _, e := range entries {
		process := e.Labels["ship.process"]
		if process == "" || isEphemeralProcess(process) {
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
		process := e.Labels["ship.process"]
		if process == "" || isEphemeralProcess(process) {
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
		dst := filepath.Join(stageDir, config.RouteStorageName(routeName))
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

	for _, envKey := range keys {
		secretKey := refs[envKey]
		val, err := resolveSecretValue(app, env, secretKey)
		if err != nil {
			if errcat.Is(err, errcat.CodeSecretMissing) {
				return nil, annotateSecretMissingRef(err, envKey)
			}
			return nil, err
		}
		out[envKey] = string(val)
	}
	return out, nil
}

func annotateSecretMissingRef(err error, envKey string) error {
	coded, ok := errcat.As(err)
	if !ok {
		return err
	}
	return errcat.WithCause(coded, fmt.Sprintf("%s (referenced by %s)", coded.Cause(), envKey))
}

func writeEnvFile(app, env string, vals map[string]string) error {
	path := identity.EnvFile(app, env)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := store.AtomicWrite(path, []byte(renderEnvFile(vals)), 0600); err != nil {
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

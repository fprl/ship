package helper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

// appApplyCmd is the per-env deploy primitive. Given a
// streamed source tarball and a manifest, it:
//
//  1. Validates the manifest.
//  2. Resolves vars/secrets into an immutable activation env file.
//  3. Builds the image or snapshots static assets.
//  4. Starts new versioned process containers and verifies web health.
//  5. Synthesizes a Caddyfile fragment, validates, reloads, and only
//     then removes old routed containers.
type appApplyCmd struct {
	App           string            `arg:"" help:"App name."`
	Env           string            `arg:"" help:"Env name."`
	Tarball       string            `name:"tarball" required:"" help:"Path to the streamed source tarball."`
	Manifest      string            `name:"manifest" required:"" help:"Path to the uploaded ship.toml."`
	SHA           string            `name:"sha" required:"" help:"Release identifier."`
	Dirty         bool              `name:"dirty" help:"Mark this release as built from a dirty worktree snapshot."`
	BaseCommit    string            `name:"base-commit" required:"" help:"Git commit the release is based on."`
	CreatedAt     string            `name:"created-at" required:"" help:"Release creation time in RFC3339."`
	Rebuild       bool              `name:"rebuild" help:"Pass --no-cache --pull=always to podman build."`
	TLS           string            `name:"tls" enum:"auto,internal" default:"auto" hidden:"" help:"TLS mode stamped by the client for this deploy."`
	PreviewAlias  string            `name:"preview-alias" hidden:"" help:"Preview branch alias host derived by the client."`
	SSHKeyComment string            `name:"ssh-key-comment" help:"SSH public key comment for the deploying key."`
	GitAuthor     string            `name:"git-author" help:"Git author configured by the deploying client."`
	ClientVersion string            `name:"client-version" hidden:"" help:"Client version contacting the helper."`
	ActivationID  string            `kong:"-"`
	Envelope      envelope.Envelope `kong:"-"`
	EnvelopeLabel string            `kong:"-"`
}

type applyReleaseResult struct {
	containersToRemove []string
	processNames       map[string]string
}

var (
	appendSanitizedDeployJournal = appendSanitizedDeployJournalEntry
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
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked()
	})
	return nil
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

func (c appApplyCmd) recordCommittedUnconverged(app *config.AppContext, previousRelease string, startedAt time.Time, err error) error {
	stepErr := newJournalStepError("converge", err, nil, nil)
	entry, scrubValues := deployJournalFailureEntry(c.App, c.Env, previousRelease, c.SHA, c.actor(), startedAt, stepErr)
	entry.Outcome = "committed_unconverged"
	entry = sanitizeDeployJournalEntry(c.App, c.Env, entry, scrubValues)
	if appendErr := appendSanitizedDeployJournal(c.App, c.Env, entry); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; run ship box doctor\n", appendErr)
	}
	return fmt.Errorf("committed but not converged; %s: %w", convergenceNextStep, err)
}

func (c appApplyCmd) recordCommittedDegraded(app *config.AppContext, previousRelease string, startedAt time.Time, err error) error {
	stepErr := newJournalStepError("durability", err, nil, nil)
	entry, scrubValues := deployJournalFailureEntry(c.App, c.Env, previousRelease, c.SHA, c.actor(), startedAt, stepErr)
	entry.Outcome = "committed_degraded"
	entry = sanitizeDeployJournalEntry(c.App, c.Env, entry, scrubValues)
	if appendErr := appendSanitizedDeployJournal(c.App, c.Env, entry); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; run ship box doctor\n", appendErr)
	}
	return fmt.Errorf("committed but degraded; %s: %w", convergenceNextStep, err)
}

func (c appApplyCmd) runLockedE() (err error) {
	startedAt := time.Now().UTC()
	previousRelease := currentActiveReleaseBestEffort(c.App, c.Env)
	var app *config.AppContext
	committed := false
	defer func() {
		if err == nil {
			return
		}
		if committed {
			var degraded committedDegradedError
			if errors.As(err, &degraded) {
				err = c.recordCommittedDegraded(app, previousRelease, startedAt, degraded.Err)
				return
			}
			err = c.recordCommittedUnconverged(app, previousRelease, startedAt, err)
			return
		}
		removePreparedCandidates(c.App, c.Env, c.ActivationID)
		err = c.recordDeployFailure(app, previousRelease, startedAt, fmt.Errorf("nothing changed: %w", err))
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

	meta, err := c.releaseMetadata()
	if err != nil {
		return err
	}
	manifestData, err := os.ReadFile(filepath.Join(ctxDir, "ship.toml"))
	if err != nil {
		return fmt.Errorf("read effective manifest: %v", err)
	}
	manifestData, err = effectiveManifestText(manifestData, app)
	if err != nil {
		return err
	}
	c.ActivationID, err = newActivationID(c.App, c.Env, c.SHA)
	if err != nil {
		return err
	}
	c.Envelope, c.EnvelopeLabel, err = releaseEnvelope(manifestData, meta)
	if err != nil {
		return err
	}
	resolved, err := resolveEnv(c.App, c.Env, app.Vars, app.SecretRefs)
	if err != nil {
		return err
	}
	for key, value := range shipInjectedEnv(c.App, c.Env, c.SHA, app) {
		resolved[key] = value
	}
	if _, err := writeActivationEnvFile(c.App, c.Env, c.ActivationID, resolved); err != nil {
		return err
	}

	result, err := c.applyRelease(ctxDir, app)
	if err != nil {
		return err
	}

	if err := c.prepareCaddy(app, result); err != nil {
		return err
	}
	activeErr := writeActive(c.App, c.Env, activation.Pointer{
		Version: 1, Release: c.SHA, Activation: c.ActivationID,
		EnvelopeHash: envelope.HashLabel(c.EnvelopeLabel),
	})
	if activeErr != nil {
		var published store.PublishedWriteError
		if !errors.As(activeErr, &published) {
			return activeErr
		}
		committed = true
		converged, convergeErr := convergeActive(c.App, c.Env)
		result.containersToRemove = uniqueContainerNames(append(result.containersToRemove, converged.StaleContainers...))
		if convergeErr != nil {
			return committedDegradedError{Err: fmt.Errorf("active pointer published but durability is degraded: %v; convergence failed: %w", activeErr, convergeErr)}
		}
		return committedDegradedError{Err: fmt.Errorf("active pointer published but durability is degraded: %w", activeErr)}
	}
	committed = true
	converged, err := convergeActive(c.App, c.Env)
	result.containersToRemove = uniqueContainerNames(append(result.containersToRemove, converged.StaleContainers...))
	if err != nil {
		return err
	}
	if err := refreshPreviewShip(c.App, c.Env, time.Now().UTC()); err != nil {
		return committedDegradedError{Err: fmt.Errorf("activation converged but preview metadata refresh failed: %w", err)}
	}
	return c.completeCommittedDeploy(app, previousRelease, startedAt, result)
}

type committedDegradedError struct{ Err error }

func (e committedDegradedError) Error() string { return e.Err.Error() }
func (e committedDegradedError) Unwrap() error { return e.Err }

func (c appApplyCmd) prepareCaddy(app *config.AppContext, result applyReleaseResult) error {
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
	path := caddyfilePath(c.App, c.Env)
	if err := validateAppCaddy(path, c.App, c.Env, app, c.SHA, result.processNames); err != nil {
		return caddyStageActionError(err, "deploy", path)
	}
	return nil
}

func (c appApplyCmd) completeCommittedDeploy(app *config.AppContext, previousRelease string, startedAt time.Time, result applyReleaseResult) error {
	previousJournal, previousJournalTorn, previousJournalErr := readLatestDeployJournalEntryWithStatus(c.App, c.Env)
	if previousJournalTorn {
		warnTornDeployJournal(identity.DeployJournalFile(c.App, c.Env))
	}
	entry := sanitizeDeployJournalEntry(c.App, c.Env, deployJournalEntry{
		SchemaVersion:    deployJournalSchemaVersion,
		App:              c.App,
		Env:              c.Env,
		Outcome:          "deployed",
		StartedAt:        startedAt.Format(time.RFC3339Nano),
		EndedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		PreviousRelease:  previousRelease,
		AttemptedRelease: c.SHA,
		Activation:       c.ActivationID,
		Identity:         c.actor(),
		Member:           currentServerMemberForJournal(),
	}, nil)
	if err := appendSanitizedDeployJournal(c.App, c.Env, entry); err != nil {
		fmt.Fprintf(os.Stderr, "warning: deployed but failed to write deploy journal: %v; run ship box doctor\n", err)
		fmt.Printf("Deployed %s (%s) at %s\n", c.App, c.Env, c.SHA)
		return nil
	}
	removeContainers(result.containersToRemove)
	bestEffortGCAfterLifecycle(c.App, c.Env)
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

	if app.HasStaticRoutes {
		_, _, err := c.applyStatic(ctxDir, app)
		if err != nil {
			return applyReleaseResult{}, err
		}
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
		result.processNames = containerResult.processNames
	} else {
		// Static-only targets have no prepared runtime shape. Convergence
		// computes every stale non-ephemeral container from the pointer.
	}

	return result, nil
}

func removeContainers(names []string) {
	for _, name := range names {
		_, _ = utils.RunChecked("podman", []string{"rm", "-f", name}, "")
	}
}

func removePreparedCandidates(app, env, activationID string) {
	if activationID == "" {
		return
	}
	entries, err := podmanPSContainers(app, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to remove pre-commit candidates for activation %s: %v; run ship box doctor\n", activationID, err)
		return
	}
	var candidates []string
	for _, entry := range entries {
		if entry.Labels["ship.activation"] != activationID || len(entry.Names) == 0 {
			continue
		}
		candidates = append(candidates, entry.Names[0])
	}
	removeContainers(uniqueContainerNames(candidates))
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

type containerApplyResult struct {
	containersToRemove []string
	processNames       map[string]string
}

func (c appApplyCmd) applyContainer(ctxDir string, app *config.AppContext, existing []containerEntry) (containerApplyResult, error) {
	var containersToRemove []string
	startedAt := time.Now().UTC().Format("20060102t150405000000000z")
	started, err := startReleaseProcesses(startReleaseProcessesParams{
		App:         c.App,
		Env:         c.Env,
		Release:     c.SHA,
		Activation:  c.ActivationID,
		Context:     app,
		OnlyPortful: true,
		BeforeStart: func(runtime processStartRuntime) error {
			buildArgs := podmanBuildArgsWithEnvelope(c.App, c.Env, runtime.ImageTag, c.SHA, filepath.Join(ctxDir, "Dockerfile"), ctxDir, c.Rebuild, c.EnvelopeLabel)
			if _, err := utils.RunChecked("podman", buildArgs, ""); err != nil {
				return newJournalStepError("build", fmt.Errorf("podman build: %w", err), runtime.ScrubValues, nil)
			}
			if app.Release != "" {
				if err := runReleaseCommandWithActivation(c.App, c.Env, app.Release, runtime.ImageTag, runtime.UserID, runtime.GroupID, c.SHA, c.ActivationID, runtime.EnvFile); err != nil {
					return newJournalStepError("release", err, runtime.ScrubValues, nil)
				}
			}
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
	return containerApplyResult{
		containersToRemove: uniqueContainerNames(containersToRemove),
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
	activeRelease := currentActiveReleaseBestEffort(c.App, c.Env)
	if err := os.MkdirAll(filepath.Dir(releaseDir), 0755); err != nil {
		return "", false, err
	}
	if _, err := os.Stat(releaseDir); err == nil {
		if _, envelopeErr := readStaticReleaseEnvelope(c.App, c.Env, c.SHA); envelopeErr == nil {
			if err := verifyStaticRelease(c.App, c.Env, c.SHA, app.Routes); err != nil {
				return "", false, err
			}
			return releaseDir, false, nil
		}
		if activeRelease == c.SHA {
			return "", false, fmt.Errorf("active static release %s cannot be replaced during prepare", c.SHA)
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
	if err := writeStaticReleaseEnvelope(c.App, c.Env, c.SHA, c.Envelope); err != nil {
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

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

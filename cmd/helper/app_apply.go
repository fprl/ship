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
	"github.com/fprl/ship/internal/artifact"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/identity"
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
	App           string                 `arg:"" help:"App name."`
	Env           string                 `arg:"" help:"Env name."`
	Tarball       string                 `name:"tarball" required:"" help:"Path to the streamed source tarball."`
	Manifest      string                 `name:"manifest" required:"" help:"Path to the uploaded ship.toml."`
	SHA           string                 `name:"sha" required:"" help:"Release identifier."`
	Dirty         bool                   `name:"dirty" help:"Mark this release as built from a dirty worktree snapshot."`
	BaseCommit    string                 `name:"base-commit" required:"" help:"Git commit the release is based on."`
	CreatedAt     string                 `name:"created-at" required:"" help:"Release creation time in RFC3339."`
	Rebuild       bool                   `name:"rebuild" help:"Pass --no-cache --pull=always to podman build."`
	Progress      bool                   `name:"progress" hidden:"" help:"Emit structured deploy progress events."`
	Logs          bool                   `name:"logs" hidden:"" help:"Emit build and release-command log events."`
	TLS           string                 `name:"tls" enum:"auto,internal" default:"auto" hidden:"" help:"TLS mode stamped by the client for this deploy."`
	PreviewAlias  string                 `name:"preview-alias" hidden:"" help:"Preview branch alias host derived by the client."`
	SSHKeyComment string                 `name:"ssh-key-comment" help:"SSH public key comment for the deploying key."`
	GitAuthor     string                 `name:"git-author" help:"Git author configured by the deploying client."`
	ActivationID  string                 `kong:"-"`
	Envelope      envelope.Envelope      `kong:"-"`
	EnvelopeLabel string                 `kong:"-"`
	ImageID       string                 `kong:"-"`
	StaticHash    string                 `kong:"-"`
	ScrubValues   []string               `kong:"-"`
	ProgressOut   *deployProgressEmitter `kong:"-"`
}

type applyReleaseResult struct {
	containersToRemove []string
	processNames       map[string]string
}

var (
	appendSanitizedDeployJournal = appendSanitizedDeployJournalEntry
)

func (c *appApplyCmd) Run() error {
	c.ProgressOut = newDeployProgressEmitter(c.Progress, c.Logs, os.Stderr)
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

func (c *appApplyCmd) runLocked() {
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

func (c *appApplyCmd) recordDeployFailure(app *config.AppContext, previousRelease string, startedAt time.Time, err error) error {
	entry, scrubValues := deployJournalFailureEntry(c.App, c.Env, previousRelease, c.SHA, c.actor(), startedAt, err)
	entry = sanitizeDeployJournalEntry(c.App, c.Env, entry, scrubValues)
	if appendErr := appendSanitizedDeployJournal(c.App, c.Env, entry); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; next: ship box doctor\n", appendErr)
	}
	if app != nil && isFailedJournalOutcome(entry.Outcome) {
		webhookDeployAborted(app.Webhook, app, entry, time.Now().UTC())
	}
	return err
}

func (c *appApplyCmd) recordCommittedUnconverged(app *config.AppContext, previousRelease string, startedAt time.Time, err error) error {
	entry, scrubValues := committedOutcomeJournalEntry(c.App, c.Env, "committed_unconverged", previousRelease, c.SHA, c.actor(), startedAt, committedFailureStep(err, "converge"), c.committedArtifact(), err)
	entry = sanitizeDeployJournalEntry(c.App, c.Env, entry, scrubValues)
	if appendErr := appendSanitizedDeployJournal(c.App, c.Env, entry); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; next: ship box doctor\n", appendErr)
	}
	return newDeployCommittedUnconvergedError(err)
}

func (c *appApplyCmd) recordCommittedDegraded(app *config.AppContext, previousRelease string, startedAt time.Time, err error) error {
	entry, scrubValues := committedOutcomeJournalEntry(c.App, c.Env, "committed_degraded", previousRelease, c.SHA, c.actor(), startedAt, "durability", c.committedArtifact(), err)
	entry = sanitizeDeployJournalEntry(c.App, c.Env, entry, scrubValues)
	if appendErr := appendSanitizedDeployJournal(c.App, c.Env, entry); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; next: ship box doctor\n", appendErr)
	}
	return newDeployCommittedDegradedError(err)
}

func newDeployCommittedUnconvergedError(err error) error {
	if errcat.Is(err, errcat.CodeDeployCommittedUnconverged) {
		return err
	}
	return &committedCodedError{coded: errcat.New(errcat.CodeDeployCommittedUnconverged, errcat.Fields{"detail": err.Error()}), cause: err}
}

func newDeployCommittedDegradedError(err error) error {
	if errcat.Is(err, errcat.CodeDeployCommittedDegraded) {
		return err
	}
	return &committedCodedError{coded: errcat.New(errcat.CodeDeployCommittedDegraded, errcat.Fields{"detail": err.Error()}), cause: err}
}

type committedCodedError struct {
	coded *errcat.Error
	cause error
}

func (e *committedCodedError) Error() string   { return e.coded.Error() }
func (e *committedCodedError) Unwrap() []error { return []error{e.coded, e.cause} }

func committedFailureStep(err error, fallback string) string {
	var convergeErr *convergeError
	if errors.As(err, &convergeErr) && convergeErr.Step != "" {
		return convergeErr.Step
	}
	var stepErr *journalStepError
	if errors.As(err, &stepErr) && stepErr.Step != "" {
		return stepErr.Step
	}
	return fallback
}

func (c *appApplyCmd) committedArtifact() *artifact.Tuple {
	tuple := artifact.Tuple{Release: c.SHA, ImageID: c.ImageID, StaticHash: c.StaticHash}
	if c.ImageID == "" {
		tuple.EnvelopeHash = envelope.HashLabel(c.EnvelopeLabel)
	}
	return &tuple
}

func (c *appApplyCmd) runLockedE() (err error) {
	if c.ProgressOut == nil {
		c.ProgressOut = newDeployProgressEmitter(false, false, os.Stderr)
	}
	startedAt := time.Now().UTC()
	previousPointer, pointerErr := readActive(c.App, c.Env)
	if pointerErr != nil && !errcat.Is(pointerErr, errcat.CodeNoDeploys) {
		return pointerErr
	}
	previousRelease := previousPointer.Artifact.Release
	if previousPointer.IsLegacy() {
		previousRelease = previousPointer.Legacy.Release
	}
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

	var ctxDir string
	finishPrepare := c.ProgressOut.start("prepare", "Prepare release")
	err = func() error {
		var prepareErr error
		ctxDir, prepareErr = c.prepareApplyContext()
		if prepareErr != nil {
			return prepareErr
		}
		app, prepareErr = c.loadApplyContext(ctxDir)
		if prepareErr != nil {
			return prepareErr
		}
		if prepareErr = attachPreviewProtection(c.App, c.Env, app); prepareErr != nil {
			return prepareErr
		}
		applyRouteTLS(app, c.TLS)

		meta, prepareErr := c.releaseMetadata()
		if prepareErr != nil {
			return prepareErr
		}
		manifestData, prepareErr := os.ReadFile(filepath.Join(ctxDir, "ship.toml"))
		if prepareErr != nil {
			return fmt.Errorf("read effective manifest: %v", prepareErr)
		}
		manifestData, prepareErr = effectiveManifestText(manifestData, app)
		if prepareErr != nil {
			return prepareErr
		}
		if app.NeedsImage {
			c.ActivationID, prepareErr = newActivationID(c.App, c.Env, c.SHA)
			if prepareErr != nil {
				return prepareErr
			}
		}
		c.Envelope, c.EnvelopeLabel, prepareErr = releaseEnvelope(manifestData, meta)
		return prepareErr
	}()
	finishPrepare(err)
	if err != nil {
		if ctxDir != "" {
			_ = os.RemoveAll(ctxDir)
		}
		return err
	}
	defer os.RemoveAll(ctxDir)

	if app.NeedsImage {
		finishBuild := c.ProgressOut.start("build", "Build image")
		buildErr := c.prepareContainerArtifact(ctxDir, previousPointer, pointerErr == nil)
		finishBuild(buildErr)
		if buildErr != nil {
			return buildErr
		}
	}
	finishRuntime := c.ProgressOut.start("runtime", "Prepare runtime")
	runtimeErr := func() error {
		resolved, resolveErr := resolveEnv(c.App, c.Env, app.Vars, app.SecretRefs)
		if resolveErr != nil {
			return resolveErr
		}
		for key, value := range shipInjectedEnv(c.App, c.Env, c.SHA, app) {
			resolved[key] = value
		}
		c.ScrubValues = collectEnvValues(resolved)
		if app.NeedsImage {
			_, resolveErr = writeActivationEnvFile(c.App, c.Env, c.ActivationID, resolved)
		}
		return resolveErr
	}()
	finishRuntime(runtimeErr)
	if runtimeErr != nil {
		return runtimeErr
	}

	result, err := c.applyRelease(ctxDir, app)
	if err != nil {
		return err
	}

	app.StaticHash = c.StaticHash
	finishRoutes := c.ProgressOut.start("routes", "Prepare routes")
	routesErr := c.prepareCaddy(app, result)
	finishRoutes(routesErr)
	if routesErr != nil {
		return routesErr
	}
	finishTraffic := c.ProgressOut.start("traffic", "Switch traffic")
	committed, err = commitAndConverge(c.App, c.Env, activation.Pointer{
		Version: 2, Activation: c.ActivationID,
		Artifact: artifact.Tuple{Release: c.SHA, ImageID: c.ImageID, StaticHash: c.StaticHash, EnvelopeHash: func() string {
			if c.ImageID == "" {
				return envelope.HashLabel(c.EnvelopeLabel)
			}
			return ""
		}()},
	}, func(stale []string) {
		result.containersToRemove = uniqueContainerNames(append(result.containersToRemove, stale...))
	}, func() error {
		return c.completeCommittedDeploy(app, previousRelease, startedAt, result)
	})
	finishTraffic(err)
	return err
}

type committedDegradedError struct{ Err error }

func (e committedDegradedError) Error() string { return e.Err.Error() }
func (e committedDegradedError) Unwrap() error { return e.Err }

func (c *appApplyCmd) prepareCaddy(app *config.AppContext, result applyReleaseResult) error {
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
		return caddyStageActionError(err, "deploy")
	}
	return nil
}

func (c *appApplyCmd) completeCommittedDeploy(app *config.AppContext, previousRelease string, startedAt time.Time, result applyReleaseResult) error {
	if err := resetLegacyDeployJournalForV2(c.App, c.Env); err != nil {
		return err
	}
	previousJournal, previousJournalTorn, previousJournalErr := readLatestDeployJournalEntryWithStatus(c.App, c.Env)
	if previousJournalTorn {
		warnTornDeployJournal(identity.DeployJournalFile(c.App, c.Env))
	}
	entry := sanitizeDeployJournalEntry(c.App, c.Env, deployJournalEntry{
		Outcome:          "deployed",
		StartedAt:        startedAt.Format(time.RFC3339Nano),
		EndedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		PreviousRelease:  previousRelease,
		AttemptedRelease: c.SHA,
		Activation:       c.ActivationID,
		Identity:         c.actor(),
		Member:           currentServerMemberForJournal(),
		Artifact: &artifact.Tuple{Release: c.SHA, ImageID: c.ImageID, StaticHash: c.StaticHash, EnvelopeHash: func() string {
			if c.ImageID == "" {
				return envelope.HashLabel(c.EnvelopeLabel)
			}
			return ""
		}()},
	}, nil)
	if err := appendSanitizedDeployJournal(c.App, c.Env, entry); err != nil {
		fmt.Fprintf(os.Stderr, "warning: deployed but failed to write deploy journal %s: %v; cleanup/GC were skipped; next: ship box doctor\n", identity.DeployJournalFile(c.App, c.Env), err)
		fmt.Printf("Deployed %s (%s) at %s\n", c.App, c.Env, artifact.Tuple{Release: c.SHA, ImageID: c.ImageID, StaticHash: c.StaticHash}.DisplayIdentity())
		return nil
	}
	removeContainers(result.containersToRemove)
	bestEffortGCAfterLifecycle(c.App, c.Env)
	if previousJournalErr == nil && isFailedJournalOutcome(previousJournal.Outcome) {
		webhookDeployRecovered(app.Webhook, app, previousJournal, entry, time.Now().UTC())
	}

	fmt.Printf("Deployed %s (%s) at %s\n", c.App, c.Env, artifact.Tuple{Release: c.SHA, ImageID: c.ImageID, StaticHash: c.StaticHash}.DisplayIdentity())
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

func (c *appApplyCmd) actor() deployIdentity {
	return deployActor(c.SSHKeyComment, c.GitAuthor)
}

func (c *appApplyCmd) releaseMetadata() (releaseMetadata, error) {
	return newReleaseMetadata(c.SHA, c.Dirty, c.BaseCommit, c.CreatedAt)
}

func (c *appApplyCmd) prepareApplyContext() (string, error) {
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

func (c *appApplyCmd) loadApplyContext(ctxDir string) (*config.AppContext, error) {
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

func (c *appApplyCmd) applyRelease(ctxDir string, app *config.AppContext) (applyReleaseResult, error) {
	var result applyReleaseResult

	if app.HasStaticRoutes {
		finishStatic := c.ProgressOut.start("static", "Publish static assets")
		_, _, err := c.applyStatic(ctxDir, app)
		finishStatic(err)
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
		result.processNames = containerResult.processNames
	} else {
		// Static-only targets have no prepared runtime shape. Convergence
		// computes every stale non-ephemeral container from the pointer.
	}

	return result, nil
}

// prepareContainerArtifact builds a fresh image or adopts an exact committed
// artifact whose envelope has the same configuration identity. The release
// name is only metadata; the runtime reference is the inspected ImageID.
func (c *appApplyCmd) prepareContainerArtifact(ctxDir string, previousPointer activation.Pointer, hasPreviousPointer bool) error {
	incomingMeta, err := releaseMetadataFromEnvelope(c.Envelope, c.SHA)
	if err != nil {
		return err
	}
	if !c.Rebuild {
		var history []artifact.Tuple
		var historyErr error
		if hasPreviousPointer {
			history, _, historyErr = committedHistoryWithPointer(c.App, c.Env, previousPointer)
		}
		if historyErr == nil {
			for _, tuple := range history {
				if tuple.Release != c.SHA || tuple.ImageID == "" {
					continue
				}
				candidate, resolveErr := resolveArtifact(c.App, c.Env, tuple)
				if resolveErr != nil || candidate.Envelope.Manifest != c.Envelope.Manifest {
					continue
				}
				meta, metaErr := releaseMetadataFromEnvelope(candidate.Envelope, c.SHA)
				if metaErr != nil || meta.Dirty != incomingMeta.Dirty || meta.BaseCommit != incomingMeta.BaseCommit {
					continue
				}
				label, labelErr := candidate.Envelope.LabelValue()
				if labelErr != nil {
					continue
				}
				c.Envelope, c.EnvelopeLabel, c.ImageID = candidate.Envelope, label, tuple.ImageID
				return nil
			}
		}
	}
	buildRef := identity.ImageTag(c.App, c.Env, "build-"+c.ActivationID)
	args := podmanBuildArgsWithEnvelope(c.App, c.Env, buildRef, c.SHA, filepath.Join(ctxDir, "Dockerfile"), ctxDir, c.Rebuild, c.EnvelopeLabel)
	if _, err := runDeployCommand(c.ProgressOut, "build", nil, 0, "podman", args, ""); err != nil {
		return newJournalStepError("build", fmt.Errorf("podman build: %w", err), nil, nil)
	}
	entry, err := inspectExactImage(buildRef)
	if err != nil {
		return err
	}
	if entry.ID == "" {
		return errors.New("podman image inspect returned empty image id")
	}
	c.ImageID = entry.ID
	committedTag := identity.ImageTag(c.App, c.Env, "img-"+normalizeImageID(c.ImageID))
	if _, err := utils.RunChecked("podman", []string{"tag", buildRef, committedTag}, ""); err != nil {
		return fmt.Errorf("tag built image %s: %w", c.ImageID, err)
	}
	// With the committed tag in place, dropping the build tag only untags;
	// leaving it would later block GC's remove-by-ID (podman refuses while
	// any tag remains). Best-effort: a leftover build tag is GC-able debris.
	if _, err := utils.RunChecked("podman", []string{"rmi", buildRef}, ""); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not drop build tag %s: %v; next: ship box gc\n", buildRef, err)
	}
	return nil
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
		fmt.Fprintf(os.Stderr, "warning: failed to remove pre-commit candidates for activation %s: %v; next: ship box doctor\n", activationID, err)
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
		fmt.Fprintf(os.Stderr, "warning: %v; next: ship box doctor\n", err)
	}
}

type containerApplyResult struct {
	processNames map[string]string
}

func (c *appApplyCmd) applyContainer(ctxDir string, app *config.AppContext, existing []containerEntry) (containerApplyResult, error) {
	startedAt := time.Now().UTC().Format("20060102t150405000000000z")
	started, err := startReleaseProcesses(startReleaseProcessesParams{
		App:         c.App,
		Env:         c.Env,
		Release:     c.SHA,
		Activation:  c.ActivationID,
		Context:     app,
		OnlyPortful: true,
		ImageID:     c.ImageID,
		EnvFile:     identity.ActivationEnvFile(c.App, c.Env, c.ActivationID),
		ScrubValues: c.ScrubValues,
		Progress:    c.ProgressOut,
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
	if app.Release != "" {
		// Migrations run against the same exact image that will be committed.
		userID, groupID, idErr := hostUserIDs(identity.SystemUser(c.App, c.Env))
		if idErr != nil {
			return containerApplyResult{}, idErr
		}
		finishRelease := c.ProgressOut.start("release", "Run release · "+app.Release)
		releaseErr := runReleaseCommandWithActivation(c.App, c.Env, app.Release, c.ImageID, userID, groupID, c.SHA, c.ActivationID, identity.ActivationEnvFile(c.App, c.Env, c.ActivationID), c.ProgressOut, c.ScrubValues)
		finishRelease(releaseErr)
		if releaseErr != nil {
			return containerApplyResult{}, newJournalStepError("release", releaseErr, started.ScrubValues, nil)
		}
	}
	return containerApplyResult{
		processNames: started.ProcessName,
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

func (c *appApplyCmd) applyStatic(ctxDir string, app *config.AppContext) (string, bool, error) {
	if err := validateRelease(c.SHA); err != nil {
		return "", false, err
	}
	staticDir := identity.StaticDir(c.App, c.Env)
	root := filepath.Join(staticDir, "releases")
	stageDir, err := os.MkdirTemp(staticDir, ".staging-")
	if err != nil {
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
	if err := normalizeStaticTree(stageDir); err != nil {
		return "", false, err
	}
	staticHash, err := artifact.StaticTreeHash(stageDir)
	if err != nil {
		return "", false, err
	}
	releaseDir := staticReleasePath(c.App, c.Env, c.SHA, staticHash)
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", false, err
	}
	if info, statErr := os.Stat(releaseDir); statErr == nil {
		if !info.IsDir() {
			return "", false, fmt.Errorf("static artifact path %s is not a directory", releaseDir)
		}
		if existingHash, hashErr := artifact.StaticTreeHash(releaseDir); hashErr != nil || existingHash != staticHash {
			return "", false, fmt.Errorf("static artifact %s does not match its directory hash", releaseDir)
		}
		_ = os.RemoveAll(stageDir)
	} else if os.IsNotExist(statErr) {
		if err := os.Rename(stageDir, releaseDir); err != nil {
			return "", false, fmt.Errorf("publish static release: %v", err)
		}
		staged = true
	} else {
		return "", false, statErr
	}
	if err := writeStaticReleaseEnvelope(c.App, c.Env, c.SHA, c.Envelope); err != nil {
		return releaseDir, true, err
	}
	c.StaticHash = staticHash
	return releaseDir, true, nil
}

func normalizeStaticTree(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0644)
		if info.IsDir() {
			mode = 0755
		}
		return os.Chmod(path, mode)
	})
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

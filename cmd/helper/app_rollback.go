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

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

// appRollbackCmd swaps one (app, env) back to an older local release. The
// release artifact supplies the image/static tree, and its envelope supplies
// the process and route shape.
type appRollbackCmd struct {
	App            string            `arg:"" help:"App name."`
	Env            string            `arg:"" help:"Env name."`
	Release        string            `arg:"" optional:"" help:"Release to run. Omitted = previous local release."`
	SSHKeyComment  string            `name:"ssh-key-comment" help:"SSH public key comment for the deploying key."`
	GitAuthor      string            `name:"git-author" help:"Git author configured by the deploying client."`
	ActivationID   string            `kong:"-"`
	TargetEnvelope envelope.Envelope `kong:"-"`
}

var rollbackCaddyPath = caddyfilePath

var appendRollbackDeployJournal = appendDeployJournalEntry

func (c appRollbackCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if c.Release != "" {
		if err := validateRelease(c.Release); err != nil {
			utils.DieError(err, 1)
		}
	}
	args := []string{}
	if c.Release != "" {
		args = append(args, "release="+c.Release)
	}
	authorizeOrDie(helperVerbRollback, authTargetForAppEnv(c.App, c.Env, "rollback", args...))
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked()
	})
	return nil
}

func (c appRollbackCmd) runLocked() {
	startedAt := time.Now().UTC()
	result, err := c.rollbackRelease(nil)
	if err != nil {
		utils.DieError(err, 1)
	}
	c.recordRollbackSuccess(result, startedAt)
}

func (c appRollbackCmd) recordRollbackSuccess(result rollbackPayload, startedAt time.Time) {
	if err := appendRollbackDeployJournal(c.App, c.Env, deployJournalEntry{
		SchemaVersion:    deployJournalSchemaVersion,
		App:              c.App,
		Env:              c.Env,
		Outcome:          "rolled_back",
		StartedAt:        startedAt.Format(time.RFC3339Nano),
		EndedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		PreviousRelease:  result.Previous,
		AttemptedRelease: result.Release,
		Identity:         c.actor(),
		Member:           currentServerMemberForJournal(),
	}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: rollback succeeded but failed to write deploy journal: %v; run ship box doctor\n", err)
	}
	fmt.Print(renderRollbackText(result))
}

func (c appRollbackCmd) actor() deployIdentity {
	return deployActor(c.SSHKeyComment, c.GitAuthor)
}

func (c appRollbackCmd) rollbackRelease(currentApp *config.AppContext) (rollbackPayload, error) {
	var currentCleanup func()
	defer func() {
		if currentCleanup != nil {
			currentCleanup()
		}
	}()
	pointer, err := readActive(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	current := pointer.Release
	var images []imageRelease
	currentEnvelope, currentEnvelopeErr := readStaticReleaseEnvelope(c.App, c.Env, current)
	if currentEnvelopeErr != nil {
		images, err = podmanImages(c.App, c.Env)
		if err != nil {
			return rollbackPayload{}, err
		}
	}
	if currentApp == nil {
		e := currentEnvelope
		if currentEnvelopeErr != nil {
			for _, image := range images {
				if image.Release == current {
					e = image.Envelope
					break
				}
			}
		}
		label, labelErr := e.LabelValue()
		if labelErr != nil || envelope.HashLabel(label) != pointer.EnvelopeHash {
			return rollbackPayload{}, fmt.Errorf("active release envelope hash does not match active.json")
		}
		currentApp, currentCleanup, err = loadAppContextFromEnvelope(c.App, c.Env, current, e, "active release envelope is missing")
		if err != nil {
			return rollbackPayload{}, err
		}
	}
	releases, err := availableRollbackReleasesWithImages(c.App, c.Env, c.Release, images)
	if err != nil {
		return rollbackPayload{}, err
	}
	target, err := selectRollbackRelease(releases, current, c.Release)
	if err != nil {
		return rollbackPayload{}, err
	}
	app, cleanup, err := loadAppContextFromEnvelope(c.App, c.Env, target.Release, target.Envelope, "release envelope is missing")
	if err != nil {
		return rollbackPayload{}, err
	}
	defer cleanup()
	if err := attachPreviewProtection(c.App, c.Env, app); err != nil {
		return rollbackPayload{}, err
	}
	c.ActivationID, err = newActivationID(c.App, c.Env, target.Release)
	if err != nil {
		return rollbackPayload{}, err
	}
	c.TargetEnvelope = target.Envelope
	return c.rollbackToTarget(current, target.Release, app)
}

func activeRelease(app, env string) (string, error) {
	if pointer, err := readActive(app, env); err == nil {
		return pointer.Release, nil
	}
	return "", fmt.Errorf("active release pointer not found")
}

func (c appRollbackCmd) rollbackToTarget(current, targetRelease string, app *config.AppContext) (payload rollbackPayload, err error) {
	containers, err := podmanPSContainers(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	var started []string
	var stoppedOld []string
	var caddyPath string
	var prevFragment []byte
	var prevExisted bool
	var caddySnapshotReady bool
	var trafficSwitched bool
	cleanupStarted := func() error {
		removeContainers(started)
		return startContainers(uniqueContainerNames(stoppedOld))
	}
	defer func() {
		if err == nil {
			return
		}

		var restoreErrs []error
		if caddySnapshotReady {
			if restoreErr := restoreCaddyFragment(caddyPath, prevFragment, prevExisted); restoreErr != nil {
				restoreErrs = append(restoreErrs, restoreErr)
			} else if trafficSwitched {
				if reloadErr := reloadCaddyOrRestore(caddyPath, prevFragment, prevExisted); reloadErr != nil {
					restoreErrs = append(restoreErrs, caddyStageActionError(reloadErr, "after rollback", caddyPath))
				}
			}
		}
		if restartErr := cleanupStarted(); restartErr != nil {
			restoreErrs = append(restoreErrs, restartErr)
		}
		if len(restoreErrs) > 0 {
			err = fmt.Errorf("%w; rollback restore failed: %v", err, errors.Join(restoreErrs...))
		}
	}()

	if app.HasStaticRoutes {
		if err := verifyStaticRelease(c.App, c.Env, targetRelease, app.Routes); err != nil {
			return rollbackPayload{}, err
		}
	}

	if app.NeedsImage {
		startedResult, err := startReleaseProcesses(startReleaseProcessesParams{
			App:        c.App,
			Env:        c.Env,
			Release:    targetRelease,
			Activation: c.ActivationID,
			Context:    app,
			BeforeProcess: func(procName string, proc config.Process) error {
				if proc.Port != nil {
					return nil
				}
				for _, old := range processContainers(containers, procName, targetRelease) {
					stopped, stopErr := stopContainers([]string{old})
					stoppedOld = append(stoppedOld, stopped...)
					if stopErr != nil {
						return stopErr
					}
				}
				return nil
			},
		})
		started = append(started, startedResult.Started...)
		if err != nil {
			return rollbackPayload{}, err
		}
	}
	if !app.NeedsImage {
		resolved, resolveErr := resolveEnv(c.App, c.Env, app.Vars, app.SecretRefs)
		if resolveErr != nil {
			return rollbackPayload{}, resolveErr
		}
		for key, value := range shipInjectedEnv(c.App, c.Env, targetRelease, app) {
			resolved[key] = value
		}
		if _, resolveErr = writeActivationEnvFile(c.App, c.Env, c.ActivationID, resolved); resolveErr != nil {
			return rollbackPayload{}, resolveErr
		}
	}
	caddyPath = rollbackCaddyPath(c.App, c.Env)
	if err := addConfiguredPreviewAlias(c.App, c.Env, app); err != nil {
		return rollbackPayload{}, err
	}
	prevFragment, prevExisted, err = snapshotCaddyFragment(caddyPath)
	if err != nil {
		return rollbackPayload{}, fmt.Errorf("snapshot existing fragment: %v", err)
	}
	caddySnapshotReady = true
	if err := renderAndReloadAppCaddy(caddyPath, c.App, c.Env, app, targetRelease, nil); err != nil {
		return rollbackPayload{}, caddyStageActionError(err, "after rollback", caddyPath)
	}
	trafficSwitched = true
	if err := writeActive(c.App, c.Env, activation.Pointer{
		Version: 1, Release: targetRelease, Activation: c.ActivationID,
		EnvelopeHash: envelope.HashLabel(c.TargetEnvelopeLabel()),
	}); err != nil {
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

func (c appRollbackCmd) TargetEnvelopeLabel() string {
	label, _ := c.TargetEnvelope.LabelValue()
	return label
}

type rollbackPayload struct {
	App       string   `json:"app"`
	Env       string   `json:"env"`
	Previous  string   `json:"previous"`
	Release   string   `json:"release"`
	Processes []string `json:"processes"`
}

type imageRelease struct {
	Release  string
	Image    string
	Envelope envelope.Envelope
}

type imageEntry struct {
	Repository string            `json:"Repository"`
	Tag        string            `json:"Tag"`
	Names      []string          `json:"Names"`
	Labels     map[string]string `json:"Labels"`
	RepoTags   []string          `json:"RepoTags"`
	Config     struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func podmanImages(app, env string) ([]imageRelease, error) {
	return podmanImagesForRelease(app, env, "")
}

func podmanImagesForRelease(app, env, requested string) ([]imageRelease, error) {
	refs := map[string]bool{}
	if requested != "" {
		// An explicit target must be inspected on its own. The active
		// release may be static and therefore have no image at all; asking
		// Podman to inspect that missing image alongside the target would
		// make a valid container rollback fail as one combined command.
		refs[identity.ImageTag(app, env, requested)] = true
	} else {
		if pointer, err := readActive(app, env); err == nil && pointer.Release != "" {
			refs[identity.ImageTag(app, env, pointer.Release)] = true
		}
		if entries, _, err := readDeployJournalEntriesWithStatus(app, env); err == nil {
			for _, entry := range entries {
				if entry.AttemptedRelease != "" {
					refs[identity.ImageTag(app, env, entry.AttemptedRelease)] = true
				}
			}
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	args := []string{"image", "inspect", "--format", "json"}
	for ref := range refs {
		args = append(args, ref)
	}
	sort.Strings(args[4:])
	out, err := utils.RunChecked("podman", args, "")
	if err != nil {
		return nil, fmt.Errorf("podman image inspect: %v", err)
	}
	out = []byte(strings.TrimSpace(string(out)))
	if len(out) == 0 {
		return nil, nil
	}
	var entries []imageEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		var entry imageEntry
		if singleErr := json.Unmarshal(out, &entry); singleErr != nil {
			return nil, fmt.Errorf("parse podman image inspect json: %v", err)
		}
		entries = []imageEntry{entry}
	}
	return imageReleasesFromEntries(app, env, entries), nil
}

func imageReleasesFromEntries(app, env string, entries []imageEntry) []imageRelease {
	var releases []imageRelease
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Labels == nil {
			e.Labels = e.Config.Labels
		}
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
		envelopeValue := e.Labels[envelope.Label]
		decoded, _ := envelope.DecodeLabel(envelopeValue)
		releases = append(releases, imageRelease{Release: release, Image: identity.ImageTag(app, env, release), Envelope: decoded})
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

func availableRollbackReleases(app, env, requested string) ([]imageRelease, error) {
	return availableRollbackReleasesWithImages(app, env, requested, nil)
}

func availableRollbackReleasesWithImages(app, env, requested string, images []imageRelease) ([]imageRelease, error) {
	if requested != "" && images == nil {
		staticTarget := false
		if sidecar, sidecarErr := readStaticReleaseEnvelope(app, env, requested); sidecarErr == nil {
			if ctx, cleanup, ctxErr := loadAppContextFromEnvelope(app, env, requested, sidecar, "release envelope is missing"); ctxErr == nil {
				staticTarget = !ctx.NeedsImage
				cleanup()
			}
		}
		if !staticTarget {
			var err error
			images, err = podmanImagesForRelease(app, env, requested)
			if err != nil {
				return nil, err
			}
		}
	}
	releases, torn, err := releaseSnapshots(app, env)
	if err != nil {
		if torn && requested == "" {
			return nil, fmt.Errorf("history incomplete; pass an explicit release")
		}
		if requested == "" || !errcat.Is(err, errcat.CodeNoDeploys) {
			return nil, err
		}
		releases = nil
	}
	if torn && requested == "" {
		return nil, fmt.Errorf("history incomplete; pass an explicit release")
	}
	needImages := false
	for _, release := range releases {
		if _, err := readStaticReleaseEnvelope(app, env, release.Release); err != nil {
			needImages = true
			break
		}
	}
	if needImages && images == nil {
		images, err = podmanImages(app, env)
		if err != nil {
			return nil, err
		}
	}
	imageByRelease := map[string]imageRelease{}
	for _, image := range images {
		imageByRelease[image.Release] = image
	}
	var available []imageRelease
	for _, release := range releases {
		candidate := imageRelease{Release: release.Release, Image: identity.ImageTag(app, env, release.Release)}
		if image, ok := imageByRelease[release.Release]; ok {
			candidate = image
		} else if sidecar, sidecarErr := readStaticReleaseEnvelope(app, env, release.Release); sidecarErr == nil {
			candidate.Envelope = sidecar
		} else {
			continue
		}
		ctx, cleanup, err := loadAppContextFromEnvelope(app, env, release.Release, candidate.Envelope, "release envelope is missing")
		if err != nil {
			continue
		}
		err = verifyReleaseArtifactsWithImages(app, env, release.Release, ctx, imageByRelease)
		cleanup()
		if err != nil {
			continue
		}
		candidate.Release = release.Release
		available = append(available, candidate)
	}
	if requested != "" {
		found := false
		for _, candidate := range available {
			if candidate.Release == requested {
				found = true
				break
			}
		}
		if !found {
			candidate := imageRelease{Release: requested, Image: identity.ImageTag(app, env, requested)}
			if sidecar, sidecarErr := readStaticReleaseEnvelope(app, env, requested); sidecarErr == nil {
				candidate.Envelope = sidecar
			} else {
				for _, image := range images {
					if image.Release == requested {
						candidate = image
						break
					}
				}
			}
			if candidate.Envelope.Schema != 0 {
				ctx, cleanup, ctxErr := loadAppContextFromEnvelope(app, env, requested, candidate.Envelope, "release envelope is missing")
				if ctxErr == nil {
					ctxErr = verifyReleaseArtifactsWithImages(app, env, requested, ctx, imageByRelease)
				}
				if cleanup != nil {
					cleanup()
				}
				if ctxErr == nil {
					available = append(available, candidate)
				}
			}
		}
	}
	return available, nil
}

func releaseSnapshots(app, env string) ([]imageRelease, bool, error) {
	entries, torn, err := readDeployJournalEntriesWithStatus(app, env)
	if err != nil {
		return nil, torn, err
	}
	seen := map[string]bool{}
	var out []imageRelease
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Outcome != "deployed" && entry.Outcome != "rolled_back" {
			continue
		}
		release := entry.AttemptedRelease
		if release == "" || seen[release] {
			continue
		}
		if err := validateRelease(release); err != nil {
			continue
		}
		seen[release] = true
		out = append(out, imageRelease{Release: release, Image: identity.ImageTag(app, env, release)})
	}
	if len(out) == 0 {
		return nil, torn, noDeployJournalError(app, env)
	}
	return out, torn, nil
}

func verifyReleaseArtifactsWithImages(app, env, release string, ctx *config.AppContext, imageByRelease map[string]imageRelease) error {
	if ctx.NeedsImage && imageByRelease[release].Envelope.Schema == 0 {
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
	pointer, err := readActive(app, env)
	if err != nil {
		return nil, func() {}, fmt.Errorf("active pointer not found; deploy once before rollback")
	}
	e, err := envelopeForRelease(app, env, pointer.Release)
	if err != nil {
		return nil, func() {}, err
	}
	label, err := e.LabelValue()
	if err != nil || envelope.HashLabel(label) != pointer.EnvelopeHash {
		return nil, func() {}, fmt.Errorf("active release envelope hash does not match active.json")
	}
	return loadAppContextFromEnvelope(app, env, pointer.Release, e, "active release envelope is missing")
}

func loadReleaseAppContext(app, env, release string) (*config.AppContext, func(), error) {
	if err := validateRelease(release); err != nil {
		return nil, func() {}, err
	}
	e, err := envelopeForRelease(app, env, release)
	if err != nil {
		return nil, func() {}, err
	}
	return loadAppContextFromEnvelope(app, env, release, e, "release envelope is missing")
}

func envelopeForRelease(app, env, release string) (envelope.Envelope, error) {
	if e, err := readStaticReleaseEnvelope(app, env, release); err == nil {
		return e, nil
	}
	images, err := podmanImages(app, env)
	if err != nil {
		return envelope.Envelope{}, err
	}
	for _, image := range images {
		if image.Release == release && image.Envelope.Schema != 0 {
			return image.Envelope, nil
		}
	}
	return envelope.Envelope{}, fmt.Errorf("release %s envelope is missing", release)
}

func loadAppContextFromEnvelope(app, env, release string, e envelope.Envelope, missingHint string) (*config.AppContext, func(), error) {
	if err := e.Validate(); err != nil {
		return nil, func() {}, fmt.Errorf("%s: %v", missingHint, err)
	}
	if _, err := releaseMetadataFromEnvelope(e, release); err != nil {
		return nil, func() {}, err
	}
	tmp, err := os.MkdirTemp("", "ship-rollback-manifest-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if err := os.WriteFile(filepath.Join(tmp, "ship.toml"), []byte(e.Manifest), 0644); err != nil {
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

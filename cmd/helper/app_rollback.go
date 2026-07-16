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
	result, err := c.rollbackRelease(nil, startedAt)
	if err != nil {
		if result.Committed {
			var degraded committedDegradedError
			if errors.As(err, &degraded) {
				c.recordRollbackDegraded(result, startedAt, degraded.Err)
				utils.DieError(rollbackCommittedError(err), 1)
			}
			c.recordRollbackFailure(result, startedAt, err)
			utils.DieError(rollbackCommittedError(err), 1)
		}
		removePreparedCandidates(c.App, c.Env, c.ActivationID)
		utils.DieError(fmt.Errorf("nothing changed: %w", err), 1)
	}
}

func rollbackCommittedError(err error) error {
	var degraded committedDegradedError
	if errors.As(err, &degraded) {
		return newDeployCommittedDegradedError(degraded.Err)
	}
	return newDeployCommittedUnconvergedError(err)
}

func (c appRollbackCmd) recordRollbackDegraded(result rollbackPayload, startedAt time.Time, err error) {
	entry := deployJournalEntry{
		SchemaVersion: deployJournalSchemaVersion, App: c.App, Env: c.Env,
		Outcome: "committed_degraded", StartedAt: startedAt.Format(time.RFC3339Nano),
		EndedAt: time.Now().UTC().Format(time.RFC3339Nano), PreviousRelease: result.Previous,
		AttemptedRelease: result.Release, FailingStep: "durability", StderrTail: err.Error(),
		Identity: c.actor(), Member: currentServerMemberForJournal(),
	}
	if appendErr := appendRollbackDeployJournal(c.App, c.Env, entry, nil); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; run ship box doctor\n", appendErr)
	}
}

func (c appRollbackCmd) recordRollbackFailure(result rollbackPayload, startedAt time.Time, err error) {
	entry := deployJournalEntry{
		SchemaVersion:    deployJournalSchemaVersion,
		App:              c.App,
		Env:              c.Env,
		Outcome:          "committed_unconverged",
		StartedAt:        startedAt.Format(time.RFC3339Nano),
		EndedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		PreviousRelease:  result.Previous,
		AttemptedRelease: result.Release,
		FailingStep:      "converge",
		StderrTail:       err.Error(),
		Identity:         c.actor(),
		Member:           currentServerMemberForJournal(),
	}
	if appendErr := appendRollbackDeployJournal(c.App, c.Env, entry, nil); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; run ship box doctor\n", appendErr)
	}
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
		Activation:       c.ActivationID,
		EnvelopeHash:     envelope.HashLabel(c.TargetEnvelopeLabel()),
		Identity:         c.actor(),
		Member:           currentServerMemberForJournal(),
	}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: rollback succeeded but failed to write deploy journal %s: %v; cleanup/GC were skipped; run ship box doctor\n", identity.DeployJournalFile(c.App, c.Env), err)
	} else {
		removeContainers(result.StaleContainers)
		bestEffortGCAfterLifecycle(c.App, c.Env)
	}
	fmt.Print(renderRollbackText(result))
}

func (c appRollbackCmd) actor() deployIdentity {
	return deployActor(c.SSHKeyComment, c.GitAuthor)
}

func (c *appRollbackCmd) rollbackRelease(currentApp *config.AppContext, startedAt time.Time) (rollbackPayload, error) {
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
	currentEnvelope, currentEnvelopeErr := readStaticReleaseEnvelopeByHash(c.App, c.Env, current, pointer.EnvelopeHash)
	if currentEnvelopeErr != nil {
		images, err = podmanImagesForEnvelopeHash(c.App, c.Env, current, pointer.EnvelopeHash)
		if err != nil {
			return rollbackPayload{}, err
		}
	}
	if currentApp == nil {
		e := currentEnvelope
		if currentEnvelopeErr != nil {
			for _, image := range images {
				if image.Release == current && image.EnvelopeHash == pointer.EnvelopeHash {
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
	return c.rollbackToTarget(current, target.Release, app, startedAt)
}

func activeRelease(app, env string) (string, error) {
	if pointer, err := readActive(app, env); err == nil {
		return pointer.Release, nil
	}
	return "", fmt.Errorf("active release pointer not found")
}

func (c appRollbackCmd) rollbackToTarget(current, targetRelease string, app *config.AppContext, startedAt time.Time) (payload rollbackPayload, err error) {
	if app.HasStaticRoutes {
		if err := verifyStaticRelease(c.App, c.Env, targetRelease, app.Routes); err != nil {
			return rollbackPayload{}, err
		}
	}
	if err := addConfiguredPreviewAlias(c.App, c.Env, app); err != nil {
		return rollbackPayload{}, err
	}

	if app.NeedsImage {
		containers, err := podmanPSContainers(c.App, c.Env)
		if err != nil {
			return rollbackPayload{}, err
		}
		startedResult, err := startReleaseProcesses(startReleaseProcessesParams{
			App:         c.App,
			Env:         c.Env,
			Release:     targetRelease,
			Activation:  c.ActivationID,
			Context:     app,
			OnlyPortful: true,
			ContainerName: func(procName string, _ config.Process) string {
				return nextProcessContainerName(containers, c.App, c.Env, procName, targetRelease, "rollback")
			},
		})
		if err != nil {
			return rollbackPayload{}, err
		}
		processNames := startedResult.ProcessName
		if err := validateAppCaddy(caddyfilePath(c.App, c.Env), c.App, c.Env, app, targetRelease, processNames); err != nil {
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
	if !app.NeedsImage {
		if err := validateAppCaddy(caddyfilePath(c.App, c.Env), c.App, c.Env, app, targetRelease, nil); err != nil {
			return rollbackPayload{}, err
		}
	}
	payload = rollbackPayload{
		App:       c.App,
		Env:       c.Env,
		Previous:  current,
		Release:   targetRelease,
		Processes: processNames(app.Processes),
	}
	payload.Committed, err = commitAndConverge(c.App, c.Env, activation.Pointer{
		Version: 1, Release: targetRelease, Activation: c.ActivationID,
		EnvelopeHash: envelope.HashLabel(c.TargetEnvelopeLabel()),
	}, func(stale []string) {
		payload.StaleContainers = uniqueContainerNames(stale)
	}, func() error {
		c.recordRollbackSuccess(payload, startedAt)
		return nil
	})
	return payload, err
}

func (c appRollbackCmd) TargetEnvelopeLabel() string {
	label, _ := c.TargetEnvelope.LabelValue()
	return label
}

type rollbackPayload struct {
	App             string   `json:"app"`
	Env             string   `json:"env"`
	Previous        string   `json:"previous"`
	Release         string   `json:"release"`
	Processes       []string `json:"processes"`
	Committed       bool     `json:"-"`
	StaleContainers []string `json:"-"`
}

type imageRelease struct {
	Release      string
	Image        string
	Envelope     envelope.Envelope
	EnvelopeHash string
	CreatedAt    time.Time
}

type imageEntry struct {
	ID         string            `json:"Id"`
	Repository string            `json:"Repository"`
	Tag        string            `json:"Tag"`
	Names      []string          `json:"Names"`
	Labels     map[string]string `json:"Labels"`
	RepoTags   []string          `json:"RepoTags"`
	CreatedAt  string            `json:"CreatedAt"`
	Config     struct {
		Labels  map[string]string `json:"Labels"`
		Created string            `json:"Created"`
	} `json:"Config"`
}

func podmanImages(app, env string) ([]imageRelease, error) {
	byKey := map[string]imageRelease{}
	var firstErr error
	if tagged, err := podmanImagesForRelease(app, env, ""); err == nil {
		for _, image := range tagged {
			byKey[image.Release+"\x00"+image.EnvelopeHash] = image
		}
	} else {
		firstErr = err
	}
	if scanned, err := podmanScannedImages(app, env); err == nil {
		for _, image := range scanned {
			byKey[image.Release+"\x00"+image.EnvelopeHash] = image
		}
	} else if len(byKey) == 0 && firstErr != nil {
		return nil, firstErr
	}
	out := make([]imageRelease, 0, len(byKey))
	for _, image := range byKey {
		out = append(out, image)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Release != out[j].Release {
			return out[i].Release < out[j].Release
		}
		return out[i].EnvelopeHash < out[j].EnvelopeHash
	})
	return out, nil
}

func podmanImagesForEnvelopeHash(app, env, release, envelopeHash string) ([]imageRelease, error) {
	images, err := podmanImages(app, env)
	if err != nil {
		return nil, err
	}
	var matched []imageRelease
	for _, image := range images {
		if image.Release == release && image.EnvelopeHash == envelopeHash {
			matched = append(matched, image)
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("release %s image with envelope %s is not available locally", release, envelopeHash)
	}
	return matched, nil
}

func podmanScannedImages(app, env string) ([]imageRelease, error) {
	out, err := utils.RunChecked("podman", []string{"images", "--format", "json"}, "")
	if err != nil {
		return nil, fmt.Errorf("podman image scan: %v", err)
	}
	var entries []imageEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &entries); err != nil {
		return nil, fmt.Errorf("parse podman images json: %v", err)
	}
	return imageReleasesFromEntries(app, env, entries), nil
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
		if release == "" || release == "<none>" {
			continue
		}
		if err := validateRelease(release); err != nil {
			continue
		}
		envelopeValue := e.Labels[envelope.Label]
		decoded, decodeErr := envelope.DecodeLabel(envelopeValue)
		envelopeHash := ""
		if decodeErr == nil {
			envelopeHash = envelope.HashLabel(envelopeValue)
		}
		key := release + "\x00" + envelopeHash
		if seen[key] {
			continue
		}
		seen[key] = true
		created := e.CreatedAt
		if created == "" {
			created = e.Config.Created
		}
		createdAt := parseImageCreatedAt(created)
		imageRef := identity.ImageTag(app, env, release)
		if e.ID != "" {
			imageRef = e.ID
		}
		releases = append(releases, imageRelease{Release: release, Image: imageRef, Envelope: decoded, EnvelopeHash: envelopeHash, CreatedAt: createdAt})
	}
	return releases
}

func parseImageCreatedAt(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 -0700",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
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

func availableRollbackReleasesWithImages(app, env, requested string, images []imageRelease) ([]imageRelease, error) {
	if requested != "" && images == nil {
		needsImage := true
		if sidecar, sidecarErr := readStaticReleaseEnvelope(app, env, requested); sidecarErr == nil {
			if ctx, cleanup, ctxErr := loadAppContextFromEnvelope(app, env, requested, sidecar, "release envelope is missing"); ctxErr == nil {
				needsImage = ctx.NeedsImage
				cleanup()
			}
		}
		if needsImage {
			var err error
			images, err = podmanImages(app, env)
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
		var sidecar envelope.Envelope
		var err error
		if release.EnvelopeHash != "" {
			sidecar, err = readStaticReleaseEnvelopeByHash(app, env, release.Release, release.EnvelopeHash)
		} else {
			sidecar, err = readStaticReleaseEnvelope(app, env, release.Release)
		}
		if err != nil {
			needImages = true
			continue
		}
		ctx, cleanup, ctxErr := loadAppContextFromEnvelope(app, env, release.Release, sidecar, "release envelope is missing")
		if cleanup != nil {
			cleanup()
		}
		if ctxErr != nil || (ctx != nil && ctx.NeedsImage) {
			needImages = true
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
		if release.EnvelopeHash != "" {
			for _, image := range images {
				if image.Release == release.Release && image.EnvelopeHash == release.EnvelopeHash {
					candidate = image
					break
				}
			}
		}
		if candidate.Envelope.Schema == 0 {
			if image, ok := imageByRelease[release.Release]; ok {
				candidate = image
			}
		}
		if candidate.Envelope.Schema == 0 {
			var sidecar envelope.Envelope
			var sidecarErr error
			if release.EnvelopeHash != "" {
				sidecar, sidecarErr = readStaticReleaseEnvelopeByHash(app, env, release.Release, release.EnvelopeHash)
			} else {
				sidecar, sidecarErr = readStaticReleaseEnvelope(app, env, release.Release)
			}
			if sidecarErr == nil {
				candidate.Envelope = sidecar
			} else {
				continue
			}
		}
		if err := verifyReleaseCandidate(app, env, candidate, imageByRelease); err != nil {
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
	history, historyErr := releaseDeployHistory(entries, nil)
	if historyErr != nil {
		return nil, torn, historyErr
	}
	if pointer, pointerErr := readActive(app, env); pointerErr == nil {
		history = retainedReleaseHistory(env, pointer.Release, history)
	}
	out := make([]imageRelease, 0, len(history))
	for _, record := range history {
		out = append(out, imageRelease{Release: record.Release, Image: identity.ImageTag(app, env, record.Release), EnvelopeHash: record.EnvelopeHash})
	}
	if len(history) == 0 {
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

func verifyReleaseCandidate(app, env string, candidate imageRelease, imageByRelease map[string]imageRelease) error {
	ctx, cleanup, err := loadAppContextFromEnvelope(app, env, candidate.Release, candidate.Envelope, "release envelope is missing")
	if err != nil {
		return err
	}
	defer cleanup()
	if ctx.NeedsImage {
		image := imageByRelease[candidate.Release]
		if image.EnvelopeHash != "" {
			label, labelErr := candidate.Envelope.LabelValue()
			if labelErr != nil || envelope.HashLabel(label) != image.EnvelopeHash {
				return fmt.Errorf("release %s image envelope does not match candidate envelope", candidate.Release)
			}
		}
	}
	return verifyReleaseArtifactsWithImages(app, env, candidate.Release, ctx, imageByRelease)
}

func loadActiveEnvelopeContext(app, env string) (*config.AppContext, func(), error) {
	pointer, err := readActive(app, env)
	if err != nil {
		return nil, func() {}, fmt.Errorf("active pointer not found; deploy once before rollback")
	}
	e, err := envelopeForPointer(app, env, pointer)
	if err != nil {
		return nil, func() {}, err
	}
	label, err := e.LabelValue()
	if err != nil || envelope.HashLabel(label) != pointer.EnvelopeHash {
		return nil, func() {}, fmt.Errorf("active release envelope hash does not match active.json")
	}
	return loadAppContextFromEnvelope(app, env, pointer.Release, e, "active release envelope is missing")
}

func envelopeForPointer(app, env string, pointer activation.Pointer) (envelope.Envelope, error) {
	if e, err := readStaticReleaseEnvelopeByHash(app, env, pointer.Release, pointer.EnvelopeHash); err == nil {
		return e, nil
	}
	images, err := podmanImagesForEnvelopeHash(app, env, pointer.Release, pointer.EnvelopeHash)
	if err != nil {
		return envelope.Envelope{}, fmt.Errorf("release %s envelope %s is missing; next: ship: %w", pointer.Release, pointer.EnvelopeHash, err)
	}
	return images[0].Envelope, nil
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
		return nil, func() {}, fmt.Errorf("activation envelope names app %s, expected %s", ctx.AppName, app)
	}
	return ctx, cleanup, nil
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

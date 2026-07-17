package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/artifact"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

// Tuple is the artifact-keyed trust identity used by all helper verbs.
type Tuple = artifact.Tuple

type artifactAbsentError struct {
	ImageID string
}

func (e *artifactAbsentError) Error() string {
	return fmt.Sprintf("pinned image %s is absent", e.ImageID)
}

type resolvedArtifact struct {
	Tuple    Tuple
	Envelope envelope.Envelope
	Context  *config.AppContext
	ImageID  string
}

// CommittedHistory returns the complete, newest-first v2 artifact history.
// The pointer is a committed root even when its success journal append was
// lost in the documented pointer/journal crash window.
func CommittedHistory(app, env string) ([]Tuple, bool, error) {
	pointer, err := readActive(app, env)
	if err != nil {
		return nil, false, err
	}
	return committedHistoryWithPointer(app, env, pointer)
}

func committedHistoryWithPointer(app, env string, pointer activation.Pointer) ([]Tuple, bool, error) {
	if pointer.IsLegacy() {
		return nil, false, nil
	}
	entries, torn, journalErr := readDeployJournalEntriesWithStatus(app, env)
	if journalErr != nil && !errcat.Is(journalErr, errcat.CodeNoDeploys) {
		return nil, torn, journalErr
	}
	seen := map[Tuple]bool{}
	history := []Tuple{}
	appendTuple := func(tuple Tuple) {
		if tuple.Release == "" || seen[tuple] {
			return
		}
		seen[tuple] = true
		history = append(history, tuple)
	}
	appendTuple(pointer.Artifact)
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Artifact == nil || (entry.Outcome != "deployed" && entry.Outcome != "rolled_back" && entry.Outcome != "committed_unconverged" && entry.Outcome != "committed_degraded") {
			continue
		}
		appendTuple(*entry.Artifact)
	}
	return history, torn, nil
}

// ResolveArtifact verifies exactly tuple. It never searches by release,
// mutable tag, or an unqualified sidecar.
func ResolveArtifact(app, env string, tuple Tuple) (*config.AppContext, error) {
	resolved, err := resolveArtifact(app, env, tuple)
	if err != nil {
		return nil, err
	}
	return resolved.Context, nil
}

func resolveActiveContext(app, env string) (*config.AppContext, Tuple, error) {
	pointer, err := readActive(app, env)
	if err != nil {
		return nil, Tuple{}, err
	}
	if err := requireV2Pointer(pointer); err != nil {
		return nil, Tuple{}, err
	}
	resolved, err := resolveArtifact(app, env, pointer.Artifact)
	if err != nil {
		return nil, Tuple{}, err
	}
	return resolved.Context, pointer.Artifact, nil
}

func resolveArtifact(app, env string, tuple Tuple) (resolvedArtifact, error) {
	if err := validateArtifactTuple(tuple); err != nil {
		return resolvedArtifact{}, err
	}
	var e envelope.Envelope
	if tuple.ImageID != "" {
		entry, err := inspectExactImage(tuple.ImageID)
		if err != nil {
			return resolvedArtifact{}, fmt.Errorf("artifact %s image %s is unavailable: %w", tuple.DisplayIdentity(), tuple.ImageID, err)
		}
		labels := entry.Labels
		if labels == nil {
			labels = entry.Config.Labels
		}
		if labels["ship.app"] != app || labels["ship.env"] != env {
			return resolvedArtifact{}, fmt.Errorf("artifact image %s is not owned by %s (%s)", tuple.ImageID, app, env)
		}
		if normalizeImageID(entry.ID) != normalizeImageID(tuple.ImageID) {
			return resolvedArtifact{}, fmt.Errorf("artifact image identity mismatch: expected %s, inspected %s", tuple.ImageID, entry.ID)
		}
		decoded, err := envelope.DecodeLabel(labels[envelope.Label])
		if err != nil {
			return resolvedArtifact{}, fmt.Errorf("artifact image %s envelope is invalid: %w", tuple.ImageID, err)
		}
		e = decoded
	} else {
		if tuple.EnvelopeHash == "" {
			return resolvedArtifact{}, errors.New("static-only artifact envelope_hash is required")
		}
		data, err := os.ReadFile(staticReleaseEnvelopePathByHash(app, env, tuple.Release, tuple.EnvelopeHash))
		if err != nil {
			return resolvedArtifact{}, fmt.Errorf("artifact %s envelope is unavailable: %w", tuple.DisplayIdentity(), err)
		}
		e, err = envelope.DecodeJSON(data)
		if err != nil {
			return resolvedArtifact{}, err
		}
		label, err := e.LabelValue()
		if err != nil || envelope.HashLabel(label) != tuple.EnvelopeHash {
			return resolvedArtifact{}, fmt.Errorf("static artifact envelope hash does not match %s", tuple.EnvelopeHash)
		}
	}
	if _, err := releaseMetadataFromEnvelope(e, tuple.Release); err != nil {
		return resolvedArtifact{}, err
	}
	if tuple.StaticHash != "" {
		path := staticReleasePath(app, env, tuple.Release, tuple.StaticHash)
		info, err := os.Lstat(path)
		if err != nil {
			return resolvedArtifact{}, fmt.Errorf("artifact %s static tree is unavailable: %w", tuple.DisplayIdentity(), err)
		}
		if !info.IsDir() {
			return resolvedArtifact{}, fmt.Errorf("artifact %s static tree is not a directory", tuple.DisplayIdentity())
		}
	}
	ctx, err := config.LoadAppContextFromManifestBytes([]byte(e.Manifest), env)
	if err != nil {
		return resolvedArtifact{}, err
	}
	if ctx.AppName != app {
		return resolvedArtifact{}, fmt.Errorf("activation envelope names app %s, expected %s", ctx.AppName, app)
	}
	ctx.StaticHash = tuple.StaticHash
	return resolvedArtifact{Tuple: tuple, Envelope: e, Context: ctx, ImageID: tuple.ImageID}, nil
}

func validateArtifactTuple(tuple Tuple) error {
	if tuple.Release == "" {
		return errors.New("artifact release is required")
	}
	if err := validateRelease(tuple.Release); err != nil {
		return fmt.Errorf("artifact release is invalid: %w", err)
	}
	if tuple.ImageID == "" && tuple.StaticHash == "" {
		return errors.New("artifact requires image_id or static_hash")
	}
	if tuple.ImageID != "" {
		if !artifact.FullHash(normalizeImageID(tuple.ImageID)) {
			return errors.New("artifact image_id must be a full image id")
		}
		if tuple.EnvelopeHash != "" {
			return errors.New("artifact envelope_hash is only valid for static-only artifacts")
		}
	}
	if tuple.ImageID == "" {
		if !artifact.FullHash(tuple.EnvelopeHash) {
			return errors.New("static-only artifact envelope_hash must be a full hash")
		}
	}
	if tuple.StaticHash != "" && !artifact.FullHash(tuple.StaticHash) {
		return errors.New("artifact static_hash must be a full hash")
	}
	return nil
}

func inspectExactImage(imageID string) (imageEntry, error) {
	out, err := utils.RunChecked("podman", []string{"image", "inspect", "--format", "json", imageID}, "")
	if err != nil {
		if _, existsErr := utils.RunChecked("podman", []string{"image", "exists", imageID}, ""); existsErr != nil {
			var commandErr *utils.CommandError
			var exitErr *exec.ExitError
			if errors.As(existsErr, &commandErr) && errors.As(commandErr, &exitErr) && exitErr.ExitCode() == 1 {
				return imageEntry{}, &artifactAbsentError{ImageID: imageID}
			}
		}
		return imageEntry{}, fmt.Errorf("podman image inspect: %w", err)
	}
	data := []byte(strings.TrimSpace(string(out)))
	if len(data) == 0 {
		return imageEntry{}, errors.New("podman image inspect returned no image")
	}
	var entries []imageEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		var entry imageEntry
		if singleErr := json.Unmarshal(data, &entry); singleErr != nil {
			return imageEntry{}, fmt.Errorf("parse podman image inspect json: %w", err)
		}
		entries = []imageEntry{entry}
	}
	if len(entries) != 1 {
		return imageEntry{}, fmt.Errorf("podman image inspect returned %d images", len(entries))
	}
	return entries[0], nil
}

func normalizeImageID(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "sha256:")
}

func staticReleasePath(app, env, release, staticHash string) string {
	return filepath.Join(identity.StaticDir(app, env), "releases", release+"-"+staticHash)
}

func staticReleaseEnvelopePathByHash(app, env, release, envelopeHash string) string {
	return filepath.Join(identity.StaticDir(app, env), "releases", ".ship-release-"+envelopeHash)
}

func activationLegacyError() error {
	return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  "legacy activation; redeploy to heal",
		"command": "ship",
	})
}

func requireV2Pointer(pointer activation.Pointer) error {
	if pointer.IsLegacy() {
		return activationLegacyError()
	}
	return nil
}

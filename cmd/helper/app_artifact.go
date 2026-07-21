package helper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/podmanruntime"
)

type artifactAbsentError struct {
	ImageID string
}

func (e *artifactAbsentError) Error() string {
	return fmt.Sprintf("pinned image %s is absent", e.ImageID)
}

type artifactValidationError struct{ Err error }

func (e *artifactValidationError) Error() string { return e.Err.Error() }
func (e *artifactValidationError) Unwrap() error { return e.Err }

type artifactPathAbsentError struct {
	Kind string
	Err  error
}

func (e *artifactPathAbsentError) Error() string {
	return fmt.Sprintf("%s is unavailable: %v", e.Kind, e.Err)
}
func (e *artifactPathAbsentError) Unwrap() error { return e.Err }

type artifactEnvelopeHashError struct{ Err error }

func (e *artifactEnvelopeHashError) Error() string { return e.Err.Error() }
func (e *artifactEnvelopeHashError) Unwrap() error { return e.Err }

type resolvedArtifact struct {
	Tuple    activationrecords.Tuple
	Envelope envelope.Envelope
	Context  *config.AppContext
	ImageID  string
}

func committedHistoryWithPointer(app, env string, pointer activationrecords.Pointer) ([]activationrecords.Tuple, bool, error) {
	if pointer.IsLegacy() {
		return nil, false, nil
	}
	return activationrecords.CommittedHistory(app, env, pointer)
}

// ResolveArtifact verifies exactly tuple. It never searches by release,
// mutable tag, or an unqualified sidecar.
func ResolveArtifact(app, env string, tuple activationrecords.Tuple) (*config.AppContext, error) {
	resolved, err := resolveArtifact(app, env, tuple)
	if err != nil {
		return nil, err
	}
	return resolved.Context, nil
}

func resolveActiveContext(app, env string) (*config.AppContext, activationrecords.Tuple, error) {
	pointer, err := readActive(app, env)
	if err != nil {
		return nil, activationrecords.Tuple{}, err
	}
	if err := requireV2Pointer(pointer); err != nil {
		return nil, activationrecords.Tuple{}, err
	}
	resolved, err := resolveArtifact(app, env, pointer.Artifact)
	if err != nil {
		return nil, activationrecords.Tuple{}, err
	}
	return resolved.Context, pointer.Artifact, nil
}

func resolveArtifact(app, env string, tuple activationrecords.Tuple) (resolvedArtifact, error) {
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
			if os.IsNotExist(err) {
				return resolvedArtifact{}, &artifactPathAbsentError{Kind: "static artifact envelope", Err: err}
			}
			return resolvedArtifact{}, fmt.Errorf("artifact %s envelope is unavailable: %w", tuple.DisplayIdentity(), err)
		}
		e, err = envelope.DecodeJSON(data)
		if err != nil {
			return resolvedArtifact{}, err
		}
		label, err := e.LabelValue()
		if err != nil || envelope.HashLabel(label) != tuple.EnvelopeHash {
			return resolvedArtifact{}, &artifactEnvelopeHashError{Err: fmt.Errorf("static artifact envelope hash does not match %s", tuple.EnvelopeHash)}
		}
	}
	if _, err := releaseMetadataFromEnvelope(e, tuple.Release); err != nil {
		return resolvedArtifact{}, err
	}
	if tuple.StaticHash != "" {
		path := staticReleasePath(app, env, tuple.Release, tuple.StaticHash)
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return resolvedArtifact{}, &artifactPathAbsentError{Kind: "static artifact tree", Err: err}
			}
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
	if ctx.HasStaticRoutes != (tuple.StaticHash != "") {
		return resolvedArtifact{}, &artifactValidationError{Err: fmt.Errorf("artifact static_hash does not match manifest serve routes")}
	}
	ctx.StaticHash = tuple.StaticHash
	return resolvedArtifact{Tuple: tuple, Envelope: e, Context: ctx, ImageID: tuple.ImageID}, nil
}

func validateArtifactTuple(tuple activationrecords.Tuple) error {
	if err := validateRelease(tuple.Release); err != nil {
		return &artifactValidationError{Err: fmt.Errorf("artifact release is invalid: %w", err)}
	}
	if err := activationrecords.ValidateArtifact(tuple); err != nil {
		return &artifactValidationError{Err: err}
	}
	return nil
}

func inspectExactImage(imageID string) (imageEntry, error) {
	entry, err := podmanruntime.CLI().InspectImage(imageID)
	var missing *podmanruntime.MissingImageError
	if errors.As(err, &missing) {
		return imageEntry{}, &artifactAbsentError{ImageID: imageID}
	}
	return entry, err
}

func normalizeImageID(value string) string {
	return podmanruntime.NormalizeImageID(value)
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

func requireV2Pointer(pointer activationrecords.Pointer) error {
	if pointer.IsLegacy() {
		return activationLegacyError()
	}
	return nil
}

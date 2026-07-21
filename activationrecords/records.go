package activationrecords

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fprl/ship/activationrecords/internal/pointer"
)

const Version = 2

type Pointer struct {
	Version    int    `json:"version"`
	Activation string `json:"activation"`
	Artifact   Tuple  `json:"artifact"`
}

type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func Validate(pointer Pointer) error {
	if pointer.Version != Version {
		return fmt.Errorf("unsupported active.json version %d", pointer.Version)
	}
	if pointer.Artifact.Release == "" {
		return fmt.Errorf("active.json requires artifact.release")
	}
	if strings.ContainsAny(pointer.Activation, "/\\") {
		return fmt.Errorf("active.json activation is not a file-safe id")
	}
	if pointer.Artifact.ImageID == "" && pointer.Artifact.StaticHash == "" {
		return fmt.Errorf("active.json artifact requires image_id or static_hash")
	}
	if pointer.Artifact.ImageID != "" && pointer.Activation == "" {
		return fmt.Errorf("active.json image artifact requires activation")
	}
	if pointer.Artifact.ImageID != "" && pointer.Artifact.EnvelopeHash != "" {
		return fmt.Errorf("active.json artifact envelope_hash is only valid for static-only artifacts")
	}
	if pointer.Artifact.ImageID == "" && pointer.Artifact.EnvelopeHash == "" {
		return fmt.Errorf("active.json static-only artifact requires envelope_hash")
	}
	if pointer.Artifact.StaticHash != "" && !fullHash(pointer.Artifact.StaticHash) {
		return fmt.Errorf("active.json static_hash must be sha256")
	}
	if pointer.Artifact.EnvelopeHash != "" && !fullHash(pointer.Artifact.EnvelopeHash) {
		return fmt.Errorf("active.json envelope_hash must be sha256")
	}
	if pointer.Artifact.ImageID != "" && !fullHash(strings.TrimPrefix(pointer.Artifact.ImageID, "sha256:")) {
		return fmt.Errorf("active.json image_id must be a full image id")
	}
	return nil
}

func Read(app, env string) (Pointer, error) {
	path := pointer.Path(app, env)
	data, err := os.ReadFile(path)
	if err != nil {
		return Pointer{}, err
	}
	var result Pointer
	if err := json.Unmarshal(data, &result); err != nil {
		return Pointer{}, &ValidationError{Err: fmt.Errorf("invalid active.json: %w", err)}
	}
	if err := Validate(result); err != nil {
		return Pointer{}, &ValidationError{Err: err}
	}
	if err := ValidateArtifact(result.Artifact); err != nil {
		return Pointer{}, &ValidationError{Err: err}
	}
	return result, nil
}

func Publish(app, env string, value Pointer) error { return PublishPrepared(app, env, value, nil) }

// PublishPrepared is the publish API for activation pointers. The hook is
// used only for pre-rename preparation and failure-injection conformance tests.
func PublishPrepared(app, env string, value Pointer, prepare func(string) error) error {
	if err := Validate(value); err != nil {
		return err
	}
	if err := ValidateArtifact(value.Artifact); err != nil {
		return err
	}
	return pointer.Publish(pointer.Path(app, env), value, prepare)
}

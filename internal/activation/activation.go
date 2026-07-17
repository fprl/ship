// Package activation owns the small durable activation pointer and the
// validation rules for its versioned shape.
package activation

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fprl/ship/internal/artifact"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
)

const Version = 2

// LegacyActivation is returned by Read for a v1 pointer. It is state, not a
// parse fallback: callers must keep serving but refuse trust-sensitive verbs
// until redeploy.
type LegacyActivation struct {
	Release      string `json:"release,omitempty"`
	Activation   string `json:"activation,omitempty"`
	EnvelopeHash string `json:"envelope_hash,omitempty"`
}

type Pointer struct {
	Version    int               `json:"version"`
	Activation string            `json:"activation"`
	Artifact   artifact.Tuple    `json:"artifact"`
	Legacy     *LegacyActivation `json:"-"`
}

type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func Validate(pointer Pointer) error {
	if pointer.Legacy != nil || pointer.Version == 1 {
		return nil
	}
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
	if pointer.Artifact.StaticHash != "" && !artifact.FullHash(pointer.Artifact.StaticHash) {
		return fmt.Errorf("active.json static_hash must be sha256")
	}
	if pointer.Artifact.EnvelopeHash != "" && !artifact.FullHash(pointer.Artifact.EnvelopeHash) {
		return fmt.Errorf("active.json envelope_hash must be sha256")
	}
	if pointer.Artifact.ImageID != "" && !artifact.FullHash(strings.TrimPrefix(pointer.Artifact.ImageID, "sha256:")) {
		return fmt.Errorf("active.json image_id must be a full image id")
	}
	return nil
}

func Read(app, env string) (Pointer, error) {
	data, err := os.ReadFile(identity.ActiveFile(app, env))
	if err != nil {
		return Pointer{}, err
	}
	var shape struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		return Pointer{}, &ValidationError{Err: fmt.Errorf("invalid active.json: %w", err)}
	}
	if shape.Version == nil || *shape.Version == 1 {
		var legacy LegacyActivation
		if err := json.Unmarshal(data, &legacy); err != nil {
			return Pointer{}, &ValidationError{Err: fmt.Errorf("invalid legacy active.json: %w", err)}
		}
		return Pointer{Version: 1, Activation: legacy.Activation, Legacy: &legacy}, nil
	}
	var pointer Pointer
	if err := json.Unmarshal(data, &pointer); err != nil {
		return Pointer{}, &ValidationError{Err: fmt.Errorf("invalid active.json: %w", err)}
	}
	if err := Validate(pointer); err != nil {
		return Pointer{}, &ValidationError{Err: err}
	}
	return pointer, nil
}

func Write(app, env string, pointer Pointer) error {
	return WritePrepared(app, env, pointer, nil)
}

// WritePrepared publishes active.json only after prepare has completed on
// the fully written, final-mode temporary inode.
func WritePrepared(app, env string, pointer Pointer, prepare func(string) error) error {
	if pointer.IsLegacy() {
		return fmt.Errorf("cannot write legacy activation; redeploy to heal")
	}
	if err := Validate(pointer); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return err
	}
	return store.AtomicWritePrepared(identity.ActiveFile(app, env), append(data, '\n'), 0644, prepare)
}

// Legacy returns whether this pointer predates artifact-keyed trust.
func (p Pointer) IsLegacy() bool { return p.Legacy != nil || p.Version == 1 }

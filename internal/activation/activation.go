// Package activation owns the small durable activation pointer and the
// validation rules for its versioned shape.
package activation

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
)

const Version = 1

type Pointer struct {
	Version      int    `json:"version"`
	Release      string `json:"release"`
	Activation   string `json:"activation"`
	EnvelopeHash string `json:"envelope_hash"`
}

func Validate(pointer Pointer) error {
	if pointer.Version != Version {
		return fmt.Errorf("unsupported active.json version %d", pointer.Version)
	}
	if pointer.Release == "" || pointer.Activation == "" || pointer.EnvelopeHash == "" {
		return fmt.Errorf("active.json requires release, activation, and envelope_hash")
	}
	if strings.ContainsAny(pointer.Activation, "/\\") {
		return fmt.Errorf("active.json activation is not a file-safe id")
	}
	if len(pointer.EnvelopeHash) != 64 {
		return fmt.Errorf("active.json envelope_hash must be sha256")
	}
	if _, err := hex.DecodeString(pointer.EnvelopeHash); err != nil {
		return fmt.Errorf("active.json envelope_hash must be sha256: %v", err)
	}
	return nil
}

func Read(app, env string) (Pointer, error) {
	data, err := os.ReadFile(identity.ActiveFile(app, env))
	if err != nil {
		return Pointer{}, err
	}
	var pointer Pointer
	if err := json.Unmarshal(data, &pointer); err != nil {
		return Pointer{}, fmt.Errorf("invalid active.json: %w", err)
	}
	if err := Validate(pointer); err != nil {
		return Pointer{}, err
	}
	return pointer, nil
}

func Write(app, env string, pointer Pointer) error {
	return WritePrepared(app, env, pointer, nil)
}

// WritePrepared publishes active.json only after prepare has completed on
// the fully written, final-mode temporary inode.
func WritePrepared(app, env string, pointer Pointer, prepare func(string) error) error {
	return WritePreparedResult(app, env, pointer, prepare).Err
}

func WritePreparedResult(app, env string, pointer Pointer, prepare func(string) error) store.WriteResult {
	if err := Validate(pointer); err != nil {
		return store.WriteResult{Err: err}
	}
	data, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return store.WriteResult{Err: err}
	}
	return store.AtomicWritePreparedResult(identity.ActiveFile(app, env), append(data, '\n'), 0644, prepare)
}

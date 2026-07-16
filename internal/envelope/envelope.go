// Package envelope stores the release's immutable effective configuration
// beside the image/static artifact that needs it for rollback.
package envelope

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	Schema          = 1
	ManifestLimit   = 64 * 1024
	SerializedLimit = 64 * 1024
	Label           = "ship.release_envelope"
)

type Envelope struct {
	Schema   int             `json:"schema"`
	Manifest string          `json:"manifest"`
	Metadata json.RawMessage `json:"metadata"`
}

func New(manifest []byte, metadata any) (Envelope, error) {
	if len(manifest) > ManifestLimit {
		return Envelope{}, fmt.Errorf("effective manifest is %d bytes; release envelope limit is 64 KiB", len(manifest))
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal release metadata: %w", err)
	}
	e := Envelope{Schema: Schema, Manifest: string(manifest), Metadata: metadataJSON}
	if err := e.Validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func (e Envelope) Validate() error {
	if e.Schema != Schema {
		return fmt.Errorf("unsupported release envelope schema %d", e.Schema)
	}
	if len([]byte(e.Manifest)) > ManifestLimit {
		return fmt.Errorf("effective manifest is %d bytes; release envelope limit is 64 KiB", len([]byte(e.Manifest)))
	}
	if !json.Valid(e.Metadata) {
		return fmt.Errorf("release envelope metadata is invalid JSON")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal release envelope: %w", err)
	}
	if len(data) > SerializedLimit {
		return fmt.Errorf("serialized release envelope is %d bytes; release envelope limit is 64 KiB", len(data))
	}
	return nil
}

func (e Envelope) JSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

func (e Envelope) LabelValue() (string, error) {
	data, err := e.JSON()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func DecodeLabel(value string) (Envelope, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return Envelope{}, fmt.Errorf("decode release envelope: %w", err)
	}
	return DecodeJSON(data)
}

func DecodeJSON(data []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return Envelope{}, fmt.Errorf("parse release envelope: %w", err)
	}
	if err := e.Validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func HashLabel(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

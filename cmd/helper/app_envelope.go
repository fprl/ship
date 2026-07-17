package helper

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
)

func releaseEnvelope(manifest []byte, meta releaseMetadata) (envelope.Envelope, string, error) {
	e, err := envelope.New(manifest, meta)
	if err != nil {
		return envelope.Envelope{}, "", err
	}
	label, err := e.LabelValue()
	if err != nil {
		return envelope.Envelope{}, "", err
	}
	return e, label, nil
}

func writeStaticReleaseEnvelope(app, env, release string, e envelope.Envelope) error {
	data, err := e.JSON()
	if err != nil {
		return err
	}
	label, err := e.LabelValue()
	if err != nil {
		return err
	}
	path := staticReleaseEnvelopePath(app, env, release, envelope.HashLabel(label))
	if err := store.AtomicWrite(path, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("write static release envelope: %v", err)
	}
	return nil
}

func staticReleaseEnvelopePath(app, env, release, envelopeHash string) string {
	return filepath.Join(identity.StaticDir(app, env), "releases", ".ship-release-"+envelopeHash)
}

func releaseMetadataFromEnvelope(e envelope.Envelope, release string) (releaseMetadata, error) {
	var meta releaseMetadata
	if err := json.Unmarshal(e.Metadata, &meta); err != nil {
		return releaseMetadata{}, fmt.Errorf("parse release envelope metadata: %v", err)
	}
	if err := validateReleaseMetadata(meta); err != nil {
		return releaseMetadata{}, err
	}
	if meta.Release != release {
		return releaseMetadata{}, fmt.Errorf("release envelope metadata names %s, expected %s", meta.Release, release)
	}
	return meta, nil
}

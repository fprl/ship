package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
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
	if _, err := utils.RunChecked("chown", []string{"root:root", path}, ""); err != nil {
		return fmt.Errorf("chown static release envelope: %v", err)
	}
	return nil
}

func readStaticReleaseEnvelope(app, env, release string) (envelope.Envelope, error) {
	entries, err := os.ReadDir(filepath.Join(identity.StaticDir(app, env), "releases", release))
	if err != nil {
		return envelope.Envelope{}, fmt.Errorf("read static release envelopes: %w", err)
	}
	var found []envelope.Envelope
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".ship-release-") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(identity.StaticDir(app, env), "releases", release, entry.Name()))
		if readErr != nil {
			return envelope.Envelope{}, fmt.Errorf("read static release envelope: %w", readErr)
		}
		e, decodeErr := envelope.DecodeJSON(data)
		if decodeErr != nil {
			return envelope.Envelope{}, decodeErr
		}
		found = append(found, e)
	}
	if len(found) != 1 {
		return envelope.Envelope{}, fmt.Errorf("static release %s has %d envelope sidecars; envelope hash is required", release, len(found))
	}
	return found[0], nil
}

func staticReleaseEnvelopePath(app, env, release, envelopeHash string) string {
	return filepath.Join(identity.StaticDir(app, env), "releases", release, ".ship-release-"+envelopeHash[:12])
}

func readStaticReleaseEnvelopeByHash(app, env, release, envelopeHash string) (envelope.Envelope, error) {
	if len(envelopeHash) < 12 {
		return envelope.Envelope{}, fmt.Errorf("invalid release envelope hash")
	}
	path := staticReleaseEnvelopePath(app, env, release, envelopeHash)
	data, err := os.ReadFile(path)
	if err != nil {
		return envelope.Envelope{}, fmt.Errorf("read static release envelope %s: %w", envelopeHash, err)
	}
	e, err := envelope.DecodeJSON(data)
	if err != nil {
		return envelope.Envelope{}, err
	}
	label, err := e.LabelValue()
	if err != nil || envelope.HashLabel(label) != envelopeHash {
		return envelope.Envelope{}, fmt.Errorf("static release envelope hash does not match %s", envelopeHash)
	}
	return e, nil
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

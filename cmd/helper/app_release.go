package helper

import (
	"fmt"

	"github.com/fprl/ship/internal/deployrequest"
	"github.com/fprl/ship/internal/releaseid"
)

type releaseMetadata struct {
	SchemaVersion int    `json:"schema_version"`
	Release       string `json:"release"`
	Dirty         bool   `json:"dirty"`
	BaseCommit    string `json:"base_commit"`
	CreatedAt     string `json:"created_at"`
	StaticHash    string `json:"static_hash,omitempty"`
}

func newReleaseMetadata(release string, dirty bool, baseCommit string, createdAt string) (releaseMetadata, error) {
	info, err := releaseid.Parse(release)
	if err != nil {
		return releaseMetadata{}, err
	}
	meta := releaseMetadata{
		SchemaVersion: 1,
		Release:       release,
		Dirty:         dirty,
		BaseCommit:    baseCommit,
		CreatedAt:     createdAt,
		StaticHash:    info.StaticHash,
	}
	if err := validateReleaseMetadata(meta); err != nil {
		return releaseMetadata{}, err
	}
	return meta, nil
}

func validateReleaseMetadata(meta releaseMetadata) error {
	if meta.SchemaVersion != 1 {
		return fmt.Errorf("unsupported release metadata schema version %d", meta.SchemaVersion)
	}
	info, err := releaseid.Parse(meta.Release)
	if err != nil {
		return err
	}
	if meta.StaticHash != info.StaticHash {
		return fmt.Errorf("release metadata static_hash %q does not match release %q", meta.StaticHash, meta.Release)
	}
	return deployrequest.ValidateReleaseProvenance(meta.Release, meta.Dirty, meta.BaseCommit, meta.CreatedAt)
}

package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/releaseid"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
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
	if err := validateRelease(meta.Release); err != nil {
		return err
	}
	info, err := releaseid.Parse(meta.Release)
	if err != nil {
		return err
	}
	if !releaseid.IsGitCommit(meta.BaseCommit) {
		return fmt.Errorf("invalid base commit: %q", meta.BaseCommit)
	}
	createdAt, err := time.Parse(time.RFC3339, meta.CreatedAt)
	if err != nil {
		return fmt.Errorf("invalid release created_at: %v", err)
	}
	if meta.StaticHash != info.StaticHash {
		return fmt.Errorf("release metadata static_hash %q does not match release %q", meta.StaticHash, meta.Release)
	}
	if meta.Dirty {
		if !info.Dirty {
			return fmt.Errorf("dirty release metadata requires <base-sha>-dirty-<timestamp>, got %q", meta.Release)
		}
		if !strings.HasPrefix(meta.BaseCommit, info.Base) {
			return fmt.Errorf("dirty release %q does not match base commit %q", meta.Release, meta.BaseCommit)
		}
		if want := releaseid.DirtyTimestamp(createdAt); info.Timestamp != want {
			return fmt.Errorf("dirty release timestamp %q does not match created_at %q", info.Timestamp, meta.CreatedAt)
		}
	} else {
		if info.Dirty {
			return fmt.Errorf("release %q has dirty shape but dirty=false", meta.Release)
		}
		if !strings.HasPrefix(meta.BaseCommit, info.Base) {
			return fmt.Errorf("clean release %q does not match base commit %q", meta.Release, meta.BaseCommit)
		}
	}
	return nil
}

func writeReleaseMetadata(app, env string, meta releaseMetadata) error {
	if err := validateReleaseMetadata(meta); err != nil {
		return err
	}
	path := identity.ReleaseMetadataFile(app, env, meta.Release)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir release metadata dir: %v", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := store.AtomicWrite(path, data, 0644); err != nil {
		return fmt.Errorf("write release metadata: %v", err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", path}, ""); err != nil {
		return fmt.Errorf("chown release metadata: %v", err)
	}
	return nil
}

func readReleaseMetadata(app, env, release string) (releaseMetadata, error) {
	if err := validateRelease(release); err != nil {
		return releaseMetadata{}, err
	}
	return readReleaseMetadataFile(identity.ReleaseMetadataFile(app, env, release), release)
}

func readReleaseMetadataFile(path, release string) (releaseMetadata, error) {
	if err := validateRelease(release); err != nil {
		return releaseMetadata{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return releaseMetadata{}, fmt.Errorf("read release metadata %s: %v", path, err)
	}
	var meta releaseMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return releaseMetadata{}, fmt.Errorf("parse release metadata: %v", err)
	}
	if err := validateReleaseMetadata(meta); err != nil {
		return releaseMetadata{}, err
	}
	if meta.Release != release {
		return releaseMetadata{}, fmt.Errorf("release metadata names %s, expected %s", meta.Release, release)
	}
	return meta, nil
}

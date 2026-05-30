package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/utils"
)

var (
	gitCommitRe      = regexp.MustCompile(`^[a-f0-9]{7,64}$`)
	staticSuffixRe   = regexp.MustCompile(`-s[0-9a-f]{12}$`)
	dirtyReleaseIDRe = regexp.MustCompile(`^([a-f0-9]{7,64})-dirty-([0-9]{8}t[0-9]{6}z)$`)
	cleanReleaseIDRe = regexp.MustCompile(`^[a-f0-9]{7,64}$`)
)

type releaseMetadata struct {
	SchemaVersion int    `json:"schema_version"`
	Release       string `json:"release"`
	Dirty         bool   `json:"dirty"`
	BaseCommit    string `json:"base_commit"`
	CreatedAt     string `json:"created_at"`
}

func newReleaseMetadata(release string, dirty bool, baseCommit string, createdAt string) (releaseMetadata, error) {
	meta := releaseMetadata{
		SchemaVersion: 1,
		Release:       release,
		Dirty:         dirty,
		BaseCommit:    baseCommit,
		CreatedAt:     createdAt,
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
	if !gitCommitRe.MatchString(meta.BaseCommit) {
		return fmt.Errorf("invalid base commit: %q", meta.BaseCommit)
	}
	createdAt, err := time.Parse(time.RFC3339, meta.CreatedAt)
	if err != nil {
		return fmt.Errorf("invalid release created_at: %v", err)
	}
	baseRelease := releaseWithoutStaticSuffix(meta.Release)
	if meta.Dirty {
		match := dirtyReleaseIDRe.FindStringSubmatch(baseRelease)
		if match == nil {
			return fmt.Errorf("dirty release metadata requires <base-sha>-dirty-<timestamp>, got %q", meta.Release)
		}
		if !strings.HasPrefix(meta.BaseCommit, match[1]) {
			return fmt.Errorf("dirty release %q does not match base commit %q", meta.Release, meta.BaseCommit)
		}
		if want := createdAt.UTC().Format("20060102t150405z"); match[2] != want {
			return fmt.Errorf("dirty release timestamp %q does not match created_at %q", match[2], meta.CreatedAt)
		}
	} else {
		if strings.Contains(baseRelease, "-dirty-") {
			return fmt.Errorf("release %q has dirty shape but dirty=false", meta.Release)
		}
		if !cleanReleaseIDRe.MatchString(baseRelease) {
			return fmt.Errorf("clean release metadata requires a git commit release id, got %q", meta.Release)
		}
		if !strings.HasPrefix(meta.BaseCommit, baseRelease) {
			return fmt.Errorf("clean release %q does not match base commit %q", meta.Release, meta.BaseCommit)
		}
	}
	return nil
}

func releaseWithoutStaticSuffix(release string) string {
	if staticSuffixRe.MatchString(release) {
		return release[:len(release)-14]
	}
	return release
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
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write release metadata: %v", err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", path}, ""); err != nil {
		return fmt.Errorf("chown release metadata: %v", err)
	}
	return nil
}

func readReleaseMetadata(app, env, release string) (releaseMetadata, bool, error) {
	if err := validateRelease(release); err != nil {
		return releaseMetadata{}, false, err
	}
	data, err := os.ReadFile(identity.ReleaseMetadataFile(app, env, release))
	if err != nil {
		if os.IsNotExist(err) {
			return releaseMetadata{}, false, nil
		}
		return releaseMetadata{}, false, err
	}
	var meta releaseMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return releaseMetadata{}, false, fmt.Errorf("parse release metadata: %v", err)
	}
	if err := validateReleaseMetadata(meta); err != nil {
		return releaseMetadata{}, false, err
	}
	if meta.Release != release {
		return releaseMetadata{}, false, fmt.Errorf("release metadata names %s, expected %s", meta.Release, release)
	}
	return meta, true, nil
}

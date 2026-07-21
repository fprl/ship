// Package deployrequest owns the semantic request shared by the Ship client
// and the box deploy ingest adapter. It intentionally contains no host paths.
package deployrequest

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fprl/ship/internal/deploybundle"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/releaseid"
)

type Actor struct {
	SSHKeyComment string `json:"ssh_key_comment"`
	GitAuthor     string `json:"git_author"`
}

type Request struct {
	App          string
	Env          string
	Bundle       deploybundle.Metadata
	SHA          string
	Dirty        bool
	BaseCommit   string
	CreatedAt    string
	Rebuild      bool
	Progress     bool
	Logs         bool
	TLS          string
	PreviewAlias string
	Actor        Actor
}

func (r Request) Validate() error {
	if !names.AppRe.MatchString(r.App) {
		return fmt.Errorf("invalid app name: %q", r.App)
	}
	if !names.EnvRe.MatchString(r.Env) {
		return fmt.Errorf("invalid env name: %q", r.Env)
	}
	if err := r.Bundle.Validate(); err != nil {
		return err
	}
	if err := ValidateReleaseProvenance(r.SHA, r.Dirty, r.BaseCommit, r.CreatedAt); err != nil {
		return err
	}
	if r.TLS != "" && r.TLS != "auto" && r.TLS != "internal" {
		return fmt.Errorf("invalid deploy TLS mode: %q", r.TLS)
	}
	return nil
}

// ValidateReleaseProvenance is the single clean/dirty release invariant used
// by the wire request and the durable release metadata reader.
func ValidateReleaseProvenance(release string, dirty bool, baseCommit, createdAtValue string) error {
	info, err := releaseid.Parse(release)
	if err != nil {
		return err
	}
	if !releaseid.IsGitCommit(baseCommit) {
		return fmt.Errorf("invalid base commit: %q", baseCommit)
	}
	createdAt, err := time.Parse(time.RFC3339, createdAtValue)
	if err != nil {
		return fmt.Errorf("invalid release created_at: %v", err)
	}
	if dirty {
		if !info.Dirty {
			return fmt.Errorf("dirty release metadata requires <base-sha>-dirty-<timestamp>, got %q", release)
		}
		if !strings.HasPrefix(baseCommit, info.Base) {
			return fmt.Errorf("dirty release %q does not match base commit %q", release, baseCommit)
		}
		if want := releaseid.DirtyTimestamp(createdAt); info.Timestamp != want {
			return fmt.Errorf("dirty release timestamp %q does not match created_at %q", info.Timestamp, createdAtValue)
		}
	} else {
		if info.Dirty {
			return fmt.Errorf("release %q has dirty shape but dirty=false", release)
		}
		if !strings.HasPrefix(baseCommit, info.Base) {
			return fmt.Errorf("clean release %q does not match base commit %q", release, baseCommit)
		}
	}
	return nil
}

// CommandArgs is the canonical command projection following `ship server`.
func (r Request) CommandArgs() []string {
	args := []string{"app", "apply"}
	if r.Progress {
		args = append(args, "--progress")
	}
	if r.Rebuild {
		args = append(args, "--rebuild")
	}
	if r.Logs {
		args = append(args, "--logs")
	}
	if r.TLS != "" {
		args = append(args, "--tls", r.TLS)
	}
	if r.PreviewAlias != "" {
		args = append(args, "--preview-alias", r.PreviewAlias)
	}
	if r.Dirty {
		args = append(args, "--dirty")
	}
	return append(args,
		"--bundle-size", strconv.FormatInt(r.Bundle.Size, 10),
		"--bundle-sha256", r.Bundle.SHA256,
		"--sha", r.SHA,
		"--base-commit", r.BaseCommit,
		"--created-at", r.CreatedAt,
		"--ssh-key-comment", r.Actor.SSHKeyComment,
		"--git-author", r.Actor.GitAuthor,
		r.App, r.Env,
	)
}

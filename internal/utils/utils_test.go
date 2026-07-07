package utils

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fprl/simple-vps/internal/errcat"
)

func TestRunCheckedWithTimeout(t *testing.T) {
	_, err := RunCheckedWithTimeout("sh", []string{"-c", "sleep 1"}, "", 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "command timed out after") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUsageOrManifestFailureClassification(t *testing.T) {
	for _, message := range []string{
		"ship.toml not found",
		"failed to parse ship.toml",
		"manifest invalid: name is required",
		"--config must point to ship.toml",
		"invalid app name: bad",
	} {
		if !usageOrManifestFailure(message) {
			t.Fatalf("expected usage/manifest failure for %q", message)
		}
	}
	for _, message := range []string{
		"deploy failed: release command failed",
		"missing secret DATABASE_URL",
		"caddy reload failed",
	} {
		if usageOrManifestFailure(message) {
			t.Fatalf("expected operation failure for %q", message)
		}
	}
}

func TestMissingManifestRemediatesShipInit(t *testing.T) {
	err := errors.New("this is a project command, but /tmp/app/ship.toml was not found.\nRun it from a directory containing ship.toml.\nTo start a new project, run `ship init`.")
	coded := normalizeExitError(err, 2)
	if coded.Code() != errcat.CodeManifestInvalid {
		t.Fatalf("code = %s, want %s", coded.Code(), errcat.CodeManifestInvalid)
	}
	if coded.Remediation() != "ship init" {
		t.Fatalf("remediation = %q, want ship init", coded.Remediation())
	}
}

func TestInvalidManifestRemediatesFixShipToml(t *testing.T) {
	err := errors.New("failed to parse ship.toml: unknown field \"runtime\"")
	coded := normalizeExitError(err, 2)
	if coded.Code() != errcat.CodeManifestInvalid {
		t.Fatalf("code = %s, want %s", coded.Code(), errcat.CodeManifestInvalid)
	}
	if coded.Remediation() != "fix ship.toml" {
		t.Fatalf("remediation = %q, want fix ship.toml", coded.Remediation())
	}
}

func TestMissingDockerfileHasDistinctCodeAndInitRemediation(t *testing.T) {
	err := errors.New("manifest declares processes but is missing a Dockerfile")
	coded := normalizeExitError(err, 2)
	if coded.Code() != errcat.CodeDockerfileMissing {
		t.Fatalf("code = %s, want %s", coded.Code(), errcat.CodeDockerfileMissing)
	}
	if coded.Remediation() != "ship init" {
		t.Fatalf("remediation = %q, want ship init", coded.Remediation())
	}
}

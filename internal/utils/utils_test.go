package utils

import (
	"strings"
	"testing"
	"time"
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

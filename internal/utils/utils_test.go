package utils

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
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

func TestCommandErrorRedactsReleaseEnvelopeFromDisplayAndOutput(t *testing.T) {
	value := "YWJjMTIzNGRhdGE="
	err := &CommandError{Name: "podman", Args: []string{"build", "--label", "ship.release_envelope=" + value}, Stderr: "ship.release_envelope=" + value + "\n"}
	text := err.Error() + "\n" + err.CombinedOutput()
	if strings.Contains(text, value) {
		t.Fatalf("envelope leaked in command error: %s", text)
	}
	if !strings.Contains(text, "ship.release_envelope=<redacted, "+"16 bytes>") {
		t.Fatalf("redaction marker missing: %s", text)
	}
}

func TestNormalizeRawErrorsDoesNotStringMatchManifestText(t *testing.T) {
	coded := normalizeExitError(errors.New("ship.toml not found"), 1)
	if coded.Code() != errcat.CodeOperationFailed {
		t.Fatalf("code = %s, want %s", coded.Code(), errcat.CodeOperationFailed)
	}
}

func TestNormalizeUsageFallbackUsesUsageError(t *testing.T) {
	coded := normalizeExitError(errors.New("--config must point to ship.toml"), 2)
	if coded.Code() != errcat.CodeUsageError {
		t.Fatalf("code = %s, want %s", coded.Code(), errcat.CodeUsageError)
	}
}

func TestNormalizeManifestUserAtBoxRemediation(t *testing.T) {
	coded := normalizeExitError(&config.ManifestError{Details: []string{`box must be a host, not user@host; remove the user part (use box = "203.0.113.7")`}}, 1)
	if coded.Code() != errcat.CodeManifestInvalid {
		t.Fatalf("code = %s, want %s", coded.Code(), errcat.CodeManifestInvalid)
	}
	if coded.Remediation() != "ship" {
		t.Fatalf("remediation = %q", coded.Remediation())
	}
}

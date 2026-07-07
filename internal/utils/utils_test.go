package utils

import (
	"errors"
	"strings"
	"testing"
	"time"

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

func TestBackupDirDefaultMatchesServerContract(t *testing.T) {
	t.Setenv("SHIP_BACKUP_DIR", "")
	if got := BackupDir(); got != "/etc/ship/backups" {
		t.Fatalf("unexpected backup dir: %s", got)
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

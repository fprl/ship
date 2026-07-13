package helper

import (
	"errors"
	"strings"
	"testing"
)

func TestPreviewCapabilityIsPerEnvAndIdempotent(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	first, err := ensurePreviewCapability("api", "feat-x-ab12")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensurePreviewCapability("api", "feat-x-ab12")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == "" {
		t.Fatalf("capability should be stable and non-empty: first=%q second=%q", first, second)
	}
}

func TestPreviewProtectionCaddyFailureIncludesManualFixPath(t *testing.T) {
	path := "/etc/caddy/conf.d/api.preview.caddy"
	err := caddyStageActionError(caddyReloadStageError{
		Stage:      "validate",
		Err:        errors.New("invalid config"),
		RestoreErr: errors.New("restore failed"),
	}, "updating preview protection", path)
	if !strings.Contains(err.Error(), "manual fix required at "+path) {
		t.Fatalf("error = %q, want manual fix path", err)
	}
}

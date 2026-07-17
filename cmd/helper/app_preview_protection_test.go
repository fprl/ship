package helper

import (
	"errors"
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

func TestPreviewProtectionCaddyFailureReportsReloadStage(t *testing.T) {
	err := caddyStageActionError(caddyReloadStageError{
		Stage: "reload",
		Err:   errors.New("reload failed"),
	}, "updating preview protection")
	if err == nil || err.Error() != "caddy reload (updating preview protection) failed: reload failed" {
		t.Fatalf("error = %v", err)
	}
}

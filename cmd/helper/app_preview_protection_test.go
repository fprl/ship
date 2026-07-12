package helper

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fprl/ship/internal/secrets"
)

func TestPreviewProtectionCredentialsAreAppWideAndIdempotent(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	first, err := ensurePreviewProtectionCredentials("api")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensurePreviewProtectionCredentials("api")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Password == "" || first.BypassToken == "" {
		t.Fatalf("credentials should be stable and non-empty: first=%+v second=%+v", first, second)
	}
	if _, err := secrets.Get("api", "preview", previewPasswordKey); err == nil {
		t.Fatal("protection credentials must not occupy user-managed preview secret scope")
	}
	info, err := os.Stat(filepath.Join(secrets.AppDir("api", previewProtectionNamespace), previewPasswordKey))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential file mode = %o, want 0600", info.Mode().Perm())
	}
}

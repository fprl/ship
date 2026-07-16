package helper

import (
	"path/filepath"
	"testing"
)

func setTestStateRoot(t *testing.T, root string) {
	t.Helper()
	t.Setenv("SHIP_STATE_DIR", root)
	t.Setenv("SHIP_VAR_DIR", filepath.Join(root, "var"))
	t.Setenv("SHIP_RUN_DIR", filepath.Join(root, "run"))
}

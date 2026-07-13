package host

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDeployTmpDirDefaultMatchesServerAPI(t *testing.T) {
	t.Setenv("SHIP_DEPLOY_TMP_DIR", "")
	if got := DeployTmpDir(); got != "/tmp/ship-deploy" {
		t.Fatalf("unexpected deploy temp dir: %s", got)
	}
}

func TestValidateDeployTmpSourceRejectsNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_DEPLOY_TMP_DIR", root)

	regular := filepath.Join(root, "snapshot.tar")
	if err := os.WriteFile(regular, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateDeployTmpSource(regular); err != nil {
		t.Fatalf("regular file rejected: %v", err)
	}

	fifo := filepath.Join(root, "snapshot.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	// A FIFO passed validation before this fix; a later blocking open would
	// hang the helper while it holds the env lifecycle lock.
	if _, err := ValidateDeployTmpSource(fifo); err == nil {
		t.Fatal("expected FIFO source to be rejected")
	}
}

func TestPathIsRelativeToAllowsNamesStartingWithDotDot(t *testing.T) {
	if !PathIsRelativeTo("/srv/app/..data/file", "/srv/app") {
		t.Fatal("expected path under base to be accepted")
	}
	if PathIsRelativeTo("/srv/app-other/file", "/srv/app") {
		t.Fatal("expected sibling path to be rejected")
	}
}

package host

import (
	"testing"
)

func TestDeployTmpDirDefaultMatchesServerAPI(t *testing.T) {
	t.Setenv("SHIP_DEPLOY_TMP_DIR", "")
	if got := DeployTmpDir(); got != "/tmp/simple-vps-deploy" {
		t.Fatalf("unexpected deploy temp dir: %s", got)
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

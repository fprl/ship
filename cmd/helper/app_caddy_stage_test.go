package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/config"
)

func testCaddyContext() *config.AppContext {
	port := 3000
	return &config.AppContext{
		Routes:    map[string]config.Route{"web.example.com": {Host: "web.example.com", Process: "web"}},
		Processes: map[string]config.Process{"web": {Port: &port}},
	}
}

func setupCaddyStageTest(t *testing.T, podman string) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "podman", podman)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(root, "conf.d", "api.production.caddy")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	return root, path
}

func TestCaddyCandidateReloadFailureLeavesServingFragmentUntouched(t *testing.T) {
	_, path := setupCaddyStageTest(t, "#!/usr/bin/env sh\nif [ \"$1\" = \"exec\" ] && [ \"$4\" = \"reload\" ]; then exit 1; fi\nexit 0\n")
	old := []byte("old serving fragment\n")
	if err := os.WriteFile(path, old, 0644); err != nil {
		t.Fatal(err)
	}

	err := renderAndReloadAppCaddy(path, "api", "production", testCaddyContext(), "abc1234", nil)
	if err == nil || !strings.Contains(err.Error(), "caddy reload failed") {
		t.Fatalf("reload error = %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(old) {
		t.Fatalf("serving fragment changed on reload failure: %q", got)
	}
}

func TestCaddyCandidateIsReloadedBeforePublication(t *testing.T) {
	_, path := setupCaddyStageTest(t, "#!/usr/bin/env sh\nif [ \"$1\" = \"exec\" ] && [ \"$4\" = \"reload\" ]; then\n  if grep -q reverse_proxy \"$FRAGMENT\" 2>/dev/null; then exit 2; fi\nfi\nexit 0\n")
	t.Setenv("FRAGMENT", path)
	if err := os.WriteFile(path, []byte("old serving fragment\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := renderAndReloadAppCaddy(path, "api", "production", testCaddyContext(), "abc1234", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "reverse_proxy") {
		t.Fatalf("published fragment does not contain candidate: %q", got)
	}
}

func TestCaddyReloadIsUnconditionalAndCleansReceipt(t *testing.T) {
	root, path := setupCaddyStageTest(t, "#!/usr/bin/env sh\nif [ \"$1\" = \"exec\" ] && [ \"$4\" = \"reload\" ]; then printf 'reload\\n' >> \"$CADDY_LOG\"; fi\nexit 0\n")
	logPath := filepath.Join(root, "caddy.log")
	t.Setenv("CADDY_LOG", logPath)
	if err := os.WriteFile(path, []byte("old serving fragment\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".loaded", []byte("stale receipt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := renderAndReloadAppCaddy(path, "api", "production", testCaddyContext(), "abc1234", nil); err != nil {
			t.Fatal(err)
		}
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(log), "reload"); got != 2 {
		t.Fatalf("reload count = %d, want 2", got)
	}
	if _, err := os.Stat(path + ".loaded"); !os.IsNotExist(err) {
		t.Fatalf("stale receipt still exists: %v", err)
	}
}

func TestCaddyRemovalReloadsCandidateWithoutFragment(t *testing.T) {
	_, path := setupCaddyStageTest(t, "#!/usr/bin/env sh\nif [ \"$1\" = \"exec\" ] && [ \"$4\" = \"reload\" ]; then\n  config=\"$6\"\n  target=\"$(dirname \"$config\")/conf.d/$(basename \"$FRAGMENT\")\"\n  if [ -e \"$target\" ]; then exit 2; fi\nfi\nexit 0\n")
	t.Setenv("FRAGMENT", path)
	if err := os.WriteFile(path, []byte("old serving fragment\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := removeAndReloadAppCaddy(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fragment still exists after removal: %v", err)
	}
}

func TestCaddyConvergeHealsCrashWindowAfterNoOpReload(t *testing.T) {
	_, path := setupCaddyStageTest(t, "#!/usr/bin/env sh\nexit 0\n")
	if err := os.WriteFile(path, []byte("different rendered content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := renderAndReloadAppCaddy(path, "api", "production", testCaddyContext(), "abc1234", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := renderAppCaddyfileWithProcessNames("api", "production", testCaddyContext(), "abc1234", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("converged fragment = %q, want rendered candidate %q", got, want)
	}
}

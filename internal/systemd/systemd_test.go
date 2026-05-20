package systemd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployTmpDirDefaultMatchesServerAPI(t *testing.T) {
	t.Setenv("SIMPLE_VPS_DEPLOY_TMP_DIR", "")
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

func TestValidateEnvironmentContentRejectsShellSyntax(t *testing.T) {
	valid := "API_KEY=secret\nEMPTY=\n# comment\n"
	if err := ValidateEnvironmentContent(valid); err != nil {
		t.Fatalf("expected valid env content, got %v", err)
	}

	tests := []string{
		"export API_KEY=secret\n",
		"API KEY=secret\n",
		"API_KEY =secret\n",
		"API_KEY='secret'\n",
		"API_KEY=secret # comment\n",
		"not-a-pair\n",
	}
	for _, content := range tests {
		t.Run(strings.TrimSpace(content), func(t *testing.T) {
			if err := ValidateEnvironmentContent(content); err == nil {
				t.Fatal("expected env content to be rejected")
			}
		})
	}
}

func TestValidateUnitSourceRequiresDeployTmpAndAppUser(t *testing.T) {
	root := t.TempDir()
	deployTmp := filepath.Join(root, "deploy-tmp")
	t.Setenv("SIMPLE_VPS_DEPLOY_TMP_DIR", deployTmp)
	if err := os.MkdirAll(deployTmp, 0755); err != nil {
		t.Fatal(err)
	}

	unitPath := filepath.Join(deployTmp, "simple-my-app-web.service")
	unit := strings.Join([]string{
		"[Unit]",
		"Description=test",
		"[Service]",
		"User=app-my-app",
		"Group=app-my-app",
		"ExecStart=/usr/bin/env bash -c 'exec bun run start'",
		"",
	}, "\n")
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		t.Fatal(err)
	}

	resolved, err := ValidateUnitSource(unitPath, "my-app", "web")
	if err != nil {
		t.Fatal(err)
	}
	wantResolved, err := filepath.EvalSymlinks(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != wantResolved {
		t.Fatalf("unexpected unit source: %s", resolved)
	}

	wrongUserPath := filepath.Join(deployTmp, "simple-my-app-api.service")
	if err := os.WriteFile(wrongUserPath, []byte("[Unit]\n[Service]\nUser=root\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateUnitSource(wrongUserPath, "my-app", "api"); err == nil || !strings.Contains(err.Error(), "unexpected User=") {
		t.Fatalf("expected wrong user rejection, got %v", err)
	}

	outsidePath := filepath.Join(root, "simple-my-app-web.service")
	if err := os.WriteFile(outsidePath, []byte(unit), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateUnitSource(outsidePath, "my-app", "web"); err == nil || !strings.Contains(err.Error(), "source file must live under") {
		t.Fatalf("expected deploy tmp rejection, got %v", err)
	}
}

func TestAppUninstallUnitRemovesSystemAndAppCopies(t *testing.T) {
	root := t.TempDir()
	appRoot := filepath.Join(root, "apps")
	unitDir := filepath.Join(root, "systemd")
	t.Setenv("SIMPLE_VPS_APP_ROOT", appRoot)
	t.Setenv("SIMPLE_VPS_SYSTEMD_UNIT_DIR", unitDir)

	appUnit := filepath.Join(appRoot, "my-app", "systemd", "simple-my-app-web.service")
	systemUnit := filepath.Join(unitDir, "simple-my-app-web.service")
	for _, path := range []string{appUnit, systemUnit} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("[Unit]\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := AppUninstallUnit("my-app", "web"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{appUnit, systemUnit} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, got %v", path, err)
		}
	}
}

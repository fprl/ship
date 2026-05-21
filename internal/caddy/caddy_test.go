package caddy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/simple-vps/internal/store"
)

func TestRenderRoutesCaddyfileIncludesTypedRoutes(t *testing.T) {
	port := 3000
	content, err := RenderRoutesCaddyfile(&store.RoutesFile{
		Version: store.CurrentVersion,
		Routes: []store.Route{
			{
				Host: "api.example.com",
				Type: "proxy",
				Port: &port,
			},
			{
				Host:    "static.example.com",
				Type:    "static",
				Root:    "/var/apps/site/current",
				Headers: map[string]string{"Cache-Control": "public, max-age=60"},
			},
			{
				Host: "old.example.com",
				Type: "redirect",
				To:   "https://new.example.com{uri}",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"# Source: /etc/simple-vps/routes.json",
		"http://:8080 {",
		"bind 127.0.0.1",
		"@route_0 host api.example.com",
		"reverse_proxy 127.0.0.1:3000",
		"@route_1 host static.example.com",
		"root * \"/var/apps/site/current\"",
		"header Cache-Control \"public, max-age=60\"",
		"file_server",
		"@route_2 host old.example.com",
		"redir \"https://new.example.com{uri}\" permanent",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered Caddyfile missing %q:\n%s", want, content)
		}
	}
}

func TestApplyCaddyfileWritesManagedFilesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SIMPLE_VPS_STATE_DIR", root)
	t.Setenv("SIMPLE_VPS_CADDYFILE_PATH", filepath.Join(root, "Caddyfile"))
	t.Setenv("SIMPLE_VPS_MANAGED_CADDY_DIR", filepath.Join(root, "simple-vps"))
	t.Setenv("SIMPLE_VPS_USER_CADDY_DIR", filepath.Join(root, "conf.d"))
	t.Setenv("SIMPLE_VPS_BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("SIMPLE_VPS_CADDY_BIN", "true")
	t.Setenv("SIMPLE_VPS_SYSTEMCTL_BIN", "true")

	changed, err := ApplyCaddyfile(&store.RoutesFile{Version: store.CurrentVersion}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected first apply to write files")
	}

	caddyfile, err := os.ReadFile(filepath.Join(root, "Caddyfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !ManagedHashIsValid(string(caddyfile)) {
		t.Fatalf("expected managed Caddyfile hash to be valid:\n%s", string(caddyfile))
	}

	routesFile, err := os.ReadFile(filepath.Join(root, "simple-vps", "routes.caddy"))
	if err != nil {
		t.Fatal(err)
	}
	if !ManagedHashIsValid(string(routesFile)) {
		t.Fatalf("expected managed routes hash to be valid:\n%s", string(routesFile))
	}

	changed, err = ApplyCaddyfile(&store.RoutesFile{Version: store.CurrentVersion}, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected second apply to be idempotent")
	}
}

func TestApplyCaddyfileRejectsManualEditInManagedRoutes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SIMPLE_VPS_STATE_DIR", root)
	t.Setenv("SIMPLE_VPS_CADDYFILE_PATH", filepath.Join(root, "Caddyfile"))
	t.Setenv("SIMPLE_VPS_MANAGED_CADDY_DIR", filepath.Join(root, "simple-vps"))
	t.Setenv("SIMPLE_VPS_USER_CADDY_DIR", filepath.Join(root, "conf.d"))
	t.Setenv("SIMPLE_VPS_BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("SIMPLE_VPS_CADDY_BIN", "true")
	t.Setenv("SIMPLE_VPS_SYSTEMCTL_BIN", "true")

	if _, err := ApplyCaddyfile(&store.RoutesFile{Version: store.CurrentVersion}, false); err != nil {
		t.Fatal(err)
	}

	routesPath := filepath.Join(root, "simple-vps", "routes.caddy")
	f, err := os.OpenFile(routesPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("# manual edit\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	port := 3000
	_, err = ApplyCaddyfile(&store.RoutesFile{
		Version: store.CurrentVersion,
		Routes: []store.Route{{
			Host: "api.example.com",
			Type: "proxy",
			Port: &port,
		}},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "manual edits detected") {
		t.Fatalf("expected manual edit rejection, got %v", err)
	}
}

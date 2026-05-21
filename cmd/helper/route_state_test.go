package helper

import (
	"path/filepath"
	"testing"

	"github.com/fprl/simple-vps/internal/store"
)

func TestUpsertRouteWritesADRRoutesFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SIMPLE_VPS_STATE_DIR", root)
	t.Setenv("SIMPLE_VPS_CADDYFILE_PATH", filepath.Join(root, "Caddyfile"))
	t.Setenv("SIMPLE_VPS_MANAGED_CADDY_DIR", filepath.Join(root, "simple-vps"))
	t.Setenv("SIMPLE_VPS_USER_CADDY_DIR", filepath.Join(root, "conf.d"))
	t.Setenv("SIMPLE_VPS_BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("SIMPLE_VPS_CADDY_BIN", "true")
	t.Setenv("SIMPLE_VPS_SYSTEMCTL_BIN", "true")

	port := 3000
	route, err := store.NormalizeRoute(store.Route{
		Host:    "API.example.com.",
		Type:    "proxy",
		App:     "api",
		Service: "web",
		Port:    &port,
	})
	if err != nil {
		t.Fatal(err)
	}

	upsertRoute(route, false)

	stateStore := store.Default()
	loaded, err := stateStore.ReadRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Routes) != 1 {
		t.Fatalf("expected one route, got %d", len(loaded.Routes))
	}
	if loaded.Routes[0].Host != "api.example.com" || loaded.Routes[0].Port == nil || *loaded.Routes[0].Port != 3000 {
		t.Fatalf("unexpected route: %+v", loaded.Routes[0])
	}
	if loaded.Routes[0].Service != "web" {
		t.Fatalf("expected service to be persisted, got route: %+v", loaded.Routes[0])
	}
}

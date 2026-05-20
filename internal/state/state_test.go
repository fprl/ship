package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStateMigratesLegacyProxyRoutes(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("SIMPLE_VPS_STATE_PATH", statePath)

	raw := map[string]any{
		"version": 1,
		"routes": []map[string]any{
			{"host": "Example.com", "port": 3000},
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 2 {
		t.Fatalf("expected version 2, got %d", loaded.Version)
	}
	if len(loaded.Routes) != 1 {
		t.Fatalf("expected one route, got %d", len(loaded.Routes))
	}
	route := loaded.Routes[0]
	if route.Host != "example.com" || route.Type != "proxy" || route.Port == nil || *route.Port != 3000 {
		t.Fatalf("unexpected route: %+v", route)
	}
}

func TestNormalizeRootWithAppMustStayUnderAppRoot(t *testing.T) {
	appRoot := filepath.Join(t.TempDir(), "apps")
	t.Setenv("SIMPLE_VPS_APP_ROOT", appRoot)

	root, err := NormalizeRoot(filepath.Join(appRoot, "data-feed", "current", "public"), "data-feed")
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(appRoot, "data-feed", "current", "public") {
		t.Fatalf("unexpected root: %s", root)
	}

	if _, err := NormalizeRoot(filepath.Join(appRoot, "data-feed-other", "public"), "data-feed"); err == nil {
		t.Fatal("expected sibling app root to be rejected")
	}
}

func TestCloudflareStateDefaultPathMatchesServerContract(t *testing.T) {
	t.Setenv("SIMPLE_VPS_CLOUDFLARE_STATE_PATH", "")
	if got := CloudflareStatePath(); got != "/etc/simple-vps/cloudflare.json" {
		t.Fatalf("unexpected Cloudflare state path: %s", got)
	}
}

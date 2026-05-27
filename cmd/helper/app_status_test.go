package helper

import (
	"strings"
	"testing"
)

func TestContainersToServicesFiltersUnlabelledAndSorts(t *testing.T) {
	// The fake `podman ps` filter accepts containers we don't own —
	// the helper relies on the `service` label to know what's
	// actually a managed simple-vps service. Anything without it
	// gets dropped.
	got := containersToServices([]containerEntry{
		{
			Names: []string{"app-api-production-worker"},
			State: "running", Status: "Up 4 minutes",
			Image: "simple-vps/api-production:abc1234",
			Labels: map[string]string{"app": "api", "env": "production", "service": "worker", "simple_vps_release": "abc1234"},
		},
		{
			Names: []string{"random-thing"},
			State: "running",
			Labels: map[string]string{"app": "api", "env": "production"}, // no `service`
		},
		{
			Names: []string{"app-api-production-web"},
			State: "running", Status: "Up 4 minutes",
			Image: "simple-vps/api-production:abc1234",
			Labels: map[string]string{"app": "api", "env": "production", "service": "web", "simple_vps_release": "abc1234"},
		},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 services, got %d: %+v", len(got), got)
	}
	// Sorted by service name.
	if got[0].Service != "web" || got[1].Service != "worker" {
		t.Fatalf("expected [web, worker] sorted, got [%s, %s]", got[0].Service, got[1].Service)
	}
	if got[0].Container != "app-api-production-web" || got[0].Release != "abc1234" {
		t.Fatalf("first service mapped wrong: %+v", got[0])
	}
}

func TestRenderStatusTextEmpty(t *testing.T) {
	out := renderStatusText("api", "production", nil)
	if !strings.Contains(out, "api (production)") {
		t.Fatalf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "no services running") {
		t.Fatalf("missing empty-state hint:\n%s", out)
	}
	if !strings.Contains(out, "simple-vps deploy production") {
		t.Fatalf("empty-state hint should point at deploy:\n%s", out)
	}
}

func TestRenderStatusTextWithServices(t *testing.T) {
	services := []serviceStatus{
		{Service: "web", Container: "app-api-production-web", State: "running", Status: "Up 4 minutes", Release: "abc1234"},
		{Service: "worker", Container: "app-api-production-worker", State: "exited", Status: "Exited (1) 2 minutes ago", Release: "abc1234"},
	}
	out := renderStatusText("api", "production", services)
	if !strings.Contains(out, "api (production)") {
		t.Fatalf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "running (Up 4 minutes)") || !strings.Contains(out, "release=abc1234") {
		t.Fatalf("missing web service row:\n%s", out)
	}
	if !strings.Contains(out, "worker") || !strings.Contains(out, "exited (Exited (1) 2 minutes ago)") {
		t.Fatalf("missing worker service row:\n%s", out)
	}
}

func TestRenderStatusTextHandlesMissingReleaseLabel(t *testing.T) {
	// Older containers from before the `simple_vps_release` label
	// existed shouldn't crash the formatter.
	services := []serviceStatus{
		{Service: "web", Container: "x", State: "running"},
	}
	out := renderStatusText("api", "production", services)
	if !strings.Contains(out, "release=?") {
		t.Fatalf("expected `release=?` fallback for missing label:\n%s", out)
	}
}

package helper

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/identity"
)

func TestContainersToProcessesFiltersUnlabelledAndSorts(t *testing.T) {
	// The fake `podman ps` filter accepts containers we don't own.
	// The helper relies on the `ship.process` label to know
	// what's actually a managed ship process.
	got := containersToProcesses([]containerEntry{
		{
			Names: []string{"ship-a8f9b2-worker-abc1234"},
			State: "running", Status: "Up 4 minutes",
			Image:  "ship/ship-a8f9b2:abc1234",
			Labels: map[string]string{"ship.app": "api", "ship.env": "production", "ship.process": "worker", "ship.release": "abc1234"},
		},
		{
			Names:  []string{"random-thing"},
			State:  "running",
			Labels: map[string]string{"ship.app": "api", "ship.env": "production"},
		},
		{
			Names: []string{"ship-a8f9b2-web-abc1234"},
			State: "running", Status: "Up 4 minutes",
			Image:  "ship/ship-a8f9b2:abc1234",
			Labels: map[string]string{"ship.app": "api", "ship.env": "production", "ship.process": "web", "ship.release": "abc1234"},
		},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 processes, got %d: %+v", len(got), got)
	}
	// Sorted by process name.
	if got[0].Process != "web" || got[1].Process != "worker" {
		t.Fatalf("expected [web, worker] sorted, got [%s, %s]", got[0].Process, got[1].Process)
	}
	if got[0].Container != "ship-a8f9b2-web-abc1234" || got[0].Release != "abc1234" {
		t.Fatalf("first process mapped wrong: %+v", got[0])
	}
}

func TestContainersToAppEnvsGroupsAndSorts(t *testing.T) {
	got := containersToAppEnvs([]containerEntry{
		{
			Names:  []string{"ship-api-staging-web"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"ship.app": "api", "ship.env": "staging", "ship.process": "web", "ship.infra_id": identity.InfraID("api", "staging")},
		},
		{
			Names:  []string{"ship-api-production-worker"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"ship.app": "api", "ship.env": "production", "ship.process": "worker", "ship.infra_id": identity.InfraID("api", "production")},
		},
		{
			Names:  []string{"ship-api-production-web"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"ship.app": "api", "ship.env": "production", "ship.process": "web", "ship.infra_id": identity.InfraID("api", "production")},
		},
		{
			Names:  []string{"ship-blog-production-web"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"ship.app": "blog", "ship.env": "production", "ship.process": "web", "ship.infra_id": identity.InfraID("blog", "production")},
		},
		{
			Names:  []string{"not-ours"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"ship.app": "api", "ship.env": "production"},
		},
		{
			Names:  []string{"wrong-infra"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"ship.app": "api", "ship.env": "production", "ship.process": "web", "ship.infra_id": "ship-other"},
		},
	})

	if len(got) != 3 {
		t.Fatalf("expected 3 app envs, got %d: %+v", len(got), got)
	}
	if got[0].App != "api" || got[0].Env != "production" {
		t.Fatalf("expected api production first, got %+v", got[0])
	}
	if got[1].App != "api" || got[1].Env != "staging" {
		t.Fatalf("expected api staging second, got %+v", got[1])
	}
	if got[2].App != "blog" || got[2].Env != "production" {
		t.Fatalf("expected blog production third, got %+v", got[2])
	}
	if len(got[0].Processes) != 2 || got[0].Processes[0].Process != "web" || got[0].Processes[1].Process != "worker" {
		t.Fatalf("expected api production processes sorted by name, got %+v", got[0].Processes)
	}
}

func TestRenderStatusTextEmpty(t *testing.T) {
	out := renderStatusText("api", "production", nil, false, nil, nil)
	if !strings.Contains(out, "api (production)") {
		t.Fatalf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "no processes running") {
		t.Fatalf("missing empty-state hint:\n%s", out)
	}
	if !strings.Contains(out, "run `ship`") {
		t.Fatalf("empty-state hint should point at ship:\n%s", out)
	}
}

func TestAppStatusJSONArrayFieldsAreNonNilWhenEmpty(t *testing.T) {
	payload := statusPayload{App: "api", Env: "production", Processes: []processStatus{}}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"processes":[]`) {
		t.Fatalf("empty processes JSON = %s", data)
	}
	static := &staticStatus{Release: "abc", Routes: []string{}}
	data, err = json.Marshal(statusPayload{App: "api", Env: "production", Processes: []processStatus{}, Static: static})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"routes":[]`) {
		t.Fatalf("empty routes JSON = %s", data)
	}
}

func TestRenderStatusTextKnownEnvWithoutProcesses(t *testing.T) {
	out := renderStatusText("site", "production", nil, true, nil, nil)
	if !strings.Contains(out, "no processes running") {
		t.Fatalf("missing empty process state:\n%s", out)
	}
	if strings.Contains(out, "run `ship`") {
		t.Fatalf("known env should not print ship hint:\n%s", out)
	}
}

func TestRenderStatusTextWithProcesses(t *testing.T) {
	processes := []processStatus{
		{Process: "web", Container: "ship-a8f9b2-web-abc1234", State: "running", Status: "Up 4 minutes", Release: "abc1234"},
		{Process: "worker", Container: "ship-a8f9b2-worker-abc1234", State: "exited", Status: "Exited (1) 2 minutes ago", Release: "abc1234"},
	}
	out := renderStatusText("api", "production", processes, true, &statusRelease{Release: "abc1234", Source: "process"}, nil)
	if !strings.Contains(out, "api (production)") {
		t.Fatalf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "running (Up 4 minutes)") || !strings.Contains(out, "release=abc1234") {
		t.Fatalf("missing web process row:\n%s", out)
	}
	if !strings.Contains(out, "worker") || !strings.Contains(out, "exited (Exited (1) 2 minutes ago)") {
		t.Fatalf("missing worker process row:\n%s", out)
	}
}

func TestRenderAppListTextEmpty(t *testing.T) {
	out := renderAppListText(appListPayload{})
	if strings.TrimSpace(out) != "no apps found" {
		t.Fatalf("unexpected empty app list text:\n%s", out)
	}
}

func TestRenderAppListTextWithApps(t *testing.T) {
	payload := appListPayload{Apps: []appListAppStatus{
		{
			App: "api",
			Envs: []appListEnvStatus{
				{
					Class:          "production",
					Branch:         "main",
					URL:            "https://api.example.com",
					CurrentRelease: "abc1234",
					Health:         "healthy",
					Processes:      []processStatus{{Process: "web", State: "running", Status: "Up 4 minutes", Release: "abc1234"}},
				},
			},
		},
	}}
	out := renderAppListText(payload)
	for _, want := range []string{"APP", "api", "Production", "main", "https://api.example.com", "abc1234", "healthy"} {
		if !strings.Contains(out, want) {
			t.Fatalf("app list table missing %q:\n%s", want, out)
		}
	}
}

func TestAppListFromStatusesSummarizesProductionAndPreview(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Hour)
	payload := appListFromStatuses([]appEnvStatus{
		{
			App: "api",
			Env: productionEnvName,
			ShippedBy: &deployIdentity{
				SSHKeyComment: "fake-vps-smoke",
				GitAuthor:     "Smoke <smoke@example.com>",
			},
			Processes: []processStatus{
				{Process: "web", State: "running", Release: "abc1234", CreatedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)},
			},
		},
		{
			App: "api",
			Env: "feat-x-a1b2",
			Preview: &identity.PreviewIdentity{
				Branch:          "feat/x",
				SanitizedBranch: "feat-x",
				Env:             "feat-x-a1b2",
				Suffix:          "a1b2",
				LastShipAt:      now.Add(-time.Minute),
				ExpiresAt:       &expires,
				Pinned:          false,
			},
			Processes: []processStatus{
				{Process: "web", State: "exited", Release: "def5678", CreatedAt: now.Add(-2 * time.Minute).Format(time.RFC3339Nano)},
			},
		},
	}, now)
	if len(payload.Apps) != 1 || len(payload.Apps[0].Envs) != 2 {
		t.Fatalf("unexpected app list payload: %+v", payload)
	}
	prod := payload.Apps[0].Envs[0]
	if prod.Class != "production" || prod.Branch != "main" || prod.Env != productionEnvName || prod.CurrentRelease != "abc1234" || prod.Health != "healthy" || prod.AgeSeconds != 60 || prod.ShippedBy == nil {
		t.Fatalf("bad production summary: %+v", prod)
	}
	preview := payload.Apps[0].Envs[1]
	if preview.Class != "preview" || preview.Branch != "feat/x" || preview.Env != "feat-x-a1b2" || preview.CurrentRelease != "def5678" || preview.Health != "degraded" || preview.AgeSeconds != 120 || preview.ExpiresAt == "" || preview.Pinned {
		t.Fatalf("bad preview summary: %+v", preview)
	}
}

func TestRenderStatusTextMarksDirtyReleaseAndStatic(t *testing.T) {
	release := &statusRelease{
		Release:    "abc1234-dirty-20260530t143012000000000z",
		Source:     "mixed",
		Dirty:      true,
		BaseCommit: "abc1234abc1234abc1234abc1234abc1234abc1234",
	}
	static := &staticStatus{
		Release: "abc1234-dirty-20260530t143012000000000z",
		Routes:  []string{"docs"},
		Dirty:   true,
	}
	processes := []processStatus{
		{Process: "web", State: "running", Release: "abc1234-dirty-20260530t143012000000000z", Dirty: true},
	}
	out := renderStatusText("api", "production", processes, true, release, static)
	if !strings.Contains(out, "release: abc1234-dirty-20260530t143012000000000z (dirty, base abc1234abc12)") {
		t.Fatalf("missing dirty release summary:\n%s", out)
	}
	if !strings.Contains(out, "static") || !strings.Contains(out, "routes=docs") {
		t.Fatalf("missing static row:\n%s", out)
	}
}

func TestActiveStatusReleaseUsesRunningProcessesOnly(t *testing.T) {
	processes := []processStatus{
		{Process: "web", State: "running", Release: "new1234"},
		{Process: "web", State: "exited", Release: "old1234"},
	}
	release := activeStatusRelease(runningProcesses(processes), nil)
	if release == nil {
		t.Fatal("expected active release")
	}
	if release.Mixed || release.Release != "new1234" {
		t.Fatalf("expected running release only, got %+v", release)
	}
}

func TestMergeAppEnvsIncludesStaticOnlyIdentity(t *testing.T) {
	got := mergeAppEnvs(
		[]appEnvStatus{
			{App: "site", Env: "production"},
			{App: "api", Env: "staging"},
		},
		[]appEnvStatus{
			{
				App: "api",
				Env: "production",
				Processes: []processStatus{
					{Process: "web", State: "running"},
				},
			},
		},
	)
	if len(got) != 3 {
		t.Fatalf("expected three app envs, got %+v", got)
	}
	if got[0].App != "api" || got[0].Env != "production" || len(got[0].Processes) != 1 {
		t.Fatalf("expected process app first, got %+v", got)
	}
	if got[2].App != "site" || got[2].Env != "production" || len(got[2].Processes) != 0 {
		t.Fatalf("expected static-only identity retained, got %+v", got)
	}
}

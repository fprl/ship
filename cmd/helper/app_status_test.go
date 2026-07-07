package helper

import (
	"strings"
	"testing"

	"github.com/fprl/simple-vps/internal/identity"
)

func TestContainersToProcessesFiltersUnlabelledAndSorts(t *testing.T) {
	// The fake `podman ps` filter accepts containers we don't own.
	// The helper relies on the `simple-vps.process` label to know
	// what's actually a managed simple-vps process.
	got := containersToProcesses([]containerEntry{
		{
			Names: []string{"svps-a8f9b2-worker-abc1234"},
			State: "running", Status: "Up 4 minutes",
			Image:  "simple-vps/svps-a8f9b2:abc1234",
			Labels: map[string]string{"simple-vps.app": "api", "simple-vps.env": "production", "simple-vps.process": "worker", "simple-vps.release": "abc1234"},
		},
		{
			Names:  []string{"random-thing"},
			State:  "running",
			Labels: map[string]string{"simple-vps.app": "api", "simple-vps.env": "production"},
		},
		{
			Names: []string{"svps-a8f9b2-web-abc1234"},
			State: "running", Status: "Up 4 minutes",
			Image:  "simple-vps/svps-a8f9b2:abc1234",
			Labels: map[string]string{"simple-vps.app": "api", "simple-vps.env": "production", "simple-vps.process": "web", "simple-vps.release": "abc1234"},
		},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 processes, got %d: %+v", len(got), got)
	}
	// Sorted by process name.
	if got[0].Process != "web" || got[1].Process != "worker" {
		t.Fatalf("expected [web, worker] sorted, got [%s, %s]", got[0].Process, got[1].Process)
	}
	if got[0].Container != "svps-a8f9b2-web-abc1234" || got[0].Release != "abc1234" {
		t.Fatalf("first process mapped wrong: %+v", got[0])
	}
}

func TestContainersToAppEnvsGroupsAndSorts(t *testing.T) {
	got := containersToAppEnvs([]containerEntry{
		{
			Names:  []string{"svps-api-staging-web"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"simple-vps.app": "api", "simple-vps.env": "staging", "simple-vps.process": "web", "simple-vps.infra_id": identity.InfraID("api", "staging")},
		},
		{
			Names:  []string{"svps-api-production-worker"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"simple-vps.app": "api", "simple-vps.env": "production", "simple-vps.process": "worker", "simple-vps.infra_id": identity.InfraID("api", "production")},
		},
		{
			Names:  []string{"svps-api-production-web"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"simple-vps.app": "api", "simple-vps.env": "production", "simple-vps.process": "web", "simple-vps.infra_id": identity.InfraID("api", "production")},
		},
		{
			Names:  []string{"svps-blog-production-web"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"simple-vps.app": "blog", "simple-vps.env": "production", "simple-vps.process": "web", "simple-vps.infra_id": identity.InfraID("blog", "production")},
		},
		{
			Names:  []string{"not-ours"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"simple-vps.app": "api", "simple-vps.env": "production"},
		},
		{
			Names:  []string{"wrong-infra"},
			State:  "running",
			Status: "Up",
			Labels: map[string]string{"simple-vps.app": "api", "simple-vps.env": "production", "simple-vps.process": "web", "simple-vps.infra_id": "svps-other"},
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
		{Process: "web", Container: "svps-a8f9b2-web-abc1234", State: "running", Status: "Up 4 minutes", Release: "abc1234"},
		{Process: "worker", Container: "svps-a8f9b2-worker-abc1234", State: "exited", Status: "Exited (1) 2 minutes ago", Release: "abc1234"},
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
	out := renderAppListText(nil)
	if strings.TrimSpace(out) != "no apps found" {
		t.Fatalf("unexpected empty app list text:\n%s", out)
	}
}

func TestRenderAppListTextWithApps(t *testing.T) {
	apps := []appEnvStatus{
		{
			App: "api",
			Env: "production",
			Processes: []processStatus{
				{Process: "web", State: "running", Status: "Up 4 minutes", Release: "abc1234"},
			},
		},
	}
	out := renderAppListText(apps)
	if !strings.Contains(out, "api (production)") {
		t.Fatalf("missing app header:\n%s", out)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "running (Up 4 minutes)") || !strings.Contains(out, "release=abc1234") {
		t.Fatalf("missing process row:\n%s", out)
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

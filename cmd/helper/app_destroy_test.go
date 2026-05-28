package helper

import (
	"strings"
	"testing"
)

func TestDestroyContainerNamesUsesLabelledServices(t *testing.T) {
	services := []serviceStatus{
		{Service: "web", Container: "app-api-production-web"},
		{Service: "worker", Container: "app-api-production-worker"},
		{Service: "broken"},
	}

	got := destroyContainerNames(services)
	want := []string{"app-api-production-web", "app-api-production-worker"}
	if len(got) != len(want) {
		t.Fatalf("unexpected names:\nwant: %#v\n got: %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected names:\nwant: %#v\n got: %#v", want, got)
		}
	}
}

func TestRenderDestroyText(t *testing.T) {
	out := renderDestroyText("api", "production", destroySummary{
		Containers:    []string{"app-api-production-web"},
		CaddyFragment: true,
		SecretsPurged: true,
	})

	for _, want := range []string{
		"Destroyed api (production)",
		"containers: 1 removed",
		"route: removed",
		"secrets: purged",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("destroy summary missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDestroyTextEmpty(t *testing.T) {
	out := renderDestroyText("api", "staging", destroySummary{})

	for _, want := range []string{
		"Destroyed api (staging)",
		"containers: none",
		"route: none",
		"secrets: kept",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("destroy summary missing %q:\n%s", want, out)
		}
	}
}

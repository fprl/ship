package helper

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/identity"
)

func TestAppPreflightReportJSONUsesIssuesOnly(t *testing.T) {
	report := appPreflightReport{
		App:     "api",
		Env:     "production",
		Healthy: false,
		Issues:  []appPreflightIssue{{Code: "env_missing", Message: "app env is not prepared"}},
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"findings"`) {
		t.Fatalf("preflight payload must not duplicate issues as findings: %s", raw)
	}
	if !strings.Contains(string(raw), `"issues":[{`) {
		t.Fatalf("preflight payload is missing issues: %s", raw)
	}
}

func TestRunningContainerExistsRequiresRunningState(t *testing.T) {
	entries := []containerEntry{
		{Names: []string{"caddy"}, State: "exited"},
		{Names: []string{"other"}, State: "running"},
	}
	if runningContainerExists(entries, "caddy") {
		t.Fatal("stopped caddy container should not satisfy preflight")
	}
	entries = append(entries, containerEntry{Names: []string{"caddy"}, State: "running"})
	if !runningContainerExists(entries, "caddy") {
		t.Fatal("running caddy container should satisfy preflight")
	}
}

func TestValidateEnvIdentityData(t *testing.T) {
	valid := []byte(`{"version":1,"app":"api","env":"production","infra_id":"` + identity.InfraID("api", "production") + `"}`)
	if err := validateEnvIdentityData("api", "production", valid); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}

	invalid := []byte(`{"version":1,"app":"api","env":"staging","infra_id":"` + identity.InfraID("api", "staging") + `"}`)
	err := validateEnvIdentityData("api", "production", invalid)
	if err == nil || !strings.Contains(err.Error(), "expected app=api env=production") {
		t.Fatalf("expected identity mismatch, got %v", err)
	}
}

package helper

import (
	"strings"
	"testing"

	"github.com/fprl/ship/internal/identity"
)

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

package helper

import "testing"

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

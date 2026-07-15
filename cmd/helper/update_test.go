package helper

import (
	"os"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/store"
)

func TestValidateUpdateTargetRefusesEqualDowngradeAndDevelopmentBuilds(t *testing.T) {
	for _, tt := range []struct {
		name      string
		installed string
		target    string
	}{
		{name: "equal", installed: "v0.4.1", target: "v0.4.1"},
		{name: "downgrade", installed: "v0.4.1", target: "v0.4.0"},
		{name: "development target", installed: "v0.4.0", target: "v0.4.1-3-gabcdef"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateUpdateTarget(tt.installed, tt.target); err == nil {
				t.Fatalf("validateUpdateTarget(%q, %q) unexpectedly succeeded", tt.installed, tt.target)
			}
		})
	}
	if err := validateUpdateTarget("v0.4.0", "v0.4.1"); err != nil {
		t.Fatalf("newer released target rejected: %v", err)
	}
}

func TestRunVerifiedUpdateJournalsStartedBeforeMutation(t *testing.T) {
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	previous := runVerifiedUpdateLocal
	runVerifiedUpdateLocal = func(binary string) error {
		data, err := os.ReadFile(store.Default().UpdatesJournalPath())
		if err != nil {
			t.Fatalf("mutation ran before update journal was written: %v", err)
		}
		if !strings.Contains(string(data), `"event":"started"`) || !strings.Contains(string(data), `"version":"v0.4.1"`) {
			t.Fatalf("journal before mutation = %s", data)
		}
		return nil
	}
	t.Cleanup(func() { runVerifiedUpdateLocal = previous })

	if err := runVerifiedUpdate("v0.4.1", func() error {
		return runVerifiedUpdateLocal("/tmp/verified-helper")
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorFlagsStartedUpdateWithoutCompletion(t *testing.T) {
	root := t.TempDir()
	stateStore := store.Store{Root: root}
	if err := appendUpdateJournalForStore(stateStore, updateJournalEntry{Event: "started", Version: "v0.4.1"}); err != nil {
		t.Fatal(err)
	}
	check := doctorBoxUpdateCheck(stateStore, "fake-vps")
	if check.Status != doctorStatusDegraded || !strings.Contains(check.Evidence, "incomplete update started for v0.4.1") {
		t.Fatalf("unexpected partial update check: %+v", check)
	}
	if check.Remediation != "ship box update fake-vps" {
		t.Fatalf("remediation = %q", check.Remediation)
	}
}

func TestDoctorPreservesIncompleteUpdateDetectionWithTornTail(t *testing.T) {
	root := t.TempDir()
	stateStore := store.Store{Root: root}
	if err := appendUpdateJournalForStore(stateStore, updateJournalEntry{Event: "started", Version: "v0.4.1"}); err != nil {
		t.Fatal(err)
	}
	path := stateStore.UpdatesJournalPath()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"event":"completed"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	check := doctorBoxUpdateCheck(stateStore, "fake-vps")
	if check.Status != doctorStatusDegraded || !strings.Contains(check.Evidence, "incomplete update started for v0.4.1") {
		t.Fatalf("unexpected partial update check: %+v", check)
	}
}

func appendUpdateJournalForStore(stateStore store.Store, entry updateJournalEntry) error {
	previous := os.Getenv("SHIP_STATE_DIR")
	if err := os.Setenv("SHIP_STATE_DIR", stateStore.Root); err != nil {
		return err
	}
	defer os.Setenv("SHIP_STATE_DIR", previous)
	return appendUpdateJournal(entry)
}

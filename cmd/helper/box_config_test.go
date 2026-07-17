package helper

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/store"
)

func TestBoxConfigSetUnsetAndJournal(t *testing.T) {
	setTestStateRoot(t, t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")

	fresh, err := readBoxConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.Config["webhook.url"]; got.Value != "" || got.Default != "" || got.Source != "default" {
		t.Fatalf("fresh webhook.url = %+v", got)
	}

	const url = "https://ntfy.example/ship"
	if err := setBoxConfig("webhook.url", url, "set box config webhook.url"); err != nil {
		t.Fatal(err)
	}
	if got, err := boxConfigValueFor("webhook.url"); err != nil || got != url {
		t.Fatalf("webhook.url = %q, %v", got, err)
	}
	if err := unsetBoxConfig("webhook.url", "unset box config webhook.url"); err != nil {
		t.Fatal(err)
	}
	after, err := readBoxConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Config["webhook.url"]; got.Value != "" || got.Source != "default" {
		t.Fatalf("unset webhook.url = %+v", got)
	}

	data, err := os.ReadFile(store.Default().UpdatesJournalPath())
	if err != nil {
		t.Fatal(err)
	}
	var entries []updateJournalEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry updateJournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if len(entries) != 2 || entries[0].Event != "config_set" || entries[1].Event != "config_unset" || entries[0].Key != "webhook.url" || entries[0].Actor == nil || entries[0].Actor.Role != "owner" {
		t.Fatalf("config journal = %+v", entries)
	}
}

func TestBoxConfigValidationErrorsAreCoded(t *testing.T) {
	setTestStateRoot(t, t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")
	setHelperBoxClientAddress(t, "203.0.113.7")
	if err := setBoxConfig("unknown.key", "value", "set box config unknown.key"); !errcat.Is(err, errcat.CodeBoxConfigKeyUnknown) {
		t.Fatalf("unknown key error = %v", err)
	} else if coded, _ := errcat.As(err); coded.Remediation() != "ship box config 203.0.113.7" {
		t.Fatalf("unknown key remediation = %q", coded.Remediation())
	}
	if err := setBoxConfig("webhook.url", "not a URL", "set box config webhook.url"); !errcat.Is(err, errcat.CodeBoxConfigValueInvalid) {
		t.Fatalf("invalid value error = %v", err)
	} else if coded, _ := errcat.As(err); coded.Remediation() != "ship box config 203.0.113.7 set webhook.url <value>" {
		t.Fatalf("invalid value remediation = %q", coded.Remediation())
	}
	if err := setBoxConfig("webhook.url", "", "set box config webhook.url"); !errcat.Is(err, errcat.CodeBoxConfigValueInvalid) {
		t.Fatalf("empty value error = %v", err)
	}
}

func TestBoxConfigWriteFailureDoesNotJournal(t *testing.T) {
	stateDir := t.TempDir()
	setTestStateRoot(t, stateDir)
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")
	if err := store.Default().WriteBoxConfig(store.BoxConfigFile{Version: store.CurrentVersion, Values: map[string]string{"webhook.url": "https://ntfy.example/old"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0755) })

	err := setBoxConfig("webhook.url", "https://ntfy.example/new", "set box config webhook.url")
	if err == nil {
		t.Fatal("expected box config write failure")
	}
	if got, err := boxConfigValueFor("webhook.url"); err != nil || got != "https://ntfy.example/old" {
		t.Fatalf("webhook.url after failed write = %q, %v", got, err)
	}
	if _, err := os.Stat(store.Default().UpdatesJournalPath()); !os.IsNotExist(err) {
		t.Fatalf("updates journal should not exist after failed write, stat err = %v", err)
	}
}

func TestBoxConfigSuccessfulWriteAppendsExactlyOneJournalEntry(t *testing.T) {
	setTestStateRoot(t, t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")
	if err := setBoxConfig("webhook.url", "https://ntfy.example/ship", "set box config webhook.url"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(store.Default().UpdatesJournalPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("journal entries = %d, want 1: %s", len(lines), data)
	}
	var entry updateJournalEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Event != "config_set" || entry.Key != "webhook.url" {
		t.Fatalf("journal entry = %+v", entry)
	}
}

func TestBoxConfigJournalFailureDoesNotFailSuccessfulWrite(t *testing.T) {
	setTestStateRoot(t, t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")
	if err := os.MkdirAll(store.Default().UpdatesJournalPath(), 0755); err != nil {
		t.Fatal(err)
	}

	stderr := captureStderr(t, func() {
		if err := setBoxConfig("webhook.url", "https://ntfy.example/ship", "set box config webhook.url"); err != nil {
			t.Fatalf("set box config = %v", err)
		}
	})
	if got, err := boxConfigValueFor("webhook.url"); err != nil || got != "https://ntfy.example/ship" {
		t.Fatalf("webhook.url = %q, %v", got, err)
	}
	if !strings.Contains(stderr, "warning: failed to write update journal:") || !strings.Contains(stderr, "next: ship box doctor") {
		t.Fatalf("journal append warning = %q", stderr)
	}
}

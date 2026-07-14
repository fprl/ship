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
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")

	fresh, err := readBoxConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.Config["notify.url"]; got.Value != "" || got.Default != "" || got.Source != "default" {
		t.Fatalf("fresh notify.url = %+v", got)
	}

	const url = "https://ntfy.example/ship"
	if err := setBoxConfig("notify.url", url, "box config set notify.url"); err != nil {
		t.Fatal(err)
	}
	if got, err := boxConfigValueFor("notify.url"); err != nil || got != url {
		t.Fatalf("notify.url = %q, %v", got, err)
	}
	if err := unsetBoxConfig("notify.url", "box config unset notify.url"); err != nil {
		t.Fatal(err)
	}
	after, err := readBoxConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Config["notify.url"]; got.Value != "" || got.Source != "default" {
		t.Fatalf("unset notify.url = %+v", got)
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
	if len(entries) != 2 || entries[0].Event != "config_set" || entries[1].Event != "config_unset" || entries[0].Key != "notify.url" || entries[0].Actor == nil || entries[0].Actor.Role != "owner" {
		t.Fatalf("config journal = %+v", entries)
	}
}

func TestBoxConfigValidationErrorsAreCoded(t *testing.T) {
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")
	if err := setBoxConfig("unknown.key", "value", "box config set unknown.key"); !errcat.Is(err, errcat.CodeBoxConfigKeyUnknown) {
		t.Fatalf("unknown key error = %v", err)
	}
	if err := setBoxConfig("notify.url", "not a URL", "box config set notify.url"); !errcat.Is(err, errcat.CodeBoxConfigValueInvalid) {
		t.Fatalf("invalid value error = %v", err)
	}
	if err := setBoxConfig("notify.url", "", "box config set notify.url"); !errcat.Is(err, errcat.CodeBoxConfigValueInvalid) {
		t.Fatalf("empty value error = %v", err)
	}
}

func TestBoxConfigWriteFailureDoesNotJournal(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("SHIP_STATE_DIR", stateDir)
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")
	if err := store.Default().WriteBoxConfig(store.BoxConfigFile{Version: store.CurrentVersion, Values: map[string]string{"notify.url": "https://ntfy.example/old"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0755) })

	err := setBoxConfig("notify.url", "https://ntfy.example/new", "box config set notify.url")
	if err == nil {
		t.Fatal("expected box config write failure")
	}
	if got, err := boxConfigValueFor("notify.url"); err != nil || got != "https://ntfy.example/old" {
		t.Fatalf("notify.url after failed write = %q, %v", got, err)
	}
	if _, err := os.Stat(store.Default().UpdatesJournalPath()); !os.IsNotExist(err) {
		t.Fatalf("updates journal should not exist after failed write, stat err = %v", err)
	}
}

func TestBoxConfigSuccessfulWriteAppendsExactlyOneJournalEntry(t *testing.T) {
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")
	if err := setBoxConfig("notify.url", "https://ntfy.example/ship", "box config set notify.url"); err != nil {
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
	if entry.Event != "config_set" || entry.Key != "notify.url" {
		t.Fatalf("journal entry = %+v", entry)
	}
}

func TestBoxConfigJournalFailureDoesNotFailSuccessfulWrite(t *testing.T) {
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	setServerMemberFingerprint("")
	if err := os.MkdirAll(store.Default().UpdatesJournalPath(), 0755); err != nil {
		t.Fatal(err)
	}

	stderr := captureStderr(t, func() {
		if err := setBoxConfig("notify.url", "https://ntfy.example/ship", "box config set notify.url"); err != nil {
			t.Fatalf("set box config = %v", err)
		}
	})
	if got, err := boxConfigValueFor("notify.url"); err != nil || got != "https://ntfy.example/ship" {
		t.Fatalf("notify.url = %q, %v", got, err)
	}
	if !strings.Contains(stderr, "warning: failed to write update journal:") || !strings.Contains(stderr, "run ship box doctor") {
		t.Fatalf("journal append warning = %q", stderr)
	}
}

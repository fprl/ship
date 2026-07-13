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

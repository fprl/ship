package helper

import (
	"strings"
	"testing"
)

func TestValidateBoxNotifyURL(t *testing.T) {
	for _, raw := range []string{"not a url", "example.com/hook", "ftp://example.com/hook"} {
		if _, err := validateBoxNotifyURL(raw); err == nil {
			t.Fatalf("validateBoxNotifyURL(%q) succeeded, want error", raw)
		}
	}
	for _, raw := range []string{"http://example.com/hook", "https://example.com/hook"} {
		got, err := validateBoxNotifyURL(raw)
		if err != nil {
			t.Fatalf("validateBoxNotifyURL(%q): %v", raw, err)
		}
		if got != raw {
			t.Fatalf("validated URL = %q, want %q", got, raw)
		}
	}
}

func TestValidateBoxNotifyURLEmptyRefusesSet(t *testing.T) {
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	setHelperBoxClientAddress(t, "203.0.113.7")
	_, err := validateBoxNotifyURL("")
	if err == nil {
		t.Fatal("validateBoxNotifyURL(\"\") succeeded, want error")
	}
	if !strings.Contains(err.Error(), "ship box config 203.0.113.7 unset notify.url") || !strings.Contains(err.Error(), "ship box notify 203.0.113.7 --rm") {
		t.Fatalf("empty URL error = %q, want unset guidance", err)
	}
}

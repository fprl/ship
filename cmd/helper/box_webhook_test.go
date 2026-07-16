package helper

import (
	"strings"
	"testing"
)

func TestValidateBoxWebhookURL(t *testing.T) {
	for _, raw := range []string{"not a url", "example.com/hook", "ftp://example.com/hook"} {
		if _, err := validateBoxWebhookURL(raw); err == nil {
			t.Fatalf("validateBoxWebhookURL(%q) succeeded, want error", raw)
		}
	}
	for _, raw := range []string{"http://example.com/hook", "https://example.com/hook"} {
		got, err := validateBoxWebhookURL(raw)
		if err != nil {
			t.Fatalf("validateBoxWebhookURL(%q): %v", raw, err)
		}
		if got != raw {
			t.Fatalf("validated URL = %q, want %q", got, raw)
		}
	}
}

func TestValidateBoxWebhookURLEmptyRefusesSet(t *testing.T) {
	setTestStateRoot(t, t.TempDir())
	setHelperBoxClientAddress(t, "203.0.113.7")
	_, err := validateBoxWebhookURL("")
	if err == nil {
		t.Fatal("validateBoxWebhookURL(\"\") succeeded, want error")
	}
	if !strings.Contains(err.Error(), "ship box config 203.0.113.7 unset webhook.url") || !strings.Contains(err.Error(), "ship box webhook 203.0.113.7 --rm") {
		t.Fatalf("empty URL error = %q, want unset guidance", err)
	}
}

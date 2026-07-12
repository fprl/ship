package helper

import "testing"

func TestValidateBoxNotifyURL(t *testing.T) {
	for _, raw := range []string{"", "not a url", "example.com/hook", "ftp://example.com/hook"} {
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

package helper

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/secrets"
)

func TestSecretSetRejectsInvalidKeyWithErrcatCode(t *testing.T) {
	err := (appSecretSetCmd{App: "api", Env: "production", Key: "not-valid"}).Run()
	if !errcat.Is(err, errcat.CodeInvalidSecretKey) {
		t.Fatalf("Run error = %v, want invalid_secret_key", err)
	}
	coded, _ := errcat.As(err)
	if coded.Remediation() != "ship secret set KEY" {
		t.Fatalf("remediation = %q", coded.Remediation())
	}
}

func TestSecretListPayloadKeepsEmptyKeysAsArray(t *testing.T) {
	payload := secretListPayloadFor("api", "production", nil)
	if payload.App != "api" || payload.Env != "production" {
		t.Fatalf("unexpected identity: %+v", payload)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"keys":[]`) {
		t.Fatalf("empty keys should encode as [], got: %s", raw)
	}
}

func TestSecretSetStoresStdinVerbatim(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	t.Setenv("SUDO_USER", "")

	previousFingerprint := serverMemberFingerprint
	previousMember := serverAuthorizedMember
	setServerMemberFingerprint("")
	t.Cleanup(func() {
		serverMemberFingerprint = previousFingerprint
		serverAuthorizedMember = previousMember
	})

	for _, tt := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "trailing newline", key: "PEM", value: "-----END PRIVATE KEY-----\n"},
		{name: "plain value", key: "TOKEN", value: "plain-secret"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withSecretSetStdin(t, []byte(tt.value))
			if err := (appSecretSetCmd{App: "api", Env: "production", Key: tt.key}).Run(); err != nil {
				t.Fatal(err)
			}
			got, err := secrets.Get("api", "production", tt.key)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.value {
				t.Fatalf("stored value = %q, want %q", got, tt.value)
			}
		})
	}
}

func TestSecretSetRejectsEmbeddedNewline(t *testing.T) {
	t.Setenv("SHIP_SECRETS_DIR", t.TempDir())
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())
	t.Setenv("SUDO_USER", "")

	previousFingerprint := serverMemberFingerprint
	previousMember := serverAuthorizedMember
	setServerMemberFingerprint("")
	t.Cleanup(func() {
		serverMemberFingerprint = previousFingerprint
		serverAuthorizedMember = previousMember
	})

	withSecretSetStdin(t, []byte("first line\nsecond line"))
	err := (appSecretSetCmd{App: "api", Env: "production", Key: "MULTILINE"}).Run()
	if !errcat.Is(err, errcat.CodeSecretInvalid) {
		t.Fatalf("Run error = %v, want secret_invalid", err)
	}
	for _, want := range []string{"embedded newlines", "encode multi-line material", "next: ship secret set KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%s", want, err)
		}
	}
	if _, err := secrets.Get("api", "production", "MULTILINE"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("embedded newline value was stored: %v", err)
	}
}

func withSecretSetStdin(t *testing.T, value []byte) {
	t.Helper()
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(value); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
}

package helper

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
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

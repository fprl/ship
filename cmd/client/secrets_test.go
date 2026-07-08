package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
)

func TestParseDotenvSecretData(t *testing.T) {
	imported, err := parseDotenvSecretData(".env", []byte(`
# ignored
export DATABASE_URL="postgres://db"
API_KEY='secret with spaces'
HASH=value#kept
EMPTY=
DUP=first
DUP=second
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(imported.Keys, ","); got != "API_KEY,DATABASE_URL,DUP,EMPTY,HASH" {
		t.Fatalf("keys = %s", got)
	}
	wants := map[string]string{
		"DATABASE_URL": "postgres://db",
		"API_KEY":      "secret with spaces",
		"HASH":         "value#kept",
		"EMPTY":        "",
		"DUP":          "second",
	}
	for key, want := range wants {
		if got := string(imported.Values[key]); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseDotenvSecretDataRejectsMalformedLineWithLineNumber(t *testing.T) {
	_, err := parseDotenvSecretData(".env", []byte("GOOD=ok\nnot dotenv\nLATER=not-written\n"))
	if !errcat.Is(err, errcat.CodeDotenvMalformed) {
		t.Fatalf("error = %v, want dotenv_malformed", err)
	}
	for _, want := range []string{
		"dotenv import failed",
		".env:2: expected KEY=VALUE",
		"next: ship secret set --from .env",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should contain %q, got:\n%s", want, err)
		}
	}
	if strings.Contains(err.Error(), "not-written") {
		t.Fatalf("malformed dotenv error leaked a value:\n%s", err)
	}
}

func TestParseDotenvSecretDataRejectsInvalidKeyWithLineNumber(t *testing.T) {
	_, err := parseDotenvSecretData(".env", []byte("1BAD=value\n"))
	if !errcat.Is(err, errcat.CodeDotenvMalformed) ||
		!strings.Contains(err.Error(), `.env:1: invalid key "1BAD"; must match ^[A-Za-z_][A-Za-z0-9_]*$`) {
		t.Fatalf("unexpected invalid key error:\n%v", err)
	}
}

func TestParseDotenvSecretFileReadErrorUsesErrcat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.env")
	_, err := parseDotenvSecretFile(path)
	if !errcat.Is(err, errcat.CodeOperationFailed) {
		t.Fatalf("error = %v, want operation_failed", err)
	}
	for _, want := range []string{
		"operation failed",
		"read dotenv file",
		"next: ship secret set --from",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should contain %q, got:\n%s", want, err)
		}
	}
}

func TestValidateSecretSetOptionsRejectsFromAndKey(t *testing.T) {
	err := validateSecretSetOptions(SecretSetOptions{Key: "API_KEY", From: ".env"})
	if !errcat.Is(err, errcat.CodeUsageError) || !strings.Contains(err.Error(), "--from and KEY cannot be combined") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplySecretImportReplaceRemovesOmittedKeys(t *testing.T) {
	runner := &fakeSecretRunner{secrets: map[string][]byte{
		"OLD":  []byte("old"),
		"KEEP": []byte("before"),
	}}
	secret := secretContext{
		AppContext: &config.AppContext{AppName: "api", Server: "deploy@example.com"},
		EnvName:    "prod",
		Runner:     runner,
	}
	imported := dotenvSecretImport{
		Keys: []string{"API_KEY", "KEEP"},
		Values: map[string][]byte{
			"API_KEY": []byte("new"),
			"KEEP":    []byte("after"),
		},
	}

	summary, err := applySecretImport(secret, imported, true, "ship secret set --from .env")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(summary.Set, ","); got != "API_KEY,KEEP" {
		t.Fatalf("set summary = %s", got)
	}
	if got := strings.Join(summary.Removed, ","); got != "OLD" {
		t.Fatalf("removed summary = %s", got)
	}
	if got := string(runner.secrets["KEEP"]); got != "after" {
		t.Fatalf("KEEP value = %q", got)
	}
	if _, ok := runner.secrets["OLD"]; ok {
		t.Fatal("OLD should have been removed")
	}

	var report bytes.Buffer
	writeSecretImportSummary(&report, summary)
	text := report.String()
	for _, want := range []string{"set: API_KEY, KEEP", "removed: OLD", "set 2, removed 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary should contain %q, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "after") || strings.Contains(text, "new") {
		t.Fatalf("summary leaked a value:\n%s", text)
	}
}

type fakeSecretRunner struct {
	secrets  map[string][]byte
	commands []string
}

func (f *fakeSecretRunner) Close() {}

func (f *fakeSecretRunner) RunSSH(_ string, command string) (string, string, int, error) {
	f.commands = append(f.commands, command)
	switch {
	case command == serverAppSecretListCommand("api", "prod", true):
		var keys []string
		for key := range f.secrets {
			keys = append(keys, key)
		}
		data, err := json.Marshal(struct {
			App  string   `json:"app"`
			Env  string   `json:"env"`
			Keys []string `json:"keys"`
		}{App: "api", Env: "prod", Keys: keys})
		if err != nil {
			return "", "", 1, err
		}
		return string(data), "", 0, nil
	case strings.HasPrefix(command, serverCommand("app", "secret", "rm", "api", "prod")):
		key := lastCommandWord(command)
		delete(f.secrets, key)
		return fmt.Sprintf("Removed secret %s\n", key), "", 0, nil
	default:
		return "", "unexpected command: " + command, 1, nil
	}
}

func (f *fakeSecretRunner) RunSSHWithStdin(_ string, command string, stdin []byte) (string, string, int, error) {
	f.commands = append(f.commands, command)
	if !strings.HasPrefix(command, serverCommand("app", "secret", "set", "api", "prod")) {
		return "", "unexpected command: " + command, 1, nil
	}
	key := lastCommandWord(command)
	f.secrets[key] = append([]byte(nil), stdin...)
	return fmt.Sprintf("Stored secret %s\n", key), "", 0, nil
}

func lastCommandWord(command string) string {
	parts := strings.Fields(command)
	return parts[len(parts)-1]
}

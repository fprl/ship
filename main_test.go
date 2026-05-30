package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

func newTestParser(t *testing.T) *kong.Kong {
	t.Helper()
	parser, err := kong.New(&cli{}, kong.Name("simple-vps"))
	if err != nil {
		t.Fatalf("parser setup failed: %v", err)
	}
	return parser
}

func TestPublicCLIParsesV1Contract(t *testing.T) {
	tests := [][]string{
		{"init"},
		{"init", "--config", "apps/api/simple-vps.toml"},
		{"check"},
		{"check", "--env", "production"},
		{"check", "-e", "production"},
		{"setup", "--env", "production"},
		{"deploy", "--env", "production"},
		{"deploy", "--env", "production", "--include-dotenv"},
		{"deploy", "-e", "production", "--config", "apps/api/simple-vps.toml"},
		{"status", "--env", "production", "--json"},
		{"logs", "web", "--env", "production", "--follow", "--tail", "100"},
		{"restart", "web", "--env", "production"},
		{"rollback", "abc1234", "--env", "production"},
		{"backup", "create", "--env", "production", "--json"},
		{"backup", "list", "--env", "production", "--json"},
		{"backup", "rm", "backup-id", "--env", "production"},
		{"restore", "--from", "backup-id", "--env", "production", "--dry-run"},
		{"secret", "set", "DATABASE_URL", "--env", "production"},
		{"secret", "list", "--env", "production", "--json"},
		{"secret", "rm", "DATABASE_URL", "--env", "production"},
		{"destroy", "--env", "production", "--confirm", "api", "--purge"},
		{"ssh", "--env", "production"},
		{"app", "list", "--server", "deploy@example.com"},
		{"host", "status", "--server", "deploy@example.com"},
		{"host", "doctor", "--server", "deploy@example.com", "--json"},
		{"version"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt, "_"), func(t *testing.T) {
			if _, err := newTestParser(t).Parse(tt); err != nil {
				t.Fatalf("parse %v failed: %v", tt, err)
			}
		})
	}
}

func TestPublicCLIRejectsRemovedCompatibilityForms(t *testing.T) {
	tests := [][]string{
		{"setup", "production"},
		{"deploy", "production"},
		{"status", "production"},
		{"backup", "production"},
		{"backup", "list", "production"},
		{"restore", "--from", "backup-id", "production"},
		{"secret", "set", "production", "DATABASE_URL"},
		{"logs", "production", "web"},
		{"restart", "production", "web"},
		{"rollback", "production"},
		{"app", "list"},
		{"host", "status"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt, "_"), func(t *testing.T) {
			if _, err := newTestParser(t).Parse(tt); err == nil {
				t.Fatalf("parse %v unexpectedly succeeded", tt)
			}
		})
	}
}

func TestAppRootUsesManifestDirectory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "apps", "api", "simple-vps.toml")
	got, err := appRoot(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "apps", "api")
	if got != want {
		t.Fatalf("appRoot = %q, want %q", got, want)
	}
}

func TestAppRootRequiresCanonicalManifestFilename(t *testing.T) {
	_, err := appRoot(filepath.Join(t.TempDir(), "deploy.toml"))
	if err == nil || !strings.Contains(err.Error(), "simple-vps.toml") {
		t.Fatalf("expected canonical manifest filename error, got %v", err)
	}
}

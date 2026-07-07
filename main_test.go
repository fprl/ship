package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

func newTestParser(t *testing.T) *kong.Kong {
	t.Helper()
	parser, err := kong.New(
		&cli{},
		kong.Name("ship"),
		kong.ExplicitGroups(cliCommandGroups()),
		kong.ConfigureHelp(kong.HelpOptions{NoExpandSubcommands: true}),
	)
	if err != nil {
		t.Fatalf("parser setup failed: %v", err)
	}
	return parser
}

func TestPublicCLIParsesV1Contract(t *testing.T) {
	tests := [][]string{
		{"init"},
		{"init", "--config", "apps/api/ship.toml"},
		{"check"},
		{"check", "--env", "production"},
		{"check", "-e", "production"},
		{"deploy"},
		{"deploy", "--branch", "feat/x"},
		{"deploy", "--env", "production"},
		{"deploy", "--env", "production", "--include-dotenv"},
		{"deploy", "-e", "production", "--config", "apps/api/ship.toml"},
		{"status", "--branch", "feat/x"},
		{"status", "--env", "production", "--json"},
		{"logs", "--branch", "feat/x"},
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
		{"init", "--tls", "internal"},
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

func TestHiddenSetupRepairCommandStillParses(t *testing.T) {
	if _, err := newTestParser(t).Parse([]string{"setup", "--env", "production"}); err != nil {
		t.Fatalf("hidden setup command should remain available for repair: %v", err)
	}
}

func TestHostWithoutSubcommandShowsSubcommandHelp(t *testing.T) {
	_, err := newTestParser(t).Parse([]string{"host"})
	if err == nil {
		t.Fatal("parse host unexpectedly succeeded")
	}
	text := err.Error()
	if strings.Contains(text, "--server") {
		t.Fatalf("host without subcommand should not fall through to host status: %v", err)
	}
	for _, want := range []string{"status", "doctor", "install"} {
		if !strings.Contains(text, want) {
			t.Fatalf("host parse error should mention %q subcommand, got: %v", want, err)
		}
	}
}

func TestTopLevelHelpShowsParentCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	parser, err := kong.New(
		&cli{},
		kong.Name("ship"),
		kong.Description("Deploy containerized apps to a single hardened VPS."),
		kong.ExplicitGroups(cliCommandGroups()),
		kong.ConfigureHelp(kong.HelpOptions{NoExpandSubcommands: true}),
		kong.UsageOnError(),
		kong.Exit(func(int) {}),
		kong.Writers(&stdout, &stderr),
	)
	if err != nil {
		t.Fatalf("parser setup failed: %v", err)
	}
	_, _ = parser.Parse([]string{"--help"})
	text := stdout.String() + stderr.String()
	for _, want := range []string{"Project commands:", "Host commands:", "Global commands:", "backup <command>", "app <command>", "secret <command>", "host <command>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("top-level help should mention %q, got:\n%s", want, text)
		}
	}
	for _, legacy := range []string{"setup", "backup create", "app list", "secret set", "host status"} {
		if strings.Contains(text, legacy) {
			t.Fatalf("top-level help should not expand %q, got:\n%s", legacy, text)
		}
	}
}

func TestCLIArgsShowsHelpForNoArgs(t *testing.T) {
	got := cliArgs(nil)
	if len(got) != 1 || got[0] != "--help" {
		t.Fatalf("cliArgs(nil) = %v, want [--help]", got)
	}
}

func TestCLIArgsKeepsExplicitArgs(t *testing.T) {
	got := cliArgs([]string{"deploy", "--env", "production"})
	want := []string{"deploy", "--env", "production"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("cliArgs kept args = %v, want %v", got, want)
	}
}

func TestAppRootUsesManifestDirectory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "apps", "api", "ship.toml")
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
	if err == nil || !strings.Contains(err.Error(), "ship.toml") {
		t.Fatalf("expected canonical manifest filename error, got %v", err)
	}
}

func TestProjectAppRootExplainsMissingManifest(t *testing.T) {
	_, err := projectAppRoot(filepath.Join(t.TempDir(), "ship.toml"))
	if err == nil {
		t.Fatal("expected missing manifest error")
	}
	text := err.Error()
	for _, want := range []string{
		"this is a project command",
		"ship.toml was not found",
		"--config path/to/ship.toml",
		"ship init",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing manifest error should contain %q, got:\n%s", want, text)
		}
	}
}

func TestProjectAppRootRejectsManifestDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "ship.toml"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := projectAppRoot(filepath.Join(root, "ship.toml"))
	if err == nil || !strings.Contains(err.Error(), "got directory") {
		t.Fatalf("expected directory error, got %v", err)
	}
}

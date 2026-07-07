package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/fprl/simple-vps/cmd/client"
	"github.com/fprl/simple-vps/internal/agentdocs"
	"github.com/fprl/simple-vps/internal/errcat"
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

func TestPublicCLIParsesV2Contract(t *testing.T) {
	tests := [][]string{
		{},
		{"--json"},
		{"--branch", "feat/x"},
		{"--tls", "internal"},
		{"init"},
		{"init", "--box", "deploy@example.com"},
		{"init", "--config", "apps/api/ship.toml"},
		{"status"},
		{"status", "--json"},
		{"logs"},
		{"logs", "--json"},
		{"logs", "web", "--follow", "--tail", "100"},
		{"exec", "env"},
		{"exec", "--branch", "feat/x", "env"},
		{"exec", "--", "--help"},
		{"why"},
		{"why", "--json"},
		{"why", "--branch", "feat/x"},
		{"rollback"},
		{"rollback", "abc1234"},
		{"rm", "feat/x"},
		{"rm", "main", "--confirm", "api"},
		{"pin", "feat/x"},
		{"unpin", "feat/x"},
		{"save"},
		{"save", "--to", "/tmp/backups"},
		{"restore", "--from", "backup-id"},
		{"secret", "set", "DATABASE_URL"},
		{"secret", "set", "DATABASE_URL", "--preview"},
		{"secret", "set", "DATABASE_URL", "--branch", "feat/x"},
		{"secret", "ls"},
		{"secret", "ls", "--json"},
		{"secret", "ls", "--preview"},
		{"secret", "ls", "--branch", "feat/x"},
		{"secret", "rm", "DATABASE_URL"},
		{"secret", "rm", "DATABASE_URL", "--preview"},
		{"secret", "rm", "DATABASE_URL", "--branch", "feat/x"},
		{"ssh"},
		{"box", "init", "deploy@example.com"},
		{"box", "doctor", "deploy@example.com"},
		{"box", "doctor", "deploy@example.com", "--json"},
		{"box", "ls", "deploy@example.com"},
		{"box", "ls", "deploy@example.com", "--json"},
		{"docs"},
		{"help"},
		{"help", "status"},
		{"help", "status", "--json"},
		{"help", "secret", "ls", "--json"},
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
		{"setup", "--env", "production"},
		{"check"},
		{"init", "--tls", "internal"},
		{"init", "--env", "production"},
		{"init", "--server", "deploy@example.com"},
		{"deploy"},
		{"deploy", "production"},
		{"deploy", "--env", "production"},
		{"status", "--env", "production"},
		{"status", "--branch", "feat/x"},
		{"status", "production"},
		{"backup", "production"},
		{"backup", "list", "production"},
		{"restore", "--from", "backup-id", "production"},
		{"restore", "--from", "backup-id", "--env", "production"},
		{"secret", "set", "production", "DATABASE_URL"},
		{"secret", "set", "DATABASE_URL", "--env", "production"},
		{"secret", "list"},
		{"logs", "production", "web"},
		{"logs", "web", "--env", "production"},
		{"restart"},
		{"restart", "production", "web"},
		{"destroy", "--env", "production"},
		{"app", "list"},
		{"host", "status"},
		{"box", "doctor", "--server", "deploy@example.com"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt, "_"), func(t *testing.T) {
			if _, err := newTestParser(t).Parse(tt); err == nil {
				t.Fatalf("parse %v unexpectedly succeeded", tt)
			}
		})
	}
}

func TestBoxWithoutSubcommandShowsSubcommandHelp(t *testing.T) {
	_, err := newTestParser(t).Parse([]string{"box"})
	if err == nil {
		t.Fatal("parse box unexpectedly succeeded")
	}
	text := err.Error()
	if strings.Contains(text, "--server") {
		t.Fatalf("box without subcommand should not mention removed --server: %v", err)
	}
	for _, want := range []string{"init", "doctor", "ls"} {
		if !strings.Contains(text, want) {
			t.Fatalf("box parse error should mention %q subcommand, got: %v", want, err)
		}
	}
}

func TestTopLevelHelpShowsParentCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	parser, err := kong.New(
		&cli{},
		kong.Name("ship"),
		kong.Description("Run `ship` inside an app to deploy the current branch. Use commands below for reads, rollback, cleanup, secrets, and box management."),
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
	for _, want := range []string{"Project commands:", "Host commands:", "Global commands:", "init", "status", "logs", "exec", "why", "rollback", "rm <branch>", "pin", "unpin", "save", "restore", "ssh", "secret <command>", "box <command>", "docs", "help", "version"} {
		if !strings.Contains(text, want) {
			t.Fatalf("top-level help should mention %q, got:\n%s", want, text)
		}
	}
	for _, legacy := range []string{"deploy <command>", "check", "restart", "backup <command>", "destroy", "app <command>", "host <command>", "--env", "--server", "--dirty"} {
		if strings.Contains(text, legacy) {
			t.Fatalf("top-level help should not expand %q, got:\n%s", legacy, text)
		}
	}
}

func TestAgentDocsErrcatDrift(t *testing.T) {
	section := markdownSection(t, embeddedAgentDocs, "<!-- BEGIN GENERATED ERRCAT -->", "<!-- END GENERATED ERRCAT -->")
	got := regexCaptures(t, section, regexp.MustCompile("(?m)^- `([a-z0-9_]+)`:"))
	want := make([]string, 0, len(errcat.Catalogue()))
	for _, entry := range errcat.Catalogue() {
		want = append(want, string(entry.Code))
	}
	assertSameStrings(t, got, want)
}

func TestAgentDocsVerbDrift(t *testing.T) {
	section := markdownSection(t, embeddedAgentDocs, "<!-- BEGIN VERBS -->", "<!-- END VERBS -->")
	got := regexCaptures(t, section, regexp.MustCompile("(?m)^### `([^`]+)`$"))
	want := parserPublicVerbs(t)
	assertSameStrings(t, got, want)
	assertSameStrings(t, agentdocs.VerbNames(), want)
}

func TestShipDocsSmoke(t *testing.T) {
	var out bytes.Buffer
	if err := writeShipDocs(&out); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(out.String(), "\n"); lines == 0 {
		t.Fatalf("ship docs line count = %d, want > 0", lines)
	}
}

func TestShipHelpJSONForEveryPublicVerb(t *testing.T) {
	for _, verb := range parserPublicVerbs(t) {
		t.Run(strings.ReplaceAll(verb, " ", "_"), func(t *testing.T) {
			args := append([]string{"help"}, strings.Fields(verb)...)
			args = append(args, "--json")
			if _, err := newTestParser(t).Parse(args); err != nil {
				t.Fatalf("parse ship %s failed: %v", strings.Join(args, " "), err)
			}
			var out bytes.Buffer
			if err := writeShipHelp(&out, verb, true); err != nil {
				t.Fatalf("ship help %s --json failed: %v", verb, err)
			}
			var payload struct {
				Verb    string           `json:"verb"`
				Purpose string           `json:"purpose"`
				Usage   string           `json:"usage"`
				Flags   []agentdocs.Flag `json:"flags"`
				Errors  []string         `json:"errors"`
			}
			if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
				t.Fatalf("help json did not parse: %v\n%s", err, out.String())
			}
			if payload.Verb != verb || payload.Purpose == "" || payload.Usage == "" || payload.Flags == nil || payload.Errors == nil {
				t.Fatalf("help json schema mismatch for %q: %+v", verb, payload)
			}
		})
	}
}

func TestCLIArgsShowsHelpForNoArgsOutsideApp(t *testing.T) {
	got := cliArgs(nil)
	if len(got) != 1 || got[0] != "--help" {
		t.Fatalf("cliArgs(nil) = %v, want [--help]", got)
	}
}

func parserPublicVerbs(t *testing.T) []string {
	t.Helper()
	parser := newTestParser(t)
	seen := map[string]bool{}
	if parser.Model.Node.DefaultCmd != nil {
		seen["ship"] = true
	}
	collectPublicCommandLeaves(parser.Model.Node, nil, seen)
	out := make([]string, 0, len(seen))
	for verb := range seen {
		out = append(out, verb)
	}
	sort.Strings(out)
	return out
}

func collectPublicCommandLeaves(node *kong.Node, path []string, out map[string]bool) {
	for _, child := range node.Children {
		if child.Hidden {
			continue
		}
		next := append(append([]string(nil), path...), child.Name)
		if child.Leaf() {
			out[strings.Join(next, " ")] = true
			continue
		}
		collectPublicCommandLeaves(child, next, out)
	}
}

func markdownSection(t *testing.T, doc, start, end string) string {
	t.Helper()
	startIdx := strings.Index(doc, start)
	if startIdx < 0 {
		t.Fatalf("markdown section missing start marker %q", start)
	}
	startIdx += len(start)
	endIdx := strings.Index(doc[startIdx:], end)
	if endIdx < 0 {
		t.Fatalf("markdown section missing end marker %q", end)
	}
	return doc[startIdx : startIdx+endIdx]
}

func regexCaptures(t *testing.T, text string, re *regexp.Regexp) []string {
	t.Helper()
	matches := re.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) != 2 {
			t.Fatalf("unexpected regex match shape: %v", match)
		}
		out = append(out, match[1])
	}
	if len(out) == 0 {
		t.Fatalf("no matches for %s", re.String())
	}
	return out
}

func assertSameStrings(t *testing.T, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("string set mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

func TestCLIArgsKeepsBareShipInsideApp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, client.ManifestFile), []byte("name = \"api\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(old)
	})
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	got := cliArgs(nil)
	if len(got) != 0 {
		t.Fatalf("cliArgs(nil) inside app = %v, want []", got)
	}
}

func TestCLIArgsKeepsExplicitArgs(t *testing.T) {
	got := cliArgs([]string{"status", "--json"})
	want := []string{"status", "--json"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("cliArgs kept args = %v, want %v", got, want)
	}
}

func TestCommandErrorExitCodeSeparatesUsageManifestFromOperations(t *testing.T) {
	if got := commandErrorExitCode(os.ErrNotExist); got != 1 {
		t.Fatalf("operation exit code = %d, want 1", got)
	}
	if got := commandErrorExitCode(filepath.ErrBadPattern); got != 1 {
		t.Fatalf("ordinary error exit code = %d, want 1", got)
	}
	if got := commandErrorExitCode(errors.New("ship.toml not found")); got != 2 {
		t.Fatalf("manifest exit code = %d, want 2", got)
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

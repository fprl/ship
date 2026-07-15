package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/fprl/ship/cmd/client"
	"github.com/fprl/ship/internal/agentdocs"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
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
		{"preview", "pin", "feat/x"},
		{"preview", "unpin", "feat/x"},
		{"preview", "share"},
		{"preview", "share", "--rotate"},
		{"data", "fork"},
		{"data", "reset"},
		{"data", "save"},
		{"data", "save", "--out", "/tmp/api.data.tar.gz"},
		{"data", "restore", "backup-id"},
		{"data", "restore", "backup-id", "--confirm", "api"},
		{"data", "ls"},
		{"data", "ls", "--json"},
		{"secret", "set", "DATABASE_URL"},
		{"secret", "set", "DATABASE_URL", "--preview"},
		{"secret", "set", "DATABASE_URL", "--branch", "feat/x"},
		{"secret", "set", "--from", ".env"},
		{"secret", "set", "--from", ".env", "--replace", "--preview"},
		{"secret", "set", "--from", ".env", "--branch", "feat/x"},
		{"secret", "ls"},
		{"secret", "ls", "--json"},
		{"secret", "ls", "--preview"},
		{"secret", "ls", "--branch", "feat/x"},
		{"secret", "rm", "DATABASE_URL"},
		{"secret", "rm", "DATABASE_URL", "--preview"},
		{"secret", "rm", "DATABASE_URL", "--branch", "feat/x"},
		{"ssh"},
		{"box", "setup", "root@example.com"},
		{"box", "doctor", "example.com"},
		{"box", "doctor", "example.com", "--json"},
		{"box", "webhook", "example.com"},
		{"box", "webhook", "example.com", "--json"},
		{"box", "webhook", "example.com", "https://ntfy.example/ship"},
		{"box", "webhook", "example.com", "--rm"},
		{"box", "app", "ls", "example.com"},
		{"box", "app", "ls", "example.com", "--json"},
		{"box", "member", "add", "./alice.pub", "--name", "alice"},
		{"box", "member", "add", "./alice.pub", "example.com", "--name", "alice", "--role", "owner"},
		{"box", "member", "ls"},
		{"box", "member", "ls", "example.com", "--json"},
		{"box", "member", "rm", "alice"},
		{"box", "member", "rm", "alice", "example.com"},
		{"box", "approval", "ls"},
		{"box", "approval", "ls", "example.com", "--json"},
		{"box", "approval", "grant", "abc123xy"},
		{"box", "approval", "grant", "abc123xy", "example.com"},
		{"box", "app", "rm", "api", "--confirm", "api"},
		{"box", "app", "rm", "api", "example.com", "--confirm", "api"},
		{"box", "forget", "example.com"},
		{"docs"},
		{"help"},
		{"help", "status"},
		{"help", "status", "--json"},
		{"help", "secret", "ls", "--json"},
		{"help", "completion", "--json"},
		{"completion", "bash"},
		{"completion", "zsh"},
		{"completion", "fish"},
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

func TestPublicCLIRequiresMemberName(t *testing.T) {
	ctx, err := newTestParser(t).Parse([]string{"box", "member", "add", "./alice.pub", "203.0.113.7"})
	if err != nil {
		t.Fatalf("member add without --name should parse for product validation: %v", err)
	}
	if err := ctx.Run(); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("member add without --name error = %v, want --name usage error", err)
	}
}

func TestMissingPositionalArgumentsRunShipErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "member rm", args: []string{"box", "member", "rm"}, want: "next: ship box member ls [<box>]"},
		{name: "approval grant", args: []string{"box", "approval", "grant"}, want: "next: ship box approval ls [<box>]"},
		{name: "app rm", args: []string{"box", "app", "rm"}, want: "next: ship box app ls [<box>]"},
		{name: "member add", args: []string{"box", "member", "add"}, want: "next: ship box member add <https-url|key|path> <box> --name <name>"},
		{name: "rm", args: []string{"rm"}, want: "next: ship status"},
		{name: "preview pin", args: []string{"preview", "pin"}, want: "next: ship status"},
		{name: "preview unpin", args: []string{"preview", "unpin"}, want: "next: ship status"},
		{name: "data restore", args: []string{"data", "restore"}, want: "next: ship data ls"},
		{name: "secret rm", args: []string{"secret", "rm"}, want: "next: ship secret ls"},
		{name: "completion", args: []string{"completion"}, want: "next: ship completion <bash|zsh|fish>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := &cli{}
			parser, err := kong.New(
				parsed,
				kong.Name("ship"),
				kong.ExplicitGroups(cliCommandGroups()),
				kong.ConfigureHelp(kong.HelpOptions{NoExpandSubcommands: true}),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := parser.Parse(tt.args)
			if err != nil {
				t.Fatalf("parse %v failed: %v", tt.args, err)
			}
			err = ctx.Run()
			if !errcat.Is(err, errcat.CodeUsageError) {
				t.Fatalf("run %v error = %v, want usage_error", tt.args, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run %v error = %v, want %q", tt.args, err, tt.want)
			}
			if strings.Contains(err.Error(), "next: ship help") {
				t.Fatalf("run %v fell back to generic help: %v", tt.args, err)
			}
		})
	}
}

func TestMissingBoxTargetsUseKnownBoxRemediation(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	knownDir := filepath.Join(configHome, "ship")
	if err := os.MkdirAll(knownDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knownDir, "known_hosts"), []byte("203.0.113.7 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICtppnbbz76teU3iU6BguTmo//WITtYN35e4gSER6UNt\n"), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "setup", args: []string{"box", "setup"}, want: "target a box to set up\nknown boxes (~/.config/ship/known_hosts):\n  203.0.113.7\nnext: ship box setup <ssh-target>"},
		{name: "forget", args: []string{"box", "forget"}, want: "target a box\nknown boxes (~/.config/ship/known_hosts):\n  203.0.113.7\nnext: ship box forget <box>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := &cli{}
			parser, err := kong.New(parsed, kong.Name("ship"), kong.ExplicitGroups(cliCommandGroups()))
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := parser.Parse(tt.args)
			if err != nil {
				t.Fatalf("parse %v failed: %v", tt.args, err)
			}
			err = ctx.Run()
			if !errcat.Is(err, errcat.CodeBoxTargetRequired) {
				t.Fatalf("run %v error = %v, want box_target_required", tt.args, err)
			}
			if got := err.Error(); got != tt.want {
				t.Fatalf("run %v error = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestMissingBoxArgumentRemediationUsesManifestBox(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "ship.toml")
	if err := os.Mkdir(filepath.Join(root, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("name = \"api\"\nbox = \"203.0.113.7\"\n\n[routes]\n\"api.example.com\" = { static = \"dist\" }\n"), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{name: "member rm", run: func() error { return (boxMemberRmCmd{Config: config}).Run() }, want: "next: ship box member ls 203.0.113.7"},
		{name: "approval grant", run: func() error { return (boxApprovalGrantCmd{Config: config}).Run() }, want: "next: ship box approval ls 203.0.113.7"},
		{name: "app rm", run: func() error { return (boxAppRmCmd{Config: config}).Run() }, want: "next: ship box app ls 203.0.113.7"},
		{name: "member add", run: func() error { return (boxMemberAddCmd{Config: config}).Run() }, want: "next: ship box member add <https-url|key|path> 203.0.113.7 --name <name>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if !errcat.Is(err, errcat.CodeUsageError) {
				t.Fatalf("error = %v, want usage_error", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestExecPassthroughRetainsSeparatorForClientNormalization(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "command without separator", args: []string{"exec", "sh", "-c", "echo hi"}, want: []string{"sh", "-c", "echo hi"}},
		{name: "separator before command", args: []string{"exec", "--", "sh", "-c", "echo hi"}, want: []string{"--", "sh", "-c", "echo hi"}},
		{name: "separator before dash command", args: []string{"exec", "--", "--flag-first-cmd"}, want: []string{"--", "--flag-first-cmd"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := &cli{}
			parser, err := kong.New(parsed, kong.Name("ship"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parser.Parse(tt.args); err != nil {
				t.Fatalf("parse %v: %v", tt.args, err)
			}
			if got := parsed.Exec.Command; !slices.Equal(got, tt.want) {
				t.Fatalf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPublicCLIRejectsRemovedDataRmVerb(t *testing.T) {
	if _, err := newTestParser(t).Parse([]string{"data", "rm"}); err == nil {
		t.Fatal("data rm should be rejected after the reset rename")
	}
}

func TestPublicCLIRejectsDeletedBoxGrammar(t *testing.T) {
	for _, args := range [][]string{
		{"box", "members"},
		{"box", "apps"},
		{"box", "rm", "api", "--confirm", "api"},
		{"box", "approvals"},
		{"box", "approve", "abc123xy"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			if _, err := newTestParser(t).Parse(args); err == nil {
				t.Fatalf("parse %v unexpectedly succeeded", args)
			}
		})
	}
}

func TestLogsTailTracksWhetherTheFlagWasSet(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want *int
	}{
		{name: "unset", args: []string{"logs"}},
		{name: "zero", args: []string{"logs", "--tail", "0"}, want: intPtrMain(0)},
		{name: "positive", args: []string{"logs", "--tail", "50"}, want: intPtrMain(50)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := &cli{}
			parser, err := kong.New(parsed, kong.Name("ship"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parser.Parse(tt.args); err != nil {
				t.Fatalf("parse %v: %v", tt.args, err)
			}
			if tt.want == nil {
				if parsed.Logs.Tail != nil {
					t.Fatalf("tail = %d, want unset", *parsed.Logs.Tail)
				}
				return
			}
			if parsed.Logs.Tail == nil || *parsed.Logs.Tail != *tt.want {
				t.Fatalf("tail = %v, want %d", parsed.Logs.Tail, *tt.want)
			}
		})
	}
}

func intPtrMain(v int) *int {
	return &v
}

func TestLogsTailRejectsNegative(t *testing.T) {
	parsed := &cli{}
	parser, err := kong.New(parsed, kong.Name("ship"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"logs", "--tail=-1"}); err != nil {
		t.Fatalf("parse negative tail: %v", err)
	}
	err = parsed.Logs.Run()
	if !errcat.Is(err, errcat.CodeUsageError) {
		t.Fatalf("logsCmd.Run() error = %v, want usage error", err)
	}
}

func TestParseBoxConfigArgs(t *testing.T) {
	tests := []struct {
		args                   []string
		wantTarget, wantAction string
		wantKey, wantValue     string
	}{
		{args: []string{"example.com"}, wantTarget: "example.com"},
		{args: []string{"example.com", "set", "webhook.url", "https://ntfy.example/ship"}, wantTarget: "example.com", wantAction: "set", wantKey: "webhook.url", wantValue: "https://ntfy.example/ship"},
		{args: []string{"unset", "webhook.url"}, wantAction: "unset", wantKey: "webhook.url"},
	}
	for _, tt := range tests {
		target, action, key, value, err := parseBoxConfigArgs(tt.args)
		if err != nil {
			t.Fatalf("parseBoxConfigArgs(%v): %v", tt.args, err)
		}
		if target != tt.wantTarget || action != tt.wantAction || key != tt.wantKey || value != tt.wantValue {
			t.Fatalf("parseBoxConfigArgs(%v) = (%q, %q, %q, %q)", tt.args, target, action, key, value)
		}
	}
	if _, _, _, _, err := parseBoxConfigArgs([]string{"example.com", "set", "webhook.url"}); err == nil {
		t.Fatal("parseBoxConfigArgs accepted set without a value")
	}
}

func TestPublicCLIRejectsRemovedCompatibilityForms(t *testing.T) {
	tests := [][]string{
		{"setup", "--env", "production"},
		{"check"},
		{"init", "--tls", "internal"},
		{"init", "--env", "production"},
		{"init", "--server", "deploy@example.com"},
		{"init", "--template", "container"},
		{"init", "--port", "3000"},
		{"--include-dotenv"},
		{"deploy"},
		{"deploy", "production"},
		{"deploy", "--env", "production"},
		{"status", "--env", "production"},
		{"status", "--branch", "feat/x"},
		{"status", "production"},
		{"backup", "production"},
		{"backup", "list", "production"},
		{"save"},
		{"restore", "--from", "backup-id"},
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
		{"pin", "feat/x"},
		{"unpin", "feat/x"},
		{"share"},
		{"preview", "password"},
		{"app", "list"},
		{"host", "status"},
		{"box", "add-key", "alice"},
		{"box", "init", "deploy@example.com"},
		{"box", "notify", "example.com"},
		{"member", "add", "alice"},
		{"member", "ls"},
		{"member", "rm", "alice"},
		{"approve"},
		{"approve", "abc123xy"},
		{"box", "doctor", "--server", "deploy@example.com"},
		{"box", "setup", "example.com", "--ingress", "cloudflare"},
		{"box", "setup", "example.com", "--admin", "tailscale"},
		{"box", "setup", "example.com", "--tailscale"},
		{"box", "setup", "example.com", "--tailscale-auth-key", "tskey-test"},
		{"box", "setup", "example.com", "--tailscale-hostname", "ship"},
		{"box", "setup", "example.com", "--cloudflare-tunnel"},
		{"box", "setup", "example.com", "--cloudflare-api-token", "token"},
		{"box", "setup", "example.com", "--cloudflare-account-id", "account"},
		{"box", "setup", "example.com", "--cloudflare-tunnel-token", "token"},
		{"box", "setup", "example.com", "--cloudflare-tunnel-config", "/tmp/config"},
		{"box", "setup", "example.com", "--litestream"},
		{"box", "setup", "example.com", "--setup-secrets-file", "/tmp/secrets"},
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
	for _, want := range []string{"setup", "doctor", "webhook", "app"} {
		if !strings.Contains(text, want) {
			t.Fatalf("box parse error should mention %q subcommand, got: %v", want, err)
		}
	}
	if strings.Contains(text, "add-key") {
		t.Fatalf("box parse error should not mention removed add-key subcommand, got: %v", err)
	}
}

func TestBoxSetupRejectsRemovedTopologyFlags(t *testing.T) {
	for _, flag := range []string{
		"--ingress=cloudflare",
		"--admin=tailscale",
		"--tailscale",
		"--tailscale-auth-key=tskey-test",
		"--tailscale-hostname=ship",
		"--cloudflare-tunnel",
		"--cloudflare-api-token=token",
		"--cloudflare-account-id=account",
		"--cloudflare-tunnel-token=token",
		"--cloudflare-tunnel-config=/tmp/config",
		"--litestream",
		"--setup-secrets-file=/tmp/secrets",
	} {
		t.Run(flag, func(t *testing.T) {
			_, err := newTestParser(t).Parse([]string{"box", "setup", "example.com", flag})
			if err == nil || !strings.Contains(err.Error(), "unknown flag") {
				t.Fatalf("parse error = %v, want unknown flag", err)
			}
		})
	}
}

func TestBoxTargetRequiredRefusalListsKnownBoxes(t *testing.T) {
	tests := []struct {
		name  string
		known string
		want  string
	}{
		{
			name: "none known",
			want: "target a box\nknown boxes (~/.config/ship/known_hosts):\n  none known yet\nnext: ship box doctor <box>",
		},
		{
			name:  "one known",
			known: "128.140.3.159 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/\n",
			want:  "target a box\nknown boxes (~/.config/ship/known_hosts):\n  128.140.3.159\nnext: ship box doctor <box>",
		},
		{
			name: "two known",
			known: "128.140.3.159 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/\n" +
				"203.0.113.7 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICtppnbbz76teU3iU6BguTmo//WITtYN35e4gSER6UNt\n" +
				"128.140.3.159 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/\n",
			want: "target a box\nknown boxes (~/.config/ship/known_hosts):\n  128.140.3.159\n  203.0.113.7\nnext: ship box doctor <box>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			if tt.known != "" {
				dir := filepath.Join(configHome, "ship")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(tt.known), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := boxTargetFor(filepath.Join(t.TempDir(), "ship.toml"), "", "ship box doctor <box>")
			if err == nil {
				t.Fatal("expected target refusal")
			}
			if got := err.Error(); got != tt.want {
				t.Fatalf("refusal mismatch\nwant:\n%s\ngot:\n%s", tt.want, got)
			}
		})
	}
}

func TestBoxVerbHelpUsesBoxPlaceholder(t *testing.T) {
	for _, args := range [][]string{
		{"box", "doctor", "--help"},
		{"box", "webhook", "--help"},
		{"box", "app", "ls", "--help"},
		{"box", "app", "rm", "--help"},
		{"box", "member", "add", "--help"},
		{"box", "member", "ls", "--help"},
		{"box", "member", "rm", "--help"},
		{"box", "approval", "ls", "--help"},
		{"box", "approval", "grant", "--help"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			parser, err := kong.New(
				&cli{},
				kong.Name("ship"),
				kong.ExplicitGroups(cliCommandGroups()),
				kong.ConfigureHelp(kong.HelpOptions{NoExpandSubcommands: true}),
				kong.Exit(func(int) {}),
				kong.Writers(&stdout, &stderr),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = parser.Parse(args)
			text := stdout.String() + stderr.String()
			if !strings.Contains(text, "<box>") {
				t.Fatalf("box help should show <box>, got:\n%s", text)
			}
			if strings.Contains(text, "<ssh-target>") || strings.Contains(text, "SSH target") {
				t.Fatalf("box help should not expose ssh-target language, got:\n%s", text)
			}
		})
	}
}

func TestBoxLsRedirectsToAppLs(t *testing.T) {
	ctx, err := newTestParser(t).Parse([]string{"box", "ls"})
	if err != nil {
		t.Fatalf("parse box ls: %v", err)
	}
	err = ctx.Run()
	if !errcat.Is(err, errcat.CodeUsageError) || !strings.Contains(err.Error(), "ship box app ls") {
		t.Fatalf("box ls error = %v, want usage remediation for box app ls", err)
	}
}

func TestBoxAppRmTargetRequiredShowsFullCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := (boxAppRmCmd{Config: filepath.Join(t.TempDir(), "ship.toml"), App: "api"}).Run()
	if err == nil {
		t.Fatal("box app rm without a target unexpectedly succeeded")
	}
	if got, want := err.Error(), "target a box\nknown boxes (~/.config/ship/known_hosts):\n  none known yet\nnext: ship box app rm api <box> --confirm api"; got != want {
		t.Fatalf("target remediation = %q, want %q", got, want)
	}
}

func TestBoxHelpHidesForget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	parser, err := kong.New(&cli{}, kong.Name("ship"), kong.ConfigureHelp(kong.HelpOptions{NoExpandSubcommands: true}), kong.Exit(func(int) {}), kong.Writers(&stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = parser.Parse([]string{"box", "--help"})
	if strings.Contains(stdout.String()+stderr.String(), "forget") {
		t.Fatalf("box help exposed hidden forget command:\n%s%s", stdout.String(), stderr.String())
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
	for _, want := range []string{"Project commands:", "Host commands:", "Global commands:", "init", "status", "logs", "exec", "why", "rollback", "rm [<branch>]", "data <command>", "preview <command>", "ssh", "secret <command>", "box <command>", "docs", "help", "version"} {
		if !strings.Contains(text, want) {
			t.Fatalf("top-level help should mention %q, got:\n%s", want, text)
		}
	}
	for _, legacy := range []string{"deploy <command>", "check", "restart", "backup <command>", "destroy", "app <command>", "host <command>", "--env", "--server", "--dirty"} {
		if strings.Contains(text, legacy) {
			t.Fatalf("top-level help should not expand %q, got:\n%s", legacy, text)
		}
	}
	for _, removed := range []string{"member <command>", "approve <"} {
		if strings.Contains(text, removed) {
			t.Fatalf("top-level help exposed removed %q, got:\n%s", removed, text)
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
	want := documentedParserVerbs(t)
	assertSameStrings(t, got, want)
	assertSameStrings(t, agentdocs.VerbNames(), want)
}

func TestAgentDocsFlagDrift(t *testing.T) {
	leafNodes := parserLeafNodes(t)
	for _, verb := range documentedParserVerbs(t) {
		t.Run(strings.ReplaceAll(verb, " ", "_"), func(t *testing.T) {
			// These are documentation projections onto boxConfigClientCmd.Args;
			// they do not have distinct Kong nodes of their own.
			if verb == "box config set" || verb == "box config unset" {
				return
			}
			node, ok := leafNodes[verb]
			if !ok {
				t.Fatalf("documented verb %q has no Kong leaf", verb)
			}
			documented := map[string]bool{}
			item, ok := agentdocs.Lookup(verb)
			if !ok {
				t.Fatalf("documented verb %q missing from agentdocs", verb)
			}
			for _, flag := range item.Flags {
				if strings.HasPrefix(flag.Name, "--") {
					documented[flag.Name] = true
				}
			}

			kongFlags := map[string]bool{}
			for _, group := range node.AllFlags(false) {
				for _, flag := range group {
					// Kong's synthesized help flag is not a field on the
					// corresponding command struct or part of the agent contract.
					if flag.Name == "help" {
						continue
					}
					kongFlags["--"+flag.Name] = flag.Hidden
				}
			}
			for name := range documented {
				if _, ok := kongFlags[name]; !ok {
					t.Errorf("documented long flag %s is absent from Kong metadata for %s", name, verb)
				}
			}
			for name, hidden := range kongFlags {
				if !hidden && !documented[name] {
					t.Errorf("visible Kong long flag %s is undocumented for %s", name, verb)
				}
			}
		})
	}
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
	for _, verb := range documentedParserVerbs(t) {
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

func TestCompletionScriptsUseAgentDocsVerbs(t *testing.T) {
	want := agentdocs.VerbNames()
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			script, ok := agentdocs.CompletionScript(shell)
			if !ok {
				t.Fatalf("missing completion script for %s", shell)
			}
			got := completionScriptVerbMetadata(t, script)
			assertSameStrings(t, got, want)
			for _, command := range []string{"secret", "box", "member", "completion"} {
				if !strings.Contains(script, command) {
					t.Fatalf("%s completion should mention %q:\n%s", shell, command, script)
				}
			}
		})
	}
}

func TestCompletionHelpMentionsInstallLines(t *testing.T) {
	var stdout, stderr bytes.Buffer
	parser, err := kong.New(
		&cli{},
		kong.Name("ship"),
		kong.ExplicitGroups(cliCommandGroups()),
		kong.ConfigureHelp(kong.HelpOptions{NoExpandSubcommands: true}),
		kong.Exit(func(int) {}),
		kong.Writers(&stdout, &stderr),
	)
	if err != nil {
		t.Fatalf("parser setup failed: %v", err)
	}
	_, _ = parser.Parse([]string{"completion", "--help"})
	text := strings.Join(strings.Fields(stdout.String()+stderr.String()), " ")
	for _, want := range []string{
		"ship completion bash > /etc/bash_completion.d/ship",
		"ship completion zsh > ~/.zsh/completions/_ship",
		"ship completion fish > ~/.config/fish/completions/ship.fish",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("completion help should mention %q, got:\n%s", want, text)
		}
	}
}

func TestReleaseWorkflowUploadsInstallerAndExpectedAssets(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "gh release upload \"$TAG\"")
	if start < 0 {
		t.Fatalf("release workflow is missing gh release upload command")
	}
	block := text[start:]
	for _, want := range []string{
		"dist/ship-linux-amd64",
		"dist/ship-linux-arm64",
		"dist/ship-darwin-amd64",
		"dist/ship-darwin-arm64",
		"dist/SHA256SUMS",
		"install.sh",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("release upload assets should include %q, got:\n%s", want, block)
		}
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

func documentedParserVerbs(t *testing.T) []string {
	t.Helper()
	out := parserPublicVerbs(t)
	out = append(out, "box config set", "box config unset", "completion")
	sort.Strings(out)
	return out
}

func completionScriptVerbMetadata(t *testing.T, script string) []string {
	t.Helper()
	matches := regexp.MustCompile(`(?m)^# ship completion verbs-json: (.+)$`).FindStringSubmatch(script)
	if len(matches) != 2 {
		t.Fatalf("completion script missing verbs metadata:\n%s", script)
	}
	var verbs []string
	if err := json.Unmarshal([]byte(matches[1]), &verbs); err != nil {
		t.Fatalf("completion verbs metadata is invalid JSON: %v\n%s", err, matches[1])
	}
	return verbs
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

func parserLeafNodes(t *testing.T) map[string]*kong.Node {
	t.Helper()
	parser := newTestParser(t)
	out := map[string]*kong.Node{}
	if parser.Model.Node.DefaultCmd != nil {
		out["ship"] = parser.Model.Node.DefaultCmd
	}
	collectCommandLeafNodes(parser.Model.Node, nil, out)
	return out
}

func collectCommandLeafNodes(node *kong.Node, path []string, out map[string]*kong.Node) {
	for _, child := range node.Children {
		next := append(append([]string(nil), path...), child.Name)
		if child.Leaf() {
			out[strings.Join(next, " ")] = child
			continue
		}
		collectCommandLeafNodes(child, next, out)
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

func TestWantsJSONErrorStopsAtPassthroughSeparator(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "command argument json", args: []string{"exec", "--", "--json"}},
		{name: "command argument json equals", args: []string{"exec", "--", "--json=pretty"}},
		{name: "global json before command", args: []string{"--json", "exec", "--", "cmd"}, want: true},
		{name: "command json before passthrough", args: []string{"exec", "--json", "--", "cmd"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wantsJSONError(tt.args); got != tt.want {
				t.Fatalf("wantsJSONError(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestErrcatExitCodeSeparatesUsageManifestFromOperations(t *testing.T) {
	if got := utils.ExitCodeForErrorCode(errcat.CodeOperationFailed); got != 1 {
		t.Fatalf("operation exit code = %d, want 1", got)
	}
	if got := utils.ExitCodeForErrorCode(errcat.CodeManifestInvalid); got != 2 {
		t.Fatalf("manifest exit code = %d, want 2", got)
	}
	if got := utils.ExitCodeForErrorCode(errcat.CodeDockerfileMissing); got != 2 {
		t.Fatalf("dockerfile-missing exit code = %d, want 2", got)
	}
	if got := utils.ExitCodeForErrorCode(errcat.CodeMultiProcessNoWebRoute); got != 2 {
		t.Fatalf("multi-process route exit code = %d, want 2", got)
	}
	if got := utils.ExitCodeForErrorCode(errcat.CodeUsageError); got != 2 {
		t.Fatalf("usage exit code = %d, want 2", got)
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

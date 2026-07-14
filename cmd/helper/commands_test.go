package helper

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/fprl/ship/internal/cliargs"
)

func parseServerCommand(t *testing.T, args ...string) *ServerCmd {
	t.Helper()
	previousRequireRoot := requireRoot
	requireRoot = func() error { return nil }
	t.Cleanup(func() { requireRoot = previousRequireRoot })

	cli := &ServerCmd{}
	parser, err := kong.New(cli, kong.Name("ship"))
	if err != nil {
		t.Fatalf("parser setup failed: %v", err)
	}
	if _, err := parser.Parse(args); err != nil {
		t.Fatalf("parse %v failed: %v", args, err)
	}
	return cli
}

func TestServerAppExecPassthroughParserShapes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "command without separator", args: []string{"app", "exec", "api", "production", "sh", "-c", "echo hi"}, want: []string{"sh", "-c", "echo hi"}},
		{name: "separator before command", args: []string{"app", "exec", "api", "production", "--", "sh", "-c", "echo hi"}, want: []string{"sh", "-c", "echo hi"}},
		{name: "separator before dash command", args: []string{"app", "exec", "api", "production", "--", "--flag-first-cmd"}, want: []string{"--flag-first-cmd"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := parseServerCommand(t, tt.args...)
			if got := cliargs.TrimLeadingPassthroughSeparator(parsed.App.Exec.Command); !slices.Equal(got, tt.want) {
				t.Fatalf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServerCLIParsesPrivilegedCommands(t *testing.T) {
	tests := [][]string{
		{"doctor"},
		{"agent-shell", "--member-fingerprint", aliceFingerprint},
		{"doctor", "--member-fingerprint", aliceFingerprint},
		{"doctor", "--json"},
		{"doctor", "--box-target", "example.com", "--json"},
		{"doctor", "record"},
		{"version", "--json", "--summary"},
		{"app", "setup-env", "api", "production"},
		{"app", "--member-fingerprint", aliceFingerprint, "status", "--json", "api", "production"},
		{"app", "preflight", "--secret", "DATABASE_URL", "--json", "api", "production"},
		{"app", "destroy", "api"},
		{"app", "destroy-env", "api", "production"},
		{"app", "destroy-env", "--purge", "api", "production"},
		{"app", "apply", "--tarball", "/tmp/ship-deploy/x.tar", "--manifest", "/tmp/ship-deploy/x.toml", "--sha", "deadbeef", "--base-commit", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "--created-at", "2026-05-30T14:30:12Z", "api", "production"},
		{"app", "preview", "resolve-or-create", "api", "feat/x"},
		{"app", "preview", "resolve", "api", "feat/x"},
		{"app", "preview", "pin", "api", "feat/x"},
		{"app", "preview", "unpin", "api", "feat/x"},
		{"app", "preview", "share", "api", "feat-x-abcd"},
		{"app", "preview", "share", "--rotate", "api", "feat-x-abcd"},
		{"app", "data", "fork", "api", "production", "feat-x-abcd"},
		{"app", "data", "reset", "api", "feat-x-abcd"},
		{"app", "data", "save", "api", "production"},
		{"app", "data", "restore", "--archive", "/tmp/ship-deploy/snapshot.data.tar.gz", "api", "production"},
		{"app", "secret", "set", "api", "production", "DATABASE_URL"},
		{"app", "secret", "list", "api", "production"},
		{"app", "secret", "list", "--json", "api", "production"},
		{"app", "secret", "rm", "api", "production", "DATABASE_URL"},
		{"app", "status", "api", "production"},
		{"app", "status", "--json", "api", "production"},
		{"app", "exec", "api", "production", "--", "env"},
		{"app", "exec", "--tty", "api", "production", "--", "sh", "-c", "exit 7"},
		{"app", "why", "api", "production"},
		{"app", "logs", "api", "production"},
		{"app", "logs", "api", "production", "web"},
		{"app", "logs", "--follow", "api", "production", "web"},
		{"app", "logs", "--tail=50", "api", "production"},
		{"app", "rollback", "api", "production"},
		{"env", "reap"},
		{"key", "add", "--name", "alice"},
		{"key", "--member-fingerprint", aliceFingerprint, "ls"},
		{"key", "add", "--name", "alice", "--role", "owner"},
		{"key", "ls"},
		{"key", "ls", "--json"},
		{"key", "rm", "alice"},
		{"approval", "--member-fingerprint", aliceFingerprint, "list"},
		{"approval", "list", "--json"},
		{"approval", "approve", "abc123xy"},
		{"webhook", "get"},
		{"webhook", "set", "https://ntfy.example/ship"},
		{"webhook", "clear"},
	}

	for _, tt := range tests {
		name := tt[0]
		if len(tt) > 1 {
			name = name + "_" + tt[1]
		}
		t.Run(name, func(t *testing.T) {
			parseServerCommand(t, tt...)
		})
	}
}

func TestAppLogsPodmanArgsIncludesTailInFollowMode(t *testing.T) {
	got := appLogsPodmanArgs(true, 0, "ship-api-production-web")
	want := []string{"logs", "-f", "--tail", "0", "ship-api-production-web"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestWriteBufferedLogsDoesNotAppendFallbackAfterStderr(t *testing.T) {
	var stdout, stderr, gotStdout, gotStderr bytes.Buffer
	stderr.WriteString("app wrote to stderr\n")

	writeBufferedLogs(&stdout, &stderr, &gotStdout, &gotStderr)

	if gotStdout.Len() != 0 || gotStderr.String() != "app wrote to stderr\n" {
		t.Fatalf("stderr-only logs = stdout %q stderr %q", gotStdout.String(), gotStderr.String())
	}
}

func TestServerCLIAppliesMemberFingerprintFlag(t *testing.T) {
	setServerMemberFingerprint("")
	t.Cleanup(func() { setServerMemberFingerprint("") })
	parseServerCommand(t, "app", "--member-fingerprint", aliceFingerprint, "status", "--json", "api", "production")
	if serverMemberFingerprint != aliceFingerprint {
		t.Fatalf("server member fingerprint = %q, want %q", serverMemberFingerprint, aliceFingerprint)
	}
}

func TestServerCLIRejectsRemovedCompatibilityCommands(t *testing.T) {
	tests := [][]string{
		{"status"},
		{"app", "restart", "api", "production"},
		{"app", "preview", "password", "api", "feat-x-abcd"},
		{"app", "share", "api", "feat-x-abcd"},
		{"app", "rollback", "--json", "api", "production"},
		{"app", "backup", "api", "production"},
		{"app", "backup", "create", "api", "production"},
		{"app", "backup", "--json", "api", "production"},
		{"app", "backup", "list", "api", "production"},
		{"app", "backup", "rm", "api", "production", "backup-id"},
		{"app", "backup", "--json", "list", "api", "production"},
		{"app", "backup", "--from", "backup-id", "restore", "api", "production"},
		{"app", "--member", "alice", "status", "api", "production"},
		{"notify", "get"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt, "_"), func(t *testing.T) {
			previousRequireRoot := requireRoot
			requireRoot = func() error { return nil }
			t.Cleanup(func() { requireRoot = previousRequireRoot })

			cli := &ServerCmd{}
			parser, err := kong.New(cli, kong.Name("ship"))
			if err != nil {
				t.Fatalf("parser setup failed: %v", err)
			}
			if _, err := parser.Parse(tt); err == nil {
				t.Fatalf("parse %v unexpectedly succeeded", tt)
			}
		})
	}
}

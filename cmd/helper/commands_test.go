package helper

import (
	"strings"
	"testing"

	"github.com/alecthomas/kong"
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

func TestServerCLIParsesPrivilegedCommands(t *testing.T) {
	tests := [][]string{
		{"status"},
		{"status", "--json"},
		{"doctor"},
		{"doctor", "--json"},
		{"cloudflare", "setup-tunnel", "--name", "ship", "--account-id", "account-test", "--token-file", "/tmp/token"},
		{"cloudflare", "publish", "--app", "api", "api.example.com"},
		{"cloudflare", "remove", "--app", "api"},
		{"app", "setup-env", "api", "production"},
		{"app", "preflight", "--secret", "DATABASE_URL", "--json", "api", "production"},
		{"app", "destroy-env", "api", "production"},
		{"app", "destroy-env", "--purge", "api", "production"},
		{"app", "apply", "--tarball", "/tmp/simple-vps-deploy/x.tar", "--manifest", "/tmp/simple-vps-deploy/x.toml", "--sha", "deadbeef", "--base-commit", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "--created-at", "2026-05-30T14:30:12Z", "api", "production"},
		{"app", "preview", "resolve-or-create", "api", "feat/x"},
		{"app", "preview", "resolve", "api", "feat/x"},
		{"app", "preview", "pin", "api", "feat/x"},
		{"app", "preview", "unpin", "api", "feat/x"},
		{"app", "secret", "set", "api", "production", "DATABASE_URL"},
		{"app", "secret", "list", "api", "production"},
		{"app", "secret", "list", "--json", "api", "production"},
		{"app", "secret", "rm", "api", "production", "DATABASE_URL"},
		{"app", "status", "api", "production"},
		{"app", "status", "--json", "api", "production"},
		{"app", "why", "api", "production"},
		{"app", "why", "--json", "api", "production"},
		{"app", "logs", "api", "production"},
		{"app", "logs", "api", "production", "web"},
		{"app", "logs", "--follow", "api", "production", "web"},
		{"app", "logs", "--tail=50", "api", "production"},
		{"app", "restart", "api", "production"},
		{"app", "restart", "api", "production", "web"},
		{"app", "rollback", "api", "production"},
		{"app", "backup", "create", "api", "production"},
		{"app", "backup", "create", "--json", "api", "production"},
		{"app", "backup", "create", "--to", "/tmp/backups", "api", "production"},
		{"app", "backup", "list", "api", "production"},
		{"app", "backup", "list", "--json", "api", "production"},
		{"app", "backup", "rm", "api", "production", "backup-id"},
		{"app", "backup", "restore", "--from", "backup-id", "api", "production"},
		{"app", "backup", "restore", "--from", "backup-id", "--dry-run", "api", "production"},
		{"env", "reap"},
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

func TestServerCLIRejectsRemovedCompatibilityCommands(t *testing.T) {
	tests := [][]string{
		{"app", "restart", "--json", "api", "production"},
		{"app", "rollback", "--json", "api", "production"},
		{"app", "backup", "api", "production"},
		{"app", "backup", "--json", "api", "production"},
		{"app", "backup", "--json", "list", "api", "production"},
		{"app", "backup", "--from", "backup-id", "restore", "api", "production"},
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

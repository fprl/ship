package helper

import (
	"testing"

	"github.com/alecthomas/kong"
)

func parseServerCommand(t *testing.T, args ...string) *ServerCmd {
	t.Helper()
	previousRequireRoot := requireRoot
	requireRoot = func() error { return nil }
	t.Cleanup(func() { requireRoot = previousRequireRoot })

	cli := &ServerCmd{}
	parser, err := kong.New(cli, kong.Name("simple-vps"))
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
		{"doctor"},
		{"cloudflare", "setup-tunnel", "--name", "simple-vps", "--account-id", "account-test", "--token-file", "/tmp/token"},
		{"cloudflare", "publish", "--app", "api", "api.example.com"},
		{"cloudflare", "remove", "--app", "api"},
		{"app", "setup-env", "api", "production"},
		{"app", "destroy-env", "api", "production"},
		{"app", "destroy-env", "--purge", "api", "production"},
		{"app", "apply", "--tarball", "/tmp/simple-vps-deploy/x.tar", "--manifest", "/tmp/simple-vps-deploy/x.toml", "--sha", "deadbeef", "api", "production"},
		{"app", "secret", "put", "api", "production", "DATABASE_URL"},
		{"app", "secret", "list", "api", "production"},
		{"app", "secret", "rm", "api", "production", "DATABASE_URL"},
		{"app", "status", "api", "production"},
		{"app", "status", "--json", "api", "production"},
		{"app", "logs", "api", "production"},
		{"app", "logs", "api", "production", "web"},
		{"app", "logs", "--follow", "api", "production", "web"},
		{"app", "logs", "--tail=50", "api", "production"},
		{"app", "restart", "api", "production"},
		{"app", "restart", "api", "production", "web"},
		{"app", "restart", "--json", "api", "production"},
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

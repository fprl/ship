package helper

import (
	"reflect"
	"testing"

	"github.com/alecthomas/kong"
)

func parseServerCommand(t *testing.T, args ...string) *serverCLI {
	t.Helper()
	cli := &serverCLI{}
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
		{"route", "list", "--json"},
		{"route", "proxy", "api.example.com", "--port", "3000", "--app", "api", "--header", "X-Test: yes"},
		{"route", "static", "static.example.com", "--root", "/var/apps/api/current", "--app", "api", "--header", "Cache-Control: no-store"},
		{"route", "redirect", "old.example.com", "--to", "https://new.example.com", "--app", "api"},
		{"route", "remove", "--app", "api"},
		{"cloudflare", "setup-tunnel", "--name", "simple-vps", "--account-id", "account-test", "--token-file", "/tmp/token"},
		{"cloudflare", "publish", "--app", "api", "api.example.com"},
		{"cloudflare", "remove", "--app", "api"},
		{"generate-caddy", "--force"},
		{"app", "create", "api"},
		{"app", "destroy", "api"},
		{"app", "read-env", "api"},
		{"app", "install-env", "api", "/tmp/env"},
		{"app", "install-unit", "api", "web", "/tmp/unit"},
		{"app", "uninstall-unit", "api", "web"},
		{"app", "daemon-reload"},
		{"app", "service", "restart", "api", "web"},
		{"app", "run-as", "api", "--cwd", "/var/apps/api/current", "--", "npm", "install"},
		{"publish", "--host", "api.example.com", "--port", "3000"},
		{"unpublish", "--host", "api.example.com"},
		{"routes", "--json"},
	}

	for _, tt := range tests {
		t.Run(tt[0], func(t *testing.T) {
			parseServerCommand(t, tt...)
		})
	}
}

func TestAppRunAsCommandIsPassThrough(t *testing.T) {
	cli := parseServerCommand(
		t,
		"app", "run-as", "api",
		"--cwd", "/var/apps/api/current",
		"--",
		"npm", "install", "--omit=dev",
	)

	want := []string{"--", "npm", "install", "--omit=dev"}
	if !reflect.DeepEqual(cli.App.RunAs.Command, want) {
		t.Fatalf("unexpected run-as command:\nwant: %#v\n got: %#v", want, cli.App.RunAs.Command)
	}
}

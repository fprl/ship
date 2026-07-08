package helper

import (
	"errors"
	"os"
)

var requireRoot = func() error {
	if os.Geteuid() != 0 {
		return errors.New("this command must run as root")
	}
	return nil
}

type ServerCmd struct {
	AgentShell agentShellCmd `cmd:"agent-shell" hidden:"" help:"Forced-command SSH gate for agent members."`
	Doctor     doctorCmd     `cmd:"" help:"Run host diagnostics."`
	Cloudflare cloudflareCmd `cmd:"" help:"Manage Cloudflare Tunnel ingress."`
	App        appCmd        `cmd:"" help:"Manage app users, files, and processes."`
	Env        envCmd        `cmd:"" help:"Manage host environments."`
	Key        keyCmd        `cmd:"" help:"Manage deploy SSH keys."`
	Approval   approvalCmd   `cmd:"" help:"Manage one-shot role approvals."`
}

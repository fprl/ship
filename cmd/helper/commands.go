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
	AgentShell  agentShellCmd    `cmd:"agent-shell" hidden:"" help:"Forced-command SSH gate for agent members."`
	Doctor      doctorCmd        `cmd:"" help:"Run host diagnostics."`
	ConvergeBoot convergeBootCmd `cmd:"converge-boot" hidden:"" help:"Converge every app environment at boot."`
	GC          gcCmd            `cmd:"gc" help:"Apply release retention garbage collection."`
	App         appCmd           `cmd:"" help:"Manage app users, files, and processes."`
	Env         envCmd           `cmd:"" help:"Manage host environments."`
	Key         keyCmd           `cmd:"" help:"Manage deploy SSH keys."`
	Approval    approvalCmd      `cmd:"" help:"Manage one-shot role approvals."`
	Config      boxConfigCmd     `cmd:"" help:"Manage box configuration."`
	Webhook     webhookCmd       `cmd:"" help:"Manage the box webhook."`
	Version     versionHelperCmd `cmd:"" help:"Print the helper version."`
	Update      updateHelperCmd  `cmd:"" help:"Converge version-owned box artifacts."`
	UpdateLocal updateLocalCmd   `cmd:"update-local" hidden:""`
}

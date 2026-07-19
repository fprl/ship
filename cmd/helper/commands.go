package helper

import (
	"errors"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/remoteprotocol"
	"github.com/fprl/ship/internal/version"
)

var requireRoot = func() error {
	if os.Geteuid() != 0 {
		return errors.New("this command must run as root")
	}
	return nil
}

type ServerCmd struct {
	ClientVersion string           `name:"client-version" hidden:"" help:"Exact local Ship version for a remote request."`
	Internal      bool             `name:"internal" hidden:"" help:"Mark a box-local scheduled command."`
	AgentShell    agentShellCmd    `cmd:"agent-shell" hidden:"" help:"Forced-command SSH gate for agent members."`
	Doctor        doctorCmd        `cmd:"" help:"Run host diagnostics."`
	ConvergeBoot  convergeBootCmd  `cmd:"converge-boot" hidden:"" help:"Converge every app environment at boot."`
	GC            gcCmd            `cmd:"gc" help:"Apply release retention garbage collection."`
	App           appCmd           `cmd:"" help:"Manage app users, files, and processes."`
	Env           envCmd           `cmd:"" help:"Manage host environments."`
	Key           keyCmd           `cmd:"" help:"Manage deploy SSH keys."`
	Approval      approvalCmd      `cmd:"" help:"Manage one-shot role approvals."`
	Config        boxConfigCmd     `cmd:"" help:"Manage box configuration."`
	Webhook       webhookCmd       `cmd:"" help:"Manage the box webhook."`
	Version       versionHelperCmd `cmd:"" help:"Print the helper version."`
	Update        updateHelperCmd  `cmd:"" help:"Converge version-owned box artifacts."`
	UpdateLocal   updateLocalCmd   `cmd:"update-local" hidden:""`
}

func (c *ServerCmd) AfterApply(ctx *kong.Context) error {
	command := strings.Fields(ctx.Command())
	if len(command) > 0 && command[0] == "server" {
		command = command[1:]
	}
	if len(command) == 0 {
		return nil
	}
	namespace := command[0]
	if remoteprotocol.RepairNamespaceAllowed(namespace) {
		return nil
	}
	if c.Internal {
		if serverInternalCommandAllowed(namespace, c) {
			return nil
		}
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "--internal is only valid for box-owned scheduled commands",
			"command": "ship box doctor",
		})
	}
	if !remoteprotocol.ClientNamespaceAllowed(namespace) {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "server command is not available to remote clients",
			"command": "ship help",
		})
	}
	return remoteprotocol.RequireExactVersion(c.ClientVersion, version.Version, "<box>")
}

func serverInternalCommandAllowed(namespace string, c *ServerCmd) bool {
	switch namespace {
	case "converge-boot", "gc":
		return true
	case "env":
		return true
	case "doctor":
		return c.Doctor.Action == "record"
	default:
		return false
	}
}

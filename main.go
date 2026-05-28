package main

import (
	"fmt"

	"github.com/alecthomas/kong"
	"github.com/fprl/simple-vps/cmd/client"
	"github.com/fprl/simple-vps/cmd/helper"
	"github.com/fprl/simple-vps/cmd/hostinstall"
	"github.com/fprl/simple-vps/internal/version"
)

// Public CLI surface. The post-cutover lifecycle is minimal on
// purpose; verbs that depended on the legacy systemd-unit /
// releases/<sha> / per-app env file model are removed and will be
// reintroduced against the new container/podman flow as that work
// lands.
type cli struct {
	Init    initCmd          `cmd:"" help:"Create a simple-vps.toml manifest and Dockerfile scaffold."`
	Check   checkCmd         `cmd:"" help:"Validate an app manifest."`
	Setup   setupCmd         `cmd:"" help:"Create the per-env Linux user, directories, and Podman network on the host."`
	Deploy  deployCmd        `cmd:"" help:"Build the container image on the host and run the app's services."`
	Status  statusCmd        `cmd:"" help:"Show running services for an environment."`
	Restart restartCmd       `cmd:"" help:"Restart services for an environment (bounces in place; same image)."`
	Destroy destroyCmd       `cmd:"" help:"Destroy an app environment on the host."`
	Logs    logsCmd          `cmd:"" help:"Tail logs for one service."`
	App     appCmd           `cmd:"" help:"Inspect apps on a host."`
	Secret  secretCmd        `cmd:"" help:"Manage per-(app, env, key) secret values referenced from the manifest."`
	SSH     sshCmd           `cmd:"ssh" help:"Open an SSH session to an app environment."`
	Host    hostCmd          `cmd:"" help:"Install or inspect a Simple VPS host."`
	Version versionCmd       `cmd:"" help:"Print the Simple VPS version."`
	Server  helper.ServerCmd `cmd:"" hidden:"" help:"Privileged host API."`
}

type versionCmd struct{}

func (versionCmd) Run() error {
	fmt.Println(version.Version)
	return nil
}

type initCmd struct{}

func (initCmd) Run() error {
	client.CmdInit(".")
	return nil
}

type checkCmd struct {
	Env string `arg:"" optional:"" help:"Environment to validate."`
}

func (c checkCmd) Run() error {
	client.CmdCheck(".", c.Env)
	return nil
}

type setupCmd struct {
	Env string `arg:"" help:"Environment to set up."`
}

func (c setupCmd) Run() error {
	client.CmdSetup(".", c.Env)
	return nil
}

type deployCmd struct {
	Env           string `arg:"" help:"Environment to deploy."`
	Dirty         bool   `help:"Allow deploying a dirty worktree."`
	IncludeDotenv bool   `name:"include-dotenv" help:"Allow deploying dotenv files."`
}

func (c deployCmd) Run() error {
	client.CmdDeploy(".", c.Env, c.Dirty, c.IncludeDotenv)
	return nil
}

type sshCmd struct {
	Env string `arg:"" help:"Environment to connect to."`
}

func (c sshCmd) Run() error {
	client.CmdSSH(".", c.Env)
	return nil
}

type statusCmd struct {
	Env  string `arg:"" help:"Environment to inspect."`
	JSON bool   `name:"json" help:"Emit structured JSON instead of the text table."`
}

func (c statusCmd) Run() error {
	client.CmdStatus(".", c.Env, c.JSON)
	return nil
}

type logsCmd struct {
	Env     string `arg:"" help:"Environment containing the service."`
	Service string `arg:"" optional:"" help:"Service name. Optional when only one service runs."`
	Follow  bool   `name:"follow" short:"f" help:"Stream new log lines."`
	Tail    int    `name:"tail" default:"100" help:"How many trailing lines to show. Ignored in --follow mode."`
}

func (c logsCmd) Run() error {
	client.CmdLogs(".", c.Env, c.Service, c.Follow, c.Tail)
	return nil
}

type appCmd struct {
	List appListCmd `cmd:"" help:"List app environments visible on a host."`
}

type appListCmd struct {
	Server string `help:"SSH target like deploy@example.com. If omitted, inferred from a single-env manifest."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text table."`
}

func (c appListCmd) Run() error {
	client.CmdAppList(".", c.Server, c.JSON)
	return nil
}

type restartCmd struct {
	Env     string `arg:"" help:"Environment to restart."`
	Service string `arg:"" optional:"" help:"Service to bounce. Omitted = all services."`
	JSON    bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c restartCmd) Run() error {
	client.CmdRestart(".", c.Env, c.Service, c.JSON)
	return nil
}

type destroyCmd struct {
	Env     string `arg:"" help:"Environment to destroy."`
	Confirm string `name:"confirm" help:"Required app-name confirmation unless --yes is passed."`
	Yes     bool   `name:"yes" help:"Skip confirmation. Intended for automation."`
	Purge   bool   `name:"purge" help:"Also delete secrets for this app/env."`
}

func (c destroyCmd) Run() error {
	client.CmdDestroy(".", c.Env, c.Confirm, c.Yes, c.Purge)
	return nil
}

type secretCmd struct {
	Put  secretPutCmd  `cmd:"" help:"Read a secret value from stdin and store it on the host."`
	List secretListCmd `cmd:"" help:"List secret keys for an environment (keys only; values are never printed)."`
	Rm   secretRmCmd   `cmd:"rm" help:"Remove a secret key from an environment."`
}

type secretPutCmd struct {
	Env string `arg:"" help:"Environment to write the secret into."`
	Key string `arg:"" help:"Env-var name (e.g., DATABASE_URL)."`
}

func (c secretPutCmd) Run() error {
	client.CmdSecretPut(".", c.Env, c.Key)
	return nil
}

type secretListCmd struct {
	Env  string `arg:"" help:"Environment to list."`
	JSON bool   `name:"json" help:"Emit structured JSON instead of plain key lines."`
}

func (c secretListCmd) Run() error {
	client.CmdSecretList(".", c.Env, c.JSON)
	return nil
}

type secretRmCmd struct {
	Env string `arg:"" help:"Environment to update."`
	Key string `arg:"" help:"Env-var name to remove."`
}

func (c secretRmCmd) Run() error {
	client.CmdSecretRm(".", c.Env, c.Key)
	return nil
}

type hostCmd struct {
	Status  hostStatusCmd  `cmd:"" default:"1" help:"Show host status."`
	Doctor  hostDoctorCmd  `cmd:"" help:"Run host diagnostics."`
	Install hostInstallCmd `cmd:"" help:"Install or converge a host."`
}

type hostStatusCmd struct {
	Server string `help:"SSH target like deploy@example.com."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c hostStatusCmd) Run() error {
	args := []string{"status"}
	if c.JSON {
		args = append(args, "--json")
	}
	if c.Server != "" {
		args = append(args, "--server", c.Server)
	}
	client.CmdHost(args)
	return nil
}

type hostDoctorCmd struct {
	Server string `help:"SSH target like deploy@example.com."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c hostDoctorCmd) Run() error {
	args := []string{"doctor"}
	if c.JSON {
		args = append(args, "--json")
	}
	if c.Server != "" {
		args = append(args, "--server", c.Server)
	}
	client.CmdHost(args)
	return nil
}

type hostInstallCmd struct {
	Mode                     string `enum:"auto,local,remote" default:"auto" help:"Execution mode."`
	TargetHost               string `name:"host" help:"Target VPS host for remote mode."`
	BootstrapUser            string `help:"SSH user for remote bootstrap."`
	SSHKey                   string `name:"ssh-key" help:"SSH private key for remote mode."`
	SSHPublicKeyFile         string `name:"ssh-public-key-file" help:"SSH public key file for operator access."`
	OperatorSSHPublicKeyFile string `help:"SSH public key file for operator access."`
	DeploySSHPublicKeyFile   string `help:"SSH public key file for deploy access."`
	SharedKey                bool   `help:"Reuse operator SSH key for deploy."`
	OperatorUser             string `help:"Operator user."`
	DeployUser               string `help:"Deploy user."`
	Timezone                 string `help:"Host timezone."`
	Locale                   string `help:"Host locale."`
	Tailscale                *bool  `negatable:"" help:"Install and configure Tailscale."`
	TailscaleAuthKey         string `help:"Tailscale auth key."`
	TailscaleHostname        string `help:"Tailscale hostname."`
	CloudflareTunnel         *bool  `negatable:"" help:"Install and configure Cloudflare Tunnel."`
	CloudflareAPIToken       string `help:"Cloudflare API token."`
	CloudflareAccountID      string `help:"Cloudflare account ID."`
	CloudflareTunnelToken    string `help:"Cloudflare tunnel token."`
	CloudflareTunnelConfig   string `help:"Cloudflare tunnel config path."`
	InstallDocker            *bool  `name:"docker" negatable:"" help:"Install Docker."`
	InstallLitestream        *bool  `name:"litestream" negatable:"" help:"Install Litestream."`
	CheckMode                bool   `name:"check" help:"Plan changes without writing files or running mutating commands."`
	AssumeYes                bool   `name:"yes" help:"Non-interactive mode."`
}

func (c hostInstallCmd) Run() error {
	opts := hostinstall.DefaultOptions(nil)
	if c.Mode != "" {
		opts.Mode = c.Mode
	}
	if c.TargetHost != "" {
		opts.TargetHost = c.TargetHost
	}
	if c.BootstrapUser != "" {
		opts.BootstrapUser = c.BootstrapUser
	}
	if c.SSHKey != "" {
		opts.SSHKey = c.SSHKey
	}
	if c.SSHPublicKeyFile != "" {
		opts.SSHPublicKeyFile = c.SSHPublicKeyFile
	}
	if c.OperatorSSHPublicKeyFile != "" {
		opts.OperatorSSHPublicKeyFile = c.OperatorSSHPublicKeyFile
	}
	if c.DeploySSHPublicKeyFile != "" {
		opts.DeploySSHPublicKeyFile = c.DeploySSHPublicKeyFile
	}
	if c.OperatorUser != "" {
		opts.OperatorUser = c.OperatorUser
	}
	if c.DeployUser != "" {
		opts.DeployUser = c.DeployUser
	}
	if c.Timezone != "" {
		opts.Timezone = c.Timezone
	}
	if c.Locale != "" {
		opts.Locale = c.Locale
	}
	if c.Tailscale != nil {
		opts.Tailscale = *c.Tailscale
	}
	if c.TailscaleAuthKey != "" {
		opts.TailscaleAuthKey = c.TailscaleAuthKey
	}
	if c.TailscaleHostname != "" {
		opts.TailscaleHostname = c.TailscaleHostname
	}
	if c.CloudflareTunnel != nil {
		opts.CloudflareTunnel = *c.CloudflareTunnel
	}
	if c.CloudflareAPIToken != "" {
		opts.CloudflareAPIToken = c.CloudflareAPIToken
	}
	if c.CloudflareAccountID != "" {
		opts.CloudflareAccountID = c.CloudflareAccountID
	}
	if c.CloudflareTunnelToken != "" {
		opts.CloudflareTunnelToken = c.CloudflareTunnelToken
	}
	if c.CloudflareTunnelConfig != "" {
		opts.CloudflareTunnelConfig = c.CloudflareTunnelConfig
	}
	if c.InstallDocker != nil {
		opts.InstallDocker = *c.InstallDocker
	}
	if c.InstallLitestream != nil {
		opts.InstallLitestream = *c.InstallLitestream
	}
	opts.SharedKey = c.SharedKey
	opts.CheckMode = c.CheckMode
	opts.AssumeYes = c.AssumeYes
	return hostinstall.NewInstaller().RunOptions(opts)
}

func main() {
	parser := kong.Parse(
		&cli{},
		kong.Name("simple-vps"),
		kong.Description("Deploy containerized apps to a single hardened VPS."),
		kong.UsageOnError(),
	)
	parser.FatalIfErrorf(parser.Run())
}

package main

import (
	"github.com/alecthomas/kong"
	"github.com/fprl/simple-vps/cmd/client"
	"github.com/fprl/simple-vps/cmd/helper"
	"github.com/fprl/simple-vps/cmd/hostinstall"
)

type cli struct {
	Init     initCmd          `cmd:"" help:"Create a simple-vps.toml manifest."`
	Check    checkCmd         `cmd:"" help:"Validate an app manifest."`
	Setup    setupCmd         `cmd:"" help:"Create the app user and directories on a host."`
	Deploy   deployCmd        `cmd:"" help:"Deploy an app release."`
	Rollback rollbackCmd      `cmd:"" help:"Rollback an app to a previous release."`
	Destroy  destroyCmd       `cmd:"" help:"Destroy app services, routes, and optionally app data."`
	Restart  restartCmd       `cmd:"" help:"Restart one app service."`
	Status   statusCmd        `cmd:"" help:"Show app status."`
	Logs     logsCmd          `cmd:"" help:"Show app service logs."`
	SSH      sshCmd           `cmd:"ssh" help:"Open an SSH session to an app environment."`
	Secret   secretCmd        `cmd:"" help:"Manage remote app secrets."`
	Env      envCmd           `cmd:"" help:"Manage remote app environment files."`
	Host     hostCmd          `cmd:"" help:"Install or inspect a Simple VPS host."`
	Route    routeCmd         `cmd:"" help:"Inspect routes from a laptop or CI runner."`
	Server   helper.ServerCmd `cmd:"" hidden:"" help:"Privileged host API."`
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

type rollbackCmd struct {
	Env     string `arg:"" help:"Environment to roll back."`
	Release string `arg:"" optional:"" help:"Release id to activate. Defaults to previous release."`
}

func (c rollbackCmd) Run() error {
	client.CmdRollback(".", c.Env, c.Release)
	return nil
}

type destroyCmd struct {
	Env     string `arg:"" help:"Environment to destroy."`
	Yes     bool   `help:"Confirm destruction."`
	Confirm string `help:"Confirm the app name."`
	Purge   bool   `help:"Remove app data after stopping services and routes."`
}

func (c destroyCmd) Run() error {
	client.CmdDestroy(".", c.Env, c.Yes, c.Confirm, c.Purge)
	return nil
}

type restartCmd struct {
	Env     string `arg:"" help:"Environment containing the service."`
	Service string `arg:"" help:"Service name to restart."`
}

func (c restartCmd) Run() error {
	client.CmdRestart(".", c.Env, c.Service)
	return nil
}

type statusCmd struct {
	Env string `arg:"" help:"Environment to inspect."`
}

func (c statusCmd) Run() error {
	client.CmdStatus(".", c.Env)
	return nil
}

type logsCmd struct {
	Env     string `arg:"" help:"Environment containing the service."`
	Service string `arg:"" optional:"" help:"Optional service name."`
	Tail    bool   `help:"Follow logs."`
}

func (c logsCmd) Run() error {
	client.CmdLogs(".", c.Env, c.Service, c.Tail)
	return nil
}

type sshCmd struct {
	Env string `arg:"" help:"Environment to connect to."`
}

func (c sshCmd) Run() error {
	client.CmdSSH(".", c.Env)
	return nil
}

type secretCmd struct {
	Put  secretPutCmd  `cmd:"" help:"Set a secret value from stdin."`
	List secretListCmd `cmd:"" help:"List secret keys."`
	Rm   secretRmCmd   `cmd:"rm" help:"Remove a secret key."`
}

type secretPutCmd struct {
	Env string `arg:"" help:"Environment to update."`
	Key string `arg:"" help:"Secret key."`
}

func (c secretPutCmd) Run() error {
	client.CmdSecretPut(".", c.Env, c.Key)
	return nil
}

type secretListCmd struct {
	Env string `arg:"" help:"Environment to inspect."`
}

func (c secretListCmd) Run() error {
	client.CmdSecretList(".", c.Env)
	return nil
}

type secretRmCmd struct {
	Env string `arg:"" help:"Environment to update."`
	Key string `arg:"" help:"Secret key."`
}

func (c secretRmCmd) Run() error {
	client.CmdSecretRm(".", c.Env, c.Key)
	return nil
}

type envCmd struct {
	Push envPushCmd `cmd:"" help:"Push a dotenv file to the remote app."`
}

type envPushCmd struct {
	Env  string `arg:"" help:"Environment to update."`
	File string `arg:"" help:"Dotenv file to upload."`
}

func (c envPushCmd) Run() error {
	client.CmdEnvPush(".", c.Env, c.File)
	return nil
}

type hostCmd struct {
	Status  hostStatusCmd  `cmd:"" default:"1" help:"Show host status."`
	Doctor  hostDoctorCmd  `cmd:"" help:"Run host diagnostics."`
	Install hostInstallCmd `cmd:"" help:"Install or converge a host."`
}

type hostStatusCmd struct {
	Server string `help:"SSH target like deploy@example.com."`
}

func (c hostStatusCmd) Run() error {
	args := []string{"status"}
	if c.Server != "" {
		args = append(args, "--server", c.Server)
	}
	client.CmdHost(args)
	return nil
}

type hostDoctorCmd struct {
	Server string `help:"SSH target like deploy@example.com."`
}

func (c hostDoctorCmd) Run() error {
	args := []string{"doctor"}
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

type routeCmd struct {
	List routeListCmd `cmd:"" default:"1" help:"List configured routes."`
}

type routeListCmd struct {
	JSON   bool   `name:"json" help:"Output JSON."`
	Server string `help:"SSH target like deploy@example.com."`
}

func (c routeListCmd) Run() error {
	args := []string{"list"}
	if c.JSON {
		args = append(args, "--json")
	}
	if c.Server != "" {
		args = append(args, "--server", c.Server)
	}
	client.CmdRoute(args)
	return nil
}

func main() {
	parser := kong.Parse(
		&cli{},
		kong.Name("simple-vps"),
		kong.Description("Deploy JS/TS apps to a VPS and manage the host runtime."),
		kong.UsageOnError(),
	)
	parser.FatalIfErrorf(parser.Run())
}

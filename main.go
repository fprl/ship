package main

import (
	"fmt"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/fprl/simple-vps/cmd/client"
	"github.com/fprl/simple-vps/cmd/helper"
	"github.com/fprl/simple-vps/cmd/hostinstall"
	"github.com/fprl/simple-vps/internal/version"
)

// Public CLI surface. The post-cutover lifecycle is minimal on
// purpose; host mutation goes through the privileged helper and runtime
// truth comes from manifest snapshots, identity files, and Podman labels.
type cli struct {
	Init     initCmd          `cmd:"" help:"Create a simple-vps.toml manifest and starter files."`
	Check    checkCmd         `cmd:"" help:"Validate an app manifest."`
	Setup    setupCmd         `cmd:"" help:"Create the per-env Linux user, directories, and Podman network on the host."`
	Deploy   deployCmd        `cmd:"" help:"Build the container image on the host and run the app's processes."`
	Status   statusCmd        `cmd:"" help:"Show running processes for an environment."`
	Restart  restartCmd       `cmd:"" help:"Restart processes for an environment (bounces in place; same image)."`
	Rollback rollbackCmd      `cmd:"" help:"Run an older image or static release for an environment."`
	Backup   backupCmd        `cmd:"" help:"Back up app data, manifest, and secrets."`
	Restore  restoreCmd       `cmd:"" help:"Restore app data, manifest, and secrets from a backup."`
	Destroy  destroyCmd       `cmd:"" help:"Destroy an app environment on the host."`
	Logs     logsCmd          `cmd:"" help:"Tail logs for one process."`
	App      appCmd           `cmd:"" help:"Inspect apps on a host."`
	Secret   secretCmd        `cmd:"" help:"Manage per-(app, env, key) secret values referenced from the manifest."`
	SSH      sshCmd           `cmd:"ssh" help:"Open an SSH session to an app environment."`
	Host     hostCmd          `cmd:"" help:"Install or inspect a Simple VPS host."`
	Version  versionCmd       `cmd:"" help:"Print the Simple VPS version."`
	Server   helper.ServerCmd `cmd:"" hidden:"" help:"Privileged host API."`
}

type versionCmd struct{}

func (versionCmd) Run() error {
	fmt.Println(version.Version)
	return nil
}

func appRoot(configPath string) (string, error) {
	if configPath == "" {
		configPath = client.ManifestFile
	}
	cleaned := filepath.Clean(configPath)
	if filepath.Base(cleaned) != client.ManifestFile {
		return "", fmt.Errorf("--config must point to %s", client.ManifestFile)
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", err
	}
	return filepath.Dir(abs), nil
}

type initCmd struct {
	Config   string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Template string `name:"template" enum:"container,static,php,hono" default:"container" help:"Scaffold template."`
	Name     string `name:"name" help:"App name. Defaults to package.json name or directory name."`
	Env      string `name:"env" short:"e" default:"production" help:"Environment block to create."`
	Server   string `name:"server" default:"deploy@example.com" help:"SSH target for the env."`
	Host     string `name:"host" help:"Route host. Defaults to <app>.example.com."`
	TLS      string `name:"tls" enum:"auto,internal" default:"auto" help:"Route TLS mode."`
	Port     int    `name:"port" help:"Internal process port for container templates."`
}

func (c initCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdInit(root, client.InitOptions{
		Template: c.Template,
		Name:     c.Name,
		Env:      c.Env,
		Server:   c.Server,
		Host:     c.Host,
		TLS:      c.TLS,
		Port:     c.Port,
	})
	return nil
}

type checkCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Env    string `name:"env" short:"e" help:"Environment to validate. Omit to validate all envs."`
}

func (c checkCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdCheck(root, c.Env)
	return nil
}

type setupCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to set up."`
}

func (c setupCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSetup(root, c.Env)
	return nil
}

type deployCmd struct {
	Config        string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Env           string `name:"env" short:"e" required:"" help:"Environment to deploy."`
	Dirty         bool   `help:"Allow deploying a dirty worktree."`
	Rebuild       bool   `help:"Refresh base images and bypass Podman's build cache."`
	IncludeDotenv bool   `name:"include-dotenv" help:"Include .env-style files in the uploaded release artifact."`
}

func (c deployCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdDeploy(root, c.Env, c.Dirty, c.Rebuild, c.IncludeDotenv)
	return nil
}

type sshCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to connect to."`
}

func (c sshCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSSH(root, c.Env)
	return nil
}

type statusCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to inspect."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text table."`
}

func (c statusCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdStatus(root, c.Env, c.JSON)
	return nil
}

type logsCmd struct {
	Config  string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Process string `arg:"" optional:"" help:"Process name. Optional when only one process runs."`
	Env     string `name:"env" short:"e" required:"" help:"Environment containing the process."`
	Follow  bool   `name:"follow" short:"f" help:"Stream new log lines."`
	Tail    int    `name:"tail" default:"100" help:"How many trailing lines to show. Ignored in --follow mode."`
}

func (c logsCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdLogs(root, c.Env, c.Process, c.Follow, c.Tail)
	return nil
}

type appCmd struct {
	List appListCmd `cmd:"" help:"List app environments visible on a host."`
}

type appListCmd struct {
	Server string `name:"server" required:"" help:"SSH target like deploy@example.com."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text table."`
}

func (c appListCmd) Run() error {
	client.CmdAppList(c.Server, c.JSON)
	return nil
}

type restartCmd struct {
	Config  string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Process string `arg:"" optional:"" help:"Process to bounce. Omitted = all processes."`
	Env     string `name:"env" short:"e" required:"" help:"Environment to restart."`
}

func (c restartCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdRestart(root, c.Env, c.Process)
	return nil
}

type rollbackCmd struct {
	Config  string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Release string `arg:"" optional:"" help:"Release to run. Omitted = previous local release."`
	Env     string `name:"env" short:"e" required:"" help:"Environment to roll back."`
}

func (c rollbackCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdRollback(root, c.Env, c.Release)
	return nil
}

type backupCmd struct {
	Create backupCreateCmd `cmd:"" help:"Create a backup for one environment."`
	List   backupListCmd   `cmd:"" help:"List backups for one environment."`
	Rm     backupRmCmd     `cmd:"rm" help:"Remove one backup."`
}

type backupCreateCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to back up."`
	To     string `name:"to" help:"Destination directory on the host. Supports plain paths and file:// URLs."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c backupCreateCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdBackup(root, c.Env, c.To, c.JSON)
	return nil
}

type backupListCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to list backups for."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of plain backup IDs."`
}

func (c backupListCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdBackupList(root, c.Env, c.JSON)
	return nil
}

type backupRmCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	ID     string `arg:"" help:"Backup ID to remove."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to remove a backup from."`
}

func (c backupRmCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdBackupRm(root, c.Env, c.ID)
	return nil
}

type restoreCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	From   string `name:"from" required:"" help:"Backup ID or path on the host."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to restore."`
	DryRun bool   `name:"dry-run" help:"Show what would be restored without writing."`
}

func (c restoreCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdRestore(root, c.Env, c.From, c.DryRun)
	return nil
}

type destroyCmd struct {
	Config  string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Env     string `name:"env" short:"e" required:"" help:"Environment to destroy."`
	App     string `name:"app" help:"App name. Required with --server when the env is no longer in simple-vps.toml."`
	Server  string `name:"server" help:"SSH target like deploy@example.com. Required with --app when the env is no longer in simple-vps.toml."`
	Confirm string `name:"confirm" help:"Required app-name confirmation unless --yes is passed."`
	Yes     bool   `name:"yes" help:"Skip confirmation. Intended for automation."`
	Purge   bool   `name:"purge" help:"Also delete secrets for this app/env."`
}

func (c destroyCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdDestroy(root, c.Env, c.Confirm, c.Yes, c.Purge, c.App, c.Server)
	return nil
}

type secretCmd struct {
	Set  secretSetCmd  `cmd:"" help:"Read a secret value from stdin and store it on the host."`
	List secretListCmd `cmd:"" help:"List secret keys for an environment (keys only; values are never printed)."`
	Rm   secretRmCmd   `cmd:"rm" help:"Remove a secret key from an environment."`
}

type secretSetCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Key    string `arg:"" help:"Env-var name (e.g., DATABASE_URL)."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to write the secret into."`
}

func (c secretSetCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSecretSet(root, c.Env, c.Key)
	return nil
}

type secretListCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to list."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of plain key lines."`
}

func (c secretListCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSecretList(root, c.Env, c.JSON)
	return nil
}

type secretRmCmd struct {
	Config string `name:"config" type:"path" default:"simple-vps.toml" help:"Path to simple-vps.toml."`
	Key    string `arg:"" help:"Env-var name to remove."`
	Env    string `name:"env" short:"e" required:"" help:"Environment to update."`
}

func (c secretRmCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSecretRm(root, c.Env, c.Key)
	return nil
}

type hostCmd struct {
	Status  hostStatusCmd  `cmd:"" default:"1" help:"Show host status."`
	Doctor  hostDoctorCmd  `cmd:"" help:"Run host diagnostics."`
	Install hostInstallCmd `cmd:"" help:"Install or converge a host."`
}

type hostStatusCmd struct {
	Server string `name:"server" required:"" help:"SSH target like deploy@example.com."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c hostStatusCmd) Run() error {
	client.CmdHostStatus(c.Server, c.JSON)
	return nil
}

type hostDoctorCmd struct {
	Server string `name:"server" required:"" help:"SSH target like deploy@example.com."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c hostDoctorCmd) Run() error {
	client.CmdHostDoctor(c.Server, c.JSON)
	return nil
}

type hostInstallCmd struct {
	Mode                     string `enum:"auto,local,remote" default:"auto" help:"Execution mode."`
	TargetHost               string `name:"host" help:"Target VPS host for remote mode."`
	BootstrapUser            string `help:"SSH user for remote bootstrap."`
	SSHKey                   string `name:"ssh-key" help:"SSH private key for remote mode."`
	OperatorSSHPublicKeyFile string `help:"SSH public key file for operator access."`
	DeploySSHPublicKeyFile   string `help:"SSH public key file for deploy access."`
	SharedKey                bool   `help:"Reuse operator SSH key for deploy."`
	OperatorUser             string `help:"Operator user."`
	DeployUser               string `help:"Deploy user."`
	Timezone                 string `help:"Host timezone."`
	Locale                   string `help:"Host locale."`
	Ingress                  string `help:"Ingress mode: public, cloudflare, or private."`
	Admin                    string `help:"Admin access mode: public-ssh or tailscale."`
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
	if c.Ingress != "" {
		opts.Ingress = c.Ingress
	}
	if c.Admin != "" {
		opts.Admin = c.Admin
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

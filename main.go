package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/fprl/ship/cmd/client"
	"github.com/fprl/ship/cmd/helper"
	"github.com/fprl/ship/cmd/hostinstall"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
)

// Public CLI surface. The post-cutover lifecycle is minimal on
// purpose; host mutation goes through the privileged helper and runtime
// truth comes from manifest snapshots, identity files, and Podman labels.
type cli struct {
	Ship     shipCmd          `cmd:"" default:"withargs" hidden:"" group:"project" help:"Deploy the current branch."`
	Init     initCmd          `cmd:"" group:"project" help:"Create local project files and a ship.toml manifest."`
	Status   statusCmd        `cmd:"" group:"project" help:"Show all live environments for this app."`
	Logs     logsCmd          `cmd:"" group:"project" help:"Tail logs for the current branch environment."`
	Exec     execCmd          `cmd:"" group:"project" help:"Run a one-off command in the current branch environment."`
	Why      whyCmd           `cmd:"" group:"project" help:"Explain the latest deploy outcome for the current branch environment."`
	Rollback rollbackCmd      `cmd:"" group:"project" help:"Roll back the current branch environment."`
	Rm       rmCmd            `cmd:"rm" group:"project" help:"Remove an environment by branch name."`
	Pin      pinCmd           `cmd:"" group:"project" help:"Pin a preview environment so the reaper leaves it running."`
	Unpin    unpinCmd         `cmd:"" group:"project" help:"Unpin a preview environment so normal expiry applies."`
	Save     saveCmd          `cmd:"" group:"project" help:"Create a backup for the current branch environment."`
	Restore  restoreCmd       `cmd:"" group:"project" help:"Restore the current branch environment from a backup."`
	SSH      sshCmd           `cmd:"ssh" group:"project" help:"Open an SSH session to the box."`
	Secret   secretCmd        `cmd:"" group:"project" help:"Manage secrets for the current branch environment."`
	Box      boxCmd           `cmd:"" group:"host" help:"Install or inspect a ship box."`
	Docs     docsCmd          `cmd:"" group:"global" help:"Print the agent contract."`
	Help     helpCmd          `cmd:"" group:"global" help:"Show usage for one verb."`
	Version  versionCmd       `cmd:"" group:"global" help:"Print the ship version."`
	Server   helper.ServerCmd `cmd:"" hidden:"" group:"global" help:"Privileged host API."`
}

func cliCommandGroups() []kong.Group {
	return []kong.Group{
		{Key: "project", Title: "Project commands:"},
		{Key: "host", Title: "Host commands:"},
		{Key: "global", Title: "Global commands:"},
	}
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
		return "", errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  fmt.Sprintf("--config must point to %s", client.ManifestFile),
			"command": "ship --config path/to/" + client.ManifestFile,
		})
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", err
	}
	return filepath.Dir(abs), nil
}

func projectAppRoot(configPath string) (string, error) {
	root, err := appRoot(configPath)
	if err != nil {
		return "", err
	}
	manifest := filepath.Join(root, client.ManifestFile)
	info, err := os.Stat(manifest)
	if os.IsNotExist(err) {
		return "", errcat.New(errcat.CodeManifestInvalid, errcat.Fields{
			"details": fmt.Sprintf("this is a project command, but %s was not found.\nRun it from a directory containing %s, or pass --config path/to/%s.", manifest, client.ManifestFile, client.ManifestFile),
			"command": "ship init",
		})
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  fmt.Sprintf("--config must point to %s, got directory %s", client.ManifestFile, manifest),
			"command": "ship --config path/to/" + client.ManifestFile,
		})
	}
	return root, nil
}

type initCmd struct {
	Config   string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Template string `name:"template" enum:"container,static,php,hono" default:"container" help:"Scaffold template."`
	Name     string `name:"name" help:"App name. Defaults to package.json name or directory name."`
	Box      string `name:"box" help:"SSH target for the box."`
	Host     string `name:"host" help:"Route host. Defaults to <app>.example.com."`
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
		Server:   c.Box,
		Host:     c.Host,
		Port:     c.Port,
	})
	return nil
}

type shipCmd struct {
	Config        string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Branch        string `name:"branch" hidden:"" help:"Branch name to use when HEAD is detached."`
	TLS           string `name:"tls" enum:"auto,internal" default:"auto" hidden:"" help:"TLS mode for this deploy."`
	JSON          bool   `name:"json" help:"Emit structured deployment JSON instead of the URL."`
	Rebuild       bool   `name:"rebuild" hidden:"" help:"Refresh base images and bypass Podman's build cache."`
	IncludeDotenv bool   `name:"include-dotenv" hidden:"" help:"Include .env-style files in the uploaded release artifact."`
}

func (c shipCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdShip(root, c.Branch, c.TLS, c.JSON, c.Rebuild, c.IncludeDotenv)
	return nil
}

type sshCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
}

func (c sshCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSSHCurrent(root)
	return nil
}

type statusCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text table."`
}

func (c statusCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdStatus(root, c.JSON)
	return nil
}

type logsCmd struct {
	Config  string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Process string `arg:"" optional:"" help:"Process name. Optional when only one process runs."`
	Follow  bool   `name:"follow" short:"f" help:"Stream new log lines."`
	Tail    int    `name:"tail" default:"100" help:"How many trailing lines to show. Ignored in --follow mode."`
	JSON    bool   `name:"json" help:"Emit log lines as JSON instead of plain text."`
}

func (c logsCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdLogs(root, c.Process, c.Follow, c.Tail, c.JSON)
	return nil
}

type execCmd struct {
	Config  string   `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Branch  string   `name:"branch" help:"Branch name to inspect."`
	Command []string `arg:"" required:"" passthrough:"" help:"Command and arguments to run."`
}

func (c execCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdExec(root, c.Branch, c.Command)
	return nil
}

type whyCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Branch string `name:"branch" help:"Branch name to inspect."`
	JSON   bool   `name:"json" help:"Emit the raw deploy journal entry as JSON."`
}

func (c whyCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdWhy(root, c.Branch, c.JSON)
	return nil
}

type pinCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Branch string `arg:"" help:"Branch name to pin."`
}

func (c pinCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdPreviewPin(root, c.Branch, true)
	return nil
}

type unpinCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Branch string `arg:"" help:"Branch name to unpin."`
}

func (c unpinCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdPreviewPin(root, c.Branch, false)
	return nil
}

type rollbackCmd struct {
	Config  string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Release string `arg:"" optional:"" help:"Release to run. Omitted = previous local release."`
}

func (c rollbackCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdRollback(root, c.Release)
	return nil
}

type saveCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	To     string `name:"to" help:"Destination directory on the host. Supports plain paths and file:// URLs."`
}

func (c saveCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSave(root, c.To)
	return nil
}

type restoreCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	From   string `name:"from" required:"" help:"Backup ID or path on the host."`
}

func (c restoreCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdRestore(root, c.From)
	return nil
}

type rmCmd struct {
	Config  string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Branch  string `arg:"" help:"Branch name whose environment should be removed."`
	Confirm string `name:"confirm" help:"Required app-name confirmation for Production."`
}

func (c rmCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdRm(root, c.Branch, c.Confirm)
	return nil
}

type secretCmd struct {
	Set secretSetCmd  `cmd:"" help:"Read a secret value from stdin and store it on the host."`
	Ls  secretListCmd `cmd:"ls" help:"List secret keys for the current branch environment (keys only; values are never printed)."`
	Rm  secretRmCmd   `cmd:"rm" help:"Remove a secret key from an environment."`
}

type secretSetCmd struct {
	Config  string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Preview bool   `name:"preview" help:"Store the shared Preview value."`
	Branch  string `name:"branch" help:"Store the value for one branch Preview env."`
	Key     string `arg:"" help:"Env-var name (e.g., DATABASE_URL)."`
}

func (c secretSetCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSecretSet(root, c.Key, c.Preview, c.Branch)
	return nil
}

type secretListCmd struct {
	Config  string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Preview bool   `name:"preview" help:"List the shared Preview scope."`
	Branch  string `name:"branch" help:"List one branch Preview scope."`
	JSON    bool   `name:"json" help:"Emit structured JSON instead of plain key lines."`
}

func (c secretListCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSecretList(root, c.JSON, c.Preview, c.Branch)
	return nil
}

type secretRmCmd struct {
	Config  string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Preview bool   `name:"preview" help:"Remove from the shared Preview scope."`
	Branch  string `name:"branch" help:"Remove from one branch Preview scope."`
	Key     string `arg:"" help:"Env-var name to remove."`
}

func (c secretRmCmd) Run() error {
	root, err := projectAppRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdSecretRm(root, c.Key, c.Preview, c.Branch)
	return nil
}

type boxCmd struct {
	Init   boxInitCmd   `cmd:"" help:"Install or converge a box."`
	Doctor boxDoctorCmd `cmd:"" help:"Run box diagnostics."`
	Ls     boxLsCmd     `cmd:"ls" help:"List app environments visible on a box."`
}

type boxLsCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" help:"SSH target. Defaults to ship.toml box when run in an app dir."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c boxLsCmd) Run() error {
	target, err := boxTarget(c.Config, c.Target)
	if err != nil {
		return err
	}
	client.CmdBoxLs(target, c.JSON)
	return nil
}

type boxDoctorCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" help:"SSH target. Defaults to ship.toml box when run in an app dir."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c boxDoctorCmd) Run() error {
	target, err := boxTarget(c.Config, c.Target)
	if err != nil {
		return err
	}
	client.CmdBoxDoctor(target, c.JSON)
	return nil
}

type boxInitCmd struct {
	Target                   string `arg:"" help:"SSH target like deploy@example.com."`
	Mode                     string `enum:"auto,local,remote" default:"auto" help:"Execution mode."`
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

func (c boxInitCmd) Run() error {
	opts := hostinstall.DefaultOptions(nil)
	opts.TargetHost = c.Target
	if c.Mode != "" {
		opts.Mode = c.Mode
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

func cliArgs(args []string) []string {
	if len(args) == 0 {
		if _, err := os.Stat(client.ManifestFile); err == nil {
			return args
		}
		return []string{"--help"}
	}
	return args
}

func boxTarget(configPath, target string) (string, error) {
	if target != "" {
		return target, nil
	}
	root, err := projectAppRoot(configPath)
	if err != nil {
		return "", errcat.New(errcat.CodeBoxTargetRequired, errcat.Fields{"command": "ship box ls <ssh-target>"})
	}
	return client.BoxTarget(root)
}

func main() {
	args := cliArgs(os.Args[1:])
	utils.SetErrorJSON(wantsJSONError(args) || os.Getenv("SHIP_ERROR_JSON") == "1" || wantsServerJSONError(args))
	parser, err := kong.New(
		&cli{},
		kong.Name("ship"),
		kong.Description("Run `ship` inside an app to deploy the current branch. Use commands below for reads, rollback, cleanup, secrets, and box management."),
		kong.ExplicitGroups(cliCommandGroups()),
		kong.ConfigureHelp(kong.HelpOptions{NoExpandSubcommands: true}),
		kong.UsageOnError(),
	)
	if err != nil {
		panic(err)
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		utils.DieError(errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  err.Error(),
			"command": "ship help",
		}), 2)
	}
	if err := ctx.Run(); err != nil {
		if wantsServerAppExecError(args) {
			dieServerAppExecError(err)
		}
		utils.DieError(err, 1)
	}
}

func wantsJSONError(args []string) bool {
	for _, arg := range args {
		if arg == "--json" || strings.HasPrefix(arg, "--json=") {
			return true
		}
	}
	return false
}

func wantsServerJSONError(args []string) bool {
	return len(args) > 0 && args[0] == "server"
}

func wantsServerAppExecError(args []string) bool {
	return len(args) >= 3 && args[0] == "server" && args[1] == "app" && args[2] == "exec"
}

func dieServerAppExecError(err error) {
	if coded, ok := errcat.As(err); ok {
		fmt.Fprintln(os.Stderr, coded.JSONLine())
		os.Exit(utils.ExitCodeForErrorCode(coded.Code()))
	}
	utils.DieError(err, 1)
}

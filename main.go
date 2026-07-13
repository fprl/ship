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
	"github.com/fprl/ship/internal/knownhosts"
	"github.com/fprl/ship/internal/shipidentity"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
)

// Public CLI surface. The post-cutover lifecycle is minimal on
// purpose; host mutation goes through the privileged helper and runtime
// truth comes from manifest snapshots, identity files, and Podman labels.
type cli struct {
	Ship       shipCmd          `cmd:"" default:"withargs" hidden:"" group:"project" help:"Deploy the current branch."`
	Init       initCmd          `cmd:"" group:"project" help:"Create local project files and a ship.toml manifest."`
	Status     statusCmd        `cmd:"" group:"project" help:"Show all live environments for this app."`
	Logs       logsCmd          `cmd:"" group:"project" help:"Tail logs for the current branch environment."`
	Exec       execCmd          `cmd:"" group:"project" help:"Run a one-off command in the current branch environment."`
	Why        whyCmd           `cmd:"" group:"project" help:"Explain the latest deploy outcome for the current branch environment."`
	Rollback   rollbackCmd      `cmd:"" group:"project" help:"Roll back the current branch environment."`
	Rm         rmCmd            `cmd:"rm" group:"project" help:"Remove an environment by branch name."`
	Data       dataCmd          `cmd:"" group:"project" help:"Manage Preview data forks."`
	Pin        pinCmd           `cmd:"" group:"project" help:"Pin a preview environment so the reaper leaves it running."`
	Unpin      unpinCmd         `cmd:"" group:"project" help:"Unpin a preview environment so normal expiry applies."`
	Preview    previewCmd       `cmd:"" group:"project" help:"Manage preview protection."`
	Share      shareCmd         `cmd:"" group:"project" help:"Mint or revoke a protected Preview share link."`
	Save       saveCmd          `cmd:"" group:"project" help:"Create a backup for the current branch environment."`
	Restore    restoreCmd       `cmd:"" group:"project" help:"Restore the current branch environment from a backup."`
	SSH        sshCmd           `cmd:"ssh" group:"project" help:"Open an SSH session to the box."`
	Secret     secretCmd        `cmd:"" group:"project" help:"Manage secrets for the current branch environment."`
	Box        boxCmd           `cmd:"" group:"host" help:"Install or inspect a ship box."`
	Member     memberCmd        `cmd:"" group:"host" help:"Manage deploy SSH members."`
	Approve    approveCmd       `cmd:"" group:"host" help:"List or approve one-shot role approvals."`
	Docs       docsCmd          `cmd:"" group:"global" help:"Print the agent contract."`
	Help       helpCmd          `cmd:"" group:"global" help:"Show usage for one verb."`
	Completion completionCmd    `cmd:"" hidden:"" group:"global" help:"Emit shell completions. Install: bash: ship completion bash > /etc/bash_completion.d/ship; zsh: ship completion zsh > ~/.zsh/completions/_ship; fish: ship completion fish > ~/.config/fish/completions/ship.fish."`
	Version    versionCmd       `cmd:"" group:"global" help:"Print the ship version."`
	Server     helper.ServerCmd `cmd:"" hidden:"" group:"global" help:"Privileged host API."`
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

type projectArgs struct {
	Config string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
}

func (p projectArgs) projectRoot() (string, error) {
	return projectAppRoot(p.Config)
}

type initCmd struct {
	Config   string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
	Template string `name:"template" enum:"container,static,php,hono" default:"container" help:"Scaffold template."`
	Name     string `name:"name" help:"App name. Defaults to package.json name or directory name."`
	Box      string `name:"box" help:"Box host."`
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
	projectArgs
	Branch        string `name:"branch" hidden:"" help:"Branch name to use when HEAD is detached."`
	TLS           string `name:"tls" enum:"auto,internal" default:"auto" hidden:"" help:"TLS mode for this deploy."`
	JSON          bool   `name:"json" help:"Emit structured deployment JSON instead of the URL."`
	Rebuild       bool   `name:"rebuild" hidden:"" help:"Refresh base images and bypass Podman's build cache."`
	IncludeDotenv bool   `name:"include-dotenv" hidden:"" help:"Include .env-style files in the uploaded release artifact."`
}

func (c shipCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdShip(root, c.Branch, c.TLS, c.JSON, c.Rebuild, c.IncludeDotenv)
	return nil
}

type sshCmd struct {
	projectArgs
}

func (c sshCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdSSHCurrent(root)
	return nil
}

type statusCmd struct {
	projectArgs
	JSON bool `name:"json" help:"Emit structured JSON instead of the text table."`
}

func (c statusCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdStatus(root, c.JSON)
	return nil
}

type logsCmd struct {
	projectArgs
	Process string `arg:"" optional:"" help:"Process name. Optional when only one process runs."`
	Follow  bool   `name:"follow" short:"f" help:"Stream new log lines."`
	Tail    *int   `name:"tail" help:"How many trailing lines to show. Defaults to 100 when omitted; use 0 with --follow to stream new lines only."`
	JSON    bool   `name:"json" help:"Emit log lines as JSON instead of plain text."`
}

func (c logsCmd) Run() error {
	if err := client.ValidateLogsTail(c.Tail); err != nil {
		return err
	}
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdLogs(root, c.Process, c.Follow, c.Tail, c.JSON)
	return nil
}

type execCmd struct {
	projectArgs
	Branch  string   `name:"branch" help:"Branch name to inspect."`
	Command []string `arg:"" required:"" passthrough:"" help:"Command and arguments to run."`
}

func (c execCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdExec(root, c.Branch, c.Command)
	return nil
}

type whyCmd struct {
	projectArgs
	Branch string `name:"branch" help:"Branch name to inspect."`
	JSON   bool   `name:"json" help:"Emit the raw deploy journal entry as JSON."`
}

func (c whyCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdWhy(root, c.Branch, c.JSON)
	return nil
}

type pinCmd struct {
	projectArgs
	Branch string `arg:"" help:"Branch name to pin."`
}

func (c pinCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdPreviewPin(root, c.Branch, true)
	return nil
}

type unpinCmd struct {
	projectArgs
	Branch string `arg:"" help:"Branch name to unpin."`
}

type previewCmd struct {
	Password previewPasswordCmd `cmd:"" help:"Print or rotate this app's Preview password and bypass token."`
}

type previewPasswordCmd struct {
	projectArgs
	Rotate bool `name:"rotate" help:"Generate a new team password; the bypass token stays unchanged."`
}

type shareCmd struct {
	projectArgs
	Rm bool `name:"rm" help:"Revoke this preview's share link."`
}

func (c shareCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdShare(root, c.Rm)
	return nil
}

func (c previewPasswordCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdPreviewPassword(root, c.Rotate)
	return nil
}

func (c unpinCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdPreviewPin(root, c.Branch, false)
	return nil
}

type rollbackCmd struct {
	projectArgs
	Release string `arg:"" optional:"" help:"Release to run. Omitted = previous local release."`
}

func (c rollbackCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdRollback(root, c.Release)
	return nil
}

type saveCmd struct {
	projectArgs
	To string `name:"to" help:"Destination directory on the host. Supports plain paths and file:// URLs."`
}

func (c saveCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdSave(root, c.To)
	return nil
}

type restoreCmd struct {
	projectArgs
	From string `name:"from" required:"" help:"Backup ID or path on the host."`
}

func (c restoreCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdRestore(root, c.From)
	return nil
}

type rmCmd struct {
	projectArgs
	Branch  string `arg:"" help:"Branch name whose environment should be removed."`
	Confirm string `name:"confirm" help:"Required app-name confirmation for Production."`
}

func (c rmCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdRm(root, c.Branch, c.Confirm)
	return nil
}

type dataCmd struct {
	Fork dataForkCmd `cmd:"" help:"Fork Production /data into this branch's Preview."`
	Rm   dataRmCmd   `cmd:"rm" help:"Reset this branch's Preview /data to empty."`
}

type dataForkCmd struct {
	projectArgs
}

func (c dataForkCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdDataFork(root)
	return nil
}

type dataRmCmd struct {
	projectArgs
}

func (c dataRmCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdDataRm(root)
	return nil
}

type secretCmd struct {
	Set secretSetCmd  `cmd:"" help:"Read a secret value from stdin and store it on the host."`
	Ls  secretListCmd `cmd:"ls" help:"List secret keys for the current branch environment (keys only; values are never printed)."`
	Rm  secretRmCmd   `cmd:"rm" help:"Remove a secret key from an environment."`
}

type secretSetCmd struct {
	projectArgs
	Preview bool   `name:"preview" help:"Store the shared Preview value."`
	Branch  string `name:"branch" help:"Store the value for one branch Preview env."`
	From    string `name:"from" type:"path" help:"Bulk import KEY=VALUE pairs from a dotenv file."`
	Replace bool   `name:"replace" help:"Make the file authoritative for the selected scope; remove keys not present in --from."`
	Key     string `arg:"" optional:"" help:"Env-var name (e.g., DATABASE_URL)."`
}

func (c secretSetCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdSecretSet(root, client.SecretSetOptions{
		Key:     c.Key,
		From:    c.From,
		Preview: c.Preview,
		Branch:  c.Branch,
		Replace: c.Replace,
	})
	return nil
}

type secretListCmd struct {
	projectArgs
	Preview bool   `name:"preview" help:"List the shared Preview scope."`
	Branch  string `name:"branch" help:"List one branch Preview scope."`
	JSON    bool   `name:"json" help:"Emit structured JSON instead of plain key lines."`
}

func (c secretListCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdSecretList(root, c.JSON, c.Preview, c.Branch)
	return nil
}

type secretRmCmd struct {
	projectArgs
	Preview bool   `name:"preview" help:"Remove from the shared Preview scope."`
	Branch  string `name:"branch" help:"Remove from one branch Preview scope."`
	Key     string `arg:"" help:"Env-var name to remove."`
}

func (c secretRmCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdSecretRm(root, c.Key, c.Preview, c.Branch)
	return nil
}

type boxCmd struct {
	Setup  boxSetupCmd  `cmd:"" help:"Install or converge a box."`
	Doctor boxDoctorCmd `cmd:"" help:"Run box diagnostics."`
	Notify boxNotifyCmd `cmd:"" help:"Read or set the box notification webhook."`
	Ls     boxLsCmd     `cmd:"ls" help:"List app environments visible on a box."`
	Rm     boxRmCmd     `cmd:"rm" help:"Destroy an app and all its environments on a box."`
	Status boxStatusCmd `cmd:"" help:"Show helper version, disk, apps, and approvals for one box."`
	Update boxUpdateCmd `cmd:"" help:"Update a box helper and version-owned artifacts."`
	Forget boxForgetCmd `cmd:"" help:"Drop a box host-key pin."`
}

type boxStatusCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	JSON   bool   `name:"json" help:"Emit stable structured box status JSON."`
}

func (c boxStatusCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box status <box>")
	if err != nil {
		return err
	}
	client.CmdBoxStatus(target, c.JSON)
	return nil
}

type boxUpdateCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
}

func (c boxUpdateCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box update <box>")
	if err != nil {
		return err
	}
	client.CmdBoxUpdate(target)
	return nil
}

type boxNotifyCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	URL    string `arg:"" optional:"" name:"url" help:"Webhook URL to set."`
	Remove bool   `name:"rm" help:"Clear the box webhook."`
}

func (c boxNotifyCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box notify <box>")
	if err != nil {
		return err
	}
	client.CmdBoxNotify(target, c.URL, c.Remove)
	return nil
}

type memberCmd struct {
	Add memberAddCmd `cmd:"" help:"Authorize a member's SSH public key for deploy access."`
	Ls  memberLsCmd  `cmd:"ls" help:"List authorized deploy members."`
	Rm  memberRmCmd  `cmd:"rm" help:"Revoke a deploy member's SSH keys."`
}

type memberAddCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Source string `arg:"" help:"GitHub username, SSH public key string, or path to a .pub/.pem file."`
	Role   string `name:"role" enum:"owner,shipper,agent" default:"shipper" help:"Role recorded for newly added keys."`
}

func (c memberAddCmd) Run() error {
	target, err := memberTarget(c.Config)
	if err != nil {
		return err
	}
	client.CmdMemberAdd(target, c.Source, c.Role)
	return nil
}

type memberLsCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of plain text."`
}

func (c memberLsCmd) Run() error {
	target, err := memberTarget(c.Config)
	if err != nil {
		return err
	}
	client.CmdMemberLs(target, c.JSON)
	return nil
}

type memberRmCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Name   string `arg:"" help:"Member name to revoke."`
}

func (c memberRmCmd) Run() error {
	target, err := memberTarget(c.Config)
	if err != nil {
		return err
	}
	client.CmdMemberRm(target, c.Name)
	return nil
}

type approveCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	JSON   bool   `name:"json" help:"Emit structured JSON for the pending approval list."`
	ID     string `arg:"" optional:"" help:"Approval id to grant. Omit to list pending requests."`
}

func (c approveCmd) Run() error {
	target, err := memberTarget(c.Config)
	if err != nil {
		return err
	}
	client.CmdApprove(target, c.ID, c.JSON)
	return nil
}

func memberTarget(configPath string) (string, error) {
	root, err := projectAppRoot(configPath)
	if err != nil {
		return "", err
	}
	return client.BoxTarget(root)
}

type boxLsCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c boxLsCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box ls <box>")
	if err != nil {
		return err
	}
	client.CmdBoxLs(target, c.JSON)
	return nil
}

type boxDoctorCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c boxDoctorCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box doctor <box>")
	if err != nil {
		return err
	}
	client.CmdBoxDoctor(target, c.JSON)
	return nil
}

type boxRmCmd struct {
	Config  string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	App     string `arg:"" help:"App name to destroy."`
	Target  string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	Confirm string `name:"confirm" help:"Required app-name confirmation."`
}

func (c boxRmCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box rm <box>")
	if err != nil {
		return err
	}
	client.CmdBoxRm(target, c.App, c.Confirm)
	return nil
}

type boxForgetCmd struct {
	Target string `arg:"" name:"box" help:"Box host to forget."`
}

func (c boxForgetCmd) Run() error {
	client.CmdBoxForget(c.Target)
	return nil
}

type boxSetupCmd struct {
	Target                   string `arg:"" name:"ssh-target" help:"Bootstrap SSH target like root@example.com or example.com."`
	Mode                     string `enum:"auto,local,remote" default:"auto" help:"Execution mode."`
	BootstrapUser            string `help:"SSH user for remote bootstrap."`
	SSHKey                   string `name:"ssh-key" help:"SSH private key for remote mode."`
	OperatorSSHPublicKeyFile string `help:"SSH public key file for operator access."`
	DeploySSHPublicKeyFile   string `help:"SSH public key file for deploy access. Default: your ship identity becomes the first member."`
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
	InstallLitestream        *bool  `name:"litestream" negatable:"" help:"Install Litestream."`
	CheckMode                bool   `name:"check" help:"Plan changes without writing files or running mutating commands."`
	SuppressSetupNarration   bool   `name:"suppress-setup-narration" hidden:""`
	SetupSecretsFile         string `name:"setup-secrets-file" hidden:""`
}

func (c boxSetupCmd) Run() error {
	opts := hostinstall.DefaultOptions(nil)
	opts.TargetHost = c.Target
	if c.Mode != "" {
		opts.Mode = c.Mode
	}
	if c.BootstrapUser != "" {
		opts.BootstrapUser = c.BootstrapUser
		opts.BootstrapUserExplicit = true
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
	if c.InstallLitestream != nil {
		opts.InstallLitestream = *c.InstallLitestream
	}
	opts.CheckMode = c.CheckMode
	opts.NarrateSetup = !c.SuppressSetupNarration
	opts.SetupSecretsFile = c.SetupSecretsFile
	if !internalLocalBoxSetupWithProvidedKeys(c) {
		identity, err := shipidentity.EnsureShipIdentity(shipidentity.Options{Output: os.Stderr})
		if err != nil {
			return err
		}
		if !identity.Created {
			fmt.Fprintf(os.Stderr, "identity: %s (~/.ssh/ship)\n", identity.Name)
		}
		opts.BootstrapIdentityKey = identity.PrivateKeyPath
		if opts.OperatorSSHPublicKeyFile == "" {
			opts.OperatorSSHPublicKeyFile = identity.PublicKeyPath
		}
		if opts.DeploySSHPublicKeyFile == "" {
			opts.DeploySSHPublicKeyFile = identity.PublicKeyPath
			opts.DeployKeyIsShipIdentity = true
		}
	}
	return hostinstall.NewInstaller().RunOptions(opts)
}

func internalLocalBoxSetupWithProvidedKeys(c boxSetupCmd) bool {
	return c.Mode == "local" &&
		c.Target == "localhost" &&
		c.OperatorSSHPublicKeyFile != "" &&
		c.DeploySSHPublicKeyFile != ""
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

func boxTargetFor(configPath, target, command string) (string, error) {
	if target != "" {
		return target, nil
	}
	root, err := projectAppRoot(configPath)
	if err != nil {
		if coded, ok := errcat.As(err); ok && coded.Code() != errcat.CodeManifestInvalid {
			return "", err
		}
		return "", boxTargetRequiredError(command)
	}
	return client.BoxTarget(root)
}

func boxTargetRequiredError(command string) error {
	boxes, err := knownhosts.ListHosts()
	if err != nil {
		boxes = nil
	}
	return errcat.New(errcat.CodeBoxTargetRequired, errcat.Fields{
		"command":     command,
		"known_boxes": knownhosts.KnownBoxesCause(boxes),
	})
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
	if len(args) < 3 || args[0] != "server" || args[1] != "app" {
		return false
	}
	i := 2
	for i < len(args) && args[i] == "--member-fingerprint" {
		i += 2
	}
	return i < len(args) && args[i] == "exec"
}

func dieServerAppExecError(err error) {
	if coded, ok := errcat.As(err); ok {
		fmt.Fprintln(os.Stderr, coded.JSONLine())
		os.Exit(utils.ExitCodeForErrorCode(coded.Code()))
	}
	utils.DieError(err, 1)
}

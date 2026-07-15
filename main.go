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
	Init       initCmd          `cmd:"" group:"project" help:"Create a ship.toml manifest."`
	Status     statusCmd        `cmd:"" group:"project" help:"Show all live environments for this app."`
	Logs       logsCmd          `cmd:"" group:"project" help:"Tail logs for the current branch environment."`
	Exec       execCmd          `cmd:"" group:"project" help:"Run a one-off command in the current branch environment."`
	Why        whyCmd           `cmd:"" group:"project" help:"Explain the latest deploy outcome for the current branch environment."`
	Rollback   rollbackCmd      `cmd:"" group:"project" help:"Roll back the current branch environment."`
	Rm         rmCmd            `cmd:"rm" group:"project" help:"Remove an environment by branch name."`
	Data       dataCmd          `cmd:"" group:"project" help:"Manage app data."`
	Preview    previewCmd       `cmd:"" group:"project" help:"Manage the current Preview."`
	SSH        sshCmd           `cmd:"ssh" group:"project" help:"Open an SSH session to the box."`
	Secret     secretCmd        `cmd:"" group:"project" help:"Manage secrets for the current branch environment."`
	Box        boxCmd           `cmd:"" group:"host" help:"Install or inspect a ship box."`
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
	Config string `name:"config" type:"path" default:"ship.toml" help:"Path to ship.toml."`
}

func (c initCmd) Run() error {
	root, err := appRoot(c.Config)
	if err != nil {
		return err
	}
	client.CmdInit(root)
	return nil
}

type shipCmd struct {
	projectArgs
	Branch  string `name:"branch" hidden:"" help:"Branch name to use when HEAD is detached."`
	TLS     string `name:"tls" enum:"auto,internal" default:"auto" hidden:"" help:"TLS mode for this deploy."`
	JSON    bool   `name:"json" help:"Emit structured deployment JSON instead of the URL."`
	Rebuild bool   `name:"rebuild" hidden:"" help:"Refresh base images and bypass Podman's build cache."`
}

func (c shipCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdShip(root, c.Branch, c.TLS, c.JSON, c.Rebuild)
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
	Command []string `arg:"" optional:"" passthrough:"" help:"Command and arguments to run; write -- before commands that start with a dash."`
}

func (c execCmd) Run() error {
	if len(c.Command) == 0 {
		return cliUsageError("ship exec requires a command", "ship exec -- <cmd...>")
	}
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

type previewPinCmd struct {
	projectArgs
	Branch string `arg:"" optional:"" help:"Branch name to pin."`
}

func (c previewPinCmd) Run() error {
	if c.Branch == "" {
		return cliUsageError("preview pin requires <branch>", "ship status")
	}
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdPreviewPin(root, c.Branch, true)
	return nil
}

type previewUnpinCmd struct {
	projectArgs
	Branch string `arg:"" optional:"" help:"Branch name to unpin."`
}

type previewCmd struct {
	Pin   previewPinCmd   `cmd:"" help:"Pin a preview environment so the reaper leaves it running."`
	Unpin previewUnpinCmd `cmd:"" help:"Unpin a preview environment so normal expiry applies."`
	Share previewShareCmd `cmd:"" help:"Print or rotate this Preview's capability URL."`
}

type previewShareCmd struct {
	projectArgs
	Rotate bool `name:"rotate" help:"Generate a new preview capability."`
}

func (c previewShareCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdPreviewShare(root, c.Rotate)
	return nil
}

func (c previewUnpinCmd) Run() error {
	if c.Branch == "" {
		return cliUsageError("preview unpin requires <branch>", "ship status")
	}
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

type rmCmd struct {
	projectArgs
	Branch  string `arg:"" optional:"" help:"Branch name whose environment should be removed."`
	Confirm string `name:"confirm" help:"Required app-name confirmation for Production."`
}

func (c rmCmd) Run() error {
	if c.Branch == "" {
		return cliUsageError("ship rm requires <branch>", "ship status")
	}
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdRm(root, c.Branch, c.Confirm)
	return nil
}

type dataCmd struct {
	Fork    dataForkCmd    `cmd:"" help:"Fork Production /data into this branch's Preview."`
	Reset   dataResetCmd   `cmd:"reset" help:"Reset this branch's Preview /data to empty."`
	Save    dataSaveCmd    `cmd:"" help:"Save this environment's /data to a local snapshot."`
	Restore dataRestoreCmd `cmd:"" help:"Restore this environment's /data from a local snapshot."`
	Ls      dataLsCmd      `cmd:"" help:"List local data snapshots for this app."`
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

type dataResetCmd struct {
	projectArgs
}

func (c dataResetCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdDataReset(root)
	return nil
}

type dataSaveCmd struct {
	projectArgs
	Out string `name:"out" type:"path" help:"Local path for the snapshot."`
}

func (c dataSaveCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdDataSave(root, c.Out)
	return nil
}

type dataRestoreCmd struct {
	projectArgs
	Snapshot string `arg:"" optional:"" help:"Local snapshot ID or path."`
	Confirm  string `name:"confirm" help:"Required app-name confirmation for Production."`
}

func (c dataRestoreCmd) Run() error {
	if c.Snapshot == "" {
		return cliUsageError("data restore requires <id|path>", "ship data ls")
	}
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdDataRestore(root, c.Snapshot, c.Confirm)
	return nil
}

type dataLsCmd struct {
	projectArgs
	JSON bool `name:"json" help:"Emit stable snapshot JSON."`
}

func (c dataLsCmd) Run() error {
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdDataLs(root, c.JSON)
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
	Key     string `arg:"" optional:"" help:"Env-var name to remove."`
}

func (c secretRmCmd) Run() error {
	if c.Key == "" {
		return cliUsageError("secret rm requires <KEY>", "ship secret ls")
	}
	root, err := c.projectRoot()
	if err != nil {
		return err
	}
	client.CmdSecretRm(root, c.Key, c.Preview, c.Branch)
	return nil
}

type boxCmd struct {
	Setup    boxSetupCmd        `cmd:"" help:"Install or converge a box."`
	Doctor   boxDoctorCmd       `cmd:"" help:"Run box diagnostics."`
	Webhook  boxWebhookCmd      `cmd:"" help:"Read or set the box webhook."`
	App      boxAppCmd          `cmd:"" help:"Manage apps on a box."`
	Ls       boxLsCmd           `cmd:"" hidden:""`
	Status   boxStatusCmd       `cmd:"" help:"Show helper version, disk, apps, members, approvals, and the last doctor result for one box."`
	Update   boxUpdateCmd       `cmd:"" help:"Update a box helper and version-owned artifacts."`
	Forget   boxForgetCmd       `cmd:"" hidden:"" help:"Drop a box host-key pin."`
	Config   boxConfigClientCmd `cmd:"" help:"Read or change box configuration."`
	Member   boxMemberCmd       `cmd:"" help:"Manage deploy SSH members."`
	Approval boxApprovalCmd     `cmd:"" help:"Manage pending role approvals."`
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

type boxWebhookCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	URL    string `arg:"" optional:"" name:"url" help:"Webhook URL to set."`
	Remove bool   `name:"rm" help:"Clear the box webhook."`
	JSON   bool   `name:"json" help:"Emit structured JSON when reading the webhook."`
}

type boxConfigClientCmd struct {
	Config string   `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Args   []string `arg:"" optional:"" name:"args" help:"Box host, optionally followed by set/unset and a key."`
	JSON   bool     `name:"json" help:"Emit stable structured box config JSON."`
}

func (c boxConfigClientCmd) Run() error {
	targetArg, action, key, value, err := parseBoxConfigArgs(c.Args)
	if err != nil {
		return err
	}
	target, err := boxTargetFor(c.Config, targetArg, "ship box config <box>")
	if err != nil {
		return err
	}
	switch action {
	case "":
		client.CmdBoxConfigGet(target, c.JSON)
	case "set":
		if c.JSON {
			return errcat.New(errcat.CodeUsageError, errcat.Fields{"detail": "--json is only valid when reading box config", "command": "ship box config <box> --json"})
		}
		client.CmdBoxConfigSet(target, key, value)
	case "unset":
		if c.JSON {
			return errcat.New(errcat.CodeUsageError, errcat.Fields{"detail": "--json is only valid when reading box config", "command": "ship box config <box> --json"})
		}
		client.CmdBoxConfigUnset(target, key)
	}
	return nil
}

func parseBoxConfigArgs(args []string) (target, action, key, value string, err error) {
	usage := func(detail string) (string, string, string, string, error) {
		return "", "", "", "", errcat.New(errcat.CodeUsageError, errcat.Fields{"detail": detail, "command": "ship box config <box> [--json] | ship box config <box> set <key> <value> | ship box config <box> unset <key>"})
	}
	if len(args) == 0 {
		return "", "", "", "", nil
	}
	if args[0] == "set" || args[0] == "unset" {
		action = args[0]
		args = args[1:]
		switch action {
		case "set":
			if len(args) != 2 {
				return usage("box config set requires <key> <value>")
			}
			return "", action, args[0], args[1], nil
		case "unset":
			if len(args) != 1 {
				return usage("box config unset requires <key>")
			}
			return "", action, args[0], "", nil
		}
	} else {
		target = args[0]
		args = args[1:]
	}
	if len(args) == 0 {
		return target, action, "", "", nil
	}
	if action != "" {
		return usage("box config action accepts too many arguments")
	}
	action = args[0]
	args = args[1:]
	switch action {
	case "set":
		if len(args) != 2 {
			return usage("box config set requires <key> <value>")
		}
		return target, action, args[0], args[1], nil
	case "unset":
		if len(args) != 1 {
			return usage("box config unset requires <key>")
		}
		return target, action, args[0], "", nil
	default:
		return usage("box config action must be set or unset")
	}
}

func (c boxWebhookCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box webhook <box>")
	if err != nil {
		return err
	}
	client.CmdBoxWebhook(target, c.URL, c.Remove, c.JSON)
	return nil
}

type boxMemberCmd struct {
	Add boxMemberAddCmd `cmd:"" help:"Authorize a member's SSH public key for deploy access."`
	Ls  boxMemberLsCmd  `cmd:"" help:"List deploy SSH members."`
	Rm  boxMemberRmCmd  `cmd:"rm" help:"Revoke a deploy member's SSH keys."`
}

type boxMemberAddCmd struct {
	Config  string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Source  string `arg:"" optional:"" help:"HTTPS keys-URL, SSH public key string, or path to a .pub/.pem file."`
	Target  string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	Name    string `name:"name" help:"Box-global member name."`
	Role    string `name:"role" enum:"owner,shipper,agent" default:"shipper" help:"Role recorded for newly added keys."`
	Confirm string `name:"confirm" help:"Commit a matching URL plan: <name>@sha256:<plan-digest>."`
}

func (c boxMemberAddCmd) Run() error {
	if c.Source == "" {
		box := boxTargetForRemediation(c.Config, c.Target)
		return cliUsageError("box member add requires <https-url|key|path>", "ship box member add <https-url|key|path> "+box+" --name <name>")
	}
	target, err := boxTargetFor(c.Config, c.Target, "ship box member add <https-url|key|path> <box> --name <name>")
	if err != nil {
		return err
	}
	return client.CmdBoxMemberAdd(target, c.Source, c.Name, c.Role, c.Confirm)
}

type boxMemberLsCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of plain text."`
}

func (c boxMemberLsCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box member ls <box>")
	if err != nil {
		return err
	}
	client.CmdBoxMemberLs(target, c.JSON)
	return nil
}

type boxMemberRmCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Name   string `arg:"" optional:"" help:"Member name to revoke."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
}

func (c boxMemberRmCmd) Run() error {
	if c.Name == "" {
		return cliUsageError("box member rm requires <name>", optionalBoxCommand(c.Config, c.Target, "ship box member ls"))
	}
	target, err := boxTargetFor(c.Config, c.Target, "ship box member rm <name> <box>")
	if err != nil {
		return err
	}
	client.CmdBoxMemberRm(target, c.Name)
	return nil
}

type boxApprovalCmd struct {
	Ls    boxApprovalLsCmd    `cmd:"" help:"List pending role approvals."`
	Grant boxApprovalGrantCmd `cmd:"" help:"Grant one pending role approval."`
}

type boxApprovalLsCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	JSON   bool   `name:"json" help:"Emit structured JSON for pending approvals."`
}

func (c boxApprovalLsCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box approval ls <box>")
	if err != nil {
		return err
	}
	client.CmdBoxApprovalLs(target, c.JSON)
	return nil
}

type boxApprovalGrantCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	ID     string `arg:"" optional:"" help:"Approval id to grant."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
}

func (c boxApprovalGrantCmd) Run() error {
	if c.ID == "" {
		return cliUsageError("box approval grant requires <id>", optionalBoxCommand(c.Config, c.Target, "ship box approval ls"))
	}
	target, err := boxTargetFor(c.Config, c.Target, "ship box approval grant <id> <box>")
	if err != nil {
		return err
	}
	client.CmdBoxApprovalGrant(target, c.ID)
	return nil
}

type boxAppCmd struct {
	Ls boxAppLsCmd `cmd:"" help:"Show the box's app table."`
	Rm boxAppRmCmd `cmd:"rm" help:"Destroy an app and all its environments on a box."`
}

type boxAppLsCmd struct {
	Config string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	Target string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	JSON   bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c boxAppLsCmd) Run() error {
	target, err := boxTargetFor(c.Config, c.Target, "ship box app ls <box>")
	if err != nil {
		return err
	}
	client.CmdBoxAppLs(target, c.JSON)
	return nil
}

// boxLsCmd reserves ls for future box listing while pointing at the box app table.
type boxLsCmd struct{}

func (boxLsCmd) Run() error {
	return errcat.New(errcat.CodeUsageError, errcat.Fields{
		"detail":  "ship box ls does not exist; the box app table is ship box app ls",
		"command": "ship box app ls",
	})
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

type boxAppRmCmd struct {
	Config  string `name:"config" type:"path" default:"ship.toml" hidden:"" help:"Path to ship.toml."`
	App     string `arg:"" optional:"" help:"App name to destroy."`
	Target  string `arg:"" optional:"" name:"box" help:"Box host. Defaults to ship.toml box when run in an app dir."`
	Confirm string `name:"confirm" help:"Required app-name confirmation."`
}

func (c boxAppRmCmd) Run() error {
	if c.App == "" {
		return cliUsageError("box app rm requires <app>", optionalBoxCommand(c.Config, c.Target, "ship box app ls"))
	}
	target, err := boxTargetFor(c.Config, c.Target, fmt.Sprintf("ship box app rm %s <box> --confirm %s", c.App, c.App))
	if err != nil {
		return err
	}
	client.CmdBoxAppRm(target, c.App, c.Confirm)
	return nil
}

type boxForgetCmd struct {
	Target string `arg:"" optional:"" name:"box" help:"Box host to forget."`
}

func (c boxForgetCmd) Run() error {
	if c.Target == "" {
		return boxTargetRequiredError("ship box forget <box>")
	}
	client.CmdBoxForget(c.Target)
	return nil
}

type boxSetupCmd struct {
	Target                   string `arg:"" optional:"" name:"ssh-target" help:"Bootstrap SSH target like root@example.com or example.com."`
	ClientAddress            string `name:"client-address" hidden:""`
	Mode                     string `enum:"auto,local,remote" default:"auto" help:"Execution mode."`
	BootstrapUser            string `help:"SSH user for remote bootstrap."`
	SSHKey                   string `name:"ssh-key" help:"SSH private key for remote mode."`
	OperatorSSHPublicKeyFile string `help:"SSH public key file for operator access."`
	DeploySSHPublicKeyFile   string `help:"SSH public key file for deploy access. Default: your ship identity becomes the first member."`
	MemberName               string `name:"member-name" hidden:"" help:"Setup member name."`
	CheckMode                bool   `name:"check" help:"Plan changes without writing files or running mutating commands."`
	SuppressSetupNarration   bool   `name:"suppress-setup-narration" hidden:""`
}

func (c boxSetupCmd) Run() error {
	if c.Target == "" {
		return errcat.WithMessage(boxTargetRequiredError("ship box setup <ssh-target>"), "target a box to set up")
	}
	opts := hostinstall.DefaultOptions(nil)
	opts.TargetHost = c.Target
	opts.ClientAddress = c.ClientAddress
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
	if c.MemberName != "" {
		opts.MemberName = c.MemberName
	}
	opts.CheckMode = c.CheckMode
	opts.NarrateSetup = !c.SuppressSetupNarration
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

func cliUsageError(detail, command string) error {
	return errcat.New(errcat.CodeUsageError, errcat.Fields{
		"detail":  detail,
		"command": command,
	})
}

func optionalBoxCommand(configPath, target, command string) string {
	box := boxTargetForRemediation(configPath, target)
	if box == "<box>" {
		return command + " [<box>]"
	}
	return command + " " + box
}

func boxTargetForRemediation(configPath, target string) string {
	if target != "" {
		return target
	}
	root, err := projectAppRoot(configPath)
	if err != nil {
		return "<box>"
	}
	box, err := client.BoxTarget(root)
	if err != nil || box == "" {
		return "<box>"
	}
	return box
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
		if arg == "--" {
			return false
		}
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

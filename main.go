package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/fprl/simple-vps/cmd/client"
	"github.com/fprl/simple-vps/cmd/helper"
	"github.com/fprl/simple-vps/cmd/hostinstall"
)

type cli struct {
	Init     initCmd     `cmd:"" help:"Create a simple-vps.toml manifest."`
	Check    checkCmd    `cmd:"" help:"Validate an app manifest."`
	Setup    setupCmd    `cmd:"" help:"Create the app user and directories on a host."`
	Deploy   deployCmd   `cmd:"" help:"Deploy an app release."`
	Rollback rollbackCmd `cmd:"" help:"Rollback an app to a previous release."`
	Destroy  destroyCmd  `cmd:"" help:"Destroy app services, routes, and optionally app data."`
	Restart  restartCmd  `cmd:"" help:"Restart one app service."`
	Status   statusCmd   `cmd:"" help:"Show app status, or server status when run on the host."`
	Logs     logsCmd     `cmd:"" help:"Show app service logs."`
	SSH      sshCmd      `cmd:"ssh" help:"Open an SSH session to an app environment."`
	Secret   secretCmd   `cmd:"" help:"Manage remote app secrets."`
	Env      envCmd      `cmd:"" help:"Manage remote app environment files."`
	Host     hostCmd     `cmd:"" help:"Install or inspect a Simple VPS host."`
	Route    routeCmd    `cmd:"" help:"Inspect routes from a laptop or CI runner."`
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
	Env string `arg:"" optional:"" help:"Environment to inspect. Omit on a host for server status."`
}

func (c statusCmd) Run() error {
	if c.Env == "" {
		helper.Run("status", nil)
		return nil
	}
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

func (hostCmd) Run() error {
	client.CmdHost(nil)
	return nil
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

type hostInstallCmd struct{}

func (hostInstallCmd) Run() error {
	return hostinstall.Run(nil)
}

type routeCmd struct {
	List routeListCmd `cmd:"" default:"1" help:"List configured routes."`
}

type routeListCmd struct {
	JSON   bool   `name:"json" help:"Output JSON."`
	Server string `help:"SSH target like deploy@example.com."`
}

func (c routeListCmd) Run() error {
	if c.Server == "" && !fileExists("simple-vps.toml") {
		args := []string{"list"}
		if c.JSON {
			args = append(args, "--json")
		}
		helper.Run("route", args)
		return nil
	}

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
	args := os.Args[1:]
	if runHostInstall(args) || runInternalCommand(args) {
		return
	}

	parser := kong.Parse(
		&cli{},
		kong.Name("simple-vps"),
		kong.Description("Deploy JS/TS apps to a VPS and manage the host runtime."),
		kong.UsageOnError(),
	)
	parser.FatalIfErrorf(parser.Run())
}

func runHostInstall(args []string) bool {
	if len(args) < 2 || args[0] != "host" || args[1] != "install" {
		return false
	}
	if err := hostinstall.Run(args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
	return true
}

func runInternalCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}

	switch args[0] {
	case "app", "cloudflare", "generate-caddy", "doctor", "publish", "unpublish", "routes":
		helper.Run(args[0], args[1:])
		return true
	case "route":
		if shouldUseInternalRoute(args[1:]) {
			helper.Run("route", args[1:])
			return true
		}
	}

	return false
}

func shouldUseInternalRoute(args []string) bool {
	if hasHelpFlag(args) {
		return false
	}
	if len(args) == 0 || args[0] != "list" {
		return true
	}
	if hasServerFlag(args) {
		return false
	}
	return !fileExists("simple-vps.toml")
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func hasServerFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--server" || strings.HasPrefix(arg, "--server=") {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

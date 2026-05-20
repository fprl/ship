package helper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/fprl/simple-vps/internal/systemd"
	"github.com/fprl/simple-vps/internal/utils"
)

type appCmd struct {
	Create        appCreateCmd        `cmd:"" help:"Create an app user and directories."`
	Destroy       appDestroyCmd       `cmd:"" help:"Destroy an app user and directories."`
	ReadEnv       appReadEnvCmd       `cmd:"read-env" help:"Read an app environment file."`
	InstallEnv    appInstallEnvCmd    `cmd:"install-env" help:"Install an app environment file."`
	InstallUnit   appInstallUnitCmd   `cmd:"install-unit" help:"Install an app systemd unit."`
	UninstallUnit appUninstallUnitCmd `cmd:"uninstall-unit" help:"Uninstall an app systemd unit."`
	DaemonReload  appDaemonReloadCmd  `cmd:"daemon-reload" help:"Reload systemd units."`
	Service       appServiceCmd       `cmd:"" help:"Run a systemctl action for an app service."`
	RunAs         appRunAsCmd         `cmd:"run-as" help:"Run a command as an app user."`
}

type appCreateCmd struct {
	Name string `arg:"" help:"App name."`
}

func (c appCreateCmd) Run() error {
	err := systemd.AppCreate(c.Name)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Printf("App %s is ready at %s\n", c.Name, systemd.AppPath(c.Name))
	return nil
}

type appDestroyCmd struct {
	Name string `arg:"" help:"App name."`
}

func (c appDestroyCmd) Run() error {
	err := systemd.AppDestroy(c.Name)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Printf("Removed app %s\n", c.Name)
	return nil
}

type appReadEnvCmd struct {
	Name string `arg:"" help:"App name."`
}

func (c appReadEnvCmd) Run() error {
	content, err := systemd.AppReadEnv(c.Name)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Print(content)
	return nil
}

type appInstallEnvCmd struct {
	Name string `arg:"" help:"App name."`
	Path string `arg:"" help:"Path to environment file."`
}

func (c appInstallEnvCmd) Run() error {
	err := systemd.AppInstallEnv(c.Name, c.Path)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Printf("Installed env for %s\n", c.Name)
	return nil
}

type appInstallUnitCmd struct {
	Name    string `arg:"" help:"App name."`
	Service string `arg:"" help:"Service name."`
	Path    string `arg:"" help:"Path to unit file."`
}

func (c appInstallUnitCmd) Run() error {
	err := systemd.AppInstallUnit(c.Name, c.Service, c.Path)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Printf("Installed %s\n", systemd.ServiceUnitName(c.Name, c.Service))
	return nil
}

type appUninstallUnitCmd struct {
	Name    string `arg:"" help:"App name."`
	Service string `arg:"" help:"Service name."`
}

func (c appUninstallUnitCmd) Run() error {
	err := systemd.AppUninstallUnit(c.Name, c.Service)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Printf("Removed %s\n", systemd.ServiceUnitName(c.Name, c.Service))
	return nil
}

type appDaemonReloadCmd struct{}

func (appDaemonReloadCmd) Run() error {
	err := systemd.AppDaemonReload()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Println("Reloaded systemd")
	return nil
}

type appServiceCmd struct {
	Action  string `arg:"" help:"Systemctl action."`
	Name    string `arg:"" help:"App name."`
	Service string `arg:"" help:"Service name."`
}

func (c appServiceCmd) Run() error {
	output, err := systemd.AppServiceAction(c.Action, c.Name, c.Service)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			fmt.Print(output)
			os.Exit(exitErr.ExitCode())
		}
		utils.Die(err.Error(), 1)
	}
	if output != "" {
		fmt.Print(output)
	}
	return nil
}

type appRunAsCmd struct {
	Name    string   `arg:"" help:"App name."`
	CWD     string   `name:"cwd" required:"" help:"Working directory."`
	Command []string `arg:"" optional:"" passthrough:"" help:"Command to run."`
}

func (c appRunAsCmd) Run() error {
	err := systemd.AppRunAs(c.Name, c.CWD, c.Command)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	return nil
}

package helper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/fprl/ship/internal/addressing"
	"github.com/fprl/ship/internal/cliargs"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/podmanruntime"
	"github.com/fprl/ship/internal/utils"
)

type appExecCmd struct {
	TTY     bool     `name:"tty" help:"Allocate a TTY for the one-off command."`
	App     string   `arg:"" help:"App name."`
	Env     string   `arg:"" help:"Env name."`
	Command []string `arg:"" passthrough:"" help:"Command and arguments to run."`
}

type execTarget struct {
	Release    string
	Activation string
	Image      string
	Context    *config.AppContext
	EnvFile    string
	Cleanup    func()
}

func (c appExecCmd) Run() error {
	if err := c.run(); err != nil {
		dieExecError(err, 1)
	}
	return nil
}

func (c appExecCmd) run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		return err
	}
	command := cliargs.TrimLeadingPassthroughSeparator(c.Command)
	if len(command) == 0 {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "server app exec requires a command",
			"command": "ship exec -- <cmd...>",
		})
	}
	authorizeOrDie(helperVerbExec, authTargetForAppEnv(c.App, c.Env, "exec", append([]string{"cmd"}, command...)...))

	target, err := resolveExecTarget(c.App, c.Env)
	if err != nil {
		return err
	}
	defer target.Cleanup()

	userID, groupID, err := hostUserIDs(identity.SystemUser(c.App, c.Env))
	if err != nil {
		return execOperationFailed(err)
	}
	previewEnv, err := isPreviewEnv(c.App, c.Env)
	if err != nil {
		return execOperationFailed(err)
	}
	envFileExists := target.EnvFile != ""

	name := identity.ContainerInstanceName(c.App, c.Env, "exec", target.Release, time.Now().UTC().Format("20060102t150405000000000z"))
	args := buildPodmanExecRunArgsWithActivation(c.App, c.Env, name, target.Image, userID, groupID, target.Release, target.Activation, command, execInjectedEnv(c.App, c.Env, target.Release, target.Context), envFileExists, previewEnv, c.TTY, target.EnvFile)
	return runPodmanExecContainer(args)
}

func resolveExecTarget(app, env string) (execTarget, error) {
	pointer, err := readActive(app, env)
	if err != nil {
		return execTarget{}, err
	}
	resolved, err := resolveArtifact(app, env, pointer.Artifact)
	if err != nil {
		return execTarget{}, execOperationFailed(err)
	}
	if !resolved.Context.NeedsImage || resolved.ImageID == "" {
		return execTarget{}, execOperationFailed(fmt.Errorf("release %s has no container image", pointer.Artifact.Release))
	}
	envFile := identity.ActivationEnvFile(app, env, pointer.Activation)
	if _, err := os.Stat(envFile); err != nil {
		return execTarget{}, errcat.New(errcat.CodeOperationFailed, errcat.Fields{
			"detail":  fmt.Sprintf("frozen environment for active activation %s is gone: %v", pointer.Activation, err),
			"command": "ship",
		})
	}
	return execTarget{
		Release:    pointer.Artifact.Release,
		Activation: pointer.Activation,
		Image:      resolved.ImageID,
		Context:    resolved.Context,
		EnvFile:    envFile,
		Cleanup:    func() {},
	}, nil
}

func execInjectedEnv(app, env, release string, ctx *config.AppContext) map[string]string {
	return shipInjectedEnv(app, env, release, ctx)
}

func shipInjectedEnv(app, env, release string, ctx *config.AppContext) map[string]string {
	kind := "production"
	branch := ctx.ProductionBranch
	if file, err := readEnvIdentity(app, env); err == nil && file.Preview != nil {
		kind = "preview"
		branch = file.Preview.Branch
	}
	return map[string]string{
		"SHIP_URL":     execDeploymentURL(ctx),
		"SHIP_BRANCH":  branch,
		"SHIP_ENV":     kind,
		"SHIP_RELEASE": release,
	}
}

func execDeploymentURL(ctx *config.AppContext) string {
	url, _ := addressing.PrimaryURL(ctx.Routes, true)
	return url
}

func buildPodmanExecRunArgsWithActivation(app, env, containerName, imageTag, userID, groupID, release, activation string, command []string, injected map[string]string, envFileExists, previewEnv, tty bool, envFile string) []string {
	resources := podmanruntime.EffectiveResources(config.Process{}.Resources, previewEnv)
	args := []string{
		"run", "--rm", "-i",
		"--name", containerName,
	}
	// Exec containers are interactive one-shots: --rm cleans them up,
	// no --restart is set, and they stay on the app network only. The
	// optional -t is added after the common baseline so tests can see it
	// only when a terminal is actually requested.
	args = append(args, podmanruntime.BaseRunArgs(podmanruntime.ContainerSpec{
		App:        app,
		Env:        env,
		Process:    "exec",
		UserID:     userID,
		GroupID:    groupID,
		Release:    release,
		Activation: activation,
		Networks:   []string{identity.Network(app, env)},
	})...)
	args = podmanruntime.WithReadOnlyRoot(args)
	if tty {
		args = append(args, "-t")
	}
	args = podmanruntime.WithResources(args, resources)
	if envFileExists && envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	for _, key := range sortedKeys(injected) {
		args = append(args, "--env", key+"="+injected[key])
	}
	args = append(args, imageTag)
	args = append(args, command...)
	return args
}

func runPodmanExecContainer(args []string) error {
	cmd := exec.Command("podman", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	return execOperationFailed(fmt.Errorf("podman run: %v", err))
}

func execOperationFailed(err error) error {
	if err == nil {
		return nil
	}
	if coded, ok := errcat.As(err); ok {
		return coded
	}
	return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  err.Error(),
		"command": "ship status",
	})
}

func dieExecError(err error, code int) {
	if coded, ok := errcat.As(err); ok {
		if coded.Code() == errcat.CodeUsageError || coded.Code() == errcat.CodeManifestInvalid {
			code = 2
		}
		fmt.Fprintln(os.Stderr, coded.JSONLine())
		os.Exit(code)
	}
	utils.DieError(err, code)
}

package helper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

type appExecCmd struct {
	TTY     bool     `name:"tty" help:"Allocate a TTY for the one-off command."`
	App     string   `arg:"" help:"App name."`
	Env     string   `arg:"" help:"Env name."`
	Command []string `arg:"" passthrough:"" help:"Command and arguments to run."`
}

type execTarget struct {
	Release string
	Image   string
	Context *config.AppContext
	Cleanup func()
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
	command := c.Command
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	if len(command) == 0 {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "server app exec requires a command",
			"command": "ship exec <cmd> [args...]",
		})
	}

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
	envFileExists := false
	if _, err := os.Stat(identity.EnvFile(c.App, c.Env)); err == nil {
		envFileExists = true
	} else if err != nil && !os.IsNotExist(err) {
		return execOperationFailed(fmt.Errorf("stat runtime env file: %v", err))
	}

	name := identity.ContainerInstanceName(c.App, c.Env, "exec", target.Release, time.Now().UTC().Format("20060102t150405000000000z"))
	args := buildPodmanExecRunArgs(c.App, c.Env, name, target.Image, userID, groupID, target.Release, command, execInjectedEnv(c.App, c.Env, target.Release, target.Context), envFileExists, previewEnv, c.TTY)
	return runPodmanExecContainer(args)
}

func resolveExecTarget(app, env string) (execTarget, error) {
	entry, err := readLatestSuccessfulDeployJournalEntry(app, env)
	if err != nil {
		return execTarget{}, err
	}
	release := entry.AttemptedRelease
	if release == "" {
		return execTarget{}, noDeployJournalError(app, env)
	}
	ctx, cleanup, err := loadReleaseAppContext(app, env, release)
	if err != nil {
		return execTarget{}, execOperationFailed(err)
	}
	if !ctx.NeedsImage {
		cleanup()
		return execTarget{}, execOperationFailed(fmt.Errorf("release %s has no container image", release))
	}
	if !execImageAvailable(app, env, release) {
		cleanup()
		return execTarget{}, execOperationFailed(fmt.Errorf("release %s image is not available locally", release))
	}
	return execTarget{
		Release: release,
		Image:   identity.ImageTag(app, env, release),
		Context: ctx,
		Cleanup: cleanup,
	}, nil
}

func execImageAvailable(app, env, release string) bool {
	images, err := podmanImages(app, env)
	if err != nil {
		return false
	}
	for _, image := range images {
		if image.Release == release {
			return true
		}
	}
	return false
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
	type candidate struct {
		rank int
		url  string
	}
	var candidates []candidate
	for _, route := range ctx.Routes {
		if route.Host == "" {
			continue
		}
		rank := 3
		switch {
		case route.Process == "web" && route.Path == "":
			rank = 0
		case route.Path == "":
			rank = 1
		case route.Process == "web":
			rank = 2
		}
		candidates = append(candidates, candidate{rank: rank, url: "https://" + route.Host + route.Path})
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].url < candidates[j].url
	})
	return candidates[0].url
}

func buildPodmanExecRunArgs(app, env, containerName, imageTag, userID, groupID, release string, command []string, injected map[string]string, envFileExists, previewEnv, tty bool) []string {
	resources := effectiveProcessResources(config.Process{}, previewEnv)
	args := []string{
		"run", "--rm", "-i",
		"--name", containerName,
	}
	// Exec containers are interactive one-shots: --rm cleans them up,
	// no --restart is set, and they stay on the app network only. The
	// optional -t is added after the common baseline so tests can see it
	// only when a terminal is actually requested.
	args = append(args, podmanBaseRunArgs(podmanBaseRunOptions{
		App:         app,
		Env:         env,
		ProcessName: "exec",
		UserID:      userID,
		GroupID:     groupID,
		Release:     release,
		Networks:    []string{identity.Network(app, env)},
	})...)
	args = appendReadOnlyRuntimeArgs(args)
	if tty {
		args = append(args, "-t")
	}
	args = appendResourceArgs(args, resources)
	if envFileExists {
		args = append(args, "--env-file", identity.EnvFile(app, env))
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

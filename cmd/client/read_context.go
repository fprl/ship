package client

import (
	"fmt"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/names"
	"strings"
)

func resolveDeployPreviewEnv(runner sshRunner, ctx *config.AppContext, address deployAddress) (string, error) {
	if address.PreviewBranch == "" {
		return address.EnvName, nil
	}
	return resolvePreviewEnv(runner, ctx, address.PreviewBranch, true)
}

func resolveReadPreviewEnv(runner sshRunner, ctx *config.AppContext, address readAddress) (string, error) {
	if address.PreviewBranch == "" {
		return address.EnvName, nil
	}
	return resolvePreviewEnv(runner, ctx, address.PreviewBranch, false)
}

func resolvePreviewEnv(runner sshRunner, ctx *config.AppContext, branch string, create bool) (string, error) {
	command := serverAppPreviewResolveCommand(ctx.AppName, branch)
	if create {
		command = serverAppPreviewResolveOrCreateCommand(ctx.AppName, branch)
	}
	remediation := "ship status"
	if create {
		remediation = "ship"
	}
	out, err := runSSHDetail(runner, ctx.Server, command, remediation)
	if err != nil {
		return "", err
	}
	env := strings.TrimSpace(out)
	if !names.EnvRe.MatchString(env) {
		return "", operationError(fmt.Sprintf("preview resolver returned invalid env name: %q", env), "ship box doctor")
	}
	return env, nil
}

type readContext struct {
	AppContext *config.AppContext
	Address    readAddress
	EnvName    string
	Runner     *CommandRunner
}

func currentReadContext(root, command string) (readContext, error) {
	return currentReadContextForBranch(root, command, "")
}

func currentReadContextForBranch(root, command, branch string) (readContext, error) {
	address, err := resolveReadAddress(root, branch, command)
	if err != nil {
		return readContext{}, err
	}
	baseEnv := baseEnvForPreview(address.EnvName, address.PreviewBranch)
	ctx, err := config.LoadAppContext(root, baseEnv)
	if err != nil {
		return readContext{}, err
	}
	runner, err := NewCommandRunner()
	if err != nil {
		return readContext{}, err
	}
	resolvedEnv, err := resolveReadPreviewEnv(runner, ctx, address)
	if err != nil {
		runner.Close()
		return readContext{}, err
	}
	ctx, err = config.LoadAppContext(root, resolvedEnv)
	if err != nil {
		runner.Close()
		return readContext{}, err
	}
	return readContext{AppContext: ctx, Address: address, EnvName: resolvedEnv, Runner: runner}, nil
}

func baseEnvForPreview(envName, previewBranch string) string {
	if previewBranch != "" {
		return productionEnvName
	}
	return envName
}

func readSurface(read readContext) (string, string) {
	if read.Address.ProductionBranch {
		return "Production", read.AppContext.ProductionBranch
	}
	return "Preview", read.Address.PreviewBranch
}

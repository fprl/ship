package client

import (
	"fmt"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/utils"
	"os"
	"regexp"
	"strings"
)

func CmdRollback(root string, release string) {
	read, err := currentReadContext(root, "rollback")
	if err != nil {
		utils.DieError(err, 1)
	}
	defer read.Runner.Close()

	actor := deployIdentity(root, read.Runner, read.AppContext.Server)
	out := runSSHChecked(read.Runner, read.AppContext.Server, serverAppRollbackCommand(read.AppContext.AppName, read.EnvName, release, actor), "rollback failed", "ship rollback "+release)
	fmt.Print(rewriteRollbackSummary(out, read))
}

func rewriteRollbackSummary(out string, read readContext) string {
	return rewriteEnvSummary(out, read, "Rolled back")
}

func rewriteEnvSummary(out string, read readContext, verb string) string {
	kind, branch := readSurface(read)
	prefix := fmt.Sprintf("%s %s (%s) ", verb, read.AppContext.AppName, read.EnvName)
	replacement := fmt.Sprintf("%s %s %s ", verb, kind, branch)
	return strings.Replace(out, prefix, replacement, 1)
}

func CmdRm(root string, branch string, confirm string) {
	address, err := resolveReadAddress(root, branch, "rm")
	if err != nil {
		utils.DieError(err, 1)
	}
	baseCtx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		utils.DieError(err, 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	envName := address.EnvName
	kind := "Production"
	displayBranch := baseCtx.ProductionBranch
	if address.PreviewBranch != "" {
		kind = "Preview"
		displayBranch = address.PreviewBranch
		envName, err = resolveReadPreviewEnv(runner, baseCtx, address)
		if err != nil {
			utils.DieError(err, 1)
		}
	} else if confirm != baseCtx.AppName {
		utils.DieError(errcat.New(errcat.CodeRmConfirmationRequired, errcat.Fields{
			"app":    baseCtx.AppName,
			"branch": displayBranch,
		}), 1)
	}

	if _, err := runSSHRequired(runner, baseCtx.Server, serverAppDestroyEnvCommand(baseCtx.AppName, envName), "rm failed", "ship rm "+displayBranch); err != nil {
		utils.DieError(err, 1)
	}
	fmt.Printf("Removed %s %s\n", kind, displayBranch)
}

var stdinIsTerminal = func() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && isTerminalFD(os.Stdin.Fd())
}

func CmdExec(root, branch string, command []string) {
	if len(command) == 0 {
		utils.DieError(errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "ship exec requires a command",
			"command": "ship exec <cmd> [args...]",
		}), 2)
	}
	read, err := currentReadContextForBranch(root, "exec", branch)
	if err != nil {
		utils.DieError(err, 1)
	}

	tty := stdinIsTerminal()
	cmdStr := serverAppExecCommand(read.AppContext.AppName, read.EnvName, tty, command)
	code, runErr := read.Runner.RunSSHPassthroughExitCode(read.AppContext.Server, cmdStr, tty)
	read.Runner.Close()
	if runErr != nil {
		utils.DieError(runErr, 1)
	}
	if code != 0 {
		os.Exit(code)
	}
}

func CmdPreviewPin(root string, branch string, pinned bool) {
	ctx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		utils.DieError(err, 1)
	}
	if branch == ctx.ProductionBranch {
		command := "ship preview pin <preview-branch>"
		if !pinned {
			command = "ship preview unpin <preview-branch>"
		}
		utils.DieError(errcat.New(errcat.CodeProductionBranchNotPreview, errcat.Fields{
			"branch":  fmt.Sprintf("%q", branch),
			"command": command,
		}), 1)
	}
	previewBranch := names.SanitizeBranchEnvName(branch)
	if previewBranch == "" {
		utils.DieError(errcat.New(errcat.CodeUnmappableBranchName, errcat.Fields{
			"branch": fmt.Sprintf("%q", branch),
		}), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	command := serverAppPreviewPinCommand(ctx.AppName, branch)
	if !pinned {
		command = serverAppPreviewUnpinCommand(ctx.AppName, branch)
	}
	out, err := runSSHDetail(runner, ctx.Server, command)
	if err != nil {
		utils.DieError(err, 1)
	}
	_ = out
	if pinned {
		fmt.Printf("Pinned Preview %s\n", branch)
		return
	}
	fmt.Printf("Unpinned Preview %s\n", branch)
}

// envKeyValid mirrors `secrets.SecretKeyRe` without taking a dep on
// the helper-only `internal/secrets` package — keeps the client
// binary's surface narrow.
func envKeyValid(key string) error {
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(key) {
		return errcat.New(errcat.CodeInvalidSecretKey, errcat.Fields{"key": fmt.Sprintf("%q", key)})
	}
	return nil
}

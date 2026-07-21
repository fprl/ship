package client

import (
	"fmt"
	"strings"

	"github.com/fprl/ship/internal/deploybundle"
	"github.com/fprl/ship/internal/deployrequest"
	"github.com/fprl/ship/internal/remoteprotocol"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
)

func serverCommand(args ...string) string {
	protocolArgs := remoteprotocol.ClientArgs(version.Version, args...)
	if _, err := remoteprotocol.Parse(protocolArgs); err != nil {
		panic("client remote command is absent from the protocol catalogue: " + err.Error())
	}
	return renderServerCommand(protocolArgs...)
}

func serverRepairCommand(args ...string) string {
	if _, err := remoteprotocol.Parse(args); err != nil {
		panic("repair command is absent from the protocol catalogue: " + err.Error())
	}
	return renderServerCommand(args...)
}

func renderServerCommand(args ...string) string {
	parts := []string{"sudo", "-n", "/usr/local/bin/ship", "server"}
	for _, arg := range args {
		parts = append(parts, utils.ShellEscape(arg))
	}
	return strings.Join(parts, " ")
}

func serverDoctorCommand(server string, jsonFlag bool) string {
	args := []string{"doctor", "--box-target", server}
	if jsonFlag {
		args = append(args, "--json")
	}
	return serverCommand(args...)
}

func serverVersionCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverRepairCommand("version", "--json")
	}
	return serverRepairCommand("version")
}

func serverBoxStatusCommand() string {
	return serverRepairCommand("version", "--json", "--summary")
}

func serverUpdateCommand(version string) string {
	return serverRepairCommand("update", "--version", version)
}

func serverAppSetupEnvCommand(appName string, envName string) string {
	return serverCommand("app", "setup-env", appName, envName)
}

func serverAppPreflightJSONCommand(appName string, envName string, requiredSecrets []string) string {
	args := []string{"app", "preflight", "--json"}
	for _, secret := range requiredSecrets {
		args = append(args, "--secret", secret)
	}
	args = append(args, appName, envName)
	return serverCommand(args...)
}

type deployIdentityJSON = deployrequest.Actor

func serverAppApplyCommand(appName string, envName string, bundle deploybundle.Metadata, plan localDeployPlan, actor deployIdentityJSON, rebuild bool, logs bool, tlsMode string, previewAlias string) string {
	request := deployrequest.Request{
		App: appName, Env: envName, Bundle: bundle, SHA: plan.Release,
		Dirty: plan.Dirty, BaseCommit: plan.BaseCommit,
		CreatedAt: plan.CreatedAt.Format(timeRFC3339UTC), Rebuild: rebuild,
		Progress: true, Logs: logs, TLS: tlsMode, PreviewAlias: previewAlias,
		Actor: actor,
	}
	return serverCommand(request.CommandArgs()...)
}

func serverAppStatusCommand(appName, envName string) string {
	return serverCommand("app", "status", "--json", appName, envName)
}

func serverAppConvergeCommand(appName, envName string, jsonFlag bool) string {
	args := []string{"app", "converge"}
	if jsonFlag {
		args = append(args, "--json")
	}
	return serverCommand(append(args, appName, envName)...)
}

func serverGCCommand(jsonFlag bool) string {
	args := []string{"gc"}
	if jsonFlag {
		args = append(args, "--json")
	}
	return serverCommand(args...)
}

func serverAppLsCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("app", "ls", "--json")
	}
	return serverCommand("app", "ls")
}

func serverAppDestroyCommand(appName string) string {
	return serverCommand("app", "destroy", appName)
}

func serverKeyAddCommand(name string, role string) string {
	return serverCommand("key", "add", "--name", name, "--role", role)
}

func serverKeyListCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("key", "ls", "--json")
	}
	return serverCommand("key", "ls")
}

func serverKeyRmCommand(name string, keyArg ...string) string {
	args := []string{"key", "rm", name}
	if len(keyArg) > 0 && keyArg[0] != "" {
		args = append(args, "--key", keyArg[0])
	}
	return serverCommand(args...)
}

func serverKeyRenameCommand(oldName, newName string) string {
	return serverCommand("key", "rename", oldName, newName)
}

func serverKeyRoleCommand(name, role string) string {
	return serverCommand("key", "role", name, role)
}

func serverApprovalLsCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("approval", "ls", "--json")
	}
	return serverCommand("approval", "ls")
}

func serverApprovalGrantCommand(id string) string {
	return serverCommand("approval", "grant", id)
}

func serverBoxWebhookGetCommand() string {
	return serverCommand("webhook", "get")
}

func serverBoxWebhookSetCommand(url string) string {
	return serverCommand("webhook", "set", url)
}

func serverBoxWebhookClearCommand() string {
	return serverCommand("webhook", "clear")
}

func serverBoxConfigGetCommand() string {
	return serverCommand("config", "get")
}

func serverBoxConfigSetCommand(key, value string) string {
	return serverCommand("config", "set", key, value)
}

func serverBoxConfigUnsetCommand(key string) string {
	return serverCommand("config", "unset", key)
}

func serverAppLogsCommand(appName, envName, process string, follow bool, tail *int) string {
	args := []string{"app", "logs"}
	if follow {
		args = append(args, "--follow")
	}
	if tail != nil {
		args = append(args, fmt.Sprintf("--tail=%d", *tail))
	}
	args = append(args, appName, envName)
	if process != "" {
		args = append(args, process)
	}
	return serverCommand(args...)
}

// ValidateLogsTail keeps the client-side flag contract separate from the
// helper default: nil means the flag was omitted, while zero is valid.
func ValidateLogsTail(tail *int) error {
	if tail != nil && *tail < 0 {
		return usageError("--tail must be zero or greater", "ship logs --tail 0")
	}
	return nil
}

func serverAppExecCommand(appName, envName string, tty bool, command []string) string {
	args := []string{"app", "exec"}
	if tty {
		args = append(args, "--tty")
	}
	args = append(args, appName, envName, "--")
	args = append(args, command...)
	return serverCommand(args...)
}

func serverAppRollbackCommand(appName, envName, release string, actor deployIdentityJSON) string {
	args := []string{"app", "rollback"}
	args = append(args,
		"--ssh-key-comment", actor.SSHKeyComment,
		"--git-author", actor.GitAuthor,
	)
	args = append(args, appName, envName)
	if release != "" {
		args = append(args, release)
	}
	return serverCommand(args...)
}

func serverAppDestroyEnvCommand(appName, envName string) string {
	args := []string{"app", "destroy-env", "--purge"}
	args = append(args, appName, envName)
	return serverCommand(args...)
}

func serverAppPreviewResolveOrCreateCommand(appName, branch string) string {
	return serverCommand("app", "preview", "resolve-or-create", appName, branch)
}

func serverAppPreviewResolveCommand(appName, branch string) string {
	return serverCommand("app", "preview", "resolve", appName, branch)
}

func serverAppPreviewPinCommand(appName, branch string) string {
	return serverCommand("app", "preview", "pin", appName, branch)
}

func serverAppPreviewUnpinCommand(appName, branch string) string {
	return serverCommand("app", "preview", "unpin", appName, branch)
}

func serverAppPreviewShareCommand(appName, envName string, rotate bool) string {
	args := []string{"app", "preview", "share"}
	if rotate {
		args = append(args, "--rotate")
	}
	args = append(args, appName, envName)
	return serverCommand(args...)
}

func serverAppDataForkCommand(appName, previewEnv string) string {
	return serverCommand("app", "data", "fork", appName, previewEnv)
}

func serverAppDataResetCommand(appName, previewEnv string) string {
	return serverCommand("app", "data", "reset", appName, previewEnv)
}

func serverAppDataSaveCommand(appName, envName string) string {
	return serverCommand("app", "data", "save", appName, envName)
}

func serverAppDataRestoreCommand(appName, envName, archive string) string {
	return serverCommand("app", "data", "restore", "--archive", archive, appName, envName)
}

func serverAppSecretSetCommand(appName, envName, key string) string {
	return serverCommand("app", "secret", "set", appName, envName, key)
}

func serverAppSecretListCommand(appName, envName string, jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("app", "secret", "list", "--json", appName, envName)
	}
	return serverCommand("app", "secret", "list", appName, envName)
}

func serverAppSecretRmCommand(appName, envName, key string) string {
	return serverCommand("app", "secret", "rm", appName, envName, key)
}

func serverAppWhyCommand(appName, envName string) string {
	return serverCommand("app", "why", appName, envName)
}

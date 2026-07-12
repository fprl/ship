package client

import (
	"fmt"
	"strings"

	"github.com/fprl/ship/internal/utils"
)

func serverCommand(args ...string) string {
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

type deployIdentityJSON struct {
	SSHKeyComment string `json:"ssh_key_comment"`
	GitAuthor     string `json:"git_author"`
}

func serverAppApplyCommand(appName string, envName string, tarballPath string, manifestPath string, plan localDeployPlan, actor deployIdentityJSON, rebuild bool, tlsMode string) string {
	args := []string{"app", "apply"}
	if rebuild {
		args = append(args, "--rebuild")
	}
	if tlsMode != "" {
		args = append(args, "--tls", tlsMode)
	}
	if plan.Dirty {
		args = append(args, "--dirty")
	}
	args = append(args,
		"--tarball", tarballPath,
		"--manifest", manifestPath,
		"--sha", plan.Release,
		"--base-commit", plan.BaseCommit,
		"--created-at", plan.CreatedAt.Format(timeRFC3339UTC),
		"--ssh-key-comment", actor.SSHKeyComment,
		"--git-author", actor.GitAuthor,
		appName, envName,
	)
	return serverCommand(args...)
}

func serverAppStatusCommand(appName, envName string) string {
	return serverCommand("app", "status", "--json", appName, envName)
}

func serverAppListCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("app", "list", "--json")
	}
	return serverCommand("app", "list")
}

func serverAppDestroyCommand(appName string) string {
	return serverCommand("app", "destroy", appName)
}

func serverKeyAddCommand(comment string, role string) string {
	return serverCommand("key", "add", "--comment", comment, "--role", role)
}

func serverKeyListCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("key", "ls", "--json")
	}
	return serverCommand("key", "ls")
}

func serverKeyRmCommand(name string) string {
	return serverCommand("key", "rm", name)
}

func serverApprovalListCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("approval", "list", "--json")
	}
	return serverCommand("approval", "list")
}

func serverApprovalApproveCommand(id string) string {
	return serverCommand("approval", "approve", id)
}

func serverAppLogsCommand(appName, envName, process string, follow bool, tail int) string {
	args := []string{"app", "logs"}
	if follow {
		args = append(args, "--follow")
	}
	if tail > 0 && !follow {
		args = append(args, fmt.Sprintf("--tail=%d", tail))
	}
	args = append(args, appName, envName)
	if process != "" {
		args = append(args, process)
	}
	return serverCommand(args...)
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

func serverAppBackupCommand(appName, envName, dest string) string {
	args := []string{"app", "backup", "create"}
	if dest != "" {
		args = append(args, "--to", dest)
	}
	args = append(args, appName, envName)
	return serverCommand(args...)
}

func serverAppRestoreCommand(appName, envName, from string) string {
	args := []string{"app", "backup", "restore", "--from", from}
	args = append(args, appName, envName)
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

func serverAppDataForkCommand(appName, prodEnv, previewEnv string) string {
	return serverCommand("app", "data", "fork", appName, prodEnv, previewEnv)
}

func serverAppDataRmCommand(appName, previewEnv string) string {
	return serverCommand("app", "data", "rm", appName, previewEnv)
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

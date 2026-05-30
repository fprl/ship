package client

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/names"
	"github.com/fprl/simple-vps/internal/utils"
)

const (
	ManifestFile       = "simple-vps.toml"
	RemoteDeployTmpDir = "/tmp/simple-vps-deploy"
)

type CommandRunner struct {
	SshOptions       []string
	RsyncRemoteShell string
	TempDir          string
}

func NewCommandRunner() (*CommandRunner, error) {
	sshOpts := []string{"-o", "BatchMode=yes"}
	key := os.Getenv("SIMPLE_VPS_SSH_KEY")
	if key == "" {
		return &CommandRunner{
			SshOptions:       sshOpts,
			RsyncRemoteShell: sshRemoteShell(sshOpts),
		}, nil
	}
	knownHosts := os.Getenv("SIMPLE_VPS_KNOWN_HOSTS")
	if knownHosts == "" {
		return nil, errors.New("SIMPLE_VPS_KNOWN_HOSTS is required when SIMPLE_VPS_SSH_KEY is set")
	}

	dir, err := os.MkdirTemp("", "simple-vps-ssh-")
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "id")
	knownHostsPath := filepath.Join(dir, "known_hosts")

	ensureNL := func(s string) string {
		if !strings.HasSuffix(s, "\n") {
			return s + "\n"
		}
		return s
	}

	if err := os.WriteFile(keyPath, []byte(ensureNL(key)), 0600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := os.WriteFile(knownHostsPath, []byte(ensureNL(knownHosts)), 0600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	sshOpts = append(sshOpts,
		"-i", keyPath,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile="+knownHostsPath,
	)

	return &CommandRunner{
		SshOptions:       sshOpts,
		RsyncRemoteShell: sshRemoteShell(sshOpts),
		TempDir:          dir,
	}, nil
}

func sshRemoteShell(sshOpts []string) string {
	if len(sshOpts) == 0 {
		return ""
	}
	var escOpts []string
	for _, opt := range sshOpts {
		escOpts = append(escOpts, utils.ShellEscape(opt))
	}
	return "ssh " + strings.Join(escOpts, " ")
}

func (r *CommandRunner) Close() {
	if r.TempDir != "" {
		_ = os.RemoveAll(r.TempDir)
	}
}

func (r *CommandRunner) RunSSH(server string, command string) (string, string, int, error) {
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	args = append(args, server, command)
	return runCommand("ssh", args, "")
}

// RunSSHWithStdin pipes `stdin` to the remote command and captures
// stdout/stderr/exit. Used by `simple-vps secret set` so the secret
// value never lands in argv, the host process table, or shell
// history — it crosses the wire on the helper's stdin and goes
// straight to disk on the other side.
func (r *CommandRunner) RunSSHWithStdin(server string, command string, stdin []byte) (string, string, int, error) {
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	args = append(args, server, command)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return stdout.String(), stderr.String(), code, err
}

func (r *CommandRunner) RunSSHPassthrough(server string, command string) error {
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	if command != "" {
		args = append(args, server, command)
	} else {
		args = append(args, server)
	}
	return runCommandPassthrough("ssh", args)
}

func (r *CommandRunner) Upload(local string, remote string, server string) error {
	var args []string
	if r.RsyncRemoteShell != "" {
		args = append(args, "-e", r.RsyncRemoteShell)
	}
	args = append(args, "-az", local, fmt.Sprintf("%s:%s", server, remote))
	_, stderr, code, err := runCommand("rsync", args, "")
	if err != nil || code != 0 {
		return fmt.Errorf("rsync failed (exit %d): %s", code, stderr)
	}
	return nil
}

func runCommand(name string, args []string, dir string) (string, string, int, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return stdout.String(), stderr.String(), code, err
}

func runCommandPassthrough(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type sshRunner interface {
	RunSSH(server string, command string) (string, string, int, error)
}

func runSSHRequired(runner sshRunner, server string, command string, errMsg string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail != "" {
			return "", fmt.Errorf("%s: %s", errMsg, detail)
		}
		return "", fmt.Errorf("%s", errMsg)
	}
	return stdout, nil
}

func runSSHChecked(runner sshRunner, server string, command string, errMsg string) string {
	stdout, err := runSSHRequired(runner, server, command, errMsg)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	return stdout
}

func serverCommand(args ...string) string {
	parts := []string{"sudo", "-n", "/usr/local/bin/simple-vps", "server"}
	for _, arg := range args {
		parts = append(parts, utils.ShellEscape(arg))
	}
	return strings.Join(parts, " ")
}

func serverStatusCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("status", "--json")
	}
	return serverCommand("status")
}

func serverDoctorCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("doctor", "--json")
	}
	return serverCommand("doctor")
}

func serverAppSetupEnvCommand(appName string, envName string) string {
	return serverCommand("app", "setup-env", appName, envName)
}

func serverAppPreflightCommand(appName string, envName string, requiredSecrets []string) string {
	return serverAppPreflightCommandWithJSON(appName, envName, requiredSecrets, false)
}

func serverAppPreflightJSONCommand(appName string, envName string, requiredSecrets []string) string {
	return serverAppPreflightCommandWithJSON(appName, envName, requiredSecrets, true)
}

func serverAppPreflightCommandWithJSON(appName string, envName string, requiredSecrets []string, jsonFlag bool) string {
	args := []string{"app", "preflight"}
	if jsonFlag {
		args = append(args, "--json")
	}
	for _, secret := range requiredSecrets {
		args = append(args, "--secret", secret)
	}
	args = append(args, appName, envName)
	return serverCommand(args...)
}

func serverAppApplyCommand(appName string, envName string, tarballPath string, manifestPath string, plan localDeployPlan, rebuild bool) string {
	args := []string{"app", "apply"}
	if rebuild {
		args = append(args, "--rebuild")
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
		appName, envName,
	)
	return serverCommand(args...)
}

func serverAppStatusCommand(appName, envName string, jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("app", "status", "--json", appName, envName)
	}
	return serverCommand("app", "status", appName, envName)
}

func serverAppListCommand(jsonFlag bool) string {
	if jsonFlag {
		return serverCommand("app", "list", "--json")
	}
	return serverCommand("app", "list")
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

func serverAppRestartCommand(appName, envName, process string) string {
	args := []string{"app", "restart"}
	args = append(args, appName, envName)
	if process != "" {
		args = append(args, process)
	}
	return serverCommand(args...)
}

func serverAppRollbackCommand(appName, envName, release string) string {
	args := []string{"app", "rollback"}
	args = append(args, appName, envName)
	if release != "" {
		args = append(args, release)
	}
	return serverCommand(args...)
}

func serverAppBackupCommand(appName, envName, dest string, jsonFlag bool) string {
	args := []string{"app", "backup", "create"}
	if jsonFlag {
		args = append(args, "--json")
	}
	if dest != "" {
		args = append(args, "--to", dest)
	}
	args = append(args, appName, envName)
	return serverCommand(args...)
}

func serverAppBackupListCommand(appName, envName string, jsonFlag bool) string {
	args := []string{"app", "backup", "list"}
	if jsonFlag {
		args = append(args, "--json")
	}
	args = append(args, appName, envName)
	return serverCommand(args...)
}

func serverAppBackupRmCommand(appName, envName, id string) string {
	return serverCommand("app", "backup", "rm", appName, envName, id)
}

func serverAppRestoreCommand(appName, envName, from string, dryRun bool) string {
	args := []string{"app", "backup", "restore", "--from", from}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, appName, envName)
	return serverCommand(args...)
}

func serverAppDestroyEnvCommand(appName, envName string, purge bool) string {
	args := []string{"app", "destroy-env"}
	if purge {
		args = append(args, "--purge")
	}
	args = append(args, appName, envName)
	return serverCommand(args...)
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

func CmdCheck(root string, envName string) {
	diags, err := checkDiagnostics(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	diags.print()
	if diags.hasErrors() {
		os.Exit(1)
	}
	if envName != "" {
		fmt.Printf("Local deploy checks passed for env %s.\n", envName)
	} else {
		fmt.Println("Manifest simple-vps.toml is valid.")
	}
}

func CmdSetup(root string, envName string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	// 1. check SSH
	runSSHChecked(runner, ctx.Server, "true", fmt.Sprintf("SSH failed for %s", ctx.Server))

	// 2. check required server tools. Per ADR-0005 language runtimes
	// live in the container image, not on the host. The host supplies
	// simple-vps + rsync only; Podman/Caddy are checked by the
	// installer, not per-deploy.
	runSSHChecked(runner, ctx.Server, "test -x /usr/local/bin/simple-vps", "missing Simple VPS server API at /usr/local/bin/simple-vps; rerun the Simple VPS install")
	runSSHChecked(runner, ctx.Server, "command -v rsync", "missing required server tool: rsync")

	// 3. create per-env user, dirs, and Podman network
	runSSHChecked(runner, ctx.Server, serverAppSetupEnvCommand(ctx.AppName, envName), "simple-vps server app setup-env failed")
	fmt.Printf("Setup complete for %s (%s)\n", ctx.AppName, envName)
}

func CmdSSH(root string, envName string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	err = runner.RunSSHPassthrough(ctx.Server, "")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
}

func CmdStatus(root string, envName string, jsonFlag bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppStatusCommand(ctx.AppName, envName, jsonFlag), "status failed")
	// Pass the helper's output through unchanged so `--json` produces
	// pipeable JSON and the text mode keeps its line breaks.
	fmt.Print(out)
}

func CmdAppList(server string, jsonFlag bool) {
	if server == "" {
		utils.Die("--server is required", 1)
	}
	if !config.ValidateSshTarget(server) {
		utils.Die("--server must be an SSH target like deploy@example.com", 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppListCommand(jsonFlag), "app list failed")
	fmt.Print(out)
}

func CmdRestart(root string, envName string, process string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	// Restart shares status's plumbing: helper does the work and prints
	// the summary, so pass its text output through unchanged.
	out := runSSHChecked(runner, ctx.Server, serverAppRestartCommand(ctx.AppName, envName, process), "restart failed")
	fmt.Print(out)
}

func CmdRollback(root string, envName string, release string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppRollbackCommand(ctx.AppName, envName, release), "rollback failed")
	fmt.Print(out)
}

func CmdBackup(root string, envName string, dest string, jsonFlag bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppBackupCommand(ctx.AppName, envName, dest, jsonFlag), "backup failed")
	fmt.Print(out)
}

func CmdBackupList(root string, envName string, jsonFlag bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppBackupListCommand(ctx.AppName, envName, jsonFlag), "backup list failed")
	fmt.Print(out)
}

func CmdBackupRm(root string, envName string, id string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppBackupRmCommand(ctx.AppName, envName, id), "backup rm failed")
	fmt.Print(out)
}

func CmdRestore(root string, envName string, from string, dryRun bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	runSSHChecked(runner, ctx.Server, serverAppSetupEnvCommand(ctx.AppName, envName), "restore setup failed")
	out := runSSHChecked(runner, ctx.Server, serverAppRestoreCommand(ctx.AppName, envName, from, dryRun), "restore failed")
	fmt.Print(out)
}

func CmdDestroy(root string, envName string, confirm string, yes bool, purge bool, appOverride string, serverOverride string) {
	appName, server, err := destroyTarget(root, envName, appOverride, serverOverride)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := validateDestroyConfirmation(appName, confirm, yes); err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppDestroyEnvCommand(appName, envName, purge), "destroy failed")
	fmt.Print(out)
}

func destroyTarget(root, envName, appOverride, serverOverride string) (string, string, error) {
	if appOverride == "" && serverOverride == "" {
		ctx, err := config.LoadAppContext(root, envName)
		if err != nil {
			return "", "", err
		}
		return ctx.AppName, ctx.Server, nil
	}
	if appOverride == "" || serverOverride == "" {
		return "", "", errors.New("destroy with manifest-free targeting requires both --app and --server")
	}
	if !names.AppRe.MatchString(appOverride) {
		return "", "", fmt.Errorf("invalid app name: %q", appOverride)
	}
	if !names.EnvRe.MatchString(envName) {
		return "", "", fmt.Errorf("invalid env name: %q", envName)
	}
	if !config.ValidateSshTarget(serverOverride) {
		return "", "", errors.New("--server must be an SSH target like deploy@example.com")
	}
	return appOverride, serverOverride, nil
}

func validateDestroyConfirmation(appName, confirm string, yes bool) error {
	if yes {
		return nil
	}
	if confirm == appName {
		return nil
	}
	return fmt.Errorf("destroy requires --confirm %s or --yes", appName)
}

func CmdLogs(root string, envName string, process string, follow bool, tail int) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	// Follow mode needs interactive stdout/stderr passthrough so the
	// user sees the stream as it arrives. Non-follow mode reads a
	// bounded amount and prints once.
	cmdStr := serverAppLogsCommand(ctx.AppName, envName, process, follow, tail)
	if follow {
		if err := runner.RunSSHPassthrough(ctx.Server, cmdStr); err != nil {
			utils.Die(err.Error(), 1)
		}
		return
	}
	out := runSSHChecked(runner, ctx.Server, cmdStr, "logs failed")
	fmt.Print(out)
}

// secretValueFromStdin reads the secret value from this process's
// stdin and trims at most one trailing newline (the kind a tty `read`
// or an `echo` tacks on). Returns the bytes verbatim past that — so
// a multi-line heredoc with intentional newlines comes through
// intact.
func secretValueFromStdin() ([]byte, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("read secret value from stdin: %v", err)
	}
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data = data[:n-1]
	}
	return data, nil
}

func CmdSecretSet(root string, envName string, key string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := envKeyValid(key); err != nil {
		utils.Die(err.Error(), 1)
	}
	value, err := secretValueFromStdin()
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	// Pipe the value over the helper's stdin — never argv, never a
	// file on disk between hops. The helper writes it straight to
	// /etc/simple-vps/secrets/<app>/<env>/<key>.
	stdout, stderr, code, err := runner.RunSSHWithStdin(ctx.Server, serverAppSecretSetCommand(ctx.AppName, envName, key), value)
	if err != nil || code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail == "" {
			detail = "no error detail"
		}
		utils.Die(fmt.Sprintf("secret set failed: %s", detail), 1)
	}
	// Don't echo stdout — it'd carry the helper's confirmation
	// (which already names the key but not the value). Print our own.
	fmt.Printf("Stored secret %s for %s (%s). Run `simple-vps deploy --env %s` to apply.\n", key, ctx.AppName, envName, envName)
}

func CmdSecretList(root string, envName string, jsonFlag bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppSecretListCommand(ctx.AppName, envName, jsonFlag), "secret list failed")
	if jsonFlag {
		fmt.Print(out)
		return
	}
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		// No keys — print nothing rather than an explicit "no
		// secrets" line so the output stays pipeable.
		return
	}
	fmt.Println(out)
}

func CmdSecretRm(root string, envName string, key string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := envKeyValid(key); err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppSecretRmCommand(ctx.AppName, envName, key), "secret rm failed")
	// The helper prints either "Removed secret X for ..." or
	// "Secret X was not set for ..."; pass it through directly so
	// the user sees the difference.
	fmt.Print(out)
}

// envKeyValid mirrors `secrets.SecretKeyRe` without taking a dep on
// the helper-only `internal/secrets` package — keeps the client
// binary's surface narrow.
func envKeyValid(key string) error {
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(key) {
		return fmt.Errorf("invalid secret key %q: must match ^[A-Za-z_][A-Za-z0-9_]*$", key)
	}
	return nil
}

func CmdHostStatus(server string, jsonFlag bool) {
	if server == "" {
		utils.Die("--server is required", 1)
	}
	if !config.ValidateSshTarget(server) {
		utils.Die("--server must be an SSH target like deploy@example.com", 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverStatusCommand(jsonFlag), "failed to read server status")
	fmt.Print(out)
}

func CmdHostDoctor(server string, jsonFlag bool) {
	if server == "" {
		utils.Die("--server is required", 1)
	}
	if !config.ValidateSshTarget(server) {
		utils.Die("--server must be an SSH target like deploy@example.com", 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverDoctorCommand(jsonFlag))
	if err != nil || code != 0 {
		if jsonFlag && json.Valid([]byte(stdout)) {
			fmt.Print(stdout)
			os.Exit(1)
		}
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail != "" {
			utils.Die(fmt.Sprintf("failed to run doctor: %s", detail), 1)
		}
		utils.Die("failed to run doctor", 1)
	}
	fmt.Print(stdout)
}

func CmdDeploy(root string, envName string, dirty bool, rebuild bool, includeDotenv bool) {
	plan, diags, err := buildLocalDeployPlan(root, envName, localDeployOptions{
		AllowDirty:    dirty,
		IncludeDotenv: includeDotenv,
	})
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	diags.print()
	if diags.hasErrors() {
		os.Exit(1)
	}
	ctx := plan.Context

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()
	if err := deployRemotePreflight(runner, ctx); err != nil {
		utils.Die(err.Error(), 1)
	}

	// 1. Tar source locally (git archive for clean tree, working tree for --dirty).
	tarDir, err := os.MkdirTemp("", "simple-vps-deploy-")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer os.RemoveAll(tarDir)

	localTar := filepath.Join(tarDir, "source.tar")
	localManifest := filepath.Join(tarDir, "simple-vps.toml")
	if err := writeSourceTar(root, localTar, plan.Dirty, plan.ServeDirs); err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := copyFile(filepath.Join(root, ManifestFile), localManifest); err != nil {
		utils.Die(fmt.Sprintf("copy manifest: %v", err), 1)
	}

	// 2. Upload tarball + manifest to a per-deploy temp dir on the host.
	remoteDir := fmt.Sprintf("%s/%s-%s-%s", RemoteDeployTmpDir, ctx.AppName, envName, plan.Release)
	cleanupRemoteDir := func() {
		_, _, _, _ = runner.RunSSH(ctx.Server, fmt.Sprintf("rm -rf %s", utils.ShellEscape(remoteDir)))
	}
	failAfterRemoteDir := func(message string) {
		cleanupRemoteDir()
		utils.Die(message, 1)
	}
	if _, err := runSSHRequired(runner, ctx.Server, fmt.Sprintf("mkdir -p %s && chmod 0700 %s", utils.ShellEscape(remoteDir), utils.ShellEscape(remoteDir)), "failed to create remote deploy dir"); err != nil {
		failAfterRemoteDir(err.Error())
	}
	if err := runner.Upload(localTar, remoteDir+"/source.tar", ctx.Server); err != nil {
		failAfterRemoteDir(fmt.Sprintf("failed to upload source: %v", err))
	}
	if err := runner.Upload(localManifest, remoteDir+"/simple-vps.toml", ctx.Server); err != nil {
		failAfterRemoteDir(fmt.Sprintf("failed to upload manifest: %v", err))
	}

	// 3. Helper builds the image or snapshots static assets, then reloads Caddy.
	applyCmd := serverAppApplyCommand(ctx.AppName, envName,
		remoteDir+"/source.tar",
		remoteDir+"/simple-vps.toml",
		plan,
		rebuild,
	)
	if _, err := runSSHRequired(runner, ctx.Server, applyCmd, "deploy failed"); err != nil {
		failAfterRemoteDir(err.Error())
	}

	// 4. Best-effort cleanup of the upload dir.
	cleanupRemoteDir()

	fmt.Printf("Deployed %s (%s) at %s\n", ctx.AppName, envName, plan.Release)
}

func writeSourceTar(root string, dest string, dirty bool, staticDirs []string) error {
	if dirty {
		cmd := exec.Command("sh", "-c", fmt.Sprintf(
			"tar -C %s --exclude .git --exclude node_modules -cf %s .",
			utils.ShellEscape(root), utils.ShellEscape(dest),
		))
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	} else if err := writeCleanSourceTar(root, dest); err != nil {
		return err
	}
	if !dirty && len(staticDirs) > 0 {
		return appendStaticDirsToTar(root, dest, staticDirs)
	}
	return nil
}

func writeCleanSourceTar(root string, dest string) error {
	repoRoot, treeish, err := gitArchiveTreeish(root)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", repoRoot, "archive", "--format=tar", "-o", dest, treeish)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func gitArchiveTreeish(root string) (repoRoot string, treeish string, err error) {
	repoRootOut, stderr, code, _ := runCommand("git", []string{"rev-parse", "--show-toplevel"}, root)
	if code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "git rev-parse --show-toplevel failed"
		}
		return "", "", errors.New(detail)
	}
	prefixOut, stderr, code, _ := runCommand("git", []string{"rev-parse", "--show-prefix"}, root)
	if code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "git rev-parse --show-prefix failed"
		}
		return "", "", errors.New(detail)
	}
	repoRoot = strings.TrimSpace(repoRootOut)
	prefix := strings.Trim(strings.TrimSpace(prefixOut), "/")
	if repoRoot == "" {
		return "", "", fmt.Errorf("git rev-parse --show-toplevel returned an empty path")
	}
	if prefix == "" {
		return repoRoot, "HEAD", nil
	}
	return repoRoot, "HEAD:" + prefix, nil
}

func staticServeDirs(routes map[string]config.Route) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, route := range routes {
		if route.Serve == "" || seen[route.Serve] {
			continue
		}
		seen[route.Serve] = true
		dirs = append(dirs, route.Serve)
	}
	sort.Strings(dirs)
	return dirs
}

func appendStaticDirsToTar(root, dest string, dirs []string) error {
	for _, dir := range dirs {
		cmd := exec.Command("tar", "-C", root, "-rf", dest, dir)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("append static dir %s: %v", dir, err)
		}
	}
	return nil
}

func staticTreeHash(root string, dirs []string) (string, error) {
	sum := sha256.New()
	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		if err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			switch {
			case info.Mode().IsDir():
				_, _ = fmt.Fprintf(sum, "dir\x00%s\x00", rel)
			case info.Mode().IsRegular():
				_, _ = fmt.Fprintf(sum, "file\x00%s\x00%d\x00", rel, info.Size())
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				if _, err := io.Copy(sum, f); err != nil {
					_ = f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
			case info.Mode()&os.ModeSymlink != 0:
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(sum, "symlink\x00%s\x00%s\x00", rel, target)
			}
			return nil
		}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func validateDeployArtifactDotenv(root string, dirty bool, staticDirs []string) error {
	if dirty {
		return validateArtifactDotenv(root)
	}
	var dotenvs []string
	tracked, err := cleanArtifactFiles(root)
	if err != nil {
		return err
	}
	for _, rel := range tracked {
		if blockedDotenv(rel) {
			dotenvs = append(dotenvs, rel)
		}
	}
	staticDotenvs, err := dotenvsInStaticDirs(root, staticDirs)
	if err != nil {
		return err
	}
	dotenvs = append(dotenvs, staticDotenvs...)
	return dotenvError(dotenvs)
}

func cleanArtifactFiles(root string) ([]string, error) {
	repoRoot, treeish, err := gitArchiveTreeish(root)
	if err != nil {
		return nil, err
	}
	out, stderr, code, _ := runCommand("git", []string{"-C", repoRoot, "ls-tree", "-r", "--name-only", "-z", treeish}, "")
	if code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "git ls-tree failed"
		}
		return nil, errors.New(detail)
	}
	var files []string
	for _, path := range strings.Split(out, "\x00") {
		if path == "" {
			continue
		}
		files = append(files, filepath.ToSlash(path))
	}
	return files, nil
}

func dotenvsInStaticDirs(root string, dirs []string) ([]string, error) {
	seen := map[string]bool{}
	var dotenvs []string
	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		if err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if blockedDotenv(rel) && !seen[rel] {
				seen[rel] = true
				dotenvs = append(dotenvs, rel)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return dotenvs, nil
}

func validateArtifactDotenv(artifactDir string) error {
	var dotenvs []string
	err := filepath.Walk(artifactDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		name := filepath.Base(path)
		if strings.HasPrefix(name, ".env") && !allowedDotenvName(name) {
			rel, relErr := filepath.Rel(artifactDir, path)
			if relErr != nil {
				return relErr
			}
			dotenvs = append(dotenvs, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return err
	}
	return dotenvError(dotenvs)
}

func blockedDotenv(rel string) bool {
	name := filepath.Base(rel)
	return strings.HasPrefix(name, ".env") && !allowedDotenvName(name)
}

func allowedDotenvName(name string) bool {
	switch name {
	case ".env.example", ".env.sample", ".env.defaults":
		return true
	default:
		return false
	}
}

func dotenvError(dotenvs []string) error {
	if len(dotenvs) == 0 {
		return nil
	}
	dotenvs = uniqueStrings(dotenvs)
	sort.Strings(dotenvs)
	if len(dotenvs) > 0 {
		return fmt.Errorf("refusing to deploy dotenv file: %s; use --include-dotenv to bypass", strings.Join(dotenvs, ", "))
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

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
	"time"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/names"
	"github.com/fprl/simple-vps/internal/utils"
)

const (
	ManifestFile       = "ship.toml"
	RemoteDeployTmpDir = "/tmp/simple-vps-deploy"
)

type CommandRunner struct {
	SshOptions       []string
	RsyncRemoteShell string
	TempDir          string
}

func NewCommandRunner() (*CommandRunner, error) {
	sshOpts := []string{"-o", "BatchMode=yes"}
	key := os.Getenv("SHIP_SSH_KEY")
	if key == "" {
		if defaultKey, ok := defaultDeployKeyPath(); ok {
			sshOpts = append(sshOpts,
				"-i", defaultKey,
				"-o", "IdentitiesOnly=yes",
			)
		}
		return &CommandRunner{
			SshOptions:       sshOpts,
			RsyncRemoteShell: sshRemoteShell(sshOpts),
		}, nil
	}
	dir, err := os.MkdirTemp("", "ship-ssh-")
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "id")

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

	sshOpts = append(sshOpts,
		"-i", keyPath,
		"-o", "IdentitiesOnly=yes",
	)

	return &CommandRunner{
		SshOptions:       sshOpts,
		RsyncRemoteShell: sshRemoteShell(sshOpts),
		TempDir:          dir,
	}, nil
}

func defaultDeployKeyPath() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	path := filepath.Join(home, ".ssh", "ship-deploy")
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
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
// stdout/stderr/exit. Used by `ship secret set` so the secret
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

func runSSHDetail(runner sshRunner, server string, command string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		detail = strings.TrimPrefix(detail, "Error: ")
		if detail == "" {
			detail = "remote command failed"
		}
		return "", fmt.Errorf("%s", detail)
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
	parts := []string{"sudo", "-n", "/usr/local/bin/ship", "server"}
	for _, arg := range args {
		parts = append(parts, utils.ShellEscape(arg))
	}
	return strings.Join(parts, " ")
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

func CmdSSHCurrent(root string) {
	read, err := currentReadContext(root, "ssh")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer read.Runner.Close()

	err = read.Runner.RunSSHPassthrough(read.AppContext.Server, "")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
}

func resolveDeployPreviewEnv(runner sshRunner, ctx *config.AppContext, address deployAddress) (string, error) {
	if address.PreviewBranch == "" {
		return address.EnvName, nil
	}
	out, err := runSSHDetail(runner, ctx.Server, serverAppPreviewResolveOrCreateCommand(ctx.AppName, address.PreviewBranch))
	if err != nil {
		return "", err
	}
	env := strings.TrimSpace(out)
	if !names.EnvRe.MatchString(env) {
		return "", fmt.Errorf("preview resolver returned invalid env name: %q", env)
	}
	return env, nil
}

func resolveReadPreviewEnv(runner sshRunner, ctx *config.AppContext, address readAddress) (string, error) {
	if address.PreviewBranch == "" {
		return address.EnvName, nil
	}
	out, err := runSSHDetail(runner, ctx.Server, serverAppPreviewResolveCommand(ctx.AppName, address.PreviewBranch))
	if err != nil {
		return "", err
	}
	env := strings.TrimSpace(out)
	if !names.EnvRe.MatchString(env) {
		return "", fmt.Errorf("preview resolver returned invalid env name: %q", env)
	}
	return env, nil
}

type readContext struct {
	AppContext *config.AppContext
	Address    readAddress
	EnvName    string
	Runner     *CommandRunner
}

func BoxTarget(root string) (string, error) {
	ctx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		return "", err
	}
	return ctx.Server, nil
}

func currentReadContext(root, command string) (readContext, error) {
	address, err := resolveReadAddress(root, "", "", command)
	if err != nil {
		return readContext{}, err
	}
	baseEnv := address.EnvName
	if address.PreviewBranch != "" {
		baseEnv = productionEnvName
	}
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

type appListJSON struct {
	Apps []appListEnvJSON `json:"apps"`
}

type appListEnvJSON struct {
	App       string             `json:"app"`
	Env       string             `json:"env"`
	Preview   *previewStatusJSON `json:"preview,omitempty"`
	Processes []processJSON      `json:"processes"`
	Static    *staticJSON        `json:"static,omitempty"`
}

type previewStatusJSON struct {
	Branch     string `json:"branch"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	Pinned     bool   `json:"pinned"`
	LastShipAt string `json:"last_ship_at,omitempty"`
}

type processJSON struct {
	Process    string `json:"process"`
	Container  string `json:"container"`
	State      string `json:"state"`
	Image      string `json:"image,omitempty"`
	Release    string `json:"release,omitempty"`
	Dirty      bool   `json:"dirty,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	Status     string `json:"status,omitempty"`
}

type staticJSON struct {
	Release    string   `json:"release"`
	Routes     []string `json:"routes"`
	Dirty      bool     `json:"dirty,omitempty"`
	BaseCommit string   `json:"base_commit,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
}

type statusPayload struct {
	App  string          `json:"app"`
	Envs []statusEnvJSON `json:"envs"`
}

type statusEnvJSON struct {
	Kind       string        `json:"kind"`
	Branch     string        `json:"branch"`
	URL        string        `json:"url"`
	Env        string        `json:"env"`
	Release    string        `json:"release,omitempty"`
	Health     string        `json:"health"`
	AgeSeconds int64         `json:"ageSeconds,omitempty"`
	ExpiresAt  string        `json:"expiresAt,omitempty"`
	Pinned     bool          `json:"pinned,omitempty"`
	Dirty      bool          `json:"dirty,omitempty"`
	Processes  []processJSON `json:"processes"`
}

func CmdStatus(root string, jsonFlag bool) {
	ctx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppListCommand(true), "status failed")
	payload, err := statusFromAppList(ctx, out)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if jsonFlag {
		buf, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		fmt.Println(string(buf))
		return
	}
	fmt.Print(renderStatusSummary(payload))
}

func statusFromAppList(ctx *config.AppContext, raw string) (statusPayload, error) {
	var list appListJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &list); err != nil {
		return statusPayload{}, fmt.Errorf("status failed: invalid app list JSON: %v", err)
	}
	payload := statusPayload{App: ctx.AppName}
	for _, item := range list.Apps {
		if item.App != ctx.AppName {
			continue
		}
		payload.Envs = append(payload.Envs, statusEnvFromAppListItem(ctx, item))
	}
	sort.Slice(payload.Envs, func(i, j int) bool {
		if payload.Envs[i].Kind != payload.Envs[j].Kind {
			return payload.Envs[i].Kind == "Production"
		}
		return payload.Envs[i].Branch < payload.Envs[j].Branch
	})
	return payload, nil
}

func statusEnvFromAppListItem(ctx *config.AppContext, item appListEnvJSON) statusEnvJSON {
	kind := "Preview"
	branch := item.Env
	if item.Env == productionEnvName {
		kind = "Production"
		branch = ctx.ProductionBranch
	}
	expiresAt := ""
	pinned := false
	if item.Preview != nil {
		branch = item.Preview.Branch
		expiresAt = item.Preview.ExpiresAt
		pinned = item.Preview.Pinned
	}
	release, dirty, createdAt := appListActiveRelease(item)
	return statusEnvJSON{
		Kind:       kind,
		Branch:     branch,
		URL:        deploymentURL(ctx, item.Env),
		Env:        item.Env,
		Release:    release,
		Health:     appListHealth(item),
		AgeSeconds: ageSeconds(createdAt),
		ExpiresAt:  expiresAt,
		Pinned:     pinned,
		Dirty:      dirty,
		Processes:  item.Processes,
	}
}

func appListActiveRelease(item appListEnvJSON) (string, bool, string) {
	if item.Static != nil && item.Static.Release != "" {
		return item.Static.Release, item.Static.Dirty, item.Static.CreatedAt
	}
	for _, proc := range item.Processes {
		if proc.Release != "" {
			return proc.Release, proc.Dirty, proc.CreatedAt
		}
	}
	return "", false, ""
}

func appListHealth(item appListEnvJSON) string {
	if len(item.Processes) == 0 {
		if item.Static != nil {
			return "healthy"
		}
		return "stopped"
	}
	for _, proc := range item.Processes {
		if proc.State != "running" {
			return "degraded"
		}
	}
	return "healthy"
}

func ageSeconds(createdAt string) int64 {
	if createdAt == "" {
		return 0
	}
	t, err := time.Parse(timeRFC3339UTC, createdAt)
	if err != nil {
		return 0
	}
	return int64(time.Since(t).Seconds())
}

func renderStatusSummary(payload statusPayload) string {
	if len(payload.Envs) == 0 {
		return fmt.Sprintf("No live envs for %s\n", payload.App)
	}
	var b strings.Builder
	for _, env := range payload.Envs {
		release := env.Release
		if release == "" {
			release = "-"
		}
		if env.Dirty {
			release += " (dirty)"
		}
		lifecycle := ""
		switch {
		case env.Kind == "Preview" && env.Pinned:
			lifecycle = " pinned"
		case env.Kind == "Preview" && env.ExpiresAt != "":
			lifecycle = " expires=" + env.ExpiresAt
		}
		fmt.Fprintf(&b, "%s %s  %s  release=%s  health=%s%s\n", env.Kind, env.Branch, env.URL, release, env.Health, lifecycle)
	}
	return b.String()
}

func CmdBoxLs(server string, jsonFlag bool) {
	if server == "" {
		utils.Die("box target is required", 1)
	}
	if !config.ValidateSshTarget(server) {
		utils.Die("box target must be an SSH target like deploy@example.com", 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppListCommand(jsonFlag), "app list failed")
	fmt.Print(out)
}

func CmdRollback(root string, release string) {
	read, err := currentReadContext(root, "rollback")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer read.Runner.Close()

	out := runSSHChecked(read.Runner, read.AppContext.Server, serverAppRollbackCommand(read.AppContext.AppName, read.EnvName, release), "rollback failed")
	fmt.Print(rewriteRollbackSummary(out, read))
}

func rewriteRollbackSummary(out string, read readContext) string {
	kind, branch := readSurface(read)
	prefix := fmt.Sprintf("Rolled back %s (%s) ", read.AppContext.AppName, read.EnvName)
	replacement := fmt.Sprintf("Rolled back %s %s ", kind, branch)
	return strings.Replace(out, prefix, replacement, 1)
}

func CmdSave(root string, dest string) {
	read, err := currentReadContext(root, "save")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer read.Runner.Close()

	out := runSSHChecked(read.Runner, read.AppContext.Server, serverAppBackupCommand(read.AppContext.AppName, read.EnvName, dest, false), "save failed")
	fmt.Print(out)
}

func CmdRestore(root string, from string) {
	read, err := currentReadContext(root, "restore")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer read.Runner.Close()

	runSSHChecked(read.Runner, read.AppContext.Server, serverAppSetupEnvCommand(read.AppContext.AppName, read.EnvName), "restore setup failed")
	out := runSSHChecked(read.Runner, read.AppContext.Server, serverAppRestoreCommand(read.AppContext.AppName, read.EnvName, from, false), "restore failed")
	fmt.Print(rewriteRestoreSummary(out, read))
}

func rewriteRestoreSummary(out string, read readContext) string {
	kind, branch := readSurface(read)
	prefix := fmt.Sprintf("Restored %s (%s) ", read.AppContext.AppName, read.EnvName)
	replacement := fmt.Sprintf("Restored %s %s ", kind, branch)
	return strings.Replace(out, prefix, replacement, 1)
}

func CmdRm(root string, branch string, confirm string) {
	address, err := resolveReadAddress(root, "", branch, "rm")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	baseCtx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
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
			utils.Die(err.Error(), 1)
		}
	} else if confirm != baseCtx.AppName {
		utils.Die(fmt.Sprintf("Production rm requires --confirm %s", baseCtx.AppName), 1)
	}

	if _, err := runSSHRequired(runner, baseCtx.Server, serverAppDestroyEnvCommand(baseCtx.AppName, envName, true), "rm failed"); err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Printf("Removed %s %s\n", kind, displayBranch)
}

func CmdLogs(root string, process string, follow bool, tail int, jsonFlag bool) {
	if follow && jsonFlag {
		utils.Die("logs --json cannot be combined with --follow", 2)
	}
	read, err := currentReadContext(root, "logs")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer read.Runner.Close()

	// Follow mode needs interactive stdout/stderr passthrough so the
	// user sees the stream as it arrives. Non-follow mode reads a
	// bounded amount and prints once.
	cmdStr := serverAppLogsCommand(read.AppContext.AppName, read.EnvName, process, follow, tail)
	if follow {
		if err := read.Runner.RunSSHPassthrough(read.AppContext.Server, cmdStr); err != nil {
			utils.Die(err.Error(), 1)
		}
		return
	}
	out := runSSHChecked(read.Runner, read.AppContext.Server, cmdStr, "logs failed")
	if !jsonFlag {
		fmt.Print(out)
		return
	}
	lines := splitLogLines(out)
	payload := struct {
		App     string   `json:"app"`
		Env     string   `json:"env"`
		Process string   `json:"process,omitempty"`
		Lines   []string `json:"lines"`
	}{
		App:     read.AppContext.AppName,
		Env:     read.EnvName,
		Process: process,
		Lines:   lines,
	}
	buf, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Println(string(buf))
}

func splitLogLines(out string) []string {
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
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

func CmdSecretSet(root string, key string) {
	read, err := currentReadContext(root, "secret set")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer read.Runner.Close()
	if err := envKeyValid(key); err != nil {
		utils.Die(err.Error(), 1)
	}
	value, err := secretValueFromStdin()
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	// Pipe the value over the helper's stdin — never argv, never a
	// file on disk between hops. The helper writes it straight to
	// /etc/simple-vps/secrets/<app>/<env>/<key>.
	stdout, stderr, code, err := read.Runner.RunSSHWithStdin(read.AppContext.Server, serverAppSecretSetCommand(read.AppContext.AppName, read.EnvName, key), value)
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
	kind, branch := readSurface(read)
	fmt.Printf("Stored secret %s for %s %s.\n", key, kind, branch)
	fmt.Fprintln(os.Stderr, "next: ship")
}

func CmdSecretList(root string, jsonFlag bool) {
	read, err := currentReadContext(root, "secret ls")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer read.Runner.Close()

	out := runSSHChecked(read.Runner, read.AppContext.Server, serverAppSecretListCommand(read.AppContext.AppName, read.EnvName, jsonFlag), "secret list failed")
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

func CmdSecretRm(root string, key string) {
	read, err := currentReadContext(root, "secret rm")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer read.Runner.Close()
	if err := envKeyValid(key); err != nil {
		utils.Die(err.Error(), 1)
	}

	out := runSSHChecked(read.Runner, read.AppContext.Server, serverAppSecretRmCommand(read.AppContext.AppName, read.EnvName, key), "secret rm failed")
	kind, branch := readSurface(read)
	if strings.Contains(out, "was not set") {
		fmt.Printf("Secret %s was not set for %s %s.\n", key, kind, branch)
		return
	}
	fmt.Printf("Removed secret %s for %s %s.\n", key, kind, branch)
}

func readSurface(read readContext) (string, string) {
	if read.Address.ProductionBranch {
		return "Production", read.AppContext.ProductionBranch
	}
	return "Preview", read.Address.PreviewBranch
}

func CmdPreviewPin(root string, branch string, pinned bool) {
	ctx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if branch == ctx.ProductionBranch {
		utils.Die(codedNextError(
			"production_branch_not_preview",
			fmt.Sprintf("branch %q maps to production", branch),
			"choose a preview branch",
		).Error(), 1)
	}
	previewBranch := sanitizeBranchEnvName(branch)
	if previewBranch == "" {
		utils.Die(codedNextError(
			"unmappable_branch_name",
			fmt.Sprintf("branch %q does not produce a valid environment name", branch),
			"rename the branch",
		).Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	command := serverAppPreviewPinCommand(ctx.AppName, branch)
	if !pinned {
		command = serverAppPreviewUnpinCommand(ctx.AppName, branch)
	}
	out, err := runSSHDetail(runner, ctx.Server, command)
	if err != nil {
		utils.Die(err.Error(), 1)
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
		return fmt.Errorf("invalid secret key %q: must match ^[A-Za-z_][A-Za-z0-9_]*$", key)
	}
	return nil
}

func CmdBoxDoctor(server string, jsonFlag bool) {
	if server == "" {
		utils.Die("box target is required", 1)
	}
	if !config.ValidateSshTarget(server) {
		utils.Die("box target must be an SSH target like deploy@example.com", 1)
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

type ShipResult struct {
	URL        string   `json:"url"`
	Env        string   `json:"env"`
	Release    string   `json:"release"`
	Processes  []string `json:"processes"`
	DurationMs int64    `json:"durationMs"`
}

type shipProgress struct {
	last time.Time
}

func newShipProgress() shipProgress {
	return shipProgress{last: time.Now()}
}

func (p *shipProgress) timed(name string) {
	now := time.Now()
	fmt.Fprintf(os.Stderr, "%s %s\n", name, formatPhaseDuration(now.Sub(p.last)))
	p.last = now
}

func (p *shipProgress) line(line string) {
	fmt.Fprintln(os.Stderr, line)
	p.last = time.Now()
}

func formatPhaseDuration(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func CmdShip(root string, branchName string, jsonFlag bool, rebuild bool, includeDotenv bool) {
	start := time.Now()
	progress := newShipProgress()
	result, err := runShip(root, branchName, rebuild, includeDotenv, &progress)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	result.DurationMs = time.Since(start).Milliseconds()
	writeShipResult(result, jsonFlag)
}

func writeShipResult(result ShipResult, jsonFlag bool) {
	if jsonFlag {
		buf, err := json.Marshal(result)
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		fmt.Println(string(buf))
		return
	}
	fmt.Println(result.URL)
}

func runShip(root string, branchName string, rebuild bool, includeDotenv bool, progress *shipProgress) (ShipResult, error) {
	address, err := resolveDeployAddress(root, "", branchName)
	if err != nil {
		return ShipResult{}, err
	}
	if address.ProductionBranch && address.Dirty {
		return ShipResult{}, codedNextError(
			"dirty_worktree",
			fmt.Sprintf("production branch %q has uncommitted changes", address.Branch),
			"git commit",
		)
	}
	baseEnv := address.EnvName
	if address.PreviewBranch != "" {
		baseEnv = productionEnvName
	}
	ctx, err := config.LoadAppContext(root, baseEnv)
	if err != nil {
		return ShipResult{}, err
	}

	runner, err := NewCommandRunner()
	if err != nil {
		return ShipResult{}, err
	}
	defer runner.Close()
	if address.ProductionBranch {
		if err := deployHostPreflight(runner, ctx); err != nil {
			return ShipResult{}, err
		}
	}
	resolvedEnv, err := resolveDeployPreviewEnv(runner, ctx, address)
	if err != nil {
		return ShipResult{}, err
	}
	address.EnvName = resolvedEnv
	envName := address.EnvName
	progress.timed("preflight")

	plan, diags, err := buildLocalDeployPlan(root, envName, localDeployOptions{
		AllowDirty:    !address.ProductionBranch,
		IncludeDotenv: includeDotenv,
	})
	if err != nil {
		return ShipResult{}, err
	}
	diags.printTo(os.Stderr)
	if diags.hasErrors() {
		return ShipResult{}, fmt.Errorf("deploy blocked by local checks")
	}
	ctx = plan.Context
	if address.ProductionBranch {
		if err := enforceProductionAncestry(root, runner, ctx, plan.BaseCommit); err != nil {
			return ShipResult{}, err
		}
	}
	if err := ensureRemoteEnvReadyForDeploy(runner, ctx); err != nil {
		return ShipResult{}, err
	}

	// 1. Tar source locally (git archive for clean tree, working tree for --dirty).
	tarDir, err := os.MkdirTemp("", "ship-deploy-")
	if err != nil {
		return ShipResult{}, err
	}
	defer os.RemoveAll(tarDir)

	localTar := filepath.Join(tarDir, "source.tar")
	localManifest := filepath.Join(tarDir, "ship.toml")
	if err := writeSourceTar(root, localTar, plan.Dirty, plan.ServeDirs); err != nil {
		return ShipResult{}, err
	}
	if err := copyFile(filepath.Join(root, ManifestFile), localManifest); err != nil {
		return ShipResult{}, fmt.Errorf("copy manifest: %v", err)
	}

	// 2. Upload tarball + manifest to a per-deploy temp dir on the host.
	remoteDir := fmt.Sprintf("%s/%s-%s-%s", RemoteDeployTmpDir, ctx.AppName, envName, plan.Release)
	cleanupRemoteDir := func() {
		_, _, _, _ = runner.RunSSH(ctx.Server, fmt.Sprintf("rm -rf %s", utils.ShellEscape(remoteDir)))
	}
	failAfterRemoteDir := func(message string) (ShipResult, error) {
		cleanupRemoteDir()
		return ShipResult{}, fmt.Errorf("%s", message)
	}
	if _, err := runSSHRequired(runner, ctx.Server, fmt.Sprintf("mkdir -p %s && chmod 0700 %s", utils.ShellEscape(remoteDir), utils.ShellEscape(remoteDir)), "failed to create remote deploy dir"); err != nil {
		return failAfterRemoteDir(err.Error())
	}
	if err := runner.Upload(localTar, remoteDir+"/source.tar", ctx.Server); err != nil {
		return failAfterRemoteDir(fmt.Sprintf("failed to upload source: %v", err))
	}
	if err := runner.Upload(localManifest, remoteDir+"/ship.toml", ctx.Server); err != nil {
		return failAfterRemoteDir(fmt.Sprintf("failed to upload manifest: %v", err))
	}
	progress.timed("build")

	// 3. Helper builds the image or snapshots static assets, then reloads Caddy.
	applyCmd := serverAppApplyCommand(ctx.AppName, envName,
		remoteDir+"/source.tar",
		remoteDir+"/ship.toml",
		plan,
		rebuild,
	)
	if _, err := runSSHRequired(runner, ctx.Server, applyCmd, "deploy failed"); err != nil {
		return failAfterRemoteDir(err.Error())
	}
	progress.timed("release")
	progress.line("probe ok")

	// 4. Best-effort cleanup of the upload dir.
	cleanupRemoteDir()
	progress.line("live")

	return ShipResult{
		URL:       deploymentURL(ctx, envName),
		Env:       envName,
		Release:   plan.Release,
		Processes: processNames(plan.Context.Processes),
	}, nil
}

func processNames(processes map[string]config.Process) []string {
	out := make([]string, 0, len(processes))
	for name := range processes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func deploymentURL(ctx *config.AppContext, envName string) string {
	if url := routedDeploymentURL(ctx); url != "" {
		return url
	}
	return fmt.Sprintf("ship://%s/%s/%s", boxHost(ctx.Server), ctx.AppName, envName)
}

type routeCandidate struct {
	rank int
	url  string
}

func routedDeploymentURL(ctx *config.AppContext) string {
	var candidates []routeCandidate
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
		candidates = append(candidates, routeCandidate{
			rank: rank,
			url:  "https://" + route.Host + route.Path,
		})
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

func boxHost(target string) string {
	if _, host, ok := strings.Cut(target, "@"); ok {
		return host
	}
	if target == "" {
		return "box"
	}
	return target
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

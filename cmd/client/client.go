package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
	key := os.Getenv("SIMPLE_VPS_SSH_KEY")
	if key == "" {
		return &CommandRunner{}, nil
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

	sshOpts := []string{
		"-i", keyPath,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHostsPath,
	}

	var escOpts []string
	for _, opt := range sshOpts {
		escOpts = append(escOpts, utils.ShellEscape(opt))
	}
	rsyncShell := "ssh " + strings.Join(escOpts, " ")

	return &CommandRunner{
		SshOptions:       sshOpts,
		RsyncRemoteShell: rsyncShell,
		TempDir:          dir,
	}, nil
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
// stdout/stderr/exit. Used by `simple-vps secret put` so the secret
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

func runSSHChecked(runner *CommandRunner, server string, command string, errMsg string) string {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail != "" {
			utils.Die(fmt.Sprintf("%s: %s", errMsg, detail), 1)
		}
		utils.Die(errMsg, 1)
	}
	return stdout
}

func serverCommand(args ...string) string {
	parts := []string{"sudo", "simple-vps", "server"}
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

func serverAppApplyCommand(appName string, envName string, tarballPath string, manifestPath string, sha string, rebuild bool) string {
	args := []string{"app", "apply"}
	if rebuild {
		args = append(args, "--rebuild")
	}
	args = append(args,
		"--tarball", tarballPath,
		"--manifest", manifestPath,
		"--sha", sha,
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

func serverAppLogsCommand(appName, envName, service string, follow bool, tail int) string {
	args := []string{"app", "logs"}
	if follow {
		args = append(args, "--follow")
	}
	if tail > 0 && !follow {
		args = append(args, fmt.Sprintf("--tail=%d", tail))
	}
	args = append(args, appName, envName)
	if service != "" {
		args = append(args, service)
	}
	return serverCommand(args...)
}

func serverAppRestartCommand(appName, envName, service string, jsonFlag bool) string {
	args := []string{"app", "restart"}
	if jsonFlag {
		args = append(args, "--json")
	}
	args = append(args, appName, envName)
	if service != "" {
		args = append(args, service)
	}
	return serverCommand(args...)
}

func serverAppRollbackCommand(appName, envName, release string, jsonFlag bool) string {
	args := []string{"app", "rollback"}
	if jsonFlag {
		args = append(args, "--json")
	}
	args = append(args, appName, envName)
	if release != "" {
		args = append(args, release)
	}
	return serverCommand(args...)
}

func serverAppBackupCommand(appName, envName, dest string) string {
	args := []string{"app", "backup"}
	if dest != "" {
		args = append(args, "--to", dest)
	}
	args = append(args, appName, envName)
	return serverCommand(args...)
}

func serverAppBackupListCommand(appName, envName string, jsonFlag bool) string {
	args := []string{"app", "backup"}
	if jsonFlag {
		args = append(args, "--json")
	}
	args = append(args, "list", appName, envName)
	return serverCommand(args...)
}

func serverAppBackupRmCommand(appName, envName, id string) string {
	return serverCommand("app", "backup", "rm", appName, envName, id)
}

func serverAppRestoreCommand(appName, envName, from string, dryRun bool) string {
	args := []string{"app", "backup", "--from", from}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "restore", appName, envName)
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

func serverAppSecretPutCommand(appName, envName, key string) string {
	return serverCommand("app", "secret", "put", appName, envName, key)
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

func parseServerFlag(args []string) (string, []string, error) {
	flags, err := parseHostFlags(args)
	if err != nil {
		return "", nil, err
	}
	return flags.server, flags.rest, nil
}

type hostFlags struct {
	server string
	json   bool
	rest   []string
}

func parseHostFlags(args []string) (hostFlags, error) {
	flags := hostFlags{}
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--json":
			flags.json = true
		case "--server":
			if i+1 >= len(args) {
				return hostFlags{}, errors.New("--server requires a value")
			}
			flags.server = args[i+1]
			if !config.ValidateSshTarget(flags.server) {
				return hostFlags{}, errors.New("--server must be an SSH target like deploy@example.com")
			}
			i++
		default:
			rest = append(rest, arg)
		}
	}
	flags.rest = rest
	return flags, nil
}

func readTargetServer(root string, explicitServer string) (string, error) {
	if explicitServer != "" {
		return explicitServer, nil
	}
	manifest, err := config.ReadManifest(root)
	if err != nil {
		return "", err
	}
	checkErrors, _, err := config.CheckManifest(root, "")
	if err != nil {
		return "", err
	}
	if len(checkErrors) > 0 {
		return "", fmt.Errorf("%s", strings.Join(checkErrors, "\n"))
	}
	if len(manifest.Env) != 1 {
		return "", errors.New("command requires exactly one env in simple-vps.toml")
	}
	for _, env := range manifest.Env {
		return env.Server, nil
	}
	return "", errors.New("at least one [env.<name>] block is required")
}

// CmdInit scaffolds a default Dockerfile + container-shaped manifest.
func CmdInit(root string) {
	manifestPath := filepath.Join(root, ManifestFile)
	if _, err := os.Stat(manifestPath); err == nil {
		utils.Die("simple-vps.toml already exists", 1)
	}

	name := defaultAppName(root)
	packageJsonPath := filepath.Join(root, "package.json")
	if data, err := os.ReadFile(packageJsonPath); err == nil {
		var pkg struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(data, &pkg)
		if pkg.Name != "" {
			name = normalizeAppName(pkg.Name)
		}
	}

	dockerfilePath := filepath.Join(root, "Dockerfile")
	createdDockerfile := false
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		dockerfileBody := `# Edit to fit your app. The Dockerfile is the build contract;
# language runtimes live in the image, not on the host.
FROM oven/bun:1
WORKDIR /app
COPY package.json bun.lock* ./
RUN bun install --frozen-lockfile --production
COPY . .
EXPOSE 3000
CMD ["bun", "run", "src/server.ts"]
`
		if err := os.WriteFile(dockerfilePath, []byte(dockerfileBody), 0644); err != nil {
			utils.Die(err.Error(), 1)
		}
		createdDockerfile = true
	}

	content := fmt.Sprintf(`name = "%s"

[env.production]
server = "deploy@100.x.y.z"

[services.web]
port = 3000
healthcheck = "/health"

[routes.app]
host = "app.example.com"
type = "proxy"
service = "web"
`, name)

	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Printf("Created %s\n", ManifestFile)
	if createdDockerfile {
		fmt.Println("Created Dockerfile")
	}
	fmt.Println("Next:")
	fmt.Printf("1. edit %s and Dockerfile\n", ManifestFile)
	fmt.Println("2. simple-vps setup production")
	fmt.Println("3. simple-vps deploy production")
}

func defaultAppName(root string) string {
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	return normalizeAppName(filepath.Base(root))
}

func normalizeAppName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if idx := strings.LastIndex(value, "/"); idx >= 0 {
		value = value[idx+1:]
	}

	var b strings.Builder
	prevDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}

	candidate := strings.Trim(b.String(), "-")
	if candidate == "" {
		candidate = "app"
	}
	if candidate[0] < 'a' || candidate[0] > 'z' {
		candidate = "app-" + candidate
	}
	if len(candidate) > 41 {
		candidate = strings.Trim(candidate[:41], "-")
	}
	if len(candidate) < 2 {
		candidate += "p"
	}
	if !names.AppRe.MatchString(candidate) {
		return "app"
	}
	return candidate
}

func CmdCheck(root string, envName string) {
	errors, warnings, err := config.CheckManifest(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	for _, w := range warnings {
		fmt.Printf("Warning: %s\n", w)
	}
	if len(errors) > 0 {
		for _, e := range errors {
			fmt.Printf("Error: %s\n", e)
		}
		os.Exit(1)
	}
	if envName != "" {
		fmt.Printf("Manifest simple-vps.toml is valid for env %s.\n", envName)
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
	tools := []string{"simple-vps", "rsync"}
	for _, tool := range tools {
		errMsg := fmt.Sprintf("missing required server tool: %s", tool)
		if tool == "simple-vps" {
			errMsg = "missing Simple VPS server API; rerun the Simple VPS install"
		}
		runSSHChecked(runner, ctx.Server, fmt.Sprintf("command -v %s", utils.ShellEscape(tool)), errMsg)
	}

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

func CmdAppList(root string, explicitServer string, jsonFlag bool) {
	server, err := readTargetServer(root, explicitServer)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppListCommand(jsonFlag), "app list failed")
	fmt.Print(out)
}

func CmdRestart(root string, envName string, service string, jsonFlag bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	// Restart shares status's plumbing: helper does the work, prints
	// the summary, we pipe its output through unchanged so `--json`
	// is pipeable and the text mode keeps its line breaks.
	out := runSSHChecked(runner, ctx.Server, serverAppRestartCommand(ctx.AppName, envName, service, jsonFlag), "restart failed")
	fmt.Print(out)
}

func CmdRollback(root string, envName string, release string, jsonFlag bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppRollbackCommand(ctx.AppName, envName, release, jsonFlag), "rollback failed")
	fmt.Print(out)
}

func CmdBackup(root string, envName string, dest string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppBackupCommand(ctx.AppName, envName, dest), "backup failed")
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

func CmdDestroy(root string, envName string, confirm string, yes bool, purge bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := validateDestroyConfirmation(ctx.AppName, confirm, yes); err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppDestroyEnvCommand(ctx.AppName, envName, purge), "destroy failed")
	fmt.Print(out)
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

func CmdLogs(root string, envName string, service string, follow bool, tail int) {
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
	cmdStr := serverAppLogsCommand(ctx.AppName, envName, service, follow, tail)
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

func CmdSecretPut(root string, envName string, key string) {
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
	stdout, stderr, code, err := runner.RunSSHWithStdin(ctx.Server, serverAppSecretPutCommand(ctx.AppName, envName, key), value)
	if err != nil || code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail == "" {
			detail = "no error detail"
		}
		utils.Die(fmt.Sprintf("secret put failed: %s", detail), 1)
	}
	// Don't echo stdout — it'd carry the helper's confirmation
	// (which already names the key but not the value). Print our own.
	fmt.Printf("Stored secret %s for %s (%s). Run `simple-vps deploy %s` to apply.\n", key, ctx.AppName, envName, envName)
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

func CmdHost(args []string) {
	sub := "status"

	flags, err := parseHostFlags(args)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	rest := flags.rest
	if len(rest) > 0 {
		sub = rest[0]
	}

	if len(rest) > 1 || (sub != "status" && sub != "doctor") {
		utils.Die("host requires subcommand: status, doctor", 1)
	}

	server, err := readTargetServer(".", flags.server)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	if sub == "status" {
		out := runSSHChecked(runner, server, serverStatusCommand(flags.json), "failed to read server status")
		fmt.Print(out)
	} else {
		stdout, stderr, code, err := runner.RunSSH(server, serverDoctorCommand(flags.json))
		if err != nil || code != 0 {
			if flags.json && json.Valid([]byte(stdout)) {
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
}

func CmdDeploy(root string, envName string, dirty bool, rebuild bool, includeDotenv bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	if ctx.Shape != config.ShapeContainer {
		utils.Die(fmt.Sprintf("deploy supports container apps only (got shape %q)", ctx.Shape), 1)
	}

	shaOut, _, code, _ := runCommand("git", []string{"rev-parse", "--short=12", "HEAD"}, root)
	if code != 0 {
		utils.Die("git rev-parse failed", 1)
	}
	release := strings.TrimSpace(shaOut)
	if release == "" {
		utils.Die("git rev-parse returned an empty release", 1)
	}

	statusOut, _, code, _ := runCommand("git", []string{"status", "--porcelain"}, root)
	if code != 0 {
		utils.Die("git status failed", 1)
	}
	worktreeDirty := strings.TrimSpace(statusOut) != ""
	if worktreeDirty && !dirty {
		utils.Die("working tree is dirty; commit changes or pass --dirty", 1)
	}
	if worktreeDirty {
		release = fmt.Sprintf("dirty-%s", time.Now().UTC().Format("20060102150405"))
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	// 1. Tar source locally (git archive for clean tree, working tree for --dirty).
	tarDir, err := os.MkdirTemp("", "simple-vps-deploy-")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer os.RemoveAll(tarDir)

	localTar := filepath.Join(tarDir, "source.tar")
	localManifest := filepath.Join(tarDir, "simple-vps.toml")
	if err := writeSourceTar(root, localTar, worktreeDirty); err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := copyFile(filepath.Join(root, ManifestFile), localManifest); err != nil {
		utils.Die(fmt.Sprintf("copy manifest: %v", err), 1)
	}

	if !includeDotenv {
		// Quick dotenv check against the working tree — the same files
		// would otherwise end up in the tarball.
		if err := validateArtifactDotenv(root); err != nil {
			utils.Die(err.Error(), 1)
		}
	}

	// 2. Upload tarball + manifest to a per-deploy temp dir on the host.
	remoteDir := fmt.Sprintf("%s/%s-%s-%s", RemoteDeployTmpDir, ctx.AppName, envName, release)
	runSSHChecked(runner, ctx.Server, fmt.Sprintf("mkdir -p %s && chmod 0700 %s", utils.ShellEscape(remoteDir), utils.ShellEscape(remoteDir)), "failed to create remote deploy dir")
	if err := runner.Upload(localTar, remoteDir+"/source.tar", ctx.Server); err != nil {
		utils.Die(fmt.Sprintf("failed to upload source: %v", err), 1)
	}
	if err := runner.Upload(localManifest, remoteDir+"/simple-vps.toml", ctx.Server); err != nil {
		utils.Die(fmt.Sprintf("failed to upload manifest: %v", err), 1)
	}

	// 3. Helper builds the image, runs services, reloads Caddy.
	applyCmd := serverAppApplyCommand(ctx.AppName, envName,
		remoteDir+"/source.tar",
		remoteDir+"/simple-vps.toml",
		release,
		rebuild,
	)
	runSSHChecked(runner, ctx.Server, applyCmd, "deploy failed")

	// 4. Best-effort cleanup of the upload dir.
	_, _, _, _ = runner.RunSSH(ctx.Server, fmt.Sprintf("rm -rf %s", utils.ShellEscape(remoteDir)))

	fmt.Printf("Deployed %s (%s) at %s\n", ctx.AppName, envName, release)
}

func writeSourceTar(root string, dest string, dirty bool) error {
	var cmd *exec.Cmd
	if dirty {
		cmd = exec.Command("sh", "-c", fmt.Sprintf(
			"tar -C %s --exclude .git --exclude node_modules -cf %s .",
			utils.ShellEscape(root), utils.ShellEscape(dest),
		))
	} else {
		cmd = exec.Command("sh", "-c", fmt.Sprintf(
			"git -C %s archive --format=tar -o %s HEAD",
			utils.ShellEscape(root), utils.ShellEscape(dest),
		))
	}
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

func validateArtifactDotenv(artifactDir string) error {
	allowed := map[string]bool{
		".env.example":  true,
		".env.sample":   true,
		".env.defaults": true,
	}
	var dotenvs []string
	err := filepath.Walk(artifactDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		name := filepath.Base(path)
		if strings.HasPrefix(name, ".env") && !allowed[name] {
			rel, relErr := filepath.Rel(artifactDir, path)
			if relErr != nil {
				return relErr
			}
			dotenvs = append(dotenvs, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(dotenvs) > 0 {
		return fmt.Errorf("refusing to deploy dotenv file: %s; use --include-dotenv to bypass", strings.Join(dotenvs, ", "))
	}
	return nil
}

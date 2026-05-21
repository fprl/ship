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
	"strconv"
	"strings"
	"time"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/utils"
)

const (
	ManifestFile         = "simple-vps.toml"
	RemoteDeployTmpDir   = "/tmp/simple-vps-deploy"
	ReleaseSuccessMarker = ".simple-vps-release-success"
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

func (r *CommandRunner) UploadDirectory(localDir string, remoteDir string, server string) error {
	var args []string
	if r.RsyncRemoteShell != "" {
		args = append(args, "-e", r.RsyncRemoteShell)
	}
	args = append(args, "-az", "--delete", localDir+"/", fmt.Sprintf("%s:%s", server, remoteDir+"/"))
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

func packageManagerForLockfile(lockfile string) string {
	switch lockfile {
	case "bun.lock", "bun.lockb":
		return "bun"
	case "pnpm-lock.yaml":
		return "pnpm"
	case "yarn.lock":
		return "yarn"
	case "package-lock.json":
		return "npm"
	}
	return "bun"
}

func runtimeForLockfile(lockfile string) string {
	if lockfile == "" {
		return "bun"
	}
	if lockfile == "bun.lock" || lockfile == "bun.lockb" {
		return "bun"
	}
	return "node"
}

func installCommandFor(lockfile string) string {
	switch lockfile {
	case "bun.lock", "bun.lockb":
		return "bun install --production --frozen-lockfile"
	case "pnpm-lock.yaml":
		return "pnpm install --prod --frozen-lockfile"
	case "package-lock.json":
		return "npm ci --omit=dev"
	case "yarn.lock":
		return "yarn install --production --frozen-lockfile"
	}
	return "bun install --production --frozen-lockfile"
}

func isInstallNeeded(runtime string, build *config.Build) bool {
	if runtime == "static" {
		return false
	}
	if build != nil && build.Install != nil {
		return *build.Install
	}
	return true
}

func envKeys(content string) []string {
	var keys []string
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		k := strings.TrimSpace(parts[0])
		if k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

func setEnvValue(content string, key string, value string) string {
	lines := strings.Split(content, "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		k := strings.TrimSpace(parts[0])
		if k == key {
			lines[i] = fmt.Sprintf("%s=%s", key, value)
			found = true
			break
		}
	}
	if !found {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines[len(lines)-1] = fmt.Sprintf("%s=%s", key, value)
			lines = append(lines, "")
		} else {
			lines = append(lines, fmt.Sprintf("%s=%s", key, value))
		}
	}
	return strings.Join(lines, "\n")
}

func removeEnvValue(content string, key string) string {
	lines := strings.Split(content, "\n")
	var nextLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			nextLines = append(nextLines, line)
			continue
		}
		if !strings.Contains(line, "=") {
			nextLines = append(nextLines, line)
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		k := strings.TrimSpace(parts[0])
		if k != key {
			nextLines = append(nextLines, line)
		}
	}
	return strings.Join(nextLines, "\n")
}

func promptHidden(label string) (string, error) {
	script := fmt.Sprintf("printf %%s %s >&2; stty -echo; IFS= read -r value; status=$?; stty echo; printf '\\n' >&2; [ \"$status\" -eq 0 ] || exit \"$status\"; printf '%%s' \"$value\"", utils.ShellEscape(label))
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdin = os.Stdin
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func readSecretInput() (string, error) {
	// If stdin is a TTY, prompt hidden
	fi, err := os.Stdin.Stat()
	if err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		return promptHidden("Value: ")
	}
	// Else read all stdin
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(data), "\n"), nil
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

func serverCommandWithRawSuffix(args []string, rawSuffix string) string {
	cmd := serverCommand(args...)
	if rawSuffix != "" {
		cmd += " " + rawSuffix
	}
	return cmd
}

func serverStatusCommand() string {
	return serverCommand("status")
}

func serverDoctorCommand() string {
	return serverCommand("doctor")
}

func serverRouteListCommand(jsonFlag bool) string {
	args := []string{"route", "list"}
	if jsonFlag {
		args = append(args, "--json")
	}
	return serverCommand(args...)
}

func serverAppCreateCommand(appName string) string {
	return serverCommand("app", "create", appName)
}

func serverAppDestroyCommand(appName string) string {
	return serverCommand("app", "destroy", appName)
}

func serverAppReadEnvCommand(appName string) string {
	return serverCommand("app", "read-env", appName)
}

func serverAppInstallEnvCommand(appName string, envPath string) string {
	return serverCommand("app", "install-env", appName, envPath)
}

func serverAppInstallUnitCommand(appName string, service string, unitPath string) string {
	return serverCommand("app", "install-unit", appName, service, unitPath)
}

func serverAppUninstallUnitCommand(appName string, service string) string {
	return serverCommand("app", "uninstall-unit", appName, service)
}

func serverAppDaemonReloadCommand() string {
	return serverCommand("app", "daemon-reload")
}

func serverAppServiceCommand(action string, appName string, service string) string {
	return serverCommand("app", "service", action, appName, service)
}

func serverAppRunAsCommand(appName string, cwd string, command string) string {
	return serverCommandWithRawSuffix([]string{"app", "run-as", appName, "--cwd", cwd, "--"}, command)
}

func serverCloudflareRemoveAppCommand(appName string) string {
	return serverCommand("cloudflare", "remove", "--app", appName)
}

func serverRouteRemoveAppCommand(appName string) string {
	return serverCommand("route", "remove", "--app", appName)
}

func parseServerFlag(args []string) (string, []string, error) {
	var rest []string
	server := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg != "--server" {
			rest = append(rest, arg)
			continue
		}
		if i+1 >= len(args) {
			return "", nil, errors.New("--server requires a value")
		}
		server = args[i+1]
		if !config.ValidateSshTarget(server) {
			return "", nil, errors.New("--server must be an SSH target like deploy@example.com")
		}
		i++
	}
	return server, rest, nil
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

// Client commands

func CmdInit(root string) {
	manifestPath := filepath.Join(root, ManifestFile)
	if _, err := os.Stat(manifestPath); err == nil {
		utils.Die("simple-vps.toml already exists", 1)
	}

	locks := config.GetLockfiles(root)
	lock := ""
	if len(locks) > 0 {
		lock = locks[0]
	}
	pm := packageManagerForLockfile(lock)
	rt := runtimeForLockfile(lock)

	// Try reading package.json for script start/build
	name := filepath.Base(root)
	startCmd := fmt.Sprintf("%s run start", pm)
	if rt == "bun" {
		startCmd = "bun run src/server.ts"
	}
	buildBlock := ""

	packageJsonPath := filepath.Join(root, "package.json")
	if data, err := os.ReadFile(packageJsonPath); err == nil {
		// Scrape name and scripts
		var pkg struct {
			Name    string            `json:"name"`
			Scripts map[string]string `json:"scripts"`
		}
		// best effort parse
		_ = json.Unmarshal(data, &pkg)
		if pkg.Name != "" {
			name = pkg.Name
		}
		if pkg.Scripts != nil {
			if _, ok := pkg.Scripts["build"]; ok {
				buildBlock = fmt.Sprintf("[build]\ncommand = \"%s run build\"\noutput = \"dist\"\n\n", pm)
			}
			if _, ok := pkg.Scripts["start"]; !ok && rt == "node" {
				startCmd = "node dist/index.js"
			}
		}
	}

	content := fmt.Sprintf(`name = "%s"

%s[env.production]
server = "deploy@100.x.y.z"
runtime = "%s"

[services.web]
command = "%s"
port = 3000
healthcheck = "/health"

[routes.app]
host = "app.example.com"
type = "proxy"
service = "web"
`, name, buildBlock, rt, startCmd)

	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		utils.Die(err.Error(), 1)
	}
	fmt.Printf("Created %s\n", ManifestFile)
	fmt.Println("Next:")
	fmt.Printf("1. edit %s\n", ManifestFile)
	fmt.Println("2. simple-vps setup production")
	fmt.Println("3. simple-vps deploy production")
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

	// 2. check required server tools
	tools := []string{"simple-vps", "rsync"}
	if ctx.Runtime != "static" {
		tools = append(tools, ctx.Runtime)
	}
	for _, tool := range tools {
		errMsg := fmt.Sprintf("missing required server tool: %s", tool)
		if tool == "simple-vps" {
			errMsg = "missing Simple VPS server API; rerun the Simple VPS install"
		}
		runSSHChecked(runner, ctx.Server, fmt.Sprintf("command -v %s", utils.ShellEscape(tool)), errMsg)
	}

	// 3. create app
	runSSHChecked(runner, ctx.Server, serverAppCreateCommand(ctx.AppName), "simple-vps server app create failed; rerun the Simple VPS install")
	fmt.Printf("Setup complete for %s (%s)\n", ctx.AppName, envName)
}

func CmdStatus(root string, envName string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	currentPath := strings.TrimSpace(runSSHChecked(runner, ctx.Server, fmt.Sprintf("readlink -f %s/current 2>/dev/null || true", ctx.AppRoot), "failed to check current release"))
	releaseName := "none"
	if currentPath != "" {
		releaseName = filepath.Base(currentPath)
	}

	routesOut := runSSHChecked(runner, ctx.Server, serverRouteListCommand(true), "failed to read routes")
	type rWrap struct {
		Routes []struct {
			Host string `json:"host"`
			Type string `json:"type"`
			App  string `json:"app"`
		} `json:"routes"`
	}
	var wrap rWrap
	_ = json.Unmarshal([]byte(routesOut), &wrap)

	fmt.Printf("%s (%s)\n", ctx.AppName, envName)
	fmt.Printf("current: %s\n", releaseName)

	for svcName := range ctx.Services {
		_, _, code, _ := runner.RunSSH(ctx.Server, serverAppServiceCommand("is-active", ctx.AppName, svcName))
		status := "inactive"
		if code == 0 {
			status = "active"
		}
		fmt.Printf("service %s: %s\n", svcName, status)
	}

	hasRoutes := false
	for _, r := range wrap.Routes {
		if r.App == ctx.AppName {
			fmt.Printf("route %s: %s\n", r.Host, r.Type)
			hasRoutes = true
		}
	}
	if !hasRoutes {
		fmt.Println("routes: none")
	}
}

func CmdLogs(root string, envName string, serviceName string, tail bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	selected := serviceName
	if selected == "" {
		names := make([]string, 0, len(ctx.Services))
		for n := range ctx.Services {
			names = append(names, n)
		}
		if len(names) == 0 {
			utils.Die("no services configured", 1)
		}
		if len(names) > 1 {
			utils.Die("logs requires a service when multiple services are configured", 1)
		}
		selected = names[0]
	} else {
		if _, ok := ctx.Services[selected]; !ok {
			utils.Die(fmt.Sprintf("unknown service: %s", selected), 1)
		}
	}

	unit := fmt.Sprintf("simple-%s-%s.service", ctx.AppName, selected)
	cmdStr := fmt.Sprintf("journalctl -u %s -n 100", utils.ShellEscape(unit))
	if tail {
		cmdStr += " -f"
	}

	err = runner.RunSSHPassthrough(ctx.Server, cmdStr)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
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

func CmdSecretPut(root string, envName string, key string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	// Validate key
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(key) {
		utils.Die(fmt.Sprintf("invalid env key: %s", key), 1)
	}
	val, err := readSecretInput()
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	currentEnv := runSSHChecked(runner, ctx.Server, serverAppReadEnvCommand(ctx.AppName), "failed to read remote env")
	nextEnv := setEnvValue(currentEnv, key, val)

	uploadEnvContent(runner, ctx, nextEnv)
	fmt.Printf("Set secret %s for %s (%s). Run simple-vps restart %s <service> to apply.\n", key, ctx.AppName, envName, envName)
}

func CmdSecretList(root string, envName string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	currentEnv := runSSHChecked(runner, ctx.Server, serverAppReadEnvCommand(ctx.AppName), "failed to read remote env")
	for _, k := range envKeys(currentEnv) {
		fmt.Println(k)
	}
}

func CmdSecretRm(root string, envName string, key string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	currentEnv := runSSHChecked(runner, ctx.Server, serverAppReadEnvCommand(ctx.AppName), "failed to read remote env")
	nextEnv := removeEnvValue(currentEnv, key)

	if nextEnv == currentEnv {
		fmt.Printf("Secret %s was not set for %s (%s).\n", key, ctx.AppName, envName)
		return
	}

	uploadEnvContent(runner, ctx, nextEnv)
	fmt.Printf("Removed secret %s for %s (%s). Run simple-vps restart %s <service> to apply.\n", key, ctx.AppName, envName, envName)
}

func CmdEnvPush(root string, envName string, file string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	uploadEnvContent(runner, ctx, string(data))
	fmt.Printf("Pushed env for %s (%s). Run simple-vps restart %s <service> to apply.\n", ctx.AppName, envName, envName)
}

func uploadEnvContent(runner *CommandRunner, ctx *config.AppContext, content string) {
	// Re-validate using server validator
	lines := strings.Split(content, "\n")
	envKeyRe := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	for i, rawLine := range lines {
		lineIndex := i + 1
		line := strings.TrimSuffix(rawLine, "\r")
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		if strings.HasPrefix(stripped, "export ") {
			utils.Die(fmt.Sprintf("line %d: export is not supported", lineIndex), 1)
		}
		if !strings.Contains(line, "=") {
			utils.Die(fmt.Sprintf("line %d: expected KEY=value", lineIndex), 1)
		}
		parts := strings.SplitN(line, "=", 2)
		key := parts[0]
		value := parts[1]

		if strings.TrimSpace(key) != key {
			utils.Die(fmt.Sprintf("line %d: whitespace around keys is not supported", lineIndex), 1)
		}
		if !envKeyRe.MatchString(key) {
			utils.Die(fmt.Sprintf("line %d: invalid env key: %s", lineIndex, key), 1)
		}
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			utils.Die(fmt.Sprintf("line %d: quoted values are not supported", lineIndex), 1)
		}
		if regexp.MustCompile(`\s+#`).MatchString(value) {
			utils.Die(fmt.Sprintf("line %d: inline comments are not supported", lineIndex), 1)
		}
	}

	localDir, err := os.MkdirTemp("", "simple-vps-env-")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer os.RemoveAll(localDir)

	localPath := filepath.Join(localDir, ".env")
	if err := os.WriteFile(localPath, []byte(content), 0600); err != nil {
		utils.Die(err.Error(), 1)
	}

	remotePath := fmt.Sprintf("%s/%s-env-%s.env", RemoteDeployTmpDir, ctx.AppName, strconv.FormatInt(time.Now().UnixNano(), 36))
	err = runner.Upload(localPath, remotePath, ctx.Server)
	if err != nil {
		utils.Die(fmt.Sprintf("env upload failed: %v", err), 1)
	}

	runSSHChecked(runner, ctx.Server, serverAppInstallEnvCommand(ctx.AppName, remotePath), "env install failed")
}

func CmdRestart(root string, envName string, service string) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if _, ok := ctx.Services[service]; !ok {
		utils.Die(fmt.Sprintf("unknown service: %s", service), 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	runSSHChecked(runner, ctx.Server, serverAppServiceCommand("restart", ctx.AppName, service), fmt.Sprintf("failed to restart %s", service))

	// Health check
	svc := ctx.Services[service]
	if svc.Port != nil && svc.Healthcheck != "" {
		expectedStatus := 200
		if svc.HealthcheckStatus != nil {
			expectedStatus = *svc.HealthcheckStatus
		}
		timeout := 10
		if svc.HealthcheckTimeout != nil {
			timeout = *svc.HealthcheckTimeout
		}
		hcCmd := healthCheckCommand(*svc.Port, svc.Healthcheck, expectedStatus, timeout)
		runSSHChecked(runner, ctx.Server, hcCmd, fmt.Sprintf("health check failed for %s", service))
	}
}

func CmdHost(args []string) {
	sub := "status"

	serverFlag, rest, err := parseServerFlag(args)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if len(rest) > 0 {
		sub = rest[0]
	}

	if len(rest) > 1 || (sub != "status" && sub != "doctor") {
		utils.Die("host requires subcommand: status, doctor", 1)
	}

	server, err := readTargetServer(".", serverFlag)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	if sub == "status" {
		out := runSSHChecked(runner, server, serverStatusCommand(), "failed to read server status")
		fmt.Print(out)
	} else {
		out := runSSHChecked(runner, server, serverDoctorCommand(), "failed to run doctor")
		fmt.Print(out)
	}
}

func CmdRoute(args []string) {
	jsonFlag := false

	if len(args) > 0 && args[0] != "list" {
		utils.Die("route requires subcommand: list", 1)
	}

	remArgs := args
	if len(args) > 0 {
		remArgs = args[1:]
	}

	serverFlag, rest, err := parseServerFlag(remArgs)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	for _, arg := range rest {
		if arg == "--json" {
			jsonFlag = true
			continue
		}
		utils.Die(fmt.Sprintf("unknown argument: %s", arg), 1)
	}

	server, err := readTargetServer(".", serverFlag)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverRouteListCommand(jsonFlag), "failed to list routes")
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

	// Assert shared dir
	runSSHChecked(runner, ctx.Server, fmt.Sprintf("test -d %s/shared", ctx.AppRoot), fmt.Sprintf("setup has not run for %s", envName))

	// Resolve rollback target
	var target string
	releasesDir := ctx.AppRoot + "/releases"
	if release != "" {
		// Validate release name format (alphanumeric or timestamp)
		if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(release) {
			utils.Die(fmt.Sprintf("invalid release name: %s", release), 1)
		}
		target = releasesDir + "/" + release
		runSSHChecked(runner, ctx.Server, fmt.Sprintf("test -d %s", utils.ShellEscape(target)), fmt.Sprintf("release not found: %s", release))
	} else {
		targetCmd := fmt.Sprintf("current=$(readlink -f %s/current 2>/dev/null || true); for dir in $(ls -1dt %s/* 2>/dev/null); do [ -f \"$dir/%s\" ] || continue; [ \"$(readlink -f \"$dir\")\" = \"$current\" ] && continue; echo \"$dir\"; exit 0; done; exit 1", ctx.AppRoot, releasesDir, ReleaseSuccessMarker)
		target = strings.TrimSpace(runSSHChecked(runner, ctx.Server, targetCmd, "no previous successful release found"))
	}

	activateRelease(runner, ctx, target)
	fmt.Printf("Rolled back to %s\n", filepath.Base(target))
}

func CmdDestroy(root string, envName string, yes bool, confirmApp string, purge bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	if purge {
		if !yes || confirmApp != ctx.AppName {
			utils.Die(fmt.Sprintf("destroy --purge requires --yes --confirm %s", ctx.AppName), 1)
		}
	} else if !yes && confirmApp != ctx.AppName {
		utils.Die(fmt.Sprintf("destroy requires --yes or --confirm %s", ctx.AppName), 1)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	// 1. stop services
	for svcName := range ctx.Services {
		_, _, _, _ = runner.RunSSH(ctx.Server, serverAppServiceCommand("stop", ctx.AppName, svcName))
		_, _, _, _ = runner.RunSSH(ctx.Server, serverAppServiceCommand("disable", ctx.AppName, svcName))
		_, _, _, _ = runner.RunSSH(ctx.Server, serverAppUninstallUnitCommand(ctx.AppName, svcName))
	}

	// 2. remove routes
	_, _, _, _ = runner.RunSSH(ctx.Server, serverCloudflareRemoveAppCommand(ctx.AppName))
	_, _, _, _ = runner.RunSSH(ctx.Server, serverRouteRemoveAppCommand(ctx.AppName))

	// 3. remove current symlink
	_, _, _, _ = runner.RunSSH(ctx.Server, fmt.Sprintf("rm -f %s/current", ctx.AppRoot))

	// 4. purge app data if requested
	if purge {
		runSSHChecked(runner, ctx.Server, serverAppDestroyCommand(ctx.AppName), "failed to purge app data")
	}

	fmt.Printf("Destroyed app %s (%s)\n", ctx.AppName, envName)
}

func CmdDeploy(root string, envName string, dirty bool, includeDotenv bool) {
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	shaOut, _, code, _ := runCommand("git", []string{"rev-parse", "HEAD"}, root)
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
		release = fmt.Sprintf("%s-dirty-%s", release, time.Now().UTC().Format("20060102150405"))
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer runner.Close()

	// Assert shared directory on host
	runSSHChecked(runner, ctx.Server, fmt.Sprintf("test -d %s/shared", ctx.AppRoot), fmt.Sprintf("setup has not run for %s", envName))

	// Prepare artifact locally
	artifactDir, locks, err := prepareArtifact(root, ctx.Build, ctx.Runtime, worktreeDirty)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer os.RemoveAll(artifactDir)
	releaseDir := ctx.AppRoot + "/releases/" + release

	if !includeDotenv {
		if err := validateArtifactDotenv(artifactDir); err != nil {
			utils.Die(err.Error(), 1)
		}
	}

	// Create release dir
	runSSHChecked(runner, ctx.Server, fmt.Sprintf("install -d -m 2775 %s", utils.ShellEscape(releaseDir)), "failed to create release directory")

	// Upload artifact
	err = runner.UploadDirectory(artifactDir, releaseDir, ctx.Server)
	if err != nil {
		utils.Die(fmt.Sprintf("failed to upload artifact: %v", err), 1)
	}
	fixReleasePermissions(runner, ctx, releaseDir)

	// Link shared paths
	for _, entry := range []string{".env", "db", "storage", "logs"} {
		runSSHChecked(runner, ctx.Server, fmt.Sprintf("ln -sfn %s/shared/%s %s/%s", ctx.AppRoot, entry, releaseDir, entry), fmt.Sprintf("failed to link shared %s", entry))
	}

	// Dependencies install
	if isInstallNeeded(ctx.Runtime, ctx.Build) && len(locks) > 0 {
		runSSHChecked(runner, ctx.Server, serverAppRunAsCommand(ctx.AppName, releaseDir, installCommandFor(locks[0])), "production install failed")
	}

	// Generate systemd unit files locally
	localUnitDir, err := os.MkdirTemp("", "simple-vps-units-")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer os.RemoveAll(localUnitDir)

	for svcName, svc := range ctx.Services {
		unitContent := renderUnit(ctx.AppName, envName, release, svcName, svc)
		unitName := fmt.Sprintf("simple-%s-%s.service", ctx.AppName, svcName)
		_ = os.WriteFile(filepath.Join(localUnitDir, unitName), []byte(unitContent), 0644)
	}

	remoteUnitDir := fmt.Sprintf("%s/%s", RemoteDeployTmpDir, release)
	runSSHChecked(runner, ctx.Server, fmt.Sprintf("mkdir -p %s", remoteUnitDir), "failed to create remote unit directory")

	err = runner.UploadDirectory(localUnitDir, remoteUnitDir, ctx.Server)
	if err != nil {
		utils.Die(fmt.Sprintf("failed to upload unit files: %v", err), 1)
	}

	for svcName := range ctx.Services {
		unitName := fmt.Sprintf("simple-%s-%s.service", ctx.AppName, svcName)
		runSSHChecked(runner, ctx.Server, serverAppInstallUnitCommand(ctx.AppName, svcName, fmt.Sprintf("%s/%s", remoteUnitDir, unitName)), fmt.Sprintf("failed to install %s unit", svcName))
	}

	// reload daemon
	runSSHChecked(runner, ctx.Server, serverAppDaemonReloadCommand(), "systemd daemon-reload failed")

	// activate release
	activateRelease(runner, ctx, releaseDir)

	// publish routes
	publishRoutes(runner, ctx)

	// touch success marker
	runSSHChecked(runner, ctx.Server, fmt.Sprintf("touch %s/%s", utils.ShellEscape(releaseDir), ReleaseSuccessMarker), "failed to mark release successful")

	// prune old releases
	keep := 5
	manifest, err := config.ReadManifest(root)
	if err == nil && manifest.Env != nil {
		if envBlock, ok := manifest.Env[envName]; ok && envBlock.KeepReleases != nil {
			keep = *envBlock.KeepReleases
		}
	}
	pruneCmd := pruneReleasesCommand(ctx.AppRoot, keep)
	if _, stderr, code, err := runner.RunSSH(ctx.Server, pruneCmd); err != nil || code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" && err != nil {
			detail = err.Error()
		}
		if detail == "" {
			detail = fmt.Sprintf("exit %d", code)
		}
		fmt.Fprintf(os.Stderr, "Warning: deploy succeeded; pruning failed: failed to prune releases: %s\n", detail)
	}

	fmt.Printf("Deployed %s %s to %s\n", ctx.AppName, release[:min(7, len(release))], envName)
}

func activateRelease(runner *CommandRunner, ctx *config.AppContext, releaseDir string) {
	// Read previous current
	previousCurrent := strings.TrimSpace(runSSHChecked(runner, ctx.Server, fmt.Sprintf("readlink -f %s/current 2>/dev/null || true", ctx.AppRoot), ""))

	// Stop old services
	for svcName := range ctx.Services {
		_, _, _, _ = runner.RunSSH(ctx.Server, serverAppServiceCommand("stop", ctx.AppName, svcName))
	}

	// Link current
	runSSHChecked(runner, ctx.Server, fmt.Sprintf("ln -sfn %s %s/current", releaseDir, ctx.AppRoot), "failed to activate release")

	// Start new services
	for svcName := range ctx.Services {
		runSSHChecked(runner, ctx.Server, serverAppServiceCommand("start", ctx.AppName, svcName), fmt.Sprintf("failed to start %s", svcName))
	}

	// Health check
	hcErr := false
	for _, svc := range ctx.Services {
		if svc.Port != nil && svc.Healthcheck != "" {
			expectedStatus := 200
			if svc.HealthcheckStatus != nil {
				expectedStatus = *svc.HealthcheckStatus
			}
			timeout := 10
			if svc.HealthcheckTimeout != nil {
				timeout = *svc.HealthcheckTimeout
			}
			hcCmd := healthCheckCommand(*svc.Port, svc.Healthcheck, expectedStatus, timeout)
			_, _, code, _ := runner.RunSSH(ctx.Server, hcCmd)
			if code != 0 {
				hcErr = true
				break
			}
		}
	}

	if hcErr {
		// Rollback services
		for svcName := range ctx.Services {
			_, _, _, _ = runner.RunSSH(ctx.Server, serverAppServiceCommand("stop", ctx.AppName, svcName))
		}
		if previousCurrent != "" {
			_, _, _, _ = runner.RunSSH(ctx.Server, fmt.Sprintf("ln -sfn %s %s/current", previousCurrent, ctx.AppRoot))
			for svcName := range ctx.Services {
				_, _, _, _ = runner.RunSSH(ctx.Server, serverAppServiceCommand("start", ctx.AppName, svcName))
			}
		}
		utils.Die("health check failed; release rolled back", 1)
	}
}

func fixReleasePermissions(runner *CommandRunner, ctx *config.AppContext, releaseDir string) {
	cmd := releasePermissionsCommand(ctx.AppName, releaseDir)
	runSSHChecked(runner, ctx.Server, cmd, "failed to restore release permissions")
}

func releasePermissionsCommand(appName string, releaseDir string) string {
	group := "app-" + appName
	escapedReleaseDir := utils.ShellEscape(releaseDir)
	return strings.Join([]string{
		fmt.Sprintf("chgrp -R %s %s", utils.ShellEscape(group), escapedReleaseDir),
		fmt.Sprintf("chmod -R g+rwX %s", escapedReleaseDir),
		fmt.Sprintf("find %s -type d -exec chmod g+s {} +", escapedReleaseDir),
		fmt.Sprintf("chmod 2775 %s", escapedReleaseDir),
	}, " && ")
}

func healthCheckCommand(port int, path string, expectedStatus int, timeout int) string {
	return fmt.Sprintf("for i in $(seq 1 %d); do status=$(curl -o /dev/null -s -w '%%{http_code}' --max-time 2 http://127.0.0.1:%d%s || true); [ \"$status\" = \"%d\" ] && exit 0; sleep 1; done; exit 1", timeout, port, path, expectedStatus)
}

func pruneReleasesCommand(appRoot string, keep int) string {
	releases := appRoot + "/releases"
	current := appRoot + "/current"
	return fmt.Sprintf("set -eu; releases=%s; current=$(readlink -f %s 2>/dev/null || true); previous=$(find \"$releases\" -mindepth 1 -maxdepth 1 -type d -printf '%%T@ %%p\\n' 2>/dev/null | sort -rn | while read -r _ dir; do [ -f \"$dir/%s\" ] || continue; resolved=$(readlink -f \"$dir\"); [ \"$resolved\" = \"$current\" ] && continue; echo \"$resolved\"; break; done); count=0; find \"$releases\" -mindepth 1 -maxdepth 1 -type d -printf '%%T@ %%p\\n' 2>/dev/null | sort -rn | while read -r _ dir; do count=$((count + 1)); resolved=$(readlink -f \"$dir\"); if [ \"$resolved\" = \"$current\" ] || [ \"$resolved\" = \"$previous\" ] || [ \"$count\" -le %d ]; then continue; fi; rm -rf -- \"$dir\"; done", utils.ShellEscape(releases), utils.ShellEscape(current), ReleaseSuccessMarker, keep)
}

func renderUnit(appName string, envName string, release string, serviceName string, svc config.Service) string {
	releaseDir := fmt.Sprintf("/var/apps/%s/releases/%s", appName, release)
	lines := []string{
		"[Unit]",
		fmt.Sprintf("Description=simple-vps: %s/%s", appName, serviceName),
		"After=network.target",
		"",
		"[Service]",
		"Type=simple",
		fmt.Sprintf("User=app-%s", appName),
		fmt.Sprintf("Group=app-%s", appName),
		fmt.Sprintf("WorkingDirectory=/var/apps/%s/current", appName),
		fmt.Sprintf("EnvironmentFile=/var/apps/%s/shared/.env", appName),
		fmt.Sprintf("Environment=\"SIMPLE_APP_NAME=%s\"", appName),
		fmt.Sprintf("Environment=\"SIMPLE_ENV=%s\"", envName),
		fmt.Sprintf("Environment=\"SIMPLE_RELEASE=%s\"", release),
		fmt.Sprintf("Environment=\"SIMPLE_RELEASE_DIR=%s\"", releaseDir),
		"Environment=\"NODE_ENV=production\"",
	}
	if svc.Port != nil {
		lines = append(lines, fmt.Sprintf("Environment=\"PORT=%d\"", *svc.Port))
	}
	escCmd := strings.ReplaceAll(svc.Command, "'", "'\\''")
	lines = append(lines,
		fmt.Sprintf("ExecStart=/usr/bin/env bash -c 'exec %s'", escCmd),
		"Restart=on-failure",
		"RestartSec=5s",
		"StandardOutput=journal",
		"StandardError=journal",
		"NoNewPrivileges=true",
		"PrivateTmp=true",
		"ProtectSystem=strict",
		"ProtectHome=true",
		fmt.Sprintf("ReadWritePaths=/var/apps/%s/shared", appName),
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	)
	return strings.Join(lines, "\n")
}

func publishRoutes(runner *CommandRunner, ctx *config.AppContext) {
	for _, route := range ctx.Routes {
		cfCmd := cloudflarePublishCommand(ctx.AppName, route.Host)
		cfOut := strings.TrimSpace(runSSHChecked(runner, ctx.Server, cfCmd, fmt.Sprintf("failed to publish Cloudflare route %s", route.Host)))
		if cfOut != "" {
			fmt.Println(cfOut)
		}

		cmd := routePublishCommand(ctx, route)
		if cmd != "" {
			runSSHChecked(runner, ctx.Server, cmd, fmt.Sprintf("failed to publish route %s", route.Host))
		}
	}
}

func cloudflarePublishCommand(appName string, host string) string {
	return serverCommand("cloudflare", "publish", "--app", appName, host)
}

func routePublishCommand(ctx *config.AppContext, route config.Route) string {
	if route.Type == "proxy" {
		svc := ctx.Services[route.Service]
		p := 80
		if svc.Port != nil {
			p = *svc.Port
		}
		args := []string{"route", "proxy", "--port", strconv.Itoa(p), "--app", ctx.AppName}
		if route.Service != "" {
			args = append(args, "--service", route.Service)
		}
		args = append(args, route.Host)
		return serverCommand(args...)
	}
	if route.Type == "static" {
		return serverCommand("route", "static", "--root", ctx.AppRoot+"/current", "--app", ctx.AppName, route.Host)
	}
	if route.Type == "redirect" {
		return serverCommand("route", "redirect", "--to", route.To, "--app", ctx.AppName, route.Host)
	}
	return ""
}

// Directory copy helper
func copyDirectoryContents(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

func copyPath(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return copyDirectoryContents(src, dst)
	}
	return copyFile(src, dst)
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

func prepareArtifact(root string, build *config.Build, runtime string, dirty bool) (string, []string, error) {
	checkoutDir, err := os.MkdirTemp("", "simple-vps-checkout-")
	if err != nil {
		return "", nil, err
	}
	var cmd *exec.Cmd
	if dirty {
		cmd = exec.Command("sh", "-c", fmt.Sprintf("tar -C %s --exclude .git --exclude node_modules -cf - . | tar -x -C %s", utils.ShellEscape(root), utils.ShellEscape(checkoutDir)))
	} else {
		cmd = exec.Command("sh", "-c", fmt.Sprintf("git -C %s archive HEAD | tar -x -C %s", utils.ShellEscape(root), utils.ShellEscape(checkoutDir)))
	}
	if err := cmd.Run(); err != nil {
		os.RemoveAll(checkoutDir)
		return "", nil, fmt.Errorf("failed to create release checkout: %w", err)
	}

	lockfiles := config.GetLockfiles(checkoutDir)

	if build == nil {
		return checkoutDir, lockfiles, nil
	}

	if build.Command != "" {
		buildCmd := exec.Command("sh", "-c", fmt.Sprintf("cd %s && %s", utils.ShellEscape(checkoutDir), build.Command))
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			os.RemoveAll(checkoutDir)
			return "", nil, fmt.Errorf("build failed: %w", err)
		}
	}

	artifactDir, err := os.MkdirTemp("", "simple-vps-artifact-")
	if err != nil {
		os.RemoveAll(checkoutDir)
		return "", nil, err
	}

	outputDir := filepath.Join(checkoutDir, build.Output)
	if err := copyDirectoryContents(outputDir, artifactDir); err != nil {
		os.RemoveAll(checkoutDir)
		os.RemoveAll(artifactDir)
		return "", nil, fmt.Errorf("failed to copy build output: %w", err)
	}

	for _, entry := range build.Include {
		src := filepath.Join(checkoutDir, entry)
		dst := filepath.Join(artifactDir, entry)
		if err := copyPath(src, dst); err != nil {
			os.RemoveAll(checkoutDir)
			os.RemoveAll(artifactDir)
			return "", nil, fmt.Errorf("failed to copy include path %q: %w", entry, err)
		}
	}

	if isInstallNeeded(runtime, build) {
		_ = copyFile(filepath.Join(checkoutDir, "package.json"), filepath.Join(artifactDir, "package.json"))
		if len(lockfiles) > 0 {
			_ = copyFile(filepath.Join(checkoutDir, lockfiles[0]), filepath.Join(artifactDir, lockfiles[0]))
		}
	}

	os.RemoveAll(checkoutDir)
	return artifactDir, lockfiles, nil
}

package hostinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fprl/simple-vps/internal/provision"
	"github.com/fprl/simple-vps/internal/provision/local"
	"github.com/fprl/simple-vps/internal/utils"
)

type Options struct {
	Mode                     string
	TargetHost               string
	BootstrapUser            string
	SSHKey                   string
	SSHPublicKeyFile         string
	OperatorSSHPublicKeyFile string
	DeploySSHPublicKeyFile   string
	OperatorUser             string
	DeployUser               string
	Timezone                 string
	Locale                   string
	Tailscale                bool
	TailscaleAuthKey         string
	TailscaleHostname        string
	CloudflareTunnel         bool
	CloudflareAPIToken       string
	CloudflareAccountID      string
	CloudflareTunnelToken    string
	CloudflareTunnelConfig   string
	InstallDocker            bool
	InstallLitestream        bool
	CheckMode                bool
	AssumeYes                bool
	SharedKey                bool
}

type Plan struct {
	Mode                     string
	TargetHost               string
	BootstrapUser            string
	SSHKey                   string
	OperatorSSHPublicKeyFile string
	DeploySSHPublicKeyFile   string
	OperatorUser             string
	DeployUser               string
	Timezone                 string
	Locale                   string
	Tailscale                bool
	TailscaleAuthKey         string
	TailscaleHostname        string
	TailscaleAuthMode        string
	CloudflareTunnel         bool
	CloudflareAPIToken       string
	CloudflareAccountID      string
	CloudflareTunnelToken    string
	CloudflareTunnelConfig   string
	CloudflareServiceMode    string
	InstallDocker            bool
	InstallLitestream        bool
	CheckMode                bool
	SharedKey                bool
}

type Installer struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Env    map[string]string

	geteuid func() int
	run     func(name string, args []string, cwd string) error
	look    func(file string) (string, error)
}

func NewInstaller() *Installer {
	return &Installer{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
		Env:    environMap(),
		geteuid: func() int {
			return os.Geteuid()
		},
		run:  runPassthrough,
		look: exec.LookPath,
	}
}

func (i *Installer) RunOptions(opts Options) error {
	plan, err := BuildPlan(opts, i.geteuid() == 0, fileExists("/etc/os-release"))
	if err != nil {
		return err
	}

	if envBool(i.Env, "SIMPLE_VPS_INSTALLER_DUMP_PLAN", false) {
		return i.dumpInstallPlan(plan)
	}

	i.info("Simple VPS installer starting")
	i.info("Mode: %s", plan.Mode)
	i.info("Operator user: %s", plan.OperatorUser)
	i.info("Deploy user: %s", plan.DeployUser)
	i.info("Timezone: %s", plan.Timezone)
	i.info("Tailscale: %s", boolText(plan.Tailscale))
	if plan.Tailscale {
		i.info("Tailscale auth: %s", presentOrMissing(plan.TailscaleAuthKey, "auth key provided", "manual login required"))
	}
	i.info("Cloudflare Tunnel: %s", boolText(plan.CloudflareTunnel))
	if plan.CloudflareTunnel {
		switch {
		case plan.CloudflareAPIToken != "":
			i.info("Cloudflare API: token provided")
		case plan.CloudflareTunnelConfig != "":
			i.info("Cloudflare Tunnel config: %s", plan.CloudflareTunnelConfig)
		default:
			i.info("Cloudflare Tunnel auth: %s", presentOrMissing(plan.CloudflareTunnelToken, "token provided", "service not enabled"))
		}
	}
	i.info("Docker: %s", boolText(plan.InstallDocker))
	i.info("Litestream: %s", boolText(plan.InstallLitestream))

	switch plan.Mode {
	case "remote":
		repoRoot, err := locateRepoRoot()
		if err != nil {
			return err
		}
		err = i.runRemote(plan, repoRoot)
	case "local":
		err = i.runLocal(plan)
	default:
		err = fmt.Errorf("invalid mode: %s", plan.Mode)
	}
	if err != nil {
		return err
	}

	i.info("Provisioning complete")
	return nil
}

func DefaultOptions(env map[string]string) Options {
	if env == nil {
		env = environMap()
	}
	return Options{
		Mode:                     "auto",
		BootstrapUser:            "root",
		OperatorSSHPublicKeyFile: env["SIMPLE_VPS_OPERATOR_SSH_PUBLIC_KEY_FILE"],
		DeploySSHPublicKeyFile:   env["SIMPLE_VPS_DEPLOY_SSH_PUBLIC_KEY_FILE"],
		OperatorUser:             envDefault(env, "SIMPLE_VPS_OPERATOR_USER", "operator"),
		DeployUser:               envDefault(env, "SIMPLE_VPS_DEPLOY_USER", "deploy"),
		Timezone:                 envDefault(env, "SIMPLE_VPS_TIMEZONE", "UTC"),
		Locale:                   envDefault(env, "SIMPLE_VPS_LOCALE", "en_US.UTF-8"),
		Tailscale:                true,
		TailscaleAuthKey:         env["SIMPLE_VPS_TAILSCALE_AUTH_KEY"],
		TailscaleHostname:        env["SIMPLE_VPS_TAILSCALE_HOSTNAME"],
		CloudflareTunnel:         true,
		CloudflareAPIToken:       env["SIMPLE_VPS_CLOUDFLARE_API_TOKEN"],
		CloudflareAccountID:      env["SIMPLE_VPS_CLOUDFLARE_ACCOUNT_ID"],
		CloudflareTunnelToken:    env["SIMPLE_VPS_CLOUDFLARE_TUNNEL_TOKEN"],
		CloudflareTunnelConfig:   env["SIMPLE_VPS_CLOUDFLARE_TUNNEL_CONFIG"],
		InstallDocker:            envBool(env, "SIMPLE_VPS_INSTALL_DOCKER", false),
		InstallLitestream:        envBool(env, "SIMPLE_VPS_INSTALL_LITESTREAM", true),
	}
}

func BuildPlan(opts Options, isRoot bool, osReleaseExists bool) (Plan, error) {
	mode := opts.Mode
	if mode == "auto" {
		if opts.TargetHost != "" {
			mode = "remote"
		} else if isRoot && osReleaseExists {
			mode = "local"
		} else {
			mode = "remote"
		}
	}
	if mode != "local" && mode != "remote" {
		return Plan{}, fmt.Errorf("invalid mode: %s (expected local, remote, or auto)", opts.Mode)
	}

	if mode == "remote" {
		if opts.TargetHost == "" {
			return Plan{}, errors.New("TARGET_HOST is required in remote mode.")
		}
		if opts.BootstrapUser == "" {
			return Plan{}, errors.New("BOOTSTRAP_USER is required in remote mode.")
		}
		if opts.SSHKey != "" && !fileExists(opts.SSHKey) {
			return Plan{}, fmt.Errorf("SSH key file not found: %s", opts.SSHKey)
		}
	}

	if opts.OperatorUser == opts.DeployUser {
		return Plan{}, errors.New("Operator and deploy users must be different.")
	}
	if !opts.Tailscale {
		if opts.TailscaleAuthKey != "" {
			return Plan{}, errors.New("--tailscale-auth-key requires Tailscale to be enabled.")
		}
		if opts.TailscaleHostname != "" {
			return Plan{}, errors.New("--tailscale-hostname requires Tailscale to be enabled.")
		}
	}
	if !opts.CloudflareTunnel {
		if opts.CloudflareTunnelToken != "" {
			return Plan{}, errors.New("--cloudflare-tunnel-token requires Cloudflare Tunnel to be enabled.")
		}
		if opts.CloudflareAPIToken != "" {
			return Plan{}, errors.New("--cloudflare-api-token requires Cloudflare Tunnel to be enabled.")
		}
		if opts.CloudflareAccountID != "" {
			return Plan{}, errors.New("--cloudflare-account-id requires Cloudflare Tunnel to be enabled.")
		}
		if opts.CloudflareTunnelConfig != "" {
			return Plan{}, errors.New("--cloudflare-tunnel-config requires Cloudflare Tunnel to be enabled.")
		}
	}
	if opts.CloudflareAPIToken != "" && opts.CloudflareTunnelToken != "" {
		return Plan{}, errors.New("use either --cloudflare-api-token or --cloudflare-tunnel-token, not both.")
	}
	if opts.CloudflareAPIToken != "" && opts.CloudflareTunnelConfig != "" {
		return Plan{}, errors.New("use either --cloudflare-api-token or --cloudflare-tunnel-config, not both.")
	}
	if opts.CloudflareTunnelToken != "" && opts.CloudflareTunnelConfig != "" {
		return Plan{}, errors.New("use either --cloudflare-tunnel-token or --cloudflare-tunnel-config, not both.")
	}

	operatorKeyFile := opts.OperatorSSHPublicKeyFile
	deployKeyFile := opts.DeploySSHPublicKeyFile
	if opts.SSHPublicKeyFile != "" {
		if operatorKeyFile == "" {
			operatorKeyFile = opts.SSHPublicKeyFile
		}
		if opts.SharedKey && deployKeyFile == "" {
			deployKeyFile = opts.SSHPublicKeyFile
		}
	} else if operatorKeyFile == "" && opts.SSHKey != "" && fileExists(opts.SSHKey+".pub") {
		operatorKeyFile = opts.SSHKey + ".pub"
	}

	return Plan{
		Mode:                     mode,
		TargetHost:               opts.TargetHost,
		BootstrapUser:            opts.BootstrapUser,
		SSHKey:                   opts.SSHKey,
		OperatorSSHPublicKeyFile: operatorKeyFile,
		DeploySSHPublicKeyFile:   deployKeyFile,
		OperatorUser:             opts.OperatorUser,
		DeployUser:               opts.DeployUser,
		Timezone:                 opts.Timezone,
		Locale:                   opts.Locale,
		Tailscale:                opts.Tailscale,
		TailscaleAuthKey:         opts.TailscaleAuthKey,
		TailscaleHostname:        opts.TailscaleHostname,
		TailscaleAuthMode:        tailscaleAuthMode(opts.Tailscale, opts.TailscaleAuthKey),
		CloudflareTunnel:         opts.CloudflareTunnel,
		CloudflareAPIToken:       opts.CloudflareAPIToken,
		CloudflareAccountID:      opts.CloudflareAccountID,
		CloudflareTunnelToken:    opts.CloudflareTunnelToken,
		CloudflareTunnelConfig:   opts.CloudflareTunnelConfig,
		CloudflareServiceMode:    cloudflareServiceMode(opts),
		InstallDocker:            opts.InstallDocker,
		InstallLitestream:        opts.InstallLitestream,
		CheckMode:                opts.CheckMode,
		SharedKey:                opts.SharedKey,
	}, nil
}

func (i *Installer) runRemote(plan Plan, repoRoot string) error {
	keyPlan, err := resolveSSHKeyPlan(plan, false, "")
	if err != nil {
		return err
	}

	i.info("Running in remote mode against %s", plan.TargetHost)
	if err := i.preflightSSH(plan); err != nil {
		return err
	}

	helperDir, cleanupHelper, err := i.prepareGoHelperBinaries(repoRoot)
	if err != nil {
		return err
	}
	defer cleanupHelper()

	arch, err := i.remoteArch(plan)
	if err != nil {
		return err
	}
	helper := filepath.Join(helperDir, "simple-vps-linux-"+arch)
	if !fileExists(helper) {
		return fmt.Errorf("Simple VPS helper binary not found for target architecture %s: %s", arch, helper)
	}

	remoteHelper := "/tmp/simple-vps-host-install"
	if err := i.copyRemote(plan, helper, remoteHelper); err != nil {
		return err
	}
	if err := i.remoteCommand(plan, "chmod 0755 "+utils.ShellEscape(remoteHelper)); err != nil {
		return err
	}
	operatorKeyFile, deployKeyFile, cleanupKeys, err := i.writeRemoteKeyFiles(plan, keyPlan)
	if err != nil {
		return err
	}
	defer cleanupKeys()

	cmd := remoteLocalInstallCommand(remoteHelper, plan, operatorKeyFile, deployKeyFile)
	i.step("Running Go provisioner on target")
	if plan.BootstrapUser == "root" {
		return i.remoteCommand(plan, cmd)
	}
	return i.remoteCommand(plan, "sudo -n "+cmd)
}

func (i *Installer) runLocal(plan Plan) error {
	if i.geteuid() != 0 {
		return errors.New("local mode must run as root")
	}
	keyPlan, err := resolveSSHKeyPlan(plan, true, "/root/.ssh/authorized_keys")
	if err != nil {
		return err
	}

	helperPath, err := os.Executable()
	if err != nil {
		return err
	}

	i.info("Running in local mode on localhost")
	summary, err := provision.RunInstall(context.Background(), local.Runner{}, provision.InstallOptions{
		OperatorUser:           plan.OperatorUser,
		DeployUser:             plan.DeployUser,
		OperatorSSHPublicKeys:  nonEmptyStrings(keyPlan.Operator),
		DeploySSHPublicKeys:    nonEmptyStrings(keyPlan.Deploy),
		Timezone:               plan.Timezone,
		Locale:                 plan.Locale,
		Tailscale:              plan.Tailscale,
		TailscaleAuthKey:       plan.TailscaleAuthKey,
		TailscaleHostname:      plan.TailscaleHostname,
		CloudflareTunnel:       plan.CloudflareTunnel,
		CloudflareAPIToken:     plan.CloudflareAPIToken,
		CloudflareAccountID:    plan.CloudflareAccountID,
		CloudflareTunnelToken:  plan.CloudflareTunnelToken,
		CloudflareTunnelConfig: plan.CloudflareTunnelConfig,
		InstallDocker:          plan.InstallDocker,
		InstallLitestream:      plan.InstallLitestream,
		CheckMode:              plan.CheckMode,
		HelperBinaryPath:       helperPath,
	})
	if err != nil {
		return err
	}
	i.info("Apply %s changed %d operations", summary.ApplyID, summary.OperationsChanged)
	return nil
}

func (i *Installer) dumpInstallPlan(plan Plan) error {
	requireOperatorKey := false
	rootKeysPath := ""
	if plan.Mode == "local" {
		requireOperatorKey = true
		rootKeysPath = "/root/.ssh/authorized_keys"
	}

	keyPlan, err := resolveSSHKeyPlan(plan, requireOperatorKey, rootKeysPath)
	if err != nil {
		return err
	}

	fmt.Fprintf(i.Stdout, "plan.mode=%s\n", plan.Mode)
	fmt.Fprintf(i.Stdout, "plan.target_host=%s\n", plan.TargetHost)
	fmt.Fprintf(i.Stdout, "plan.bootstrap_user=%s\n", plan.BootstrapUser)
	fmt.Fprintf(i.Stdout, "plan.operator_user=%s\n", plan.OperatorUser)
	fmt.Fprintf(i.Stdout, "plan.deploy_user=%s\n", plan.DeployUser)
	fmt.Fprintf(i.Stdout, "plan.shared_key=%s\n", boolText(plan.SharedKey))
	fmt.Fprintf(i.Stdout, "plan.tailscale=%s\n", boolText(plan.Tailscale))
	fmt.Fprintf(i.Stdout, "plan.tailscale_auth_mode=%s\n", plan.TailscaleAuthMode)
	fmt.Fprintf(i.Stdout, "plan.cloudflare_tunnel=%s\n", boolText(plan.CloudflareTunnel))
	fmt.Fprintf(i.Stdout, "plan.cloudflare_service_mode=%s\n", plan.CloudflareServiceMode)
	fmt.Fprintf(i.Stdout, "plan.docker=%s\n", boolText(plan.InstallDocker))
	fmt.Fprintf(i.Stdout, "plan.litestream=%s\n", boolText(plan.InstallLitestream))
	fmt.Fprintf(i.Stdout, "plan.check_mode=%s\n", boolText(plan.CheckMode))
	fmt.Fprintf(i.Stdout, "plan.operator_key=%s\n", presentOrMissing(keyPlan.Operator, "present", "missing"))
	fmt.Fprintf(i.Stdout, "plan.deploy_key=%s\n", presentOrMissing(keyPlan.Deploy, "present", "missing"))
	if plan.Mode == "remote" {
		fmt.Fprintln(i.Stdout, "--- remote-local-command ---")
		fmt.Fprintln(i.Stdout, remoteLocalInstallCommand("/tmp/simple-vps-host-install", plan, "/tmp/simple-vps-operator.pub", "/tmp/simple-vps-deploy.pub"))
	}
	return nil
}

func (i *Installer) prepareGoHelperBinaries(repoRoot string) (string, func(), error) {
	distDir := filepath.Join(repoRoot, "dist")
	if helperBinariesExist(distDir) {
		i.info("Using prebuilt Simple VPS Go helper binaries from %s", distDir)
		return distDir, func() {}, nil
	}

	if _, err := i.look("go"); err != nil {
		return "", func() {}, errors.New("Simple VPS Go helper binaries are required, but no prebuilt dist/ binaries were found and Go is not installed")
	}

	if !fileExists(filepath.Join(repoRoot, "go.mod")) {
		return "", func() {}, fmt.Errorf("Simple VPS Go module not found at %s; cannot prepare helper binaries", repoRoot)
	}

	outputDir, err := os.MkdirTemp("", "simple-vps-helper-")
	if err != nil {
		return "", func() {}, err
	}

	i.info("Building Simple VPS Go helper binaries")
	for _, arch := range []string{"amd64", "arm64"} {
		env := append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(outputDir, "simple-vps-linux-"+arch), ".")
		cmd.Dir = repoRoot
		cmd.Env = env
		cmd.Stdout = i.Stdout
		cmd.Stderr = i.Stderr
		if err := cmd.Run(); err != nil {
			_ = os.RemoveAll(outputDir)
			return "", func() {}, err
		}
	}
	return outputDir, func() { _ = os.RemoveAll(outputDir) }, nil
}

func (i *Installer) preflightSSH(plan Plan) error {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=7"}
	if plan.SSHKey != "" {
		args = append(args, "-i", plan.SSHKey)
	}
	args = append(args, plan.BootstrapUser+"@"+plan.TargetHost, "echo connected")
	if err := i.run("ssh", args, ""); err != nil {
		var msg bytes.Buffer
		fmt.Fprintf(&msg, "SSH preflight failed for %s@%s.", plan.BootstrapUser, plan.TargetHost)
		if plan.SSHKey == "" {
			msg.WriteString("\nRemote mode expects SSH key-based auth (via ssh config/agent/default keys).")
			msg.WriteString("\nIf you only have password credentials, SSH to the VPS first and use --mode local.")
		}
		msg.WriteString("\nCheck host, credentials, and key access.")
		return errors.New(msg.String())
	}
	return nil
}

func (i *Installer) remoteArch(plan Plan) (string, error) {
	output, err := i.remoteOutput(plan, "uname -m")
	if err != nil {
		return "", err
	}
	switch strings.TrimSpace(output) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported target architecture: %s", strings.TrimSpace(output))
	}
}

func (i *Installer) remoteOutput(plan Plan, command string) (string, error) {
	args := sshArgs(plan, command)
	cmd := exec.Command("ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh command failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}

func (i *Installer) remoteCommand(plan Plan, command string) error {
	return i.run("ssh", sshArgs(plan, command), "")
}

func (i *Installer) copyRemote(plan Plan, src string, dst string) error {
	args := []string{"-q"}
	if plan.SSHKey != "" {
		args = append(args, "-i", plan.SSHKey)
	}
	args = append(args, src, plan.BootstrapUser+"@"+plan.TargetHost+":"+dst)
	return i.run("scp", args, "")
}

func (i *Installer) writeRemoteKeyFiles(plan Plan, keys keyPlan) (string, string, func(), error) {
	var paths []string
	writeKey := func(name string, key string) (string, error) {
		if key == "" {
			return "", nil
		}
		path := "/tmp/simple-vps-" + name + ".pub"
		cmd := "printf '%s\n' " + utils.ShellEscape(key) + " > " + utils.ShellEscape(path) + " && chmod 0600 " + utils.ShellEscape(path)
		if err := i.remoteCommand(plan, cmd); err != nil {
			return "", err
		}
		paths = append(paths, path)
		return path, nil
	}
	operatorPath, err := writeKey("operator", keys.Operator)
	if err != nil {
		return "", "", func() {}, err
	}
	deployPath, err := writeKey("deploy", keys.Deploy)
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() {
		for _, path := range paths {
			_ = i.remoteCommand(plan, "rm -f "+utils.ShellEscape(path))
		}
	}
	return operatorPath, deployPath, cleanup, nil
}

func sshArgs(plan Plan, command string) []string {
	args := []string{"-o", "BatchMode=yes"}
	if plan.SSHKey != "" {
		args = append(args, "-i", plan.SSHKey)
	}
	args = append(args, plan.BootstrapUser+"@"+plan.TargetHost, command)
	return args
}

func remoteLocalInstallCommand(binary string, plan Plan, operatorKeyFile string, deployKeyFile string) string {
	args := []string{
		binary,
		"host",
		"install",
		"--mode", "local",
		"--operator-user", plan.OperatorUser,
		"--deploy-user", plan.DeployUser,
		"--timezone", plan.Timezone,
		"--locale", plan.Locale,
	}
	if operatorKeyFile != "" {
		args = append(args, "--operator-ssh-public-key-file", operatorKeyFile)
	}
	if deployKeyFile != "" {
		args = append(args, "--deploy-ssh-public-key-file", deployKeyFile)
	}
	if plan.Tailscale {
		if plan.TailscaleAuthKey != "" {
			args = append(args, "--tailscale-auth-key", plan.TailscaleAuthKey)
		}
		if plan.TailscaleHostname != "" {
			args = append(args, "--tailscale-hostname", plan.TailscaleHostname)
		}
	} else {
		args = append(args, "--no-tailscale")
	}
	if plan.CloudflareTunnel {
		if plan.CloudflareAPIToken != "" {
			args = append(args, "--cloudflare-api-token", plan.CloudflareAPIToken)
		}
		if plan.CloudflareAccountID != "" {
			args = append(args, "--cloudflare-account-id", plan.CloudflareAccountID)
		}
		if plan.CloudflareTunnelToken != "" {
			args = append(args, "--cloudflare-tunnel-token", plan.CloudflareTunnelToken)
		}
		if plan.CloudflareTunnelConfig != "" {
			args = append(args, "--cloudflare-tunnel-config", plan.CloudflareTunnelConfig)
		}
	} else {
		args = append(args, "--no-cloudflare-tunnel")
	}
	if plan.InstallDocker {
		args = append(args, "--docker")
	}
	if !plan.InstallLitestream {
		args = append(args, "--no-litestream")
	}
	if plan.CheckMode {
		args = append(args, "--check")
	}

	escaped := make([]string, 0, len(args))
	for _, arg := range args {
		escaped = append(escaped, utils.ShellEscape(arg))
	}
	return strings.Join(escaped, " ")
}

type keyPlan struct {
	Operator string
	Deploy   string
}

func resolveSSHKeyPlan(plan Plan, requireOperator bool, rootKeysPath string) (keyPlan, error) {
	operatorKey, err := readPublicKeyFile(plan.OperatorSSHPublicKeyFile)
	if err != nil {
		return keyPlan{}, err
	}
	deployKey, err := readPublicKeyFile(plan.DeploySSHPublicKeyFile)
	if err != nil {
		return keyPlan{}, err
	}

	if deployKey == "" {
		if plan.SharedKey {
			deployKey = operatorKey
		} else {
			return keyPlan{}, errors.New("No SSH public key source found for deploy user.\nProvide --deploy-ssh-public-key-file, or pass --shared-key to reuse the operator key.")
		}
	}

	if requireOperator && operatorKey == "" && !nonEmptyFile(rootKeysPath) {
		return keyPlan{}, fmt.Errorf("No SSH public key source found for operator user.\nProvide --operator-ssh-public-key-file or --ssh-public-key-file, or create %s first.\nThis protects against locking yourself out when password auth is disabled.", rootKeysPath)
	}

	return keyPlan{Operator: operatorKey, Deploy: deployKey}, nil
}

func locateRepoRoot() (string, error) {
	var candidates []string
	if envDir := os.Getenv("SIMPLE_VPS_REPO_ROOT"); envDir != "" {
		candidates = append(candidates, envDir)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, exeDir)
		candidates = append(candidates, filepath.Join(exeDir, ".."))
	}

	for _, candidate := range candidates {
		if repoLooksValid(candidate) {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return "", err
			}
			return abs, nil
		}
	}
	return "", errors.New("Simple VPS Go module was not found; run from a checkout or set SIMPLE_VPS_REPO_ROOT")
}

func repoLooksValid(dir string) bool {
	return fileExists(filepath.Join(dir, "go.mod"))
}

func tailscaleAuthMode(enabled bool, authKey string) string {
	if !enabled {
		return "disabled"
	}
	if authKey != "" {
		return "auth-key"
	}
	return "manual"
}

func cloudflareServiceMode(opts Options) string {
	if !opts.CloudflareTunnel {
		return "disabled"
	}
	switch {
	case opts.CloudflareAPIToken != "":
		return "api"
	case opts.CloudflareTunnelToken != "":
		return "token"
	case opts.CloudflareTunnelConfig != "":
		return "config"
	default:
		return "manual"
	}
}

func helperBinariesExist(dir string) bool {
	return fileExists(filepath.Join(dir, "simple-vps-linux-amd64")) &&
		fileExists(filepath.Join(dir, "simple-vps-linux-arm64"))
}

func readPublicKeyFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("SSH public key file not found: %s", path)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "", fmt.Errorf("SSH public key file is empty: %s", path)
}

func runPassthrough(name string, args []string, cwd string) error {
	cmd := exec.Command(name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func environMap() map[string]string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func envDefault(env map[string]string, name string, fallback string) string {
	if value := env[name]; value != "" {
		return value
	}
	return fallback
}

func envBool(env map[string]string, name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(env[name]))
	if value == "" {
		return fallback
	}
	return value == "true" || value == "1" || value == "yes"
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func presentOrMissing(value string, present string, missing string) string {
	if value != "" {
		return present
	}
	return missing
}

func nonEmptyStrings(values ...string) []string {
	var out []string
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func (i *Installer) info(format string, args ...any) {
	fmt.Fprintf(i.Stdout, "==> "+format+"\n", args...)
}

func (i *Installer) step(format string, args ...any) {
	fmt.Fprintf(i.Stdout, "--> "+format+"\n", args...)
}

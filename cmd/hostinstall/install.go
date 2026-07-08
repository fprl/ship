package hostinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/provision"
	"github.com/fprl/ship/internal/provision/local"
	"github.com/fprl/ship/internal/utils"
)

type Options struct {
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
	Ingress                  string
	Admin                    string
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
	Ingress                  string
	Admin                    string
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
}

type Installer struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Env    map[string]string

	geteuid   func() int
	run       func(name string, args []string, cwd string) error
	look      func(file string) (string, error)
	remoteOut func(plan Plan, command string) (string, error)
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

	if envBool(i.Env, "SHIP_INSTALLER_DUMP_PLAN", false) {
		return i.dumpInstallPlan(plan)
	}

	i.info("ship installer starting")
	i.info("Mode: %s", plan.Mode)
	i.info("Operator user: %s", plan.OperatorUser)
	i.info("Deploy user: %s", plan.DeployUser)
	i.info("Timezone: %s", plan.Timezone)
	i.info("Ingress: %s", plan.Ingress)
	i.info("Admin: %s", plan.Admin)
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
		err = i.runRemote(plan)
	case "local":
		err = i.runLocal(plan)
	default:
		err = internalInstallError(fmt.Sprintf("invalid resolved install mode: %s", plan.Mode), boxInitCommand(plan.TargetHost))
	}
	if err != nil {
		return err
	}

	i.info("Provisioning complete")
	i.printNextSteps(plan)
	return nil
}

func DefaultOptions(env map[string]string) Options {
	if env == nil {
		env = environMap()
	}
	return Options{
		Mode:                     "auto",
		BootstrapUser:            "root",
		OperatorSSHPublicKeyFile: env["SHIP_OPERATOR_SSH_PUBLIC_KEY_FILE"],
		DeploySSHPublicKeyFile:   env["SHIP_DEPLOY_SSH_PUBLIC_KEY_FILE"],
		OperatorUser:             envDefault(env, "SHIP_OPERATOR_USER", "operator"),
		DeployUser:               envDefault(env, "SHIP_DEPLOY_USER", "deploy"),
		Timezone:                 envDefault(env, "SHIP_TIMEZONE", "UTC"),
		Locale:                   envDefault(env, "SHIP_LOCALE", "en_US.UTF-8"),
		Ingress:                  env["SHIP_INGRESS"],
		Admin:                    env["SHIP_ADMIN"],
		Tailscale:                false,
		TailscaleAuthKey:         env["SHIP_TAILSCALE_AUTH_KEY"],
		TailscaleHostname:        env["SHIP_TAILSCALE_HOSTNAME"],
		CloudflareTunnel:         false,
		CloudflareAPIToken:       env["SHIP_CLOUDFLARE_API_TOKEN"],
		CloudflareAccountID:      env["SHIP_CLOUDFLARE_ACCOUNT_ID"],
		CloudflareTunnelToken:    env["SHIP_CLOUDFLARE_TUNNEL_TOKEN"],
		CloudflareTunnelConfig:   env["SHIP_CLOUDFLARE_TUNNEL_CONFIG"],
		InstallDocker:            envBool(env, "SHIP_INSTALL_DOCKER", false),
		InstallLitestream:        envBool(env, "SHIP_INSTALL_LITESTREAM", false),
	}
}

func BuildPlan(opts Options, isRoot bool, osReleaseExists bool) (Plan, error) {
	var err error
	opts, err = applyInstallPresets(opts)
	if err != nil {
		return Plan{}, err
	}

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
		return Plan{}, installUsageError(fmt.Sprintf("invalid mode: %s (expected local, remote, or auto)", opts.Mode), boxInitCommand(opts.TargetHost, "--mode", "auto"))
	}

	if mode == "remote" {
		if opts.TargetHost == "" {
			return Plan{}, installUsageError("TARGET_HOST is required in remote mode", boxInitCommand("", "--mode", "remote"))
		}
		if opts.BootstrapUser == "" {
			return Plan{}, installUsageError("BOOTSTRAP_USER is required in remote mode", boxInitCommand(opts.TargetHost, "--bootstrap-user", "root"))
		}
		if opts.SSHKey != "" && !fileExists(opts.SSHKey) {
			return Plan{}, errcat.New(errcat.CodeSSHPrivateKeyMissing, errcat.Fields{
				"path":    opts.SSHKey,
				"command": boxInitCommand(opts.TargetHost, "--ssh-key", "<path-to-existing-private-key>"),
			})
		}
	}

	if opts.OperatorUser == opts.DeployUser {
		return Plan{}, installUsageError("Operator and deploy users must be different", boxInitCommand(opts.TargetHost, "--operator-user", "operator", "--deploy-user", "deploy"))
	}
	if !opts.Tailscale {
		if opts.TailscaleAuthKey != "" {
			return Plan{}, installUsageError("--tailscale-auth-key requires Tailscale to be enabled", boxInitCommand(opts.TargetHost, "--tailscale", "--tailscale-auth-key", "<key>"))
		}
		if opts.TailscaleHostname != "" {
			return Plan{}, installUsageError("--tailscale-hostname requires Tailscale to be enabled", boxInitCommand(opts.TargetHost, "--tailscale", "--tailscale-hostname", "<name>"))
		}
	}
	if !opts.CloudflareTunnel {
		if opts.CloudflareTunnelToken != "" {
			return Plan{}, installUsageError("--cloudflare-tunnel-token requires Cloudflare Tunnel to be enabled", boxInitCommand(opts.TargetHost, "--ingress", "cloudflare", "--cloudflare-tunnel-token", "<token>"))
		}
		if opts.CloudflareAPIToken != "" {
			return Plan{}, installUsageError("--cloudflare-api-token requires Cloudflare Tunnel to be enabled", boxInitCommand(opts.TargetHost, "--ingress", "cloudflare", "--cloudflare-api-token", "<token>"))
		}
		if opts.CloudflareAccountID != "" {
			return Plan{}, installUsageError("--cloudflare-account-id requires Cloudflare Tunnel to be enabled", boxInitCommand(opts.TargetHost, "--ingress", "cloudflare", "--cloudflare-account-id", "<account-id>"))
		}
		if opts.CloudflareTunnelConfig != "" {
			return Plan{}, installUsageError("--cloudflare-tunnel-config requires Cloudflare Tunnel to be enabled", boxInitCommand(opts.TargetHost, "--ingress", "cloudflare", "--cloudflare-tunnel-config", "<path>"))
		}
	}
	if opts.CloudflareAPIToken != "" && opts.CloudflareTunnelToken != "" {
		return Plan{}, installUsageError("use either --cloudflare-api-token or --cloudflare-tunnel-token, not both", boxInitCommand(opts.TargetHost, "--ingress", "cloudflare", "--cloudflare-api-token", "<token>"))
	}
	if opts.CloudflareAPIToken != "" && opts.CloudflareTunnelConfig != "" {
		return Plan{}, installUsageError("use either --cloudflare-api-token or --cloudflare-tunnel-config, not both", boxInitCommand(opts.TargetHost, "--ingress", "cloudflare", "--cloudflare-api-token", "<token>"))
	}
	if opts.CloudflareTunnelToken != "" && opts.CloudflareTunnelConfig != "" {
		return Plan{}, installUsageError("use either --cloudflare-tunnel-token or --cloudflare-tunnel-config, not both", boxInitCommand(opts.TargetHost, "--ingress", "cloudflare", "--cloudflare-tunnel-token", "<token>"))
	}

	operatorKeyFile := opts.OperatorSSHPublicKeyFile
	deployKeyFile := opts.DeploySSHPublicKeyFile
	if operatorKeyFile == "" && opts.SSHKey != "" && fileExists(opts.SSHKey+".pub") {
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
		Ingress:                  opts.Ingress,
		Admin:                    opts.Admin,
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
	}, nil
}

func applyInstallPresets(opts Options) (Options, error) {
	switch opts.Ingress {
	case "":
		if opts.CloudflareTunnel {
			opts.Ingress = "cloudflare"
		} else {
			opts.Ingress = "public"
		}
	case "public":
		opts.Ingress = "public"
		opts.CloudflareTunnel = false
	case "cloudflare":
		opts.CloudflareTunnel = true
	case "private":
		opts.CloudflareTunnel = false
	default:
		return Options{}, installUsageError(fmt.Sprintf("invalid ingress mode: %s (expected public, cloudflare, or private)", opts.Ingress), boxInitCommand("", "--ingress", "public"))
	}

	switch opts.Admin {
	case "":
		if opts.Tailscale {
			opts.Admin = "tailscale"
		} else {
			opts.Admin = "public-ssh"
		}
	case "public-ssh":
		opts.Admin = "public-ssh"
		opts.Tailscale = false
	case "tailscale":
		opts.Tailscale = true
	default:
		return Options{}, installUsageError(fmt.Sprintf("invalid admin mode: %s (expected public-ssh or tailscale)", opts.Admin), boxInitCommand("", "--admin", "public-ssh"))
	}
	return opts, nil
}

func (i *Installer) runRemote(plan Plan) error {
	i.info("Running in remote mode against %s", plan.TargetHost)
	if err := i.preflightSSH(plan); err != nil {
		return err
	}
	bootstrapKeys := ""
	var err error
	if planNeedsBootstrapAuthorizedKeys(plan) {
		bootstrapKeys, err = i.remoteBootstrapAuthorizedKeys(plan)
		if err != nil {
			return err
		}
	}
	keyPlan, err := resolveSSHKeyPlan(plan, true, bootstrapKeys, sshCopyIDTarget(plan))
	if err != nil {
		return err
	}

	arch, err := i.remoteArch(plan)
	if err != nil {
		return err
	}
	helper, cleanupHelper, err := i.prepareRemoteHelperBinary(plan, arch)
	if err != nil {
		return err
	}
	defer cleanupHelper()

	remoteHelper := "/tmp/ship-host-install"
	if err := i.copyRemote(plan, helper, remoteHelper); err != nil {
		return remoteInstallTransferError(plan, "copy helper binary to target", err)
	}
	chmodHelperCommand := "chmod 0755 " + utils.ShellEscape(remoteHelper)
	if err := i.remoteCommand(plan, chmodHelperCommand); err != nil {
		return remoteInstallCommandError(plan, "chmod helper binary on target", chmodHelperCommand, err)
	}
	operatorKeyFile, deployKeyFile, cleanupKeys, err := i.writeRemoteKeyFiles(plan, keyPlan)
	if err != nil {
		return err
	}
	defer cleanupKeys()

	cmd := remoteLocalInstallCommand(remoteHelper, plan, operatorKeyFile, deployKeyFile)
	i.step("Running Go provisioner on target")
	if plan.BootstrapUser == "root" {
		if err := i.remoteCommand(plan, cmd); err != nil {
			return hostInstallApplyError(plan, err)
		}
		i.printPromotedMembers(keyPlan)
		return nil
	}
	if err := i.remoteCommand(plan, "sudo -n "+cmd); err != nil {
		return hostInstallApplyError(plan, err)
	}
	i.printPromotedMembers(keyPlan)
	return nil
}

func (i *Installer) runLocal(plan Plan) error {
	if i.geteuid() != 0 {
		return errcat.New(errcat.CodeHostInstallRequiresRoot, errcat.Fields{
			"command": "sudo " + boxInitCommand("localhost", "--mode", "local"),
		})
	}
	bootstrapKeys, err := readOptionalFile("/root/.ssh/authorized_keys")
	if err != nil {
		return err
	}
	keyPlan, err := resolveSSHKeyPlan(plan, true, bootstrapKeys, sshCopyIDTarget(plan))
	if err != nil {
		return err
	}

	helperPath, err := os.Executable()
	if err != nil {
		return internalInstallError("resolve current executable failed: "+oneLineError(err), boxInitCommand("localhost", "--mode", "local"))
	}

	i.info("Running in local mode on localhost")
	summary, err := provision.RunInstall(context.Background(), local.Runner{}, provision.InstallOptions{
		OperatorUser:           plan.OperatorUser,
		DeployUser:             plan.DeployUser,
		OperatorSSHPublicKeys:  keyLines(keyPlan.Operator),
		DeploySSHPublicKeys:    keyLines(keyPlan.Deploy),
		Timezone:               plan.Timezone,
		Locale:                 plan.Locale,
		Ingress:                plan.Ingress,
		Admin:                  plan.Admin,
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
		return hostInstallApplyError(plan, err)
	}
	i.info("Apply %s changed %d operations", summary.ApplyID, summary.OperationsChanged)
	i.printPromotedMembers(keyPlan)
	return nil
}

func (i *Installer) dumpInstallPlan(plan Plan) error {
	requireOperatorKey := false
	bootstrapKeys := ""
	bootstrapSource := "remote bootstrap authorized_keys"
	if plan.Mode == "local" {
		requireOperatorKey = true
		bootstrapSource = "/root/.ssh/authorized_keys"
		var err error
		bootstrapKeys, err = readOptionalFile("/root/.ssh/authorized_keys")
		if err != nil {
			return err
		}
	}

	keyPlan, err := resolveSSHKeyPlan(plan, requireOperatorKey, bootstrapKeys, sshCopyIDTarget(plan))
	if err != nil && plan.Mode == "local" {
		return err
	}

	fmt.Fprintf(i.Stdout, "plan.mode=%s\n", plan.Mode)
	fmt.Fprintf(i.Stdout, "plan.target_host=%s\n", plan.TargetHost)
	fmt.Fprintf(i.Stdout, "plan.bootstrap_user=%s\n", plan.BootstrapUser)
	fmt.Fprintf(i.Stdout, "plan.operator_user=%s\n", plan.OperatorUser)
	fmt.Fprintf(i.Stdout, "plan.deploy_user=%s\n", plan.DeployUser)
	fmt.Fprintf(i.Stdout, "plan.ingress=%s\n", plan.Ingress)
	fmt.Fprintf(i.Stdout, "plan.admin=%s\n", plan.Admin)
	fmt.Fprintf(i.Stdout, "plan.tailscale=%s\n", boolText(plan.Tailscale))
	fmt.Fprintf(i.Stdout, "plan.tailscale_auth_mode=%s\n", plan.TailscaleAuthMode)
	fmt.Fprintf(i.Stdout, "plan.cloudflare_tunnel=%s\n", boolText(plan.CloudflareTunnel))
	fmt.Fprintf(i.Stdout, "plan.cloudflare_service_mode=%s\n", plan.CloudflareServiceMode)
	fmt.Fprintf(i.Stdout, "plan.docker=%s\n", boolText(plan.InstallDocker))
	fmt.Fprintf(i.Stdout, "plan.litestream=%s\n", boolText(plan.InstallLitestream))
	fmt.Fprintf(i.Stdout, "plan.check_mode=%s\n", boolText(plan.CheckMode))
	if err != nil {
		fmt.Fprintf(i.Stdout, "plan.operator_key=%s\n", keyPlanSource(plan.OperatorSSHPublicKeyFile, bootstrapSource))
		fmt.Fprintf(i.Stdout, "plan.deploy_key=%s\n", keyPlanSource(plan.DeploySSHPublicKeyFile, bootstrapSource))
	} else {
		fmt.Fprintf(i.Stdout, "plan.operator_key=%s\n", presentOrMissingKeys(keyPlan.Operator, "present", "missing"))
		fmt.Fprintf(i.Stdout, "plan.deploy_key=%s\n", presentOrMissingKeys(keyPlan.Deploy, "present", "missing"))
	}
	if plan.Mode == "remote" {
		fmt.Fprintln(i.Stdout, "--- remote-local-command ---")
		fmt.Fprintln(i.Stdout, remoteLocalInstallCommand("/tmp/ship-host-install", plan, "/tmp/ship-operator.pub", "/tmp/ship-deploy.pub"))
	}
	return nil
}

func (i *Installer) prepareGoHelperBinaries(repoRoot string, target string) (string, func(), error) {
	distDir := filepath.Join(repoRoot, "dist")
	if helperBinariesExist(distDir) {
		i.info("Using prebuilt ship Go helper binaries from %s", distDir)
		return distDir, func() {}, nil
	}

	if _, err := i.look("go"); err != nil {
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  "ship Go helper binaries are required, but no prebuilt dist/ binaries were found and Go is not installed",
			"command": "SHIP_HELPER_DIR=<dir-containing-ship-linux-amd64-and-ship-linux-arm64> " + boxInitCommand(target),
		})
	}

	if !fileExists(filepath.Join(repoRoot, "go.mod")) {
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  fmt.Sprintf("ship Go module not found at %s", repoRoot),
			"command": "SHIP_REPO_ROOT=<path-to-ship-checkout> " + boxInitCommand(target),
		})
	}

	outputDir, err := os.MkdirTemp("", "ship-helper-")
	if err != nil {
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  "create temporary helper build dir failed: " + oneLineError(err),
			"command": "TMPDIR=/tmp " + boxInitCommand(target),
		})
	}

	i.info("Building ship Go helper binaries")
	for _, arch := range []string{"amd64", "arm64"} {
		env := append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(outputDir, "ship-linux-"+arch), ".")
		cmd.Dir = repoRoot
		cmd.Env = env
		cmd.Stdout = i.Stdout
		cmd.Stderr = i.Stderr
		if err := cmd.Run(); err != nil {
			_ = os.RemoveAll(outputDir)
			return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
				"detail":  fmt.Sprintf("build Linux helper for %s failed: %s", arch, oneLineError(err)),
				"command": helperBuildCommand(arch),
			})
		}
	}
	return outputDir, func() { _ = os.RemoveAll(outputDir) }, nil
}

func (i *Installer) preflightSSH(plan Plan) error {
	output, err := i.remoteOutput(plan, "echo connected")
	if err != nil {
		detail := oneLineError(err)
		if publicKeyAuthFailure(detail) {
			return deployKeyMissingError(
				fmt.Sprintf("SSH public-key auth failed for %s; the provider gave a password; this installs your key using it once; ship's hardening then disables password login permanently", bootstrapSSHTarget(plan)),
				sshCopyIDTarget(plan),
			)
		}
		return errcat.New(errcat.CodeHostInstallSSHFailed, errcat.Fields{
			"detail":  fmt.Sprintf("SSH preflight failed for %s: %s", bootstrapSSHTarget(plan), detail),
			"command": sshCommand(plan, ""),
		})
	}
	if strings.TrimSpace(output) != "connected" {
		return errcat.New(errcat.CodeHostInstallSSHFailed, errcat.Fields{
			"detail":  fmt.Sprintf("SSH preflight expected connected sentinel from %s, got %q", bootstrapSSHTarget(plan), strings.TrimSpace(output)),
			"command": sshCommand(plan, "echo connected"),
		})
	}
	fmt.Fprintln(i.Stdout, "connected")
	return nil
}

func (i *Installer) remoteBootstrapAuthorizedKeys(plan Plan) (string, error) {
	command := "if test -f ~/.ssh/authorized_keys; then cat ~/.ssh/authorized_keys; fi"
	out, err := i.remoteOutput(plan, command)
	if err != nil {
		return "", remoteInstallCommandError(plan, "read bootstrap authorized_keys on target", command, err)
	}
	return out, nil
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
		return "", errcat.New(errcat.CodeUnsupportedTargetArchitecture, errcat.Fields{
			"arch": strings.TrimSpace(output),
		})
	}
}

func (i *Installer) remoteOutput(plan Plan, command string) (string, error) {
	if i.remoteOut != nil {
		return i.remoteOut(plan, command)
	}
	args := sshArgs(plan, command)
	cmd := exec.Command("ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = oneLineError(err)
		}
		return "", errcat.New(errcat.CodeHostInstallSSHFailed, errcat.Fields{
			"detail":  fmt.Sprintf("SSH command failed for %s: %s", bootstrapSSHTarget(plan), oneLine(detail)),
			"command": sshCommand(plan, command),
		})
	}
	return stdout.String(), nil
}

func (i *Installer) remoteCommand(plan Plan, command string) error {
	return i.run("ssh", sshArgs(plan, command), "")
}

func (i *Installer) copyRemote(plan Plan, src string, dst string) error {
	args := []string{
		"-q",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if plan.SSHKey != "" {
		args = append(args, "-i", plan.SSHKey)
	}
	args = append(args, src, plan.BootstrapUser+"@"+plan.TargetHost+":"+dst)
	return i.run("scp", args, "")
}

func (i *Installer) writeRemoteKeyFiles(plan Plan, keys keyPlan) (string, string, func(), error) {
	var paths []string
	writeKey := func(name string, keys []plannedKey) (string, error) {
		lines := keyLines(keys)
		if len(lines) == 0 {
			return "", nil
		}
		path := "/tmp/ship-" + name + ".pub"
		content := strings.Join(lines, "\n") + "\n"
		cmd := "printf %s " + utils.ShellEscape(content) + " > " + utils.ShellEscape(path) + " && chmod 0600 " + utils.ShellEscape(path)
		if err := i.remoteCommand(plan, cmd); err != nil {
			return "", remoteInstallCommandError(plan, "write "+name+" SSH public key on target", cmd, err)
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
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if plan.SSHKey != "" {
		args = append(args, "-i", plan.SSHKey)
	}
	args = append(args, plan.BootstrapUser+"@"+plan.TargetHost, command)
	return args
}

func remoteLocalInstallCommand(binary string, plan Plan, operatorKeyFile string, deployKeyFile string) string {
	args := []string{
		binary,
		"box",
		"init",
		"localhost",
		"--mode", "local",
		"--operator-user", plan.OperatorUser,
		"--deploy-user", plan.DeployUser,
		"--timezone", plan.Timezone,
		"--locale", plan.Locale,
		"--ingress", plan.Ingress,
		"--admin", plan.Admin,
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
	if plan.InstallLitestream {
		args = append(args, "--litestream")
	} else {
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
	Operator []plannedKey
	Deploy   []plannedKey
}

type plannedKey struct {
	Line        string
	Type        string
	Body        string
	Comment     string
	Fingerprint string
	Promoted    bool
}

func resolveSSHKeyPlan(plan Plan, requireOperator bool, bootstrapAuthorizedKeys string, passwordTarget string) (keyPlan, error) {
	operatorKeys, err := readPublicKeyFile(plan.OperatorSSHPublicKeyFile)
	if err != nil {
		return keyPlan{}, err
	}
	deployKeys, err := readPublicKeyFile(plan.DeploySSHPublicKeyFile)
	if err != nil {
		return keyPlan{}, err
	}

	var bootstrapKeys []plannedKey
	if len(operatorKeys) == 0 || len(deployKeys) == 0 {
		bootstrapKeys = parseBootstrapAuthorizedKeys(bootstrapAuthorizedKeys)
	}

	if len(deployKeys) == 0 {
		if len(bootstrapKeys) == 0 {
			return keyPlan{}, deployKeyMissingError(
				"bootstrap authorized_keys is empty; the provider gave a password; this installs your key using it once; ship's hardening then disables password login permanently",
				passwordTarget,
			)
		}
		deployKeys = markPromoted(bootstrapKeys, true)
	}

	if len(operatorKeys) == 0 && len(bootstrapKeys) > 0 {
		operatorKeys = markPromoted(bootstrapKeys, false)
	}

	if requireOperator && len(operatorKeys) == 0 {
		return keyPlan{}, errcat.New(errcat.CodeOperatorKeyMissing, errcat.Fields{
			"command": "ssh-copy-id " + passwordTarget,
		})
	}

	return keyPlan{Operator: operatorKeys, Deploy: deployKeys}, nil
}

func locateRepoRoot() (string, error) {
	var candidates []string
	if envDir := os.Getenv("SHIP_REPO_ROOT"); envDir != "" {
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
				return "", errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
					"detail":  "resolve ship repo root failed: " + oneLineError(err),
					"command": "SHIP_REPO_ROOT=<path-to-ship-checkout> ship box init <ssh-target>",
				})
			}
			return abs, nil
		}
	}
	return "", errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
		"detail":  "ship Go module was not found",
		"command": "SHIP_REPO_ROOT=<path-to-ship-checkout> ship box init <ssh-target>",
	})
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
	return fileExists(filepath.Join(dir, "ship-linux-amd64")) &&
		fileExists(filepath.Join(dir, "ship-linux-arm64"))
}

func readPublicKeyFile(path string) ([]plannedKey, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errcat.New(errcat.CodeSSHPublicKeyFileMissing, errcat.Fields{
			"path":    path,
			"command": keygenCommand(privateKeyPathForPublic(path)),
		})
	}
	keys, err := normalizePublicKeys(string(data), false)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errcat.New(errcat.CodeSSHPublicKeyFileEmpty, errcat.Fields{
			"path":    path,
			"command": publicKeyFromPrivateCommand(path),
		})
	}
	return keys, nil
}

func parseBootstrapAuthorizedKeys(raw string) []plannedKey {
	keys, _ := normalizePublicKeys(raw, true)
	return markPromoted(keys, true)
}

func normalizePublicKeys(raw string, skipInvalid bool) ([]plannedKey, error) {
	var keys []plannedKey
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, err := normalizePublicKeyLine(line)
		if err != nil {
			if skipInvalid {
				continue
			}
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func normalizePublicKeyLine(line string) (plannedKey, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return plannedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "public key line must contain key type and key body"})
	}
	if !supportedPublicKeyType(fields[0]) {
		return plannedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": fmt.Sprintf("unsupported public key type %q", fields[0])})
	}
	if fields[1] == "" {
		return plannedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "public key body is empty"})
	}
	fingerprint, err := publicKeyFingerprint(fields[1])
	if err != nil {
		return plannedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": err.Error()})
	}
	comment := ""
	if len(fields) > 2 {
		comment = strings.Join(fields[2:], " ")
	}
	comment = strings.Join(strings.Fields(comment), " ")
	if comment == "" {
		comment = "ship-member"
	}
	return plannedKey{
		Line:        fields[0] + " " + fields[1] + " " + comment,
		Type:        fields[0],
		Body:        fields[1],
		Comment:     comment,
		Fingerprint: fingerprint,
	}, nil
}

func supportedPublicKeyType(value string) bool {
	switch value {
	case "ssh-ed25519", "ssh-rsa",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	default:
		return false
	}
}

func publicKeyFingerprint(body string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("public key body is not valid base64")
	}
	if len(blob) == 0 {
		return "", fmt.Errorf("public key body is empty")
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

func markPromoted(keys []plannedKey, promoted bool) []plannedKey {
	out := make([]plannedKey, len(keys))
	for index, key := range keys {
		key.Promoted = promoted
		out[index] = key
	}
	return out
}

func keyLines(keys []plannedKey) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key.Line) != "" {
			out = append(out, key.Line)
		}
	}
	return out
}

func (i *Installer) printPromotedMembers(keys keyPlan) {
	for _, key := range keys.Deploy {
		if !key.Promoted {
			continue
		}
		fmt.Fprintf(i.Stdout, "member added: %s (%s %s, from root's authorized keys)\n", key.Comment, key.Type, key.Fingerprint)
	}
}

func deployKeyMissingError(detail string, passwordTarget string) error {
	if strings.TrimSpace(passwordTarget) == "" {
		passwordTarget = "root@<ip>"
	}
	return errcat.New(errcat.CodeDeployKeyMissing, errcat.Fields{
		"detail":  detail,
		"command": "ssh-copy-id " + passwordTarget,
	})
}

func publicKeyAuthFailure(detail string) bool {
	detail = strings.ToLower(detail)
	return strings.Contains(detail, "permission denied") && strings.Contains(detail, "publickey")
}

func sshCopyIDTarget(plan Plan) string {
	host := hostOnly(plan.TargetHost)
	if host == "" || host == "localhost" {
		host = "localhost"
	}
	return "root@" + host
}

func hostOnly(target string) string {
	target = strings.TrimSpace(target)
	if at := strings.LastIndex(target, "@"); at >= 0 && at < len(target)-1 {
		return target[at+1:]
	}
	return target
}

func planNeedsBootstrapAuthorizedKeys(plan Plan) bool {
	return plan.OperatorSSHPublicKeyFile == "" || plan.DeploySSHPublicKeyFile == ""
}

func keyPlanSource(path string, bootstrapSource string) string {
	if path != "" {
		return "file:" + path
	}
	return bootstrapSource
}

func presentOrMissingKeys(value []plannedKey, present string, missing string) string {
	if len(value) != 0 {
		return present
	}
	return missing
}

func readOptionalFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  "read " + path + ": " + oneLineError(err),
		"command": "sudo " + boxInitCommand("localhost", "--mode", "local"),
	})
}

func runPassthrough(name string, args []string, cwd string) error {
	cmd := exec.Command(name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stdin = os.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		return &utils.CommandError{
			Name:   name,
			Args:   append([]string(nil), args...),
			Stdout: stdout.String(),
			Stderr: stderr.String(),
			Err:    err,
		}
	}
	return nil
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

func installUsageError(detail, command string) error {
	return errcat.New(errcat.CodeUsageError, errcat.Fields{
		"detail":  detail,
		"command": command,
	})
}

func internalInstallError(detail string, command string) error {
	return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  detail,
		"command": command,
	})
}

func remoteInstallTransferError(plan Plan, action string, err error) error {
	if coded, ok := errcat.As(err); ok {
		return coded
	}
	return errcat.New(errcat.CodeHostInstallSSHFailed, errcat.Fields{
		"detail":  action + " failed for " + bootstrapSSHTarget(plan) + ": " + oneLineError(err),
		"command": boxInitCommand(plan.TargetHost, "--mode", "remote"),
	})
}

func remoteInstallCommandError(plan Plan, action string, command string, err error) error {
	if coded, ok := errcat.As(err); ok {
		return coded
	}
	if permissionFailure(err) {
		return errcat.New(errcat.CodeHostInstallPermissionDenied, errcat.Fields{
			"detail":  action + " failed for " + bootstrapSSHTarget(plan) + ": " + oneLineError(err),
			"command": hostInstallPermissionCommand(plan),
		})
	}
	return errcat.New(errcat.CodeHostInstallSSHFailed, errcat.Fields{
		"detail":  action + " failed for " + bootstrapSSHTarget(plan) + ": " + oneLineError(err),
		"command": sshCommand(plan, command),
	})
}

func hostInstallApplyError(plan Plan, err error) error {
	if coded, ok := errcat.As(err); ok {
		return coded
	}
	if tool, ok := missingExecutable(err); ok {
		if tool == "apt-get" || tool == "dpkg-query" {
			return errcat.New(errcat.CodeHostInstallUnsupportedOS, errcat.Fields{"tool": tool})
		}
		return errcat.New(errcat.CodeHostInstallMissingTool, errcat.Fields{"tool": tool})
	}
	if permissionFailure(err) {
		return errcat.New(errcat.CodeHostInstallPermissionDenied, errcat.Fields{
			"detail":  oneLineError(err),
			"command": hostInstallPermissionCommand(plan),
		})
	}
	return errcat.New(errcat.CodeHostInstallApplyFailed, errcat.Fields{
		"detail":  oneLineError(err),
		"command": hostInstallRetryCommand(plan),
	})
}

func missingExecutable(err error) (string, bool) {
	var execErr *exec.Error
	if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
		return execErr.Name, true
	}
	detail := oneLineError(err)
	if strings.Contains(detail, "executable file not found") {
		start := strings.Index(detail, `"`)
		if start >= 0 {
			rest := detail[start+1:]
			end := strings.Index(rest, `"`)
			if end > 0 {
				return rest[:end], true
			}
		}
	}
	for _, tool := range []string{"apt-get", "dpkg-query", "systemctl", "visudo", "curl", "gpg", "ufw", "podman"} {
		if strings.Contains(detail, tool) && strings.Contains(strings.ToLower(detail), "not found") {
			return tool, true
		}
	}
	return "", false
}

func permissionFailure(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) || errors.Is(err, os.ErrPermission) {
		return true
	}
	detail := strings.ToLower(oneLineError(err))
	return strings.Contains(detail, "permission denied") ||
		strings.Contains(detail, "operation not permitted") ||
		strings.Contains(detail, "sudo: a password is required") ||
		strings.Contains(detail, "sudo: a terminal is required")
}

func hostInstallPermissionCommand(plan Plan) string {
	if plan.Mode == "local" || plan.TargetHost == "" || plan.TargetHost == "localhost" {
		return "sudo " + boxInitCommand("localhost", "--mode", "local")
	}
	return boxInitCommand(plan.TargetHost, "--mode", "remote", "--bootstrap-user", "root")
}

func hostInstallRetryCommand(plan Plan) string {
	if plan.Mode == "local" || plan.TargetHost == "" || plan.TargetHost == "localhost" {
		return "sudo " + boxInitCommand("localhost", "--mode", "local")
	}
	return boxInitCommand(plan.TargetHost, "--mode", "remote")
}

func boxInitCommand(target string, extra ...string) string {
	args := []string{"ship", "box", "init", targetOrPlaceholder(target)}
	args = append(args, extra...)
	return shellCommand(args)
}

func sshCommand(plan Plan, command string) string {
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"}
	if plan.SSHKey != "" {
		args = append(args, "-i", plan.SSHKey)
	}
	args = append(args, bootstrapSSHTarget(plan))
	if command != "" {
		args = append(args, command)
	}
	return shellCommand(args)
}

func bootstrapSSHTarget(plan Plan) string {
	target := targetOrPlaceholder(plan.TargetHost)
	if strings.Contains(target, "@") || plan.BootstrapUser == "" {
		return target
	}
	return plan.BootstrapUser + "@" + target
}

func targetOrPlaceholder(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return "<ssh-target>"
	}
	return target
}

func shellCommand(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellArg(arg))
	}
	return strings.Join(parts, " ")
}

func shellArg(arg string) string {
	if isPlaceholder(arg) {
		return arg
	}
	return utils.ShellEscape(arg)
}

func isPlaceholder(arg string) bool {
	return strings.HasPrefix(arg, "<") && strings.HasSuffix(arg, ">")
}

func keygenCommand(path string) string {
	return "ssh-keygen -q -t ed25519 -N '' -f " + shellArg(path)
}

func privateKeyPathForPublic(path string) string {
	if strings.HasSuffix(path, ".pub") {
		return strings.TrimSuffix(path, ".pub")
	}
	return path
}

func publicKeyFromPrivateCommand(path string) string {
	return "ssh-keygen -y -f " + shellArg(privateKeyPathForPublic(path)) + " > " + shellArg(path)
}

func helperBuildCommand(arch string) string {
	arch = oneLine(arch)
	return "CGO_ENABLED=0 GOOS=linux GOARCH=" + shellArg(arch) + " go build -trimpath -ldflags='-s -w' -o " + shellArg(filepath.Join("dist", "ship-linux-"+arch)) + " ."
}

func helperDownloadCommand(target string, baseURL string, token string) string {
	if baseURL != defaultReleaseBaseURL {
		return "SHIP_RELEASE_BASE_URL=" + shellArg(defaultReleaseBaseURL) + " " + boxInitCommand(target)
	}
	if token == "" {
		return "SHIP_RELEASE_TOKEN=<token> " + boxInitCommand(target)
	}
	return "SHIP_REPO_ROOT=<path-to-ship-checkout> " + boxInitCommand(target)
}

func helperDownloadError(detail string, command string) error {
	return errcat.New(errcat.CodeHostHelperDownloadFailed, errcat.Fields{
		"detail":  oneLine(detail),
		"command": command,
	})
}

func oneLineError(err error) string {
	if err == nil {
		return "no error detail"
	}
	var commandErr *utils.CommandError
	if errors.As(err, &commandErr) {
		if output := oneLine(commandErr.CombinedOutput()); output != "no error detail" {
			return output
		}
	}
	return oneLine(err.Error())
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "no error detail"
	}
	return value
}

func (i *Installer) printNextSteps(plan Plan) {
	if plan.Mode != "remote" {
		return
	}
	server := plan.DeployUser + "@" + plan.TargetHost
	fmt.Fprintln(i.Stdout, "Next:")
	step := 1
	if privateKey := deployPrivateKeyHint(plan); privateKey != "" {
		fmt.Fprintf(i.Stdout, "%d. export SHIP_SSH_KEY=\"$(cat %s)\"\n", step, utils.ShellEscape(privateKey))
		step++
	}
	fmt.Fprintf(i.Stdout, "%d. ship box doctor %s\n", step, server)
	step++
	fmt.Fprintf(i.Stdout, "%d. ship init --box %s --host <app-domain>\n", step, server)
}

func deployPrivateKeyHint(plan Plan) string {
	pub := plan.DeploySSHPublicKeyFile
	if strings.HasSuffix(pub, ".pub") {
		return strings.TrimSuffix(pub, ".pub")
	}
	return ""
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

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
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/knownhosts"
	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/provision"
	"github.com/fprl/ship/internal/provision/local"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
)

type Options struct {
	Mode                     string
	TargetHost               string
	ClientAddress            string
	BootstrapUser            string
	BootstrapUserExplicit    bool
	SSHKey                   string
	BootstrapIdentityKey     string
	OperatorSSHPublicKeyFile string
	DeploySSHPublicKeyFile   string
	DeployKeyIsShipIdentity  bool
	CheckMode                bool
	NarrateSetup             bool
}

type Plan struct {
	Mode                     string
	TargetHost               string
	ClientAddress            string
	BootstrapUser            string
	KnownHostsFile           string
	SSHKey                   string
	BootstrapIdentityKey     string
	OperatorSSHPublicKeyFile string
	DeploySSHPublicKeyFile   string
	DeployKeyIsShipIdentity  bool
	CheckMode                bool
	NarrateSetup             bool
}

type Installer struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Env    map[string]string

	geteuid    func() int
	look       func(file string) (string, error)
	remoteOut  func(plan Plan, command string) (string, error)
	remoteRun  func(plan Plan, command string, stdin []byte) error
	remoteCopy func(plan Plan, src string, dst string) error
	sleep      func(time.Duration)
}

const passwordProvisionedDetail = "provider gave a password; this installs your ship key using it once; hardening then disables password login permanently"

const (
	remoteHelperPattern = "/tmp/ship-host-install.XXXXXX"
	remoteHelperExample = "/tmp/ship-host-install.example"
)

func NewInstaller() *Installer {
	return &Installer{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
		Env:    environMap(),
		geteuid: func() int {
			return os.Geteuid()
		},
		look:  exec.LookPath,
		sleep: time.Sleep,
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

	cleanupKnownHosts := func() {}
	if plan.Mode == "remote" {
		knownHostsFile, cleanup, err := knownhosts.TempFile()
		if err != nil {
			return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
				"detail":  "create setup known_hosts failed: " + oneLineError(err),
				"command": "TMPDIR=/tmp ship box setup <ssh-target>",
			})
		}
		plan.KnownHostsFile = knownHostsFile
		cleanupKnownHosts = cleanup
	}
	defer cleanupKnownHosts()

	keyPlan, err := resolveSSHKeyPlan(plan, true, "", sshCopyIDTarget(plan))
	if err != nil {
		return err
	}

	var summary provision.InstallSummary
	switch plan.Mode {
	case "remote":
		summary, err = i.runRemote(plan, keyPlan)
	case "local":
		summary, err = i.runLocal(plan, keyPlan)
	default:
		err = internalInstallError(fmt.Sprintf("invalid resolved install mode: %s", plan.Mode), boxSetupCommand(plan.TargetHost))
	}
	if err != nil {
		return err
	}

	if !plan.CheckMode {
		i.printMemberEnrollment(summary.DeployKeyResults)
	}
	if !plan.CheckMode && plan.Mode == "remote" {
		if err := i.pinKnownHost(plan); err != nil {
			return err
		}
	}
	if plan.NarrateSetup {
		i.printBoxReady()
		i.printNextSteps(plan)
	}
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
		NarrateSetup:             true,
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
		return Plan{}, installUsageError(fmt.Sprintf("invalid mode: %s (expected local, remote, or auto)", opts.Mode), boxSetupCommand(opts.TargetHost, "--mode", "auto"))
	}

	if mode == "remote" {
		if opts.TargetHost == "" {
			return Plan{}, installUsageError("TARGET_HOST is required in remote mode", boxSetupCommand("", "--mode", "remote"))
		}
		targetHost, bootstrapUser, err := parseBoxSetupTarget(opts.TargetHost, opts.BootstrapUser, opts.BootstrapUserExplicit)
		if err != nil {
			return Plan{}, err
		}
		opts.TargetHost = targetHost
		opts.ClientAddress = targetHost
		opts.BootstrapUser = bootstrapUser
		if opts.BootstrapUser == "" {
			return Plan{}, installUsageError("BOOTSTRAP_USER is required in remote mode", boxSetupCommand(opts.TargetHost, "--bootstrap-user", "root"))
		}
		if opts.SSHKey != "" && !fileExists(opts.SSHKey) {
			return Plan{}, errcat.New(errcat.CodeSSHPrivateKeyMissing, errcat.Fields{
				"path":    opts.SSHKey,
				"command": boxSetupCommand(opts.TargetHost, "--ssh-key", "<path-to-existing-private-key>"),
			})
		}
	}
	if opts.ClientAddress == "" {
		opts.ClientAddress = opts.TargetHost
	}

	operatorKeyFile := opts.OperatorSSHPublicKeyFile
	deployKeyFile := opts.DeploySSHPublicKeyFile
	if operatorKeyFile == "" && opts.SSHKey != "" && fileExists(opts.SSHKey+".pub") {
		operatorKeyFile = opts.SSHKey + ".pub"
	}

	return Plan{
		Mode:                     mode,
		TargetHost:               opts.TargetHost,
		ClientAddress:            opts.ClientAddress,
		BootstrapUser:            opts.BootstrapUser,
		SSHKey:                   opts.SSHKey,
		BootstrapIdentityKey:     opts.BootstrapIdentityKey,
		OperatorSSHPublicKeyFile: operatorKeyFile,
		DeploySSHPublicKeyFile:   deployKeyFile,
		DeployKeyIsShipIdentity:  opts.DeployKeyIsShipIdentity,
		CheckMode:                opts.CheckMode,
		NarrateSetup:             opts.NarrateSetup,
	}, nil
}

func parseBoxSetupTarget(rawTarget string, bootstrapUser string, bootstrapUserExplicit bool) (string, string, error) {
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		return "", "", installUsageError("TARGET_HOST is required in remote mode", boxSetupCommand("", "--mode", "remote"))
	}
	if strings.HasPrefix(target, "-") {
		return "", "", invalidBoxSetupTarget(target)
	}
	if strings.Count(target, "@") > 1 {
		return "", "", invalidBoxSetupTarget(target)
	}

	targetUser := ""
	host := target
	if user, rest, ok := strings.Cut(target, "@"); ok {
		targetUser = strings.TrimSpace(user)
		host = strings.TrimSpace(rest)
		if targetUser == "" || host == "" {
			return "", "", invalidBoxSetupTarget(target)
		}
		if !config.SystemUserRe.MatchString(targetUser) {
			return "", "", installUsageError(
				fmt.Sprintf("invalid bootstrap user %q in ssh target %q", targetUser, target),
				boxSetupCommand("<ssh-target>"),
			)
		}
	}

	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return "", "", installUsageError(
			"IPv6 bracket SSH targets are not supported; use a DNS name or IPv4 host",
			boxSetupCommand("<host-or-user@host>"),
		)
	}
	if !config.ValidateHost(host) {
		return "", "", invalidBoxSetupTarget(target)
	}

	user := strings.TrimSpace(bootstrapUser)
	if targetUser != "" {
		if bootstrapUserExplicit && user != "" && user != targetUser {
			return "", "", installUsageError(
				fmt.Sprintf("ssh target specifies bootstrap user %q but --bootstrap-user is %q", targetUser, user),
				boxSetupCommand(target),
			)
		}
		user = targetUser
	}
	return host, user, nil
}

func invalidBoxSetupTarget(target string) error {
	return installUsageError(
		fmt.Sprintf("invalid ssh target %q: expected host or user@host with a DNS name or IPv4 host", target),
		boxSetupCommand("<host-or-user@host>"),
	)
}

func (i *Installer) runRemote(plan Plan, keyPlan keyPlan) (provision.InstallSummary, error) {
	if err := i.preflightSSH(plan); err != nil {
		return provision.InstallSummary{}, err
	}
	if plan.NarrateSetup {
		i.printSetupNarration(plan)
	}
	if err := i.ensureRemoteBootstrapAuthorizedKeys(plan); err != nil {
		return provision.InstallSummary{}, err
	}

	arch, err := i.remoteArch(plan)
	if err != nil {
		return provision.InstallSummary{}, err
	}
	helper, cleanupHelper, err := i.prepareRemoteHelperBinary(plan, arch)
	if err != nil {
		return provision.InstallSummary{}, err
	}
	defer cleanupHelper()

	remoteHelper, cleanupRemoteHelper, err := i.createRemoteHelperPath(plan)
	if err != nil {
		return provision.InstallSummary{}, err
	}
	defer cleanupRemoteHelper()
	if err := i.copyRemote(plan, helper, remoteHelper); err != nil {
		return provision.InstallSummary{}, remoteInstallTransferError(plan, "copy helper binary to target", err)
	}
	chmodHelperCommand := "chmod 0755 " + utils.ShellEscape(remoteHelper)
	if err := i.remoteCommand(plan, chmodHelperCommand); err != nil {
		return provision.InstallSummary{}, remoteInstallCommandError(plan, "chmod helper binary on target", chmodHelperCommand, err)
	}
	operatorKeyFile, deployKeyFile, cleanupKeys, err := i.writeRemoteKeyFiles(plan, keyPlan)
	if err != nil {
		return provision.InstallSummary{}, err
	}
	defer cleanupKeys()
	cmd := remoteLocalInstallCommand(remoteHelper, plan, operatorKeyFile, deployKeyFile)
	cmd = cleanupRemoteHelperCommand(cmd, remoteHelper)
	i.step("running provisioner on target")
	if plan.BootstrapUser == "root" {
		if err := i.remoteCommand(plan, cmd); err != nil {
			return provision.InstallSummary{}, hostInstallApplyError(plan, err)
		}
		return provision.InstallSummary{}, i.waitForRemoteSSH(plan)
	}
	if err := i.remoteCommand(plan, "sudo -n "+cmd); err != nil {
		return provision.InstallSummary{}, hostInstallApplyError(plan, err)
	}
	return provision.InstallSummary{}, i.waitForRemoteSSH(plan)
}

func (i *Installer) runLocal(plan Plan, keyPlan keyPlan) (provision.InstallSummary, error) {
	if i.geteuid() != 0 {
		return provision.InstallSummary{}, errcat.New(errcat.CodeHostInstallRequiresRoot, errcat.Fields{
			"command": "sudo " + boxSetupCommand("localhost", "--mode", "local"),
		})
	}

	helperPath, err := os.Executable()
	if err != nil {
		return provision.InstallSummary{}, internalInstallError("resolve current executable failed: "+oneLineError(err), boxSetupCommand("localhost", "--mode", "local"))
	}

	if plan.NarrateSetup {
		i.printSetupNarration(plan)
	}
	summary, err := provision.RunInstall(context.Background(), local.Runner{}, provision.InstallOptions{
		OperatorSSHPublicKeys: keyLines(keyPlan.Operator),
		DeploySSHPublicKeys:   keyLines(keyPlan.Deploy),
		ClientAddress:         plan.ClientAddress,
		CheckMode:             plan.CheckMode,
		HelperBinaryPath:      helperPath,
	})
	if err != nil {
		return provision.InstallSummary{}, hostInstallApplyError(plan, err)
	}
	i.info("provisioning %s %d changes", summary.ApplyID, summary.OperationsChanged)
	return summary, nil
}

func (i *Installer) dumpInstallPlan(plan Plan) error {
	keyPlan, err := resolveSSHKeyPlan(plan, true, "", sshCopyIDTarget(plan))
	if err != nil {
		return err
	}

	fmt.Fprintf(i.Stdout, "plan.mode=%s\n", plan.Mode)
	fmt.Fprintf(i.Stdout, "plan.target_host=%s\n", plan.TargetHost)
	fmt.Fprintf(i.Stdout, "plan.bootstrap_user=%s\n", plan.BootstrapUser)
	fmt.Fprintf(i.Stdout, "plan.check_mode=%s\n", boolText(plan.CheckMode))
	fmt.Fprintf(i.Stdout, "plan.operator_key=%s\n", presentOrMissingKeys(keyPlan.Operator, "present", "missing"))
	fmt.Fprintf(i.Stdout, "plan.deploy_key=%s\n", presentOrMissingKeys(keyPlan.Deploy, "present", "missing"))
	if plan.Mode == "remote" {
		fmt.Fprintln(i.Stdout, "--- remote-local-command ---")
		fmt.Fprintln(i.Stdout, remoteLocalInstallCommand(remoteHelperExample, plan, "/tmp/ship-operator.pub", "/tmp/ship-deploy.pub"))
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
			"command": "SHIP_HELPER_DIR=<dir-containing-ship-linux-amd64-and-ship-linux-arm64> " + boxSetupCommand(target),
		})
	}

	if !fileExists(filepath.Join(repoRoot, "go.mod")) {
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  fmt.Sprintf("ship Go module not found at %s", repoRoot),
			"command": "SHIP_REPO_ROOT=<path-to-ship-checkout> " + boxSetupCommand(target),
		})
	}

	outputDir, err := os.MkdirTemp("", "ship-helper-")
	if err != nil {
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  "create temporary helper build dir failed: " + oneLineError(err),
			"command": "TMPDIR=/tmp " + boxSetupCommand(target),
		})
	}

	i.info("Building ship Go helper binaries")
	for _, arch := range []string{"amd64", "arm64"} {
		env := append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		// Stamp the running client's version so the pushed helper is
		// client-matched; an unstamped helper reports "dev" and defeats
		// the §16 skew comparison on every box it lands on.
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -X github.com/fprl/ship/internal/version.Version="+version.Version, "-o", filepath.Join(outputDir, "ship-linux-"+arch), ".")
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
				passwordProvisionedDetail,
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
	fmt.Fprintf(i.Stderr, "connected as %s (bootstrap)\n", plan.BootstrapUser)
	return nil
}

func (i *Installer) waitForRemoteSSH(plan Plan) error {
	var last error
	for attempt := 0; attempt < 30; attempt++ {
		_, err := i.remoteOutput(plan, "true")
		if err == nil {
			return nil
		}
		last = err
		if !transientSSHReconnectError(oneLineError(err)) {
			break
		}
		if attempt < 29 {
			if i.sleep != nil {
				i.sleep(500 * time.Millisecond)
			} else {
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
	return errcat.New(errcat.CodeHostInstallSSHFailed, errcat.Fields{
		"detail":  fmt.Sprintf("SSH did not become ready after provisioning for %s: %s", bootstrapSSHTarget(plan), oneLineError(last)),
		"command": boxSetupCommand(plan.TargetHost),
	})
}

func transientSSHReconnectError(detail string) bool {
	detail = strings.ToLower(detail)
	for _, pattern := range []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"operation timed out",
		"connection closed",
		"closed by remote host",
		"broken pipe",
		"i/o timeout",
	} {
		if strings.Contains(detail, pattern) {
			return true
		}
	}
	return false
}

func (i *Installer) remoteBootstrapAuthorizedKeys(plan Plan) (string, error) {
	command := "if test -f ~/.ssh/authorized_keys; then cat ~/.ssh/authorized_keys; fi"
	out, err := i.remoteOutput(plan, command)
	if err != nil {
		return "", remoteInstallCommandError(plan, "read bootstrap authorized_keys on target", command, err)
	}
	return out, nil
}

func (i *Installer) ensureRemoteBootstrapAuthorizedKeys(plan Plan) error {
	out, err := i.remoteBootstrapAuthorizedKeys(plan)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return deployKeyMissingError(
			passwordProvisionedDetail,
			sshCopyIDTarget(plan),
		)
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
	return i.remoteCommandInput(plan, command, nil)
}

func (i *Installer) remoteCommandInput(plan Plan, command string, stdin []byte) error {
	if i.remoteRun != nil {
		return i.remoteRun(plan, command, stdin)
	}
	stdout, stderr, err := runCapturedInput("ssh", sshArgs(plan, command), "", stdin)
	if err != nil {
		return err
	}
	if stdout != "" {
		fmt.Fprint(i.Stdout, stdout)
	}
	if stderr != "" {
		fmt.Fprint(i.Stderr, stderr)
	}
	return nil
}

func (i *Installer) copyRemote(plan Plan, src string, dst string) error {
	if i.remoteCopy != nil {
		return i.remoteCopy(plan, src, dst)
	}
	args := []string{
		"-q",
		"-o", "BatchMode=yes",
	}
	args = append(args, setupKnownHostOptions(plan, "accept-new")...)
	if plan.SSHKey != "" {
		args = append(args, "-i", plan.SSHKey)
	}
	if plan.BootstrapIdentityKey != "" {
		args = append(args, "-i", plan.BootstrapIdentityKey)
	}
	args = append(args, src, bootstrapSSHTarget(plan)+":"+dst)
	_, _, err := runCaptured("scp", args, "")
	return err
}

func (i *Installer) createRemoteHelperPath(plan Plan) (string, func(), error) {
	createCommand := "mktemp " + utils.ShellEscape(remoteHelperPattern)
	path, err := i.remoteOutput(plan, createCommand)
	if err != nil {
		return "", func() {}, remoteInstallCommandError(plan, "create helper path on target", createCommand, err)
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", func() {}, fmt.Errorf("create helper path on target: empty path")
	}
	cleanup := func() {
		_ = i.remoteCommand(plan, "rm -f "+utils.ShellEscape(path))
	}
	return path, cleanup, nil
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

func cleanupRemoteHelperCommand(command string, path string) string {
	return "sh -c " + utils.ShellEscape(command+"; status=$?; rm -f "+utils.ShellEscape(path)+"; exit $status")
}

func sshArgs(plan Plan, command string) []string {
	args := []string{
		"-o", "BatchMode=yes",
	}
	args = append(args, setupKnownHostOptions(plan, "accept-new")...)
	if plan.SSHKey != "" {
		args = append(args, "-i", plan.SSHKey)
	}
	if plan.BootstrapIdentityKey != "" {
		args = append(args, "-i", plan.BootstrapIdentityKey)
	}
	args = append(args, bootstrapSSHTarget(plan), command)
	return args
}

func setupKnownHostOptions(plan Plan, strict string) []string {
	path := strings.TrimSpace(plan.KnownHostsFile)
	if path == "" {
		path = knownhosts.DisplayPath
	}
	return knownhosts.SSHOptions(path, strict)
}

func remoteLocalInstallCommand(binary string, plan Plan, operatorKeyFile string, deployKeyFile string) string {
	args := []string{
		binary,
		"box",
		"setup",
		"localhost",
		"--mode", "local",
		"--suppress-setup-narration",
	}
	if plan.ClientAddress != "" {
		args = append(args, "--client-address", plan.ClientAddress)
	}
	if operatorKeyFile != "" {
		args = append(args, "--operator-ssh-public-key-file", operatorKeyFile)
	}
	if deployKeyFile != "" {
		args = append(args, "--deploy-ssh-public-key-file", deployKeyFile)
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

type plannedKey = memberkeys.AuthorizedKey

func resolveSSHKeyPlan(plan Plan, requireOperator bool, _ string, passwordTarget string) (keyPlan, error) {
	operatorKeys, err := readPublicKeyFile(plan.OperatorSSHPublicKeyFile)
	if err != nil {
		return keyPlan{}, err
	}
	deployKeys, err := readPublicKeyFile(plan.DeploySSHPublicKeyFile)
	if err != nil {
		return keyPlan{}, err
	}

	if len(deployKeys) == 0 {
		return keyPlan{}, deployKeyMissingError(passwordProvisionedDetail, passwordTarget)
	}

	if requireOperator && len(operatorKeys) == 0 {
		return keyPlan{}, errcat.New(errcat.CodeOperatorKeyMissing, errcat.Fields{
			"command": sshCopyIDCommand(passwordTarget),
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
					"command": "SHIP_REPO_ROOT=<path-to-ship-checkout> ship box setup <ssh-target>",
				})
			}
			return abs, nil
		}
	}
	return "", errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
		"detail":  "ship Go module was not found",
		"command": "SHIP_REPO_ROOT=<path-to-ship-checkout> ship box setup <ssh-target>",
	})
}

func repoLooksValid(dir string) bool {
	return fileExists(filepath.Join(dir, "go.mod"))
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
	empty := true
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			empty = false
			break
		}
	}
	if empty {
		return nil, errcat.New(errcat.CodeSSHPublicKeyFileEmpty, errcat.Fields{
			"path":    path,
			"command": publicKeyFromPrivateCommand(path),
		})
	}
	keys, err := memberkeys.Normalize(string(data), "")
	if err != nil {
		return nil, err
	}
	return keys, nil
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

func (i *Installer) printSetupNarration(plan Plan) {
	fmt.Fprintln(i.Stderr, "ingress: public 80/443")
	fmt.Fprintln(i.Stderr, "admin: SSH keys only")
}

func (i *Installer) printMemberEnrollment(results []memberkeys.AddResult) {
	for _, result := range results {
		role := result.Role
		if role == "" {
			role = string(store.MemberRoleShipper)
		}
		if result.Added {
			fmt.Fprintf(i.Stderr, "member added: %s (%s, %s)\n", result.Key.Comment, role, result.Key.Fingerprint)
			continue
		}
		fmt.Fprintf(i.Stderr, "member %s already authorized (%s, %s)\n", result.Key.Comment, role, result.Key.Fingerprint)
	}
}

func deployKeyMissingError(detail string, passwordTarget string) error {
	if strings.TrimSpace(passwordTarget) == "" {
		passwordTarget = "root@<ip>"
	}
	return errcat.New(errcat.CodeDeployKeyMissing, errcat.Fields{
		"detail":  detail,
		"command": sshCopyIDCommand(passwordTarget),
	})
}

func sshCopyIDCommand(passwordTarget string) string {
	return "ssh-copy-id -i ~/.ssh/ship.pub " + passwordTarget
}

func publicKeyAuthFailure(detail string) bool {
	detail = strings.ToLower(detail)
	return strings.Contains(detail, "permission denied") && strings.Contains(detail, "publickey")
}

func sshCopyIDTarget(plan Plan) string {
	return bootstrapSSHTarget(plan)
}

func presentOrMissingKeys(value []plannedKey, present string, missing string) string {
	if len(value) != 0 {
		return present
	}
	return missing
}

func runCaptured(name string, args []string, cwd string) (string, string, error) {
	return runCapturedInput(name, args, cwd, nil)
}

func runCapturedInput(name string, args []string, cwd string, stdin []byte) (string, string, error) {
	cmd := exec.Command(name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(), &utils.CommandError{
			Name:   name,
			Args:   append([]string(nil), args...),
			Stdout: stdout.String(),
			Stderr: stderr.String(),
			Err:    err,
		}
	}
	return stdout.String(), stderr.String(), nil
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
		"command": boxSetupCommand(plan.TargetHost, "--mode", "remote"),
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
		return "sudo " + boxSetupCommand("localhost", "--mode", "local")
	}
	return boxSetupCommand(plan.TargetHost, "--mode", "remote", "--bootstrap-user", "root")
}

func hostInstallRetryCommand(plan Plan) string {
	if plan.Mode == "local" || plan.TargetHost == "" || plan.TargetHost == "localhost" {
		return "sudo " + boxSetupCommand("localhost", "--mode", "local")
	}
	return boxSetupCommand(plan.TargetHost, "--mode", "remote")
}

func boxSetupCommand(target string, extra ...string) string {
	args := []string{"ship", "box", "setup", targetOrPlaceholder(target)}
	args = append(args, extra...)
	return shellCommand(args)
}

func sshCommand(plan Plan, command string) string {
	args := []string{"ssh", "-o", "BatchMode=yes"}
	args = append(args, setupKnownHostOptions(plan, "accept-new")...)
	if plan.SSHKey != "" {
		args = append(args, "-i", plan.SSHKey)
	}
	if plan.BootstrapIdentityKey != "" {
		args = append(args, "-i", plan.BootstrapIdentityKey)
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
		return "SHIP_RELEASE_BASE_URL=" + shellArg(defaultReleaseBaseURL) + " " + boxSetupCommand(target)
	}
	if token == "" {
		return "SHIP_RELEASE_TOKEN=<token> " + boxSetupCommand(target)
	}
	return "SHIP_REPO_ROOT=<path-to-ship-checkout> " + boxSetupCommand(target)
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
	host := rememberedHost(plan)
	if privateKey := deployPrivateKeyHint(plan); privateKey != "" {
		fmt.Fprintf(i.Stderr, "set: export SHIP_SSH_KEY=\"$(cat %s)\"\n", utils.ShellEscape(privateKey))
	}
	fmt.Fprintf(i.Stderr, "next: ship box doctor %s\n", host)
}

func (i *Installer) pinKnownHost(plan Plan) error {
	host := rememberedHost(plan)
	changed, err := knownhosts.Reconcile(host, plan.KnownHostsFile)
	if err != nil {
		return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
			"detail":  "pin box host key failed: " + oneLineError(err),
			"command": "fix " + knownhosts.DisplayPath,
		})
	}
	if changed {
		fmt.Fprintln(i.Stderr, knownhosts.SetupHostKeyChangedMessage)
	}
	fmt.Fprintf(i.Stderr, "pinned box %s (%s)\n", host, knownhosts.DisplayPath)
	return nil
}

func rememberedHost(plan Plan) string {
	if strings.TrimSpace(plan.TargetHost) == "" {
		return "localhost"
	}
	return plan.TargetHost
}

func (i *Installer) printBoxReady() {
	fmt.Fprintln(i.Stderr, "box ready")
}

func deployPrivateKeyHint(plan Plan) string {
	if plan.DeployKeyIsShipIdentity {
		return ""
	}
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

func (i *Installer) info(format string, args ...any) {
	fmt.Fprintf(i.Stderr, format+"\n", args...)
}

func (i *Installer) step(format string, args ...any) {
	fmt.Fprintf(i.Stderr, format+"\n", args...)
}

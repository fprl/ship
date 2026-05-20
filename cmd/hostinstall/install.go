package hostinstall

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	HelperBinaryDir          string
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
	provisioningDir, repoRoot, err := locateProvisioningDir()
	if err != nil {
		return err
	}

	plan, err := BuildPlan(opts, i.geteuid() == 0, fileExists("/etc/os-release"))
	if err != nil {
		return err
	}

	if envBool(i.Env, "SIMPLE_VPS_INSTALLER_DUMP_PLAN", false) {
		return i.dumpInstallPlan(plan)
	}

	if err := prepareAnsibleEnv(provisioningDir); err != nil {
		return err
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
		err = i.runRemote(plan, provisioningDir, repoRoot)
	case "local":
		err = i.runLocal(plan, provisioningDir, repoRoot)
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

func (i *Installer) runRemote(plan Plan, provisioningDir string, repoRoot string) error {
	if _, err := i.look("ansible-playbook"); err != nil {
		return errors.New("required command not found: ansible-playbook")
	}

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
	plan.HelperBinaryDir = helperDir

	inventory, err := writeTempFile("simple-vps-inventory-", remoteInventory(plan))
	if err != nil {
		return err
	}
	defer os.Remove(inventory)

	extraVars, err := writeTempFile("simple-vps-vars-", renderExtraVars(plan, keyPlan))
	if err != nil {
		return err
	}
	defer os.Remove(extraVars)

	commonArgs := []string{"-i", inventory, "-e", "target=simple_vps", "-e", "@" + extraVars}
	bootstrapArgs := append([]string{}, commonArgs...)
	bootstrapArgs = append(bootstrapArgs, "-u", plan.BootstrapUser)
	applyArgs := append([]string{}, commonArgs...)
	applyArgs = append(applyArgs, "-u", plan.OperatorUser)
	if plan.SSHKey != "" {
		bootstrapArgs = append(bootstrapArgs, "--private-key", plan.SSHKey)
		applyArgs = append(applyArgs, "--private-key", plan.SSHKey)
	}

	i.step("Phase 1/2: bootstrap")
	if err := i.runAnsible(plan, provisioningDir, "vps-bootstrap.yml", bootstrapArgs); err != nil {
		return err
	}

	i.step("Phase 2/2: apply")
	if err := i.runAnsible(plan, provisioningDir, "vps-apply.yml", applyArgs); err != nil {
		i.warn("Apply phase as '%s' failed; retrying as '%s'.", plan.OperatorUser, plan.BootstrapUser)
		return i.runAnsible(plan, provisioningDir, "vps-apply.yml", bootstrapArgs)
	}
	return nil
}

func (i *Installer) runLocal(plan Plan, provisioningDir string, repoRoot string) error {
	if i.geteuid() != 0 {
		return errors.New("local mode must run as root")
	}
	if err := i.ensureAnsibleInplace(); err != nil {
		return err
	}

	keyPlan, err := resolveSSHKeyPlan(plan, true, "/root/.ssh/authorized_keys")
	if err != nil {
		return err
	}

	helperDir, cleanupHelper, err := i.prepareGoHelperBinaries(repoRoot)
	if err != nil {
		return err
	}
	defer cleanupHelper()
	plan.HelperBinaryDir = helperDir

	i.info("Running in local mode on localhost")

	inventory, err := writeTempFile("simple-vps-inventory-", localInventory())
	if err != nil {
		return err
	}
	defer os.Remove(inventory)

	extraVars, err := writeTempFile("simple-vps-vars-", renderExtraVars(plan, keyPlan))
	if err != nil {
		return err
	}
	defer os.Remove(extraVars)

	commonArgs := []string{"-i", inventory, "-e", "target=simple_vps", "-e", "@" + extraVars}
	i.step("Phase 1/2: bootstrap")
	if err := i.runAnsible(plan, provisioningDir, "vps-bootstrap.yml", commonArgs); err != nil {
		return err
	}

	i.step("Phase 2/2: apply")
	return i.runAnsible(plan, provisioningDir, "vps-apply.yml", commonArgs)
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
	fmt.Fprintln(i.Stdout, "--- extra-vars ---")
	fmt.Fprint(i.Stdout, renderExtraVars(plan, keyPlan))
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

func (i *Installer) ensureAnsibleInplace() error {
	if _, err := i.look("ansible-playbook"); err == nil {
		return nil
	}
	if _, err := i.look("apt-get"); err != nil {
		return errors.New("required command not found: apt-get")
	}
	i.info("Ansible not found. Installing with apt-get...")
	_ = os.Setenv("DEBIAN_FRONTEND", "noninteractive")
	if err := i.run("apt-get", []string{"update", "-y"}, ""); err != nil {
		return err
	}
	return i.run("apt-get", []string{"install", "-y", "ansible"}, "")
}

func (i *Installer) runAnsible(plan Plan, provisioningDir string, playbook string, args []string) error {
	fullArgs := append([]string{}, args...)
	if plan.CheckMode {
		fullArgs = append(fullArgs, "--check")
	}
	fullArgs = append(fullArgs, filepath.Join(provisioningDir, "playbooks", playbook))
	return i.run("ansible-playbook", fullArgs, "")
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

func renderExtraVars(plan Plan, keys keyPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "simple_vps_operator_user: \"%s\"\n", plan.OperatorUser)
	fmt.Fprintf(&b, "simple_vps_deploy_user: \"%s\"\n", plan.DeployUser)
	fmt.Fprintf(&b, "simple_vps_allow_shared_ssh_key: %s\n", boolText(plan.SharedKey))
	fmt.Fprintf(&b, "simple_vps_timezone: \"%s\"\n", plan.Timezone)
	fmt.Fprintf(&b, "simple_vps_locale: \"%s\"\n", plan.Locale)
	fmt.Fprintf(&b, "security_enable_tailscale: %s\n", boolText(plan.Tailscale))
	fmt.Fprintf(&b, "simple_vps_tailscale_auth_key: '%s'\n", yamlSingle(plan.TailscaleAuthKey))
	fmt.Fprintf(&b, "simple_vps_tailscale_hostname: '%s'\n", yamlSingle(plan.TailscaleHostname))
	fmt.Fprintf(&b, "simple_vps_enable_cloudflare_tunnel: %s\n", boolText(plan.CloudflareTunnel))
	fmt.Fprintf(&b, "simple_vps_cloudflare_api_token: '%s'\n", yamlSingle(plan.CloudflareAPIToken))
	fmt.Fprintf(&b, "simple_vps_cloudflare_account_id: '%s'\n", yamlSingle(plan.CloudflareAccountID))
	fmt.Fprintf(&b, "simple_vps_cloudflare_tunnel_token: '%s'\n", yamlSingle(plan.CloudflareTunnelToken))
	fmt.Fprintf(&b, "simple_vps_cloudflare_tunnel_config_path: '%s'\n", yamlSingle(plan.CloudflareTunnelConfig))
	fmt.Fprintf(&b, "simple_vps_install_docker: %s\n", boolText(plan.InstallDocker))
	fmt.Fprintf(&b, "simple_vps_install_litestream: %s\n", boolText(plan.InstallLitestream))
	if plan.HelperBinaryDir != "" {
		fmt.Fprintf(&b, "simple_vps_helper_binary_dir: '%s'\n", yamlSingle(plan.HelperBinaryDir))
	}

	if keys.Operator != "" {
		fmt.Fprintln(&b, "simple_vps_operator_ssh_public_keys:")
		fmt.Fprintf(&b, "  - '%s'\n", yamlSingle(keys.Operator))
	} else {
		fmt.Fprintln(&b, "simple_vps_operator_ssh_public_keys: []")
	}

	if keys.Deploy != "" {
		fmt.Fprintln(&b, "simple_vps_deploy_ssh_public_keys:")
		fmt.Fprintf(&b, "  - '%s'\n", yamlSingle(keys.Deploy))
	} else {
		fmt.Fprintln(&b, "simple_vps_deploy_ssh_public_keys: []")
	}

	return b.String()
}

func remoteInventory(plan Plan) string {
	return fmt.Sprintf("[simple_vps]\nsimple_vps_host ansible_host=%s ansible_python_interpreter=/usr/bin/python3\n", plan.TargetHost)
}

func localInventory() string {
	return "[simple_vps]\nsimple_vps_local ansible_connection=local ansible_python_interpreter=/usr/bin/python3\n"
}

func locateProvisioningDir() (string, string, error) {
	var candidates []string
	if envDir := os.Getenv("SIMPLE_VPS_PROVISIONING_DIR"); envDir != "" {
		candidates = append(candidates, envDir)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "provisioning"))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(exeDir, "provisioning"))
		candidates = append(candidates, filepath.Join(exeDir, "..", "provisioning"))
	}

	for _, candidate := range candidates {
		if provisioningLooksValid(candidate) {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return "", "", err
			}
			return abs, filepath.Dir(abs), nil
		}
	}
	return "", "", errors.New("Simple VPS provisioning files were not found; run from a checkout or set SIMPLE_VPS_PROVISIONING_DIR")
}

func provisioningLooksValid(dir string) bool {
	return fileExists(filepath.Join(dir, "playbooks", "vps-bootstrap.yml")) &&
		fileExists(filepath.Join(dir, "playbooks", "vps-apply.yml")) &&
		fileExists(filepath.Join(dir, "roles", "system", "tasks", "main.yml"))
}

func prepareAnsibleEnv(provisioningDir string) error {
	cfg := filepath.Join(provisioningDir, "ansible.cfg")
	if fileExists(cfg) {
		if err := os.Setenv("ANSIBLE_CONFIG", cfg); err != nil {
			return err
		}
	}
	if os.Getenv("ANSIBLE_LOCAL_TEMP") == "" {
		base := os.TempDir()
		if tmp := os.Getenv("TMPDIR"); tmp != "" {
			base = tmp
		}
		if err := os.Setenv("ANSIBLE_LOCAL_TEMP", filepath.Join(base, "simple-vps-ansible-tmp")); err != nil {
			return err
		}
	}
	return os.MkdirAll(os.Getenv("ANSIBLE_LOCAL_TEMP"), 0755)
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

func writeTempFile(pattern string, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := os.Chmod(name, 0600); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
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

func yamlSingle(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func presentOrMissing(value string, present string, missing string) string {
	if value != "" {
		return present
	}
	return missing
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

func (i *Installer) warn(format string, args ...any) {
	fmt.Fprintf(i.Stdout, "Warning: "+format+"\n", args...)
}

func (i *Installer) step(format string, args ...any) {
	fmt.Fprintf(i.Stdout, "--> "+format+"\n", args...)
}

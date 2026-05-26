package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fprl/simple-vps/internal/provision/host"
	"github.com/fprl/simple-vps/internal/store"
)

const (
	litestreamVersion           = "0.5.8"
	litestreamSHA256X8664       = "854ed88ce4c30da887e7c099ffb2941bae2f7108db89886e7ec5ee80c81356b7"
	litestreamSHA256ARM64       = "bc92d95bc8203a41afe9b5df552aafe1bc0781c6b4341d52970e3f1cd7220c8d"
	caddyAptKeyFingerprint      = "65760C51EDEA2017CEA2CA15155B6D79CA56EA34"
	dockerAptKeyFingerprint     = "9DC858229FC7DD38854AE2D88D81803C0EBFCD88"
	tailscaleAptKeyFingerprint  = "2596A99EAAB33821893C0A79458CA832957F5868"
	cloudflareAptKeyFingerprint = "CC94B39C77AE7342A68B89628A682D308D4E5E73"
)

type InstallOptions struct {
	OperatorUser           string
	DeployUser             string
	OperatorSSHPublicKeys  []string
	DeploySSHPublicKeys    []string
	Timezone               string
	Locale                 string
	Tailscale              bool
	TailscaleAuthKey       string
	TailscaleHostname      string
	CloudflareTunnel       bool
	CloudflareAPIToken     string
	CloudflareAccountID    string
	CloudflareTunnelToken  string
	CloudflareTunnelConfig string
	InstallDocker          bool
	InstallLitestream      bool
	CheckMode              bool
	StateRoot              string
	HelperBinaryPath       string
	ApplyID                string
	Now                    func() time.Time
}

type InstallSummary struct {
	ApplyID           string
	OperationsChanged int
}

type operation struct {
	name string
	run  func(host.Apply) (bool, error)
}

func RunInstall(ctx context.Context, runner host.Runner, opts InstallOptions) (InstallSummary, error) {
	opts = normalizeOptions(opts)
	apply := host.Apply{
		Context:   ctx,
		Runner:    runner,
		CheckMode: opts.CheckMode,
		State:     &host.RunState{},
	}
	stateStore := store.Store{Root: opts.StateRoot}
	startedAt := opts.Now().UTC()
	applyID := opts.ApplyID
	if applyID == "" {
		applyID = startedAt.Format("20060102T150405Z")
	}

	ops := installOperations(opts, stateStore)
	changedCount := 0
	for _, op := range ops {
		changed, err := op.run(apply)
		if err != nil {
			if !opts.CheckMode {
				_ = writeApplyState(stateStore, opts, applyID, startedAt, opts.Now().UTC(), "failed", changedCount)
			}
			return InstallSummary{ApplyID: applyID, OperationsChanged: changedCount}, fmt.Errorf("%s: %w", op.name, err)
		}
		if changed {
			changedCount++
		}
	}

	if !opts.CheckMode {
		if err := writeApplyState(stateStore, opts, applyID, startedAt, opts.Now().UTC(), "ok", changedCount); err != nil {
			return InstallSummary{ApplyID: applyID, OperationsChanged: changedCount}, err
		}
	}
	return InstallSummary{ApplyID: applyID, OperationsChanged: changedCount}, nil
}

func installOperations(opts InstallOptions, stateStore store.Store) []operation {
	var ops []operation
	add := func(name string, run func(host.Apply) (bool, error)) {
		ops = append(ops, operation{name: name, run: run})
	}

	add("write host desired state", func(apply host.Apply) (bool, error) {
		desired := desiredHost(opts)
		changed, err := hostDesiredChanged(stateStore, desired)
		if err != nil {
			return false, err
		}
		if changed && !opts.CheckMode {
			if err := stateStore.WriteHostDesired(desired); err != nil {
				return false, err
			}
		}
		return changed, nil
	})

	for _, dir := range []host.Directory{
		{Path: "/etc/simple-vps", Owner: "root", Group: "root", Mode: 0755},
		{Path: "/etc/simple-vps/backups", Owner: "root", Group: "root", Mode: 0755},
		{Path: "/etc/simple-vps/providers", Owner: "root", Group: "root", Mode: 0755},
		{Path: "/etc/simple-vps/secrets", Owner: "root", Group: "root", Mode: 0755},
	} {
		dir := dir
		add("ensure directory "+dir.Path, func(apply host.Apply) (bool, error) {
			return host.EnsureDirectory(apply, dir)
		})
	}

	for _, pkg := range essentialPackages() {
		pkg := pkg
		add("install package "+pkg, func(apply host.Apply) (bool, error) {
			return host.EnsurePackage(apply, pkg)
		})
	}

	add("ensure operator user", func(apply host.Apply) (bool, error) {
		return host.EnsureUser(apply, host.User{Name: opts.OperatorUser, PrimaryGroup: opts.OperatorUser, Shell: "/bin/bash", CreateHome: true})
	})
	add("operator sudo group", func(apply host.Apply) (bool, error) {
		return host.EnsureGroupMembership(apply, opts.OperatorUser, "sudo")
	})
	add("operator sudoers", func(apply host.Apply) (bool, error) {
		return host.EnsureSudoersFile(apply, "operator", []byte(fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", opts.OperatorUser)))
	})
	add("ensure deploy user", func(apply host.Apply) (bool, error) {
		return host.EnsureUser(apply, host.User{Name: opts.DeployUser, PrimaryGroup: opts.DeployUser, Shell: "/bin/bash", CreateHome: true})
	})
	addAuthorizedKeys(&ops, opts.OperatorUser, opts.OperatorSSHPublicKeys)
	addAuthorizedKeys(&ops, opts.DeployUser, opts.DeploySSHPublicKeys)

	add("timezone", func(apply host.Apply) (bool, error) {
		return host.EnsureTimezone(apply, opts.Timezone)
	})
	add("locale", func(apply host.Apply) (bool, error) {
		return host.EnsureLocale(apply, opts.Locale)
	})
	addSSHHardening(&ops)
	addSecurity(&ops, opts)
	addHelper(&ops, opts)
	addPodman(&ops)
	addCaddy(&ops)
	if opts.InstallLitestream {
		addLitestream(&ops)
	}
	if opts.InstallDocker {
		addDocker(&ops, opts)
	}
	if opts.Tailscale {
		addTailscale(&ops, opts)
	}
	if opts.CloudflareTunnel {
		addCloudflare(&ops, opts)
	}

	return ops
}

func addAuthorizedKeys(ops *[]operation, user string, keys []string) {
	dir := fmt.Sprintf("/home/%s/.ssh", user)
	*ops = append(*ops, operation{name: "ssh directory " + user, run: func(apply host.Apply) (bool, error) {
		return host.EnsureDirectory(apply, host.Directory{Path: dir, Owner: user, Group: user, Mode: 0700})
	}})
	*ops = append(*ops, operation{name: "authorized keys " + user, run: func(apply host.Apply) (bool, error) {
		content := ""
		if len(keys) > 0 {
			content = strings.Join(keys, "\n") + "\n"
		}
		return host.EnsureFile(apply, host.File{
			Path:    filepath.Join(dir, "authorized_keys"),
			Content: []byte(content),
			Owner:   user,
			Group:   user,
			Mode:    0600,
		})
	}})
}

func addSSHHardening(ops *[]operation) {
	*ops = append(*ops, operation{name: "ssh hardening", run: ensureSSHHardening})
}

func ensureSSHHardening(apply host.Apply) (bool, error) {
	changed := false
	for _, item := range []struct {
		re   string
		line string
	}{
		{`^#?PermitRootLogin\b`, "PermitRootLogin prohibit-password"},
		{`^#?PasswordAuthentication\b`, "PasswordAuthentication no"},
		{`^#?PubkeyAuthentication\b`, "PubkeyAuthentication yes"},
		{`^#?X11Forwarding\b`, "X11Forwarding no"},
		{`^#?MaxAuthTries\b`, "MaxAuthTries 3"},
	} {
		lineChanged, err := host.EnsureLineInFile(apply, host.LineInFile{
			Path:   "/etc/ssh/sshd_config",
			Regexp: item.re,
			Line:   item.line,
			Owner:  "root",
			Group:  "root",
			Mode:   0644,
		})
		if err != nil {
			return false, err
		}
		changed = changed || lineChanged
	}
	if !changed {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	if _, err := host.EnsureSystemdUnit(apply, host.SystemdUnit{Name: "ssh.service", Action: host.Restarted}); err != nil {
		return false, err
	}
	return true, nil
}

func addSecurity(ops *[]operation, opts InstallOptions) {
	for _, file := range []host.File{
		{
			Path:    "/etc/apt/apt.conf.d/20auto-upgrades",
			Content: []byte("APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n"),
			Owner:   "root", Group: "root", Mode: 0644,
		},
		{
			Path:    "/etc/fail2ban/jail.local",
			Content: []byte("[sshd]\nenabled = true\nport = ssh\nfilter = sshd\nlogpath = /var/log/auth.log\nmaxretry = 3\nbantime = 3600\nfindtime = 600\n"),
			Owner:   "root", Group: "root", Mode: 0644,
		},
	} {
		file := file
		*ops = append(*ops, operation{name: "write " + file.Path, run: func(apply host.Apply) (bool, error) {
			return host.EnsureFile(apply, file)
		}})
	}
	for _, rule := range []host.UfwRule{
		{Rule: "default deny incoming"},
		{Rule: "default allow outgoing"},
		{Rule: "allow 22/tcp"},
		{Rule: "allow 41641/udp"},
		{Rule: "allow 80/tcp", Delete: opts.CloudflareTunnel},
		{Rule: "allow 443/tcp", Delete: opts.CloudflareTunnel},
	} {
		rule := rule
		*ops = append(*ops, operation{name: "ufw " + rule.Rule, run: func(apply host.Apply) (bool, error) {
			return host.EnsureUfwRule(apply, rule)
		}})
	}
	*ops = append(*ops, operation{name: "enable ufw", run: func(apply host.Apply) (bool, error) {
		active, err := ufwActive(apply)
		if err != nil {
			return false, err
		}
		if active {
			return false, nil
		}
		if apply.CheckMode {
			return true, nil
		}
		result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "ufw", Args: []string{"--force", "enable"}})
		if err != nil {
			return false, err
		}
		return true, commandOK(result, "ufw", []string{"--force", "enable"})
	}})
	*ops = append(*ops, operation{name: "fail2ban service", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSystemdUnit(apply, host.SystemdUnit{Name: "fail2ban.service", Action: host.Started})
	}})
}

func ufwActive(apply host.Apply) (bool, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "ufw", Args: []string{"status"}})
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, nil
	}
	return strings.Contains(strings.ToLower(string(result.Stdout)), "status: active"), nil
}

func addHelper(ops *[]operation, opts InstallOptions) {
	*ops = append(*ops, operation{name: "install simple-vps helper", run: func(apply host.Apply) (bool, error) {
		path := opts.HelperBinaryPath
		if path == "" {
			var err error
			path, err = os.Executable()
			if err != nil {
				return false, err
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		return host.EnsureFile(apply, host.File{Path: "/usr/local/bin/simple-vps", Content: data, Owner: "root", Group: "root", Mode: 0755})
	}})
	*ops = append(*ops, operation{name: "simple-vps sudoers", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSudoersFile(apply, "simple-vps", []byte(fmt.Sprintf("%s ALL=(root) NOPASSWD: /usr/local/bin/simple-vps\n", opts.DeployUser)))
	}})
}

// addPodman installs the Podman container engine and creates the shared
// `ingress` network used by Caddy to reach app containers.
//
// Per ADR-0005 Section 14: Podman is installed from the Ubuntu 24.04
// Universe archive, no third-party apt repo. The Universe-shipped Podman
// (4.9.x on Noble) is sufficient for systemd integration and the security
// flags Section 7 requires.
//
// Per ADR-0006 Cut 2: the `ingress` Podman network is created once at host
// install time; app containers join it at run time so Caddy can reach them
// by container DNS.
func addPodman(ops *[]operation) {
	*ops = append(*ops, operation{name: "podman package", run: func(apply host.Apply) (bool, error) {
		return host.EnsurePackage(apply, "podman")
	}})
	*ops = append(*ops, operation{name: "podman ingress network", run: ensureIngressNetwork})
}

func ensureIngressNetwork(apply host.Apply) (bool, error) {
	if apply.CheckMode {
		return true, nil
	}
	probe, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "podman", Args: []string{"network", "exists", "ingress"}})
	if err != nil {
		return false, err
	}
	if probe.ExitCode == 0 {
		return false, nil
	}
	create, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "podman", Args: []string{"network", "create", "ingress"}})
	if err != nil {
		return false, err
	}
	if err := commandOK(create, "podman", []string{"network", "create", "ingress"}); err != nil {
		return false, err
	}
	return true, nil
}

func addCaddy(ops *[]operation) {
	*ops = append(*ops, operation{name: "caddy repo", run: func(apply host.Apply) (bool, error) {
		return host.EnsureAptRepo(apply, host.AptRepo{
			Name:           "caddy",
			KeyURL:         "https://dl.cloudsmith.io/public/caddy/stable/gpg.key",
			KeyPath:        "/usr/share/keyrings/caddy-stable-archive-keyring.gpg",
			KeyFingerprint: caddyAptKeyFingerprint,
			KeyDearmor:     true,
			SourcePath:     "/etc/apt/sources.list.d/caddy-stable.list",
			SourceLine:     "deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main",
		})
	}})
	*ops = append(*ops, operation{name: "caddy package", run: func(apply host.Apply) (bool, error) { return host.EnsurePackage(apply, "caddy") }})
	for _, dir := range []host.Directory{
		{Path: "/etc/caddy/simple-vps", Owner: "root", Group: "root", Mode: 0755},
		{Path: "/etc/caddy/conf.d", Owner: "root", Group: "root", Mode: 0755},
		{Path: "/var/www/html", Owner: "caddy", Group: "caddy", Mode: 0755},
	} {
		dir := dir
		*ops = append(*ops, operation{name: "caddy dir " + dir.Path, run: func(apply host.Apply) (bool, error) { return host.EnsureDirectory(apply, dir) }})
	}
	*ops = append(*ops, operation{name: "caddy service", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSystemdUnit(apply, host.SystemdUnit{Name: "caddy.service", Action: host.Started})
	}})
	*ops = append(*ops, operation{name: "generate caddy", run: runCommandChangedUnlessOutputContains("simple-vps", []string{"server", "generate-caddy"}, "already up to date")})
}

func addLitestream(ops *[]operation) {
	*ops = append(*ops, operation{name: "litestream deb", run: func(apply host.Apply) (bool, error) {
		arch := litestreamArch(runtime.GOARCH)
		if arch == "" {
			return false, fmt.Errorf("unsupported Litestream architecture: %s", runtime.GOARCH)
		}
		expectedSHA256 := litestreamSHA256(arch)
		if expectedSHA256 == "" {
			return false, fmt.Errorf("missing Litestream checksum for architecture: %s", arch)
		}
		url := fmt.Sprintf("https://github.com/benbjohnson/litestream/releases/download/v%s/litestream-%s-linux-%s.deb", litestreamVersion, litestreamVersion, arch)
		installed, err := litestreamInstalled(apply)
		if err != nil {
			return false, err
		}
		if installed {
			return false, nil
		}
		if apply.CheckMode {
			return true, nil
		}
		tempDir, err := createLitestreamTempDir(apply)
		if err != nil {
			return false, err
		}
		defer cleanupTempDir(apply, tempDir)

		deb := fmt.Sprintf("%s/litestream-%s-linux-%s.deb", tempDir, litestreamVersion, arch)
		result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "curl", Args: []string{"-fsSL", url, "-o", deb}})
		if err != nil {
			return false, err
		}
		if err := commandOK(result, "curl", []string{"-fsSL", url, "-o", deb}); err != nil {
			return false, err
		}
		if err := requireFileSHA256(apply, "litestream", deb, expectedSHA256); err != nil {
			return false, err
		}
		result, err = apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "apt-get", Args: []string{"install", "-y", deb}})
		if err != nil {
			return false, err
		}
		return true, commandOK(result, "apt-get", []string{"install", "-y", deb})
	}})
}

func litestreamSHA256(arch string) string {
	switch arch {
	case "x86_64":
		return litestreamSHA256X8664
	case "arm64":
		return litestreamSHA256ARM64
	default:
		return ""
	}
}

func createLitestreamTempDir(apply host.Apply) (string, error) {
	args := []string{"-d", "/tmp/simple-vps-litestream.XXXXXX"}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "mktemp", Args: args})
	if err != nil {
		return "", err
	}
	if err := commandOK(result, "mktemp", args); err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(result.Stdout))
	if path == "" {
		return "", errors.New("litestream temp dir creation returned empty path")
	}
	return path, nil
}

func cleanupTempDir(apply host.Apply, path string) {
	_, _ = apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "rm", Args: []string{"-rf", "--", path}})
}

func requireFileSHA256(apply host.Apply, label string, path string, expected string) error {
	args := []string{path}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "sha256sum", Args: args})
	if err != nil {
		return err
	}
	if err := commandOK(result, "sha256sum", args); err != nil {
		return err
	}
	fields := strings.Fields(string(result.Stdout))
	if len(fields) < 2 || len(fields[0]) != 64 {
		return fmt.Errorf("%s checksum output malformed for %s", label, path)
	}
	got := strings.ToLower(fields[0])
	want := strings.ToLower(strings.TrimSpace(expected))
	if got != want {
		return fmt.Errorf("%s checksum mismatch for %s: expected %s, got %s", label, path, want, got)
	}
	return nil
}

func litestreamInstalled(apply host.Apply) (bool, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "dpkg-query", Args: []string{"-W", "-f=${Version}", "litestream"}})
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, nil
	}
	return strings.TrimSpace(string(result.Stdout)) == litestreamVersion, nil
}

func addDocker(ops *[]operation, opts InstallOptions) {
	*ops = append(*ops, operation{name: "docker repo", run: func(apply host.Apply) (bool, error) {
		codename, err := ubuntuCodename(apply)
		if err != nil {
			return false, err
		}
		return host.EnsureAptRepo(apply, host.AptRepo{
			Name:           "docker",
			KeyURL:         "https://download.docker.com/linux/ubuntu/gpg",
			KeyPath:        "/usr/share/keyrings/docker.asc",
			KeyFingerprint: dockerAptKeyFingerprint,
			SourcePath:     "/etc/apt/sources.list.d/docker.list",
			SourceLine:     "deb [arch=" + debArch(runtime.GOARCH) + " signed-by=/usr/share/keyrings/docker.asc] https://download.docker.com/linux/ubuntu " + codename + " stable",
		})
	}})
	for _, pkg := range []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"} {
		pkg := pkg
		*ops = append(*ops, operation{name: "docker package " + pkg, run: func(apply host.Apply) (bool, error) { return host.EnsurePackage(apply, pkg) }})
	}
	*ops = append(*ops, operation{name: "operator docker group", run: func(apply host.Apply) (bool, error) {
		return host.EnsureGroupMembership(apply, opts.OperatorUser, "docker")
	}})
	*ops = append(*ops, operation{name: "docker daemon config", run: func(apply host.Apply) (bool, error) {
		return host.EnsureFile(apply, host.File{Path: "/etc/docker/daemon.json", Content: []byte("{\n  \"log-driver\": \"json-file\",\n  \"log-opts\": {\n    \"max-size\": \"10m\",\n    \"max-file\": \"3\"\n  }\n}\n"), Owner: "root", Group: "root", Mode: 0644})
	}})
	*ops = append(*ops, operation{name: "docker service", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSystemdUnit(apply, host.SystemdUnit{Name: "docker.service", Action: host.Started})
	}})
}

func addTailscale(ops *[]operation, opts InstallOptions) {
	*ops = append(*ops, operation{name: "tailscale repo", run: func(apply host.Apply) (bool, error) {
		codename, err := ubuntuCodename(apply)
		if err != nil {
			return false, err
		}
		return host.EnsureAptRepo(apply, host.AptRepo{
			Name:           "tailscale",
			KeyURL:         "https://pkgs.tailscale.com/stable/ubuntu/" + codename + ".noarmor.gpg",
			KeyPath:        "/usr/share/keyrings/tailscale-archive-keyring.gpg",
			KeyFingerprint: tailscaleAptKeyFingerprint,
			SourcePath:     "/etc/apt/sources.list.d/tailscale.list",
			SourceLine:     "deb [signed-by=/usr/share/keyrings/tailscale-archive-keyring.gpg] https://pkgs.tailscale.com/stable/ubuntu " + codename + " main",
		})
	}})
	*ops = append(*ops, operation{name: "tailscale package", run: func(apply host.Apply) (bool, error) { return host.EnsurePackage(apply, "tailscale") }})
	*ops = append(*ops, operation{name: "tailscaled service", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSystemdUnit(apply, host.SystemdUnit{Name: "tailscaled.service", Action: host.Started})
	}})
	if opts.TailscaleAuthKey != "" {
		args := []string{"up", "--auth-key=" + opts.TailscaleAuthKey}
		if opts.TailscaleHostname != "" {
			args = append(args, "--hostname="+opts.TailscaleHostname)
		}
		*ops = append(*ops, operation{name: "tailscale auth", run: func(apply host.Apply) (bool, error) {
			active, err := tailscaleRunning(apply)
			if err != nil {
				return false, err
			}
			if active {
				return false, nil
			}
			return runCommand("tailscale", args...)(apply)
		}})
	}
}

func tailscaleRunning(apply host.Apply) (bool, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "tailscale", Args: []string{"status", "--json"}})
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, nil
	}
	return strings.Contains(string(result.Stdout), `"BackendState":"Running"`), nil
}

func addCloudflare(ops *[]operation, opts InstallOptions) {
	*ops = append(*ops, operation{name: "cloudflared repo", run: func(apply host.Apply) (bool, error) {
		return host.EnsureAptRepo(apply, host.AptRepo{
			Name:           "cloudflared",
			KeyURL:         "https://pkg.cloudflare.com/cloudflare-main.gpg",
			KeyPath:        "/usr/share/keyrings/cloudflare-main.gpg",
			KeyFingerprint: cloudflareAptKeyFingerprint,
			SourcePath:     "/etc/apt/sources.list.d/cloudflared.list",
			SourceLine:     "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main",
		})
	}})
	*ops = append(*ops, operation{name: "cloudflared package", run: func(apply host.Apply) (bool, error) { return host.EnsurePackage(apply, "cloudflared") }})
	*ops = append(*ops, operation{name: "cloudflared user", run: func(apply host.Apply) (bool, error) {
		return host.EnsureUser(apply, host.User{Name: "cloudflared", PrimaryGroup: "cloudflared", Shell: "/usr/sbin/nologin", Home: "/var/lib/cloudflared", System: true, CreateHome: true})
	}})
	*ops = append(*ops, operation{name: "cloudflared config dir", run: func(apply host.Apply) (bool, error) {
		return host.EnsureDirectory(apply, host.Directory{Path: "/etc/cloudflared", Owner: "root", Group: "cloudflared", Mode: 0750})
	}})
	// Token/setup ops intentionally precede the service op; this flag carries
	// their drift forward so service convergence can restart only when needed.
	cloudflaredRuntimeChanged := false
	if opts.CloudflareTunnelToken != "" {
		*ops = append(*ops, operation{name: "cloudflared token", run: func(apply host.Apply) (bool, error) {
			changed, err := host.EnsureFile(apply, host.File{Path: "/etc/cloudflared/tunnel-token", Content: []byte(strings.TrimSpace(opts.CloudflareTunnelToken) + "\n"), Owner: "root", Group: "cloudflared", Mode: 0640})
			if changed {
				cloudflaredRuntimeChanged = true
			}
			return changed, err
		}})
	}
	if opts.CloudflareAPIToken != "" {
		*ops = append(*ops, operation{name: "cloudflare api token", run: func(apply host.Apply) (bool, error) {
			return host.EnsureFile(apply, host.File{Path: "/etc/simple-vps/cloudflare-api-token", Content: []byte(strings.TrimSpace(opts.CloudflareAPIToken) + "\n"), Owner: "root", Group: "root", Mode: 0600})
		}})
		args := []string{"server", "cloudflare", "setup-tunnel", "--token-file", "/etc/simple-vps/cloudflare-api-token", "--name", "simple-vps-" + hostname()}
		if opts.CloudflareAccountID != "" {
			args = append(args, "--account-id", opts.CloudflareAccountID)
		}
		*ops = append(*ops, operation{name: "cloudflare setup tunnel", run: func(apply host.Apply) (bool, error) {
			ready, err := cloudflareTunnelAlreadyConfigured(apply)
			if err != nil {
				return false, err
			}
			if ready {
				return false, nil
			}
			changed, err := runCommand("simple-vps", args...)(apply)
			if changed {
				cloudflaredRuntimeChanged = true
			}
			return changed, err
		}})
	}
	if opts.CloudflareTunnelToken != "" || opts.CloudflareAPIToken != "" || opts.CloudflareTunnelConfig != "" {
		*ops = append(*ops, operation{name: "cloudflared service", run: func(apply host.Apply) (bool, error) {
			execStart := "/usr/bin/cloudflared tunnel --no-autoupdate run --token-file /etc/cloudflared/tunnel-token"
			if opts.CloudflareTunnelConfig != "" {
				execStart = "/usr/bin/cloudflared --config " + opts.CloudflareTunnelConfig + " tunnel run"
			}
			return ensureCloudflaredService(apply, []byte(cloudflaredUnit(execStart)), cloudflaredRuntimeChanged)
		}})
	}
}

func ensureCloudflaredService(apply host.Apply, unitContent []byte, restartOnChange bool) (bool, error) {
	unitChanged, err := host.EnsureSystemdUnit(apply, host.SystemdUnit{Name: "cloudflared.service", Content: unitContent})
	if err != nil {
		return false, err
	}
	if unitChanged || restartOnChange {
		if apply.CheckMode {
			return true, nil
		}
		result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "systemctl", Args: []string{"restart", "cloudflared.service"}})
		if err != nil {
			return false, err
		}
		return true, commandOK(result, "systemctl", []string{"restart", "cloudflared.service"})
	}
	active, err := systemdServiceActive(apply, "cloudflared.service")
	if err != nil {
		return false, err
	}
	if active {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "systemctl", Args: []string{"start", "cloudflared.service"}})
	if err != nil {
		return false, err
	}
	return true, commandOK(result, "systemctl", []string{"start", "cloudflared.service"})
}

func systemdServiceActive(apply host.Apply, name string) (bool, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "systemctl", Args: []string{"is-active", "--quiet", name}})
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

func cloudflareTunnelAlreadyConfigured(apply host.Apply) (bool, error) {
	if _, err := apply.Runner.ReadFile(apply.ContextOrBackground(), "/etc/cloudflared/tunnel-token"); err != nil {
		if errors.Is(err, host.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	stateStore := store.Default()
	cloudflarePath := stateStore.CloudflarePath()
	data, err := apply.Runner.ReadFile(apply.ContextOrBackground(), cloudflarePath)
	if err != nil {
		if errors.Is(err, host.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	var file store.CloudflareFile
	if err := json.Unmarshal(data.Content, &file); err != nil {
		return false, fmt.Errorf("invalid %s: %w", cloudflarePath, err)
	}
	return file.TunnelID != "" && file.TunnelName != "", nil
}

func runCommand(program string, args ...string) func(host.Apply) (bool, error) {
	return func(apply host.Apply) (bool, error) {
		if apply.CheckMode {
			return true, nil
		}
		result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: program, Args: args})
		if err != nil {
			return false, err
		}
		return true, commandOK(result, program, args)
	}
}

func runCommandChangedUnlessOutputContains(program string, args []string, unchangedText string) func(host.Apply) (bool, error) {
	return func(apply host.Apply) (bool, error) {
		if apply.CheckMode {
			return true, nil
		}
		result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: program, Args: args})
		if err != nil {
			return false, err
		}
		if err := commandOK(result, program, args); err != nil {
			return false, err
		}
		output := strings.ToLower(string(result.Stdout) + string(result.Stderr))
		return !strings.Contains(output, strings.ToLower(unchangedText)), nil
	}
}

func commandOK(result host.CommandResult, program string, args []string) error {
	if result.ExitCode == 0 {
		return nil
	}
	return fmt.Errorf("command failed: %s %v: exit %d: %s", program, args, result.ExitCode, strings.TrimSpace(string(result.Stderr)))
}

func desiredHost(opts InstallOptions) store.HostDesired {
	ingress := store.HostIngressDesired{Expose: store.ExposePublic, Tunnel: store.TunnelNone}
	if opts.CloudflareTunnel {
		ingress = store.HostIngressDesired{Expose: store.ExposePrivate, Tunnel: store.TunnelCloudflare}
	}
	packages := map[string]store.DesiredPackage{
		"caddy":  {Source: "caddy-apt", Track: "stable"},
		"podman": {Source: "ubuntu", Track: "noble"},
	}
	if opts.InstallLitestream {
		packages["litestream"] = store.DesiredPackage{Source: "github-release", Version: litestreamVersion}
	}
	if opts.InstallDocker {
		packages["docker"] = store.DesiredPackage{Source: "docker-apt", Track: "stable"}
	}
	return store.HostDesired{
		Users:   store.HostUsers{Operator: opts.OperatorUser, Deploy: opts.DeployUser},
		Ingress: ingress,
		Features: store.HostFeatures{
			Docker:     opts.InstallDocker,
			Litestream: opts.InstallLitestream,
			Runtimes:   []string{},
		},
		Packages: packages,
	}
}

func hostDesiredChanged(stateStore store.Store, desired store.HostDesired) (bool, error) {
	current, err := stateStore.ReadHost()
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	currentData, err := json.Marshal(current.Desired)
	if err != nil {
		return false, err
	}
	nextData, err := json.Marshal(desired)
	if err != nil {
		return false, err
	}
	return string(currentData) != string(nextData), nil
}

func writeApplyState(stateStore store.Store, opts InstallOptions, applyID string, startedAt time.Time, finishedAt time.Time, status string, changed int) error {
	return stateStore.WriteHostState(store.HostObserved{
		Packages: map[string]store.ObservedPackage{},
		Ingress:  store.HostIngressObserved{CloudflaredServiceActive: opts.CloudflareTunnel},
	}, store.HostMeta{
		SimpleVPSVersion: "dev",
		LastApply: &store.ApplyMeta{
			ID:                applyID,
			StartedAt:         startedAt.Format(time.RFC3339),
			FinishedAt:        finishedAt.Format(time.RFC3339),
			Status:            status,
			OperationsChanged: changed,
		},
	})
}

func normalizeOptions(opts InstallOptions) InstallOptions {
	if opts.OperatorUser == "" {
		opts.OperatorUser = "operator"
	}
	if opts.DeployUser == "" {
		opts.DeployUser = "deploy"
	}
	if opts.Timezone == "" {
		opts.Timezone = "UTC"
	}
	if opts.Locale == "" {
		opts.Locale = "en_US.UTF-8"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return opts
}

func essentialPackages() []string {
	return []string{
		"apt-listchanges",
		"apt-transport-https",
		"build-essential",
		"ca-certificates",
		"curl",
		"fail2ban",
		"git",
		"gnupg",
		"jq",
		"rsync",
		"sudo",
		"ufw",
		"unattended-upgrades",
		"unzip",
		"wget",
	}
}

func ubuntuCodename(apply host.Apply) (string, error) {
	file, err := apply.Runner.ReadFile(apply.ContextOrBackground(), "/etc/os-release")
	if err != nil {
		if errors.Is(err, host.ErrNotExist) {
			return "noble", nil
		}
		return "", err
	}
	if codename := osReleaseValue(file.Content, "VERSION_CODENAME"); codename != "" {
		return codename, nil
	}
	if codename := osReleaseValue(file.Content, "UBUNTU_CODENAME"); codename != "" {
		return codename, nil
	}
	return "noble", nil
}

func osReleaseValue(content []byte, key string) string {
	for _, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || name != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

func debArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return goarch
	}
}

func litestreamArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "arm64"
	default:
		return ""
	}
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "host"
	}
	return name
}

func cloudflaredUnit(execStart string) string {
	return `[Unit]
Description=Cloudflare Tunnel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=cloudflared
Group=cloudflared
ExecStart=` + execStart + `
Restart=on-failure
RestartSec=5s
TimeoutStartSec=0
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
`
}

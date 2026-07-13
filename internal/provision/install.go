package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/provision/host"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/version"
)

const (
	defaultOperatorUser = "operator"
	defaultDeployUser   = "deploy"
	defaultTimezone     = "UTC"
	defaultLocale       = "en_US.UTF-8"
)

type InstallOptions struct {
	OperatorSSHPublicKeys []string
	DeploySSHPublicKeys   []string
	CheckMode             bool
	StateRoot             string
	HelperBinaryPath      string
	ApplyID               string
	Now                   func() time.Time
}

type InstallSummary struct {
	ApplyID           string
	OperationsChanged int
	DeployKeyResults  []memberkeys.AddResult
}

// VersionConvergeOptions contains the narrow, version-owned portion of host
// provisioning used by `ship box update`. It intentionally excludes day-zero
// concerns such as packages, users, network policy, and provider setup.
type VersionConvergeOptions struct {
	StateRoot        string
	HelperBinaryPath string
	Now              func() time.Time
}

// RunVersionConverge installs the current helper and reapplies the generated
// helper sudoers, forced agent-shell keys, and ship-owned timer units.
func RunVersionConverge(ctx context.Context, runner host.Runner, opts VersionConvergeOptions) (InstallSummary, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	stateStore := store.Store{Root: opts.StateRoot}
	hostFile, err := stateStore.ReadHost()
	if err != nil {
		return InstallSummary{}, err
	}
	apply := host.Apply{Context: ctx, Runner: runner, State: &host.RunState{}}
	startedAt := opts.Now().UTC()
	summary := InstallSummary{ApplyID: startedAt.Format("20060102T150405Z")}
	var ops []operation
	installOpts := InstallOptions{HelperBinaryPath: opts.HelperBinaryPath}
	addHelper(&ops, installOpts)
	addDeployMembersStore(&ops, stateStore, defaultDeployUser, &summary)
	addPreviewReaper(&ops)
	addDoctorTimer(&ops)

	for _, op := range ops {
		changed, err := op.run(apply)
		if err != nil {
			return summary, fmt.Errorf("%s: %w", op.name, err)
		}
		if changed {
			summary.OperationsChanged++
		}
	}
	meta := hostFile.Meta
	meta.ShipVersion = version.Version
	meta.LastApply = &store.ApplyMeta{
		ID:                summary.ApplyID,
		StartedAt:         startedAt.Format(time.RFC3339),
		FinishedAt:        opts.Now().UTC().Format(time.RFC3339),
		Status:            "ok",
		OperationsChanged: summary.OperationsChanged,
	}
	if err := stateStore.WriteHostState(hostFile.Observed, meta); err != nil {
		return summary, err
	}
	return summary, nil
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

	summary := InstallSummary{ApplyID: applyID}
	ops := installOperations(opts, stateStore, &summary)
	changedCount := 0
	for _, op := range ops {
		changed, err := op.run(apply)
		if err != nil {
			if !opts.CheckMode {
				_ = writeApplyState(stateStore, opts, applyID, startedAt, opts.Now().UTC(), "failed", changedCount)
			}
			summary.OperationsChanged = changedCount
			return summary, fmt.Errorf("%s: %w", op.name, err)
		}
		if changed {
			changedCount++
		}
	}

	if !opts.CheckMode {
		if err := writeApplyState(stateStore, opts, applyID, startedAt, opts.Now().UTC(), "ok", changedCount); err != nil {
			summary.OperationsChanged = changedCount
			return summary, err
		}
	}
	summary.OperationsChanged = changedCount
	return summary, nil
}

func installOperations(opts InstallOptions, stateStore store.Store, summary *InstallSummary) []operation {
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
		{Path: "/etc/ship", Owner: "root", Group: "root", Mode: 0755},
		{Path: "/etc/ship/secrets", Owner: "root", Group: "root", Mode: 0700},
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
		return host.EnsureUser(apply, host.User{Name: defaultOperatorUser, PrimaryGroup: defaultOperatorUser, Shell: "/bin/bash", CreateHome: true})
	})
	add("operator sudo group", func(apply host.Apply) (bool, error) {
		return host.EnsureGroupMembership(apply, defaultOperatorUser, "sudo")
	})
	add("operator sudoers", func(apply host.Apply) (bool, error) {
		return host.EnsureSudoersFile(apply, "operator", []byte(fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", defaultOperatorUser)))
	})
	add("ensure deploy user", func(apply host.Apply) (bool, error) {
		return host.EnsureUser(apply, host.User{Name: defaultDeployUser, PrimaryGroup: defaultDeployUser, Shell: "/bin/bash", CreateHome: true})
	})
	addAuthorizedKeys(&ops, defaultOperatorUser, opts.OperatorSSHPublicKeys, nil)
	addAuthorizedKeys(&ops, defaultDeployUser, opts.DeploySSHPublicKeys, func(results []memberkeys.AddResult) {
		summary.DeployKeyResults = results
	})
	addDeployMembersStore(&ops, stateStore, defaultDeployUser, summary)

	add("timezone", func(apply host.Apply) (bool, error) {
		return host.EnsureTimezone(apply, defaultTimezone)
	})
	add("locale", func(apply host.Apply) (bool, error) {
		return host.EnsureLocale(apply, defaultLocale)
	})
	addSSHHardening(&ops)
	addSecurity(&ops, opts)
	addHelper(&ops, opts)
	addPreviewReaper(&ops)
	addDoctorTimer(&ops)
	addPodman(&ops)
	addPodmanHostBaseline(&ops)
	addDeployTmpDir(&ops)
	addCaddy(&ops, opts)
	return ops
}

func addAuthorizedKeys(ops *[]operation, user string, keyLines []string, capture func([]memberkeys.AddResult)) {
	dir := fmt.Sprintf("/home/%s/.ssh", user)
	*ops = append(*ops, operation{name: "ssh directory " + user, run: func(apply host.Apply) (bool, error) {
		return host.EnsureDirectory(apply, host.Directory{Path: dir, Owner: user, Group: user, Mode: 0700})
	}})
	*ops = append(*ops, operation{name: "authorized keys " + user, run: func(apply host.Apply) (bool, error) {
		var keys []memberkeys.AuthorizedKey
		if len(keyLines) > 0 {
			var err error
			keys, err = memberkeys.Normalize(strings.Join(keyLines, "\n"), "")
			if err != nil {
				return false, err
			}
		}
		current, err := apply.Runner.ReadFile(apply.ContextOrBackground(), filepath.Join(dir, "authorized_keys"))
		if err != nil && !errors.Is(err, host.ErrNotExist) {
			return false, err
		}
		existing := memberkeys.Parse(current.Content)
		lines, results := memberkeys.Merge(existing, keys)
		if capture != nil {
			capture(results)
		}
		return host.EnsureFile(apply, host.File{
			Path:    filepath.Join(dir, "authorized_keys"),
			Content: memberkeys.Content(lines),
			Owner:   user,
			Group:   user,
			Mode:    0600,
		})
	}})
}

func addDeployMembersStore(ops *[]operation, stateStore store.Store, deployUser string, summary *InstallSummary) {
	path := filepath.Join("/home", deployUser, ".ssh", "authorized_keys")
	*ops = append(*ops, operation{name: "members store", run: func(apply host.Apply) (bool, error) {
		current, err := apply.Runner.ReadFile(apply.ContextOrBackground(), path)
		if err != nil && !errors.Is(err, host.ErrNotExist) {
			return false, err
		}
		keys := memberkeys.Parse(current.Content)
		members, err := stateStore.ReadMembers()
		if err != nil {
			return false, err
		}
		overrides := setupMemberRoleOverrides(keys, summary.DeployKeyResults)
		next := memberkeys.ReconciledMembersFile(keys, *members, overrides)
		for i := range summary.DeployKeyResults {
			fingerprint := summary.DeployKeyResults[i].Key.Fingerprint
			if record, ok := next.Members[fingerprint]; ok {
				summary.DeployKeyResults[i].Key.Comment = record.Name
				summary.DeployKeyResults[i].Role = string(record.Role)
			}
		}
		if reflect.DeepEqual(*members, next) {
			return false, nil
		}
		if apply.CheckMode {
			return true, nil
		}
		return true, stateStore.WriteMembers(next)
	}})
}

func setupMemberRoleOverrides(keys []memberkeys.AuthorizedKey, results []memberkeys.AddResult) map[string]store.MemberRecord {
	parseableCount := 0
	for _, key := range keys {
		if key.Material != "" {
			parseableCount++
		}
	}
	addedCount := 0
	for _, result := range results {
		if result.Added {
			addedCount++
		}
	}
	role := store.MemberRoleShipper
	if addedCount > 0 && parseableCount == addedCount {
		role = store.MemberRoleOwner
	}

	overrides := map[string]store.MemberRecord{}
	for _, result := range results {
		if !result.Added {
			continue
		}
		overrides[result.Key.Fingerprint] = store.MemberRecord{
			Name: result.Key.Comment,
			Role: role,
		}
	}
	return overrides
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
		{Rule: "allow 80/tcp"},
		{Rule: "allow 443/tcp"},
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
		return true, host.RequireZero(result, "ufw", []string{"--force", "enable"})
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
	*ops = append(*ops, operation{name: "install ship helper", run: func(apply host.Apply) (bool, error) {
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
		return host.EnsureFile(apply, host.File{Path: "/usr/local/bin/ship", Content: data, Owner: "root", Group: "root", Mode: 0755})
	}})
	*ops = append(*ops, operation{name: "ship sudoers", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSudoersFile(apply, "ship", []byte(fmt.Sprintf("%s ALL=(root) NOPASSWD: /usr/local/bin/ship server app *, /usr/local/bin/ship server doctor, /usr/local/bin/ship server doctor *, /usr/local/bin/ship server key *, /usr/local/bin/ship server approval *, /usr/local/bin/ship server config *, /usr/local/bin/ship server notify *, /usr/local/bin/ship server version, /usr/local/bin/ship server version *, /usr/local/bin/ship server update *\n", defaultDeployUser)))
	}})
}

func addPreviewReaper(ops *[]operation) {
	*ops = append(*ops, operation{name: "preview reaper service", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSystemdUnit(apply, host.SystemdUnit{
			Name:    "ship-preview-reaper.service",
			Content: []byte(previewReaperServiceUnit()),
		})
	}})
	*ops = append(*ops, operation{name: "preview reaper timer", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSystemdUnit(apply, host.SystemdUnit{
			Name:    "ship-preview-reaper.timer",
			Content: []byte(previewReaperTimerUnit()),
			Action:  host.Started,
		})
	}})
	*ops = append(*ops, operation{name: "preview reaper timer enabled", run: func(apply host.Apply) (bool, error) {
		return ensureSystemdUnitEnabled(apply, "ship-preview-reaper.timer")
	}})
}

func ensureSystemdUnitEnabled(apply host.Apply, unit string) (bool, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "systemctl", Args: []string{"is-enabled", "--quiet", unit}})
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	enable, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "systemctl", Args: []string{"enable", unit}})
	if err != nil {
		return false, err
	}
	return true, host.RequireZero(enable, "systemctl", []string{"enable", unit})
}

func previewReaperServiceUnit() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=ship preview environment reaper",
		"",
		"[Service]",
		"Type=oneshot",
		"ExecStart=/usr/local/bin/ship server env reap",
		"",
	}, "\n")
}

func previewReaperTimerUnit() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=Run ship preview environment reaper",
		"",
		"[Timer]",
		"OnBootSec=15min",
		"OnUnitActiveSec=1h",
		"Persistent=true",
		"",
		"[Install]",
		"WantedBy=timers.target",
		"",
	}, "\n")
}

func addDoctorTimer(ops *[]operation) {
	*ops = append(*ops, operation{name: "doctor record service", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSystemdUnit(apply, host.SystemdUnit{
			Name:    "ship-doctor.service",
			Content: []byte(doctorServiceUnit()),
		})
	}})
	*ops = append(*ops, operation{name: "doctor record timer", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSystemdUnit(apply, host.SystemdUnit{
			Name:    "ship-doctor.timer",
			Content: []byte(doctorTimerUnit()),
			Action:  host.Started,
		})
	}})
	*ops = append(*ops, operation{name: "doctor record timer enabled", run: func(apply host.Apply) (bool, error) {
		return ensureSystemdUnitEnabled(apply, "ship-doctor.timer")
	}})
}

func doctorServiceUnit() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=ship daily doctor state recorder",
		"",
		"[Service]",
		"Type=oneshot",
		"ExecStart=/usr/local/bin/ship server doctor record",
		"",
	}, "\n")
}

func doctorTimerUnit() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=Run ship doctor state recorder",
		"",
		"[Timer]",
		"OnBootSec=30min",
		"OnUnitActiveSec=24h",
		"Persistent=true",
		"",
		"[Install]",
		"WantedBy=timers.target",
		"",
	}, "\n")
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

// addDeployTmpDir creates /tmp/ship-deploy with mode 1777 (sticky
// world-writable) so the unprivileged deploy user can drop the source
// tarball and manifest under it during `ship deploy`, while still
// preventing other local users from deleting another user's files mid-
// deploy. The helper's `server app apply` reads from this directory via
// systemd.ValidateDeployTmpSource, which also enforces ownership via
// SUDO_UID.
func addDeployTmpDir(ops *[]operation) {
	*ops = append(*ops, operation{name: "deploy tmp dir", run: ensureDeployTmpDir})
}

// 1777 = sticky + world-writable. EnsureDirectory's mode argument is
// stripped to .Perm() (low 9 bits) so it can't express the sticky bit;
// roll our own stat-then-install for this one path.
func ensureDeployTmpDir(apply host.Apply) (bool, error) {
	const path = "/tmp/ship-deploy"
	const wantMode = "1777"
	probe, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "stat", Args: []string{"-c", "%U\t%G\t%a\t%F", path}})
	if err != nil {
		return false, err
	}
	fields := strings.Split(strings.TrimSpace(string(probe.Stdout)), "\t")
	if probe.ExitCode == 0 && len(fields) == 4 &&
		fields[0] == "root" && fields[1] == "root" && fields[2] == wantMode && fields[3] == "directory" {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	args := []string{"-d", "-o", "root", "-g", "root", "-m", wantMode, path}
	res, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "install", Args: args})
	if err != nil {
		return false, err
	}
	return true, host.RequireZero(res, "install", args)
}

// addPodmanHostBaseline writes the host config that makes Podman bridge
// networking actually work on Ubuntu's default-deny UFW posture, and
// makes short image names (`FROM nginx:alpine`) resolve. Both surfaced
// during real-box smoke; the fake-VPS fixture couldn't catch them
// because it doesn't run real podman or real ufw.
//
// Scope is deliberately narrow:
//
//   - Allow input + forward on `podman+` interfaces only. No public
//     interface is touched. Public posture (22/80/443 per install
//     mode) is unchanged.
//   - Flip DEFAULT_FORWARD_POLICY from DROP to ACCEPT so the kernel
//     forwards between Podman bridges. Same scope: only matters for
//     traffic the kernel would otherwise drop on FORWARD, which
//     today is bridge-internal Podman traffic.
//   - Configure unqualified-search-registries=docker.io so user
//     Dockerfiles don't have to fully qualify every image.
//
// All three are idempotent: the UFW block is delimited by BEGIN/END
// markers so unrelated user edits to before.rules survive; the
// default/ufw line is regex-targeted; the registries file is a
// dedicated drop-in under /etc/containers/registries.conf.d/ so we
// never touch the main file.
func addPodmanHostBaseline(ops *[]operation) {
	*ops = append(*ops, operation{name: "podman ufw rules", run: ensurePodmanUfwRules})
	*ops = append(*ops, operation{name: "podman unqualified registries", run: ensurePodmanRegistries})
}

const podmanUfwMarker = "ship podman bridges"

func podmanUfwBlock() string {
	return strings.Join([]string{
		"# Allow Podman bridge interfaces (podman0/podman1/...) to reach",
		"# the host's bridge gateway for aardvark-dns and to forward",
		"# between containers on the same bridge. Scope is bridge-internal",
		"# only; public ingress is unchanged. See ADR-0006 Cut 2 and",
		"# docs/security-model.md.",
		"-A ufw-before-input -i podman+ -j ACCEPT",
		"-A ufw-before-forward -i podman+ -j ACCEPT",
		"-A ufw-before-forward -o podman+ -j ACCEPT",
	}, "\n")
}

func ensurePodmanUfwRules(apply host.Apply) (bool, error) {
	rulesChanged, err := ensurePodmanUfwBeforeRules(apply)
	if err != nil {
		return false, err
	}
	policyChanged, err := host.EnsureLineInFile(apply, host.LineInFile{
		Path:   "/etc/default/ufw",
		Regexp: `^DEFAULT_FORWARD_POLICY=`,
		Line:   `DEFAULT_FORWARD_POLICY="ACCEPT"`,
		Owner:  "root",
		Group:  "root",
		Mode:   0644,
	})
	if err != nil {
		return false, err
	}
	if !rulesChanged && !policyChanged {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	// Reload UFW so the in-kernel rules pick up our edits. If UFW
	// isn't active yet (first install runs this before
	// addSecurity's `ufw --force enable`), `ufw reload` no-ops
	// cleanly. Either way, the edits are on disk for the next
	// `ufw enable`/reload.
	result, err := apply.Runner.Run(apply.ContextOrBackground(), host.Command{Program: "ufw", Args: []string{"reload"}})
	if err != nil {
		return false, err
	}
	// `ufw reload` exits non-zero with "Firewall not enabled" before
	// `ufw enable` runs. That's expected on first install — don't
	// surface it.
	if result.ExitCode != 0 && !strings.Contains(string(result.Stdout)+string(result.Stderr), "not enabled") {
		return false, fmt.Errorf("ufw reload: exit %d: %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	return true, nil
}

// ensurePodmanUfwBeforeRules splices our marked block into
// /etc/ufw/before.rules just after the `# End required lines` anchor
// that every default Ubuntu file ships with. If the markers already
// exist, the block is replaced in place. Unrelated lines are left
// untouched. If the anchor is missing (user heavily rewrote the file),
// we refuse to guess at a safe insertion point and error out so the
// operator can decide.
func ensurePodmanUfwBeforeRules(apply host.Apply) (bool, error) {
	const path = "/etc/ufw/before.rules"
	current, err := apply.Runner.ReadFile(apply.ContextOrBackground(), path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	next, changed, err := injectPodmanUfwBlock(string(current.Content), podmanUfwBlock())
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	return host.EnsureFile(apply, host.File{
		Path:    path,
		Content: []byte(next),
		Owner:   "root",
		Group:   "root",
		Mode:    0640,
	})
}

// injectPodmanUfwBlock is the pure-function core of
// ensurePodmanUfwBeforeRules. Exported as a free function for unit
// testing without a fake runner.
func injectPodmanUfwBlock(text string, body string) (string, bool, error) {
	begin := "# BEGIN " + podmanUfwMarker
	end := "# END " + podmanUfwMarker
	desired := begin + "\n" + strings.TrimRight(body, "\n") + "\n" + end

	// Replace-in-place path: both markers present and well-ordered.
	startIdx := strings.Index(text, begin)
	endIdx := strings.Index(text, end)
	if startIdx >= 0 && endIdx > startIdx {
		// Extend endIdx past the marker line itself.
		endIdx += len(end)
		next := text[:startIdx] + desired + text[endIdx:]
		return next, next != text, nil
	}
	// Inconsistent: one marker without the other → refuse.
	if (startIdx < 0) != (endIdx < 0) {
		return "", false, fmt.Errorf("/etc/ufw/before.rules has one of `# BEGIN/END %s` but not both; resolve manually", podmanUfwMarker)
	}
	// Fresh insert: must land inside the *filter table block, after
	// the chain declarations. Ubuntu's default file marks the boundary
	// with `# End required lines`.
	const anchor = "# End required lines"
	anchorIdx := strings.Index(text, anchor)
	if anchorIdx < 0 {
		return "", false, fmt.Errorf("/etc/ufw/before.rules is missing the `%s` anchor; cannot safely insert the ship podman block", anchor)
	}
	// Insert after the line containing the anchor.
	lineEnd := strings.Index(text[anchorIdx:], "\n")
	if lineEnd < 0 {
		return "", false, fmt.Errorf("/etc/ufw/before.rules ends mid-line at the `%s` anchor", anchor)
	}
	insertAt := anchorIdx + lineEnd + 1
	next := text[:insertAt] + "\n" + desired + "\n" + text[insertAt:]
	return next, true, nil
}

func ensurePodmanRegistries(apply host.Apply) (bool, error) {
	// Drop-in under /etc/containers/registries.conf.d/ so we never
	// touch the distro-shipped /etc/containers/registries.conf.
	body := strings.Join([]string{
		"# Managed by ship. Lets `FROM nginx:alpine` and similar",
		"# short image names resolve via docker.io. To pull from another",
		"# registry, fully qualify the image in your Dockerfile.",
		`unqualified-search-registries = ["docker.io"]`,
		"",
	}, "\n")
	return host.EnsureFile(apply, host.File{
		Path:    "/etc/containers/registries.conf.d/00-ship.conf",
		Content: []byte(body),
		Owner:   "root",
		Group:   "root",
		Mode:    0644,
	})
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
	if err := host.RequireZero(create, "podman", []string{"network", "create", "ingress"}); err != nil {
		return false, err
	}
	return true, nil
}

// addCaddy installs and starts Caddy as a Podman container on the
// shared `ingress` network, per ADR-0006 Cut 2. The previous apt-based
// install + systemd-from-apt path is gone: ship no longer treats
// Caddy as a host service. App containers join `ingress` and Caddy
// reaches them by container DNS.
//
// Ordering matters: the Caddyfile is written before `caddy.service`
// starts. The Caddy container's ExecStart is `caddy run --config
// /etc/caddy/Caddyfile`; a missing file makes the container exit 1
// and systemd loops through Restart=on-failure until "start request
// repeated too quickly" kills the service. We learned that the hard
// way on real-box smoke.
func addCaddy(ops *[]operation, opts InstallOptions) {
	appsRoot := identity.AppsRoot()
	for _, dir := range []host.Directory{
		{Path: "/etc/caddy", Owner: "root", Group: "root", Mode: 0755},
		{Path: "/etc/caddy/conf.d", Owner: "root", Group: "root", Mode: 0755},
		// Caddy's runtime data (certificates, last_config.json, etc.)
		// lives outside /etc so config edits stay clean to diff.
		{Path: "/var/lib/caddy", Owner: "root", Group: "root", Mode: 0755},
		// Caddy bind-mounts the app root read-only so static routes can serve
		// host-side releases. Podman refuses to start if the host source is
		// missing, even when no app has been deployed yet.
		{Path: appsRoot, Owner: "root", Group: "root", Mode: 0755},
	} {
		dir := dir
		*ops = append(*ops, operation{name: "caddy dir " + dir.Path, run: func(apply host.Apply) (bool, error) { return host.EnsureDirectory(apply, dir) }})
	}
	*ops = append(*ops, operation{name: "caddyfile", run: func(apply host.Apply) (bool, error) {
		return host.EnsureFile(apply, host.File{
			Path:    "/etc/caddy/Caddyfile",
			Content: []byte(caddyMainFile()),
			Owner:   "root",
			Group:   "root",
			Mode:    0644,
		})
	}})
	*ops = append(*ops, operation{name: "caddy service", run: func(apply host.Apply) (bool, error) {
		return host.EnsureSystemdUnit(apply, host.SystemdUnit{
			Name:    "caddy.service",
			Content: []byte(caddyUnit()),
			Action:  host.Started,
		})
	}})
}

// caddyMainFile is the bootstrap /etc/caddy/Caddyfile. It only imports
// the conf.d/ fragments that `server app apply` writes per-(app, env).
// On a fresh host the import matches nothing, which Caddy treats as an
// empty config — the container stays up and accepts reloads as
// fragments land.
func caddyMainFile() string {
	return "# Managed by ship. Per-app routes live in conf.d/*.caddy.\n\nimport conf.d/*.caddy\n"
}

// caddyUnit returns the systemd unit content that runs Caddy as a
// Podman container on the shared `ingress` network. It publishes public
// HTTP and HTTPS for every box.
func caddyUnit() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=Caddy (ship managed, podman)",
		"Wants=network-online.target",
		"After=network-online.target",
		"",
		"[Service]",
		"Type=simple",
		"TimeoutStartSec=0",
		"ExecStartPre=-/usr/bin/podman stop caddy",
		"ExecStartPre=-/usr/bin/podman rm caddy",
		"ExecStart=/usr/bin/podman run --rm --name caddy" +
			" --network ingress" +
			" --publish 80:80 --publish 443:443" +
			" -v /etc/caddy:/etc/caddy:Z" +
			" -v /var/lib/caddy:/data:Z" +
			" -v " + identity.AppsRoot() + ":/var/apps:ro,Z" +
			" docker.io/library/caddy:2-alpine",
		"ExecStop=/usr/bin/podman stop caddy",
		"Restart=on-failure",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n")
}

func desiredHost(InstallOptions) store.HostDesired {
	packages := map[string]store.DesiredPackage{
		"podman":  {Source: "ubuntu", Track: "noble"},
		"sqlite3": {Source: "ubuntu", Track: "noble"},
	}
	return store.HostDesired{
		Users:   store.HostUsers{Operator: defaultOperatorUser, Deploy: defaultDeployUser},
		Ingress: store.HostIngressDesired{Expose: store.ExposePublic},
		Features: store.HostFeatures{
			Docker: false,
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

func writeApplyState(stateStore store.Store, _ InstallOptions, applyID string, startedAt time.Time, finishedAt time.Time, status string, changed int) error {
	return stateStore.WriteHostState(store.HostObserved{
		Packages: map[string]store.ObservedPackage{},
		Ingress:  store.HostIngressObserved{},
	}, store.HostMeta{
		ShipVersion: version.Version,
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
		"sqlite3",
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

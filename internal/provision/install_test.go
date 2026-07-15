package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/provision/host"
	"github.com/fprl/ship/internal/store"
)

const (
	operatorTestPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/ operator"
	deployTestPublicKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICtppnbbz76teU3iU6BguTmo//WITtYN35e4gSER6UNt deploy"
)

func TestRunInstallWritesHonestChangedCount(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}

	runner := &installFakeRunner{files: map[string]host.FileState{}}
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	summary, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		ClientAddress:         "203.0.113.7",
		StateRoot:             root,
		HelperBinaryPath:      helper,
		ApplyID:               "apply-test",
		Now:                   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ApplyID != "apply-test" {
		t.Fatalf("unexpected apply id: %s", summary.ApplyID)
	}
	if summary.OperationsChanged == 0 {
		t.Fatal("expected install to report changed operations")
	}
	if len(summary.DeployKeyResults) != 1 || !summary.DeployKeyResults[0].Added || summary.DeployKeyResults[0].Role != "owner" {
		t.Fatalf("deploy enrollment results = %+v, want one added owner key", summary.DeployKeyResults)
	}

	loaded, err := (store.Store{Root: root}).ReadHost()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.LastApply == nil {
		t.Fatal("expected last_apply metadata")
	}
	if loaded.Meta.LastApply.OperationsChanged != summary.OperationsChanged {
		t.Fatalf("metadata count %d did not match summary count %d", loaded.Meta.LastApply.OperationsChanged, summary.OperationsChanged)
	}
	if loaded.Meta.LastApply.Status != "ok" {
		t.Fatalf("unexpected apply status: %s", loaded.Meta.LastApply.Status)
	}
	if loaded.Meta.ClientAddress != "203.0.113.7" {
		t.Fatalf("client address = %q, want 203.0.113.7", loaded.Meta.ClientAddress)
	}
	if _, ok := runner.files["/etc/systemd/system/ssh.service"]; ok {
		t.Fatal("install must not overwrite the packaged ssh.service unit")
	}
	if !runner.ranCommand("install", "-d -o root -g root -m 700 /etc/ship/secrets") {
		t.Fatal("expected /etc/ship/secrets to be created mode 0700")
	}
	members, err := (store.Store{Root: root}).ReadMembers()
	if err != nil {
		t.Fatal(err)
	}
	member := members.Members[summary.DeployKeyResults[0].Key.Fingerprint]
	if member.Name != "deploy" || member.Role != store.MemberRoleOwner {
		t.Fatalf("setup member record = %+v, want deploy owner", member)
	}
}

func TestSetupMemberRoleOverridesUsesExplicitGitDerivedMemberName(t *testing.T) {
	keys, err := memberkeys.Normalize(deployTestPublicKey, "")
	if err != nil {
		t.Fatal(err)
	}
	overrides := setupMemberRoleOverrides(keys, []memberkeys.AddResult{{Key: keys[0], Added: true}}, "Franco Pablo")
	got := overrides[keys[0].Fingerprint]
	if got.Name != "Franco Pablo" || got.Role != store.MemberRoleOwner {
		t.Fatalf("setup override = %+v, want explicit owner name", got)
	}
}

func TestRunVersionConvergeRendersAuthorizedKeysFromMembersStore(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	deployKeys, err := memberkeys.Normalize(deployTestPublicKey, "")
	if err != nil {
		t.Fatal(err)
	}
	deployFingerprint := deployKeys[0].Fingerprint
	stateStore := store.Store{Root: root}
	if err := stateStore.WriteHostDesired(store.HostDesired{
		Users:    store.HostUsers{Operator: "operator", Deploy: "deploy"},
		Ingress:  store.HostIngressDesired{Expose: store.ExposePublic},
		Packages: map[string]store.DesiredPackage{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := stateStore.WriteHostState(store.HostObserved{Packages: map[string]store.ObservedPackage{}}, store.HostMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := stateStore.WriteMembers(store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			deployFingerprint: {Name: "Franco Pablo", Role: store.MemberRoleAgent},
		},
	}); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{
		"/home/deploy/.ssh/authorized_keys": {
			Content: []byte(deployTestPublicKey + "\n" + operatorTestPublicKey + "\n"),
			Owner:   "deploy",
			Group:   "deploy",
			Mode:    0600,
		},
	}}

	_, err = RunVersionConverge(context.Background(), runner, VersionConvergeOptions{
		StateRoot:        root,
		HelperBinaryPath: helper,
		Now:              func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKey, err := memberkeys.Normalize(deployTestPublicKey, "Franco Pablo")
	if err != nil {
		t.Fatal(err)
	}
	want := memberkeys.RenderAuthorizedKeyLine(wantKey[0], store.MemberRecord{Name: "Franco Pablo", Role: store.MemberRoleAgent}) + "\n"
	got := string(runner.files["/home/deploy/.ssh/authorized_keys"].Content)
	if got != want {
		t.Fatalf("converged authorized_keys = %q, want %q", got, want)
	}
	members, err := stateStore.ReadMembers()
	if err != nil {
		t.Fatal(err)
	}
	if len(members.Members) != 1 || members.Members[deployFingerprint].Name != "Franco Pablo" {
		t.Fatalf("converged members = %+v, want only recorded member", members.Members)
	}
}

func TestRunInstallDoesNotRestartSSHWhenConfigAlreadyConverged(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{
		"/etc/ssh/sshd_config": {
			Content: []byte(strings.Join([]string{
				"PermitRootLogin prohibit-password",
				"PasswordAuthentication no",
				"PubkeyAuthentication yes",
				"X11Forwarding no",
				"MaxAuthTries 3",
				"",
			}, "\n")),
			Owner: "root",
			Group: "root",
			Mode:  0644,
		},
	}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if command.Program == "systemctl" && strings.Join(command.Args, " ") == "restart ssh.service" {
			t.Fatalf("ssh restart should be gated on sshd_config drift, commands: %+v", runner.commands)
		}
	}
}

func TestRunInstallEnrollsAuthorizedKeysWithoutReplacingExistingMembers(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	existingDeploy := strings.Replace(operatorTestPublicKey, " operator", " teammate", 1)
	existingOperator := strings.Replace(deployTestPublicKey, " deploy", " bootstrap-operator", 1)
	runner := &installFakeRunner{files: map[string]host.FileState{
		"/home/deploy/.ssh/authorized_keys": {
			Content: []byte(existingDeploy + "\n"),
			Owner:   "deploy",
			Group:   "deploy",
			Mode:    0600,
		},
		"/home/operator/.ssh/authorized_keys": {
			Content: []byte(existingOperator + "\n"),
			Owner:   "operator",
			Group:   "operator",
			Mode:    0600,
		},
	}}

	opts := InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
		ApplyID:               "enroll-test",
	}
	summary, err := RunInstall(context.Background(), runner, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.DeployKeyResults) != 1 || !summary.DeployKeyResults[0].Added || summary.DeployKeyResults[0].Role != "shipper" {
		t.Fatalf("deploy enrollment results = %+v, want one added shipper key", summary.DeployKeyResults)
	}
	deployKeys := string(runner.files["/home/deploy/.ssh/authorized_keys"].Content)
	assertAuthorizedKeysLines(t, deployKeys, []string{existingDeploy, deployTestPublicKey})
	operatorKeys := string(runner.files["/home/operator/.ssh/authorized_keys"].Content)
	assertAuthorizedKeysLines(t, operatorKeys, []string{existingOperator, operatorTestPublicKey})

	summary, err = RunInstall(context.Background(), runner, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.DeployKeyResults) != 1 || summary.DeployKeyResults[0].Added || summary.DeployKeyResults[0].Role != "shipper" {
		t.Fatalf("second deploy enrollment results = %+v, want already authorized shipper", summary.DeployKeyResults)
	}
	assertAuthorizedKeysLines(t, string(runner.files["/home/deploy/.ssh/authorized_keys"].Content), []string{existingDeploy, deployTestPublicKey})
	assertAuthorizedKeysLines(t, string(runner.files["/home/operator/.ssh/authorized_keys"].Content), []string{existingOperator, operatorTestPublicKey})
}

// --- Podman provisioner coverage (ADR-0005 cutover items 23, 24; ADR-0006 Cut 2) ---

func TestRunInstallInstallsPodmanFromUbuntuUniverse(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !runner.ranCommand("apt-get", "install -y podman") {
		t.Fatalf("expected podman to be installed via apt-get, commands: %+v", runner.commands)
	}
	if !runner.ranCommand("apt-get", "install -y sqlite3") {
		t.Fatalf("expected sqlite3 to be installed via apt-get, commands: %+v", runner.commands)
	}

	loaded, err := (store.Store{Root: root}).ReadHost()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Desired.Packages["podman"]
	if !ok {
		t.Fatalf("expected podman in desired packages, got %+v", loaded.Desired.Packages)
	}
	if got.Source != "ubuntu" {
		t.Fatalf("expected podman source=ubuntu, got %+v", got)
	}
	if got, ok := loaded.Desired.Packages["sqlite3"]; !ok || got.Source != "ubuntu" {
		t.Fatalf("expected sqlite3 in desired packages, got %+v", loaded.Desired.Packages)
	}
	if _, ok := loaded.Desired.Packages["caddy"]; ok {
		t.Fatalf("caddy is a podman service, not a desired package: %+v", loaded.Desired.Packages)
	}
}

func TestRunInstallCreatesIngressNetworkWhenAbsent(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{
		files: map[string]host.FileState{},
		commandResults: map[string]host.CommandResult{
			"podman network exists ingress": {ExitCode: 1},
		},
	}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !runner.ranCommand("podman", "network create ingress") {
		t.Fatalf("expected ingress network to be created, commands: %+v", runner.commands)
	}
}

func TestRunInstallCreatesDeployTmpDirWithStickyMode(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Mode 1777 = sticky world-writable. The deploy user needs to drop
	// files there, but other local users must not delete them mid-deploy.
	if !runner.ranCommand("install", "-d -o root -g root -m 1777 /tmp/ship-deploy") {
		t.Fatalf("expected /tmp/ship-deploy to be created with mode 1777, commands: %+v", runner.commands)
	}
}

func TestRunInstallWritesCaddyContainerSystemdUnit(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	unit, ok := runner.files["/etc/systemd/system/caddy.service"]
	if !ok {
		t.Fatal("expected caddy.service unit to be installed")
	}
	if !runner.ranCommand("install", "-d -o root -g root -m 755 /var/apps") {
		t.Fatal("expected /var/apps to be created before caddy.service starts")
	}
	content := string(unit.Content)
	for _, want := range []string{
		"podman run --rm --name caddy",
		"--network ingress",
		"--publish 80:80",
		"--publish 443:443",
		"-v /etc/caddy:/etc/caddy:Z",
		"docker.io/library/caddy:2-alpine",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("caddy.service missing %q\nunit:\n%s", want, content)
		}
	}
}

func TestRunInstallWritesPreviewReaperAndDoctorTimers(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	service, ok := runner.files["/etc/systemd/system/ship-preview-reaper.service"]
	if !ok {
		t.Fatal("expected preview reaper service")
	}
	if !strings.Contains(string(service.Content), "ExecStart=/usr/local/bin/ship server env reap") {
		t.Fatalf("unexpected reaper service:\n%s", service.Content)
	}
	timer, ok := runner.files["/etc/systemd/system/ship-preview-reaper.timer"]
	if !ok {
		t.Fatal("expected preview reaper timer")
	}
	timerContent := string(timer.Content)
	for _, want := range []string{"OnBootSec=15min", "OnUnitActiveSec=1h", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(timerContent, want) {
			t.Fatalf("timer missing %q:\n%s", want, timerContent)
		}
	}

	doctorService, ok := runner.files["/etc/systemd/system/ship-doctor.service"]
	if !ok {
		t.Fatal("expected doctor service")
	}
	if !strings.Contains(string(doctorService.Content), "ExecStart=/usr/local/bin/ship server doctor record") {
		t.Fatalf("unexpected doctor service:\n%s", doctorService.Content)
	}
	doctorTimer, ok := runner.files["/etc/systemd/system/ship-doctor.timer"]
	if !ok {
		t.Fatal("expected doctor timer")
	}
	doctorTimerContent := string(doctorTimer.Content)
	for _, want := range []string{"OnBootSec=30min", "OnUnitActiveSec=24h", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(doctorTimerContent, want) {
			t.Fatalf("doctor timer missing %q:\n%s", want, doctorTimerContent)
		}
	}
}

const ubuntuBeforeRules = `#
# rules.before
#
# Rules that should be run before the ufw command line added rules. Custom
# rules should be added to one of these chains:
#   ufw-before-input
#   ufw-before-output
#   ufw-before-forward
#

# Don't delete these required lines, otherwise there will be errors
*filter
:ufw-before-input - [0:0]
:ufw-before-output - [0:0]
:ufw-before-forward - [0:0]
:ufw-not-local - [0:0]
# End required lines

# allow all on loopback
-A ufw-before-input -i lo -j ACCEPT
-A ufw-before-output -o lo -j ACCEPT

COMMIT
`

func TestEnsureSystemdUnitEnabledRunsEnableWhenDisabled(t *testing.T) {
	runner := &installFakeRunner{
		files: map[string]host.FileState{},
		commandResults: map[string]host.CommandResult{
			"systemctl is-enabled --quiet ship-preview-reaper.timer": {ExitCode: 1},
		},
	}
	changed, err := ensureSystemdUnitEnabled(host.Apply{Context: context.Background(), Runner: runner}, "ship-preview-reaper.timer")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected disabled timer to be enabled")
	}
	if !runner.ranCommand("systemctl", "enable ship-preview-reaper.timer") {
		t.Fatalf("expected systemctl enable, commands: %+v", runner.commands)
	}
}

func TestInjectPodmanUfwBlockIsIdempotent(t *testing.T) {
	once, _, err := injectPodmanUfwBlock(ubuntuBeforeRules, podmanUfwBlock())
	if err != nil {
		t.Fatal(err)
	}
	twice, changed, err := injectPodmanUfwBlock(once, podmanUfwBlock())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second injection must be a no-op")
	}
	if twice != once {
		t.Fatalf("second injection mutated content:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
}

func TestInjectPodmanUfwBlockReplacesExistingBlock(t *testing.T) {
	stale := strings.Replace(
		ubuntuBeforeRules,
		"# End required lines\n",
		"# End required lines\n\n# BEGIN ship podman bridges\n-A ufw-before-input -i podman+ -j REJECT\n# END ship podman bridges\n\n",
		1,
	)
	next, changed, err := injectPodmanUfwBlock(stale, podmanUfwBlock())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change when replacing a stale block")
	}
	if strings.Contains(next, "-j REJECT") {
		t.Fatalf("stale REJECT rule survived replacement:\n%s", next)
	}
	if !strings.Contains(next, "-A ufw-before-input -i podman+ -j ACCEPT") {
		t.Fatalf("canonical ACCEPT rule missing after replace:\n%s", next)
	}
	// Exactly one BEGIN/END pair after replacement.
	if strings.Count(next, "# BEGIN ship podman bridges") != 1 {
		t.Fatalf("expected exactly one BEGIN marker:\n%s", next)
	}
	if strings.Count(next, "# END ship podman bridges") != 1 {
		t.Fatalf("expected exactly one END marker:\n%s", next)
	}
}

func TestInjectPodmanUfwBlockPreservesUserContent(t *testing.T) {
	customized := strings.Replace(
		ubuntuBeforeRules,
		"-A ufw-before-input -i lo -j ACCEPT\n",
		"-A ufw-before-input -i lo -j ACCEPT\n# user: allow vpn\n-A ufw-before-input -p udp --dport 51820 -j ACCEPT\n",
		1,
	)
	next, _, err := injectPodmanUfwBlock(customized, podmanUfwBlock())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "# user: allow vpn") {
		t.Fatalf("dropped user comment:\n%s", next)
	}
	if !strings.Contains(next, "-A ufw-before-input -p udp --dport 51820 -j ACCEPT") {
		t.Fatalf("dropped user rule:\n%s", next)
	}
}

func TestInjectPodmanUfwBlockRejectsHalfMarker(t *testing.T) {
	half := strings.Replace(
		ubuntuBeforeRules,
		"# End required lines\n",
		"# End required lines\n\n# BEGIN ship podman bridges\n# but no END here\n",
		1,
	)
	if _, _, err := injectPodmanUfwBlock(half, podmanUfwBlock()); err == nil {
		t.Fatal("expected error on half-present marker pair")
	}
}

func TestInjectPodmanUfwBlockRejectsMissingAnchor(t *testing.T) {
	noAnchor := strings.ReplaceAll(ubuntuBeforeRules, "# End required lines\n", "")
	if _, _, err := injectPodmanUfwBlock(noAnchor, podmanUfwBlock()); err == nil {
		t.Fatal("expected error when the anchor line is absent")
	}
}

func TestRunInstallWritesPodmanHostBaseline(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{
		files: map[string]host.FileState{
			"/etc/ufw/before.rules": {
				Content: []byte(ubuntuBeforeRules),
				Owner:   "root", Group: "root", Mode: 0640,
			},
			"/etc/default/ufw": {
				Content: []byte("DEFAULT_FORWARD_POLICY=\"DROP\"\nIPV6=yes\n"),
				Owner:   "root", Group: "root", Mode: 0644,
			},
		},
	}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. before.rules has our marker block inserted after the anchor.
	updated, ok := runner.files["/etc/ufw/before.rules"]
	if !ok {
		t.Fatal("expected /etc/ufw/before.rules to be written")
	}
	if !strings.Contains(string(updated.Content), "-A ufw-before-input -i podman+ -j ACCEPT") {
		t.Fatalf("before.rules missing input ACCEPT:\n%s", updated.Content)
	}
	if !strings.Contains(string(updated.Content), "-A ufw-before-forward -i podman+ -j ACCEPT") ||
		!strings.Contains(string(updated.Content), "-A ufw-before-forward -o podman+ -j ACCEPT") {
		t.Fatalf("before.rules missing forward ACCEPT pair:\n%s", updated.Content)
	}
	if !strings.Contains(string(updated.Content), "# allow all on loopback") {
		t.Fatalf("before.rules lost the user/distro content below the anchor:\n%s", updated.Content)
	}

	// 2. default/ufw flipped to ACCEPT, IPV6 line preserved.
	policy, ok := runner.files["/etc/default/ufw"]
	if !ok {
		t.Fatal("expected /etc/default/ufw to be written")
	}
	if !strings.Contains(string(policy.Content), `DEFAULT_FORWARD_POLICY="ACCEPT"`) {
		t.Fatalf("default/ufw did not flip forward policy:\n%s", policy.Content)
	}
	if !strings.Contains(string(policy.Content), "IPV6=yes") {
		t.Fatalf("default/ufw lost unrelated lines:\n%s", policy.Content)
	}

	// 3. registries drop-in written.
	reg, ok := runner.files["/etc/containers/registries.conf.d/00-ship.conf"]
	if !ok {
		t.Fatal("expected /etc/containers/registries.conf.d/00-ship.conf to be written")
	}
	if !strings.Contains(string(reg.Content), `unqualified-search-registries = ["docker.io"]`) {
		t.Fatalf("registries drop-in missing the docker.io entry:\n%s", reg.Content)
	}

	// 4. `ufw reload` was invoked at least once.
	if !runner.ranCommand("ufw", "reload") {
		t.Fatalf("expected `ufw reload` after editing UFW config, commands: %+v", runner.commands)
	}
}

func TestRunInstallCheckModeTreatsMissingUfwAsPending(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &installFakeRunner{
		files:        map[string]host.FileState{},
		missingFiles: map[string]bool{"/etc/ufw/before.rules": true},
		runErrors: map[string]error{
			"ufw status verbose": exec.ErrNotFound,
			"ufw status":         exec.ErrNotFound,
		},
	}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
		CheckMode:             true,
	})
	if err != nil {
		t.Fatalf("check mode should report missing UFW as pending: %v", err)
	}
}

func TestRunInstallSkipsIngressNetworkCreationWhenPresent(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "ship")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	// Default fake runner returns ExitCode 0 for unknown commands, so
	// "podman network exists ingress" reports "exists" (exit 0).
	runner := &installFakeRunner{files: map[string]host.FileState{}}

	_, err := RunInstall(context.Background(), runner, InstallOptions{
		OperatorSSHPublicKeys: []string{operatorTestPublicKey},
		DeploySSHPublicKeys:   []string{deployTestPublicKey},
		StateRoot:             root,
		HelperBinaryPath:      helper,
	})
	if err != nil {
		t.Fatal(err)
	}

	if runner.ranCommand("podman", "network create ingress") {
		t.Fatalf("ingress network create should be skipped when present, commands: %+v", runner.commands)
	}
}

func assertAuthorizedKeysLines(t *testing.T, got string, want []string) {
	t.Helper()
	got = strings.TrimSpace(got)
	wantText := strings.Join(want, "\n")
	if got != wantText {
		t.Fatalf("authorized_keys mismatch\nwant:\n%s\ngot:\n%s", wantText, got)
	}
}

type installFakeRunner struct {
	files          map[string]host.FileState
	missingFiles   map[string]bool
	commands       []host.Command
	commandResults map[string]host.CommandResult
	runErrors      map[string]error
}

func (r *installFakeRunner) ReadFile(_ context.Context, path string) (host.FileState, error) {
	if r.missingFiles[path] {
		return host.FileState{}, host.ErrNotExist
	}
	if file, ok := r.files[path]; ok {
		return file, nil
	}
	// Pretend the essential package install seeded the standard
	// Ubuntu config files. The fake doesn't actually run apt, so
	// tests that exercise ops which read those files (e.g.
	// addPodmanHostBaseline reading /etc/ufw/before.rules) would
	// otherwise hit ErrNotExist on a "successful" essentials step.
	if defaults, ok := installFakeDefaults[path]; ok {
		return defaults, nil
	}
	return host.FileState{}, host.ErrNotExist
}

// installFakeDefaults mirrors what `apt-get install -y ufw` etc.
// drop on a fresh Ubuntu 24.04 box. Add entries here when a new op
// needs to read a file the install assumes is already present.
var installFakeDefaults = map[string]host.FileState{
	"/etc/ufw/before.rules": {
		Content: []byte(ubuntuBeforeRules),
		Owner:   "root", Group: "root", Mode: 0640,
	},
	"/etc/default/ufw": {
		Content: []byte("IPV6=yes\nDEFAULT_INPUT_POLICY=\"DROP\"\nDEFAULT_OUTPUT_POLICY=\"ACCEPT\"\nDEFAULT_FORWARD_POLICY=\"DROP\"\n"),
		Owner:   "root", Group: "root", Mode: 0644,
	},
}

func (r *installFakeRunner) WriteFile(_ context.Context, file host.File) error {
	r.files[file.Path] = host.FileState{
		Content: append([]byte(nil), file.Content...),
		Owner:   file.Owner,
		Group:   file.Group,
		Mode:    file.Mode,
	}
	return nil
}

func (r *installFakeRunner) Validate(_ context.Context, _ host.Validation) error {
	return nil
}

func (r *installFakeRunner) Run(_ context.Context, command host.Command) (host.CommandResult, error) {
	r.commands = append(r.commands, command)
	if err, ok := r.runErrors[installCommandKey(command)]; ok {
		return host.CommandResult{}, err
	}
	if result, ok := r.commandResults[installCommandKey(command)]; ok {
		return result, nil
	}
	switch command.Program {
	case "stat":
		return host.CommandResult{ExitCode: 1}, nil
	case "dpkg-query":
		return host.CommandResult{ExitCode: 1}, nil
	case "getent":
		return host.CommandResult{ExitCode: 1}, nil
	case "id":
		if len(command.Args) > 0 && command.Args[0] == "-nG" {
			return host.CommandResult{Stdout: []byte(command.Args[1] + "\n")}, nil
		}
		return host.CommandResult{ExitCode: 1}, nil
	case "timedatectl":
		if strings.Contains(strings.Join(command.Args, " "), "show") {
			return host.CommandResult{Stdout: []byte("UTC\n")}, nil
		}
	case "localectl":
		return host.CommandResult{Stdout: []byte("System Locale: LANG=en_US.UTF-8\n")}, nil
	}
	return host.CommandResult{}, nil
}

func installCommandKey(command host.Command) string {
	return command.Program + " " + strings.Join(command.Args, " ")
}

func (r *installFakeRunner) ranCommand(program string, args string) bool {
	for _, command := range r.commands {
		if command.Program == program && strings.Join(command.Args, " ") == args {
			return true
		}
	}
	return false
}

var _ host.Runner = (*installFakeRunner)(nil)

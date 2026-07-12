package hostinstall

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/knownhosts"
)

const (
	alicePublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/ alice"
	bobPublicKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICtppnbbz76teU3iU6BguTmo//WITtYN35e4gSER6UNt bob"
)

func TestBuildPlanAndRemoteLocalInstallCommand(t *testing.T) {
	operatorKeyFile := writeKeyFile(t, alicePublicKey+"\n")
	deployKeyFile := writeKeyFile(t, bobPublicKey+"\n")

	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "203.0.113.10"
	opts.BootstrapUser = "root"
	opts.OperatorSSHPublicKeyFile = operatorKeyFile
	opts.DeploySSHPublicKeyFile = deployKeyFile
	opts.Ingress = "cloudflare"
	opts.Admin = "tailscale"
	opts.TailscaleAuthKey = "tskey-auth-test"
	opts.CloudflareAPIToken = "cf-token-test"
	opts.CloudflareAccountID = "account-test"
	opts.InstallLitestream = false
	opts.CheckMode = true

	plan, err := BuildPlan(opts, false, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveSSHKeyPlan(plan, false, "", "root@203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}

	if plan.Mode != "remote" || plan.TargetHost != "203.0.113.10" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.Ingress != "cloudflare" || plan.Admin != "tailscale" {
		t.Fatalf("unexpected presets: ingress=%s admin=%s", plan.Ingress, plan.Admin)
	}
	if plan.TailscaleAuthMode != "auth-key" {
		t.Fatalf("unexpected tailscale auth mode: %s", plan.TailscaleAuthMode)
	}
	if plan.CloudflareServiceMode != "api" {
		t.Fatalf("unexpected cloudflare mode: %s", plan.CloudflareServiceMode)
	}

	command := remoteLocalInstallCommand(remoteHelperExample, plan, "/tmp/operator.pub", "/tmp/deploy.pub", remoteSetupSecretsExample)
	for _, want := range []string{
		`/tmp/ship-host-install.example box setup localhost --mode local`,
		`--ingress cloudflare`,
		`--admin tailscale`,
		`--suppress-setup-narration`,
		`--operator-ssh-public-key-file /tmp/operator.pub`,
		`--deploy-ssh-public-key-file /tmp/deploy.pub`,
		`--cloudflare-account-id account-test`,
		`--setup-secrets-file /tmp/ship-setup-secrets.example`,
		`--no-litestream`,
		`--check`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("expected command to contain %q:\n%s", want, command)
		}
	}
	for _, secret := range []string{"tskey-auth-test", "cf-token-test"} {
		if strings.Contains(command, secret) {
			t.Fatalf("remote command leaked secret %q:\n%s", secret, command)
		}
	}
}

func TestRemoteSetupSecretsArePipedAndCleanedUp(t *testing.T) {
	plan := Plan{
		BootstrapUser:         "root",
		TailscaleAuthKey:      "tskey-auth-test",
		CloudflareAPIToken:    "cf-token-test",
		CloudflareTunnelToken: "cf-tunnel-token-test",
	}
	var calls []struct {
		command string
		stdin   string
	}
	installer := NewInstaller()
	var createCommand string
	installer.remoteOut = func(_ Plan, command string) (string, error) {
		createCommand = command
		return "/tmp/ship-setup-secrets.ABCDEF\n", nil
	}
	installer.remoteRun = func(_ Plan, command string, stdin []byte) error {
		calls = append(calls, struct {
			command string
			stdin   string
		}{command: command, stdin: string(stdin)})
		return nil
	}

	path, cleanup, err := installer.writeRemoteSetupSecrets(plan)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/tmp/ship-setup-secrets.ABCDEF" {
		t.Fatalf("path = %q, want generated root-only path", path)
	}
	if createCommand != "umask 077; mktemp /tmp/ship-setup-secrets.XXXXXX" {
		t.Fatalf("secret transport must create a root-only temp file, command:\n%s", createCommand)
	}
	if len(calls) != 1 {
		t.Fatalf("write calls = %d, want 1", len(calls))
	}
	if !strings.Contains(calls[0].command, "cat > /tmp/ship-setup-secrets.ABCDEF && chmod 0600 /tmp/ship-setup-secrets.ABCDEF") {
		t.Fatalf("secret transport must create a 0600 file, command:\n%s", calls[0].command)
	}
	for _, secret := range []string{"tskey-auth-test", "cf-token-test", "cf-tunnel-token-test"} {
		if strings.Contains(calls[0].command, secret) {
			t.Fatalf("secret leaked into write argv %q:\n%s", secret, calls[0].command)
		}
		if !strings.Contains(calls[0].stdin, secret) {
			t.Fatalf("secret missing from transport payload %q:\n%s", secret, calls[0].stdin)
		}
	}

	command := remoteLocalInstallCommand(remoteHelperExample, plan, "/tmp/operator.pub", "/tmp/deploy.pub", path)
	for _, secret := range []string{"tskey-auth-test", "cf-token-test", "cf-tunnel-token-test"} {
		if strings.Contains(command, secret) {
			t.Fatalf("remote helper argv leaked secret %q:\n%s", secret, command)
		}
	}
	if !strings.Contains(command, "--setup-secrets-file /tmp/ship-setup-secrets.ABCDEF") {
		t.Fatalf("remote helper did not receive transport path:\n%s", command)
	}

	cleanup()
	if len(calls) != 2 || calls[1].command != "rm -f /tmp/ship-setup-secrets.ABCDEF" {
		t.Fatalf("cleanup call = %+v, want root-only transport removal", calls)
	}
}

func TestApplySetupSecretsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup-secrets.json")
	if err := os.WriteFile(path, []byte(`{"tailscale_auth_key":"tskey-auth-test","cloudflare_api_token":"cf-token-test","cloudflare_tunnel_token":"cf-tunnel-token-test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{SetupSecretsFile: path}
	if err := applySetupSecretsFile(&opts); err != nil {
		t.Fatal(err)
	}
	if opts.TailscaleAuthKey != "tskey-auth-test" || opts.CloudflareAPIToken != "cf-token-test" || opts.CloudflareTunnelToken != "cf-tunnel-token-test" {
		t.Fatalf("setup secrets were not loaded: %+v", opts)
	}
}

func TestRunRemoteUsesUniqueHelperPathAndCleansItUp(t *testing.T) {
	for _, tt := range []struct {
		name         string
		installError error
	}{
		{name: "success"},
		{name: "install failure", installError: errors.New("provisioner failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const remoteHelper = "/tmp/ship-host-install.ABCDEF"
			helper := filepath.Join(t.TempDir(), "ship-linux-amd64")
			if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
				t.Fatal(err)
			}

			var commands []string
			var copiedTo string
			installer := NewInstaller()
			installer.Env = map[string]string{"SHIP_LINUX_HELPER": helper}
			installer.remoteOut = func(_ Plan, command string) (string, error) {
				switch command {
				case "echo connected":
					return "connected\n", nil
				case "if test -f ~/.ssh/authorized_keys; then cat ~/.ssh/authorized_keys; fi":
					return alicePublicKey + "\n", nil
				case "uname -m":
					return "x86_64\n", nil
				case "mktemp /tmp/ship-host-install.XXXXXX":
					return remoteHelper + "\n", nil
				case "true":
					return "", nil
				default:
					t.Fatalf("unexpected remote output command: %s", command)
					return "", nil
				}
			}
			installer.remoteCopy = func(_ Plan, _ string, dst string) error {
				copiedTo = dst
				return nil
			}
			installer.remoteRun = func(_ Plan, command string, _ []byte) error {
				commands = append(commands, command)
				if strings.Contains(command, "box setup localhost") {
					return tt.installError
				}
				return nil
			}

			_, err := installer.runRemote(Plan{
				Mode:          "remote",
				TargetHost:    "203.0.113.10",
				BootstrapUser: "root",
				Ingress:       "public",
				Admin:         "public-ssh",
			}, keyPlan{})
			if tt.installError == nil && err != nil {
				t.Fatal(err)
			}
			if tt.installError != nil && err == nil {
				t.Fatal("expected install failure")
			}
			if copiedTo != remoteHelper {
				t.Fatalf("helper copied to %q, want generated path %q", copiedTo, remoteHelper)
			}

			installCommandIndex := -1
			cleanupCommandIndex := -1
			for index, command := range commands {
				if strings.Contains(command, "/tmp/ship-host-install ") {
					t.Fatalf("remote command used fixed helper path: %s", command)
				}
				if strings.Contains(command, "box setup localhost") {
					installCommandIndex = index
					if !strings.Contains(command, "rm -f /tmp/ship-host-install.ABCDEF") {
						t.Fatalf("install command did not clean up helper: %s", command)
					}
				}
				if command == "rm -f /tmp/ship-host-install.ABCDEF" {
					cleanupCommandIndex = index
				}
			}
			if installCommandIndex == -1 {
				t.Fatalf("missing remote install command: %v", commands)
			}
			if cleanupCommandIndex <= installCommandIndex {
				t.Fatalf("helper fallback cleanup did not run after install: %v", commands)
			}
		})
	}
}

func TestResolveSSHKeyPlanDoesNotPromoteBootstrapKeys(t *testing.T) {
	plan := Plan{Mode: "remote"}

	_, err := resolveSSHKeyPlan(plan, false, alicePublicKey+"\n"+bobPublicKey+"\n", "root@203.0.113.10")
	if err == nil {
		t.Fatal("expected missing deploy key error")
	}
	if !errcat.Is(err, errcat.CodeDeployKeyMissing) {
		t.Fatalf("code = %v, want %s", err, errcat.CodeDeployKeyMissing)
	}
}

func TestResolveSSHKeyPlanMissingDeployKeyUsesErrcat(t *testing.T) {
	plan := Plan{Mode: "remote"}

	_, err := resolveSSHKeyPlan(plan, false, "", "root@203.0.113.10")
	if err == nil {
		t.Fatal("expected missing deploy key error")
	}
	if !errcat.Is(err, errcat.CodeDeployKeyMissing) {
		t.Fatalf("code = %v, want %s", err, errcat.CodeDeployKeyMissing)
	}
	wantNext := "next: ssh-copy-id -i ~/.ssh/ship.pub root@203.0.113.10"
	if !strings.Contains(err.Error(), wantNext) {
		t.Fatalf("expected remediation %q, got:\n%v", wantNext, err)
	}
	if !strings.Contains(errcatCause(t, err), "provider gave a password") {
		t.Fatalf("cause should explain password-provisioned remediation, got %q", errcatCause(t, err))
	}
}

func TestBuildPlanParsesSetupTarget(t *testing.T) {
	tests := []struct {
		name                  string
		target                string
		bootstrapUser         string
		bootstrapUserExplicit bool
		wantHost              string
		wantUser              string
		wantErr               string
	}{
		{
			name:          "host only uses default bootstrap user",
			target:        "203.0.113.10",
			bootstrapUser: "root",
			wantHost:      "203.0.113.10",
			wantUser:      "root",
		},
		{
			name:          "user at host sets bootstrap user",
			target:        "deployer@203.0.113.10",
			bootstrapUser: "root",
			wantHost:      "203.0.113.10",
			wantUser:      "deployer",
		},
		{
			name:                  "matching bootstrap user flag is accepted",
			target:                "deployer@203.0.113.10",
			bootstrapUser:         "deployer",
			bootstrapUserExplicit: true,
			wantHost:              "203.0.113.10",
			wantUser:              "deployer",
		},
		{
			name:                  "conflicting bootstrap user flag is rejected",
			target:                "deployer@203.0.113.10",
			bootstrapUser:         "root",
			bootstrapUserExplicit: true,
			wantErr:               `ssh target specifies bootstrap user "deployer" but --bootstrap-user is "root"`,
		},
		{
			name:          "double user target is rejected",
			target:        "root@root@203.0.113.10",
			bootstrapUser: "root",
			wantErr:       `invalid ssh target "root@root@203.0.113.10"`,
		},
		{
			name:          "bracketed IPv6 is rejected clearly",
			target:        "root@[2001:db8::1]",
			bootstrapUser: "root",
			wantErr:       "IPv6 bracket SSH targets are not supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := DefaultOptions(nil)
			opts.Mode = "remote"
			opts.TargetHost = tt.target
			opts.BootstrapUser = tt.bootstrapUser
			opts.BootstrapUserExplicit = tt.bootstrapUserExplicit

			plan, err := BuildPlan(opts, false, false)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !errcat.Is(err, errcat.CodeUsageError) {
					t.Fatalf("code = %v, want %s", err, errcat.CodeUsageError)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error to contain %q, got:\n%v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.TargetHost != tt.wantHost || plan.BootstrapUser != tt.wantUser {
				t.Fatalf("target parsed to host=%q user=%q, want host=%q user=%q", plan.TargetHost, plan.BootstrapUser, tt.wantHost, tt.wantUser)
			}
			if got := bootstrapSSHTarget(plan); got != tt.wantUser+"@"+tt.wantHost {
				t.Fatalf("bootstrap target = %q, want %q", got, tt.wantUser+"@"+tt.wantHost)
			}
		})
	}
}

func TestSSHArgsUseParsedTargetOnce(t *testing.T) {
	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "root@203.0.113.10"

	plan, err := BuildPlan(opts, false, false)
	if err != nil {
		t.Fatal(err)
	}

	args := sshArgs(plan, "true")
	if !contains(args, "root@203.0.113.10") {
		t.Fatalf("expected parsed bootstrap target in ssh args, got %v", args)
	}
	if contains(args, "root@root@203.0.113.10") {
		t.Fatalf("ssh args doubled bootstrap user: %v", args)
	}
}

func TestSetupNarrationPrintsChoicesOnly(t *testing.T) {
	var out bytes.Buffer
	installer := NewInstaller()
	installer.Stderr = &out

	installer.printSetupNarration(Plan{Ingress: "public", Admin: "public-ssh"})

	want := "ingress: public 80/443 (--ingress ...)\n" +
		"admin: SSH keys only (--admin tailscale)\n"
	if out.String() != want {
		t.Fatalf("setup narration mismatch\nwant:\n%s\ngot:\n%s", want, out.String())
	}
}

func TestInstallSummaryNarrationDiet(t *testing.T) {
	var out bytes.Buffer
	installer := NewInstaller()
	installer.Stderr = &out

	installer.printInstallSummary(Plan{})

	for _, notWant := range []string{
		"ship installer starting",
		"Operator user:",
		"Deploy user:",
		"Timezone:",
		"Tailscale: false",
		"Cloudflare Tunnel: false",
		"Docker: false",
		"Litestream: false",
	} {
		if strings.Contains(out.String(), notWant) {
			t.Fatalf("default-off narration should not contain %q:\n%s", notWant, out.String())
		}
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("UTC/default summary should be silent, got:\n%s", out.String())
	}
}

func TestRemotePreConnectionFailureDoesNotPrintMemberAdded(t *testing.T) {
	operatorKeyFile := writeKeyFile(t, alicePublicKey+"\n")
	deployKeyFile := writeKeyFile(t, bobPublicKey+"\n")
	var out bytes.Buffer
	installer := NewInstaller()
	installer.Stderr = &out
	installer.remoteOut = func(plan Plan, command string) (string, error) {
		return "", errors.New("root@203.0.113.10: Permission denied (publickey).")
	}

	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "203.0.113.10"
	opts.OperatorSSHPublicKeyFile = operatorKeyFile
	opts.DeploySSHPublicKeyFile = deployKeyFile

	err := installer.RunOptions(opts)
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	if strings.Contains(out.String(), "member added:") {
		t.Fatalf("pre-connection failure must not claim enrollment, got:\n%s", out.String())
	}
}

func TestRemoteSetupFailureDoesNotPinKnownHost(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	operatorKeyFile := writeKeyFile(t, alicePublicKey+"\n")
	deployKeyFile := writeKeyFile(t, bobPublicKey+"\n")
	installer := NewInstaller()
	installer.remoteOut = func(plan Plan, command string) (string, error) {
		return "", errors.New("root@203.0.113.10: Permission denied (publickey).")
	}

	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "203.0.113.10"
	opts.OperatorSSHPublicKeyFile = operatorKeyFile
	opts.DeploySSHPublicKeyFile = deployKeyFile

	if err := installer.RunOptions(opts); err == nil {
		t.Fatal("expected setup failure")
	}
	if _, err := os.Stat(filepath.Join(configHome, "ship", "known_hosts")); !os.IsNotExist(err) {
		t.Fatalf("known_hosts should not be created before successful setup, stat err=%v", err)
	}
}

func TestMalformedTargetFailsBeforeSSHPreflight(t *testing.T) {
	operatorKeyFile := writeKeyFile(t, alicePublicKey+"\n")
	deployKeyFile := writeKeyFile(t, bobPublicKey+"\n")
	installer := NewInstaller()
	installer.remoteOut = func(plan Plan, command string) (string, error) {
		t.Fatalf("remote preflight should not run for malformed target")
		return "", nil
	}

	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "root@root@203.0.113.10"
	opts.OperatorSSHPublicKeyFile = operatorKeyFile
	opts.DeploySSHPublicKeyFile = deployKeyFile

	err := installer.RunOptions(opts)
	if err == nil {
		t.Fatal("expected malformed target error")
	}
	if !errcat.Is(err, errcat.CodeUsageError) {
		t.Fatalf("code = %v, want %s", err, errcat.CodeUsageError)
	}
	if strings.Contains(err.Error(), "ssh-copy-id") {
		t.Fatalf("malformed target should not get password-bridge remediation:\n%v", err)
	}
}

func TestDefaultOptionsDoNotInstallLitestream(t *testing.T) {
	opts := DefaultOptions(nil)
	if opts.InstallLitestream {
		t.Fatal("Litestream should be opt-in for v1")
	}
}

func TestRemoteLocalInstallCommandEnablesLitestreamExplicitly(t *testing.T) {
	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "203.0.113.12"
	opts.OperatorSSHPublicKeyFile = writeKeyFile(t, alicePublicKey+"\n")
	opts.DeploySSHPublicKeyFile = writeKeyFile(t, bobPublicKey+"\n")
	opts.Ingress = "public"
	opts.Admin = "public-ssh"
	opts.InstallLitestream = true

	plan, err := BuildPlan(opts, false, false)
	if err != nil {
		t.Fatal(err)
	}
	command := remoteLocalInstallCommand(remoteHelperExample, plan, "/tmp/operator.pub", "/tmp/deploy.pub", "")
	if !strings.Contains(command, "--litestream") {
		t.Fatalf("expected command to explicitly enable litestream:\n%s", command)
	}
	if strings.Contains(command, "--no-litestream") {
		t.Fatalf("did not expect conflicting --no-litestream:\n%s", command)
	}
}

func TestPrintNextStepsForRemoteInstall(t *testing.T) {
	var out bytes.Buffer
	installer := NewInstaller()
	installer.Stderr = &out
	installer.printNextSteps(Plan{
		Mode:                   "remote",
		TargetHost:             "203.0.113.12",
		DeploySSHPublicKeyFile: "/keys/deploy.pub",
	})

	text := out.String()
	for _, want := range []string{
		`export SHIP_SSH_KEY="$(cat /keys/deploy)"`,
		"next: ship box doctor 203.0.113.12",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected next steps to contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "SHIP_KNOWN_HOSTS") {
		t.Fatalf("next steps should use normal SSH known_hosts, got:\n%s", text)
	}
	if strings.Index(text, "export SHIP_SSH_KEY") > strings.Index(text, "ship box doctor") {
		t.Fatalf("deploy key export should be printed before box doctor:\n%s", text)
	}
}

func TestPrintNextStepsForIdentityKeyOmitsKeyEnv(t *testing.T) {
	var out bytes.Buffer
	installer := NewInstaller()
	installer.Stderr = &out
	installer.printNextSteps(Plan{
		Mode:                    "remote",
		TargetHost:              "203.0.113.12",
		DeploySSHPublicKeyFile:  "/home/me/.ssh/ship.pub",
		DeployKeyIsShipIdentity: true,
	})

	text := out.String()
	if strings.Contains(text, "SHIP_SSH_KEY") || strings.Contains(text, "SHIP_KNOWN_HOSTS") {
		t.Fatalf("identity key should not require env exports:\n%s", text)
	}
	if !strings.Contains(text, "next: ship box doctor 203.0.113.12") {
		t.Fatalf("expected box doctor to be first step:\n%s", text)
	}
}

func TestPinKnownHostReconcilesSetupTempFile(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	temp := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(temp, []byte("203.0.113.12 "+hostKeyAForInstallTest()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	installer := NewInstaller()
	installer.Stderr = &out
	plan := Plan{TargetHost: "203.0.113.12", KnownHostsFile: temp}

	if err := installer.pinKnownHost(plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configHome + "/ship/known_hosts")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "203.0.113.12 "+hostKeyAForInstallTest()+"\n" {
		t.Fatalf("known_hosts mismatch, got:\n%s", data)
	}
	want := "pinned box 203.0.113.12 (" + knownhosts.DisplayPath + ")"
	if strings.Count(out.String(), want) != 1 {
		t.Fatalf("pin narration mismatch:\n%s", out.String())
	}
}

func TestPinKnownHostNarratesChangedKeyBeforePin(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	canonical, err := knownhosts.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("203.0.113.12 "+hostKeyAForInstallTest()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(temp, []byte("203.0.113.12 "+hostKeyBForInstallTest()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	installer := NewInstaller()
	installer.Stderr = &out
	if err := installer.pinKnownHost(Plan{TargetHost: "203.0.113.12", KnownHostsFile: temp}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, knownhosts.SetupHostKeyChangedMessage+"\n"+"pinned box 203.0.113.12") {
		t.Fatalf("changed-key narration should precede pin line:\n%s", text)
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), hostKeyAForInstallTest()) || !strings.Contains(string(data), hostKeyBForInstallTest()) {
		t.Fatalf("known_hosts was not re-pinned:\n%s", data)
	}
}

func TestHostInstallSSHAcceptsNewHostKeysOnly(t *testing.T) {
	args := sshArgs(Plan{
		BootstrapUser:        "root",
		TargetHost:           "203.0.113.12",
		KnownHostsFile:       "/tmp/setup-known-hosts",
		SSHKey:               "/keys/root",
		BootstrapIdentityKey: "/home/me/.ssh/ship",
	}, "true")

	for _, want := range []string{"BatchMode=yes", "UserKnownHostsFile=/tmp/setup-known-hosts", "StrictHostKeyChecking=accept-new", "HashKnownHosts=no", "/keys/root", "/home/me/.ssh/ship", "root@203.0.113.12"} {
		if !contains(args, want) {
			t.Fatalf("expected ssh args to contain %q, got %v", want, args)
		}
	}
	if contains(args, "IdentitiesOnly=yes") {
		t.Fatalf("bootstrap SSH should not force IdentitiesOnly, got %v", args)
	}
}

func hostKeyAForInstallTest() string {
	return strings.Join(strings.Fields(alicePublicKey)[:2], " ")
}

func hostKeyBForInstallTest() string {
	return strings.Join(strings.Fields(bobPublicKey)[:2], " ")
}

func TestInstallPresetsMapToProviderFlags(t *testing.T) {
	tests := []struct {
		name             string
		ingress          string
		admin            string
		wantCloudflare   bool
		wantTailscale    bool
		wantCloudflareMo string
		wantTailscaleMo  string
	}{
		{name: "defaults", wantCloudflareMo: "disabled", wantTailscaleMo: "disabled"},
		{name: "public ssh", ingress: "public", admin: "public-ssh", wantCloudflareMo: "disabled", wantTailscaleMo: "disabled"},
		{name: "cloudflare tailscale", ingress: "cloudflare", admin: "tailscale", wantCloudflare: true, wantTailscale: true, wantCloudflareMo: "manual", wantTailscaleMo: "manual"},
		{name: "private", ingress: "private", admin: "public-ssh", wantCloudflareMo: "disabled", wantTailscaleMo: "disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := DefaultOptions(nil)
			opts.Mode = "remote"
			opts.TargetHost = "203.0.113.20"
			opts.DeploySSHPublicKeyFile = writeKeyFile(t, bobPublicKey+"\n")
			opts.Ingress = tt.ingress
			opts.Admin = tt.admin

			plan, err := BuildPlan(opts, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if plan.CloudflareTunnel != tt.wantCloudflare {
				t.Fatalf("cloudflare=%v, want %v", plan.CloudflareTunnel, tt.wantCloudflare)
			}
			if plan.Tailscale != tt.wantTailscale {
				t.Fatalf("tailscale=%v, want %v", plan.Tailscale, tt.wantTailscale)
			}
			if plan.CloudflareServiceMode != tt.wantCloudflareMo {
				t.Fatalf("cloudflare mode=%s, want %s", plan.CloudflareServiceMode, tt.wantCloudflareMo)
			}
			if plan.TailscaleAuthMode != tt.wantTailscaleMo {
				t.Fatalf("tailscale mode=%s, want %s", plan.TailscaleAuthMode, tt.wantTailscaleMo)
			}
		})
	}
}

func TestInstallPresetsRejectInvalidValues(t *testing.T) {
	opts := DefaultOptions(nil)
	opts.Ingress = "vpn-provider-matrix"
	_, err := BuildPlan(opts, false, false)
	if err == nil || !strings.Contains(err.Error(), "invalid ingress mode") {
		t.Fatalf("expected invalid ingress error, got %v", err)
	}

	opts = DefaultOptions(nil)
	opts.Admin = "root-password"
	_, err = BuildPlan(opts, false, false)
	if err == nil || !strings.Contains(err.Error(), "invalid admin mode") {
		t.Fatalf("expected invalid admin error, got %v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func errcatCause(t *testing.T, err error) string {
	t.Helper()
	coded, ok := errcat.As(err)
	if !ok {
		t.Fatalf("expected errcat error, got %v", err)
	}
	return coded.Cause()
}

func TestCloudflareTokenRequiresTunnel(t *testing.T) {
	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "203.0.113.12"
	opts.DeploySSHPublicKeyFile = "deploy.pub"
	opts.CloudflareTunnel = false
	opts.CloudflareAPIToken = "cf-token-test"

	_, err := BuildPlan(opts, false, false)
	if err == nil {
		t.Fatal("expected invalid Cloudflare options to fail")
	}
	if !strings.Contains(err.Error(), "--cloudflare-api-token requires Cloudflare Tunnel to be enabled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAutoModeChoosesLocalOnlyOnRootHost(t *testing.T) {
	opts := DefaultOptions(nil)

	plan, err := BuildPlan(opts, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "local" {
		t.Fatalf("expected local mode, got %s", plan.Mode)
	}

	_, err = BuildPlan(opts, false, false)
	if err == nil || !strings.Contains(err.Error(), "TARGET_HOST is required") {
		t.Fatalf("expected missing remote host error, got %v", err)
	}
}

func TestPreflightSSHRequiresConnectedSentinel(t *testing.T) {
	installer := NewInstaller()
	installer.remoteOut = func(plan Plan, command string) (string, error) {
		return "", nil
	}

	err := installer.preflightSSH(Plan{BootstrapUser: "root", TargetHost: "203.0.113.10"})
	if err == nil {
		t.Fatal("expected empty SSH preflight output to fail")
	}
	if !strings.Contains(err.Error(), "expected connected sentinel") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflightSSHIncludesSSHError(t *testing.T) {
	installer := NewInstaller()
	installer.remoteOut = func(plan Plan, command string) (string, error) {
		return "", errors.New("ssh command failed: Host key verification failed")
	}

	err := installer.preflightSSH(Plan{BootstrapUser: "root", TargetHost: "203.0.113.10"})
	if err == nil {
		t.Fatal("expected SSH preflight error")
	}
	if !errcat.Is(err, errcat.CodeHostInstallSSHFailed) {
		t.Fatalf("code = %v, want %s", err, errcat.CodeHostInstallSSHFailed)
	}
	for _, want := range []string{
		"SSH preflight failed for root@203.0.113.10: ssh command failed: Host key verification failed",
		"Host key verification failed",
		"next: ssh -o BatchMode=yes -o 'UserKnownHostsFile=~/.config/ship/known_hosts' -o StrictHostKeyChecking=accept-new -o HashKnownHosts=no root@203.0.113.10",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
}

func TestPreflightSSHPublicKeyFailureSuggestsSSHCopyID(t *testing.T) {
	installer := NewInstaller()
	installer.remoteOut = func(plan Plan, command string) (string, error) {
		return "", errors.New("root@203.0.113.10: Permission denied (publickey).")
	}

	err := installer.preflightSSH(Plan{BootstrapUser: "root", TargetHost: "203.0.113.10"})
	if err == nil {
		t.Fatal("expected SSH preflight error")
	}
	if !errcat.Is(err, errcat.CodeDeployKeyMissing) {
		t.Fatalf("code = %v, want %s", err, errcat.CodeDeployKeyMissing)
	}
	for _, want := range []string{
		"provider gave a password",
		"this installs your ship key using it once",
		"next: ssh-copy-id -i ~/.ssh/ship.pub root@203.0.113.10",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
}

func TestPreflightSSHPublicKeyFailureUsesParsedBootstrapUser(t *testing.T) {
	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "deployer@203.0.113.10"
	plan, err := BuildPlan(opts, false, false)
	if err != nil {
		t.Fatal(err)
	}

	installer := NewInstaller()
	installer.remoteOut = func(plan Plan, command string) (string, error) {
		return "", errors.New("deployer@203.0.113.10: Permission denied (publickey,password).")
	}

	err = installer.preflightSSH(plan)
	if err == nil {
		t.Fatal("expected SSH preflight error")
	}
	if !errcat.Is(err, errcat.CodeDeployKeyMissing) {
		t.Fatalf("code = %v, want %s", err, errcat.CodeDeployKeyMissing)
	}
	for _, want := range []string{
		"next: ssh-copy-id -i ~/.ssh/ship.pub deployer@203.0.113.10",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "root@deployer@203.0.113.10") {
		t.Fatalf("error doubled bootstrap user: %v", err)
	}
}

func TestWaitForRemoteSSHSwallowsTransientRestartNoise(t *testing.T) {
	var out bytes.Buffer
	attempts := 0
	installer := NewInstaller()
	installer.Stderr = &out
	installer.sleep = func(time.Duration) {}
	installer.remoteOut = func(plan Plan, command string) (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("ssh: connect to host 203.0.113.10 port 22: Connection refused")
		}
		return "", nil
	}

	err := installer.waitForRemoteSSH(Plan{BootstrapUser: "root", TargetHost: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if strings.Contains(out.String(), "Connection refused") || strings.Contains(out.String(), "ssh:") {
		t.Fatalf("transient SSH noise leaked:\n%s", out.String())
	}
}

func TestWaitForRemoteSSHReportsOneCodedErrorAfterRetryBudget(t *testing.T) {
	installer := NewInstaller()
	installer.sleep = func(time.Duration) {}
	installer.remoteOut = func(plan Plan, command string) (string, error) {
		return "", errors.New("ssh: connect to host 203.0.113.10 port 22: Connection refused")
	}

	err := installer.waitForRemoteSSH(Plan{BootstrapUser: "root", TargetHost: "203.0.113.10"})
	if err == nil {
		t.Fatal("expected reconnect failure")
	}
	if !errcat.Is(err, errcat.CodeHostInstallSSHFailed) {
		t.Fatalf("code = %v, want %s", err, errcat.CodeHostInstallSSHFailed)
	}
	if strings.Count(err.Error(), "Connection refused") != 1 {
		t.Fatalf("final error should include one connection-refused detail:\n%s", err)
	}
}

func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/key.pub"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

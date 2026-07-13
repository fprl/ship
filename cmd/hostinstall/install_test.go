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

func TestReadPublicKeyFileErrorCodes(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.pub")
		_, err := readPublicKeyFile(path)
		coded, ok := errcat.As(err)
		if !ok || coded.Code() != errcat.CodeSSHPublicKeyFileMissing {
			t.Fatalf("code = %v, want %s", err, errcat.CodeSSHPublicKeyFileMissing)
		}
		if got, want := coded.Cause(), "SSH public key file not found: "+path; got != want {
			t.Fatalf("cause = %q, want %q", got, want)
		}
		if got, want := coded.Remediation(), keygenCommand(privateKeyPathForPublic(path)); got != want {
			t.Fatalf("remediation = %q, want %q", got, want)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		path := writeKeyFile(t, "\n# no keys\n")
		_, err := readPublicKeyFile(path)
		coded, ok := errcat.As(err)
		if !ok || coded.Code() != errcat.CodeSSHPublicKeyFileEmpty {
			t.Fatalf("code = %v, want %s", err, errcat.CodeSSHPublicKeyFileEmpty)
		}
		if got, want := coded.Cause(), "SSH public key file is empty: "+path; got != want {
			t.Fatalf("cause = %q, want %q", got, want)
		}
		if got, want := coded.Remediation(), publicKeyFromPrivateCommand(path); got != want {
			t.Fatalf("remediation = %q, want %q", got, want)
		}
	})

	t.Run("invalid key line", func(t *testing.T) {
		_, err := readPublicKeyFile(writeKeyFile(t, "ssh-ed25519\n"))
		coded, ok := errcat.As(err)
		if !ok || coded.Code() != errcat.CodeSSHPublicKeyInvalid {
			t.Fatalf("code = %v, want %s", err, errcat.CodeSSHPublicKeyInvalid)
		}
		if got, want := coded.Cause(), "public key line must contain key type and key body"; got != want {
			t.Fatalf("detail = %q, want %q", got, want)
		}
	})
}

func TestBuildPlanAndRemoteLocalInstallCommand(t *testing.T) {
	operatorKeyFile := writeKeyFile(t, alicePublicKey+"\n")
	deployKeyFile := writeKeyFile(t, bobPublicKey+"\n")

	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "203.0.113.10"
	opts.BootstrapUser = "root"
	opts.OperatorSSHPublicKeyFile = operatorKeyFile
	opts.DeploySSHPublicKeyFile = deployKeyFile
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
	command := remoteLocalInstallCommand(remoteHelperExample, plan, "/tmp/operator.pub", "/tmp/deploy.pub")
	for _, want := range []string{
		`/tmp/ship-host-install.example box setup localhost --mode local`,
		`--suppress-setup-narration`,
		`--operator-ssh-public-key-file /tmp/operator.pub`,
		`--deploy-ssh-public-key-file /tmp/deploy.pub`,
		`--check`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("expected command to contain %q:\n%s", want, command)
		}
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
			if err := os.WriteFile(helper, elfHeader(62), 0755); err != nil {
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

func TestSetupNarrationPrintsFixedTopology(t *testing.T) {
	var out bytes.Buffer
	installer := NewInstaller()
	installer.Stderr = &out

	installer.printSetupNarration(Plan{})

	want := "ingress: public 80/443\n" +
		"admin: SSH keys only\n"
	if out.String() != want {
		t.Fatalf("setup narration mismatch\nwant:\n%s\ngot:\n%s", want, out.String())
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

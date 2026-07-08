package hostinstall

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
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
	opts.OperatorUser = "ops"
	opts.DeployUser = "deployer"
	opts.OperatorSSHPublicKeyFile = operatorKeyFile
	opts.DeploySSHPublicKeyFile = deployKeyFile
	opts.Ingress = "cloudflare"
	opts.Admin = "tailscale"
	opts.TailscaleAuthKey = "tskey-auth-test"
	opts.CloudflareAPIToken = "cf-token-test"
	opts.CloudflareAccountID = "account-test"
	opts.InstallDocker = true
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

	command := remoteLocalInstallCommand("/tmp/ship-host-install", plan, "/tmp/operator.pub", "/tmp/deploy.pub")
	for _, want := range []string{
		`/tmp/ship-host-install box init localhost --mode local`,
		`--operator-user ops`,
		`--deploy-user deployer`,
		`--ingress cloudflare`,
		`--admin tailscale`,
		`--operator-ssh-public-key-file /tmp/operator.pub`,
		`--deploy-ssh-public-key-file /tmp/deploy.pub`,
		`--tailscale-auth-key tskey-auth-test`,
		`--cloudflare-api-token cf-token-test`,
		`--cloudflare-account-id account-test`,
		`--docker`,
		`--no-litestream`,
		`--check`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("expected command to contain %q:\n%s", want, command)
		}
	}
}

func TestResolveSSHKeyPlanPromotesBootstrapKeys(t *testing.T) {
	plan := Plan{Mode: "remote"}

	keys, err := resolveSSHKeyPlan(plan, false, alicePublicKey+"\n"+bobPublicKey+"\n", "root@203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.Deploy) != 2 || len(keys.Operator) != 2 {
		t.Fatalf("expected bootstrap keys for deploy and operator: %+v", keys)
	}
	if keys.Deploy[0].Comment != "alice" || !keys.Deploy[0].Promoted {
		t.Fatalf("unexpected promoted key metadata: %+v", keys.Deploy[0])
	}
	if keys.Operator[0].Promoted {
		t.Fatalf("operator fallback should not print as promoted member: %+v", keys.Operator[0])
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
	wantNext := "next: ssh-copy-id root@203.0.113.10"
	if !strings.Contains(err.Error(), wantNext) {
		t.Fatalf("expected remediation %q, got:\n%v", wantNext, err)
	}
	if !strings.Contains(errcatCause(t, err), "provider gave a password") {
		t.Fatalf("cause should explain password-provisioned remediation, got %q", errcatCause(t, err))
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
	command := remoteLocalInstallCommand("/tmp/ship-host-install", plan, "/tmp/operator.pub", "/tmp/deploy.pub")
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
	installer.Stdout = &out
	installer.printNextSteps(Plan{
		Mode:                   "remote",
		TargetHost:             "203.0.113.12",
		DeployUser:             "deploy",
		DeploySSHPublicKeyFile: "/keys/deploy.pub",
	})

	text := out.String()
	for _, want := range []string{
		`export SHIP_SSH_KEY="$(cat /keys/deploy)"`,
		"ship box doctor deploy@203.0.113.12",
		"ship init --box deploy@203.0.113.12 --host <app-domain>",
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

func TestPrintNextStepsForPromotedBootstrapKeyOmitsKeyEnv(t *testing.T) {
	var out bytes.Buffer
	installer := NewInstaller()
	installer.Stdout = &out
	installer.printNextSteps(Plan{
		Mode:       "remote",
		TargetHost: "203.0.113.12",
		DeployUser: "deploy",
	})

	text := out.String()
	if strings.Contains(text, "SHIP_SSH_KEY") || strings.Contains(text, "SHIP_KNOWN_HOSTS") {
		t.Fatalf("bootstrap-promoted key should not require env exports:\n%s", text)
	}
	if !strings.Contains(text, "1. ship box doctor deploy@203.0.113.12") {
		t.Fatalf("expected box doctor to be first step:\n%s", text)
	}
}

func TestHostInstallSSHAcceptsNewHostKeysOnly(t *testing.T) {
	args := sshArgs(Plan{
		BootstrapUser: "root",
		TargetHost:    "203.0.113.12",
		SSHKey:        "/keys/root",
	}, "true")

	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=accept-new", "/keys/root", "root@203.0.113.12"} {
		if !contains(args, want) {
			t.Fatalf("expected ssh args to contain %q, got %v", want, args)
		}
	}
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
		"next: ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@203.0.113.10",
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
		"SSH public-key auth failed for root@203.0.113.10",
		"provider gave a password",
		"next: ssh-copy-id root@203.0.113.10",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
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

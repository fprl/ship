package hostinstall

import (
	"os"
	"strings"
	"testing"
)

func TestBuildPlanAndRenderExtraVars(t *testing.T) {
	operatorKeyFile := writeKeyFile(t, "ssh-ed25519 AAAAoperator test-operator\n")
	deployKeyFile := writeKeyFile(t, "ssh-ed25519 AAAAdeploy test-deploy\n")

	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "203.0.113.10"
	opts.BootstrapUser = "root"
	opts.OperatorUser = "ops"
	opts.DeployUser = "deployer"
	opts.OperatorSSHPublicKeyFile = operatorKeyFile
	opts.DeploySSHPublicKeyFile = deployKeyFile
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
	keys, err := resolveSSHKeyPlan(plan, false, "")
	if err != nil {
		t.Fatal(err)
	}

	if plan.Mode != "remote" || plan.TargetHost != "203.0.113.10" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.TailscaleAuthMode != "auth-key" {
		t.Fatalf("unexpected tailscale auth mode: %s", plan.TailscaleAuthMode)
	}
	if plan.CloudflareServiceMode != "api" {
		t.Fatalf("unexpected cloudflare mode: %s", plan.CloudflareServiceMode)
	}

	extraVars := renderExtraVars(plan, keys)
	for _, want := range []string{
		`simple_vps_operator_user: "ops"`,
		`simple_vps_deploy_user: "deployer"`,
		`simple_vps_tailscale_auth_key: 'tskey-auth-test'`,
		`simple_vps_cloudflare_api_token: 'cf-token-test'`,
		`simple_vps_cloudflare_account_id: 'account-test'`,
		`simple_vps_install_docker: true`,
		`simple_vps_install_litestream: false`,
		`  - 'ssh-ed25519 AAAAoperator test-operator'`,
		`  - 'ssh-ed25519 AAAAdeploy test-deploy'`,
	} {
		if !strings.Contains(extraVars, want) {
			t.Fatalf("expected extra vars to contain %q:\n%s", want, extraVars)
		}
	}
}

func TestSharedKeyRendersForOperatorAndDeploy(t *testing.T) {
	operatorKeyFile := writeKeyFile(t, "ssh-ed25519 AAAAoperator test-operator\n")

	opts := DefaultOptions(nil)
	opts.Mode = "remote"
	opts.TargetHost = "203.0.113.11"
	opts.OperatorSSHPublicKeyFile = operatorKeyFile
	opts.SharedKey = true
	opts.Tailscale = false
	opts.CloudflareTunnel = false

	plan, err := BuildPlan(opts, false, false)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := resolveSSHKeyPlan(plan, false, "")
	if err != nil {
		t.Fatal(err)
	}

	if plan.TailscaleAuthMode != "disabled" {
		t.Fatalf("unexpected tailscale auth mode: %s", plan.TailscaleAuthMode)
	}
	if plan.CloudflareServiceMode != "disabled" {
		t.Fatalf("unexpected cloudflare mode: %s", plan.CloudflareServiceMode)
	}

	extraVars := renderExtraVars(plan, keys)
	if got := strings.Count(extraVars, "  - 'ssh-ed25519 AAAAoperator test-operator'"); got != 2 {
		t.Fatalf("expected shared key to render twice, got %d:\n%s", got, extraVars)
	}
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
	if !strings.Contains(err.Error(), "--cloudflare-api-token requires Cloudflare Tunnel to be enabled.") {
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

func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/key.pub"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

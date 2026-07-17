package identity

import (
	"strings"
	"testing"
)

func TestEnvRootIsFlatAndHumanReadable(t *testing.T) {
	if got := EnvRoot("api", "production"); got != "/var/apps/api.production" {
		t.Fatalf("EnvRoot = %q, want /var/apps/api.production", got)
	}
}

func TestDataRuntimeStaticAndActivationPaths(t *testing.T) {
	if got := DataDir("api", "production"); got != "/var/apps/api.production/data" {
		t.Fatalf("DataDir = %q", got)
	}
	if got := RuntimeDir("api", "production"); got != "/var/apps/api.production/runtime" {
		t.Fatalf("RuntimeDir = %q", got)
	}
	if got := ActivationsDir("api", "production"); got != "/var/apps/api.production/runtime/activations" {
		t.Fatalf("ActivationsDir = %q", got)
	}
	if got := ActivationEnvFile("api", "production", "abc123-00112233"); got != "/var/apps/api.production/runtime/activations/abc123-00112233.env" {
		t.Fatalf("ActivationEnvFile = %q", got)
	}
	if got := StaticDir("api", "production"); got != "/var/apps/api.production/static" {
		t.Fatalf("StaticDir = %q", got)
	}
	if got := ReleaseDir("api", "production"); got != "/var/apps/api.production/releases" {
		t.Fatalf("ReleaseDir = %q", got)
	}
	if got := ActiveFile("api", "production"); got != "/var/apps/api.production/active.json" {
		t.Fatalf("ActiveFile = %q", got)
	}
	if got := IdentityFile("api", "production"); got != "/var/apps/api.production/ship.json" {
		t.Fatalf("IdentityFile = %q", got)
	}
	if got, want := CaddyFragmentFile("api", "production"), "/etc/caddy/conf.d/ship-"+EnvironmentKey("api", "production")+".caddy"; got != want {
		t.Fatalf("CaddyFragmentFile = %q, want %q", got, want)
	}
}

func TestEnvironmentKeyIsDeterministicAndBounded(t *testing.T) {
	a := EnvironmentKey("api", "production")
	b := EnvironmentKey("api", "production")
	if a != b {
		t.Fatalf("EnvironmentKey not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "ship-") || len(a) != len("ship-")+12 {
		t.Fatalf("EnvironmentKey = %q, want ship- plus 12 hex chars", a)
	}
	if a == EnvironmentKey("api", "staging") {
		t.Fatal("different envs should not share an environment key")
	}
}

func TestInfraNamesStayWithinLimits(t *testing.T) {
	app := "very-long-application-name"
	env := "production-environment"
	process := "background-worker-process"
	release := "abc1234-dirty-20260528t123456000000000z"

	for name, value := range map[string]string{
		"SystemUser":    SystemUser(app, env),
		"Network":       Network(app, env),
		"ContainerName": ContainerName(app, env, process, release),
	} {
		if len(value) > dnsLabelLimit {
			t.Fatalf("%s = %q exceeds DNS label limit", name, value)
		}
		if strings.Contains(value, ".") {
			t.Fatalf("%s = %q should be DNS/user safe, not host-path style", name, value)
		}
	}
	if len(SystemUser(app, env)) > linuxUserNameLimit {
		t.Fatalf("SystemUser exceeds Linux username limit: %q", SystemUser(app, env))
	}
}

func TestImageTagUsesImageRepo(t *testing.T) {
	wantRepo := ImageRepo("api", "production")
	if !strings.HasPrefix(wantRepo, "ship/") {
		t.Fatalf("ImageRepo = %q, want ship/ prefix", wantRepo)
	}
	if got := ImageTag("api", "production", "abc123"); got != wantRepo+":abc123" {
		t.Fatalf("ImageTag = %q", got)
	}
}

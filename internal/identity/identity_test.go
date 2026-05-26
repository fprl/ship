package identity

import "testing"

func TestSystemUserUsesAppEnvPair(t *testing.T) {
	if got := SystemUser("api", "production"); got != "app-api-production" {
		t.Fatalf("SystemUser = %q, want app-api-production", got)
	}
}

func TestNetworkUsesAppEnvPair(t *testing.T) {
	if got := Network("api", "production"); got != "app-api-production" {
		t.Fatalf("Network = %q, want app-api-production", got)
	}
}

func TestContainerNameIncludesService(t *testing.T) {
	if got := ContainerName("api", "production", "web"); got != "app-api-production-web" {
		t.Fatalf("ContainerName = %q, want app-api-production-web", got)
	}
}

func TestContainerNameNewSuffix(t *testing.T) {
	if got := ContainerNameNew("api", "production", "web"); got != "app-api-production-web-new" {
		t.Fatalf("ContainerNameNew = %q, want app-api-production-web-new", got)
	}
}

func TestImageTagUsesPerEnvScope(t *testing.T) {
	if got := ImageTag("api", "production", "abc1234"); got != "simple-vps/api-production:abc1234" {
		t.Fatalf("ImageTag = %q, want simple-vps/api-production:abc1234", got)
	}
}

func TestImageRepoStripsSHA(t *testing.T) {
	if got := ImageRepo("api", "production"); got != "simple-vps/api-production" {
		t.Fatalf("ImageRepo = %q, want simple-vps/api-production", got)
	}
}

func TestAppEnvRootIsPerEnv(t *testing.T) {
	if got := AppEnvRoot("api", "production"); got != "/var/apps/api/production" {
		t.Fatalf("AppEnvRoot = %q, want /var/apps/api/production", got)
	}
}

func TestAppRootIsParentOfEnvs(t *testing.T) {
	if got := AppRoot("api"); got != "/var/apps/api" {
		t.Fatalf("AppRoot = %q, want /var/apps/api", got)
	}
}

func TestSharedDirIsUnderEnvRoot(t *testing.T) {
	if got := SharedDir("api", "production"); got != "/var/apps/api/production/shared" {
		t.Fatalf("SharedDir = %q, want /var/apps/api/production/shared", got)
	}
}

func TestEnvFileIsUnderSharedDir(t *testing.T) {
	if got := EnvFile("api", "production"); got != "/var/apps/api/production/shared/.env" {
		t.Fatalf("EnvFile = %q, want /var/apps/api/production/shared/.env", got)
	}
}

func TestSystemUserFitsLinuxLimitForReasonableNames(t *testing.T) {
	// Worst case from ADR-0005 Section 1: app=16, env=8 → app-<16>-<8> = 29 chars.
	got := SystemUser("aaaaaaaaaaaaaaaa", "bbbbbbbb")
	if len(got) != 29 {
		t.Fatalf("expected len 29 for worst-case 16+8 names, got %d (%q)", len(got), got)
	}
	if len(got) > 31 {
		t.Fatalf("SystemUser %q exceeds 31-char Linux limit", got)
	}
}

package helper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/secrets"
)

func TestResolveOrCreatePreviewCreatesStableMapping(t *testing.T) {
	setupPreviewHostTest(t)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	env, err := resolveOrCreatePreview("api", "feat/x", now)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^feat-x-[a-z0-9]{4}$`).MatchString(env) {
		t.Fatalf("unexpected preview env name: %q", env)
	}

	again, err := resolveOrCreatePreview("api", "feat/x", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again != env {
		t.Fatalf("same branch should resolve to stable env: first=%s second=%s", env, again)
	}

	file, err := readEnvIdentity("api", env)
	if err != nil {
		t.Fatal(err)
	}
	if file.Preview == nil {
		t.Fatal("preview metadata missing")
	}
	if file.Preview.Branch != "feat/x" || file.Preview.SanitizedBranch != "feat-x" || file.Preview.Env != env {
		t.Fatalf("unexpected preview mapping: %+v", file.Preview)
	}
	if !regexp.MustCompile(`^[a-z0-9]{4}$`).MatchString(file.Preview.Suffix) {
		t.Fatalf("unexpected suffix: %q", file.Preview.Suffix)
	}
	if !file.Preview.LastShipAt.Equal(now) {
		t.Fatalf("last ship = %s, want %s", file.Preview.LastShipAt, now)
	}
	if file.Preview.ExpiresAt == nil || !file.Preview.ExpiresAt.Equal(now.Add(previewTTL)) {
		t.Fatalf("expiry = %v, want %s", file.Preview.ExpiresAt, now.Add(previewTTL))
	}
}

func TestRefreshPreviewShipUpdatesExpiryUnlessPinned(t *testing.T) {
	setupPreviewHostTest(t)
	initial := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	env := writePreviewIdentityForTest(t, "api", "feat-x-ab12", "feat/x", "feat-x", "ab12", initial, false)

	nextShip := initial.Add(2 * time.Hour)
	if err := refreshPreviewShip("api", env, nextShip); err != nil {
		t.Fatal(err)
	}
	file, err := readEnvIdentity("api", env)
	if err != nil {
		t.Fatal(err)
	}
	if !file.Preview.LastShipAt.Equal(nextShip) {
		t.Fatalf("last ship = %s, want %s", file.Preview.LastShipAt, nextShip)
	}
	if file.Preview.ExpiresAt == nil || !file.Preview.ExpiresAt.Equal(nextShip.Add(previewTTL)) {
		t.Fatalf("expiry = %v, want %s", file.Preview.ExpiresAt, nextShip.Add(previewTTL))
	}

	if err := pinPreview("api", env, true); err != nil {
		t.Fatal(err)
	}
	pinnedShip := nextShip.Add(time.Hour)
	if err := refreshPreviewShip("api", env, pinnedShip); err != nil {
		t.Fatal(err)
	}
	file, err = readEnvIdentity("api", env)
	if err != nil {
		t.Fatal(err)
	}
	if !file.Preview.Pinned || file.Preview.ExpiresAt != nil {
		t.Fatalf("pinned refresh should keep no expiry: %+v", file.Preview)
	}
}

func TestPinPreviewClearsExpiryAndUnpinRestoresFromLastShip(t *testing.T) {
	setupPreviewHostTest(t)
	lastShip := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	env := writePreviewIdentityForTest(t, "api", "feat-x-ab12", "feat/x", "feat-x", "ab12", lastShip, false)

	if err := pinPreview("api", env, true); err != nil {
		t.Fatal(err)
	}
	file, err := readEnvIdentity("api", env)
	if err != nil {
		t.Fatal(err)
	}
	if !file.Preview.Pinned || file.Preview.ExpiresAt != nil {
		t.Fatalf("pin should clear expiry: %+v", file.Preview)
	}

	if err := pinPreview("api", env, false); err != nil {
		t.Fatal(err)
	}
	file, err = readEnvIdentity("api", env)
	if err != nil {
		t.Fatal(err)
	}
	if file.Preview.Pinned || file.Preview.ExpiresAt == nil || !file.Preview.ExpiresAt.Equal(lastShip.Add(previewTTL)) {
		t.Fatalf("unpin should restore last_ship+TTL: %+v", file.Preview)
	}
}

func TestResolveOrCreatePreviewRetriesSuffixCollision(t *testing.T) {
	setupPreviewHostTest(t)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	writePreviewIdentityForTest(t, "api", "feat-x-ab12", "feat/x", "feat-x", "ab12", now, false)

	suffixes := []string{"ab12", "cd34"}
	previous := newPreviewSuffix
	newPreviewSuffix = func() (string, error) {
		if len(suffixes) == 0 {
			t.Fatal("suffix generator called too many times")
		}
		next := suffixes[0]
		suffixes = suffixes[1:]
		return next, nil
	}
	t.Cleanup(func() { newPreviewSuffix = previous })

	env, err := resolveOrCreatePreview("api", "feat.x", now)
	if err != nil {
		t.Fatal(err)
	}
	if env != "feat-x-cd34" {
		t.Fatalf("expected suffix collision retry to choose feat-x-cd34, got %s", env)
	}
}

func TestResolveOrCreatePreviewKeysByRawBranchNotSanitizedBranch(t *testing.T) {
	setupPreviewHostTest(t)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	suffixes := []string{"ab12", "cd34"}
	previous := newPreviewSuffix
	newPreviewSuffix = func() (string, error) {
		if len(suffixes) == 0 {
			t.Fatal("suffix generator called too many times")
		}
		next := suffixes[0]
		suffixes = suffixes[1:]
		return next, nil
	}
	t.Cleanup(func() { newPreviewSuffix = previous })

	first, err := resolveOrCreatePreview("api", "feat/login", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveOrCreatePreview("api", "feat.login", now)
	if err != nil {
		t.Fatal(err)
	}
	if first != "feat-login-ab12" || second != "feat-login-cd34" {
		t.Fatalf("raw branch collision should create distinct envs, got %s and %s", first, second)
	}
	if again, err := resolveOrCreatePreview("api", "feat/login", now.Add(time.Hour)); err != nil || again != first {
		t.Fatalf("raw branch should resolve stably: env=%s err=%v", again, err)
	}
}

func TestReapExpiredPreviewsDestroysUnpinnedPurgesSecretsAndSkipsPinnedAndProd(t *testing.T) {
	setupPreviewHostTest(t)
	secretsRoot := t.TempDir()
	t.Setenv("SHIP_SECRETS_DIR", secretsRoot)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	expiredEnv := writePreviewIdentityForTest(t, "api", "old-branch-ab12", "old/branch", "old-branch", "ab12", now.Add(-previewTTL-time.Hour), false)
	pinnedEnv := writePreviewIdentityForTest(t, "api", "pinned-branch-cd34", "pinned/branch", "pinned-branch", "cd34", now.Add(-previewTTL-time.Hour), true)
	futureEnv := writePreviewIdentityForTest(t, "api", "fresh-branch-ef56", "fresh/branch", "fresh-branch", "ef56", now.Add(-time.Hour), false)
	writeIdentityForTest(t, identity.EnvIdentity{
		Version: 1,
		App:     "api",
		Env:     productionEnvName,
		InfraID: identity.InfraID("api", productionEnvName),
	})
	if err := secrets.Put("api", expiredEnv, "DATABASE_URL", []byte("secret")); err != nil {
		t.Fatal(err)
	}

	var destroyed []string
	count, err := reapExpiredPreviews(now, func(app, env string, purge bool) (destroySummary, error) {
		destroyed = append(destroyed, app+"/"+env)
		if !purge {
			t.Fatalf("reaper must purge secrets for %s/%s", app, env)
		}
		if err := os.RemoveAll(secrets.EnvDir(app, env)); err != nil {
			return destroySummary{}, err
		}
		return destroySummary{SecretsPurged: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(destroyed) != 1 || destroyed[0] != "api/"+expiredEnv {
		t.Fatalf("unexpected reap set: count=%d destroyed=%v", count, destroyed)
	}
	if _, err := os.Stat(secrets.EnvDir("api", expiredEnv)); !os.IsNotExist(err) {
		t.Fatalf("expired preview secrets should be purged, stat err=%v", err)
	}
	for _, env := range []string{pinnedEnv, futureEnv, productionEnvName} {
		if _, err := readEnvIdentity("api", env); err != nil {
			t.Fatalf("%s should survive reap: %v", env, err)
		}
	}
}

func TestUnknownPreviewBranchErrorText(t *testing.T) {
	got := unknownPreviewBranchError("feat/x").Error()
	want := "unknown_preview_branch: no preview environment is mapped for branch \"feat/x\"\nnext: ship"
	if got != want {
		t.Fatalf("unexpected error:\nwant: %q\n got: %q", want, got)
	}
}

func setupPreviewHostTest(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_DEPLOY_TMP_DIR", filepath.Join(root, "deploy-tmp"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "id", "#!/usr/bin/env sh\nexit 1\n")
	for _, name := range []string{"useradd", "usermod", "chown", "chmod"} {
		writeFakeCommand(t, bin, name, "#!/usr/bin/env sh\nexit 0\n")
	}
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
if [ "$1" = "network" ] && [ "$2" = "exists" ]; then
  exit 1
fi
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeFakeCommand(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

func writePreviewIdentityForTest(t *testing.T, app, env, branch, sanitizedBranch, suffix string, lastShip time.Time, pinned bool) string {
	t.Helper()
	var expires *time.Time
	if !pinned {
		expiry := lastShip.Add(previewTTL)
		expires = &expiry
	}
	writeIdentityForTest(t, identity.EnvIdentity{
		Version: 1,
		App:     app,
		Env:     env,
		InfraID: identity.InfraID(app, env),
		Preview: &identity.PreviewIdentity{
			Branch:          branch,
			SanitizedBranch: sanitizedBranch,
			Env:             env,
			Suffix:          suffix,
			LastShipAt:      lastShip,
			ExpiresAt:       expires,
			Pinned:          pinned,
		},
	})
	return env
}

func writeIdentityForTest(t *testing.T, file identity.EnvIdentity) {
	t.Helper()
	path := identity.IdentityFile(file.App, file.Env)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

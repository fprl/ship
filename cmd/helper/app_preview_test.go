package helper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/secrets"
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
	if file.Preview.Branch != "feat/x" || names.PreviewSanitizedBranch(file.Preview.Branch) != "feat-x" || names.PreviewBranchSlug(file.Env) != "feat-x" {
		t.Fatalf("unexpected preview mapping: %+v", file.Preview)
	}
	if suffix, ok := names.PreviewSuffix(file.Env); !ok || !regexp.MustCompile(`^[a-z0-9]{4}$`).MatchString(suffix) {
		t.Fatalf("unexpected suffix derived from env: %q", file.Env)
	}
	if !file.Preview.LastShipAt.Equal(now) {
		t.Fatalf("last ship = %s, want %s", file.Preview.LastShipAt, now)
	}
	if file.Preview.ExpiresAt == nil || !file.Preview.ExpiresAt.Equal(now.Add(previewTTL)) {
		t.Fatalf("expiry = %v, want %s", file.Preview.ExpiresAt, now.Add(previewTTL))
	}
}

func TestShipIdentityPersistsOnlyNonDerivablePreviewFields(t *testing.T) {
	setupPreviewHostTest(t)
	expires := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	preview := &identity.PreviewIdentity{
		Branch:     "Feature/Login",
		LastShipAt: expires.Add(-previewTTL),
		ExpiresAt:  &expires,
	}
	if err := writeEnvIdentityWithPreview("api", "feature-login-a1b2", preview); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(identity.IdentityFile("api", "feature-login-a1b2"))
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	previewShape, ok := shape["preview"].(map[string]any)
	if !ok {
		t.Fatalf("preview shape = %#v", shape["preview"])
	}
	for _, deleted := range []string{"sanitized_branch", "suffix", "env", "pinned"} {
		if _, found := previewShape[deleted]; found {
			t.Fatalf("deleted preview field %q persisted: %s", deleted, raw)
		}
	}
	for _, retained := range []string{"branch", "last_ship_at", "expires_at"} {
		if _, found := previewShape[retained]; !found {
			t.Fatalf("retained preview field %q missing: %s", retained, raw)
		}
	}
	if err := writeEnvIdentity("api", productionEnvName); err != nil {
		t.Fatal(err)
	}
	productionRaw, err := os.ReadFile(identity.IdentityFile("api", productionEnvName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(productionRaw), `"preview"`) {
		t.Fatalf("production identity unexpectedly has preview block: %s", productionRaw)
	}
}

func TestValidatePreviewIdentityUsesDerivedFields(t *testing.T) {
	expires := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	valid := identity.EnvIdentity{
		Version: 1, App: "api", Env: "feature-login-a1b2",
		Preview: &identity.PreviewIdentity{Branch: "feature/login", LastShipAt: expires.Add(-previewTTL), ExpiresAt: &expires},
	}
	if err := validatePreviewIdentity(valid); err != nil {
		t.Fatalf("valid derived preview rejected: %v", err)
	}
	for name, file := range map[string]identity.EnvIdentity{
		"invalid branch":      invalidPreviewIdentity("feature..login", "feature-login-a1b2"),
		"wrong sanitized env": invalidPreviewIdentity("feature/login", "other-a1b2"),
		"invalid suffix":      invalidPreviewIdentity("feature/login", "feature-login-a12"),
		"production preview":  invalidPreviewIdentity("feature/login", productionEnvName),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePreviewIdentity(file); err == nil {
				t.Fatal("malformed preview identity was accepted")
			}
		})
	}
}

func invalidPreviewIdentity(branch, env string) identity.EnvIdentity {
	return identity.EnvIdentity{
		Version: 1, App: "api", Env: env,
		Preview: &identity.PreviewIdentity{Branch: branch, LastShipAt: time.Now().UTC()},
	}
}

func TestResolveOrCreatePreviewWritesOnlyFinalIdentity(t *testing.T) {
	setupPreviewHostTest(t)
	previousSuffix := newPreviewSuffix
	newPreviewSuffix = func() (string, error) { return "a1b2", nil }
	t.Cleanup(func() { newPreviewSuffix = previousSuffix })
	previousWrite := atomicWriteEnvIdentity
	var writes [][]byte
	atomicWriteEnvIdentity = func(path string, data []byte, mode os.FileMode) error {
		writes = append(writes, append([]byte(nil), data...))
		return previousWrite(path, data, mode)
	}
	t.Cleanup(func() { atomicWriteEnvIdentity = previousWrite })

	if _, err := resolveOrCreatePreview("api", "feat/write-once", time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 {
		t.Fatalf("preview allocation identity writes = %d, want 1", len(writes))
	}
	var file identity.EnvIdentity
	if err := json.Unmarshal(writes[0], &file); err != nil {
		t.Fatal(err)
	}
	if file.Preview == nil || file.Preview.Branch != "feat/write-once" {
		t.Fatalf("identity write was not final preview identity: %+v", file)
	}
}

func TestResolveOrCreatePreviewHealsPartialAllocationOnRetry(t *testing.T) {
	setupPreviewHostTest(t)
	env := "feat-retry-a1b2"
	if err := secrets.PutPreviewCapability("api", env, []byte("inert-capability")); err != nil {
		t.Fatal(err)
	}
	previousSuffix := newPreviewSuffix
	newPreviewSuffix = func() (string, error) { return "a1b2", nil }
	t.Cleanup(func() { newPreviewSuffix = previousSuffix })

	got, err := resolveOrCreatePreview("api", "feat/retry", time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got != env {
		t.Fatalf("retry resolved env = %q, want %q", got, env)
	}
	file, err := readEnvIdentity("api", env)
	if err != nil {
		t.Fatal(err)
	}
	if file.Preview == nil || file.Preview.Branch != "feat/retry" {
		t.Fatalf("partial allocation was not healed: %+v", file)
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
	if !names.PreviewPinned(file.Preview.ExpiresAt) || file.Preview.ExpiresAt != nil {
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
	if !names.PreviewPinned(file.Preview.ExpiresAt) || file.Preview.ExpiresAt != nil {
		t.Fatalf("pin should clear expiry: %+v", file.Preview)
	}

	if err := pinPreview("api", env, false); err != nil {
		t.Fatal(err)
	}
	file, err = readEnvIdentity("api", env)
	if err != nil {
		t.Fatal(err)
	}
	if names.PreviewPinned(file.Preview.ExpiresAt) || file.Preview.ExpiresAt == nil || !file.Preview.ExpiresAt.Equal(lastShip.Add(previewTTL)) {
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

func TestResolveOrCreatePreviewRetriesBoxGlobalHostLabelCollision(t *testing.T) {
	setupPreviewHostTest(t)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	writePreviewIdentityForTest(t, "api-foo", "xxxx-ab12", "xxxx", "xxxx", "ab12", now, false)

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

	env, err := resolveOrCreatePreview("api", "foo/xxxx", now)
	if err != nil {
		t.Fatal(err)
	}
	if env != "foo-xxxx-cd34" {
		t.Fatalf("host-label collision should retry, got %s", env)
	}
}

func TestWriteEnvIdentityRefusesProductionHostLabelCollisionWithPreview(t *testing.T) {
	setupPreviewHostTest(t)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	writePreviewIdentityForTest(t, "api", "foo-ab12", "foo", "foo", "ab12", now, false)

	err := writeEnvIdentity("api-foo-ab12", productionEnvName)
	if !errcat.Is(err, errcat.CodeHostLabelConflict) {
		t.Fatalf("write production identity error = %v, want host_label_conflict", err)
	}
	coded, _ := errcat.As(err)
	if got, want := coded.Cause(), "app api-foo-ab12 (production) generates host label api-foo-ab12, already used by api (foo-ab12)"; got != want {
		t.Fatalf("collision cause = %q, want %q", got, want)
	}
	if got, want := coded.Remediation(), "change the top-level name in ship.toml, then ship"; got != want {
		t.Fatalf("collision remediation = %q, want %q", got, want)
	}
	if envIdentityExists("api-foo-ab12", productionEnvName) {
		t.Fatal("production identity was created despite a host-label collision")
	}
}

func TestResolveOrCreatePreviewRetriesTruncatedHostLabelCollision(t *testing.T) {
	setupPreviewHostTest(t)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	app := strings.Repeat("a", 40)
	writePreviewIdentityForTest(t, app, "abcdefghijklmnop-foo-ab12", "abcdefghijklmnop/foo", "abcdefghijklmnop-foo", "ab12", now, false)

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

	env, err := resolveOrCreatePreview(app, "abcdefghijklmnop/bar", now)
	if err != nil {
		t.Fatal(err)
	}
	if env != "abcdefghijklmnop-bar-cd34" {
		t.Fatalf("truncated host-label collision should retry, got %s", env)
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

func TestReapExpiredPreviewsSkipsPreviewPinnedAfterInitialCheck(t *testing.T) {
	setupPreviewHostTest(t)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	env := writePreviewIdentityForTest(t, "api", "old-branch-ab12", "old/branch", "old-branch", "ab12", now.Add(-previewTTL-time.Hour), false)

	count, err := reapExpiredPreviewsWithLock(now, func(app, gotEnv string, purge bool) (destroySummary, error) {
		t.Fatalf("reaper destroyed %s/%s after it was pinned", app, gotEnv)
		return destroySummary{}, nil
	}, func(app, gotEnv string) (*appEnvLock, error) {
		if err := pinPreview(app, gotEnv, true); err != nil {
			return nil, err
		}
		return acquireAppEnvLock(app, gotEnv)
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("reaped %d previews, want 0", count)
	}
	file, err := readEnvIdentity("api", env)
	if err != nil {
		t.Fatal(err)
	}
	if file.Preview == nil || !names.PreviewPinned(file.Preview.ExpiresAt) {
		t.Fatalf("preview should remain pinned: %+v", file.Preview)
	}
}

func TestUnknownPreviewBranchErrorText(t *testing.T) {
	err := unknownPreviewBranchError("feat/x")
	want := "preview environment lookup failed\nno preview environment is mapped for branch \"feat/x\"\nnext: git checkout feat/x && ship"
	if !errcat.Is(err, errcat.CodeUnknownPreviewBranch) || err.Error() != want {
		t.Fatalf("unexpected error:\nwant: %q\n got: %q", want, err.Error())
	}
}

func TestPreviewAliasRoutesRenderWithCapabilityAndAreRemovedWhenDisabled(t *testing.T) {
	port := 3000
	canonical := "api-feat-x-ab12.preview.example.com"
	alias := "feat-x.preview.example.com"
	ctx := &config.AppContext{
		Preview:                config.Preview{Base: "preview.example.com", Aliases: true},
		PreviewCapabilityToken: "capability-token",
		Processes:              map[string]config.Process{"web": {Port: &port}},
		Routes: map[string]config.Route{
			canonical:           {Host: canonical, Process: "web"},
			canonical + "/docs": {Host: canonical, Path: "/docs", Process: "web"},
		},
	}
	if err := addPreviewAliasRoutes("api", "feat-x-ab12", alias, ctx); err != nil {
		t.Fatal(err)
	}
	rendered, err := renderAppCaddyfileWithProcessNames("api", "feat-x-ab12", ctx, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"` + canonical + `" {`, `"` + alias + `" {`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered fragment missing %s:\n%s", want, rendered)
		}
	}
	if got := strings.Count(rendered, "respond @ship_capability_denied 401"); got != 2 {
		t.Fatalf("capability guard count = %d, want one per canonical and alias host:\n%s", got, rendered)
	}

	withoutAlias := &config.AppContext{
		PreviewCapabilityToken: "capability-token",
		Processes:              ctx.Processes,
		Routes: map[string]config.Route{
			canonical: {Host: canonical, Process: "web"},
		},
	}
	rendered, err = renderAppCaddyfileWithProcessNames("api", "feat-x-ab12", withoutAlias, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, alias) {
		t.Fatalf("aliases=false deploy should remove previous alias from fragment:\n%s", rendered)
	}
}

func TestPreviewAliasOwnerCollisionMatrix(t *testing.T) {
	const alias = "feat-x.preview.example.com"
	currentRoutes := map[string]config.Route{
		"api-feat-x-ab12.preview.example.com": {Host: "api-feat-x-ab12.preview.example.com", Process: "web"},
	}

	tests := []struct {
		name  string
		setup func(t *testing.T)
		kind  string
	}{
		{
			name: "existing alias wins",
			setup: func(t *testing.T) {
				writePreviewIdentityForTest(t, "other", "feat-x-cd34", "feat/x", "feat-x", "cd34", time.Now().UTC(), false)
				writeActiveEnvelopeForPreviewAliasTest(t, "other", "feat-x-cd34", `name = "other"
box = "example.com"

[preview]
base = "preview.example.com"
aliases = true

[processes]
web = { port = 3000 }

[routes]
"other-feat-x-cd34.preview.example.com" = { static = "dist" }
`)
				previousRead := readCaddyFragment
				readCaddyFragment = func(path string) ([]byte, error) {
					if path == identity.CaddyFragmentFile("other", "feat-x-cd34") {
						return []byte(`"feat-x.preview.example.com" {
}
`), nil
					}
					return nil, os.ErrNotExist
				}
				t.Cleanup(func() { readCaddyFragment = previousRead })
			},
			kind: "preview alias",
		},
		{
			name: "configured route wins",
			setup: func(t *testing.T) {
				writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "site", Env: productionEnvName})
				writeActiveEnvelopeForPreviewAliasTest(t, "site", productionEnvName, `name = "site"
box = "example.com"

[processes]
web = { port = 3000 }

[routes]
"feat-x.preview.example.com" = { static = "dist" }
`)
			},
			kind: "route",
		},
		{
			name: "production synthesized host wins",
			setup: func(t *testing.T) {
				writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "feat-x", Env: productionEnvName})
				writeActiveEnvelopeForPreviewAliasTest(t, "feat-x", productionEnvName, `name = "feat-x"
box = "example.com"

[processes]
web = { port = 3000 }

[routes]
"feat-x.203-0-113-7.sslip.io" = { static = "dist" }
`)
			},
			kind: "route",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupPreviewHostTest(t)
			tt.setup(t)
			candidate := alias
			if tt.name == "production synthesized host wins" {
				candidate = "feat-x.203-0-113-7.sslip.io"
			}
			owner, found, err := previewAliasOwner(candidate, "api", "feat-x-ab12", currentRoutes)
			if err != nil {
				t.Fatal(err)
			}
			if !found || owner.Kind != tt.kind {
				t.Fatalf("owner = %+v found=%v, want kind %q", owner, found, tt.kind)
			}
		})
	}

	owner, found, err := previewAliasOwner("api-feat-x-ab12.preview.example.com", "api", "feat-x-ab12", currentRoutes)
	if err != nil {
		t.Fatal(err)
	}
	if !found || owner.App != "api" || owner.Env != "feat-x-ab12" {
		t.Fatalf("canonical route must remain its own owner, got %+v found=%v", owner, found)
	}
	if len(currentRoutes) != 1 {
		t.Fatalf("collision check changed canonical routes: %+v", currentRoutes)
	}
}

func writeActiveEnvelopeForPreviewAliasTest(t *testing.T, app, env, body string) {
	t.Helper()
	release := "abc1234"
	meta, err := newReleaseMetadata(release, false, release, "2026-07-14T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	e, label, err := releaseEnvelope([]byte(body), meta)
	if err != nil {
		t.Fatal(err)
	}
	staticHash := strings.Repeat("a", 64)
	if err := activationrecords.Publish(app, env, activationrecords.Pointer{Version: 2, Activation: release + "-activation", Artifact: activationrecords.Tuple{Release: release, StaticHash: staticHash, EnvelopeHash: envelope.HashLabel(label)}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staticReleasePath(app, env, release, staticHash), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeStaticReleaseEnvelope(app, env, release, e); err != nil {
		t.Fatal(err)
	}
}

func setupPreviewHostTest(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_SECRETS_DIR", filepath.Join(root, "secrets"))
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
if [ "$1" = "ps" ]; then
  printf '[]\n'
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
		Preview: &identity.PreviewIdentity{
			Branch:     branch,
			LastShipAt: lastShip,
			ExpiresAt:  expires,
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

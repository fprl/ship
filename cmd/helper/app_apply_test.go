package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/secrets"
)

// --- resolveEnv: literal + secret merging for app apply ---

func TestResolveEnvMergesLiteralsAndSecrets(t *testing.T) {
	t.Setenv("SIMPLE_VPS_SECRETS_DIR", t.TempDir())
	if err := secrets.Put("api", "production", "db_url", []byte("postgres://x")); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put("api", "production", "stripe_key", []byte("sk_test_123")); err != nil {
		t.Fatal(err)
	}

	got, err := resolveEnv("api", "production",
		map[string]string{"LOG_LEVEL": "info", "PUBLIC_API_URL": "https://api.example.com"},
		map[string]string{"DATABASE_URL": "db_url", "STRIPE_KEY": "stripe_key"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"LOG_LEVEL":      "info",
		"PUBLIC_API_URL": "https://api.example.com",
		"DATABASE_URL":   "postgres://x",
		"STRIPE_KEY":     "sk_test_123",
	} {
		if got[k] != want {
			t.Fatalf("resolved %s = %q, want %q (full: %v)", k, got[k], want, got)
		}
	}
}

func TestResolveEnvFailsOnMissingSecretBeforeAnyContainerStarts(t *testing.T) {
	t.Setenv("SIMPLE_VPS_SECRETS_DIR", t.TempDir())
	// Only one of the two refs is in the store. Deploy must fail
	// rather than silently emit the env file with one variable.
	if err := secrets.Put("api", "production", "stripe_key", []byte("sk_x")); err != nil {
		t.Fatal(err)
	}
	_, err := resolveEnv("api", "production",
		nil,
		map[string]string{"DATABASE_URL": "db_url", "STRIPE_KEY": "stripe_key"},
	)
	if err == nil {
		t.Fatal("expected error for missing @secret reference")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "db_url") {
		t.Fatalf("error should name the missing env-var AND the secret key, got: %v", err)
	}
	// And tell the user how to fix it.
	if !strings.Contains(err.Error(), "simple-vps secret put") {
		t.Fatalf("error should point at `simple-vps secret put`, got: %v", err)
	}
}

func TestResolveEnvPreservesLiteralsWhenNoSecrets(t *testing.T) {
	t.Setenv("SIMPLE_VPS_SECRETS_DIR", t.TempDir())
	got, err := resolveEnv("api", "production",
		map[string]string{"LOG_LEVEL": "info"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["LOG_LEVEL"] != "info" {
		t.Fatalf("expected single literal, got %v", got)
	}
}

func TestResolveEnvDoesNotMutateInputMaps(t *testing.T) {
	t.Setenv("SIMPLE_VPS_SECRETS_DIR", t.TempDir())
	_ = secrets.Put("api", "production", "k", []byte("v"))
	literals := map[string]string{"L": "lit"}
	refs := map[string]string{"R": "k"}
	_, err := resolveEnv("api", "production", literals, refs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := literals["R"]; ok {
		t.Fatal("resolveEnv leaked resolved secrets back into the literals map")
	}
}

// --- podmanBuildArgs: cache/pull policy ---

func TestPodmanBuildArgsDefaultUsesCache(t *testing.T) {
	args := podmanBuildArgs("api", "production", "simple-vps/api-production:abc123", "abc123", "/tmp/Dockerfile", "/tmp/ctx", false)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--no-cache") || strings.Contains(joined, "--pull=always") {
		t.Fatalf("default build should leave Podman's cache/pull policy alone: %s", joined)
	}
	for _, want := range []string{
		"build",
		"-t simple-vps/api-production:abc123",
		"--label app=api",
		"--label env=production",
		"--label simple_vps_release=abc123",
		"-f /tmp/Dockerfile",
		"/tmp/ctx",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("build args missing %q: %s", want, joined)
		}
	}
}

func TestPodmanBuildArgsRebuildBypassesCacheAndPullsBases(t *testing.T) {
	args := podmanBuildArgs("api", "production", "simple-vps/api-production:abc123", "abc123", "/tmp/Dockerfile", "/tmp/ctx", true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--no-cache --pull=always") {
		t.Fatalf("rebuild should pass --no-cache and --pull=always together: %s", joined)
	}
}

// --- buildPodmanRunArgs: container security floor + manifest tmpfs ---

func TestBuildPodmanRunArgsAlwaysEmitsHardeningFloor(t *testing.T) {
	svc := config.Service{}
	args := buildPodmanRunArgs("api", "production", "web", svc, "img:tag", "999", "988", false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"--pids-limit 512",
		"--read-only",
		// mode=1777 is required so the unprivileged container user can
		// actually write to the tmpfs. See finding in PR #36.
		"--tmpfs /tmp:size=64m,mode=1777",
		"--user 999:988",
		"--network app-api-production",
		"--network ingress",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in args:\n%s", want, joined)
		}
	}
}

func TestBuildPodmanRunArgsAppendsManifestTmpfsAfterDefault(t *testing.T) {
	svc := config.Service{
		Tmpfs: map[string]string{
			"/var/cache/nginx": "64m",
			"/var/run":         "16m",
			"/run/lock":        "1k",
		},
	}
	args := buildPodmanRunArgs("api", "production", "web", svc, "img:tag", "999", "988", false)

	// Default /tmp tmpfs always present.
	tmpfsValues := []string{}
	for i, a := range args {
		if a == "--tmpfs" && i+1 < len(args) {
			tmpfsValues = append(tmpfsValues, args[i+1])
		}
	}
	want := []string{
		"/tmp:size=64m,mode=1777",
		"/run/lock:size=1k,mode=1777",
		"/var/cache/nginx:size=64m,mode=1777",
		"/var/run:size=16m,mode=1777",
	}
	if len(tmpfsValues) != len(want) {
		t.Fatalf("expected %d --tmpfs values, got %d: %v", len(want), len(tmpfsValues), tmpfsValues)
	}
	for i, w := range want {
		if tmpfsValues[i] != w {
			t.Fatalf("--tmpfs values not in deterministic order:\nwant: %v\n got: %v", want, tmpfsValues)
		}
	}
}

func TestBuildPodmanRunArgsEmitsManifestResourceCapsAndNetBindCapability(t *testing.T) {
	memory := "512m"
	cpus := 0.5
	netBind := true
	svc := config.Service{
		Memory:         &memory,
		CPUs:           &cpus,
		NetBindService: &netBind,
	}
	args := buildPodmanRunArgs("api", "production", "web", svc, "img:tag", "999", "988", false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--memory 512m",
		"--cpus 0.5",
		"--cap-add NET_BIND_SERVICE",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in args:\n%s", want, joined)
		}
	}
}

func TestBuildPodmanRunArgsImageComesAfterFlagsAndBeforeCommand(t *testing.T) {
	svc := config.Service{
		Command: "/usr/bin/myserver --foo",
		Tmpfs:   map[string]string{"/var/cache": "16m"},
	}
	args := buildPodmanRunArgs("api", "production", "web", svc, "simple-vps/api-production:abcd1234", "999", "988", true)

	var imageIdx, shIdx, cmdIdx int = -1, -1, -1
	for i, a := range args {
		switch a {
		case "simple-vps/api-production:abcd1234":
			imageIdx = i
		case "/bin/sh":
			shIdx = i
		case "-c":
			cmdIdx = i
		}
	}
	if imageIdx == -1 {
		t.Fatalf("image tag missing from args: %v", args)
	}
	if !(shIdx == imageIdx+1 && cmdIdx == imageIdx+2) {
		t.Fatalf("expected image followed by /bin/sh -c, got args:\n%s", strings.Join(args, " "))
	}
	// Every --tmpfs/--env-file/--label flag should precede the image.
	for i, a := range args {
		if a == "--tmpfs" || a == "--env-file" || a == "--label" {
			if i > imageIdx {
				t.Fatalf("flag %q at index %d appears after image at %d", a, i, imageIdx)
			}
		}
	}
}

func TestBuildPodmanRunArgsSkipsEnvFileWhenAbsent(t *testing.T) {
	args := buildPodmanRunArgs("api", "production", "web", config.Service{}, "img:tag", "999", "988", false)
	for _, a := range args {
		if a == "--env-file" {
			t.Fatalf("did not expect --env-file when env file is absent, args:\n%s", strings.Join(args, " "))
		}
	}
}

func TestRenderEnvFileEmitsSortedKeyValuePairs(t *testing.T) {
	vals := map[string]string{
		"LOG_LEVEL": "info",
		"DEBUG":     "false",
		"PORT":      "3000",
	}
	got := renderEnvFile(vals)
	want := "DEBUG=false\nLOG_LEVEL=info\nPORT=3000\n"
	if got != want {
		t.Fatalf("renderEnvFile mismatch:\nwant: %q\n got: %q", want, got)
	}
}

func TestRenderEnvFileEmptyMapProducesEmptyString(t *testing.T) {
	if got := renderEnvFile(nil); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
	if got := renderEnvFile(map[string]string{}); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestRenderAppCaddyfileProxyRouteUsesContainerDNS(t *testing.T) {
	// Per ADR-0006 Cut 2: Caddy reaches each service over the shared
	// `ingress` Podman network by container name, not by host-loopback
	// port. The fragment must encode that exact name.
	port := 3000
	ctx := &config.AppContext{
		Services: map[string]config.Service{
			"web": {Port: &port},
		},
		Routes: map[string]config.Route{
			"app": {
				Host:    "api.example.com",
				Type:    "proxy",
				Service: "web",
			},
		},
	}
	got, err := renderAppCaddyfile("api", "production", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"api.example.com" {`) {
		t.Fatalf("expected quoted host block, got:\n%s", got)
	}
	if !strings.Contains(got, "reverse_proxy http://app-api-production-web:3000") {
		t.Fatalf("expected container-DNS reverse_proxy, got:\n%s", got)
	}
	if strings.Contains(got, "127.0.0.1") {
		t.Fatalf("rendered Caddyfile still uses host loopback:\n%s", got)
	}
}

func TestRenderAppCaddyfileEmitsTLSInternalForInternalRoute(t *testing.T) {
	port := 3000
	ctx := &config.AppContext{
		Services: map[string]config.Service{
			"web": {Port: &port},
		},
		Routes: map[string]config.Route{
			"app": {
				Host:    "smoke.example.com",
				Type:    "proxy",
				Service: "web",
				TLS:     "internal",
			},
		},
	}
	got, err := renderAppCaddyfile("api", "production", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\ttls internal\n") {
		t.Fatalf("expected `tls internal` directive, got:\n%s", got)
	}
	// reverse_proxy still rendered to the per-(app, env, service) DNS name.
	if !strings.Contains(got, "reverse_proxy http://app-api-production-web:3000") {
		t.Fatalf("expected reverse_proxy after tls directive, got:\n%s", got)
	}
}

func TestRenderAppCaddyfileOmitsTLSDirectiveForAuto(t *testing.T) {
	port := 3000
	for _, tls := range []string{"", "auto"} {
		t.Run("tls="+tls, func(t *testing.T) {
			ctx := &config.AppContext{
				Services: map[string]config.Service{
					"web": {Port: &port},
				},
				Routes: map[string]config.Route{
					"app": {
						Host:    "api.example.com",
						Type:    "proxy",
						Service: "web",
						TLS:     tls,
					},
				},
			}
			got, err := renderAppCaddyfile("api", "production", ctx)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, "tls") {
				t.Fatalf("expected no tls directive for tls=%q, got:\n%s", tls, got)
			}
		})
	}
}

func TestRenderAppCaddyfileRedirectRoute(t *testing.T) {
	ctx := &config.AppContext{
		Routes: map[string]config.Route{
			"r": {
				Host: "old.example.com",
				Type: "redirect",
				To:   "https://new.example.com",
			},
		},
	}
	got, err := renderAppCaddyfile("api", "production", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"old.example.com" {`) {
		t.Fatalf("expected quoted host block, got:\n%s", got)
	}
	if !strings.Contains(got, `redir "https://new.example.com" permanent`) {
		t.Fatalf("expected quoted redir directive with permanent flag, got:\n%s", got)
	}
}

func TestRenderAppCaddyfileRejectsProxyWithoutServicePort(t *testing.T) {
	ctx := &config.AppContext{
		Services: map[string]config.Service{
			"worker": {}, // no port
		},
		Routes: map[string]config.Route{
			"r": {
				Host:    "x.example.com",
				Type:    "proxy",
				Service: "worker",
			},
		},
	}
	if _, err := renderAppCaddyfile("api", "production", ctx); err == nil {
		t.Fatal("expected error for proxy route pointing at portless service")
	}
}

func TestRenderAppCaddyfileRejectsUnknownRouteType(t *testing.T) {
	ctx := &config.AppContext{
		Routes: map[string]config.Route{
			"r": {
				Host: "x.example.com",
				Type: "weirdo",
			},
		},
	}
	if _, err := renderAppCaddyfile("api", "production", ctx); err == nil {
		t.Fatal("expected error for unknown route type")
	}
}

func TestRenderAppCaddyfileQuotesHostAndRedirectTarget(t *testing.T) {
	// Manifest validation only enforces an http:// / https:// prefix on
	// redirect targets, so a hostile value like "https://x.com\nbad" could
	// otherwise inject directives. CaddyQuote rejects newlines and
	// JSON-escapes everything else.
	ctx := &config.AppContext{
		Routes: map[string]config.Route{
			"r": {
				Host: "old.example.com",
				Type: "redirect",
				To:   "https://new.example.com",
			},
		},
	}
	got, err := renderAppCaddyfile("api", "production", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"old.example.com" {`) {
		t.Fatalf("expected quoted host on block selector, got:\n%s", got)
	}
	if !strings.Contains(got, `redir "https://new.example.com" permanent`) {
		t.Fatalf("expected quoted redir target with permanent flag, got:\n%s", got)
	}
}

func TestRenderAppCaddyfileRejectsRedirectTargetWithNewline(t *testing.T) {
	ctx := &config.AppContext{
		Routes: map[string]config.Route{
			"r": {
				Host: "old.example.com",
				Type: "redirect",
				To:   "https://new.example.com\nrespond 500",
			},
		},
	}
	if _, err := renderAppCaddyfile("api", "production", ctx); err == nil {
		t.Fatal("expected error for redirect target containing a newline")
	}
}

func TestSnapshotAndRestoreCaddyFragmentRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frag.caddy")
	original := []byte("\"api.example.com\" {\n\treverse_proxy http://app-api-production-web:3000\n}\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	snapshot, existed, err := snapshotCaddyFragment(path)
	if err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Fatal("expected fragment to exist")
	}
	if string(snapshot) != string(original) {
		t.Fatalf("snapshot mismatch:\nwant: %q\n got: %q", original, snapshot)
	}

	// Simulate writing a bad fragment, then restoring.
	if err := os.WriteFile(path, []byte("garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := restoreCaddyFragment(path, snapshot, existed); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("restore mismatch:\nwant: %q\n got: %q", original, got)
	}
}

func TestSnapshotCaddyFragmentReportsAbsence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.caddy")
	snapshot, existed, err := snapshotCaddyFragment(path)
	if err != nil {
		t.Fatal(err)
	}
	if existed {
		t.Fatal("expected existed=false for missing path")
	}
	if snapshot != nil {
		t.Fatalf("expected nil snapshot, got %q", snapshot)
	}
}

func TestRestoreCaddyFragmentRemovesWhenNoPreviousExisted(t *testing.T) {
	// A failed first-time deploy must leave nothing behind — not the
	// new bad fragment, and not a phantom restored file.
	dir := t.TempDir()
	path := filepath.Join(dir, "new.caddy")
	if err := os.WriteFile(path, []byte("bad fragment that failed validate"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := restoreCaddyFragment(path, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected fragment removed, stat err = %v", err)
	}
}

func TestValidateAppEnvAcceptsCanonicalNames(t *testing.T) {
	if err := validateAppEnv("api", "production"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateAppEnv("multi-word-app", "stage-2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAppEnvRejectsLeadingDigitOrPunctuation(t *testing.T) {
	for _, name := range []string{"1bad", "-bad", "bad name", "BAD"} {
		if err := validateAppEnv(name, "production"); err == nil {
			t.Fatalf("expected error for app=%q", name)
		}
		if err := validateAppEnv("good", name); err == nil {
			t.Fatalf("expected error for env=%q", name)
		}
	}
}

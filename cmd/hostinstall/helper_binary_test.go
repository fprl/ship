package hostinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/version"
)

func TestPrepareRemoteHelperUsesConfiguredHelperDir(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "ship-linux-amd64")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	installer := NewInstaller()
	installer.Env = map[string]string{"SHIP_HELPER_DIR": dir}

	got, cleanup, err := installer.prepareRemoteHelperBinary("amd64")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if got != helper {
		t.Fatalf("expected configured helper %q, got %q", helper, got)
	}
}

func TestPrepareRemoteHelperDownloadsReleaseAsset(t *testing.T) {
	prev := version.Version
	version.Version = "v9.9.9"
	t.Cleanup(func() { version.Version = prev })

	helper := []byte("downloaded-helper")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v9.9.9/ship-linux-arm64":
			_, _ = w.Write(helper)
		case "/v9.9.9/SHA256SUMS":
			_, _ = w.Write([]byte(sha256SumLine("ship-linux-arm64", helper)))
		default:
			t.Fatalf("unexpected release asset path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	installer := NewInstaller()
	installer.Env = map[string]string{"SHIP_RELEASE_BASE_URL": server.URL}

	got, cleanup, err := installer.prepareRemoteHelperBinary("arm64")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(helper) {
		t.Fatalf("unexpected downloaded helper content: %q", data)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("expected helper mode 0755, got %v", info.Mode().Perm())
	}
}

func TestPrepareRemoteHelperReleaseBuildPrefersDownloadOverRepoRoot(t *testing.T) {
	prev := version.Version
	version.Version = "v9.9.9"
	t.Cleanup(func() { version.Version = prev })

	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "go.mod"), []byte("module invalid.example/ship\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHIP_REPO_ROOT", sourceDir)
	previousWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousWorkingDir); err != nil {
			t.Fatal(err)
		}
	})

	helper := []byte("downloaded-helper")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v9.9.9/ship-linux-amd64":
			_, _ = w.Write(helper)
		case "/v9.9.9/SHA256SUMS":
			_, _ = w.Write([]byte(sha256SumLine("ship-linux-amd64", helper)))
		default:
			t.Fatalf("unexpected release asset path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	installer := NewInstaller()
	installer.Env = map[string]string{"SHIP_RELEASE_BASE_URL": server.URL}

	got, cleanup, err := installer.prepareRemoteHelperBinary("amd64")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(helper) {
		t.Fatalf("expected downloaded helper, got %q", data)
	}
}

func TestPrepareRemoteHelperUsesReleaseToken(t *testing.T) {
	prev := version.Version
	version.Version = "v9.9.9"
	t.Cleanup(func() { version.Version = prev })

	helper := []byte("downloaded-helper")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		switch r.URL.Path {
		case "/v9.9.9/ship-linux-amd64":
			_, _ = w.Write(helper)
		case "/v9.9.9/SHA256SUMS":
			_, _ = w.Write([]byte(sha256SumLine("ship-linux-amd64", helper)))
		default:
			t.Fatalf("unexpected release asset path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	installer := NewInstaller()
	installer.Env = map[string]string{
		"SHIP_RELEASE_BASE_URL": server.URL,
		"GH_TOKEN":              "test-token",
	}

	_, cleanup, err := installer.prepareRemoteHelperBinary("amd64")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRemoteHelperFallsBackToGitHubAssetAPI(t *testing.T) {
	prev := version.Version
	version.Version = "v9.9.9"
	t.Cleanup(func() { version.Version = prev })

	var serverURL string
	helper := []byte("private-release-helper")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("unexpected authorization header: %q", got)
		}

		switch r.URL.Path {
		case "/v9.9.9/ship-linux-amd64", "/v9.9.9/SHA256SUMS":
			http.NotFound(w, r)
		case "/releases/tags/v9.9.9":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"assets":[{"name":"ship-linux-amd64","url":%q},{"name":"SHA256SUMS","url":%q}]}`, serverURL+"/assets/1", serverURL+"/assets/2")
		case "/assets/1":
			if got := r.Header.Get("Accept"); got != "application/octet-stream" {
				t.Fatalf("unexpected accept header: %q", got)
			}
			_, _ = w.Write(helper)
		case "/assets/2":
			if got := r.Header.Get("Accept"); got != "application/octet-stream" {
				t.Fatalf("unexpected accept header: %q", got)
			}
			_, _ = w.Write([]byte(sha256SumLine("ship-linux-amd64", helper)))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	serverURL = server.URL
	t.Cleanup(server.Close)

	installer := NewInstaller()
	installer.Env = map[string]string{
		"SHIP_RELEASE_BASE_URL":     server.URL,
		"SHIP_RELEASE_API_BASE_URL": server.URL,
		"GH_TOKEN":                  "test-token",
	}

	got, cleanup, err := installer.prepareRemoteHelperBinary("amd64")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(helper) {
		t.Fatalf("unexpected downloaded helper content: %q", data)
	}
}

func TestPrepareRemoteHelperRejectsChecksumMismatch(t *testing.T) {
	prev := version.Version
	version.Version = "v9.9.9"
	t.Cleanup(func() { version.Version = prev })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v9.9.9/ship-linux-amd64":
			_, _ = w.Write([]byte("downloaded-helper"))
		case "/v9.9.9/SHA256SUMS":
			_, _ = w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  ship-linux-amd64\n"))
		default:
			t.Fatalf("unexpected release asset path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	installer := NewInstaller()
	installer.Env = map[string]string{"SHIP_RELEASE_BASE_URL": server.URL}

	_, cleanup, err := installer.prepareRemoteHelperBinary("amd64")
	defer cleanup()
	if err == nil {
		t.Fatal("expected checksum mismatch to fail")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
}

func TestReleaseVersionDetection(t *testing.T) {
	for _, value := range []string{"v0.4.0", "v1.2.3-rc1"} {
		if !isReleaseVersion(value) {
			t.Fatalf("%q should be treated as a release version", value)
		}
	}
	for _, value := range []string{"dev", "v0.4.0-dirty", "v0.4.2-7-gc0c1510", "aeadd20"} {
		if isReleaseVersion(value) {
			t.Fatalf("%q should not be treated as a release version", value)
		}
	}
}

func sha256SumLine(name string, data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

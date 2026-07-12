package helper

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
)

func TestCreateBackupFailsWhenSecretsCannotBeReadWithoutArchive(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	secretsRoot := filepath.Join(root, "secrets")
	t.Setenv("SHIP_SECRETS_DIR", secretsRoot)
	if err := os.WriteFile(secretsRoot, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}

	const app = "api"
	const env = "production"
	const release = "abc1234"
	const route = "api.example.com"
	if err := os.MkdirAll(filepath.Dir(identity.ManifestFile(app, env)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identity.ManifestFile(app, env), []byte("name = \"api\"\nbox = \"example.com\"\n\n[routes]\n\"api.example.com\" = { static = \"dist\" }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	staticRelease := filepath.Join(identity.StaticDir(app, env), "releases", release, config.RouteStorageName(route))
	if err := os.MkdirAll(staticRelease, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(identity.StaticDir(app, env), "releases", release), filepath.Join(identity.StaticDir(app, env), "current")); err != nil {
		t.Fatal(err)
	}

	backupDir := filepath.Join(root, "backups")
	_, err := createBackup(app, env, backupDir, time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC))
	if !errcat.Is(err, errcat.CodeSecretReadError) {
		t.Fatalf("createBackup error = %v, want secret_read_error", err)
	}
	archives, err := filepath.Glob(filepath.Join(backupDir, "*.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) != 0 {
		t.Fatalf("backup must not leave an archive after a secret read error: %v", archives)
	}
}

func TestRestoreDryRunRequiresReleaseMetadataBeforeMutation(t *testing.T) {
	path := writeTestBackupTar(t, t.TempDir(), "missing-release.tar", nil)
	_, err := restoreBackup("api", "production", path, "", true)
	if err == nil || !strings.Contains(err.Error(), "backup release metadata") {
		t.Fatalf("expected missing release metadata error, got %v", err)
	}
}

func TestRestoreDryRunRejectsCorruptReleaseMetadata(t *testing.T) {
	path := writeTestBackupTar(t, t.TempDir(), "corrupt-release.tar", []byte("{not-json\n"))
	_, err := restoreBackup("api", "production", path, "", true)
	if err == nil || !strings.Contains(err.Error(), "parse release metadata") {
		t.Fatalf("expected corrupt release metadata error, got %v", err)
	}
}

func TestRestoreDryRunRejectsMismatchedReleaseMetadata(t *testing.T) {
	releaseMeta, err := newReleaseMetadata("def1234", false, "def1234def1234def1234def1234def1234def1234", "2026-05-30T14:30:12Z")
	if err != nil {
		t.Fatal(err)
	}
	path := writeTestBackupTarJSON(t, t.TempDir(), "mismatched-release.tar", releaseMeta)
	_, err = restoreBackup("api", "production", path, "", true)
	if err == nil || !strings.Contains(err.Error(), "release metadata names def1234, expected abc1234") {
		t.Fatalf("expected mismatched release metadata error, got %v", err)
	}
}

func TestBackupInfoForPathReadsRequiredReleaseMetadata(t *testing.T) {
	dir := t.TempDir()
	meta, err := newReleaseMetadata("abc1234", false, "abc1234abc1234abc1234abc1234abc1234abc1234", "2026-05-30T14:30:12Z")
	if err != nil {
		t.Fatal(err)
	}
	path := writeTestBackupTarJSON(t, dir, "20260530T143012Z-abc1234.tar", meta)
	info, err := backupInfoForPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Release != "abc1234" || info.CreatedAt != "2026-05-30T14:30:12Z" {
		t.Fatalf("unexpected backup info: %+v", info)
	}
}

func writeTestBackupTarJSON(t *testing.T, dir, name string, releaseMeta releaseMetadata) string {
	t.Helper()
	return writeTestBackupTarWithRelease(t, dir, name, func(tw *tar.Writer) error {
		return addJSON(tw, "release.json", releaseMeta)
	})
}

func writeTestBackupTar(t *testing.T, dir, name string, releaseData []byte) string {
	t.Helper()
	return writeTestBackupTarWithRelease(t, dir, name, func(tw *tar.Writer) error {
		if releaseData == nil {
			return nil
		}
		return writeTarFile(tw, "release.json", releaseData, 0600)
	})
}

func writeTestBackupTarWithRelease(t *testing.T, dir, name string, addRelease func(*tar.Writer) error) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	meta := backupMetadata{
		SchemaVersion: 1,
		App:           "api",
		Env:           "production",
		ID:            strings.TrimSuffix(name, ".tar"),
		CreatedAt:     time.Date(2026, 5, 30, 14, 30, 12, 0, time.UTC).Format(time.RFC3339),
		Release:       "abc1234",
		Shape:         "container",
		Processes:     []string{"web"},
	}
	if err := addJSON(tw, "metadata.json", meta); err != nil {
		t.Fatal(err)
	}
	if err := addJSON(tw, "secrets.json", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := writeTarFile(tw, "ship.toml", []byte("name = \"api\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "data/", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := addRelease(tw); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

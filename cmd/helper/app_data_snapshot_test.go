package helper

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
)

func TestRestoreDataSnapshotRejectsInvalidArchiveBeforeLiveDataMutation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_DEPLOY_TMP_DIR", root)
	dataDir := identity.DataDir("api", "production")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "keep.txt"), []byte("live"), 0644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "bad.data.tar.gz")
	if err := os.WriteFile(archive, []byte("not a gzip archive"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := restoreAppData("api", "production", archive)
	if !errcat.Is(err, errcat.CodeDataSnapshotInvalid) {
		t.Fatalf("restore error = %v, want data_snapshot_invalid", err)
	}
	assertLiveData(t, dataDir, "live")
}

func TestRestoreDataSnapshotRejectsMissingDataBeforeLiveDataMutation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_DEPLOY_TMP_DIR", root)
	dataDir := identity.DataDir("api", "production")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "keep.txt"), []byte("live"), 0644); err != nil {
		t.Fatal(err)
	}
	archive := writeDataSnapshotForTest(t, root, false, dataSnapshotMetadata{SchemaVersion: 1, App: "api", Env: "prod", Release: "abc1234", CreatedAt: time.Now().UTC().Format(time.RFC3339)})

	_, err := restoreAppData("api", "production", archive)
	if !errcat.Is(err, errcat.CodeDataSnapshotInvalid) {
		t.Fatalf("restore error = %v, want data_snapshot_invalid", err)
	}
	assertLiveData(t, dataDir, "live")
}

func TestRestoreDataSnapshotRejectsCorruptMetadataBeforeLiveDataMutation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_DEPLOY_TMP_DIR", root)
	dataDir := identity.DataDir("api", "production")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "keep.txt"), []byte("live"), 0644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "corrupt-metadata.data.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := writeDataTarFile(tw, "metadata.json", []byte("{not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "data/", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = restoreAppData("api", "production", archive)
	if !errcat.Is(err, errcat.CodeDataSnapshotInvalid) {
		t.Fatalf("restore error = %v, want data_snapshot_invalid", err)
	}
	assertLiveData(t, dataDir, "live")
}

func TestRestoreDataSnapshotRejectsAppMismatchBeforeLiveDataMutation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_DEPLOY_TMP_DIR", root)
	dataDir := identity.DataDir("api", "production")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "keep.txt"), []byte("live"), 0644); err != nil {
		t.Fatal(err)
	}
	archive := writeDataSnapshotForTest(t, root, true, dataSnapshotMetadata{SchemaVersion: 1, App: "other", Env: "prod", Release: "abc1234", CreatedAt: time.Now().UTC().Format(time.RFC3339)})

	_, err := restoreAppData("api", "production", archive)
	if !errcat.Is(err, errcat.CodeDataSnapshotInvalid) {
		t.Fatalf("restore error = %v, want data_snapshot_invalid", err)
	}
	assertLiveData(t, dataDir, "live")
}

func TestRestoreDataSnapshotStagingFailureLeavesLiveDataUntouched(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_DEPLOY_TMP_DIR", root)
	dataDir := identity.DataDir("api", "production")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "keep.txt"), []byte("live"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	archive := writeDataSnapshotForTest(t, root, true, dataSnapshotMetadata{SchemaVersion: 1, App: "api", Env: "prod", Release: "abc1234", CreatedAt: time.Now().UTC().Format(time.RFC3339)})

	if _, err := restoreAppData("api", "production", archive); err == nil {
		t.Fatal("expected staging failure")
	}
	assertLiveData(t, dataDir, "live")
}

func TestRestoreDataSnapshotRejectsArchiveOutsideDeployTmpWithoutRemovingIt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_DEPLOY_TMP_DIR", filepath.Join(root, "deploy-tmp"))
	archive := filepath.Join(root, "outside.data.tar.gz")
	if err := os.WriteFile(archive, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := restoreAppData("api", "production", archive); err == nil {
		t.Fatal("restore accepted archive outside deploy tmp")
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("outside archive was removed: %v", err)
	}
}

func TestSweepDataSnapshotStagingRemovesStaleDirectories(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	envRoot := identity.EnvRoot("api", "production")
	stale := []string{
		filepath.Join(envRoot, ".data-save-interrupted"),
		filepath.Join(envRoot, ".data-restore-interrupted"),
	}
	for _, dir := range stale {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "partial"), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(envRoot, "data")
	if err := os.MkdirAll(keep, 0755); err != nil {
		t.Fatal(err)
	}

	if err := sweepDataSnapshotStaging("api", "production"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range stale {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("stale staging %s still exists: %v", dir, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("kept data directory missing: %v", err)
	}
}

func writeDataSnapshotForTest(t *testing.T, dir string, dataDir bool, meta dataSnapshotMetadata) string {
	t.Helper()
	path := filepath.Join(dir, "snapshot.data.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDataTarFile(tw, "metadata.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	if dataDir {
		if err := tw.WriteHeader(&tar.Header{Name: "data/", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
		if err := writeDataTarFile(tw, "data/payload.txt", []byte("snapshot"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertLiveData(t *testing.T, dataDir, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dataDir, "keep.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("live data = %q, want %q", got, want)
	}
}

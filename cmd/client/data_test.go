package client

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
)

func TestRunDataSaveKeepsExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.data.tar.gz")
	runner := &fakeDataSaveRunner{metadata: dataSnapshotMetadata{App: "api", Env: "preview", Release: "archive-release"}}
	got, err := runDataSave(dataSaveContext{
		AppContext: &config.AppContext{AppName: "api", Server: "example.com"},
		EnvName:    "preview",
		Runner:     runner,
	}, path, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("output path = %q, want %q", got, path)
	}
}

func TestRunDataSaveDefaultNameUsesArchiveMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	now := time.Date(2026, time.July, 14, 12, 30, 45, 0, time.UTC)
	runner := &fakeDataSaveRunner{metadata: dataSnapshotMetadata{App: "api", Env: "preview-after-deploy", Release: "archive-release"}}
	got, err := runDataSave(dataSaveContext{
		AppContext: &config.AppContext{AppName: "api", Server: "example.com"},
		EnvName:    "preview-before-deploy",
		Runner:     runner,
	}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := "preview-after-deploy-archive-release-20260714T123045Z.data.tar.gz"; filepath.Base(got) != want {
		t.Fatalf("snapshot name = %q, want %q", filepath.Base(got), want)
	}
	if len(runner.commands) != 1 || runner.commands[0] != serverAppDataSaveCommand("api", "preview-before-deploy") {
		t.Fatalf("commands = %#v, want only snapshot stream", runner.commands)
	}
}

func TestDefaultDataSnapshotPathUsesProductionEnvName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	now := time.Date(2026, time.July, 14, 12, 30, 45, 0, time.UTC)
	path, err := defaultDataSnapshotPath("api", productionEnvName, "abc1234", now)
	if err != nil {
		t.Fatal(err)
	}
	// Collision suffixes are allocated only by claimDataSnapshotPath at
	// link time; the default name is always the plain stamp.
	if got := filepath.Base(path); got != "production-abc1234-20260714T123045Z.data.tar.gz" {
		t.Fatalf("snapshot name = %q", got)
	}
}

func TestClaimDataSnapshotPathDoesNotOverwriteExistingSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preview-abc1234-20260714T123045Z.data.tar.gz")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	tmpPath := filepath.Join(dir, ".snapshot.partial")
	if err := os.WriteFile(tmpPath, []byte("new snapshot"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := claimDataSnapshotPath(tmpPath, path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "preview-abc1234-20260714T123045Z-2.data.tar.gz")
	if got != want {
		t.Fatalf("snapshot path = %q, want %q", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatalf("existing snapshot = %q, want unchanged", data)
	}
	data, err = os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new snapshot" {
		t.Fatalf("saved snapshot = %q", data)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("temporary snapshot still exists: %v", err)
	}
}

func TestClaimDataSnapshotPathKeepsExplicitSuffixedName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db-3.data.tar.gz")
	tmpPath := filepath.Join(dir, ".snapshot.partial")
	if err := os.WriteFile(tmpPath, []byte("new snapshot"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := claimDataSnapshotPath(tmpPath, path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("snapshot path = %q, want the explicit name %q kept", got, path)
	}
}

func TestListDataSnapshotsReturnsEmptyJSONArrayForExistingEmptyDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	dir, err := dataSnapshotDir("api")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	items, err := listDataSnapshots("api")
	if err != nil {
		t.Fatal(err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("snapshots = %#v, want non-nil empty slice", items)
	}
	data, err := json.Marshal(struct {
		Snapshots []dataSnapshotInfo `json:"snapshots"`
	}{Snapshots: items})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"snapshots":[]}` {
		t.Fatalf("snapshots JSON = %s, want empty array", data)
	}
}

func TestRunDataRestoreCleansRemoteStagingOnFailure(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot.data.tar.gz")
	if err := os.WriteFile(snapshot, []byte("snapshot"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name         string
		uploadErr    error
		restoreFails bool
		wantCommands int
	}{
		{name: "upload failure", uploadErr: errors.New("rsync failed"), wantCommands: 2},
		{name: "restore failure", restoreFails: true, wantCommands: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeDataRestoreRunner{uploadErr: tt.uploadErr, restoreFails: tt.restoreFails}
			err := runDataRestore(dataRestoreContext{
				AppContext: &config.AppContext{AppName: "api", Server: "example.com"},
				EnvName:    "preview",
				Runner:     runner,
			}, snapshot, "")
			if err == nil {
				t.Fatal("restore error = nil, want failure")
			}
			if len(runner.commands) != tt.wantCommands {
				t.Fatalf("commands = %#v, want %d commands", runner.commands, tt.wantCommands)
			}
			cleanup := runner.commands[len(runner.commands)-1]
			if !strings.HasPrefix(cleanup, "rm -rf ") {
				t.Fatalf("cleanup command = %q", cleanup)
			}
			remoteDir := strings.TrimPrefix(cleanup, "rm -rf ")
			if !strings.Contains(runner.commands[0], remoteDir) {
				t.Fatalf("mkdir command %q did not use cleanup dir %q", runner.commands[0], remoteDir)
			}
		})
	}
}

func TestRunDataRestoreUsesRestoreConfirmationErrorOnProduction(t *testing.T) {
	err := runDataRestore(dataRestoreContext{
		AppContext: &config.AppContext{AppName: "api", Server: "example.com"},
		Address:    readAddress{ProductionBranch: true},
		EnvName:    "production",
		Runner:     &fakeDataRestoreRunner{},
	}, "backup-id", "")
	if !errcat.Is(err, errcat.CodeDataRestoreConfirmationRequired) {
		t.Fatalf("error = %v, want data_restore_confirmation_required", err)
	}
	if !strings.Contains(err.Error(), "Production restore requires --confirm api") || !strings.Contains(err.Error(), "ship data restore backup-id --confirm api") {
		t.Fatalf("confirmation error = %v", err)
	}
}

func TestDataPreviewURLReturnsLiveLookupError(t *testing.T) {
	runner := &fakeDataRunner{fakeSSHRunner: &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
		serverAppLsCommand(true): {{stderr: "lookup failed", code: 1}},
	}}}
	_, err := dataPreviewURL(dataContext{
		AppContext: &config.AppContext{AppName: "api", Server: "203.0.113.7"},
		EnvName:    "preview",
		Runner:     runner,
	})
	if err == nil || !strings.Contains(err.Error(), "lookup failed") {
		t.Fatalf("preview URL error = %v, want live lookup failure", err)
	}
	if len(runner.commands) != 1 || runner.commands[0] != serverAppLsCommand(true) {
		t.Fatalf("commands = %#v, want only live URL lookup", runner.commands)
	}
}

func TestDataPreviewURLFallsBackWhenLiveURLIsEmpty(t *testing.T) {
	runner := &fakeDataRunner{fakeSSHRunner: &fakeSSHRunner{responses: map[string]string{
		serverAppLsCommand(true): `{"apps":[{"app":"api","envs":[{"env":"preview"}]}]}`,
	}}}
	url, err := dataPreviewURL(dataContext{
		AppContext: &config.AppContext{
			AppName: "api",
			Server:  "203.0.113.7",
			Processes: map[string]config.Process{
				"web": {},
			},
		},
		EnvName: "preview",
		Runner:  runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://api-preview.203-0-113-7.sslip.io" {
		t.Fatalf("preview URL = %q", url)
	}
}

func TestRunDataForkPreservesSuccessfulMutationWhenURLLookupFails(t *testing.T) {
	runner := &fakeDataRunner{fakeSSHRunner: &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
		serverAppDataForkCommand("api", "preview"): {{stdout: `{"files":[{"path":"app.db","size":4,"sqlite":true}],"sqliteFiles":1}`}},
		serverAppLsCommand(true):                   {{stderr: "status lookup failed", code: 1}},
	}}}
	result, err := runDataFork(dataContext{
		AppContext:    &config.AppContext{AppName: "api", Server: "example.com"},
		PreviewBranch: "feature/data",
		EnvName:       "preview",
		Runner:        runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.URLLookupErr == nil || result.Summary.SQLiteFiles != 1 {
		t.Fatalf("result = %+v, want completed fork with URL lookup error", result)
	}
	stdout, stderr := renderDataForkOutput("feature/data", result)
	if stdout != "" {
		t.Fatalf("stdout = %q, want no URL when lookup failed", stdout)
	}
	for _, want := range []string{"Forked data for Preview feature/data\n", "app.db 4 bytes (sqlite)\n", DataForkPIINote + "\n", "warning: preview URL lookup failed: ", "next: ship status\n"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
}

func TestRunDataResetPreservesSuccessfulMutationWhenURLLookupFails(t *testing.T) {
	runner := &fakeDataRunner{fakeSSHRunner: &fakeSSHRunner{sequences: map[string][]fakeSSHResult{
		serverAppDataResetCommand("api", "preview"): {{stdout: "removed"}},
		serverAppLsCommand(true):                    {{stderr: "status lookup failed", code: 1}},
	}}}
	result, err := runDataReset(dataContext{
		AppContext:    &config.AppContext{AppName: "api", Server: "example.com"},
		PreviewBranch: "feature/data",
		EnvName:       "preview",
		Runner:        runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.URLLookupErr == nil {
		t.Fatalf("result = %+v, want URL lookup error", result)
	}
	stdout, stderr := renderDataResetOutput("feature/data", result)
	if stdout != "" {
		t.Fatalf("stdout = %q, want no URL when lookup failed", stdout)
	}
	if want := "Reset data for Preview feature/data\nwarning: preview URL lookup failed: "; !strings.Contains(stderr, want) || !strings.Contains(stderr, "next: ship status\n") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestDataForkAndResetOutputKeepStdoutToPreviewURL(t *testing.T) {
	forkStdout, forkStderr := renderDataForkOutput("feature/data", dataForkResult{
		Summary: dataForkSummary{Files: []dataForkFile{{Path: "app.db", Size: 4, SQLite: true}}, SQLiteFiles: 1},
		URL:     "https://preview.example.com",
	})
	if forkStdout != "https://preview.example.com\n" || strings.Contains(forkStderr, "preview:") {
		t.Fatalf("fork streams:\nstdout=%q\nstderr=%q", forkStdout, forkStderr)
	}
	resetStdout, resetStderr := renderDataResetOutput("feature/data", dataResetResult{URL: "https://preview.example.com"})
	if resetStdout != "https://preview.example.com\n" || resetStderr != "Reset data for Preview feature/data\n" {
		t.Fatalf("reset streams:\nstdout=%q\nstderr=%q", resetStdout, resetStderr)
	}
}

type fakeDataRestoreRunner struct {
	commands     []string
	uploadErr    error
	restoreFails bool
}

func (f *fakeDataRestoreRunner) RunSSH(_ string, command string) (string, string, int, error) {
	f.commands = append(f.commands, command)
	if f.restoreFails && strings.Contains(command, " app data restore ") {
		return "", "restore failed", 1, nil
	}
	return "", "", 0, nil
}

func (f *fakeDataRestoreRunner) Upload(_, _, _ string) error {
	return f.uploadErr
}

type fakeDataRunner struct {
	*fakeSSHRunner
}

func (f *fakeDataRunner) Close() {}

type fakeDataSaveRunner struct {
	commands []string
	metadata dataSnapshotMetadata
}

func (f *fakeDataSaveRunner) RunSSHToFile(_ string, command, path string) (string, string, int, error) {
	f.commands = append(f.commands, command)
	if err := writeClientSnapshotMetadata(path, f.metadata); err != nil {
		return "", "", 1, err
	}
	return "", "", 0, nil
}

func writeClientSnapshotMetadata(path string, metadata dataSnapshotMetadata) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "metadata.json", Mode: 0600, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err = tw.Write(data)
	return err
}

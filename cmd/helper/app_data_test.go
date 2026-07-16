package helper

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/identity"
)

func TestIsSQLiteDataFileRequiresSQLiteExtensionAndMagic(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"app.db", sqliteHeaderMagic + "payload", true},
		{"app.sqlite", sqliteHeaderMagic + "payload", true},
		{"app.sqlite3", sqliteHeaderMagic + "payload", true},
		{"app.db", "not sqlite", false},
		{"empty.db", "", false},
		{"app.txt", sqliteHeaderMagic + "payload", false},
		{"app.sqlite-wal", sqliteHeaderMagic + "payload", false},
	}
	for i, tt := range tests {
		t.Run(tt.name+"_"+string(rune('a'+i)), func(t *testing.T) {
			ext := filepath.Ext(tt.name)
			base := strings.TrimSuffix(tt.name, ext)
			path := filepath.Join(dir, base+"-"+string(rune('a'+i))+ext)
			if err := os.WriteFile(path, []byte(tt.data), 0644); err != nil {
				t.Fatal(err)
			}
			got, err := isSQLiteDataFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("isSQLiteDataFile(%s) = %t, want %t", tt.name, got, tt.want)
			}
		})
	}
}

func TestForkAppDataVacuumCopiesSQLiteUploadsAndLeavesProdUnchanged(t *testing.T) {
	requireSQLite3(t)
	setupDataHostTest(t)
	app := "dataapi"
	prod := productionEnvName
	preview := "feature-data-abcd"
	writeDataPreviewIdentity(t, app, preview, "feature/data")
	prodDir := identity.DataDir(app, prod)
	previewDir := identity.DataDir(app, preview)
	if err := os.MkdirAll(filepath.Join(prodDir, "uploads"), 0775); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previewDir, 0775); err != nil {
		t.Fatal(err)
	}
	runSQLite(t, filepath.Join(prodDir, "app.db"), "CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO items(name) VALUES ('a'), ('b'), ('c');")
	if err := os.WriteFile(filepath.Join(prodDir, "uploads", "avatar.txt"), []byte("prod-upload"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previewDir, "old.txt"), []byte("old-preview"), 0644); err != nil {
		t.Fatal(err)
	}
	prodBefore := hashTree(t, prodDir)

	summary, err := forkAppData(app, preview, dataForkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.SQLiteFiles != 1 || len(summary.Files) != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if got := sqliteScalar(t, filepath.Join(previewDir, "app.db"), "SELECT count(*) FROM items;"); got != "3" {
		t.Fatalf("preview row count = %s, want 3", got)
	}
	if got := strings.TrimSpace(readFileForDataTest(t, filepath.Join(previewDir, "uploads", "avatar.txt"))); got != "prod-upload" {
		t.Fatalf("preview upload = %q", got)
	}
	if _, err := os.Stat(filepath.Join(previewDir, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old preview data should be gone after fork, stat err=%v", err)
	}
	if prodAfter := hashTree(t, prodDir); prodAfter != prodBefore {
		t.Fatalf("prod data changed:\nbefore %s\nafter  %s", prodBefore, prodAfter)
	}
}

func TestForkAppDataPreservesSymlinksWithoutFollowingThemAsRoot(t *testing.T) {
	setupDataHostTest(t)
	app := "linkapi"
	preview := "feature-link-abcd"
	writeDataPreviewIdentity(t, app, preview, "feature/link")
	prodDir := identity.DataDir(app, productionEnvName)
	previewDir := identity.DataDir(app, preview)
	if err := os.MkdirAll(prodDir, 0775); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previewDir, 0775); err != nil {
		t.Fatal(err)
	}

	// A root-readable secret outside the app's /data. The app user can plant
	// a symlink to it; snapshotting must not follow the link and copy its
	// contents (the .db name would otherwise route through the sqlite path
	// and be read by root).
	secret := filepath.Join(t.TempDir(), "root-secret")
	if err := os.WriteFile(secret, []byte("TOP-SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(prodDir, "leak.db")); err != nil {
		t.Fatal(err)
	}

	summary, err := forkAppData(app, preview, dataForkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.SQLiteFiles != 0 {
		t.Fatalf("symlink must not be treated as sqlite: %+v", summary)
	}

	copied := filepath.Join(previewDir, "leak.db")
	info, err := os.Lstat(copied)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("leak.db should remain a symlink, got mode %v", info.Mode())
	}
	if target, err := os.Readlink(copied); err != nil || target != secret {
		t.Fatalf("symlink target = %q err=%v, want %q", target, err, secret)
	}
	// The snapshot stored a link, not the secret's bytes: the entry is not a
	// regular file, so root never copied the target's contents into it.
	if info.Mode().IsRegular() {
		t.Fatal("snapshot copied the secret file contents through the symlink")
	}
}

func TestForkAppDataFailureBeforeSwapLeavesPreviewDataIntact(t *testing.T) {
	requireSQLite3(t)
	setupDataHostTest(t)
	app := "crashapi"
	writeDataPreviewIdentity(t, app, "feature-crash-abcd", "feature/crash")
	prodDir := identity.DataDir(app, productionEnvName)
	previewDir := identity.DataDir(app, "feature-crash-abcd")
	if err := os.MkdirAll(prodDir, 0775); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previewDir, 0775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prodDir, "prod.txt"), []byte("prod"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previewDir, "keep.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	wantPreview := hashTree(t, previewDir)
	errBoom := errors.New("boom before swap")

	_, err := forkAppData(app, "feature-crash-abcd", dataForkOptions{BeforeSwap: func() error {
		return errBoom
	}})
	if !errors.Is(err, errBoom) {
		t.Fatalf("fork error = %v, want %v", err, errBoom)
	}
	if gotPreview := hashTree(t, previewDir); gotPreview != wantPreview {
		t.Fatalf("preview changed after pre-swap failure:\nbefore %s\nafter  %s", wantPreview, gotPreview)
	}
	if _, err := os.Stat(filepath.Join(previewDir, "prod.txt")); !os.IsNotExist(err) {
		t.Fatalf("failed fork should not expose prod file in preview, stat err=%v", err)
	}
}

func TestResetAppDataRestartsContainersStoppedBeforeStopFailure(t *testing.T) {
	setupDataHostTest(t)
	app := "dataapi"
	env := "feature-data-abcd"
	writeDataPreviewIdentity(t, app, env, "feature/data")
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	logPath := filepath.Join(t.TempDir(), "podman.log")
	t.Setenv("PODMAN_LOG", logPath)
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
printf '%s %s\n' "$1" "$2" >> "$PODMAN_LOG"
case "$1" in
  ps)
    printf '%s\n' '[{"Names":["web-a"],"State":"running","Labels":{"ship.process":"web"}},{"Names":["web-b"],"State":"running","Labels":{"ship.process":"web"}}]'
    exit 0
    ;;
  stop)
    if [ "$2" = "web-b" ]; then
      echo "forced stop failure" >&2
      exit 1
    fi
    exit 0
    ;;
  start) exit 0 ;;
esac
exit 0
`)

	err := resetAppData(app, env)
	if err == nil || !strings.Contains(err.Error(), "stop web-b") {
		t.Fatalf("reset data error = %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stop web-a", "stop web-b", "start web-a"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("podman calls missing %q:\n%s", want, log)
		}
	}
}

func setupDataHostTest(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	writeFakeCommand(t, bin, "chmod", "#!/usr/bin/env sh\nexit 0\n")
	writeFakeCommand(t, bin, "podman", `#!/usr/bin/env sh
case "$1" in
  ps) printf '[]\n'; exit 0 ;;
  stop|start) exit 0 ;;
esac
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeDataPreviewIdentity(t *testing.T, app, env, branch string) {
	t.Helper()
	if err := writeEnvIdentityWithPreview(app, env, &identity.PreviewIdentity{
		Branch: branch,
	}); err != nil {
		t.Fatal(err)
	}
}

func requireSQLite3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
}

func runSQLite(t *testing.T, db, sql string) {
	t.Helper()
	cmd := exec.Command("sqlite3", "-batch", "-init", "/dev/null", db, sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3 failed: %v\n%s", err, out)
	}
}

func sqliteScalar(t *testing.T, db, sql string) string {
	t.Helper()
	out, err := exec.Command("sqlite3", "-batch", "-init", "/dev/null", "-noheader", "-cmd", ".mode list", db, sql).CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite3 scalar failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func hashTree(t *testing.T, root string) string {
	t.Helper()
	sum := sha256.New()
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		_, _ = sum.Write([]byte(filepath.ToSlash(rel) + "\x00" + info.Mode().String() + "\x00"))
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, _ = sum.Write(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func readFileForDataTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

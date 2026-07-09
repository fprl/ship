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

	summary, err := forkAppData(app, prod, preview, dataForkOptions{})
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

	_, err := forkAppData(app, productionEnvName, "feature-crash-abcd", dataForkOptions{BeforeSwap: func() error {
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
		Env:    env,
		Suffix: "abcd",
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

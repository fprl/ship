package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteSyncsParentDirectoryAfterRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active.json")
	called := false
	previous := syncParentDirectory
	syncParentDirectory = func(got string) error {
		called = true
		if got != dir {
			t.Fatalf("parent dir = %q, want %q", got, dir)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("renamed file was not visible before directory sync: %v", err)
		}
		return nil
	}
	t.Cleanup(func() { syncParentDirectory = previous })

	if err := AtomicWrite(path, []byte("active\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("parent-directory fsync path was not exercised")
	}
}

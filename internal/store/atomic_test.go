package store

import (
	"errors"
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

func TestAtomicWriteReportsPublishedWhenDirectorySyncFailsAfterRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active.json")
	calls := 0
	previous := syncParentDirectory
	syncParentDirectory = func(string) error {
		calls++
		return os.ErrPermission
	}
	t.Cleanup(func() { syncParentDirectory = previous })

	result := AtomicWriteResult(path, []byte("active\n"), 0644)
	if !result.Published || result.Durable || result.Err == nil {
		t.Fatalf("result = %+v, want published and non-durable error", result)
	}
	if calls != 2 {
		t.Fatalf("directory sync calls = %d, want one retry", calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("published file missing after durability failure: %v", err)
	}
	var published PublishedWriteError
	if !errors.As(result.Err, &published) || !published.Published || published.Durable {
		t.Fatalf("error = %T %v, want published write error", result.Err, result.Err)
	}
}

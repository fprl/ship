package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const jsonIndent = "  "

var syncParentDirectory = func(dir string) error {
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync parent directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close parent directory: %w", err)
	}
	return nil
}

type PublishedWriteError struct {
	Published bool
	Durable   bool
	Err       error
}

func (e PublishedWriteError) Error() string { return e.Err.Error() }
func (e PublishedWriteError) Unwrap() error { return e.Err }

func readJSON(path string, dest any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("invalid %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSON(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", jsonIndent)
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), mode)
}

func AtomicWrite(path string, content []byte, mode os.FileMode) error {
	return AtomicWritePrepared(path, content, mode, nil)
}

// AtomicWritePrepared writes and syncs content, applies mode, lets the
// caller prepare the temporary inode, and only then renames it into place.
// The prepare hook is deliberately before Rename so ownership and other
// inode properties are part of the atomic publish boundary.
func AtomicWritePrepared(path string, content []byte, mode os.FileMode, prepare func(string) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if _, err := os.Stat(tmpName); err == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if prepare != nil {
		if err := prepare(tmpName); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := syncParentDirectory(dir); err != nil {
		first := err
		if retryErr := syncParentDirectory(dir); retryErr != nil {
			return PublishedWriteError{Published: true, Durable: false, Err: fmt.Errorf("parent directory durability failed after publish: %v; retry: %w", first, retryErr)}
		}
	}
	return nil
}

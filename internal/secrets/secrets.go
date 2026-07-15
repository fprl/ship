// Package secrets is the host-side per-(app, env, key) secret store
// for bare `@secret` references resolved by `server app apply`.
//
// Storage shape: one file per secret, value verbatim, no metadata.
//
//	/etc/ship/secrets                       mode 0700, root:root
//	/etc/ship/secrets/<app>/<env>/<key>     mode 0600, root:root
//
// `key` is the env-var name (`DATABASE_URL`, `STRIPE_KEY`, ...) — the
// validator at the call site (`SecretKeyRe`) keeps it filesystem-safe
// so it can be used directly as the filename. The dir tree is created
// on demand with the same 0700 mode so `ls /etc/ship/secrets/`
// from any non-root account fails before it can enumerate apps.
//
// What this package deliberately does NOT do:
//
//   - No rotation / versioning. Future ADR territory.
//   - No implicit dotenv push. Per-key set reads from stdin; explicit
//     `ship secret set --from` imports dotenv KEY=VALUE pairs from a file.
//   - No printing of values in client surfaces. List returns names
//     only; Get is helper-internal (called by `app apply`).
//
// Atomicity: Put writes to a sibling temp file and renames into place,
// so a reader (or `app apply` resolution) never sees a partial value.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/names"
)

// SecretKeyRe matches the env-var grammar. Callers must validate
// before reaching the filesystem.
var SecretKeyRe = names.EnvKeyRe

// Default location. Override with SHIP_SECRETS_DIR for tests so
// they don't need root to exercise the real path layout.
const defaultRoot = "/etc/ship/secrets"

func root() string {
	if v := os.Getenv("SHIP_SECRETS_DIR"); v != "" {
		return v
	}
	return defaultRoot
}

func RootDir() string {
	return root()
}

// Path returns the on-disk location for one (app, env, key) triple.
// Pure — does not touch the filesystem.
func Path(app, env, key string) string {
	return filepath.Join(root(), app, env, key)
}

// EnvDir returns the directory containing every key for one
// (app, env) pair. Pure.
func EnvDir(app, env string) string {
	return filepath.Join(root(), app, env)
}

// ValidateKey rejects anything that wouldn't be a safe filename or a
// valid env-var name. Callers should run this before Put/Get/Rm.
func ValidateKey(key string) error {
	if !SecretKeyRe.MatchString(key) {
		return fmt.Errorf("invalid secret key %q: must match %s", key, SecretKeyRe.String())
	}
	return nil
}

// Put atomically writes `value` as the secret for (app, env, key).
// Creates the per-(app, env) directory tree on demand. Root-owned,
// mode 0600.
func Put(app, env, key string, value []byte) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if err := validateValue(value); err != nil {
		return err
	}
	dir := EnvDir(app, env)
	// Per-(app, env) dirs are root-only too so non-root accounts on
	// the box can't enumerate keys or stat individual secrets.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create secret dir %s: %w", dir, err)
	}
	target := filepath.Join(dir, key)
	tmp, err := os.CreateTemp(dir, ".secret-")
	if err != nil {
		return fmt.Errorf("create secret tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if we error out mid-write.
	defer func() { _ = os.Remove(tmpPath) }()
	if err := os.Chmod(tmpPath, 0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod secret tempfile: %w", err)
	}
	if _, err := tmp.Write(value); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write secret tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close secret tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("rename secret into place: %w", err)
	}
	return nil
}

// Get returns the value for (app, env, key). Returns ErrNotFound if
// the secret doesn't exist; other errors propagate as-is.
func Get(app, env, key string) ([]byte, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(Path(app, env, key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

// PutPreviewCapability atomically writes the internal preview capability. Its
// dashed filename deliberately cannot collide with a user-managed env key.
func PutPreviewCapability(app, env string, value []byte) error {
	if err := validateValue(value); err != nil {
		return err
	}
	return putInternalEnvFile(app, env, "capability-token", value)
}

// GetPreviewCapability returns the internal preview capability for one env.
func GetPreviewCapability(app, env string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(EnvDir(app, env), "capability-token"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

// RmPreviewCapability removes the internal preview capability for one env.
func RmPreviewCapability(app, env string) error {
	if err := os.Remove(filepath.Join(EnvDir(app, env), "capability-token")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// Rm removes the secret for (app, env, key). Returns ErrNotFound if
// it wasn't there to begin with — callers can treat that as success
// or report a "wasn't set" message.
func Rm(app, env, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if err := os.Remove(Path(app, env, key)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// List returns every key currently stored for (app, env), sorted.
// Empty slice (not an error) when the per-(app, env) dir is missing.
func List(app, env string) ([]string, error) {
	entries, err := os.ReadDir(EnvDir(app, env))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		name := e.Name()
		// Skip in-flight tempfiles from concurrent Puts.
		if strings.HasPrefix(name, ".secret-") {
			continue
		}
		// Reject any filename that wouldn't match the validator —
		// belt-and-suspenders against manual fs edits.
		if !SecretKeyRe.MatchString(name) {
			continue
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys, nil
}

// ErrNotFound is returned by Get and Rm when a key is missing. Use
// errors.Is to detect.
var ErrNotFound = errors.New("secret not found")

func validateValue(value []byte) error {
	// NUL bytes break the env file consumer (Podman --env-file rejects
	// them; runtime env-var APIs treat them as terminators). Reject at
	// the door rather than mid-deploy.
	for _, b := range value {
		if b == 0 {
			return errors.New("secret value cannot contain NUL bytes")
		}
	}
	return nil
}

func putInternalEnvFile(app, env, name string, value []byte) error {
	dir := EnvDir(app, env)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create secret dir %s: %w", dir, err)
	}
	target := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, ".secret-")
	if err != nil {
		return fmt.Errorf("create secret tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := os.Chmod(tmpPath, 0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod secret tempfile: %w", err)
	}
	if _, err := tmp.Write(value); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write secret tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close secret tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("rename secret into place: %w", err)
	}
	return nil
}

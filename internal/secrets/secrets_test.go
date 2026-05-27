package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SIMPLE_VPS_SECRETS_DIR", root)
	return root
}

func TestPutThenGetRoundTrips(t *testing.T) {
	withRoot(t)
	if err := Put("api", "production", "DATABASE_URL", []byte("postgres://x")); err != nil {
		t.Fatal(err)
	}
	got, err := Get("api", "production", "DATABASE_URL")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "postgres://x" {
		t.Fatalf("got %q, want %q", got, "postgres://x")
	}
}

func TestPutCreatesPerEnvDirsWith0700(t *testing.T) {
	root := withRoot(t)
	if err := Put("api", "production", "K", []byte("v")); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"api", "api/production"} {
		st, err := os.Stat(filepath.Join(root, sub))
		if err != nil {
			t.Fatalf("missing dir %s: %v", sub, err)
		}
		if st.Mode().Perm() != 0700 {
			t.Fatalf("dir %s mode %o, want 0700", sub, st.Mode().Perm())
		}
	}
}

func TestPutWritesSecretFileWith0600(t *testing.T) {
	root := withRoot(t)
	if err := Put("api", "production", "K", []byte("v")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(root, "api", "production", "K"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("secret mode %o, want 0600", st.Mode().Perm())
	}
}

func TestPutPreservesValueBytesExactly(t *testing.T) {
	withRoot(t)
	// No trimming, no encoding, no munging. What the caller wrote is
	// what `app apply` will inject into the env file.
	cases := [][]byte{
		[]byte("plain"),
		[]byte("with newline\n"),
		[]byte(""), // empty value is legal
		[]byte("=equals=and:colons"),
		[]byte("with spaces and  tabs\t"),
	}
	for _, v := range cases {
		if err := Put("api", "production", "K", v); err != nil {
			t.Fatalf("Put(%q): %v", v, err)
		}
		got, err := Get("api", "production", "K")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, v) {
			t.Fatalf("round-trip mismatch:\nwant: %q\n got: %q", v, got)
		}
	}
}

func TestPutIsAtomicAcrossRewrites(t *testing.T) {
	root := withRoot(t)
	if err := Put("api", "production", "K", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := Put("api", "production", "K", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	got, _ := Get("api", "production", "K")
	if string(got) != "v2" {
		t.Fatalf("got %q after rewrite, want v2", got)
	}
	// No stray tempfiles left behind.
	entries, _ := os.ReadDir(filepath.Join(root, "api", "production"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".secret-") {
			t.Fatalf("leaked tempfile: %s", e.Name())
		}
	}
}

func TestPutRejectsInvalidKey(t *testing.T) {
	withRoot(t)
	for _, key := range []string{"", "1BAD", "with space", "../etc", "a/b", "abc!"} {
		if err := Put("api", "production", key, []byte("v")); err == nil {
			t.Fatalf("Put(%q) should have rejected", key)
		}
	}
}

func TestPutRejectsNULByteInValue(t *testing.T) {
	withRoot(t)
	err := Put("api", "production", "K", []byte("nul\x00inside"))
	if err == nil {
		t.Fatal("Put should reject NUL bytes")
	}
}

func TestGetReturnsErrNotFoundForMissingSecret(t *testing.T) {
	withRoot(t)
	_, err := Get("api", "production", "MISSING")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRmRemovesSecret(t *testing.T) {
	withRoot(t)
	_ = Put("api", "production", "K", []byte("v"))
	if err := Rm("api", "production", "K"); err != nil {
		t.Fatal(err)
	}
	_, err := Get("api", "production", "K")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after Rm, got %v", err)
	}
}

func TestRmReturnsErrNotFoundWhenAbsent(t *testing.T) {
	withRoot(t)
	if err := Rm("api", "production", "MISSING"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListReturnsSortedKeys(t *testing.T) {
	withRoot(t)
	_ = Put("api", "production", "ZED", []byte("z"))
	_ = Put("api", "production", "ALPHA", []byte("a"))
	_ = Put("api", "production", "MIKE", []byte("m"))

	got, err := List("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ALPHA", "MIKE", "ZED"}
	if len(got) != len(want) {
		t.Fatalf("len got %d want %d (%v)", len(got), len(want), got)
	}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("List[%d] = %q, want %q", i, got[i], k)
		}
	}
}

func TestListReturnsEmptyForUnknownEnv(t *testing.T) {
	withRoot(t)
	got, err := List("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing env dir, got %v", got)
	}
}

func TestListIgnoresStaleTempfiles(t *testing.T) {
	root := withRoot(t)
	dir := filepath.Join(root, "api", "production")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "REAL"), []byte("v"), 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate a Put that crashed before rename.
	if err := os.WriteFile(filepath.Join(dir, ".secret-leftover"), []byte("v"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := List("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "REAL" {
		t.Fatalf("expected only [REAL], got %v", got)
	}
}

func TestPerAppPerEnvScoping(t *testing.T) {
	// `(app, env)` is the scope. Same KEY in different envs holds
	// different values; same KEY in different apps too.
	withRoot(t)
	_ = Put("api", "production", "K", []byte("prod-val"))
	_ = Put("api", "staging", "K", []byte("stg-val"))
	_ = Put("worker", "production", "K", []byte("worker-val"))

	prod, _ := Get("api", "production", "K")
	stg, _ := Get("api", "staging", "K")
	worker, _ := Get("worker", "production", "K")
	if string(prod) != "prod-val" || string(stg) != "stg-val" || string(worker) != "worker-val" {
		t.Fatalf("scoping broken: prod=%q staging=%q worker=%q", prod, stg, worker)
	}
}

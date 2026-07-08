package shipidentity

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
)

func TestEnsureShipIdentityCreatesEd25519KeyWithSanitizedGitName(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer

	identity, err := EnsureShipIdentity(Options{
		HomeDir:     home,
		Output:      &out,
		GitUserName: func() string { return "Franco Pablo!! Roman" },
		Env:         map[string]string{"USER": "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if identity.Name != "franco-pablo-roman" {
		t.Fatalf("identity name = %q", identity.Name)
	}
	if out.String() != "identity: franco-pablo-roman (created ~/.ssh/ship)\n" {
		t.Fatalf("unexpected narration: %q", out.String())
	}
	assertFileMode(t, filepath.Join(home, ".ssh", "ship"), 0600)
	assertFileMode(t, filepath.Join(home, ".ssh", "ship.pub"), 0644)
	if !strings.HasPrefix(identity.PublicKeyLine, "ssh-ed25519 ") {
		t.Fatalf("public key should be ed25519, got %q", identity.PublicKeyLine)
	}
	if PublicKeyComment(identity.PublicKeyLine) != identity.Name {
		t.Fatalf("public key comment did not round-trip: %q", identity.PublicKeyLine)
	}
}

func TestEnsureShipIdentityFallsBackToUserAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	var first bytes.Buffer

	created, err := EnsureShipIdentity(Options{
		HomeDir:     home,
		Output:      &first,
		GitUserName: func() string { return "" },
		Env:         map[string]string{"USER": "CI User!!"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "ci-user" {
		t.Fatalf("fallback name = %q", created.Name)
	}
	if !created.Created {
		t.Fatal("first ensure should report Created")
	}

	var second bytes.Buffer
	existing, err := EnsureShipIdentity(Options{
		HomeDir:     home,
		Output:      &second,
		GitUserName: func() string { return "Different Person" },
		Env:         map[string]string{"USER": "different"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if existing.Created {
		t.Fatal("second ensure should not report Created")
	}
	if existing.Name != "ci-user" {
		t.Fatalf("second ensure should read name from ship.pub, got %q", existing.Name)
	}
	if second.Len() != 0 {
		t.Fatalf("second ensure should print nothing, got %q", second.String())
	}
}

func TestEnsureShipIdentityReturnsErrcatWhenSSHDirCannotBeCreated(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".ssh"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := EnsureShipIdentity(Options{
		HomeDir:     home,
		GitUserName: func() string { return "Franco" },
		Env:         map[string]string{"USER": "franco"},
	})
	if err == nil {
		t.Fatal("expected identity creation error")
	}
	if !errcat.Is(err, errcat.CodeOperationFailed) {
		t.Fatalf("expected errcat operation_failed, got %v", err)
	}
	if !strings.Contains(err.Error(), "mkdir -p ~/.ssh && chmod 700 ~/.ssh") {
		t.Fatalf("expected remediation, got:\n%v", err)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

package knownhosts

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	hostKeyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/"
	hostKeyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICtppnbbz76teU3iU6BguTmo//WITtYN35e4gSER6UNt"
)

func TestEnsureCreatesShipKnownHostsWithPrivateModes(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	path, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(configHome, "ship", "known_hosts") {
		t.Fatalf("path = %q", path)
	}
	assertMode(t, filepath.Dir(path), 0700)
	assertMode(t, path, 0600)
}

func TestListHostsParsesKnownHostsFile(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"203.0.113.7 " + hostKeyA,
		"fake-vps,192.0.2.10 " + hostKeyA,
		"[example.com]:2222 " + hostKeyB,
		"|1|hashed|entry " + hostKeyB,
		"203.0.113.7 " + hostKeyA,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := ListHosts()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"203.0.113.7", "fake-vps", "192.0.2.10", "example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
}

func TestReconcilePinsOnlyAfterSetupProvidesHostKey(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("other.example "+hostKeyA+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(temp, []byte("203.0.113.7 "+hostKeyB+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := Reconcile("203.0.113.7", temp)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("first pin should not be reported as changed")
	}
	data := readKnownHosts(t, path)
	if !strings.Contains(data, "other.example "+hostKeyA) || !strings.Contains(data, "203.0.113.7 "+hostKeyB) {
		t.Fatalf("unexpected known_hosts:\n%s", data)
	}
}

func TestReconcileReportsChangedKeyAndReplacesOldEntries(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("203.0.113.7 "+hostKeyA+"\nother.example "+hostKeyA+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(temp, []byte("203.0.113.7 "+hostKeyB+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := Reconcile("203.0.113.7", temp)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed key should be reported")
	}
	data := readKnownHosts(t, path)
	if strings.Contains(data, "203.0.113.7 "+hostKeyA) {
		t.Fatalf("old host key should be removed:\n%s", data)
	}
	if !strings.Contains(data, "203.0.113.7 "+hostKeyB) || !strings.Contains(data, "other.example "+hostKeyA) {
		t.Fatalf("new host key and unrelated entries should remain:\n%s", data)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("203.0.113.7 "+hostKeyA+"\nother.example "+hostKeyB+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	removed, err := Remove("203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("expected host entry to be removed")
	}
	removed, err = Remove("203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("second remove should be a no-op")
	}
	if got := readKnownHosts(t, path); got != "other.example "+hostKeyB+"\n" {
		t.Fatalf("known_hosts = %q", got)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func readKnownHosts(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

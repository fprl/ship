package helper

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/deploybundle"
)

func TestAppIngestRemovesPrivateDirectoryWhenReceiveFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_DEPLOY_TMP_DIR", root)
	payload := []byte("not a tar archive")
	digest := sha256.Sum256(payload)
	cmd := appIngestCmd{
		App: "api", Env: "production", SHA: "abc1234",
		BaseCommit: "abc1234" + strings.Repeat("a", 33), CreatedAt: "2026-07-18T12:00:00Z",
		BundleSize: int64(len(payload)), BundleSHA256: hex.EncodeToString(digest[:]),
		Input: bytes.NewReader(payload),
	}
	if err := cmd.Run(); err == nil || !strings.Contains(err.Error(), "read deploy bundle") {
		t.Fatalf("Run() = %v, want invalid bundle", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("private ingest directory leaked after failure: %v", entries)
	}
}

func TestAppIngestRemovesPrivateDirectoryWhenApplyFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_DEPLOY_TMP_DIR", root)
	previousAuthorize := authorizeAppApply
	authorizeAppApply = func(helperVerb, authTarget) (serverMember, error) { return serverMember{}, nil }
	t.Cleanup(func() { authorizeAppApply = previousAuthorize })
	previousRun := runIngestApply
	runIngestApply = func(*appApplyCmd) error { return errors.New("build failed") }
	t.Cleanup(func() { runIngestApply = previousRun })

	source := filepath.Join(t.TempDir(), deploybundle.SourceName)
	manifest := filepath.Join(t.TempDir(), deploybundle.ManifestName)
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("[app]\nname = \"api\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "deploy.tar")
	metadata, err := deploybundle.Write(bundlePath, source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	cmd := appIngestCmd{
		App: "api", Env: "production", SHA: "abc1234",
		BaseCommit: "abc1234" + strings.Repeat("a", 33), CreatedAt: "2026-07-18T12:00:00Z",
		BundleSize: metadata.Size, BundleSHA256: metadata.SHA256, Input: input,
	}
	if err := cmd.Run(); err == nil || err.Error() != "build failed" {
		t.Fatalf("Run() = %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("private ingest directory leaked after apply failure: %v", entries)
	}
}

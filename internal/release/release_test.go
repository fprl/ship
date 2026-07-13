package release

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestVerifyAssetChecksumRejectsTamperedArtifact(t *testing.T) {
	good := []byte("official helper")
	sum := sha256.Sum256(good)
	err := VerifyAssetChecksum("ship-linux-amd64", []byte("tampered helper"), []byte(hex.EncodeToString(sum[:])+"  ship-linux-amd64\n"))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("VerifyAssetChecksum error = %v, want checksum mismatch", err)
	}
}

func TestIsVersionRejectsDevelopmentBuilds(t *testing.T) {
	if !IsVersion("v0.4.1") {
		t.Fatal("released version rejected")
	}
	for _, value := range []string{"v0.4.1-3-gabcdef", "v0.4.1-dirty", "dev"} {
		if IsVersion(value) {
			t.Fatalf("development version %q accepted", value)
		}
	}
}

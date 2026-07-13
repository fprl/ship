package memberkeys

import (
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
)

const validEd25519Key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/ alice"

func TestNormalizeLineValidatesSSHKeyBlobAndType(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantDetail string
		wantValid  bool
	}{
		{name: "valid ed25519 key", line: validEd25519Key, wantValid: true},
		{name: "parseable base64 garbage", line: "ssh-ed25519 AA==", wantDetail: "not a valid SSH public key"},
		{name: "declared type mismatches blob", line: "ssh-rsa AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/", wantDetail: "does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := NormalizeLine(tt.line, "")
			if tt.wantValid {
				if err != nil || key.Type != "ssh-ed25519" || key.Fingerprint == "" {
					t.Fatalf("NormalizeLine() = %+v, %v", key, err)
				}
				return
			}
			coded, ok := errcat.As(err)
			if !ok || coded.Code() != errcat.CodeSSHPublicKeyInvalid || !strings.Contains(coded.Cause(), tt.wantDetail) {
				t.Fatalf("error = %v, want ssh_public_key_invalid containing %q", err, tt.wantDetail)
			}
		})
	}
}

func TestParseLineInvalidSSHKeyUsesErrcat(t *testing.T) {
	_, err := ParseLine("ssh-ed25519 AA==")
	coded, ok := errcat.As(err)
	if !ok || coded.Code() != errcat.CodeSSHPublicKeyInvalid || !strings.Contains(coded.Cause(), "not a valid SSH public key") {
		t.Fatalf("error = %v, want ssh_public_key_invalid with a clear detail", err)
	}
}

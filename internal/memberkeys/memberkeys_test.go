package memberkeys

import (
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/store"
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

func TestRenderAuthorizedKeyLinesDropsUnparsableAndUnrecordedLines(t *testing.T) {
	keys := Parse([]byte(validEd25519Key + "\nunsupported-key-format\n"))
	if len(keys) != 2 {
		t.Fatalf("parsed keys = %+v, want two lines", keys)
	}
	recorded := keys[0]
	lines := RenderAuthorizedKeyLines(keys, map[string]store.MemberRecord{
		recorded.Fingerprint: {Name: "alice", Role: store.MemberRoleOwner},
	})
	want := []string{validEd25519Key}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rendered lines = %q, want %q", lines, want)
	}
}

func TestMergeReportsEveryProvidedKey(t *testing.T) {
	existing, err := Normalize(validEd25519Key, "")
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := Normalize("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICtppnbbz76teU3iU6BguTmo//WITtYN35e4gSER6UNt bob", "")
	if err != nil {
		t.Fatal(err)
	}
	lines, results := Merge(existing, []AuthorizedKey{existing[0], newKey[0]})
	if len(lines) != 2 || len(results) != 2 {
		t.Fatalf("merge = lines=%d results=%d, want two lines and results", len(lines), len(results))
	}
	if results[0].Added || !results[1].Added {
		t.Fatalf("merge results = %+v, want existing false and new true", results)
	}
}

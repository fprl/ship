package memberkeys

import (
	"testing"

	"github.com/fprl/ship/internal/store"
)

func FuzzParseLine(f *testing.F) {
	for _, seed := range []string{
		validEd25519Key,
		"ssh-ed25519 AA==",
		"ssh-rsa AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/",
		"unsupported-key-format",
		"# member comment",
		"",
		"command=\"/bin/ship\",restrict " + validEd25519Key,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, line string) {
		key, err := ParseLine(line)
		if err != nil {
			return
		}

		rendered := RenderAuthorizedKeyLine(key, store.MemberRecord{
			Name: "fuzz-member",
			Role: store.MemberRoleAgent,
		})
		renderedKey, err := ParseLine(rendered)
		if err != nil {
			t.Fatalf("rendered accepted key is not parseable: %q: %v", rendered, err)
		}
		if renderedKey.Type != key.Type || renderedKey.Body != key.Body ||
			renderedKey.Material != key.Material || renderedKey.Fingerprint != key.Fingerprint {
			t.Fatalf("parse-render-parse changed key: original=%+v rendered=%q reparsed=%+v", key, rendered, renderedKey)
		}
	})
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		validEd25519Key + "\n",
		validEd25519Key + "\nunsupported-key-format\n",
		"# comment\n\nnot a key\n" + validEd25519Key + "\n",
		"command=\"/bin/ship\",restrict " + validEd25519Key + "\n",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, content []byte) {
		keys := Parse(content)
		for _, key := range keys {
			if key.Material == "" || key.Fingerprint == "" {
				continue
			}
			rendered := RenderAuthorizedKeyLine(key, store.MemberRecord{
				Name: "fuzz-member",
				Role: store.MemberRoleOwner,
			})
			reparsed, err := ParseLine(rendered)
			if err != nil {
				t.Fatalf("rendered parsed key is not parseable: %q: %v", rendered, err)
			}
			if reparsed.Type != key.Type || reparsed.Body != key.Body ||
				reparsed.Material != key.Material || reparsed.Fingerprint != key.Fingerprint {
				t.Fatalf("parse-render-parse changed key: original=%+v rendered=%q reparsed=%+v", key, rendered, reparsed)
			}
		}
	})
}

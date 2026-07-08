package helper

import "testing"

const (
	alicePublicKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/ alice"
	aliceFingerprint = "SHA256:DUvOnIMvzMmJVSD+t9uB9yD7f8nYIQt2y1vGztKOWTg"
	bobPublicKey     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICtppnbbz76teU3iU6BguTmo//WITtYN35e4gSER6UNt bob"
	bobFingerprint   = "SHA256:pcC/cEQYpaTALptLsyfIG8CzllhYJfcxp1vVNQ9PFDc"
)

func TestPublicKeyFingerprintMatchesOpenSSHSHA256(t *testing.T) {
	got, err := publicKeyFingerprint("AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/")
	if err != nil {
		t.Fatal(err)
	}
	if got != aliceFingerprint {
		t.Fatalf("fingerprint = %q, want %q", got, aliceFingerprint)
	}
}

func TestNormalizeAuthorizedKeysStampsMemberMetadata(t *testing.T) {
	keys, err := normalizeAuthorizedKeys(alicePublicKey, "alice.pub")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("len(keys) = %d, want 1", len(keys))
	}
	key := keys[0]
	if key.Comment != "alice.pub" || key.Type != "ssh-ed25519" || key.Fingerprint != aliceFingerprint {
		t.Fatalf("unexpected key metadata: %+v", key)
	}
	wantLine := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/ alice.pub"
	if key.Line != wantLine {
		t.Fatalf("line = %q, want %q", key.Line, wantLine)
	}
}

func TestMemberRowsSortStable(t *testing.T) {
	alice, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := normalizeAuthorizedKeys(bobPublicKey, "bob")
	if err != nil {
		t.Fatal(err)
	}
	rows := memberRows([]authorizedKey{bob[0], alice[0], {Line: "not a key"}})
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2: %+v", len(rows), rows)
	}
	if rows[0].Name != "alice" || rows[0].Fingerprint != aliceFingerprint {
		t.Fatalf("row[0] = %+v, want alice", rows[0])
	}
	if rows[1].Name != "bob" || rows[1].Fingerprint != bobFingerprint {
		t.Fatalf("row[1] = %+v, want bob", rows[1])
	}
}

func TestMemberOutputExamples(t *testing.T) {
	keys, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	row := memberRows(keys)[0]

	added := formatKeyAddResult(keyAddResult{Key: keys[0], Added: true})
	wantAdded := "added alice ssh-ed25519 " + aliceFingerprint
	if added != wantAdded {
		t.Fatalf("add output = %q, want %q", added, wantAdded)
	}

	skipped := formatKeyAddResult(keyAddResult{Key: keys[0]})
	wantSkipped := "skipped alice ssh-ed25519 " + aliceFingerprint + " (already authorized)"
	if skipped != wantSkipped {
		t.Fatalf("dedupe output = %q, want %q", skipped, wantSkipped)
	}

	list := formatMemberRow(row)
	wantList := "alice ssh-ed25519 " + aliceFingerprint
	if list != wantList {
		t.Fatalf("list output = %q, want %q", list, wantList)
	}
}

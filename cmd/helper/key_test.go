package helper

import (
	"testing"

	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/store"
)

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
	rows := memberRows([]authorizedKey{bob[0], alice[0], {Line: "not a key"}}, store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
			bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
		},
	})
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2: %+v", len(rows), rows)
	}
	if rows[0].Name != "alice" || rows[0].Role != "agent" || rows[0].Fingerprint != aliceFingerprint {
		t.Fatalf("row[0] = %+v, want alice", rows[0])
	}
	if rows[1].Name != "bob" || rows[1].Role != "owner" || rows[1].Fingerprint != bobFingerprint {
		t.Fatalf("row[1] = %+v, want bob", rows[1])
	}
}

func TestMemberOutputExamples(t *testing.T) {
	keys, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	members := store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "alice", Role: store.MemberRoleShipper},
		},
	}
	row := memberRows(keys, members)[0]

	added := formatKeyAddResult(keyAddResult{Key: keys[0], Added: true, Role: "shipper"})
	wantAdded := "member added: alice (shipper, " + aliceFingerprint + ")"
	if added != wantAdded {
		t.Fatalf("add output = %q, want %q", added, wantAdded)
	}

	defaultAdded := formatKeyAddResult(keyAddResult{Key: keys[0], Added: true})
	if defaultAdded != wantAdded {
		t.Fatalf("default add output = %q, want %q", defaultAdded, wantAdded)
	}

	skipped := formatKeyAddResult(keyAddResult{Key: keys[0], Role: "shipper"})
	wantSkipped := "member alice already authorized (shipper, " + aliceFingerprint + ")"
	if skipped != wantSkipped {
		t.Fatalf("dedupe output = %q, want %q", skipped, wantSkipped)
	}

	list := formatMemberRow(row)
	wantList := "alice shipper ssh-ed25519 " + aliceFingerprint
	if list != wantList {
		t.Fatalf("list output = %q, want %q", list, wantList)
	}
}

func TestAuthorizedKeyLineRenderingUsesAgentForcedCommand(t *testing.T) {
	keys, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	agentLine := memberkeys.RenderAuthorizedKeyLine(keys[0], store.MemberRecord{Name: "alice", Role: store.MemberRoleAgent})
	wantAgent := `command="/usr/local/bin/ship server agent-shell --member alice",restrict ` + alicePublicKey
	if agentLine != wantAgent {
		t.Fatalf("agent authorized_keys line:\nwant: %s\n got: %s", wantAgent, agentLine)
	}
	parsed, err := memberkeys.ParseLine(agentLine)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Options != `command="/usr/local/bin/ship server agent-shell --member alice",restrict` ||
		parsed.Fingerprint != aliceFingerprint ||
		parsed.Comment != "alice" {
		t.Fatalf("parsed forced entry = %+v", parsed)
	}

	ownerLine := memberkeys.RenderAuthorizedKeyLine(keys[0], store.MemberRecord{Name: "alice", Role: store.MemberRoleOwner})
	if ownerLine != alicePublicKey {
		t.Fatalf("owner authorized_keys line = %q, want plain key", ownerLine)
	}
}

func TestReconciledMembersRecordsExplicitRoles(t *testing.T) {
	keys, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []store.MemberRole{store.MemberRoleOwner, store.MemberRoleShipper, store.MemberRoleAgent} {
		t.Run(string(role), func(t *testing.T) {
			file := memberkeysReconciledForTest(keys, map[string]store.MemberRecord{
				aliceFingerprint: {Name: "alice", Role: role},
			})
			member := file.Members[aliceFingerprint]
			if member.Name != "alice" || member.Role != role {
				t.Fatalf("member record = %+v, want role %s", member, role)
			}
			row := memberRows(keys, file)[0]
			if row.Role != string(role) {
				t.Fatalf("listed role = %q, want %q", row.Role, role)
			}
		})
	}
}

func TestReconciledMembersDefaultsMissingStoreByAuthorizedKeyCount(t *testing.T) {
	alice, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := normalizeAuthorizedKeys(bobPublicKey, "bob")
	if err != nil {
		t.Fatal(err)
	}

	one := memberRows(alice, store.MembersFile{Version: store.CurrentVersion})
	if one[0].Role != "owner" {
		t.Fatalf("single missing store role = %q, want owner", one[0].Role)
	}

	two := memberRows([]authorizedKey{alice[0], bob[0]}, store.MembersFile{Version: store.CurrentVersion})
	if two[0].Role != "shipper" || two[1].Role != "shipper" {
		t.Fatalf("multi-key missing store roles = %+v, want all shipper", two)
	}
}

func TestReconciledMembersPrunesRemovedKeys(t *testing.T) {
	alice, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	current := store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "alice", Role: store.MemberRoleOwner},
			bobFingerprint:   {Name: "bob", Role: store.MemberRoleShipper},
		},
	}

	file := memberkeys.ReconciledMembersFile(alice, current, nil)
	if _, ok := file.Members[bobFingerprint]; ok {
		t.Fatalf("stale bob record should be pruned: %+v", file.Members)
	}
}

func memberkeysReconciledForTest(keys []authorizedKey, overrides map[string]store.MemberRecord) store.MembersFile {
	return memberkeys.ReconciledMembersFile(keys, store.MembersFile{Version: store.CurrentVersion}, overrides)
}

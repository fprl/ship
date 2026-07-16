package helper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/store"
)

const (
	alicePublicKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK5lsspZV02+XPTr8x9fKLEByOHASzHLlF0+dvc+acJ/ alice"
	aliceFingerprint = "SHA256:DUvOnIMvzMmJVSD+t9uB9yD7f8nYIQt2y1vGztKOWTg"
	bobPublicKey     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICtppnbbz76teU3iU6BguTmo//WITtYN35e4gSER6UNt bob"
	bobFingerprint   = "SHA256:pcC/cEQYpaTALptLsyfIG8CzllhYJfcxp1vVNQ9PFDc"
)

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
	wantList := "alice                shipper  SHA256:DUvOnIMvzMmJ ssh-ed25519                  "
	if list != wantList {
		t.Fatalf("list output = %q, want %q", list, wantList)
	}
}

func TestAppendDeployAuthorizedKeysRejectsConflictingRoleForExistingMember(t *testing.T) {
	root := t.TempDir()
	authorizedKeysPath := filepath.Join(root, "authorized_keys")
	t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", authorizedKeysPath)
	setTestStateRoot(t, root)
	setHelperBoxClientAddress(t, "203.0.113.7")
	if err := os.WriteFile(authorizedKeysPath, []byte(alicePublicKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Default().WriteMembers(store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "shared", Role: store.MemberRoleAgent},
		},
	}); err != nil {
		t.Fatal(err)
	}
	keys, err := normalizeAuthorizedKeys(bobPublicKey, "shared")
	if err != nil {
		t.Fatal(err)
	}
	_, err = appendDeployAuthorizedKeys("deploy", keys, store.MemberRoleOwner)
	if err == nil || err.Error() != "command usage failed\nmember \"shared\" already has role \"agent\"; additional keys must use that role\nnext: ship box member add <https-url|key|path> 203.0.113.7 --name shared --role agent" {
		t.Fatalf("conflicting member role err = %v", err)
	}
}

func TestAppendDeployAuthorizedKeysHealsHalfEnrollment(t *testing.T) {
	root := t.TempDir()
	authorizedKeysPath := filepath.Join(root, "authorized_keys")
	t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", authorizedKeysPath)
	setTestStateRoot(t, root)
	setHelperBoxClientAddress(t, "203.0.113.7")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(authorizedKeysPath, []byte(alicePublicKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Default().WriteMembers(store.MembersFile{Version: store.CurrentVersion, Members: map[string]store.MemberRecord{}}); err != nil {
		t.Fatal(err)
	}
	keys, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	results, err := appendDeployAuthorizedKeys("deploy", keys, store.MemberRoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Added {
		t.Fatalf("re-add results = %+v, want one already-authorized key", results)
	}
	content, err := os.ReadFile(authorizedKeysPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), alicePublicKey+"\n"; got != want {
		t.Fatalf("healed authorized_keys = %q, want %q", got, want)
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		t.Fatal(err)
	}
	if got := members.Members[aliceFingerprint]; got.Name != "alice" || got.Role != store.MemberRoleOwner {
		t.Fatalf("reconciled member = %+v, want alice owner", got)
	}
}

func TestAppendDeployAuthorizedKeysWritesMembersBeforeAuthorizedKeys(t *testing.T) {
	root := t.TempDir()
	authorizedKeysPath := filepath.Join(root, "authorized_keys")
	t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", authorizedKeysPath)
	setTestStateRoot(t, root)
	setHelperBoxClientAddress(t, "203.0.113.7")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(authorizedKeysPath, []byte(alicePublicKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Default().WriteMembers(store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "alice", Role: store.MemberRoleOwner},
		},
	}); err != nil {
		t.Fatal(err)
	}
	keys, err := normalizeAuthorizedKeys(bobPublicKey, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })

	if _, err := appendDeployAuthorizedKeys("deploy", keys, store.MemberRoleShipper); err == nil {
		t.Fatal("expected members store write failure")
	}
	content, err := os.ReadFile(authorizedKeysPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), alicePublicKey+"\n"; got != want {
		t.Fatalf("authorized_keys = %q, want unchanged %q", got, want)
	}
}

func TestMemberMutationWriteOrderingFailsClosedOnWriteFailures(t *testing.T) {
	tests := []struct {
		name string
		fn   func(t *testing.T, root, keysPath string, oldKeys []authorizedKey, oldMembers store.MembersFile, next store.MembersFile)
	}{
		{name: "grant store failure", fn: func(t *testing.T, root, keysPath string, oldKeys []authorizedKey, oldMembers store.MembersFile, next store.MembersFile) {
			if err := os.Chmod(root, 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(root, 0700) })
			if err := writeMemberGrant("deploy", []authorizedKey{oldKeys[0], mustNormalizeKey(t, bobPublicKey, "bob")[0]}, next); err == nil {
				t.Fatal("expected members store write failure")
			}
			assertMemberPairHasNoUnrecordedKeys(t)
		},
		},
		{name: "grant authorized_keys failure", fn: func(t *testing.T, root, keysPath string, oldKeys []authorizedKey, oldMembers store.MembersFile, next store.MembersFile) {
			if err := os.Chmod(filepath.Dir(keysPath), 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Dir(keysPath), 0700) })
			if err := writeMemberGrant("deploy", []authorizedKey{oldKeys[0], mustNormalizeKey(t, bobPublicKey, "bob")[0]}, next); err == nil {
				t.Fatal("expected authorized_keys write failure")
			}
			assertMemberPairHasNoUnrecordedKeys(t)
		},
		},
		{name: "revocation authorized_keys failure", fn: func(t *testing.T, root, keysPath string, oldKeys []authorizedKey, oldMembers store.MembersFile, next store.MembersFile) {
			if err := os.Chmod(filepath.Dir(keysPath), 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Dir(keysPath), 0700) })
			if err := writeMemberRevocation("deploy", filepath.Dir(keysPath), keysPath, memberkeys.RenderAuthorizedKeyLines(oldKeys[:1], next.Members), next); err == nil {
				t.Fatal("expected authorized_keys write failure")
			}
			assertMemberPairHasNoUnrecordedKeys(t)
		},
		},
		{name: "revocation store failure", fn: func(t *testing.T, root, keysPath string, oldKeys []authorizedKey, oldMembers store.MembersFile, next store.MembersFile) {
			if err := os.Chmod(root, 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(root, 0700) })
			if err := writeMemberRevocation("deploy", filepath.Dir(keysPath), keysPath, memberkeys.RenderAuthorizedKeyLines(oldKeys[:1], next.Members), next); err == nil {
				t.Fatal("expected members store write failure")
			}
			assertMemberPairHasNoUnrecordedKeys(t)
		},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			keysDir := filepath.Join(root, "keys")
			if err := os.MkdirAll(keysDir, 0700); err != nil {
				t.Fatal(err)
			}
			keysPath := filepath.Join(keysDir, "authorized_keys")
			setTestStateRoot(t, root)
			t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", keysPath)
			bin := filepath.Join(root, "bin")
			if err := os.MkdirAll(bin, 0755); err != nil {
				t.Fatal(err)
			}
			writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			oldKeys := mustNormalizeKey(t, alicePublicKey, "alice")
			bob := mustNormalizeKey(t, bobPublicKey, "bob")[0]
			oldMembers := store.MembersFile{Version: store.CurrentVersion, Members: map[string]store.MemberRecord{aliceFingerprint: {Name: "alice", Role: store.MemberRoleOwner}, bobFingerprint: {Name: "bob", Role: store.MemberRoleShipper}}}
			next := store.MembersFile{Version: store.CurrentVersion, Members: map[string]store.MemberRecord{aliceFingerprint: {Name: "alice", Role: store.MemberRoleOwner}, bobFingerprint: {Name: "bob", Role: store.MemberRoleShipper}}}
			if strings.Contains(tt.name, "revocation") {
				next.Members = map[string]store.MemberRecord{aliceFingerprint: {Name: "alice", Role: store.MemberRoleOwner}}
			}
			if err := os.WriteFile(keysPath, []byte(alicePublicKey+"\n"+bobPublicKey+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.Default().WriteMembers(oldMembers); err != nil {
				t.Fatal(err)
			}
			tt.fn(t, root, keysPath, append(oldKeys, bob), oldMembers, next)
		})
	}
}

func mustNormalizeKey(t *testing.T, raw, comment string) []authorizedKey {
	t.Helper()
	keys, err := normalizeAuthorizedKeys(raw, comment)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func assertMemberPairHasNoUnrecordedKeys(t *testing.T) {
	t.Helper()
	keys, err := readDeployAuthorizedKeys("deploy")
	if err != nil {
		t.Fatal(err)
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		t.Fatal(err)
	}
	records := memberkeys.EffectiveMemberRecords(keys, *members, nil)
	for _, key := range keys {
		if key.Material != "" {
			if _, ok := records[key.Fingerprint]; !ok {
				t.Fatalf("authorized key %s has no store record: %+v", key.Fingerprint, members.Members)
			}
		}
	}
}

func TestAppendDeployAuthorizedKeysDropsStrayButKeepsRecordedOwner(t *testing.T) {
	root := t.TempDir()
	authorizedKeysPath := filepath.Join(root, "authorized_keys")
	t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", authorizedKeysPath)
	setTestStateRoot(t, root)
	setHelperBoxClientAddress(t, "203.0.113.7")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stray := strings.Replace(bobPublicKey, " bob", " stray", 1)
	if err := os.WriteFile(authorizedKeysPath, []byte(alicePublicKey+"\n"+stray+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Default().WriteMembers(store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "alice", Role: store.MemberRoleOwner},
		},
	}); err != nil {
		t.Fatal(err)
	}
	keys, err := normalizeAuthorizedKeys(bobPublicKey, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendDeployAuthorizedKeys("deploy", keys, store.MemberRoleShipper); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(authorizedKeysPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), alicePublicKey+"\n"+bobPublicKey+"\n"; got != want {
		t.Fatalf("authorized_keys = %q, want %q", got, want)
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		t.Fatal(err)
	}
	if got := members.Members[aliceFingerprint]; got.Name != "alice" || got.Role != store.MemberRoleOwner {
		t.Fatalf("recorded owner = %+v, want alice owner", got)
	}
}

func TestRemoveDeployAuthorizedKeysRemovesUnrecordedStray(t *testing.T) {
	root := t.TempDir()
	authorizedKeysPath := filepath.Join(root, "authorized_keys")
	t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", authorizedKeysPath)
	setTestStateRoot(t, root)
	setHelperBoxClientAddress(t, "203.0.113.7")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, bin, "chown", "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stray := strings.Replace(bobPublicKey, " bob", " stray", 1)
	if err := os.WriteFile(authorizedKeysPath, []byte(alicePublicKey+"\n"+stray+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Default().WriteMembers(store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "alice", Role: store.MemberRoleOwner},
		},
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := removeDeployAuthorizedKeys("deploy", "stray")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	content, err := os.ReadFile(authorizedKeysPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), alicePublicKey+"\n"; got != want {
		t.Fatalf("authorized_keys = %q, want recorded owner %q", got, want)
	}
}

func TestAppendDeployAuthorizedKeysKeepsOneNameAcrossMultipleKeysAndRejectsReassignment(t *testing.T) {
	root := t.TempDir()
	authorizedKeysPath := filepath.Join(root, "authorized_keys")
	t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", authorizedKeysPath)
	setTestStateRoot(t, root)
	setHelperBoxClientAddress(t, "203.0.113.7")
	if err := os.WriteFile(authorizedKeysPath, []byte(alicePublicKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Default().WriteMembers(store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "shared", Role: store.MemberRoleOwner},
		},
	}); err != nil {
		t.Fatal(err)
	}
	bob, err := normalizeAuthorizedKeys(bobPublicKey, "shared")
	if err != nil {
		t.Fatal(err)
	}
	existing, err := readAuthorizedKeys(authorizedKeysPath)
	if err != nil {
		t.Fatal(err)
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMemberEnrollment(existing, memberkeys.EffectiveMemberRecords(existing, *members, nil), bob, store.MemberRoleOwner); err != nil {
		t.Fatalf("adding a second key to one member = %v", err)
	}
	lines, results := memberkeys.Merge(existing, bob)
	if len(results) != 1 || !results[0].Added || len(lines) != 2 {
		t.Fatalf("multi-key merge = lines=%d results=%+v", len(lines), results)
	}
	next := memberkeys.ReconciledMembersFile(memberkeys.Parse(memberkeys.Content(lines)), *members, map[string]store.MemberRecord{bob[0].Fingerprint: {Name: "shared", Role: store.MemberRoleOwner}})
	if len(next.Members) != 2 || next.Members[bob[0].Fingerprint].Name != "shared" {
		t.Fatalf("multi-key member records = %+v, want two shared records", next.Members)
	}

	reassigned, err := normalizeAuthorizedKeys(alicePublicKey, "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMemberEnrollment(existing, memberkeys.EffectiveMemberRecords(existing, *members, nil), reassigned, store.MemberRoleOwner); !errcat.Is(err, errcat.CodeUsageError) || !strings.Contains(err.Error(), "already belongs to member \"shared\"") {
		t.Fatalf("reassigning existing key error = %v, want ownership usage error", err)
	}
	content, err := os.ReadFile(authorizedKeysPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), bobPublicKey[:strings.Index(bobPublicKey, " ")]) {
		t.Fatalf("authorized_keys changed unexpectedly after rejected reassignment: %s", content)
	}
}

func TestMemberRemediationsUseRecordedBoxAddress(t *testing.T) {
	root := t.TempDir()
	authorizedKeysPath := filepath.Join(root, "authorized_keys")
	t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", authorizedKeysPath)
	setTestStateRoot(t, root)
	setHelperBoxClientAddress(t, "203.0.113.7")
	if err := os.WriteFile(authorizedKeysPath, []byte(alicePublicKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Default().WriteMembers(store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "alice", Role: store.MemberRoleOwner},
		},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name            string
		member          string
		code            errcat.Code
		wantRemediation string
	}{
		{name: "missing member", member: "nobody", code: errcat.CodeMemberNotFound, wantRemediation: "ship box member ls 203.0.113.7"},
		{name: "last effective owner", member: "alice", code: errcat.CodeMemberLastOwner, wantRemediation: "ship box member add <https-url|key|path> 203.0.113.7 --name <new-owner> --role owner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := removeDeployAuthorizedKeys("deploy", tt.member)
			if !errcat.Is(err, tt.code) {
				t.Fatalf("remove member error = %v, want %s", err, tt.code)
			}
			coded, _ := errcat.As(err)
			if got := coded.Remediation(); got != tt.wantRemediation {
				t.Fatalf("remediation = %q, want %q", got, tt.wantRemediation)
			}
		})
	}
}

func TestNormalizeAuthorizedKeysUsesRecordedBoxAddress(t *testing.T) {
	setTestStateRoot(t, t.TempDir())
	setHelperBoxClientAddress(t, "203.0.113.7")
	_, err := normalizeAuthorizedKeys("not an SSH key", "alice")
	if !errcat.Is(err, errcat.CodeSSHPublicKeyInvalid) {
		t.Fatalf("normalize error = %v, want ssh_public_key_invalid", err)
	}
	coded, _ := errcat.As(err)
	if got, want := coded.Remediation(), "ship box member add <https-url|key|path> 203.0.113.7 --name alice"; got != want {
		t.Fatalf("remediation = %q, want %q", got, want)
	}
}

func TestEmptyHelperListPayloadsEncodeArrays(t *testing.T) {
	members, err := json.Marshal(memberKeyListPayload{Members: groupMemberRows(memberRows(nil, store.MembersFile{}))})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(members), `{"members":[]}`; got != want {
		t.Fatalf("empty member payload = %s, want %s", got, want)
	}

	approvals, err := json.Marshal(approvalListPayload{Approvals: approvalRows(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(approvals), `{"approvals":[]}`; got != want {
		t.Fatalf("empty approval payload = %s, want %s", got, want)
	}
}

func TestMemberListPayloadIsNested(t *testing.T) {
	rows := []memberKeyRow{{Name: "alice", Role: "owner", KeyID: "SHA256:abcdefghijkl", KeyType: "ssh-ed25519", Fingerprint: aliceFingerprint, Current: true}}
	data, err := json.Marshal(memberKeyListPayload{Members: groupMemberRows(rows)})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"members":[{"name":"alice","role":"owner","keys":[{"id":"SHA256:abcdefghijkl","fingerprint":"` + aliceFingerprint + `","type":"ssh-ed25519","current":true}]}]}`
	if string(data) != want {
		t.Fatalf("nested member payload = %s, want %s", data, want)
	}
}

func setHelperBoxClientAddress(t *testing.T, address string) {
	t.Helper()
	stateStore := store.Default()
	if err := stateStore.WriteBoxConfig(store.BoxConfigFile{Version: store.CurrentVersion, Values: map[string]string{"box.address": address}}); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizedKeyLineRenderingUsesAgentForcedCommand(t *testing.T) {
	keys, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	agentLine := memberkeys.RenderAuthorizedKeyLine(keys[0], store.MemberRecord{Name: "alice", Role: store.MemberRoleAgent})
	wantAgent := `command="/usr/local/bin/ship server agent-shell --member-fingerprint ` + aliceFingerprint + `",restrict ` + alicePublicKey
	if agentLine != wantAgent {
		t.Fatalf("agent authorized_keys line:\nwant: %s\n got: %s", wantAgent, agentLine)
	}
	parsed, err := memberkeys.ParseLine(agentLine)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Fingerprint != aliceFingerprint ||
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

func TestReconciledMembersRefusesMissingStoreRecords(t *testing.T) {
	alice, err := normalizeAuthorizedKeys(alicePublicKey, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := normalizeAuthorizedKeys(bobPublicKey, "bob")
	if err != nil {
		t.Fatal(err)
	}

	one := memberRows(alice, store.MembersFile{Version: store.CurrentVersion})
	if len(one) != 0 {
		t.Fatalf("single missing store rows = %+v, want none", one)
	}

	two := memberRows([]authorizedKey{alice[0], bob[0]}, store.MembersFile{Version: store.CurrentVersion})
	if len(two) != 0 {
		t.Fatalf("multi-key missing store rows = %+v, want none", two)
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

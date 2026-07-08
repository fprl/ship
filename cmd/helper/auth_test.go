package helper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/store"
)

func TestApprovalFlowIsOneShotAndJournaled(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
	})
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	setApprovalNowForTest(t, now)
	target := authTargetForAppEnv("api", productionEnvName, "release=abc123")

	setServerMemberFingerprint(aliceFingerprint)
	_, err := authorizeHelper(helperVerbShip, target)
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("agent prod ship err = %v, want approval_required", err)
	}
	coded, _ := errcat.As(err)
	if !strings.Contains(coded.Message(), target.Summary) {
		t.Fatalf("approval_required message should carry summary, got %q", coded.Message())
	}
	if !strings.HasPrefix(coded.Remediation(), "ship approve ") {
		t.Fatalf("approval remediation = %q, want ship approve <id>", coded.Remediation())
	}
	id := strings.TrimPrefix(coded.Remediation(), "ship approve ")

	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeApprovalGrant(id); !errcat.Is(err, errcat.CodeRoleDenied) {
		t.Fatalf("agent approve err = %v, want role_denied", err)
	}

	setServerMemberFingerprint(bobFingerprint)
	approver, err := authorizeApprovalGrant(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approveRequest(id, approver); err != nil {
		t.Fatal(err)
	}

	setServerMemberFingerprint(aliceFingerprint)
	member, err := authorizeHelper(helperVerbShip, target)
	if err != nil {
		t.Fatalf("approved retry failed: %v", err)
	}
	if member.Role != store.MemberRoleAgent {
		t.Fatalf("authorized member role = %s, want agent", member.Role)
	}
	pending, err := pendingApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("approval should be consumed, pending=%+v", pending)
	}

	setServerMemberFingerprint(aliceFingerprint)
	_, err = authorizeHelper(helperVerbShip, target)
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("second use err = %v, want new approval_required", err)
	}
	next, _ := errcat.As(err)
	if next.Remediation() == coded.Remediation() {
		t.Fatalf("second approval reused consumed id %q", next.Remediation())
	}

	events := readApprovalJournalForTest(t)
	assertApprovalJournalEvent(t, events, "requested", "alice", "agent")
	assertApprovalJournalEvent(t, events, "approved", "bob", "owner")
	assertApprovalJournalEvent(t, events, "consumed", "alice", "agent")
}

func TestExpiredApprovedRequestFailsConsumptionWithFreshRetryRemediation(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
	})
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	setApprovalNowForTest(t, now)
	target := authTargetForAppEnv("api", productionEnvName, "release=abc123")

	setServerMemberFingerprint(aliceFingerprint)
	_, err := authorizeHelper(helperVerbShip, target)
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("request err = %v, want approval_required", err)
	}
	coded, _ := errcat.As(err)
	id := strings.TrimPrefix(coded.Remediation(), "ship approve ")

	setServerMemberFingerprint(bobFingerprint)
	approver, err := authorizeApprovalGrant(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approveRequest(id, approver); err != nil {
		t.Fatal(err)
	}

	setApprovalNowForTest(t, now.Add(approvalTTL))
	setServerMemberFingerprint(aliceFingerprint)
	_, err = authorizeHelper(helperVerbShip, target)
	if !errcat.Is(err, errcat.CodeApprovalExpired) {
		t.Fatalf("expired retry err = %v, want approval_expired", err)
	}
	expired, _ := errcat.As(err)
	if expired.Remediation() != "retry the command to mint a fresh request" {
		t.Fatalf("expired remediation = %q", expired.Remediation())
	}
}

func TestPendingApprovalsListPrunesExpired(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
	})
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	setApprovalNowForTest(t, now)
	setServerMemberFingerprint(aliceFingerprint)
	_, err := authorizeHelper(helperVerbShip, authTargetForAppEnv("api", productionEnvName, "release=abc123"))
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("request err = %v, want approval_required", err)
	}

	setApprovalNowForTest(t, now.Add(approvalTTL))
	pending, err := pendingApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expired approvals should be pruned, got %+v", pending)
	}
	file, err := store.Default().ReadApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Requests) != 0 {
		t.Fatalf("expired approvals still in store: %+v", file.Requests)
	}
}

func TestUnknownFingerprintRefusesWithMemberAddRemediation(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
	})
	setServerMemberFingerprint("SHA256:not-authorized")
	_, err := authorizeHelper(helperVerbRead, authTargetForBox("status"))
	if !errcat.Is(err, errcat.CodeMemberUnknown) {
		t.Fatalf("unknown fingerprint err = %v, want member_unknown", err)
	}
	coded, _ := errcat.As(err)
	if coded.Remediation() != "ship member add" {
		t.Fatalf("unknown remediation = %q", coded.Remediation())
	}
}

func TestAuthorizedKeyMissingFromMembersUsesEffectiveShipperRole(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
	})
	setServerMemberFingerprint(bobFingerprint)
	member, err := authorizeHelper(helperVerbShip, authTargetForAppEnv("api", productionEnvName, "release=abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if member.Name != "bob" || member.Role != store.MemberRoleShipper {
		t.Fatalf("effective member = %+v, want bob shipper", member)
	}
}

func TestRoleMatrixShipperDeniedMemberOwnerUnrestricted(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleShipper},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
	})

	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeHelper(helperVerbShip, authTargetForAppEnv("api", productionEnvName, "release=abc123")); err != nil {
		t.Fatalf("shipper prod ship should be allowed: %v", err)
	}
	if _, err := authorizeHelper(helperVerbMember, authTargetForBox("member add", "name=teammate")); !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("shipper member add err = %v, want approval_required", err)
	}

	setServerMemberFingerprint(bobFingerprint)
	if _, err := authorizeHelper(helperVerbMember, authTargetForBox("member add", "name=teammate")); err != nil {
		t.Fatalf("owner member add should be allowed: %v", err)
	}
	if _, err := authorizeHelper(helperVerbBackupRestore, authTargetForAppEnv("api", productionEnvName, "backup=latest")); err != nil {
		t.Fatalf("owner restore should be allowed: %v", err)
	}
}

func TestApprovalListHumanTable(t *testing.T) {
	requests := []store.ApprovalRequest{{
		ID: "abc123xy",
		Member: store.ApprovalMember{
			Fingerprint: aliceFingerprint,
			Name:        "alice",
			Role:        store.MemberRoleAgent,
		},
		Target: store.ApprovalTarget{
			Summary: "app=api env=prod class=production release=abc123",
		},
		ExpiresAt: "2026-07-08T10:15:00Z",
	}}
	rows := approvalRows(requests)
	got := "ID MEMBER REQUEST EXPIRES\n" + formatApprovalRow(rows[0]) + "\n"
	want := "ID MEMBER REQUEST EXPIRES\nabc123xy alice app=api env=prod class=production release=abc123 2026-07-08T10:15:00Z\n"
	if got != want {
		t.Fatalf("approval listing:\nwant: %s\n got: %s", want, got)
	}
}

func TestApprovalRequiredHumanErrorShape(t *testing.T) {
	request := store.ApprovalRequest{
		ID: "abc123xy",
		Member: store.ApprovalMember{
			Name: "alice",
			Role: store.MemberRoleAgent,
		},
		Target: store.ApprovalTarget{
			Summary: "app=api env=prod class=production release=abc123",
		},
	}
	got := approvalRequiredError(request).Error()
	want := "approval required for app=api env=prod class=production release=abc123\n" +
		"alice (agent) requested app=api env=prod class=production release=abc123; approval id abc123xy\n" +
		"next: ship approve abc123xy"
	if got != want {
		t.Fatalf("approval_required error:\nwant:\n%s\n got:\n%s", want, got)
	}
}

func setupAuthTest(t *testing.T, members map[string]store.MemberRecord) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SHIP_STATE_DIR", root)
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	authorizedKeysPath := filepath.Join(root, "authorized_keys")
	t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", authorizedKeysPath)
	t.Setenv("SUDO_USER", "")
	t.Cleanup(func() {
		setServerMemberFingerprint("")
		serverAuthorizedMember = nil
	})
	if err := os.WriteFile(authorizedKeysPath, []byte(alicePublicKey+"\n"+bobPublicKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Default().WriteMembers(store.MembersFile{
		Version: store.CurrentVersion,
		Members: members,
	}); err != nil {
		t.Fatal(err)
	}
	return root
}

func setApprovalNowForTest(t *testing.T, now time.Time) {
	t.Helper()
	previous := approvalNow
	approvalNow = func() time.Time { return now }
	t.Cleanup(func() { approvalNow = previous })
}

func readApprovalJournalForTest(t *testing.T) []approvalJournalEntry {
	t.Helper()
	data, err := os.ReadFile(store.Default().ApprovalsJournalPath())
	if err != nil {
		t.Fatal(err)
	}
	var entries []approvalJournalEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry approvalJournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("parse approval journal: %v\n%s", err, line)
		}
		entries = append(entries, entry)
	}
	return entries
}

func assertApprovalJournalEvent(t *testing.T, entries []approvalJournalEntry, event, actor, role string) {
	t.Helper()
	for _, entry := range entries {
		if entry.Event == event && entry.Actor.Name == actor && string(entry.Actor.Role) == role {
			return
		}
	}
	t.Fatalf("approval journal missing event=%s actor=%s role=%s: %+v", event, actor, role, entries)
}

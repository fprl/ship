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
	target := authTargetForAppEnv("api", productionEnvName, "ship", "release=abc123")

	setServerMemberFingerprint(aliceFingerprint)
	_, err := authorizeHelper(helperVerbShip, target)
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("agent production ship err = %v, want approval_required", err)
	}
	coded, _ := errcat.As(err)
	if !strings.Contains(coded.Message(), target.Summary) {
		t.Fatalf("approval_required message should carry summary, got %q", coded.Message())
	}
	if !strings.HasPrefix(coded.Remediation(), "ship box approval grant ") || !strings.HasSuffix(coded.Remediation(), " 203.0.113.7") {
		t.Fatalf("approval remediation = %q, want fully resolved box approval command", coded.Remediation())
	}
	id := approvalIDFromRemediation(t, coded.Remediation())
	requests, err := store.Default().ReadApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(requests.Requests) != 1 || requests.Requests[0].RequiredRole != store.MemberRoleShipper {
		t.Fatalf("minted request required role = %+v, want shipper", requests.Requests)
	}

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

func TestApprovalGrantRequiresCoveredRoleAndDifferentFingerprint(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleShipper},
	})
	setServerMemberFingerprint(aliceFingerprint)
	_, err := authorizeHelper(helperVerbMember, authTargetForBox("add member", "name=teammate"))
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("agent member add err = %v, want approval_required", err)
	}
	coded, _ := errcat.As(err)
	id := approvalIDFromRemediation(t, coded.Remediation())

	requests, err := store.Default().ReadApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(requests.Requests) != 1 || requests.Requests[0].RequiredRole != store.MemberRoleOwner {
		t.Fatalf("minted request required role = %+v, want owner", requests.Requests)
	}

	setServerMemberFingerprint(bobFingerprint)
	if _, err := authorizeApprovalGrant(id); !errcat.Is(err, errcat.CodeRoleDenied) {
		t.Fatalf("shipper owner-gated approval err = %v, want role_denied", err)
	} else if !strings.Contains(err.Error(), "request requires owner") || !strings.Contains(err.Error(), "ask an owner") {
		t.Fatalf("shipper owner-gated approval error = %q", err)
	}
	if _, err := approveRequest(id, serverMember{Fingerprint: bobFingerprint, Name: "bob", Role: store.MemberRoleShipper}); !errcat.Is(err, errcat.CodeRoleDenied) {
		t.Fatalf("locked shipper owner-gated approval err = %v, want role_denied", err)
	}

	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeApprovalGrant(id); !errcat.Is(err, errcat.CodeRoleDenied) {
		t.Fatalf("self approval err = %v, want role_denied", err)
	} else if !strings.Contains(err.Error(), "requests cannot be self-approved") || !strings.Contains(err.Error(), "another owner") {
		t.Fatalf("self approval error = %q", err)
	}
	if _, err := approveRequest(id, serverMember{Fingerprint: aliceFingerprint, Name: "alice", Role: store.MemberRoleAgent}); !errcat.Is(err, errcat.CodeRoleDenied) {
		t.Fatalf("locked self approval err = %v, want role_denied", err)
	}
}

func TestApprovalGrantRefreshesExpiryWindow(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
	})
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	setApprovalNowForTest(t, now)
	target := authTargetForAppEnv("api", productionEnvName, "ship", "release=abc123")

	setServerMemberFingerprint(aliceFingerprint)
	_, err := authorizeHelper(helperVerbShip, target)
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("agent production ship err = %v, want approval_required", err)
	}
	coded, _ := errcat.As(err)
	id := approvalIDFromRemediation(t, coded.Remediation())

	grantedAt := now.Add(14 * time.Minute)
	setApprovalNowForTest(t, grantedAt)
	setServerMemberFingerprint(bobFingerprint)
	approver, err := authorizeApprovalGrant(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approveRequest(id, approver); err != nil {
		t.Fatal(err)
	}

	requests, err := store.Default().ReadApprovals()
	if err != nil {
		t.Fatal(err)
	}
	wantExpiry := grantedAt.Add(approvalTTL).Format(time.RFC3339Nano)
	if len(requests.Requests) != 1 || requests.Requests[0].ExpiresAt != wantExpiry {
		t.Fatalf("granted expiry = %+v, want %q", requests.Requests, wantExpiry)
	}

	setApprovalNowForTest(t, now.Add(28*time.Minute))
	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeHelper(helperVerbShip, target); err != nil {
		t.Fatalf("retry inside refreshed window failed: %v", err)
	}
}

func TestPreviewShareRotationRequiresApprovalForAgents(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
	})
	setServerMemberFingerprint(aliceFingerprint)
	_, err := authorizeHelper(helperVerbShare, authTargetForPreviewBranch("api", "feat/protected", "share", "preview-share"))
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("agent preview share rotation err = %v, want approval_required", err)
	}
}

func TestShareAllowsShippersAndRequiresApprovalForAgents(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleShipper},
	})
	target := authTargetForPreviewBranch("api", "feat/protected", "share", "share")
	setServerMemberFingerprint(bobFingerprint)
	if _, err := authorizeHelper(helperVerbShare, target); err != nil {
		t.Fatalf("shipper share authorization: %v", err)
	}
	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeHelper(helperVerbShare, target); !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("agent share err = %v, want approval_required", err)
	}
}

func TestExpiredApprovedRequestFailsConsumptionWithFreshRetryRemediation(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
	})
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	setApprovalNowForTest(t, now)
	target := authTargetForAppEnv("api", productionEnvName, "ship", "release=abc123")

	setServerMemberFingerprint(aliceFingerprint)
	_, err := authorizeHelper(helperVerbShip, target)
	if !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("request err = %v, want approval_required", err)
	}
	coded, _ := errcat.As(err)
	id := approvalIDFromRemediation(t, coded.Remediation())

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
	_, err := authorizeHelper(helperVerbShip, authTargetForAppEnv("api", productionEnvName, "ship", "release=abc123"))
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
	if coded.Remediation() != "ship box member add <https-url|key|path> 203.0.113.7 --name <name>" {
		t.Fatalf("unknown remediation = %q", coded.Remediation())
	}
}

func TestPreviewResolveAuthorizationSummaryDoesNotRepeatResolve(t *testing.T) {
	target := authTargetForPreviewBranch("api", "feat/x", "resolve")
	if got, want := target.Summary, "resolve app=api class=preview branch=feat/x"; got != want {
		t.Fatalf("preview resolve summary = %q, want %q", got, want)
	}
}

func TestDeployPreparationUsesShipAuthorization(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleShipper},
	})

	setupTarget := authTargetForAppEnv("api", productionEnvName, "setup-env")
	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeHelper(helperVerbShip, setupTarget); !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("agent setup-env authorization = %v, want approval_required", err)
	}

	for _, fingerprint := range []string{bobFingerprint, ""} {
		setServerMemberFingerprint(fingerprint)
		if _, err := authorizeHelper(helperVerbShip, setupTarget); err != nil {
			t.Fatalf("%s setup-env authorization = %v", fingerprint, err)
		}
	}

	previewTarget := authTargetForPreviewBranch("api", "feat/x", "resolve-or-create")
	for _, fingerprint := range []string{aliceFingerprint, bobFingerprint, ""} {
		setServerMemberFingerprint(fingerprint)
		if _, err := authorizeHelper(helperVerbShip, previewTarget); err != nil {
			t.Fatalf("%s preview resolve-or-create authorization = %v", fingerprint, err)
		}
	}
}

func TestFingerprintResolutionRejectsSameNameRoleCollision(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "shared", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "shared", Role: store.MemberRoleOwner},
	})
	setServerMemberFingerprint(aliceFingerprint)
	member, err := authorizeHelper(helperVerbShip, authTargetForPreviewBranch("api", "feat/pin", "ship", "release=abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if member.Fingerprint != aliceFingerprint || member.Name != "shared" || member.Role != store.MemberRoleAgent {
		t.Fatalf("agent fingerprint resolved to %+v, want the agent record", member)
	}
}

func TestAuthorizedKeyMissingFromMembersIsUnknown(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
	})
	setServerMemberFingerprint(bobFingerprint)
	_, err := authorizeHelper(helperVerbShip, authTargetForAppEnv("api", productionEnvName, "ship", "release=abc123"))
	if !errcat.Is(err, errcat.CodeMemberUnknown) {
		t.Fatalf("unrecorded fingerprint err = %v, want member_unknown", err)
	}
}

func TestRoleMatrixShipperDeniedMemberOwnerUnrestricted(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleShipper},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
	})

	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeHelper(helperVerbShip, authTargetForAppEnv("api", productionEnvName, "ship", "release=abc123")); err != nil {
		t.Fatalf("shipper production ship should be allowed: %v", err)
	}
	if _, err := authorizeHelper(helperVerbMember, authTargetForBox("add member", "name=teammate")); !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("shipper member add err = %v, want approval_required", err)
	}

	setServerMemberFingerprint(bobFingerprint)
	if _, err := authorizeHelper(helperVerbMember, authTargetForBox("add member", "name=teammate")); err != nil {
		t.Fatalf("owner member add should be allowed: %v", err)
	}
	if _, err := authorizeHelper(helperVerbData, authTargetForAppEnv("api", productionEnvName, "restore", "data=restore")); err != nil {
		t.Fatalf("owner restore should be allowed: %v", err)
	}
}

func TestDataForkRequiresShipperOwnerAndAgentsRequestApproval(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleAgent},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleShipper},
	})
	target := authTargetForAppEnv("api", "feat-x-abcd", "fork", "data=fork", "from=production")

	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeHelper(helperVerbData, target); !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("agent data fork err = %v, want approval_required", err)
	}

	setServerMemberFingerprint(bobFingerprint)
	if _, err := authorizeHelper(helperVerbData, target); err != nil {
		t.Fatalf("shipper data fork should be allowed: %v", err)
	}
}

func TestDataRestoreProductionRequiresOwnerAfterDataAuthorization(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleShipper},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
	})
	target := authTargetForAppEnv("api", productionEnvName, "restore", "data=restore")

	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeHelper(helperVerbData, target); err != nil {
		t.Fatalf("shipper data authorization: %v", err)
	}
	if _, err := authorizeHelper(helperVerbBoxMutation, target); !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("shipper production restore owner gate = %v, want approval_required", err)
	}

	setServerMemberFingerprint(bobFingerprint)
	if _, err := authorizeHelper(helperVerbData, target); err != nil {
		t.Fatalf("owner data authorization: %v", err)
	}
	if _, err := authorizeHelper(helperVerbBoxMutation, target); err != nil {
		t.Fatalf("owner production restore gate: %v", err)
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
			Summary: "ship app=api env=production class=production release=abc123",
		},
		ExpiresAt: "2026-07-08T10:15:00Z",
	}}
	rows := approvalRows(requests)
	got := "ID MEMBER REQUEST EXPIRES\n" + formatApprovalRow(rows[0]) + "\n"
	want := "ID MEMBER REQUEST EXPIRES\nabc123xy alice ship app=api env=production class=production release=abc123 2026-07-08T10:15:00Z\n"
	if got != want {
		t.Fatalf("approval ls output:\nwant: %s\n got: %s", want, got)
	}
}

func TestApprovalRequiredHumanErrorShape(t *testing.T) {
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	writeValidHost(t, store.Default().HostPath())
	if err := store.Default().WriteHostState(store.HostObserved{Packages: map[string]store.ObservedPackage{}}, store.HostMeta{ClientAddress: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}
	request := store.ApprovalRequest{
		ID: "abc123xy",
		Member: store.ApprovalMember{
			Name: "alice",
			Role: store.MemberRoleAgent,
		},
		Target: store.ApprovalTarget{
			Summary: "ship app=api env=production class=production release=abc123",
		},
	}
	got := approvalRequiredError(request).Error()
	want := "approval required for ship app=api env=production class=production release=abc123\n" +
		"alice (agent) requested ship app=api env=production class=production release=abc123; approval id abc123xy\n" +
		"next: ship box approval grant abc123xy 203.0.113.7"
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
	stateStore := store.Default()
	writeValidHost(t, stateStore.HostPath())
	if err := stateStore.WriteHostState(store.HostObserved{Packages: map[string]store.ObservedPackage{}}, store.HostMeta{ClientAddress: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}
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

func approvalIDFromRemediation(t *testing.T, remediation string) string {
	t.Helper()
	fields := strings.Fields(remediation)
	if len(fields) != 6 || fields[0] != "ship" || fields[1] != "box" || fields[2] != "approval" || fields[3] != "grant" || fields[4] == "" || fields[5] != "203.0.113.7" {
		t.Fatalf("approval remediation = %q, want ship box approval grant <id> 203.0.113.7", remediation)
	}
	return fields[4]
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

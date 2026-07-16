package helper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/provision"
	"github.com/fprl/ship/internal/store"
)

func TestProductionDataSaveUsesOneShotOwnerApproval(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleShipper},
		bobFingerprint:   {Name: "bob", Role: store.MemberRoleOwner},
	})
	target := authTargetForDataSave("api", productionEnvName)
	if target.Summary != "save production data for api" {
		t.Fatalf("save summary = %q", target.Summary)
	}

	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeHelper(helperVerbDataSave, target); err != nil {
		t.Fatalf("shipper data-save authorization = %v", err)
	}
	_, gateErr := authorizeHelper(helperVerbBoxMutation, target)
	if !errcat.Is(gateErr, errcat.CodeApprovalRequired) {
		t.Fatalf("shipper production data-save gate = %v, want approval_required", gateErr)
	}
	coded, _ := errcat.As(gateErr)
	if !strings.Contains(coded.Message(), "save production data for api") || !strings.HasSuffix(coded.Remediation(), " 203.0.113.7") {
		t.Fatalf("approval response = %v", coded)
	}
	id := approvalIDFromRemediation(t, coded.Remediation())
	file, err := store.Default().ReadApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Requests) != 1 || file.Requests[0].RequiredRole != store.MemberRoleOwner {
		t.Fatalf("pending save approval = %+v, want one owner-required request", file.Requests)
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
	if _, err := authorizeHelper(helperVerbDataSave, target); err != nil {
		t.Fatalf("approved retry data-save authorization = %v", err)
	}
	if _, err := authorizeHelper(helperVerbBoxMutation, target); err != nil {
		t.Fatalf("approved retry owner gate = %v", err)
	}
	if _, err := authorizeHelper(helperVerbBoxMutation, target); !errcat.Is(err, errcat.CodeApprovalRequired) {
		t.Fatalf("second production data-save use = %v, want fresh approval_required", err)
	}
}

func TestDataSavePreviewRemainsUngatedAndAgentShellUsesSameDispatch(t *testing.T) {
	setupAuthTest(t, map[string]store.MemberRecord{
		aliceFingerprint: {Name: "alice", Role: store.MemberRoleShipper},
	})
	setServerMemberFingerprint(aliceFingerprint)
	if _, err := authorizeHelper(helperVerbDataSave, authTargetForAppEnv("api", "preview-abcd", "save", "data=save")); err != nil {
		t.Fatalf("preview data-save authorization = %v", err)
	}
	action, err := agentShellActionFor("sudo -n /usr/local/bin/ship server app data save api production", "SHA256:agent")
	if err != nil || action.Kind != agentShellActionExec {
		t.Fatalf("agent data-save dispatch = %+v, err=%v", action, err)
	}
}

func TestDoctorHardeningDriftUsesProvisionExpectationsAndDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_HARDENING_ROOT", root)
	for _, expectation := range provision.SSHHardeningExpectations() {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, strings.TrimPrefix(expectation.Path, "/etc/"))), 0755); err != nil {
			t.Fatal(err)
		}
	}
	sshPath := filepath.Join(root, "ssh/sshd_config")
	sshLines := make([]string, 0, len(provision.SSHHardeningExpectations()))
	for _, expectation := range provision.SSHHardeningExpectations() {
		sshLines = append(sshLines, expectation.Line)
	}
	if err := os.WriteFile(sshPath, []byte(strings.Join(sshLines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	before := provision.FirewallBeforeRulesExpectation()
	beforePath := filepath.Join(root, "ufw/before.rules")
	if err := os.MkdirAll(filepath.Dir(beforePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(beforePath, []byte("# End required lines\n\n"+before.Content+"\n"), 0640); err != nil {
		t.Fatal(err)
	}
	defaultPath := filepath.Join(root, "default/ufw")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0755); err != nil {
		t.Fatal(err)
	}
	policy := provision.FirewallFileExpectations()[0]
	if err := os.WriteFile(defaultPath, []byte(policy.Line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sudoers := provision.ShipSudoersExpectation()
	sudoersPath := filepath.Join(root, "sudoers.d/ship")
	if err := os.MkdirAll(filepath.Dir(sudoersPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sudoersPath, []byte(sudoers.Content), 0440); err != nil {
		t.Fatal(err)
	}

	status := "Status: active\nDefault: deny (incoming), allow (outgoing), disabled (routed)\n22/tcp ALLOW\n80/tcp ALLOW\n443/tcp ALLOW\n"
	check := doctorHardeningDriftCheck(os.ReadFile, func() (string, error) { return status, nil }, "fake-vps")
	if check.Status != doctorStatusOK {
		t.Fatalf("faithful hardening fixture = %+v", check)
	}

	beforeBytes, err := os.ReadFile(beforePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultPath, []byte("DEFAULT_FORWARD_POLICY=DROP\n"), 0644); err != nil {
		t.Fatal(err)
	}
	check = doctorHardeningDriftCheck(os.ReadFile, func() (string, error) { return status, nil }, "fake-vps")
	if check.Status != doctorStatusDegraded || !strings.Contains(check.Evidence, "/etc/default/ufw: 1 difference") {
		t.Fatalf("firewall drift = %+v", check)
	}
	afterBytes, err := os.ReadFile(beforePath)
	if err != nil || string(afterBytes) != string(beforeBytes) {
		t.Fatalf("doctor changed before.rules: before=%q after=%q err=%v", beforeBytes, afterBytes, err)
	}

	checks := doctorChecksFor(doctorOptions{
		ReadFile:       os.ReadFile,
		FirewallStatus: func() (string, error) { return status, nil },
		AppEnvs:        func() ([]appEnvStatus, error) { return nil, nil },
		TLSStatuses:    func(time.Time) ([]tlsCertStatus, error) { return nil, nil },
	})
	raw, err := json.Marshal(checks)
	if err != nil || !strings.Contains(string(raw), `"id":"hardening_drift"`) {
		t.Fatalf("doctor JSON shape = %s err=%v", raw, err)
	}
}

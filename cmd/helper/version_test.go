package helper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
)

func TestReadBoxStatusSummaryUsesIdentityLayoutAndDoctorRecord(t *testing.T) {
	root := t.TempDir()
	setTestStateRoot(t, filepath.Join(root, "state"))
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))
	authorizedKeysPath := filepath.Join(root, "authorized_keys")
	t.Setenv("SHIP_AUTHORIZED_KEYS_FILE", authorizedKeysPath)
	if err := os.WriteFile(authorizedKeysPath, []byte(alicePublicKey+"\n"+bobPublicKey+"\n"+alicePublicKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: "production", InfraID: identity.InfraID("api", "production")})
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: "preview", InfraID: identity.InfraID("api", "preview")})
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "web", Env: "production", InfraID: identity.InfraID("web", "production")})
	if err := store.Default().WriteMembers(store.MembersFile{
		Version: store.CurrentVersion,
		Members: map[string]store.MemberRecord{
			aliceFingerprint: {Name: "alice", Role: store.MemberRoleOwner},
			bobFingerprint:   {Name: "shipper", Role: store.MemberRoleShipper},
		},
	}); err != nil {
		t.Fatal(err)
	}

	brokenIdentity := identity.IdentityFile("broken", "production")
	if err := os.MkdirAll(brokenIdentity, 0755); err != nil {
		t.Fatal(err)
	}

	if err := store.Default().WriteDoctor(store.DoctorFile{
		Version:    store.CurrentVersion,
		RecordedAt: "2026-07-14T08:00:00Z",
		Checks: []store.DoctorCheck{
			{ID: "disk_space", Status: doctorStatusOK},
			{ID: "service_health", Status: doctorStatusFailed},
		},
	}); err != nil {
		t.Fatal(err)
	}

	summary, err := readBoxStatusSummary()
	if err != nil {
		t.Fatal(err)
	}
	wantApps := []boxStatusAppSummary{{App: "api", EnvCount: 2}, {App: "web", EnvCount: 1}}
	if len(summary.Apps) != len(wantApps) {
		t.Fatalf("apps = %+v, want %+v", summary.Apps, wantApps)
	}
	for i, want := range wantApps {
		if summary.Apps[i] != want {
			t.Fatalf("apps = %+v, want %+v", summary.Apps, wantApps)
		}
	}
	if summary.Doctor == nil || summary.Doctor.Status != doctorStatusDegraded || summary.Doctor.RecordedAt != "2026-07-14T08:00:00Z" {
		t.Fatalf("doctor = %+v, want degraded recorded doctor", summary.Doctor)
	}
	if summary.Members == nil || *summary.Members != (boxStatusMembersSummary{Total: 2, Owners: 1}) {
		t.Fatalf("members = %+v, want 2 people and 1 owner", summary.Members)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Doctor  *boxStatusDoctorSummary  `json:"doctor"`
		Members *boxStatusMembersSummary `json:"members"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Doctor == nil || *payload.Doctor != *summary.Doctor {
		t.Fatalf("doctor JSON = %s", data)
	}
	if payload.Members == nil || *payload.Members != *summary.Members {
		t.Fatalf("members JSON = %s", data)
	}
}

func TestReadBoxStatusSummaryIgnoresUnreadableMembers(t *testing.T) {
	root := t.TempDir()
	setTestStateRoot(t, filepath.Join(root, "state"))
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))

	membersPath := store.Default().MembersPath()
	if err := os.MkdirAll(filepath.Dir(membersPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(membersPath, []byte(`{"version":`), 0644); err != nil {
		t.Fatal(err)
	}

	summary, err := readBoxStatusSummary()
	if err != nil {
		t.Fatalf("summary should degrade gracefully when members cannot be read: %v", err)
	}
	if summary.Members != nil {
		t.Fatalf("members = %+v, want omitted for unreadable state", summary.Members)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["members"]; ok {
		t.Fatalf("members JSON = %s, want omitted", data)
	}
}

func TestReadBoxStatusSummaryOmitsDoctorBeforeFirstRecord(t *testing.T) {
	root := t.TempDir()
	setTestStateRoot(t, filepath.Join(root, "state"))
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))

	summary, err := readBoxStatusSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Doctor != nil {
		t.Fatalf("doctor = %+v, want nil before the first record", summary.Doctor)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["doctor"]; ok {
		t.Fatalf("doctor JSON = %s, want doctor omitted", data)
	}
	if string(payload["apps"]) != "[]" {
		t.Fatalf("apps JSON = %s, want empty array", data)
	}
	if got := string(payload["members"]); got != `{"total":0,"owners":0}` {
		t.Fatalf("members JSON = %s, want empty member summary", data)
	}
}

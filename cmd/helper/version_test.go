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
	t.Setenv("SHIP_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	t.Setenv("SHIP_LOCK_DIR", filepath.Join(root, "locks"))

	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: "production", InfraID: identity.InfraID("api", "production")})
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: "preview", InfraID: identity.InfraID("api", "preview")})
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "web", Env: "production", InfraID: identity.InfraID("web", "production")})

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
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Doctor *boxStatusDoctorSummary `json:"doctor"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Doctor == nil || *payload.Doctor != *summary.Doctor {
		t.Fatalf("doctor JSON = %s", data)
	}
}

func TestReadBoxStatusSummaryOmitsDoctorBeforeFirstRecord(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_STATE_DIR", filepath.Join(root, "state"))
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
}

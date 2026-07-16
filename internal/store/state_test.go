package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreUsesSeparatedStateRootsAndModes(t *testing.T) {
	root := t.TempDir()
	varRoot := filepath.Join(root, "var")
	runRoot := filepath.Join(root, "run")
	state := Store{Root: filepath.Join(root, "etc"), VarRoot: varRoot, RunRoot: runRoot}

	if got := state.DoctorPath(); got != filepath.Join(varRoot, "doctor.json") {
		t.Fatalf("doctor path = %s", got)
	}
	if got := state.ApprovalsJournalPath(); got != filepath.Join(varRoot, "approvals-journal.jsonl") {
		t.Fatalf("approval journal path = %s", got)
	}
	if got := state.UpdatesJournalPath(); got != filepath.Join(varRoot, "updates-journal.jsonl") {
		t.Fatalf("update journal path = %s", got)
	}
	if got := state.ApprovalsPath(); got != filepath.Join(runRoot, "approvals.json") {
		t.Fatalf("approvals path = %s", got)
	}

	if err := state.WriteDoctor(DoctorFile{Version: CurrentVersion, RecordedAt: "2026-07-16T10:00:00Z", Checks: []DoctorCheck{{ID: "disk_space", Status: "ok"}}}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteApprovals(ApprovalsFile{Version: CurrentVersion, Requests: []ApprovalRequest{}}); err != nil {
		t.Fatal(err)
	}
	assertMode(t, varRoot, 0755)
	assertMode(t, runRoot, 0700)
	assertMode(t, state.DoctorPath(), 0644)
	assertMode(t, state.ApprovalsPath(), 0600)

	if _, err := os.Stat(filepath.Join(state.Root, "doctor.json")); !os.IsNotExist(err) {
		t.Fatalf("doctor unexpectedly written under intent root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state.Root, "approvals.json")); !os.IsNotExist(err) {
		t.Fatalf("approvals unexpectedly written under intent root: %v", err)
	}
}

func TestStorePathEnvironmentOverrides(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "etc")
	varRoot := filepath.Join(t.TempDir(), "var")
	runRoot := filepath.Join(t.TempDir(), "run")
	t.Setenv("SHIP_STATE_DIR", stateRoot)
	t.Setenv("SHIP_VAR_DIR", varRoot)
	t.Setenv("SHIP_RUN_DIR", runRoot)
	state := Default()
	if state.Root != stateRoot || state.DoctorPath() != filepath.Join(varRoot, "doctor.json") || state.ApprovalsPath() != filepath.Join(runRoot, "approvals.json") {
		t.Fatalf("environment roots not applied: %+v doctor=%s approvals=%s", state, state.DoctorPath(), state.ApprovalsPath())
	}
}

func TestReadApprovalsMissingFileIsEmptyRegister(t *testing.T) {
	state := Store{Root: t.TempDir(), RunRoot: t.TempDir()}
	file, err := state.ReadApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if file.Version != CurrentVersion || file.Requests == nil || len(file.Requests) != 0 {
		t.Fatalf("missing approvals = %+v", file)
	}
}

func TestDoctorCheckpointRoundTripOmitsDelta(t *testing.T) {
	state := Store{Root: t.TempDir(), VarRoot: t.TempDir()}
	if err := state.WriteDoctor(DoctorFile{
		Version:    CurrentVersion,
		RecordedAt: "2026-07-16T10:00:00Z",
		Checks:     []DoctorCheck{{ID: "service_health", Status: "degraded"}},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(state.DoctorPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "delta") {
		t.Fatalf("doctor checkpoint persisted delta: %s", raw)
	}
	loaded, err := state.ReadDoctor()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RecordedAt != "2026-07-16T10:00:00Z" || len(loaded.Checks) != 1 || loaded.Checks[0].Status != "degraded" {
		t.Fatalf("doctor checkpoint changed on round trip: %+v", loaded)
	}
}

func TestBoxConfigAcceptsAddressAndMirrorsWebhookPolicy(t *testing.T) {
	key, ok := LookupBoxConfigKey("box.address")
	if !ok {
		t.Fatal("box.address missing from closed schema")
	}
	webhook, _ := LookupBoxConfigKey("webhook.url")
	if key.WriteRole != webhook.WriteRole || key.OutOfRoleNeedsApproval != webhook.OutOfRoleNeedsApproval {
		t.Fatalf("box.address policy = %+v, webhook policy = %+v", key, webhook)
	}
	for _, value := range []string{"example.com", "203.0.113.7:8443"} {
		if err := ValidateBoxConfigValue("box.address", value); err != nil {
			t.Errorf("address %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "https://example.com", "example.com:0", "example.com:65536", "example.com:not-a-port", "bad host"} {
		if err := ValidateBoxConfigValue("box.address", value); err == nil {
			t.Errorf("address %q accepted", value)
		}
	}
}

func TestBoxConfigRejectsUnknownKeysAndWrongTypes(t *testing.T) {
	state := Store{Root: t.TempDir()}
	if err := os.WriteFile(state.BoxConfigPath(), []byte(`{"version":1,"values":{"unknown.key":"value"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadBoxConfig(); err == nil {
		t.Fatal("ReadBoxConfig accepted an unknown key")
	}
	if err := os.WriteFile(state.BoxConfigPath(), []byte(`{"version":1,"values":{"box.address":123}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadBoxConfig(); err == nil {
		t.Fatal("ReadBoxConfig accepted a non-string value")
	}
}

func TestMembersRejectNormalizedNameRoleCollision(t *testing.T) {
	state := Store{Root: t.TempDir()}
	file := MembersFile{Version: CurrentVersion, Members: map[string]MemberRecord{
		"SHA256:agent": {Name: "shared", Role: MemberRoleAgent},
		"SHA256:owner": {Name: " shared ", Role: MemberRoleOwner},
	}}
	if err := state.WriteMembers(file); err == nil || !strings.Contains(err.Error(), `member "shared" has conflicting roles`) {
		t.Fatalf("WriteMembers collision error = %v", err)
	}
}

func TestStoreValidatesVersions(t *testing.T) {
	state := Store{Root: t.TempDir(), VarRoot: t.TempDir()}
	if err := state.WriteDoctor(DoctorFile{}); err == nil || !strings.Contains(err.Error(), "doctor.json version is required") {
		t.Fatalf("zero doctor version error = %v", err)
	}
	if err := os.WriteFile(state.DoctorPath(), []byte(`{"version":2,"recorded_at":"now","checks":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadDoctor(); err == nil || !strings.Contains(err.Error(), "unsupported doctor.json version 2") {
		t.Fatalf("future doctor version error = %v", err)
	}
}

func TestStoreWritesMembersAndBoxConfigInEtcRoot(t *testing.T) {
	state := Store{Root: t.TempDir(), VarRoot: t.TempDir(), RunRoot: t.TempDir()}
	if err := state.WriteMembers(MembersFile{Version: CurrentVersion, Members: map[string]MemberRecord{"SHA256:abc": {Name: "alice", Role: MemberRoleOwner}}}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteBoxConfig(BoxConfigFile{Version: CurrentVersion, Values: map[string]string{"box.address": "example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadMembers(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadBoxConfig(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, state.MembersPath(), 0644)
	assertMode(t, state.BoxConfigPath(), 0600)
	if _, err := json.Marshal(state); err != nil {
		t.Fatal(err)
	}
}

func TestGoSourcesContainNoLegacyHostStateReference(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	legacy := "host" + ".json"
	err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), legacy) {
			return &legacyReferenceError{path: path}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type legacyReferenceError struct{ path string }

func (e *legacyReferenceError) Error() string { return "legacy host state reference in " + e.path }

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("expected %s mode %o, got %o", path, want, got)
	}
}

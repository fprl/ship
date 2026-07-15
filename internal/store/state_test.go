package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreWritesADR0002Files(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}

	desired := validHostDesired()
	observed := HostObserved{
		Packages: map[string]ObservedPackage{
			"podman": {Version: "5.0.3"},
		},
	}

	if err := store.WriteHostDesired(desired); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteHostState(observed, HostMeta{}); err != nil {
		t.Fatal(err)
	}
	if got := store.HostPath(); got != filepath.Join(root, "host.json") {
		t.Fatalf("unexpected host path: %s", got)
	}

	data, err := os.ReadFile(store.HostPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"version": 1`,
		`"desired": {`,
		`"observed": {`,
		`"meta": {`,
		`"expose": "private"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected host.json to contain %q:\n%s", want, text)
		}
	}
	assertMode(t, store.HostPath(), 0644)

	loaded, err := store.ReadHost()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 1 || loaded.Desired.Users.Operator != "operator" {
		t.Fatalf("unexpected loaded host file: %+v", loaded)
	}

	if err := store.WriteDoctor(DoctorFile{
		Version:    CurrentVersion,
		RecordedAt: "2026-07-07T10:00:00Z",
		Checks: []DoctorCheck{
			{ID: "disk_space", Status: "ok", Evidence: "used=10%", Remediation: "ship box doctor fake-vps"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.DoctorPath(); got != filepath.Join(root, "doctor.json") {
		t.Fatalf("unexpected doctor path: %s", got)
	}
	doctorData, err := os.ReadFile(store.DoctorPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doctorData), `"delta": []`) {
		t.Fatalf("doctor state should encode empty delta as [], got:\n%s", doctorData)
	}
	assertMode(t, store.DoctorPath(), 0644)

	doctorState, err := store.ReadDoctor()
	if err != nil {
		t.Fatal(err)
	}
	if doctorState.Version != CurrentVersion || len(doctorState.Checks) != 1 || len(doctorState.Delta) != 0 {
		t.Fatalf("unexpected doctor state: %+v", doctorState)
	}

	if err := store.WriteMembers(MembersFile{
		Version: CurrentVersion,
		Members: map[string]MemberRecord{
			"SHA256:abc": {Name: "alice", Role: MemberRoleOwner},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.MembersPath(); got != filepath.Join(root, "members.json") {
		t.Fatalf("unexpected members path: %s", got)
	}
	membersData, err := os.ReadFile(store.MembersPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"version": 1`,
		`"members": {`,
		`"SHA256:abc": {`,
		`"name": "alice"`,
		`"role": "owner"`,
	} {
		if !strings.Contains(string(membersData), want) {
			t.Fatalf("expected members.json to contain %q:\n%s", want, membersData)
		}
	}
	assertMode(t, store.MembersPath(), 0644)
	tempFiles, err := filepath.Glob(filepath.Join(root, ".members.json.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tempFiles) != 0 {
		t.Fatalf("atomic members write left temp files: %v", tempFiles)
	}
	membersState, err := store.ReadMembers()
	if err != nil {
		t.Fatal(err)
	}
	if membersState.Version != CurrentVersion || membersState.Members["SHA256:abc"].Role != MemberRoleOwner {
		t.Fatalf("unexpected members state: %+v", membersState)
	}

	if err := store.WriteBoxConfig(BoxConfigFile{Version: CurrentVersion, Values: map[string]string{"webhook.url": "https://ntfy.example/ship"}}); err != nil {
		t.Fatal(err)
	}
	if got := store.BoxConfigPath(); got != filepath.Join(root, "box-config.json") {
		t.Fatalf("unexpected box config path: %s", got)
	}
	assertMode(t, store.BoxConfigPath(), 0600)
	boxConfig, err := store.ReadBoxConfig()
	if err != nil {
		t.Fatal(err)
	}
	if boxConfig.Values["webhook.url"] != "https://ntfy.example/ship" {
		t.Fatalf("box config webhook.url = %q", boxConfig.Values["webhook.url"])
	}

	if err := store.WriteApprovals(ApprovalsFile{
		Version: CurrentVersion,
		Requests: []ApprovalRequest{
			{
				ID: "abc123xy",
				Member: ApprovalMember{
					Fingerprint: "SHA256:agent",
					Name:        "agent",
					Role:        MemberRoleAgent,
				},
				RequiredRole: MemberRoleShipper,
				Verb:         "ship",
				Target: ApprovalTarget{
					App:     "api",
					Env:     "production",
					Class:   "production",
					Args:    []string{"release=abc123"},
					Summary: "ship app=api env=production class=production release=abc123",
				},
				MatchKey:  `{"member":"SHA256:agent","verb":"ship"}`,
				Status:    ApprovalStatusPending,
				CreatedAt: "2026-07-08T10:00:00Z",
				ExpiresAt: "2026-07-08T10:15:00Z",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.ApprovalsPath(); got != filepath.Join(root, "approvals.json") {
		t.Fatalf("unexpected approvals path: %s", got)
	}
	if got := store.ApprovalsJournalPath(); got != filepath.Join(root, "approvals-journal.jsonl") {
		t.Fatalf("unexpected approvals journal path: %s", got)
	}
	approvalsData, err := os.ReadFile(store.ApprovalsPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"version": 1`,
		`"requests": [`,
		`"id": "abc123xy"`,
		`"role": "agent"`,
		`"required_role": "shipper"`,
		`"status": "pending"`,
		`"expires": "2026-07-08T10:15:00Z"`,
	} {
		if !strings.Contains(string(approvalsData), want) {
			t.Fatalf("expected approvals.json to contain %q:\n%s", want, approvalsData)
		}
	}
	assertMode(t, store.ApprovalsPath(), 0644)
	tempFiles, err = filepath.Glob(filepath.Join(root, ".approvals.json.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tempFiles) != 0 {
		t.Fatalf("atomic approvals write left temp files: %v", tempFiles)
	}
	approvalsState, err := store.ReadApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if approvalsState.Version != CurrentVersion || len(approvalsState.Requests) != 1 || approvalsState.Requests[0].Member.Role != MemberRoleAgent || approvalsState.Requests[0].RequiredRole != MemberRoleShipper {
		t.Fatalf("unexpected approvals state: %+v", approvalsState)
	}
}

func TestMembersRejectNormalizedNameRoleCollision(t *testing.T) {
	state := Store{Root: t.TempDir()}
	file := MembersFile{
		Version: CurrentVersion,
		Members: map[string]MemberRecord{
			"SHA256:agent": {Name: "shared", Role: MemberRoleAgent},
			"SHA256:owner": {Name: " shared ", Role: MemberRoleOwner},
		},
	}
	if err := state.WriteMembers(file); err == nil || !strings.Contains(err.Error(), `member "shared" has conflicting roles`) {
		t.Fatalf("WriteMembers collision error = %v", err)
	}
	if err := os.WriteFile(state.MembersPath(), []byte(`{"version":1,"members":{"SHA256:agent":{"name":"shared","role":"agent"},"SHA256:owner":{"name":" shared ","role":"owner"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadMembers(); err == nil || !strings.Contains(err.Error(), `member "shared" has conflicting roles`) {
		t.Fatalf("ReadMembers collision error = %v", err)
	}
}

func TestBoxConfigRefusesUnknownKeysAndWrongTypes(t *testing.T) {
	state := Store{Root: t.TempDir()}
	if err := os.WriteFile(state.BoxConfigPath(), []byte(`{"version":1,"values":{"unknown.key":"value"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadBoxConfig(); err == nil {
		t.Fatal("ReadBoxConfig accepted an unknown key")
	}
	if err := os.WriteFile(state.BoxConfigPath(), []byte(`{"version":1,"values":{"webhook.url":123}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadBoxConfig(); err == nil {
		t.Fatal("ReadBoxConfig accepted a non-string value")
	}
}

func TestWriteHostStatePreservesDesired(t *testing.T) {
	store := Store{Root: t.TempDir()}
	raw := `{
  "version": 1,
  "desired": {
      "users": {"operator":"operator", "deploy":"deploy"},
      "ingress": {"expose":"private"},
      "features": {"docker":false},
      "packages": {
        "podman": {"track":"noble", "source":"ubuntu"}
      }
  },
  "observed": {
    "packages": {},
    "ingress": {}
  },
  "meta": {}
}`
	if err := os.WriteFile(store.HostPath(), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	before := hostDesiredRaw(t, store.HostPath())

	if err := store.WriteHostState(HostObserved{
		Packages: map[string]ObservedPackage{
			"caddy": {Version: "2.8.4"},
		},
	}, HostMeta{
		ShipVersion: "0.3.0",
	}); err != nil {
		t.Fatal(err)
	}
	after := hostDesiredRaw(t, store.HostPath())

	if before != after {
		t.Fatalf("WriteHostState mutated desired:\nbefore: %s\nafter:  %s", before, after)
	}

	loaded, err := store.ReadHost()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Observed.Packages["caddy"].Version != "2.8.4" {
		t.Fatalf("observed package version was not written: %+v", loaded.Observed.Packages)
	}
	if loaded.Meta.ShipVersion != "0.3.0" {
		t.Fatalf("meta was not written: %+v", loaded.Meta)
	}
	written, err := os.ReadFile(store.HostPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), `"ship_version": "0.3.0"`) {
		t.Fatalf("host state should write ship_version:\n%s", written)
	}
}

func TestWriteHostStateIsStableAcrossRepeatedWrites(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := store.WriteHostDesired(validHostDesired()); err != nil {
		t.Fatal(err)
	}
	observed := HostObserved{
		Packages: map[string]ObservedPackage{
			"caddy": {Version: "2.8.4"},
		},
	}
	meta := HostMeta{ShipVersion: "0.3.0"}

	if err := store.WriteHostState(observed, meta); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.HostPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadHost(); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteHostState(observed, meta); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(store.HostPath())
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Fatalf("host state rewrites are not stable:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestStoreRejectsInvalidHostVersions(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "missing",
			raw:  `{"version":0}`,
			want: "host.json version is required",
		},
		{
			name: "future",
			raw:  `{"version":2}`,
			want: "unsupported host.json version 2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			writeRawFile(t, store.HostPath(), tc.raw)

			_, err := store.ReadHost()
			if err == nil {
				t.Fatal("expected invalid host schema version to fail")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestStoreRejectsInvalidMembersFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "missing version",
			raw:  `{"members":{}}`,
			want: "members.json version is required",
		},
		{
			name: "invalid role",
			raw:  `{"version":1,"members":{"SHA256:abc":{"name":"alice","role":"admin"}}}`,
			want: "role must be owner, shipper, or agent",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			writeRawFile(t, store.MembersPath(), tc.raw)

			_, err := store.ReadMembers()
			if err == nil {
				t.Fatal("expected invalid members file to fail")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestStoreTracksHostInstalledByHostFilePresence(t *testing.T) {
	store := Store{Root: t.TempDir()}

	installed, err := store.HostInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("expected fresh store to report host not installed")
	}

	if err := store.WriteHostDesired(validHostDesired()); err != nil {
		t.Fatal(err)
	}
	installed, err = store.HostInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Fatal("expected host file to report installed")
	}
}

func TestStoreValidatesVersionsAcrossStateFiles(t *testing.T) {
	store := Store{Root: t.TempDir()}

	for _, tc := range []struct {
		name        string
		path        string
		read        func() error
		writeZero   func() error
		zeroRaw     string
		futureRaw   string
		required    string
		unsupported string
	}{
		{
			name:        "doctor",
			path:        store.DoctorPath(),
			read:        func() error { _, err := store.ReadDoctor(); return err },
			writeZero:   func() error { return store.WriteDoctor(DoctorFile{}) },
			zeroRaw:     `{"version":0,"recorded_at":"2026-07-07T10:00:00Z","checks":[],"delta":[]}`,
			futureRaw:   `{"version":2,"recorded_at":"2026-07-07T10:00:00Z","checks":[],"delta":[]}`,
			required:    "doctor.json version is required",
			unsupported: "unsupported doctor.json version 2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.writeZero(); err == nil || !strings.Contains(err.Error(), tc.required) {
				t.Fatalf("expected write error %q, got %v", tc.required, err)
			}
			writeRawFile(t, tc.path, tc.zeroRaw)
			if err := tc.read(); err == nil || !strings.Contains(err.Error(), tc.required) {
				t.Fatalf("expected read error %q, got %v", tc.required, err)
			}
			writeRawFile(t, tc.path, tc.futureRaw)
			if err := tc.read(); err == nil || !strings.Contains(err.Error(), tc.unsupported) {
				t.Fatalf("expected read error %q, got %v", tc.unsupported, err)
			}
		})
	}
}

func TestWriteHostStateRequiresExistingHostDesired(t *testing.T) {
	store := Store{Root: t.TempDir()}

	err := store.WriteHostState(HostObserved{}, HostMeta{})
	if err == nil {
		t.Fatal("expected missing host.json to fail")
	}
	if !strings.Contains(err.Error(), "host.json is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadHostRejectsInvalidDesiredValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*HostDesired)
		want   string
	}{
		{
			name:   "missing operator",
			mutate: func(d *HostDesired) { d.Users.Operator = "" },
			want:   "users.operator",
		},
		{
			name:   "missing deploy",
			mutate: func(d *HostDesired) { d.Users.Deploy = "" },
			want:   "users.deploy",
		},
		{
			name:   "invalid expose",
			mutate: func(d *HostDesired) { d.Ingress.Expose = "" },
			want:   "ingress.expose",
		},
		{
			name: "missing package source",
			mutate: func(d *HostDesired) {
				d.Packages["caddy"] = DesiredPackage{Track: "stable"}
			},
			want: "packages.caddy.source",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			desired := validHostDesired()
			tc.mutate(&desired)
			writeHostWithDesired(t, store, desired)

			_, err := store.ReadHost()
			if err == nil {
				t.Fatal("expected invalid desired to fail")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func validHostDesired() HostDesired {
	return HostDesired{
		Users: HostUsers{
			Operator: "operator",
			Deploy:   "deploy",
		},
		Ingress: HostIngressDesired{
			Expose: ExposePrivate,
		},
		Features: HostFeatures{},
		Packages: map[string]DesiredPackage{},
	}
}

func hostDesiredRaw(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Desired json.RawMessage `json:"desired"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return string(raw.Desired)
}

func writeHostWithDesired(t *testing.T, store Store, desired HostDesired) {
	t.Helper()
	data, err := json.MarshalIndent(HostFile{
		Version:  CurrentVersion,
		Desired:  desired,
		Observed: HostObserved{Packages: map[string]ObservedPackage{}},
		Meta:     HostMeta{},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeRawFile(t, store.HostPath(), string(data))
}

func writeRawFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

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

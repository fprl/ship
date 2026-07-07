package helper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/store"
)

func TestDoctorHostStateCheckReportsMissingHostWithoutRawError(t *testing.T) {
	root := t.TempDir()
	secretsRoot := prepareDoctorSecretsRoot(t, 0700)
	stateStore := store.Store{Root: root}
	_ = secretsRoot

	check := doctorHostStateCheck(stateStore, "fake-vps")
	if check.Status != doctorStatusFailed || !strings.Contains(check.Evidence, "host is not installed") {
		t.Fatalf("unexpected missing host check: %+v", check)
	}
	if strings.Contains(check.Evidence, "open ") {
		t.Fatalf("doctor leaked raw open error: %s", check.Evidence)
	}
	if check.Remediation != "ship box init fake-vps" {
		t.Fatalf("unexpected remediation: %s", check.Remediation)
	}
}

func TestDoctorHostStateCheckClearsAfterValidHost(t *testing.T) {
	root := t.TempDir()
	prepareDoctorSecretsRoot(t, 0700)
	stateStore := store.Store{Root: root}
	writeValidHost(t, stateStore.HostPath())

	check := doctorHostStateCheck(stateStore, "fake-vps")
	if check.Status != doctorStatusOK {
		t.Fatalf("expected ok check for a valid host, got: %+v", check)
	}
}

func TestDoctorHostStateCheckReportsWrongSecretsRootMode(t *testing.T) {
	root := t.TempDir()
	secretsRoot := prepareDoctorSecretsRoot(t, 0755)
	stateStore := store.Store{Root: root}
	writeValidHost(t, stateStore.HostPath())

	check := doctorHostStateCheck(stateStore, "fake-vps")
	if check.Status != doctorStatusFailed || !strings.Contains(check.Evidence, "mode 755, want 700") || !strings.Contains(check.Evidence, secretsRoot) {
		t.Fatalf("unexpected secrets root check: %+v", check)
	}
}

func prepareDoctorSecretsRoot(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHIP_SECRETS_DIR", path)
	return path
}

func TestDoctorReportJSONShape(t *testing.T) {
	checks := []store.DoctorCheck{
		{ID: "host_state", Status: "failed", Evidence: "host is not installed", Remediation: "ship box init fake-vps"},
	}
	raw, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []store.DoctorCheck
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].ID != "host_state" || decoded[0].Status != "failed" || decoded[0].Remediation == "" {
		t.Fatalf("unexpected doctor JSON shape: %s", raw)
	}
}

func TestDoctorServiceFindingsRequireCaddy(t *testing.T) {
	desired := validDoctorHostDesired()
	findings := doctorServiceFindingsFor(desired, func(service string) string {
		if service == "caddy" {
			return "failed"
		}
		return "inactive"
	})

	if len(findings) != 1 || !strings.Contains(findings[0], "caddy service is failed") {
		t.Fatalf("unexpected service findings: %+v", findings)
	}
}

func TestDoctorServiceFindingsAllowInactiveOptionalServices(t *testing.T) {
	desired := validDoctorHostDesired()
	findings := doctorServiceFindingsFor(desired, func(service string) string {
		if service == "caddy" {
			return "active"
		}
		return "inactive"
	})

	if len(findings) != 0 {
		t.Fatalf("expected inactive optional services to pass, got: %+v", findings)
	}
}

func TestDoctorServiceFindingsRequireConfiguredTunnelService(t *testing.T) {
	desired := validDoctorHostDesired()
	desired.Ingress.Tunnel = store.TunnelCloudflare
	findings := doctorServiceFindingsFor(desired, func(service string) string {
		if service == "caddy" {
			return "active"
		}
		return "inactive"
	})

	if len(findings) != 1 || !strings.Contains(findings[0], "cloudflared service is inactive") {
		t.Fatalf("unexpected service findings: %+v", findings)
	}
}

func TestDoctorDiskSpaceThresholds(t *testing.T) {
	tests := []struct {
		name   string
		used   uint64
		status string
	}{
		{name: "below degraded", used: 79, status: doctorStatusOK},
		{name: "degraded at 80", used: 80, status: doctorStatusDegraded},
		{name: "failed at 90", used: 90, status: doctorStatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := doctorDiskSpaceCheck(func(path string) (diskUsage, error) {
				return diskUsage{Path: path, TotalBytes: 100 * gib, AvailableBytes: (100 - tt.used) * gib}, nil
			}, "fake-vps")
			if check.Status != tt.status {
				t.Fatalf("status = %s, want %s (%+v)", check.Status, tt.status, check)
			}
			if !strings.Contains(check.Evidence, "used=") || !strings.Contains(check.Evidence, "GiB") {
				t.Fatalf("disk evidence should include actual numbers: %+v", check)
			}
		})
	}
}

func TestDoctorTLSCertificateThresholds(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		statuses []tlsCertStatus
		want     string
	}{
		{name: "no routed hosts", statuses: nil, want: doctorStatusOK},
		{name: "ok at 14 days", statuses: []tlsCertStatus{{Host: "api.example.com", Found: true, NotAfter: now.Add(14 * 24 * time.Hour)}}, want: doctorStatusOK},
		{name: "degraded below 14 days", statuses: []tlsCertStatus{{Host: "api.example.com", Found: true, NotAfter: now.Add(13 * 24 * time.Hour)}}, want: doctorStatusDegraded},
		{name: "failed when expired", statuses: []tlsCertStatus{{Host: "api.example.com", Found: true, NotAfter: now.Add(-24 * time.Hour)}}, want: doctorStatusFailed},
		{name: "failed when missing", statuses: []tlsCertStatus{{Host: "api.example.com", Found: false}}, want: doctorStatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := doctorTLSCertsCheck(func(time.Time) ([]tlsCertStatus, error) {
				return tt.statuses, nil
			}, now, "fake-vps")
			if check.Status != tt.want {
				t.Fatalf("status = %s, want %s (%+v)", check.Status, tt.want, check)
			}
			if check.Evidence == "" {
				t.Fatalf("TLS evidence must not be empty: %+v", check)
			}
		})
	}
}

func TestDoctorReaperTimerCheckRequiresPresentActiveEnabledTimer(t *testing.T) {
	tests := []struct {
		name  string
		state systemdUnitState
		want  string
	}{
		{name: "ok", state: systemdUnitState{Name: reaperTimerUnit, Path: "/etc/systemd/system/" + reaperTimerUnit, Present: true, Active: "active", Enabled: "enabled"}, want: doctorStatusOK},
		{name: "degraded when inactive", state: systemdUnitState{Name: reaperTimerUnit, Path: "/etc/systemd/system/" + reaperTimerUnit, Present: true, Active: "inactive", Enabled: "enabled"}, want: doctorStatusDegraded},
		{name: "failed when missing", state: systemdUnitState{Name: reaperTimerUnit, Path: "/etc/systemd/system/" + reaperTimerUnit, Present: false, Active: "inactive", Enabled: "disabled"}, want: doctorStatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := doctorReaperTimerCheck(func(string) systemdUnitState { return tt.state }, "fake-vps")
			if check.Status != tt.want {
				t.Fatalf("status = %s, want %s (%+v)", check.Status, tt.want, check)
			}
		})
	}
}

func TestDoctorDeployJournalCheckReadsEachAppEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	journalPath := identity.DeployJournalFile("api", "production")
	if err := os.MkdirAll(filepath.Dir(journalPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, []byte(`{"schema_version":1,"app":"api","env":"production"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := doctorDeployJournalsCheck(func() ([]appEnvStatus, error) {
		return []appEnvStatus{{App: "api", Env: "production"}}, nil
	}, "fake-vps")
	if check.Status != doctorStatusOK || !strings.Contains(check.Evidence, "api/production") {
		t.Fatalf("unexpected journal check: %+v", check)
	}

	missing := doctorDeployJournalsCheck(func() ([]appEnvStatus, error) {
		return []appEnvStatus{{App: "web", Env: "production"}}, nil
	}, "fake-vps")
	if missing.Status != doctorStatusFailed || !strings.Contains(missing.Remediation, "touch") {
		t.Fatalf("missing journal should fail with runnable remediation: %+v", missing)
	}
}

func TestDoctorDeltaTracksSeverityIncreasesOnly(t *testing.T) {
	previous := []store.DoctorCheck{{ID: doctorCheckReaperTimer, Status: doctorStatusDegraded}}
	if delta := doctorDelta(previous, []store.DoctorCheck{{ID: doctorCheckReaperTimer, Status: doctorStatusDegraded}}); len(delta) != 0 {
		t.Fatalf("unchanged degraded check should not be delta: %+v", delta)
	}
	if delta := doctorDelta(previous, []store.DoctorCheck{{ID: doctorCheckReaperTimer, Status: doctorStatusFailed}}); len(delta) != 1 {
		t.Fatalf("degraded to failed should be delta: %+v", delta)
	}
	if delta := doctorDelta([]store.DoctorCheck{{ID: doctorCheckReaperTimer, Status: doctorStatusFailed}}, []store.DoctorCheck{{ID: doctorCheckReaperTimer, Status: doctorStatusDegraded}}); len(delta) != 0 {
		t.Fatalf("failed to degraded should not be delta: %+v", delta)
	}
	if delta := doctorDelta(nil, []store.DoctorCheck{{ID: doctorCheckReaperTimer, Status: doctorStatusDegraded}}); len(delta) != 1 {
		t.Fatalf("first degraded observation should be delta: %+v", delta)
	}
}

func TestRecordDoctorRunPersistsChecksAndDelta(t *testing.T) {
	stateStore := store.Store{Root: t.TempDir()}
	writeValidHost(t, stateStore.HostPath())
	prepareDoctorSecretsRoot(t, 0700)
	setupDoctorSudoers(t)
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	timer := systemdUnitState{Name: reaperTimerUnit, Path: "/etc/systemd/system/" + reaperTimerUnit, Present: true, Active: "inactive", Enabled: "enabled"}
	opts := doctorOptions{
		StateStore: stateStore,
		Now:        func() time.Time { return now },
		Service:    func(string) string { return "active" },
		Disk: func(path string) (diskUsage, error) {
			return diskUsage{Path: path, TotalBytes: 100 * gib, AvailableBytes: 90 * gib}, nil
		},
		TLSStatuses: func(time.Time) ([]tlsCertStatus, error) { return nil, nil },
		AppEnvs:     func() ([]appEnvStatus, error) { return nil, nil },
		Timer:       func(string) systemdUnitState { return timer },
	}

	first, err := recordDoctorRun(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Checks) == 0 || len(first.Delta) != 1 || first.Delta[0].ID != doctorCheckReaperTimer {
		t.Fatalf("unexpected first recorded doctor state: %+v", first)
	}

	second, err := recordDoctorRun(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Delta) != 0 {
		t.Fatalf("second unchanged run should have empty delta: %+v", second.Delta)
	}

	loaded, err := stateStore.ReadDoctor()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RecordedAt != now.Format(time.RFC3339Nano) || len(loaded.Delta) != 0 {
		t.Fatalf("unexpected persisted doctor state: %+v", loaded)
	}
}

func TestHelperSudoRegexRequiresServerSubtree(t *testing.T) {
	good := "deploy ALL=(root) NOPASSWD: /usr/local/bin/ship server app *, /usr/local/bin/ship server doctor, /usr/local/bin/ship server doctor *"
	if !HelperSudoRe.MatchString(good) {
		t.Fatal("expected server subtree sudoers grant to match")
	}
	if HelperSudoRe.MatchString("deploy ALL=(root) NOPASSWD: /usr/local/bin/ship") {
		t.Fatal("broad ship sudoers grant must not match")
	}
	if HelperSudoRe.MatchString("deploy ALL=(root) NOPASSWD: /usr/local/bin/ship server *") {
		t.Fatal("whole server subtree grant must not match")
	}
}

const gib = 1024 * 1024 * 1024

func setupDoctorSudoers(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SHIP_SUDOERS_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "operator"), []byte("operator ALL=(ALL) NOPASSWD:ALL\n"), 0440); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ship"), []byte("deploy ALL=(root) NOPASSWD: /usr/local/bin/ship server app *, /usr/local/bin/ship server doctor, /usr/local/bin/ship server doctor *\n"), 0440); err != nil {
		t.Fatal(err)
	}
}

func writeValidHost(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	raw := `{
  "version": 1,
  "desired": {
    "users": {"operator": "operator", "deploy": "deploy"},
    "ingress": {"expose": "private", "tunnel": "none"},
    "features": {"docker": false, "litestream": false},
    "packages": {}
  },
  "observed": {"packages": {}, "ingress": {}},
  "meta": {}
}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
}

func validDoctorHostDesired() store.HostDesired {
	return store.HostDesired{
		Users: store.HostUsers{Operator: "operator", Deploy: "deploy"},
		Ingress: store.HostIngressDesired{
			Expose: store.ExposePublic,
			Tunnel: store.TunnelNone,
		},
		Features: store.HostFeatures{},
		Packages: map[string]store.DesiredPackage{},
	}
}

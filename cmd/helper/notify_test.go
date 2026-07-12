package helper

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
)

func TestNotifyDeployAbortedPayloadCarriesScrubbedJournalAndRemediation(t *testing.T) {
	setupPreviewHostTest(t)
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: productionEnvName, InfraID: identity.InfraID("api", productionEnvName)})
	sink := newNotifyTestSink(t)
	now := time.Date(2026, 7, 7, 10, 1, 2, 0, time.UTC)
	entry := sanitizeDeployJournalEntry("api", productionEnvName, deployJournalEntry{
		Outcome:          "aborted_release",
		StartedAt:        "2026-07-07T10:00:00Z",
		EndedAt:          "2026-07-07T10:00:01Z",
		PreviousRelease:  "aaa111bbb222",
		AttemptedRelease: "ccc333ddd444",
		FailingStep:      "release",
		StderrTail:       "release failed with notify-secret-token",
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}, []string{"notify-secret-token"})

	notifyDeployAborted(sink.URL, &config.AppContext{ProductionBranch: "main"}, entry, now)

	payload := sink.singlePayload(t)
	assertNotifyField(t, payload, "event", notifyEventDeployAborted)
	assertNotifyField(t, payload, "env", "Production main")
	assertNotifyField(t, payload, "release", "ccc333ddd444")
	assertNotifyNestedField(t, payload, "why", "stderr_tail", "release failed with [redacted]")
	assertNotifyNestedField(t, payload, "remediation", "command", "ship")
	assertNotifyNestedField(t, payload, "remediation", "journal.outcome", "aborted_release")
	assertNotifyDoesNotContain(t, payload, "notify-secret-token")
	t.Logf("deploy_aborted payload:\n%s", prettyNotifyJSON(t, payload))
}

func TestNotifyDeployRecoveredPayloadCarriesPreviousFailure(t *testing.T) {
	setupPreviewHostTest(t)
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: productionEnvName, InfraID: identity.InfraID("api", productionEnvName)})
	sink := newNotifyTestSink(t)
	now := time.Date(2026, 7, 7, 10, 2, 3, 0, time.UTC)
	previous := sanitizeDeployJournalEntry("api", productionEnvName, deployJournalEntry{
		Outcome:          "aborted_probe",
		StartedAt:        "2026-07-07T10:00:00Z",
		EndedAt:          "2026-07-07T10:00:01Z",
		PreviousRelease:  "aaa111bbb222",
		AttemptedRelease: "bad333bad333",
		FailingStep:      "probe",
		Probe:            &journalProbe{Status: 502, BodySnippet: "upstream refused"},
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}, nil)
	current := sanitizeDeployJournalEntry("api", productionEnvName, deployJournalEntry{
		Outcome:          "deployed",
		StartedAt:        "2026-07-07T10:02:00Z",
		EndedAt:          "2026-07-07T10:02:02Z",
		PreviousRelease:  "aaa111bbb222",
		AttemptedRelease: "good444good4",
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}, nil)

	notifyDeployRecovered(sink.URL, &config.AppContext{ProductionBranch: "main"}, previous, current, now)

	payload := sink.singlePayload(t)
	assertNotifyField(t, payload, "event", notifyEventDeployRecovered)
	assertNotifyNestedField(t, payload, "why", "previous_failure.outcome", "aborted_probe")
	assertNotifyNestedField(t, payload, "why", "current.outcome", "deployed")
	assertNotifyNestedField(t, payload, "remediation", "command", "ship status")
	assertNotifyNestedField(t, payload, "remediation", "previous_failure.attempted_release", "bad333bad333")
	t.Logf("deploy_recovered payload:\n%s", prettyNotifyJSON(t, payload))
}

func TestNotifyPreviewReapedPayloadCarriesBranchAndEnv(t *testing.T) {
	sink := newNotifyTestSink(t)
	now := time.Date(2026, 7, 7, 10, 3, 4, 0, time.UTC)
	expires := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	file := identity.EnvIdentity{
		Version: 1,
		App:     "api",
		Env:     "feature-payments-ab12",
		InfraID: identity.InfraID("api", "feature-payments-ab12"),
		Preview: &identity.PreviewIdentity{
			Branch:          "feature/payments",
			SanitizedBranch: "feature-payments",
			Env:             "feature-payments-ab12",
			Suffix:          "ab12",
			LastShipAt:      expires.Add(-previewTTL),
			ExpiresAt:       &expires,
		},
	}

	notifyPreviewReaped(sink.URL, file, "aaa111bbb222", now)

	payload := sink.singlePayload(t)
	assertNotifyField(t, payload, "event", notifyEventPreviewReaped)
	assertNotifyField(t, payload, "env", "Preview feature/payments")
	assertNotifyNestedField(t, payload, "why", "branch", "feature/payments")
	assertNotifyNestedField(t, payload, "why", "env", "Preview feature/payments")
	assertNotifyNestedField(t, payload, "remediation", "command", "git checkout feature/payments && ship")
	t.Logf("preview_reaped payload:\n%s", prettyNotifyJSON(t, payload))
}

func TestNotifyDoctorDegradedPayloadCarriesEvidenceAndRunnableRemediation(t *testing.T) {
	setupPreviewHostTest(t)
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	sink := newNotifyTestSink(t)
	if err := store.Default().WriteBoxNotify(store.BoxNotifyFile{Version: store.CurrentVersion, URL: sink.URL}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 7, 10, 4, 5, 0, time.UTC)

	notifyDoctorDegraded("fake-vps", []store.DoctorCheck{{
		ID:          "reaper_timer",
		Status:      "degraded",
		Evidence:    "ship-preview-reaper.timer present, active=inactive, enabled=enabled",
		Remediation: "sudo systemctl enable ship-preview-reaper.timer && sudo systemctl start ship-preview-reaper.timer",
	}}, now)

	payload := sink.singlePayload(t)
	assertNotifyField(t, payload, "event", notifyEventDoctorDegraded)
	assertNotifyField(t, payload, "box", "fake-vps")
	assertNotifyNestedField(t, payload, "why", "evidence", "ship-preview-reaper.timer present, active=inactive, enabled=enabled")
	assertNotifyNestedField(t, payload, "remediation", "command", "sudo systemctl enable ship-preview-reaper.timer && sudo systemctl start ship-preview-reaper.timer")
	t.Logf("doctor_degraded payload:\n%s", prettyNotifyJSON(t, payload))
}

func TestNotifyApprovalRequestedPayloadCarriesLiteralApproveCommand(t *testing.T) {
	setupPreviewHostTest(t)
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: productionEnvName, InfraID: identity.InfraID("api", productionEnvName)})
	sink := newNotifyTestSink(t)
	if err := store.Default().WriteBoxNotify(store.BoxNotifyFile{Version: store.CurrentVersion, URL: sink.URL}); err != nil {
		t.Fatal(err)
	}
	if err := appendDeployJournalEntry("api", productionEnvName, deployJournalEntry{
		Outcome:          "deployed",
		StartedAt:        "2026-07-08T09:00:00Z",
		EndedAt:          "2026-07-08T09:00:01Z",
		AttemptedRelease: "aaa111bbb222",
		Identity:         deployIdentity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	request := store.ApprovalRequest{
		ID: "abc123xy",
		Member: store.ApprovalMember{
			Fingerprint: aliceFingerprint,
			Name:        "alice",
			Role:        store.MemberRoleAgent,
		},
		Verb: "ship",
		Target: store.ApprovalTarget{
			App:     "api",
			Env:     productionEnvName,
			Class:   "production",
			Summary: "app=api env=prod class=production release=aaa111",
		},
		CreatedAt: "2026-07-08T10:00:00Z",
		ExpiresAt: "2026-07-08T10:15:00Z",
	}

	notifyApprovalRequested(request, time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC))

	payload := sink.singlePayload(t)
	assertNotifyField(t, payload, "event", notifyEventApprovalRequested)
	if box, _ := payload["box"].(string); box == "" {
		t.Fatalf("approval_requested missing box host: %s", prettyNotifyJSON(t, payload))
	}
	assertNotifyField(t, payload, "release", "aaa111bbb222")
	assertNotifyNestedField(t, payload, "why", "id", "abc123xy")
	assertNotifyNestedField(t, payload, "remediation", "command", "ship approve abc123xy")
	t.Logf("approval_requested payload:\n%s", prettyNotifyJSON(t, payload))
}

func TestNotifyApprovalRequestedForBoxTargetUsesBoxWebhook(t *testing.T) {
	setupPreviewHostTest(t)
	t.Setenv("SHIP_STATE_DIR", t.TempDir())
	sink := newNotifyTestSink(t)
	if err := store.Default().WriteBoxNotify(store.BoxNotifyFile{Version: store.CurrentVersion, URL: sink.URL}); err != nil {
		t.Fatal(err)
	}

	notifyApprovalRequested(store.ApprovalRequest{
		ID:        "abc123xy",
		Member:    store.ApprovalMember{Name: "alice", Role: store.MemberRoleShipper},
		Verb:      "box_mutation",
		Target:    store.ApprovalTarget{Summary: "box notify set"},
		ExpiresAt: "2026-07-08T10:15:00Z",
	}, time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC))

	payload := sink.singlePayload(t)
	assertNotifyField(t, payload, "event", notifyEventApprovalRequested)
	if app, _ := payload["app"].(string); app != "" {
		t.Fatalf("box approval app = %q, want empty: %s", app, prettyNotifyJSON(t, payload))
	}
	assertNotifyNestedField(t, payload, "why", "target.summary", "box notify set")
}

func TestNotifyFailureIsBoundedAndDoesNotLeakURL(t *testing.T) {
	oldTimeout := notifyTimeout
	oldStderr := notifyStderr
	defer func() {
		notifyTimeout = oldTimeout
		notifyStderr = oldStderr
	}()
	notifyTimeout = 50 * time.Millisecond
	var stderr bytes.Buffer
	notifyStderr = &stderr
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(250 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	start := time.Now()

	postNotify(server.URL+"/hook?token=notify-url-secret", notifyPayload{
		App: "api", Env: "Production main", Event: notifyEventDeployRecovered, TS: "2026-07-07T10:00:00Z",
	})

	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("notify call exceeded bounded timeout: %s", elapsed)
	}
	got := stderr.String()
	if strings.Count(strings.TrimSpace(got), "\n") > 0 {
		t.Fatalf("notify failure should log at most one line, got:\n%s", got)
	}
	if strings.Contains(got, "notify-url-secret") || strings.Contains(got, server.URL) {
		t.Fatalf("notify failure leaked URL/token:\n%s", got)
	}
}

type notifyTestSink struct {
	*httptest.Server
	bodies [][]byte
}

func newNotifyTestSink(t *testing.T) *notifyTestSink {
	t.Helper()
	sink := &notifyTestSink{}
	sink.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		if _, err := body.ReadFrom(r.Body); err != nil {
			t.Errorf("read notify body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		sink.bodies = append(sink.bodies, append([]byte(nil), body.Bytes()...))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sink.Close)
	return sink
}

func (s *notifyTestSink) singlePayload(t *testing.T) map[string]any {
	t.Helper()
	if len(s.bodies) != 1 {
		t.Fatalf("expected one notify payload, got %d", len(s.bodies))
	}
	var payload map[string]any
	if err := json.Unmarshal(s.bodies[0], &payload); err != nil {
		t.Fatalf("notify payload is not JSON: %v\nraw:\n%s", err, s.bodies[0])
	}
	return payload
}

func writeAppliedNotifyManifest(t *testing.T, app, env, notifyURL string) {
	t.Helper()
	root := identity.EnvRoot(app, env)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `name = "` + app + `"
box = "fake-vps"
notify = "` + notifyURL + `"

[processes]
web = { port = 3000 }
`
	if err := os.WriteFile(filepath.Join(root, "ship.toml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
}

func assertNotifyField(t *testing.T, payload map[string]any, field, want string) {
	t.Helper()
	if got, _ := payload[field].(string); got != want {
		t.Fatalf("payload[%s] = %q, want %q\npayload:\n%s", field, got, want, prettyNotifyJSON(t, payload))
	}
}

func assertNotifyNestedField(t *testing.T, payload map[string]any, parent, path, want string) {
	t.Helper()
	current, ok := payload[parent].(map[string]any)
	if !ok {
		t.Fatalf("payload[%s] is not object:\n%s", parent, prettyNotifyJSON(t, payload))
	}
	parts := strings.Split(path, ".")
	var value any = current
	for _, part := range parts {
		obj, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("payload[%s].%s is not object before %s:\n%s", parent, path, part, prettyNotifyJSON(t, payload))
		}
		value = obj[part]
	}
	if got, _ := value.(string); got != want {
		t.Fatalf("payload[%s].%s = %q, want %q\npayload:\n%s", parent, path, got, want, prettyNotifyJSON(t, payload))
	}
}

func assertNotifyDoesNotContain(t *testing.T, payload map[string]any, secret string) {
	t.Helper()
	raw := prettyNotifyJSON(t, payload)
	if strings.Contains(raw, secret) {
		t.Fatalf("notify payload leaked %q:\n%s", secret, raw)
	}
}

func prettyNotifyJSON(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

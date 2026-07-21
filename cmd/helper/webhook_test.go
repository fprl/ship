package helper

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
)

func TestWebhookDeployAbortedPayloadCarriesScrubbedJournalAndRemediation(t *testing.T) {
	setupPreviewHostTest(t)
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: productionEnvName})
	sink := newWebhookTestSink(t)
	now := time.Date(2026, 7, 7, 10, 1, 2, 0, time.UTC)
	entry := activationrecords.JournalEntry{
		SchemaVersion:    activationrecords.DeployJournalSchemaVersion,
		App:              "api",
		Env:              productionEnvName,
		Outcome:          "failed",
		StartedAt:        "2026-07-07T10:00:00Z",
		EndedAt:          "2026-07-07T10:00:01Z",
		PreviousRelease:  "aaa111bbb222",
		AttemptedRelease: "ccc333ddd444",
		FailingStep:      "release",
		StderrTail:       "release failed with [redacted]",
		Identity:         activationrecords.Identity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}

	webhookDeployAborted(sink.URL, &config.AppContext{ProductionBranch: "main"}, entry, now)

	payload := sink.singlePayload(t)
	assertWebhookField(t, payload, "event", webhookEventDeployAborted)
	assertWebhookField(t, payload, "env", "Production main")
	assertWebhookField(t, payload, "release", "ccc333ddd444")
	assertWebhookNestedField(t, payload, "why", "stderr_tail", "release failed with [redacted]")
	assertWebhookNestedField(t, payload, "remediation", "command", "ship")
	assertWebhookNestedField(t, payload, "remediation", "journal.outcome", "failed")
	assertWebhookDoesNotContain(t, payload, "webhook-secret-token")
	t.Logf("deploy_aborted payload:\n%s", prettyWebhookJSON(t, payload))
}

func TestWebhookDeployRecoveredPayloadCarriesPreviousFailure(t *testing.T) {
	setupPreviewHostTest(t)
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: productionEnvName})
	sink := newWebhookTestSink(t)
	now := time.Date(2026, 7, 7, 10, 2, 3, 0, time.UTC)
	previous := activationrecords.JournalEntry{
		SchemaVersion:    activationrecords.DeployJournalSchemaVersion,
		App:              "api",
		Env:              productionEnvName,
		Outcome:          "failed",
		StartedAt:        "2026-07-07T10:00:00Z",
		EndedAt:          "2026-07-07T10:00:01Z",
		PreviousRelease:  "aaa111bbb222",
		AttemptedRelease: "bad333bad333",
		FailingStep:      "probe",
		Probe:            &activationrecords.Probe{Status: 502, BodySnippet: "upstream refused"},
		Identity:         activationrecords.Identity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}
	current := activationrecords.JournalEntry{
		SchemaVersion:    activationrecords.DeployJournalSchemaVersion,
		App:              "api",
		Env:              productionEnvName,
		Outcome:          "deployed",
		StartedAt:        "2026-07-07T10:02:00Z",
		EndedAt:          "2026-07-07T10:02:02Z",
		PreviousRelease:  "aaa111bbb222",
		AttemptedRelease: "good444good4",
		Identity:         activationrecords.Identity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
	}

	webhookDeployRecovered(sink.URL, &config.AppContext{ProductionBranch: "main"}, previous, current, now)

	payload := sink.singlePayload(t)
	assertWebhookField(t, payload, "event", webhookEventDeployRecovered)
	assertWebhookNestedField(t, payload, "why", "previous_failure.outcome", "failed")
	assertWebhookNestedField(t, payload, "why", "current.outcome", "deployed")
	assertWebhookNestedField(t, payload, "remediation", "command", "ship status")
	assertWebhookNestedField(t, payload, "remediation", "previous_failure.attempted_release", "bad333bad333")
	t.Logf("deploy_recovered payload:\n%s", prettyWebhookJSON(t, payload))
}

func TestWebhookPreviewReapedPayloadCarriesBranchAndEnv(t *testing.T) {
	sink := newWebhookTestSink(t)
	now := time.Date(2026, 7, 7, 10, 3, 4, 0, time.UTC)
	expires := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	file := identity.EnvIdentity{
		Version: 1,
		App:     "api",
		Env:     "feature-payments-ab12",
		Preview: &identity.PreviewIdentity{
			Branch:     "feature/payments",
			LastShipAt: expires.Add(-previewTTL),
			ExpiresAt:  &expires,
		},
	}

	webhookPreviewReaped(sink.URL, file, "aaa111bbb222", now)

	payload := sink.singlePayload(t)
	assertWebhookField(t, payload, "event", webhookEventPreviewReaped)
	assertWebhookField(t, payload, "env", "Preview feature/payments")
	assertWebhookNestedField(t, payload, "why", "branch", "feature/payments")
	assertWebhookNestedField(t, payload, "why", "env", "Preview feature/payments")
	assertWebhookNestedField(t, payload, "remediation", "command", "git checkout feature/payments && ship")
	t.Logf("preview_reaped payload:\n%s", prettyWebhookJSON(t, payload))
}

func TestWebhookDoctorDegradedPayloadCarriesEvidenceAndRunnableRemediation(t *testing.T) {
	setupPreviewHostTest(t)
	setTestStateRoot(t, t.TempDir())
	sink := newWebhookTestSink(t)
	if err := store.Default().WriteBoxConfig(store.BoxConfigFile{Version: store.CurrentVersion, Values: map[string]string{"webhook.url": sink.URL, "box.address": "203.0.113.7"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 7, 10, 4, 5, 0, time.UTC)

	webhookDoctorDegraded("fake-vps", []store.DoctorCheck{{
		ID:          "reaper_timer",
		Status:      "degraded",
		Evidence:    "ship-preview-reaper.timer present, active=inactive, enabled=enabled",
		Remediation: "sudo systemctl enable ship-preview-reaper.timer && sudo systemctl start ship-preview-reaper.timer",
	}}, now)

	payload := sink.singlePayload(t)
	assertWebhookField(t, payload, "event", webhookEventDoctorDegraded)
	assertWebhookField(t, payload, "box", "fake-vps")
	assertWebhookNestedField(t, payload, "why", "evidence", "ship-preview-reaper.timer present, active=inactive, enabled=enabled")
	assertWebhookNestedField(t, payload, "remediation", "command", "sudo systemctl enable ship-preview-reaper.timer && sudo systemctl start ship-preview-reaper.timer")
	t.Logf("doctor_degraded payload:\n%s", prettyWebhookJSON(t, payload))
}

func TestWebhookApprovalRequestedPayloadCarriesLiteralApproveCommand(t *testing.T) {
	setupPreviewHostTest(t)
	setTestStateRoot(t, t.TempDir())
	setHelperBoxClientAddress(t, "203.0.113.7")
	writeIdentityForTest(t, identity.EnvIdentity{Version: 1, App: "api", Env: productionEnvName})
	sink := newWebhookTestSink(t)
	if err := store.Default().WriteBoxConfig(store.BoxConfigFile{Version: store.CurrentVersion, Values: map[string]string{"webhook.url": sink.URL, "box.address": "203.0.113.7"}}); err != nil {
		t.Fatal(err)
	}
	if err := appendDeployJournalEntry("api", productionEnvName, activationrecords.JournalEntry{
		Outcome:          "deployed",
		StartedAt:        "2026-07-08T09:00:00Z",
		EndedAt:          "2026-07-08T09:00:01Z",
		AttemptedRelease: "aaa111bbb222",
		Identity:         activationrecords.Identity{SSHKeyComment: "fake-vps-smoke", GitAuthor: "Smoke <smoke@example.com>"},
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
			Summary: "ship app=api env=production class=production release=aaa111",
		},
		CreatedAt: "2026-07-08T10:00:00Z",
		ExpiresAt: "2026-07-08T10:15:00Z",
	}

	webhookApprovalRequested(request, time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC))

	payload := sink.singlePayload(t)
	assertWebhookField(t, payload, "event", webhookEventApprovalRequested)
	assertWebhookField(t, payload, "box", "203.0.113.7")
	assertWebhookField(t, payload, "release", "aaa111bbb222")
	assertWebhookNestedField(t, payload, "why", "id", "abc123xy")
	assertWebhookNestedField(t, payload, "remediation", "command", "ship box approval grant abc123xy 203.0.113.7")
	t.Logf("approval_requested payload:\n%s", prettyWebhookJSON(t, payload))
}

func TestWebhookApprovalRequestedForBoxTargetUsesBoxWebhook(t *testing.T) {
	setupPreviewHostTest(t)
	setTestStateRoot(t, t.TempDir())
	setHelperBoxClientAddress(t, "203.0.113.7")
	sink := newWebhookTestSink(t)
	if err := store.Default().WriteBoxConfig(store.BoxConfigFile{Version: store.CurrentVersion, Values: map[string]string{"webhook.url": sink.URL, "box.address": "203.0.113.7"}}); err != nil {
		t.Fatal(err)
	}

	webhookApprovalRequested(store.ApprovalRequest{
		ID:        "abc123xy",
		Member:    store.ApprovalMember{Name: "alice", Role: store.MemberRoleShipper},
		Verb:      "box_mutation",
		Target:    store.ApprovalTarget{Summary: "box webhook set"},
		ExpiresAt: "2026-07-08T10:15:00Z",
	}, time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC))

	payload := sink.singlePayload(t)
	assertWebhookField(t, payload, "event", webhookEventApprovalRequested)
	if app, _ := payload["app"].(string); app != "" {
		t.Fatalf("box approval app = %q, want empty: %s", app, prettyWebhookJSON(t, payload))
	}
	assertWebhookNestedField(t, payload, "why", "target.summary", "box webhook set")
	assertWebhookField(t, payload, "box", "203.0.113.7")
	assertWebhookNestedField(t, payload, "remediation", "command", "ship box approval grant abc123xy 203.0.113.7")
}

func TestWebhookFailureIsBoundedAndDoesNotLeakURL(t *testing.T) {
	oldTimeout := webhookTimeout
	oldStderr := webhookStderr
	defer func() {
		webhookTimeout = oldTimeout
		webhookStderr = oldStderr
	}()
	webhookTimeout = 50 * time.Millisecond
	var stderr bytes.Buffer
	webhookStderr = &stderr
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(250 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	start := time.Now()

	postWebhook(server.URL+"/hook?token=webhook-url-secret", webhookPayload{
		App: "api", Env: "Production main", Event: webhookEventDeployRecovered, TS: "2026-07-07T10:00:00Z",
	})

	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("webhook call exceeded bounded timeout: %s", elapsed)
	}
	got := stderr.String()
	if strings.Count(strings.TrimSpace(got), "\n") > 0 {
		t.Fatalf("webhook failure should log at most one line, got:\n%s", got)
	}
	if strings.Contains(got, "webhook-url-secret") || strings.Contains(got, server.URL) {
		t.Fatalf("webhook failure leaked URL/token:\n%s", got)
	}
}

type webhookTestSink struct {
	*httptest.Server
	bodies [][]byte
}

func newWebhookTestSink(t *testing.T) *webhookTestSink {
	t.Helper()
	sink := &webhookTestSink{}
	sink.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		if _, err := body.ReadFrom(r.Body); err != nil {
			t.Errorf("read webhook body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		sink.bodies = append(sink.bodies, append([]byte(nil), body.Bytes()...))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sink.Close)
	return sink
}

func (s *webhookTestSink) singlePayload(t *testing.T) map[string]any {
	t.Helper()
	if len(s.bodies) != 1 {
		t.Fatalf("expected one webhook payload, got %d", len(s.bodies))
	}
	var payload map[string]any
	if err := json.Unmarshal(s.bodies[0], &payload); err != nil {
		t.Fatalf("webhook payload is not JSON: %v\nraw:\n%s", err, s.bodies[0])
	}
	return payload
}

func assertWebhookField(t *testing.T, payload map[string]any, field, want string) {
	t.Helper()
	if got, _ := payload[field].(string); got != want {
		t.Fatalf("payload[%s] = %q, want %q\npayload:\n%s", field, got, want, prettyWebhookJSON(t, payload))
	}
}

func assertWebhookNestedField(t *testing.T, payload map[string]any, parent, path, want string) {
	t.Helper()
	current, ok := payload[parent].(map[string]any)
	if !ok {
		t.Fatalf("payload[%s] is not object:\n%s", parent, prettyWebhookJSON(t, payload))
	}
	parts := strings.Split(path, ".")
	var value any = current
	for _, part := range parts {
		obj, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("payload[%s].%s is not object before %s:\n%s", parent, path, part, prettyWebhookJSON(t, payload))
		}
		value = obj[part]
	}
	if got, _ := value.(string); got != want {
		t.Fatalf("payload[%s].%s = %q, want %q\npayload:\n%s", parent, path, got, want, prettyWebhookJSON(t, payload))
	}
}

func assertWebhookDoesNotContain(t *testing.T, payload map[string]any, secret string) {
	t.Helper()
	raw := prettyWebhookJSON(t, payload)
	if strings.Contains(raw, secret) {
		t.Fatalf("webhook payload leaked %q:\n%s", secret, raw)
	}
}

func prettyWebhookJSON(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

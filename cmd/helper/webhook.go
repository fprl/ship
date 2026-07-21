package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

const (
	webhookEventDeployAborted     = "deploy_aborted"
	webhookEventDeployRecovered   = "deploy_recovered"
	webhookEventPreviewReaped     = "preview_reaped"
	webhookEventDoctorDegraded    = "doctor_degraded"
	webhookEventApprovalRequested = "approval_requested"
)

var (
	webhookTimeout           = 2 * time.Second
	webhookStderr  io.Writer = os.Stderr
)

type webhookPayload struct {
	Box         string `json:"box,omitempty"`
	App         string `json:"app"`
	Env         string `json:"env"`
	Event       string `json:"event"`
	Release     string `json:"release"`
	Summary     string `json:"summary"`
	Why         any    `json:"why"`
	Remediation any    `json:"remediation"`
	TS          string `json:"ts"`
}

type deployRecoveryWhy struct {
	PreviousFailure deployJournalEntry `json:"previous_failure"`
	Current         deployJournalEntry `json:"current"`
}

type deployWebhookRemediation struct {
	Command         string              `json:"command"`
	Journal         deployJournalEntry  `json:"journal"`
	PreviousFailure *deployJournalEntry `json:"previous_failure,omitempty"`
}

type reapWebhookWhy struct {
	Branch    string `json:"branch"`
	Env       string `json:"env"`
	ExpiredAt string `json:"expired_at,omitempty"`
}

type reapWebhookRemediation struct {
	Command string `json:"command"`
	Branch  string `json:"branch"`
	Env     string `json:"env"`
}

type doctorWebhookRemediation struct {
	Command string            `json:"command"`
	Check   store.DoctorCheck `json:"check"`
}

type approvalWebhookWhy struct {
	ID      string               `json:"id"`
	Member  store.ApprovalMember `json:"member"`
	Verb    string               `json:"verb"`
	Target  store.ApprovalTarget `json:"target"`
	Expires string               `json:"expires"`
}

type approvalWebhookRemediation struct {
	Command string                `json:"command"`
	Request store.ApprovalRequest `json:"request"`
}

func webhookDeployAborted(url string, ctx *config.AppContext, entry deployJournalEntry, now time.Time) {
	if strings.TrimSpace(url) == "" {
		return
	}
	payload := webhookPayload{
		App:     entry.App,
		Env:     webhookEnvLabel(entry.App, entry.Env, ctx),
		Event:   webhookEventDeployAborted,
		Release: entry.AttemptedRelease,
		Summary: fmt.Sprintf("Deploy aborted for %s at release %s.", webhookEnvLabel(entry.App, entry.Env, ctx), dashWebhook(entry.AttemptedRelease)),
		Why:     entry,
		Remediation: deployWebhookRemediation{
			Command: "ship",
			Journal: entry,
		},
		TS: now.UTC().Format(time.RFC3339Nano),
	}
	postWebhook(url, payload)
}

func webhookDeployRecovered(url string, ctx *config.AppContext, previousFailure, current deployJournalEntry, now time.Time) {
	if strings.TrimSpace(url) == "" {
		return
	}
	previous := previousFailure
	payload := webhookPayload{
		App:     current.App,
		Env:     webhookEnvLabel(current.App, current.Env, ctx),
		Event:   webhookEventDeployRecovered,
		Release: current.AttemptedRelease,
		Summary: fmt.Sprintf("Deploy recovered for %s at release %s.", webhookEnvLabel(current.App, current.Env, ctx), dashWebhook(current.AttemptedRelease)),
		Why: deployRecoveryWhy{
			PreviousFailure: previousFailure,
			Current:         current,
		},
		Remediation: deployWebhookRemediation{
			Command:         "ship status",
			Journal:         current,
			PreviousFailure: &previous,
		},
		TS: now.UTC().Format(time.RFC3339Nano),
	}
	postWebhook(url, payload)
}

func webhookPreviewReaped(url string, file identity.EnvIdentity, release string, now time.Time) {
	if strings.TrimSpace(url) == "" || file.Preview == nil {
		return
	}
	envLabel := "Preview " + file.Preview.Branch
	expiredAt := ""
	if file.Preview.ExpiresAt != nil {
		expiredAt = file.Preview.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	payload := webhookPayload{
		App:     file.App,
		Env:     envLabel,
		Event:   webhookEventPreviewReaped,
		Release: release,
		Summary: fmt.Sprintf("Preview %s was reaped.", file.Preview.Branch),
		Why: reapWebhookWhy{
			Branch:    file.Preview.Branch,
			Env:       envLabel,
			ExpiredAt: expiredAt,
		},
		Remediation: reapWebhookRemediation{
			Command: "git checkout " + utils.ShellEscape(file.Preview.Branch) + " && ship",
			Branch:  file.Preview.Branch,
			Env:     envLabel,
		},
		TS: now.UTC().Format(time.RFC3339Nano),
	}
	postWebhook(url, payload)
}

func webhookDoctorDegraded(box string, checks []store.DoctorCheck, now time.Time) {
	if len(checks) == 0 {
		return
	}
	url := boxWebhookURL()
	for _, check := range checks {
		payload := webhookPayload{
			Box:     box,
			Event:   webhookEventDoctorDegraded,
			Summary: fmt.Sprintf("Doctor check %s is %s for box %s.", check.ID, check.Status, dashWebhook(box)),
			Why:     check,
			Remediation: doctorWebhookRemediation{
				Command: check.Remediation,
				Check:   check,
			},
			TS: now.UTC().Format(time.RFC3339Nano),
		}
		postWebhook(url, payload)
	}
}

func webhookApprovalRequested(request store.ApprovalRequest, now time.Time) {
	command := approvalCommand(request.ID)
	box := boxClientAddress()
	app := strings.TrimSpace(request.Target.App)
	env := strings.TrimSpace(request.Target.Env)
	var ctx *config.AppContext
	if app != "" && env != "" {
		loaded, _, err := resolveActiveContext(app, env)
		if err == nil {
			ctx = loaded
		}
	}
	payload := webhookPayload{
		Box:     box,
		App:     app,
		Env:     webhookEnvLabel(app, env, ctx),
		Event:   webhookEventApprovalRequested,
		Release: latestSuccessfulRelease(app, env),
		Summary: fmt.Sprintf("%s requested approval for %s.", request.Member.Name, request.Target.Summary),
		Why: approvalWebhookWhy{
			ID:      request.ID,
			Member:  request.Member,
			Verb:    request.Verb,
			Target:  request.Target,
			Expires: request.ExpiresAt,
		},
		Remediation: approvalWebhookRemediation{
			Command: command,
			Request: request,
		},
		TS: now.UTC().Format(time.RFC3339Nano),
	}
	postWebhook(boxWebhookURL(), payload)
}

func boxWebhookURL() string {
	url, err := boxConfigValueFor("webhook.url")
	if err != nil {
		fmt.Fprintf(webhookStderr, "warning: failed to read box-config.json for webhook: %v\n", err)
		return ""
	}
	return url
}

func boxClientAddress() string {
	address, err := boxConfigValueFor("box.address")
	if err != nil || strings.TrimSpace(address) == "" {
		return "<box>"
	}
	return strings.TrimSpace(address)
}

func webhookEnvLabel(app, env string, ctx *config.AppContext) string {
	if strings.TrimSpace(app) == "" || strings.TrimSpace(env) == "" {
		return ""
	}
	if file, err := readEnvIdentity(app, env); err == nil && file.Preview != nil {
		return "Preview " + file.Preview.Branch
	}
	branch := "main"
	if ctx != nil && ctx.ProductionBranch != "" {
		branch = ctx.ProductionBranch
	}
	if env == productionEnvName {
		return "Production " + branch
	}
	return "Preview " + env
}

func latestSuccessfulRelease(app, env string) string {
	entry, torn, err := readLatestSuccessfulDeployJournalEntryWithStatus(app, env)
	if torn {
		warnTornDeployJournal(identity.DeployJournalFile(app, env))
	}
	if err != nil {
		return ""
	}
	return entry.AttemptedRelease
}

func postWebhook(url string, payload webhookPayload) {
	if strings.TrimSpace(url) == "" {
		return
	}
	if err := doPostWebhook(url, payload); err != nil {
		fmt.Fprintf(webhookStderr, "webhook delivery failed: %s\n", err)
	}
}

func doPostWebhook(url string, payload webhookPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("payload encode failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errors.New("request setup failed")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: webhookTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func dashWebhook(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

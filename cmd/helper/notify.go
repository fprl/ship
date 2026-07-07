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

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/store"
	"github.com/fprl/simple-vps/internal/utils"
)

const (
	notifyEventDeployAborted   = "deploy_aborted"
	notifyEventDeployRecovered = "deploy_recovered"
	notifyEventPreviewReaped   = "preview_reaped"
	notifyEventDoctorDegraded  = "doctor_degraded"
)

var (
	notifyTimeout           = 2 * time.Second
	notifyStderr  io.Writer = os.Stderr
)

type notifyPayload struct {
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

type deployNotifyRemediation struct {
	Command         string              `json:"command"`
	Journal         deployJournalEntry  `json:"journal"`
	PreviousFailure *deployJournalEntry `json:"previous_failure,omitempty"`
}

type reapNotifyWhy struct {
	Branch    string `json:"branch"`
	Env       string `json:"env"`
	ExpiredAt string `json:"expired_at,omitempty"`
}

type reapNotifyRemediation struct {
	Command string `json:"command"`
	Branch  string `json:"branch"`
	Env     string `json:"env"`
}

type doctorNotifyRemediation struct {
	Command string            `json:"command"`
	Check   store.DoctorCheck `json:"check"`
}

type appliedNotifyTarget struct {
	App     string
	Env     string
	URL     string
	Label   string
	Release string
}

func notifyDeployAborted(url string, ctx *config.AppContext, entry deployJournalEntry, now time.Time) {
	if strings.TrimSpace(url) == "" {
		return
	}
	payload := notifyPayload{
		App:     entry.App,
		Env:     notifyEnvLabel(entry.App, entry.Env, ctx),
		Event:   notifyEventDeployAborted,
		Release: entry.AttemptedRelease,
		Summary: fmt.Sprintf("Deploy aborted for %s at release %s.", notifyEnvLabel(entry.App, entry.Env, ctx), dashNotify(entry.AttemptedRelease)),
		Why:     entry,
		Remediation: deployNotifyRemediation{
			Command: "ship",
			Journal: entry,
		},
		TS: now.UTC().Format(time.RFC3339Nano),
	}
	postNotify(url, payload)
}

func notifyDeployRecovered(url string, ctx *config.AppContext, previousFailure, current deployJournalEntry, now time.Time) {
	if strings.TrimSpace(url) == "" {
		return
	}
	previous := previousFailure
	payload := notifyPayload{
		App:     current.App,
		Env:     notifyEnvLabel(current.App, current.Env, ctx),
		Event:   notifyEventDeployRecovered,
		Release: current.AttemptedRelease,
		Summary: fmt.Sprintf("Deploy recovered for %s at release %s.", notifyEnvLabel(current.App, current.Env, ctx), dashNotify(current.AttemptedRelease)),
		Why: deployRecoveryWhy{
			PreviousFailure: previousFailure,
			Current:         current,
		},
		Remediation: deployNotifyRemediation{
			Command:         "ship status",
			Journal:         current,
			PreviousFailure: &previous,
		},
		TS: now.UTC().Format(time.RFC3339Nano),
	}
	postNotify(url, payload)
}

func notifyPreviewReaped(url string, file identity.EnvIdentity, release string, now time.Time) {
	if strings.TrimSpace(url) == "" || file.Preview == nil {
		return
	}
	envLabel := "Preview " + file.Preview.Branch
	expiredAt := ""
	if file.Preview.ExpiresAt != nil {
		expiredAt = file.Preview.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	payload := notifyPayload{
		App:     file.App,
		Env:     envLabel,
		Event:   notifyEventPreviewReaped,
		Release: release,
		Summary: fmt.Sprintf("Preview %s was reaped.", file.Preview.Branch),
		Why: reapNotifyWhy{
			Branch:    file.Preview.Branch,
			Env:       envLabel,
			ExpiredAt: expiredAt,
		},
		Remediation: reapNotifyRemediation{
			Command: "git checkout " + utils.ShellEscape(file.Preview.Branch) + " && ship",
			Branch:  file.Preview.Branch,
			Env:     envLabel,
		},
		TS: now.UTC().Format(time.RFC3339Nano),
	}
	postNotify(url, payload)
}

func notifyDoctorDegraded(checks []store.DoctorCheck, now time.Time) {
	if len(checks) == 0 {
		return
	}
	targets := appliedNotifyTargets()
	for _, target := range targets {
		for _, check := range checks {
			payload := notifyPayload{
				App:     target.App,
				Env:     target.Label,
				Event:   notifyEventDoctorDegraded,
				Release: target.Release,
				Summary: fmt.Sprintf("Doctor check %s is %s for %s.", check.ID, check.Status, target.Label),
				Why:     check,
				Remediation: doctorNotifyRemediation{
					Command: check.Remediation,
					Check:   check,
				},
				TS: now.UTC().Format(time.RFC3339Nano),
			}
			postNotify(target.URL, payload)
		}
	}
}

func appliedNotifyTargets() []appliedNotifyTarget {
	apps, err := identityAppEnvs()
	if err != nil {
		return nil
	}
	targets := make([]appliedNotifyTarget, 0, len(apps))
	for _, app := range apps {
		ctx, cleanup, err := loadAppliedAppContext(app.App, app.Env)
		if err != nil {
			continue
		}
		url := strings.TrimSpace(ctx.Notify)
		label := notifyEnvLabel(app.App, app.Env, ctx)
		cleanup()
		if url == "" {
			continue
		}
		targets = append(targets, appliedNotifyTarget{
			App:     app.App,
			Env:     app.Env,
			URL:     url,
			Label:   label,
			Release: latestSuccessfulRelease(app.App, app.Env),
		})
	}
	return targets
}

func notifyEnvLabel(app, env string, ctx *config.AppContext) string {
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
	entry, err := readLatestSuccessfulDeployJournalEntry(app, env)
	if err != nil {
		return ""
	}
	return entry.AttemptedRelease
}

func isAbortedJournalOutcome(outcome string) bool {
	return strings.HasPrefix(outcome, "aborted_")
}

func postNotify(url string, payload notifyPayload) {
	if strings.TrimSpace(url) == "" {
		return
	}
	if err := doPostNotify(url, payload); err != nil {
		fmt.Fprintf(notifyStderr, "notify webhook failed: %s\n", err)
	}
}

func doPostNotify(url string, payload notifyPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("payload encode failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errors.New("request setup failed")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: notifyTimeout}
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

func dashNotify(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

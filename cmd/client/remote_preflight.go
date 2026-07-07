package client

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
)

func ensureRemoteEnvReadyForDeploy(runner sshRunner, ctx *config.AppContext) error {
	if err := deployHostPreflight(runner, ctx); err != nil {
		return err
	}
	report, err := fetchRemotePreflightReport(runner, ctx)
	if err != nil {
		return err
	}
	if report.Healthy {
		return nil
	}
	if !remotePreflightOnlyNeedsEnvPreparation(report) {
		return remotePreflightError(report, false)
	}
	if _, err := runSSHRequired(runner, ctx.Server, serverAppSetupEnvCommand(ctx.AppName, ctx.EnvName), "failed to prepare app environment"); err != nil {
		return err
	}
	report, err = fetchRemotePreflightReport(runner, ctx)
	if err != nil {
		return deployPreflightAfterPreparationError(preflightErrorDetail(err))
	}
	if !report.Healthy {
		return remotePreflightError(report, true)
	}
	return nil
}

func deployHostPreflight(runner sshRunner, ctx *config.AppContext) error {
	if stdout, stderr, code, err := runner.RunSSH(ctx.Server, "true"); err != nil || code != 0 {
		return errcat.New(errcat.CodeSSHUnreachable, errcat.Fields{
			"target": ctx.Server,
			"detail": commandDetail(stdout, stderr, "remote SSH command failed"),
		})
	}
	if stdout, stderr, code, err := runner.RunSSH(ctx.Server, "test -x /usr/local/bin/ship"); err != nil || code != 0 {
		return errcat.New(errcat.CodeBoxNotInitialized, errcat.Fields{"target": ctx.Server, "detail": commandDetail(stdout, stderr, "missing ship server API")})
	}
	if _, _, code, err := runner.RunSSH(ctx.Server, "command -v rsync >/dev/null"); err != nil || code != 0 {
		return errcat.New(errcat.CodeBoxMissingTool, errcat.Fields{
			"target": ctx.Server,
			"tool":   "rsync",
		})
	}
	return nil
}

func fetchRemotePreflightReport(runner sshRunner, ctx *config.AppContext) (remotePreflightReport, error) {
	stdout, stderr, code, err := runner.RunSSH(ctx.Server, serverAppPreflightJSONCommand(ctx.AppName, ctx.EnvName, secretRefKeys(ctx.SecretRefs)))
	if report, ok := parseRemotePreflightReport(stdout); ok {
		if code != 0 && report.Healthy {
			return remotePreflightReport{}, deployPreflightError("preflight command failed but reported healthy")
		}
		return report, nil
	}
	if err == nil && code == 0 {
		return remotePreflightReport{}, deployPreflightError("invalid preflight response from host")
	}
	remote := extractRemoteError(stdout, stderr, "no error detail")
	if remote.Coded != nil {
		return remotePreflightReport{}, remote.Coded
	}
	return remotePreflightReport{}, deployPreflightError(remote.Detail)
}

func commandDetail(stdout, stderr, fallback string) string {
	return extractRemoteError(stdout, stderr, fallback).Detail
}

type remotePreflightReport struct {
	App      string                 `json:"app"`
	Env      string                 `json:"env"`
	Healthy  bool                   `json:"healthy"`
	Issues   []remotePreflightIssue `json:"issues"`
	Findings []string               `json:"findings"`
}

type remotePreflightIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const remotePreflightEnvMissing = "env_missing"

func parseRemotePreflightReport(out string) (remotePreflightReport, bool) {
	var report remotePreflightReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &report); err != nil {
		return remotePreflightReport{}, false
	}
	if report.App == "" || report.Env == "" {
		return remotePreflightReport{}, false
	}
	return report, true
}

func renderRemotePreflightFindings(report remotePreflightReport) string {
	findings := remotePreflightFindingMessages(report)
	if len(findings) == 0 {
		return fmt.Sprintf("preflight for %s (%s) failed without findings", report.App, report.Env)
	}
	var lines []string
	for _, finding := range findings {
		lines = append(lines, "  - "+finding)
	}
	return strings.Join(lines, "\n")
}

func remotePreflightError(report remotePreflightReport, afterPreparation bool) error {
	if err := codedRemotePreflightIssue(report); err != nil {
		return err
	}
	if afterPreparation {
		return deployPreflightAfterPreparationError(renderRemotePreflightFindings(report))
	}
	return deployPreflightError(renderRemotePreflightFindings(report))
}

func codedRemotePreflightIssue(report remotePreflightReport) error {
	for _, issue := range report.Issues {
		if issue.Code != string(errcat.CodeSecretMissing) {
			continue
		}
		cause, remediation := splitPreflightIssueRemediation(issue.Message)
		err := errcat.New(errcat.CodeSecretMissing, errcat.Fields{
			"secret":  "required secret",
			"scope":   "target environment",
			"command": remediation,
		})
		return errcat.WithCause(err, cause)
	}
	return nil
}

func splitPreflightIssueRemediation(message string) (string, string) {
	message = strings.TrimSpace(message)
	if lines := strings.Split(message, "\n"); len(lines) >= 3 && strings.HasPrefix(lines[len(lines)-1], "next: ") {
		return strings.TrimSpace(lines[1]), strings.TrimSpace(strings.TrimPrefix(lines[len(lines)-1], "next: "))
	}
	if cause, rest, ok := strings.Cut(message, "; run `"); ok {
		return strings.TrimSpace(cause), strings.TrimSuffix(strings.TrimSpace(rest), "`")
	}
	if message == "" {
		message = "missing required secret"
	}
	return message, "ship secret set KEY"
}

func remotePreflightFindingMessages(report remotePreflightReport) []string {
	if len(report.Findings) > 0 {
		return report.Findings
	}
	messages := make([]string, 0, len(report.Issues))
	for _, issue := range report.Issues {
		messages = append(messages, issue.Message)
	}
	return messages
}

func remotePreflightOnlyNeedsEnvPreparation(report remotePreflightReport) bool {
	if len(report.Issues) == 0 {
		return false
	}
	for _, issue := range report.Issues {
		if issue.Code != remotePreflightEnvMissing {
			return false
		}
	}
	return true
}

func deployPreflightError(detail string) error {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "no error detail"
	}
	return errcat.New(errcat.CodeRemotePreflightFailed, errcat.Fields{
		"detail": detail + "\nNo remote files, routes, or containers were changed.",
	})
}

func deployPreflightAfterPreparationError(detail string) error {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "no error detail"
	}
	return errcat.New(errcat.CodeRemotePreflightAfterPrepareFailed, errcat.Fields{
		"detail": detail + "\nNo release was uploaded, built, or routed.",
	})
}

func preflightErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	coded, _ := errcat.As(err)
	detail := coded.Cause()
	detail = strings.TrimSuffix(detail, "No remote files, routes, or containers were changed.")
	detail = strings.TrimSuffix(detail, "No release was uploaded, built, or routed.")
	return strings.TrimSpace(detail)
}

func secretRefKeys(refs map[string]string) []string {
	seen := map[string]bool{}
	var keys []string
	for _, key := range refs {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

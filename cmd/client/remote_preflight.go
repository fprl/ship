package client

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/config"
)

func deployRemotePreflight(runner sshRunner, ctx *config.AppContext) error {
	if err := deployHostPreflight(runner, ctx); err != nil {
		return err
	}
	report, err := fetchRemotePreflightReport(runner, ctx)
	if err != nil {
		return err
	}
	if !report.Healthy {
		return deployPreflightError(renderRemotePreflightFindings(report))
	}
	return nil
}

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
		return deployPreflightError(renderRemotePreflightFindings(report))
	}
	if _, err := runSSHRequired(runner, ctx.Server, serverAppSetupEnvCommand(ctx.AppName, ctx.EnvName), "failed to prepare app environment"); err != nil {
		return err
	}
	report, err = fetchRemotePreflightReport(runner, ctx)
	if err != nil {
		return deployPreflightAfterPreparationError(preflightErrorDetail(err))
	}
	if !report.Healthy {
		return deployPreflightAfterPreparationError(renderRemotePreflightFindings(report))
	}
	return nil
}

func deployHostPreflight(runner sshRunner, ctx *config.AppContext) error {
	if _, err := runSSHRequired(runner, ctx.Server, "true", fmt.Sprintf("SSH failed for %s", ctx.Server)); err != nil {
		return deployPreflightError(err.Error())
	}
	if _, err := runSSHRequired(runner, ctx.Server, "test -x /usr/local/bin/ship", "missing ship server API at /usr/local/bin/ship; run `ship box init "+ctx.Server+"` for this VPS"); err != nil {
		return deployPreflightError(err.Error())
	}
	if _, err := runSSHRequired(runner, ctx.Server, "command -v rsync >/dev/null", "missing required server tool: rsync; rerun `ship box init "+ctx.Server+"`"); err != nil {
		return deployPreflightError(err.Error())
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
	detail := strings.TrimSpace(stdout)
	if detail == "" {
		detail = strings.TrimSpace(stderr)
	}
	if detail == "" {
		detail = "no error detail"
	}
	return remotePreflightReport{}, deployPreflightError(detail)
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
		if report.Healthy {
			return fmt.Sprintf("preflight for %s (%s) returned no findings", report.App, report.Env)
		}
		return fmt.Sprintf("preflight for %s (%s) failed without findings", report.App, report.Env)
	}
	var lines []string
	for _, finding := range findings {
		lines = append(lines, "  - "+finding)
	}
	return strings.Join(lines, "\n")
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
	return fmt.Errorf("deploy preflight failed before upload/build/mutation:\n%s\nNo remote files, routes, or containers were changed.", detail)
}

func deployPreflightAfterPreparationError(detail string) error {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "no error detail"
	}
	return fmt.Errorf("deploy preflight failed after preparing the app environment:\n%s\nNo release was uploaded, built, or routed.", detail)
}

func preflightErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	detail := err.Error()
	prefix := "deploy preflight failed before upload/build/mutation:"
	if strings.HasPrefix(detail, prefix) {
		detail = strings.TrimSpace(strings.TrimPrefix(detail, prefix))
	}
	suffix := "No remote files, routes, or containers were changed."
	detail = strings.TrimSpace(strings.TrimSuffix(detail, suffix))
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

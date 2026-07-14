package client

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
)

type appListJSON struct {
	Apps []appListAppJSON `json:"apps"`
}

type appListAppJSON struct {
	App  string           `json:"app"`
	Envs []appListEnvJSON `json:"envs"`
}

type appListEnvJSON struct {
	Class          string              `json:"class"`
	Branch         string              `json:"branch"`
	URL            string              `json:"url"`
	CapabilityURL  string              `json:"capability_url,omitempty"`
	Env            string              `json:"env"`
	CurrentRelease string              `json:"current_release"`
	Health         string              `json:"health"`
	AgeSeconds     int64               `json:"age_seconds"`
	ExpiresAt      string              `json:"expires_at"`
	Pinned         bool                `json:"pinned"`
	Dirty          bool                `json:"dirty"`
	ShippedBy      *deployIdentityJSON `json:"shipped_by,omitempty"`
	Processes      []processJSON       `json:"processes"`
	Static         *staticJSON         `json:"static,omitempty"`
}

type processJSON struct {
	Process    string `json:"process"`
	Container  string `json:"container"`
	State      string `json:"state"`
	Image      string `json:"image,omitempty"`
	Release    string `json:"release,omitempty"`
	Dirty      bool   `json:"dirty,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	Status     string `json:"status,omitempty"`
}

type staticJSON struct {
	Release    string   `json:"release"`
	Routes     []string `json:"routes"`
	Dirty      bool     `json:"dirty,omitempty"`
	BaseCommit string   `json:"base_commit,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
}

type statusPayload struct {
	App  string          `json:"app"`
	Envs []statusEnvJSON `json:"envs"`
}

type statusEnvJSON struct {
	Class         string              `json:"class"`
	Branch        string              `json:"branch"`
	URL           string              `json:"url"`
	CapabilityURL string              `json:"capability_url,omitempty"`
	Env           string              `json:"env"`
	Release       string              `json:"release,omitempty"`
	Health        string              `json:"health"`
	AgeSeconds    int64               `json:"ageSeconds,omitempty"`
	ExpiresAt     string              `json:"expiresAt,omitempty"`
	Pinned        bool                `json:"pinned,omitempty"`
	Dirty         bool                `json:"dirty,omitempty"`
	ShippedBy     *deployIdentityJSON `json:"shipped_by,omitempty"`
	Processes     []processJSON       `json:"processes"`
}

func CmdStatus(root string, jsonFlag bool) {
	ctx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		utils.DieError(err, 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, ctx.Server, serverAppListCommand(true), "status failed", "ship status")
	payload, err := statusFromAppList(ctx, out)
	if err != nil {
		utils.DieError(err, 1)
	}
	if jsonFlag {
		buf, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(buf))
		return
	}
	pending, err := fetchPendingApprovalCount(runner, ctx.Server)
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Print(renderStatusSummaryWithApprovals(payload, pending))
}

type whyJournalEntry struct {
	SchemaVersion    int                `json:"schema_version"`
	App              string             `json:"app"`
	Env              string             `json:"env"`
	Outcome          string             `json:"outcome"`
	StartedAt        string             `json:"started_at"`
	EndedAt          string             `json:"ended_at"`
	PreviousRelease  string             `json:"previous_release"`
	AttemptedRelease string             `json:"attempted_release"`
	FailingStep      string             `json:"failing_step"`
	StderrTail       string             `json:"stderr_tail"`
	Identity         deployIdentityJSON `json:"identity"`
	Probe            *whyJournalProbe   `json:"probe"`
}

type whyJournalProbe struct {
	Status      int    `json:"status"`
	BodySnippet string `json:"body_snippet"`
}

func CmdWhy(root, branch string, jsonFlag bool) {
	read, err := currentReadContextForBranch(root, "why", branch)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer read.Runner.Close()

	out, err := runSSHDetail(read.Runner, read.AppContext.Server, serverAppWhyCommand(read.AppContext.AppName, read.EnvName))
	if err != nil {
		utils.DieError(err, 1)
	}
	if jsonFlag {
		fmt.Print(out)
		return
	}
	var entry whyJournalEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &entry); err != nil {
		utils.DieError(operationError(fmt.Sprintf("why failed: invalid journal JSON: %v", err), "ship why"), 1)
	}
	fmt.Print(renderWhy(entry, read))
}

func renderWhy(entry whyJournalEntry, read readContext) string {
	kind, branch := readSurface(read)
	when := entry.EndedAt
	if when == "" {
		when = entry.StartedAt
	}
	var b strings.Builder
	switch entry.Outcome {
	case "deployed":
		fmt.Fprintf(&b, "Deploy succeeded for %s %s at %s.\n", kind, branch, when)
		fmt.Fprintf(&b, "release: %s", dashIfEmpty(entry.AttemptedRelease))
		if entry.PreviousRelease != "" {
			fmt.Fprintf(&b, " (previous %s)", entry.PreviousRelease)
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "traffic: release %s is live.\n", dashIfEmpty(entry.AttemptedRelease))
		fmt.Fprintf(&b, "shipped by: %s (ssh key: %s)\n", entry.Identity.GitAuthor, entry.Identity.SSHKeyComment)
		b.WriteString("next: ship status\n")
	case "rolled_back":
		fmt.Fprintf(&b, "Rollback completed for %s %s at %s.\n", kind, branch, when)
		fmt.Fprintf(&b, "release: %s (from %s)\n", dashIfEmpty(entry.AttemptedRelease), dashIfEmpty(entry.PreviousRelease))
		fmt.Fprintf(&b, "traffic: release %s is live.\n", dashIfEmpty(entry.AttemptedRelease))
		fmt.Fprintf(&b, "shipped by: %s (ssh key: %s)\n", entry.Identity.GitAuthor, entry.Identity.SSHKeyComment)
		b.WriteString("next: ship status\n")
	default:
		fmt.Fprintf(&b, "Deploy aborted for %s %s at %s.\n", kind, branch, when)
		fmt.Fprintf(&b, "attempted release: %s\n", dashIfEmpty(entry.AttemptedRelease))
		fmt.Fprintf(&b, "previous release: %s\n", dashIfEmpty(entry.PreviousRelease))
		fmt.Fprintf(&b, "failing step: %s\n", dashIfEmpty(entry.FailingStep))
		fmt.Fprintf(&b, "probable cause: %s\n", probableCause(entry))
		if entry.StderrTail != "" {
			b.WriteString("stderr tail:\n")
			b.WriteString(entry.StderrTail)
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "traffic: %s\n", trafficImpact(entry))
		fmt.Fprintf(&b, "shipped by: %s (ssh key: %s)\n", entry.Identity.GitAuthor, entry.Identity.SSHKeyComment)
		fmt.Fprintf(&b, "next: %s\n", whyRemediation(entry))
	}
	return b.String()
}

func whyRemediation(entry whyJournalEntry) string {
	switch entry.Outcome {
	case "aborted_release":
		if entry.FailingStep == "release" {
			return "fix the release command in ship.toml, then ship"
		}
		return "ship"
	case "aborted_probe":
		return "fix the process port or probe path in ship.toml, then ship"
	default:
		return "ship"
	}
}

func probableCause(entry whyJournalEntry) string {
	switch entry.Outcome {
	case "aborted_build":
		return "image build failed."
	case "aborted_probe":
		if entry.Probe != nil && entry.Probe.Status != 0 {
			if entry.Probe.BodySnippet != "" {
				return fmt.Sprintf("probe returned HTTP %d with body: %s", entry.Probe.Status, singleLineSnippet(entry.Probe.BodySnippet))
			}
			return fmt.Sprintf("probe returned HTTP %d.", entry.Probe.Status)
		}
		return "the new container did not pass its health probe."
	case "aborted_release":
		if entry.FailingStep == "release" {
			return "release command exited non-zero before traffic switched."
		}
		return "deploy failed before traffic switched."
	default:
		return "latest journal entry did not record a known failure pattern."
	}
}

func trafficImpact(entry whyJournalEntry) string {
	if entry.PreviousRelease == "" {
		return "no previous release was serving, so no old traffic was available."
	}
	if entry.Outcome == "aborted_probe" {
		return fmt.Sprintf("old release %s kept serving; failed probes never receive traffic with the current engine.", entry.PreviousRelease)
	}
	return fmt.Sprintf("old release %s kept serving; no traffic was switched.", entry.PreviousRelease)
}

func singleLineSnippet(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func statusFromAppList(ctx *config.AppContext, raw string) (statusPayload, error) {
	var list appListJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &list); err != nil {
		return statusPayload{}, operationError(fmt.Sprintf("status failed: invalid app list JSON: %v", err), "ship status")
	}
	payload := statusPayload{App: ctx.AppName}
	for _, app := range list.Apps {
		if app.App != ctx.AppName {
			continue
		}
		for _, env := range app.Envs {
			payload.Envs = append(payload.Envs, statusEnvFromAppListItem(ctx, env))
		}
	}
	sort.Slice(payload.Envs, func(i, j int) bool {
		if payload.Envs[i].Class != payload.Envs[j].Class {
			return payload.Envs[i].Class == "production"
		}
		return payload.Envs[i].Branch < payload.Envs[j].Branch
	})
	return payload, nil
}

func statusEnvFromAppListItem(ctx *config.AppContext, item appListEnvJSON) statusEnvJSON {
	class := item.Class
	if class == "" {
		class = "preview"
	}
	branch := item.Branch
	if branch == "" && class == "production" {
		branch = ctx.ProductionBranch
	}
	url := item.URL
	if item.Class == "preview" && item.CapabilityURL != "" {
		url = item.CapabilityURL
	}
	if url == "" {
		url = deploymentURL(ctx, item.Env)
	}
	return statusEnvJSON{
		Class:         class,
		Branch:        branch,
		URL:           url,
		CapabilityURL: item.CapabilityURL,
		Env:           item.Env,
		Release:       item.CurrentRelease,
		Health:        item.Health,
		AgeSeconds:    item.AgeSeconds,
		ExpiresAt:     item.ExpiresAt,
		Pinned:        item.Pinned,
		Dirty:         item.Dirty,
		ShippedBy:     item.ShippedBy,
		Processes:     item.Processes,
	}
}

func renderStatusSummary(payload statusPayload) string {
	if len(payload.Envs) == 0 {
		return fmt.Sprintf("No live envs for %s\n", payload.App)
	}
	var b strings.Builder
	for _, env := range payload.Envs {
		release := env.Release
		if release == "" {
			release = "-"
		}
		if env.Dirty {
			release += " (dirty)"
		}
		lifecycle := ""
		switch {
		case env.Class == "preview" && env.Pinned:
			lifecycle = " pinned"
		case env.Class == "preview" && env.ExpiresAt != "":
			lifecycle = " expires=" + env.ExpiresAt
		}
		shippedBy := ""
		if env.ShippedBy != nil {
			shippedBy = fmt.Sprintf("  shipped_by=%q ssh_key=%q", env.ShippedBy.GitAuthor, env.ShippedBy.SSHKeyComment)
		}
		fmt.Fprintf(&b, "%s %s  %s  release=%s  health=%s%s%s\n", statusClassLabel(env.Class), env.Branch, env.URL, release, env.Health, lifecycle, shippedBy)
	}
	return b.String()
}

func renderStatusSummaryWithApprovals(payload statusPayload, pendingApprovals int) string {
	out := renderStatusSummary(payload)
	if pendingApprovals > 0 {
		out += fmt.Sprintf("%d approvals pending — ship approve\n", pendingApprovals)
	}
	return out
}

type remoteApprovalListPayload struct {
	Approvals []struct {
		ID      string `json:"id"`
		Member  string `json:"member"`
		Role    string `json:"role"`
		Request string `json:"request"`
		Expires string `json:"expires"`
	} `json:"approvals"`
}

func fetchPendingApprovalCount(runner sshRunner, server string) (int, error) {
	out, err := runSSHDetail(runner, server, serverApprovalListCommand(true))
	if err != nil {
		return 0, err
	}
	var payload remoteApprovalListPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		return 0, operationError(fmt.Sprintf("status failed: invalid approval list JSON: %v", err), "ship approve")
	}
	return len(payload.Approvals), nil
}

func statusClassLabel(class string) string {
	if class == "production" {
		return "Production"
	}
	return "Preview"
}

func CmdLogs(root string, process string, follow bool, tail *int, jsonFlag bool) {
	if follow && jsonFlag {
		utils.DieError(errcat.New(errcat.CodeLogsFollowJSONConflict, nil), 2)
	}
	if err := ValidateLogsTail(tail); err != nil {
		utils.DieError(err, 2)
	}
	read, err := currentReadContext(root, "logs")
	if err != nil {
		utils.DieError(err, 1)
	}
	defer read.Runner.Close()

	// Follow mode needs interactive stdout/stderr passthrough so the
	// user sees the stream as it arrives. Non-follow mode reads a
	// bounded amount and prints once.
	cmdStr := serverAppLogsCommand(read.AppContext.AppName, read.EnvName, process, follow, tail)
	if follow {
		if err := read.Runner.RunSSHPassthrough(read.AppContext.Server, cmdStr); err != nil {
			utils.DieError(err, 1)
		}
		return
	}
	out, stderr, code, err := read.Runner.RunSSH(read.AppContext.Server, cmdStr)
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(out, stderr, code, err, "logs failed")
		if outcome.TransportCoded != nil {
			utils.DieError(outcome.TransportCoded, 1)
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			utils.DieError(outcome.RemoteCoded, 1)
		}
		if outcome.Detail != "" {
			utils.DieError(operationError(fmt.Sprintf("logs failed: %s", outcome.Detail), "ship logs"), 1)
		}
		utils.DieError(operationError("logs failed", "ship logs"), 1)
	}
	forwardStderr := strings.TrimSpace(stderr) != ""
	if _, stderrIsErrorJSON := errcat.ParseJSON(stderr); stderrIsErrorJSON {
		forwardStderr = false
	}
	writeRemoteStderr(remoteOutcome{Stderr: stderr, ForwardStderr: forwardStderr})
	if strings.TrimSpace(out) == "" && strings.TrimSpace(stderr) == "" {
		fmt.Fprintln(os.Stderr, "no log lines yet")
	}
	if !jsonFlag {
		fmt.Print(out)
		return
	}
	lines := splitLogLines(out)
	payload := struct {
		App     string   `json:"app"`
		Env     string   `json:"env"`
		Process string   `json:"process,omitempty"`
		Lines   []string `json:"lines"`
	}{
		App:     read.AppContext.AppName,
		Env:     read.EnvName,
		Process: process,
		Lines:   lines,
	}
	buf, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println(string(buf))
}

func splitLogLines(out string) []string {
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return []string{}
	}
	return strings.Split(out, "\n")
}

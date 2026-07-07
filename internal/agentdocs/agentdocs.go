package agentdocs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/errcat"
)

type Flag struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Default string `json:"default"`
	Purpose string `json:"purpose"`
}

type Help struct {
	Verb    string   `json:"verb"`
	Purpose string   `json:"purpose"`
	Usage   string   `json:"usage"`
	Flags   []Flag   `json:"flags"`
	Errors  []string `json:"errors"`
}

type Verb struct {
	Verb       string
	Purpose    string
	Usage      string
	Flags      []Flag
	JSONSchema string
	ExitCodes  string
	Errors     []string
	Notes      []string
}

func VerbNames() []string {
	out := make([]string, 0, len(verbs))
	for _, verb := range verbs {
		out = append(out, verb.Verb)
	}
	return out
}

func Lookup(verb string) (Verb, bool) {
	verb = normalizeVerb(verb)
	for _, item := range verbs {
		if item.Verb == verb {
			return item, true
		}
	}
	return Verb{}, false
}

func HelpFor(verb string) (Help, bool) {
	item, ok := Lookup(verb)
	if !ok {
		return Help{}, false
	}
	return item.Help(), true
}

func (v Verb) Help() Help {
	flags := make([]Flag, len(v.Flags))
	copy(flags, v.Flags)
	errors := make([]string, len(v.Errors))
	copy(errors, v.Errors)
	return Help{
		Verb:    v.Verb,
		Purpose: v.Purpose,
		Usage:   v.Usage,
		Flags:   flags,
		Errors:  errors,
	}
}

func HelpJSON(verb string) ([]byte, bool, error) {
	help, ok := HelpFor(verb)
	if !ok {
		return nil, false, nil
	}
	data, err := marshalIndentedNoEscape(help)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func RenderHelpText(verb string) (string, bool) {
	item, ok := Lookup(verb)
	if !ok {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nUsage: %s\n\n", item.Purpose, item.Usage)
	if len(item.Flags) > 0 {
		b.WriteString("Arguments and flags:\n")
		for _, flag := range item.Flags {
			name := flag.Name
			if flag.Value != "" {
				name += " " + flag.Value
			}
			if flag.Default != "" {
				fmt.Fprintf(&b, "- `%s` (default `%s`): %s\n", name, flag.Default, flag.Purpose)
			} else {
				fmt.Fprintf(&b, "- `%s`: %s\n", name, flag.Purpose)
			}
		}
		b.WriteByte('\n')
	}
	if item.JSONSchema != "" {
		b.WriteString("--json stdout schema:\n")
		b.WriteString(item.JSONSchema)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Exit codes: %s\n", item.ExitCodes)
	if len(item.Errors) > 0 {
		b.WriteString("Common errors: ")
		for i, code := range item.Errors {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "`%s`", code)
		}
		b.WriteByte('\n')
	}
	return b.String(), true
}

func RenderSummary() string {
	var b strings.Builder
	b.WriteString("Usage: ship help [verb] [--json]\n\n")
	b.WriteString("Verbs:\n")
	for _, item := range verbs {
		fmt.Fprintf(&b, "- `%s`: %s\n", item.Verb, item.Purpose)
	}
	return b.String()
}

func RenderSummaryJSON() ([]byte, error) {
	payload := Help{
		Verb:    "",
		Purpose: "List ship command usage.",
		Usage:   "ship help [verb] [--json]",
		Flags: []Flag{
			{Name: "--json", Purpose: "Emit this help object as JSON."},
			{Name: "verb", Purpose: "Command name, such as status, secret ls, or box doctor."},
		},
		Errors: []string{string(errcat.CodeUsageError)},
	}
	data, err := marshalIndentedNoEscape(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func RenderMarkdown() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(agentIntro))
	b.WriteString("\n\n")
	b.WriteString(renderVerbSection())
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(outputAndDataContracts))
	b.WriteString("\n\n")
	b.WriteString(RenderErrorCatalogue())
	b.WriteString("\n")
	return b.String()
}

func RenderErrorCatalogue() string {
	var b strings.Builder
	b.WriteString("## Error-code catalogue\n\n")
	b.WriteString("<!-- BEGIN GENERATED ERRCAT -->\n")
	for _, entry := range errcat.Catalogue() {
		if len(entry.Defaults) > 0 {
			keys := make([]string, 0, len(entry.Defaults))
			for key := range entry.Defaults {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			var parts []string
			for _, key := range keys {
				parts = append(parts, fmt.Sprintf("%s=%q", key, entry.Defaults[key]))
			}
			fmt.Fprintf(&b, "- `%s`: %s; cause: %s; remediation: `%s`; defaults: `%s`.\n", entry.Code, entry.MessageTemplate, entry.CauseTemplate, entry.RemediationTemplate, strings.Join(parts, ", "))
		} else {
			fmt.Fprintf(&b, "- `%s`: %s; cause: %s; remediation: `%s`.\n", entry.Code, entry.MessageTemplate, entry.CauseTemplate, entry.RemediationTemplate)
		}
	}
	b.WriteString("<!-- END GENERATED ERRCAT -->")
	return b.String()
}

func renderVerbSection() string {
	var b strings.Builder
	b.WriteString("## Public verbs\n\n")
	b.WriteString("<!-- BEGIN VERBS -->\n")
	for _, item := range verbs {
		fmt.Fprintf(&b, "### `%s`\n", item.Verb)
		fmt.Fprintf(&b, "- Purpose: %s\n", item.Purpose)
		fmt.Fprintf(&b, "- Usage: `%s`\n", item.Usage)
		fmt.Fprintf(&b, "- Arguments and flags: %s\n", inlineFlags(item.Flags))
		if item.JSONSchema != "" {
			fmt.Fprintf(&b, "- `--json` stdout schema: `%s`\n", inlineSchema(item.JSONSchema))
		}
		if len(item.Notes) > 0 {
			fmt.Fprintf(&b, "- Notes: %s\n", strings.Join(item.Notes, " "))
		}
		fmt.Fprintf(&b, "- Exit codes: %s\n", item.ExitCodes)
		if len(item.Errors) > 0 {
			fmt.Fprintf(&b, "- Common error codes: %s\n", inlineCodes(item.Errors))
		}
		b.WriteByte('\n')
	}
	b.WriteString("<!-- END VERBS -->")
	return b.String()
}

func inlineFlags(flags []Flag) string {
	if len(flags) == 0 {
		return "none."
	}
	parts := make([]string, 0, len(flags))
	for _, flag := range flags {
		name := flag.Name
		if flag.Value != "" {
			name += " " + flag.Value
		}
		purpose := inlinePurpose(flag.Purpose)
		part := fmt.Sprintf("`%s`: %s", name, purpose)
		if flag.Default != "" {
			part = fmt.Sprintf("`%s` default `%s`: %s", name, flag.Default, purpose)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ") + "."
}

func inlinePurpose(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), ".")
}

func inlineCodes(codes []string) string {
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, "`"+code+"`")
	}
	return strings.Join(parts, ", ")
}

func inlineSchema(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) >= 2 && strings.HasPrefix(lines[0], "```") && strings.TrimSpace(lines[len(lines)-1]) == "```" {
		lines = lines[1 : len(lines)-1]
	}
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

func normalizeVerb(verb string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(verb)), " ")
}

func schema(lines ...string) string {
	var b bytes.Buffer
	b.WriteString("```json\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("```")
	return b.String()
}

func marshalIndentedNoEscape(value any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

var configFlag = Flag{Name: "--config", Value: "<path>", Default: "ship.toml", Purpose: "Path to the app manifest."}

var normalExit = "0 success; 1 operation failed with an error object when available; 2 usage or manifest error."

var verbs = []Verb{
	{
		Verb:    "ship",
		Purpose: "Deploy the current branch and print the deployment URL.",
		Usage:   "ship [--json] [--branch <name>] [--tls auto|internal] [--rebuild] [--include-dotenv] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "--json", Purpose: "Emit the mutation object instead of stdout-is-URL."},
			{Name: "--branch", Value: "<name>", Purpose: "Detached HEAD only; supplies the branch used for branch=env resolution."},
			{Name: "--tls", Value: "auto|internal", Default: "auto", Purpose: "Select automatic public TLS or internal TLS for synthesized routes."},
			{Name: "--rebuild", Purpose: "Refresh base images and bypass the container build cache."},
			{Name: "--include-dotenv", Purpose: "Allow .env-style files in the uploaded artifact."},
		},
		JSONSchema: schema(
			`{"url":"https://...","env":"prod","release":"abc123","processes":["web"],"durationMs":1234}`,
		),
		ExitCodes: normalExit,
		Errors: []string{
			"not_a_git_repo", "detached_head_requires_branch", "branch_flag_requires_detached_head",
			"unmappable_branch_name", "dirty_worktree", "behind_production", "manifest_invalid", "dockerfile_missing",
			"multi_process_no_web_route", "secret_missing", "remote_preflight_failed",
			"remote_preflight_after_prepare_failed", "deploy_blocked_local_checks", "release_command_failed",
			"probe_failed", "dotenv_rejected",
		},
		Notes: []string{
			"Successful non-JSON stdout is exactly one URL plus a trailing newline; all phase lines go to stderr.",
			"Production refuses dirty worktrees and stale checkouts; Preview accepts dirty worktrees and creates the preview mapping if needed.",
		},
	},
	{
		Verb:    "init",
		Purpose: "Create local project files and a ship.toml manifest.",
		Usage:   "ship init [--template container|static|php|hono] [--name <app>] [--box <ssh-target>] [--host <host>] [--port <port>] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "--template", Value: "container|static|php|hono", Default: "container", Purpose: "Scaffold shape."},
			{Name: "--name", Value: "<app>", Purpose: "App name. Defaults to package.json name or the directory name."},
			{Name: "--box", Value: "<ssh-target>", Default: "deploy@example.com", Purpose: "Box SSH target written to the manifest."},
			{Name: "--host", Value: "<host>", Purpose: "Route host. Defaults to <app>.example.com."},
			{Name: "--port", Value: "<port>", Purpose: "Internal process port for container templates."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"usage_error", "manifest_invalid"},
		Notes:     []string{"Never overwrites existing files; kept files are reported on stdout."},
	},
	{
		Verb:    "status",
		Purpose: "Show all live environments for this app.",
		Usage:   "ship status [--json] [--config <path>]",
		Flags:   []Flag{configFlag, {Name: "--json", Purpose: "Emit structured JSON instead of the text table."}},
		JSONSchema: schema(
			`{"app":"api","envs":[{"kind":"Production","branch":"main","url":"https://...","env":"prod","release":"abc123","health":"healthy","ageSeconds":10,"expiresAt":"2026-07-10T10:00:00Z","pinned":false,"dirty":false,"shipped_by":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"processes":[{"process":"web","container":"...","state":"running","image":"...","release":"abc123","dirty":false,"base_commit":"...","created_at":"...","status":"Up 1 minute"}]}]}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"manifest_invalid", "ssh_unreachable", "box_not_initialized", "operation_failed"},
	},
	{
		Verb:    "logs",
		Purpose: "Print logs for the current branch environment.",
		Usage:   "ship logs [process] [--follow] [--tail N] [--json] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "process", Purpose: "Process name. Optional only when one process exists."},
			{Name: "--follow", Purpose: "Stream new log lines."},
			{Name: "--tail", Value: "<N>", Default: "100", Purpose: "Number of trailing lines in non-follow mode."},
			{Name: "--json", Purpose: "Emit captured log lines as JSON. Cannot be combined with --follow."},
		},
		JSONSchema: schema(
			`{"app":"api","env":"prod","process":"web","lines":["line 1","line 2"]}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"logs_follow_json_conflict", "unknown_preview_branch", "operation_failed"},
	},
	{
		Verb:    "exec",
		Purpose: "Run a one-off command inside the current branch environment.",
		Usage:   "ship exec [--branch <name>] [--config <path>] -- <cmd...>",
		Flags: []Flag{
			configFlag,
			{Name: "--branch", Value: "<name>", Purpose: "Read/exec another branch environment."},
			{Name: "cmd", Value: "<cmd...>", Purpose: "Command and arguments passed through to the remote process environment."},
		},
		ExitCodes: "0 when the remote command exits 0; the remote command exit status is passed through unchanged; 1 only for setup/transport failure; 2 usage or manifest error.",
		Errors:    []string{"usage_error", "unknown_preview_branch", "no_deploys", "operation_failed"},
		Notes: []string{
			"After setup, stdin/stdout/stderr are passthrough. The command runs with resolved secrets and /data mounted.",
			"Use `--` before commands that start with a dash.",
		},
	},
	{
		Verb:    "why",
		Purpose: "Explain the latest deploy journal entry for the current branch environment.",
		Usage:   "ship why [--branch <name>] [--json] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "--branch", Value: "<name>", Purpose: "Inspect another branch environment."},
			{Name: "--json", Purpose: "Emit the raw deploy journal entry."},
		},
		JSONSchema: schema(
			`{"schema_version":1,"app":"api","env":"prod","outcome":"aborted_probe","started_at":"...","ended_at":"...","previous_release":"abc","attempted_release":"def","failing_step":"probe","stderr_tail":"...","identity":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"probe":{"status":502,"body_snippet":"..."}}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"unknown_preview_branch", "no_deploys", "operation_failed"},
	},
	{
		Verb:      "rollback",
		Purpose:   "Move the current branch environment back to a previous release.",
		Usage:     "ship rollback [release] [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "release", Purpose: "Release to run. Omitted means previous local release."}},
		ExitCodes: normalExit,
		Errors:    []string{"unknown_preview_branch", "no_deploys", "operation_failed"},
	},
	{
		Verb:      "rm",
		Purpose:   "Destroy an environment by branch name.",
		Usage:     "ship rm <branch> [--confirm <app>] [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "branch", Purpose: "Branch whose environment should be removed."}, {Name: "--confirm", Value: "<app>", Purpose: "Required app-name confirmation for Production."}},
		ExitCodes: normalExit,
		Errors:    []string{"rm_confirmation_required", "unknown_preview_branch", "production_branch_not_preview", "operation_failed"},
	},
	{
		Verb:      "pin",
		Purpose:   "Pin a Preview environment so the reaper leaves it running.",
		Usage:     "ship pin <branch> [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "branch", Purpose: "Preview branch to pin."}},
		ExitCodes: normalExit,
		Errors:    []string{"production_branch_not_preview", "unmappable_branch_name", "unknown_preview_branch", "operation_failed"},
	},
	{
		Verb:      "unpin",
		Purpose:   "Unpin a Preview environment so normal expiry applies.",
		Usage:     "ship unpin <branch> [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "branch", Purpose: "Preview branch to unpin."}},
		ExitCodes: normalExit,
		Errors:    []string{"production_branch_not_preview", "unmappable_branch_name", "unknown_preview_branch", "operation_failed"},
	},
	{
		Verb:      "save",
		Purpose:   "Create a backup for the current branch environment.",
		Usage:     "ship save [--to <path>] [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "--to", Value: "<path>", Purpose: "Destination directory on the host. Supports plain paths and file:// URLs."}},
		ExitCodes: normalExit,
		Errors:    []string{"unknown_preview_branch", "operation_failed"},
	},
	{
		Verb:      "restore",
		Purpose:   "Restore the current branch environment from a backup.",
		Usage:     "ship restore --from <id|path> [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "--from", Value: "<id|path>", Purpose: "Backup ID or path on the host."}},
		ExitCodes: normalExit,
		Errors:    []string{"unknown_preview_branch", "operation_failed"},
	},
	{
		Verb:      "ssh",
		Purpose:   "Open an SSH session to the box for the current app.",
		Usage:     "ship ssh [--config <path>]",
		Flags:     []Flag{configFlag},
		ExitCodes: "0 when SSH exits 0; SSH failures return 1; usage or manifest errors return 2.",
		Errors:    []string{"manifest_invalid", "ssh_unreachable", "operation_failed"},
	},
	{
		Verb:    "secret set",
		Purpose: "Read a secret value from stdin and store it on the host.",
		Usage:   "ship secret set <KEY> [--preview|--branch <name>] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "KEY", Purpose: "Environment variable name, matching ^[A-Za-z_][A-Za-z0-9_]*$."},
			{Name: "--preview", Purpose: "Store the shared Preview value."},
			{Name: "--branch", Value: "<name>", Purpose: "Store the value for one branch Preview environment."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"invalid_secret_key", "secret_scope_conflict", "unknown_preview_branch", "operation_failed"},
		Notes:     []string{"Values are stdin-only and are never echoed, placed in argv, or written into the repo."},
	},
	{
		Verb:    "secret ls",
		Purpose: "List secret keys for a scope. Values are never printed.",
		Usage:   "ship secret ls [--preview|--branch <name>] [--json] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "--preview", Purpose: "List the shared Preview scope."},
			{Name: "--branch", Value: "<name>", Purpose: "List one branch Preview scope."},
			{Name: "--json", Purpose: "Emit structured JSON."},
		},
		JSONSchema: schema(
			`{"app":"api","env":"prod","keys":["DATABASE_URL"]}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"secret_scope_conflict", "unknown_preview_branch", "operation_failed"},
	},
	{
		Verb:    "secret rm",
		Purpose: "Remove a secret key from a scope.",
		Usage:   "ship secret rm <KEY> [--preview|--branch <name>] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "KEY", Purpose: "Environment variable name to remove."},
			{Name: "--preview", Purpose: "Remove from the shared Preview scope."},
			{Name: "--branch", Value: "<name>", Purpose: "Remove from one branch Preview scope."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"invalid_secret_key", "secret_scope_conflict", "unknown_preview_branch", "operation_failed"},
	},
	{
		Verb:    "box init",
		Purpose: "Install or converge a box.",
		Usage:   "ship box init <ssh-target> [flags]",
		Flags: []Flag{
			{Name: "ssh-target", Purpose: "SSH target like deploy@example.com."},
			{Name: "--mode", Value: "auto|local|remote", Default: "auto", Purpose: "Execution mode."},
			{Name: "--host", Value: "<host>", Purpose: "Target VPS host for remote bootstrap."},
			{Name: "--bootstrap-user", Value: "<user>", Purpose: "SSH user for remote bootstrap."},
			{Name: "--ssh-key", Value: "<path>", Purpose: "SSH private key for remote mode."},
			{Name: "--operator-ssh-public-key-file", Value: "<path>", Purpose: "SSH public key file for operator access."},
			{Name: "--deploy-ssh-public-key-file", Value: "<path>", Purpose: "SSH public key file for deploy access."},
			{Name: "--shared-key", Purpose: "Reuse the operator SSH key for deploy access."},
			{Name: "--operator-user", Value: "<user>", Purpose: "Operator user."},
			{Name: "--deploy-user", Value: "<user>", Purpose: "Deploy user."},
			{Name: "--timezone", Value: "<tz>", Purpose: "Host timezone."},
			{Name: "--locale", Value: "<locale>", Purpose: "Host locale."},
			{Name: "--ingress", Value: "public|cloudflare|private", Purpose: "Ingress mode."},
			{Name: "--admin", Value: "public-ssh|tailscale", Purpose: "Admin access mode."},
			{Name: "--tailscale / --no-tailscale", Purpose: "Install and configure Tailscale."},
			{Name: "--tailscale-auth-key", Value: "<key>", Purpose: "Tailscale auth key."},
			{Name: "--tailscale-hostname", Value: "<name>", Purpose: "Tailscale hostname."},
			{Name: "--cloudflare-tunnel / --no-cloudflare-tunnel", Purpose: "Install and configure Cloudflare Tunnel."},
			{Name: "--cloudflare-api-token", Value: "<token>", Purpose: "Cloudflare API token."},
			{Name: "--cloudflare-account-id", Value: "<id>", Purpose: "Cloudflare account ID."},
			{Name: "--cloudflare-tunnel-token", Value: "<token>", Purpose: "Cloudflare tunnel token."},
			{Name: "--cloudflare-tunnel-config", Value: "<path>", Purpose: "Cloudflare tunnel config path."},
			{Name: "--docker / --no-docker", Purpose: "Install Docker."},
			{Name: "--litestream / --no-litestream", Purpose: "Install Litestream."},
			{Name: "--check", Purpose: "Plan changes without mutating the host."},
			{Name: "--yes", Purpose: "Non-interactive mode."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"invalid_box_target", "operation_failed"},
	},
	{
		Verb:    "box add-key",
		Purpose: "Authorize SSH public key access for the box deploy user.",
		Usage:   "ship box add-key <github-user|key|path> [ssh-target]",
		Flags: []Flag{
			{Name: "github-user|key|path", Purpose: "A GitHub username, literal SSH public key, or path to a .pub file."},
			{Name: "ssh-target", Purpose: "SSH target. Defaults to ship.toml box when run in an app directory."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"box_target_required", "invalid_box_target", "github_keys_unavailable", "ssh_public_key_invalid", "operation_failed"},
		Notes:     []string{"Bare GitHub usernames fetch https://github.com/<user>.keys. Existing keys are deduplicated by key material."},
	},
	{
		Verb:    "box doctor",
		Purpose: "Run box diagnostics.",
		Usage:   "ship box doctor [ssh-target] [--json]",
		Flags: []Flag{
			{Name: "ssh-target", Purpose: "SSH target. Defaults to ship.toml box when run in an app directory."},
			{Name: "--json", Purpose: "Emit structured checks instead of text."},
		},
		JSONSchema: schema(
			`[{"id":"disk_space","status":"ok","evidence":"used=10%","remediation":"ship box doctor deploy@example.com"}]`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"box_target_required", "invalid_box_target", "ssh_unreachable", "box_not_initialized", "operation_failed"},
	},
	{
		Verb:    "box ls",
		Purpose: "List app environments visible on a box.",
		Usage:   "ship box ls [ssh-target] [--json]",
		Flags: []Flag{
			{Name: "ssh-target", Purpose: "SSH target. Defaults to ship.toml box when run in an app directory."},
			{Name: "--json", Purpose: "Emit the fleet view JSON."},
		},
		JSONSchema: schema(
			`{"apps":[{"app":"api","envs":[{"class":"production","branch":"main","url":"https://api.example.com","env":"prod","current_release":"abc123","health":"healthy","age_seconds":60,"expires_at":"","pinned":false,"dirty":false,"shipped_by":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"processes":[{"process":"web","container":"...","state":"running","release":"abc123"}],"static":{"release":"abc123","routes":["api.example.com"]}}]}]}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"box_target_required", "invalid_box_target", "ssh_unreachable", "box_not_initialized", "operation_failed"},
	},
	{
		Verb:    "box rm",
		Purpose: "Destroy an app and all of its environments on a box.",
		Usage:   "ship box rm <app> [ssh-target] --confirm <app>",
		Flags: []Flag{
			{Name: "app", Purpose: "App name to destroy."},
			{Name: "ssh-target", Purpose: "SSH target. Defaults to ship.toml box when run in an app directory."},
			{Name: "--confirm", Value: "<app>", Purpose: "Required app-name confirmation."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"box_rm_confirmation_required", "box_target_required", "invalid_box_target", "operation_failed"},
	},
	{
		Verb:      "docs",
		Purpose:   "Print this complete agent contract.",
		Usage:     "ship docs",
		ExitCodes: "0 success.",
	},
	{
		Verb:    "help",
		Purpose: "Print compact usage for one verb.",
		Usage:   "ship help [verb] [--json]",
		Flags: []Flag{
			{Name: "verb", Purpose: "Command name, such as status, secret ls, or box doctor."},
			{Name: "--json", Purpose: "Emit {verb,purpose,usage,flags,errors}."},
		},
		JSONSchema: schema(
			`{"verb":"status","purpose":"Show all live environments for this app.","usage":"ship status [--json] [--config <path>]","flags":[{"name":"--json","value":"","default":"","purpose":"Emit structured JSON instead of the text table."}],"errors":["manifest_invalid"]}`,
		),
		ExitCodes: "0 success; 2 unknown verb or usage error.",
		Errors:    []string{"usage_error"},
	},
	{
		Verb:      "version",
		Purpose:   "Print the ship version.",
		Usage:     "ship version",
		ExitCodes: "0 success.",
	},
}

const agentIntro = `
# ship agent contract

This file is written for coding agents operating ` + "`ship`" + `. Treat it as
the durable CLI contract. The implementation can change internally; these
surfaces should not drift.

## Mental model

The product has five ideas:

- ` + "`repo`" + `: a Git checkout containing one ` + "`ship.toml`" + ` manifest.
- ` + "`box`" + `: one hardened Linux host reached over SSH.
- ` + "`branch`" + `: the environment selector. There is no public ` + "`--env`" + ` flag.
- ` + "`snapshot`" + `: an immutable deployed release, usually a commit-derived id.
- ` + "`URL`" + `: the thing humans review. A successful ` + "`ship`" + ` prints exactly this.

Branch resolution is client-side:

- Current branch equal to ` + "`production_branch`" + ` deploys Production. If the manifest
  omits it, ` + "`main`" + ` is used when present, otherwise ` + "`master`" + `.
- Any other branch is Preview. The box maps the raw branch to a sanitized
  env name plus a persisted random 4-character suffix.
- Detached HEAD requires ` + "`ship --branch <name>`" + ` for deploy. On a normal
  checked-out branch, deploy rejects ` + "`--branch`" + ` because Git is the truth.
- Read verbs that accept ` + "`--branch`" + ` can inspect another branch environment.
- Production refuses dirty worktrees and stale checkouts. Preview accepts dirty
  worktrees and marks the release dirty.

Preview lifecycle:

- First deploy creates the preview mapping and URL.
- The default TTL is 72 hours from the last ship.
- ` + "`ship pin <branch>`" + ` clears expiry; ` + "`ship unpin <branch>`" + ` restores it.
- The box reaper destroys expired previews and purges their secrets.
- Production is never reaped. ` + "`ship rm`" + ` on Production requires ` + "`--confirm <app>`" + `.
- Preview URLs are the preview env host, usually a synthesized sslip.io host
  unless a later wildcard-domain feature exists.

Truth stores:

- Manifest truth is the repo ` + "`ship.toml`" + ` plus the manifest snapshot stored with each
  release under the env release directory on the box.
- Box truth is host state: env identity files, preview mapping metadata,
  release metadata, deploy journals, secrets, Podman labels, Caddy fragments,
  and doctor state.
- Use manifest snapshots to answer "what did this release intend?"
- Use box state to answer "what is live now?"

Manifest env:

- ` + "`[env]`" + ` defines committed container environment variables for every deploy.
- Values are strings. ` + "`\"@secret\"`" + ` means secret name equals the env key;
  ` + "`\"@secret:NAME\"`" + ` points at a different secret key.
- ` + "`[env.preview]`" + ` overlays ` + "`[env]`" + ` for Preview only. Keys merge, and the
  Preview value wins. Production ignores the overlay.
- ` + "`[env.preview]`" + ` secrets resolve through Preview secret scoping: branch first,
  then shared Preview, never Production.
- The scalar key ` + "`preview`" + ` is reserved under ` + "`[env]`" + `, and no other
  ` + "`[env.<name>]`" + ` table exists.

Secret scoping:

- ` + "`ship secret set KEY`" + ` stores the Production value.
- ` + "`ship secret set KEY --preview`" + ` stores one shared Preview value.
- ` + "`ship secret set KEY --branch <name>`" + ` stores a value for that branch Preview env.
- Production resolves Production values only.
- Preview resolves branch value first, then shared Preview value.
- Preview never falls back to Production.
- Values are stdin-only. Keys can be listed; values are never printed.
`

const outputAndDataContracts = `
## Output contract

- Successful ` + "`ship`" + ` without ` + "`--json`" + ` writes exactly the deployment URL to stdout.
- All progress, warnings, timings, and next steps go to stderr.
- ` + "`ship --json`" + ` writes the mutation object to stdout instead of the URL.
- During deploy, stderr has phase lines such as ` + "`preflight 0.4s`" + `, ` + "`build 6.2s`" + `, ` + "`release 1.1s`" + `, ` + "`probe ok`" + `, and ` + "`live`" + `.
- Human errors are exactly: what failed, cause, then ` + "`next: <command>`" + `.
- JSON errors are ` + "`{\"error\":{\"code\":\"...\",\"message\":\"...\",\"cause\":\"...\",\"remediation\":\"...\"}}`" + `.
- Exit codes are ` + "`0`" + ` success, ` + "`1`" + ` operation failed, ` + "`2`" + ` usage or manifest error, except ` + "`ship exec`" + ` passes through the remote command exit status after setup.
- User-facing language is ` + "`Production <branch>`" + ` or ` + "`Preview <branch>`" + `. Internal env slugs appear only in URLs and JSON fields.

## Deploy journal schema

Each env has an append-only ` + "`journal.jsonl`" + `. Each line is:

` + "```json" + `
{"schema_version":1,"app":"api","env":"prod","outcome":"deployed | aborted_build | aborted_release | aborted_probe | rolled_back","started_at":"2026-07-07T10:00:00Z","ended_at":"2026-07-07T10:00:10Z","previous_release":"abc123","attempted_release":"def456","failing_step":"build | release | probe","stderr_tail":"last scrubbed stderr lines","identity":{"ssh_key_comment":"ship-deploy","git_author":"Name <name@example.com>"},"probe":{"status":502,"body_snippet":"scrubbed response body"}}
` + "```" + `

## Notify payload schemas

All events POST ` + "`{\"app\",\"env\",\"event\",\"release\",\"summary\",\"why\",\"remediation\",\"ts\"}`" + ` and never fail the operation.

- ` + "`deploy_aborted`" + `: ` + "`why`" + ` is a deploy journal entry; ` + "`remediation`" + ` is ` + "`{\"command\":\"ship\",\"journal\":\"<entry>\"}`" + `.
- ` + "`deploy_recovered`" + `: ` + "`why`" + ` is ` + "`{\"previous_failure\":\"<entry>\",\"current\":\"<entry>\"}`" + `; ` + "`remediation`" + ` is ` + "`{\"command\":\"ship status\",\"journal\":\"<current>\",\"previous_failure\":\"<previous>\"}`" + `.
- ` + "`preview_reaped`" + `: ` + "`why`" + ` is ` + "`{\"branch\":\"feature/x\",\"env\":\"Preview feature/x\",\"expired_at\":\"...\"}`" + `; ` + "`remediation`" + ` is ` + "`{\"command\":\"git checkout feature/x && ship\",\"branch\":\"feature/x\",\"env\":\"Preview feature/x\"}`" + `.
- ` + "`doctor_degraded`" + `: ` + "`why`" + ` is a doctor check ` + "`{\"id\",\"status\",\"evidence\",\"remediation\"}`" + `; ` + "`remediation`" + ` is ` + "`{\"command\":\"<check.remediation>\",\"check\":\"<doctor check>\"}`" + `.
`

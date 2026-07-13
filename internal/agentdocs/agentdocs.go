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

func CompletionScript(shell string) (string, bool) {
	switch shell {
	case "bash":
		return renderBashCompletion(), true
	case "zsh":
		return renderZshCompletion(), true
	case "fish":
		return renderFishCompletion(), true
	default:
		return "", false
	}
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

func completionVerbsJSON() string {
	data, err := json.Marshal(VerbNames())
	if err != nil {
		panic(err)
	}
	return string(data)
}

func renderBashCompletion() string {
	var b strings.Builder
	writeCompletionHeader(&b, "bash")
	fmt.Fprintf(&b, "_ship_completion() {\n")
	fmt.Fprintf(&b, "  local cur=\"${COMP_WORDS[COMP_CWORD]}\"\n")
	fmt.Fprintf(&b, "  local top_commands=%q\n", strings.Join(topLevelCompletionCommands(), " "))
	fmt.Fprintf(&b, "  local shell_commands=\"bash zsh fish\"\n")
	writeBashFlagLocals(&b)
	fmt.Fprintf(&b, "  if [[ $COMP_CWORD -eq 1 ]]; then\n")
	fmt.Fprintf(&b, "    if [[ \"$cur\" == --* ]]; then COMPREPLY=( $(compgen -W \"$ship_flags\" -- \"$cur\") ); else COMPREPLY=( $(compgen -W \"$top_commands\" -- \"$cur\") ); fi\n")
	fmt.Fprintf(&b, "    return\n")
	fmt.Fprintf(&b, "  fi\n")
	fmt.Fprintf(&b, "  case \"${COMP_WORDS[1]}\" in\n")
	for _, parent := range topLevelCompletionCommands() {
		children := completionChildren(parent)
		if len(children) == 0 {
			flags := bashFlagLocalName(parent)
			fmt.Fprintf(&b, "    %s)\n", bashCaseWord(parent))
			if parent == "completion" {
				fmt.Fprintf(&b, "      COMPREPLY=( $(compgen -W \"$shell_commands\" -- \"$cur\") )\n")
			} else {
				fmt.Fprintf(&b, "      COMPREPLY=( $(compgen -W \"$%s\" -- \"$cur\") )\n", flags)
			}
			fmt.Fprintf(&b, "      return ;;\n")
			continue
		}
		fmt.Fprintf(&b, "    %s)\n", bashCaseWord(parent))
		fmt.Fprintf(&b, "      if [[ $COMP_CWORD -eq 2 ]]; then COMPREPLY=( $(compgen -W %q -- \"$cur\") ); return; fi\n", strings.Join(children, " "))
		fmt.Fprintf(&b, "      case \"${COMP_WORDS[2]}\" in\n")
		for _, child := range children {
			verb := parent + " " + child
			fmt.Fprintf(&b, "        %s) COMPREPLY=( $(compgen -W \"$%s\" -- \"$cur\") ); return ;;\n", bashCaseWord(child), bashFlagLocalName(verb))
		}
		if parent == "completion" {
			fmt.Fprintf(&b, "        *) COMPREPLY=( $(compgen -W \"$shell_commands\" -- \"$cur\") ); return ;;\n")
		}
		fmt.Fprintf(&b, "      esac\n")
		fmt.Fprintf(&b, "      ;;\n")
	}
	fmt.Fprintf(&b, "  esac\n")
	fmt.Fprintf(&b, "}\n")
	fmt.Fprintf(&b, "complete -F _ship_completion ship\n")
	return b.String()
}

func renderZshCompletion() string {
	var b strings.Builder
	writeCompletionHeader(&b, "zsh")
	b.WriteString("#compdef ship\n\n")
	b.WriteString("_ship() {\n")
	fmt.Fprintf(&b, "  local -a top_commands\n  top_commands=(%s)\n", strings.Join(shellWords(topLevelCompletionCommands()), " "))
	b.WriteString("  if (( CURRENT == 2 )); then\n")
	b.WriteString("    compadd -- $top_commands\n")
	b.WriteString("    return\n")
	b.WriteString("  fi\n")
	b.WriteString("  case \"$words[2]\" in\n")
	for _, parent := range topLevelCompletionCommands() {
		children := completionChildren(parent)
		if len(children) == 0 {
			fmt.Fprintf(&b, "    %s)\n", bashCaseWord(parent))
			if parent == "completion" {
				fmt.Fprintf(&b, "      compadd -- 'bash' 'zsh' 'fish'\n")
			} else {
				fmt.Fprintf(&b, "      compadd -- %s\n", strings.Join(shellWords(completionFlagNames(parent)), " "))
			}
			b.WriteString("      return ;;\n")
			continue
		}
		fmt.Fprintf(&b, "    %s)\n", bashCaseWord(parent))
		b.WriteString("      if (( CURRENT == 3 )); then\n")
		fmt.Fprintf(&b, "        compadd -- %s\n", strings.Join(shellWords(children), " "))
		b.WriteString("        return\n")
		b.WriteString("      fi\n")
		b.WriteString("      case \"$words[3]\" in\n")
		for _, child := range children {
			verb := parent + " " + child
			fmt.Fprintf(&b, "        %s) compadd -- %s; return ;;\n", bashCaseWord(child), strings.Join(shellWords(completionFlagNames(verb)), " "))
		}
		b.WriteString("      esac\n")
		b.WriteString("      ;;\n")
	}
	b.WriteString("  esac\n")
	b.WriteString("}\n\n")
	b.WriteString("_ship \"$@\"\n")
	return b.String()
}

func renderFishCompletion() string {
	var b strings.Builder
	writeCompletionHeader(&b, "fish")
	b.WriteString("complete -c ship -f\n")
	for _, flag := range completionFlagNames("ship") {
		writeFishFlag(&b, "__fish_use_subcommand", flag)
	}
	for _, command := range topLevelCompletionCommands() {
		verb, _ := Lookup(command)
		fmt.Fprintf(&b, "complete -c ship -n __fish_use_subcommand -a %s -d %s\n", fishQuote(command), fishQuote(verb.Purpose))
		for _, flag := range completionFlagNames(command) {
			writeFishFlag(&b, "__fish_seen_subcommand_from "+command, flag)
		}
		if command == "completion" {
			b.WriteString("complete -c ship -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'\n")
		}
		children := completionChildren(command)
		if len(children) == 0 {
			continue
		}
		condition := "__fish_seen_subcommand_from " + command + "; and not __fish_seen_subcommand_from " + strings.Join(children, " ")
		for _, child := range children {
			verb, _ := Lookup(command + " " + child)
			fmt.Fprintf(&b, "complete -c ship -n %s -a %s -d %s\n", fishQuote(condition), fishQuote(child), fishQuote(verb.Purpose))
			childCondition := "__fish_seen_subcommand_from " + command + "; and __fish_seen_subcommand_from " + child
			for _, flag := range completionFlagNames(command + " " + child) {
				writeFishFlag(&b, childCondition, flag)
			}
		}
	}
	return b.String()
}

func writeCompletionHeader(b *strings.Builder, shell string) {
	fmt.Fprintf(b, "# ship %s completion\n", shell)
	fmt.Fprintf(b, "# ship completion verbs-json: %s\n", completionVerbsJSON())
	switch shell {
	case "bash":
		b.WriteString("# Install: ship completion bash > /etc/bash_completion.d/ship\n\n")
	case "zsh":
		b.WriteString("# Install: mkdir -p ~/.zsh/completions && ship completion zsh > ~/.zsh/completions/_ship\n\n")
	case "fish":
		b.WriteString("# Install: mkdir -p ~/.config/fish/completions && ship completion fish > ~/.config/fish/completions/ship.fish\n\n")
	}
}

func writeBashFlagLocals(b *strings.Builder) {
	fmt.Fprintf(b, "  local ship_flags=%q\n", strings.Join(completionFlagNames("ship"), " "))
	for _, verb := range VerbNames() {
		if verb == "ship" {
			continue
		}
		fmt.Fprintf(b, "  local %s=%q\n", bashFlagLocalName(verb), strings.Join(completionFlagNames(verb), " "))
	}
}

func topLevelCompletionCommands() []string {
	seen := map[string]bool{}
	for _, verb := range VerbNames() {
		if verb == "ship" {
			continue
		}
		first := strings.Fields(verb)[0]
		seen[first] = true
	}
	return sortedMapKeys(seen)
}

func completionChildren(parent string) []string {
	seen := map[string]bool{}
	for _, verb := range VerbNames() {
		parts := strings.Fields(verb)
		if len(parts) != 2 || parts[0] != parent {
			continue
		}
		seen[parts[1]] = true
	}
	return sortedMapKeys(seen)
}

func completionFlagNames(verb string) []string {
	item, ok := Lookup(verb)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for _, flag := range item.Flags {
		for _, name := range strings.Split(flag.Name, " / ") {
			name = strings.TrimSpace(name)
			if strings.HasPrefix(name, "--") {
				seen[name] = true
			}
		}
	}
	return sortedMapKeys(seen)
}

func sortedMapKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func bashFlagLocalName(verb string) string {
	return strings.NewReplacer(" ", "_", "-", "_").Replace(verb) + "_flags"
}

func bashCaseWord(value string) string {
	return strings.ReplaceAll(value, "'", "'\\''")
}

func shellWords(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, shellQuote(value))
	}
	return out
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func fishQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "\\'") + "'"
}

func writeFishFlag(b *strings.Builder, condition, flag string) {
	flag = strings.TrimPrefix(flag, "--")
	fmt.Fprintf(b, "complete -c ship -n %s -l %s\n", fishQuote(condition), fishQuote(flag))
}

var configFlag = Flag{Name: "--config", Value: "<path>", Default: "ship.toml", Purpose: "Path to the app manifest."}

var normalExit = "0 success; 1 operation failed with an error object when available; 2 usage or manifest error."

var verbs = []Verb{
	{
		Verb:    "ship",
		Purpose: "Deploy the current branch and print the deployment URL.",
		Usage:   "ship [--json] [--branch <name>] [--tls auto|internal] [--rebuild] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "--json", Purpose: "Emit the mutation object instead of stdout-is-URL."},
			{Name: "--branch", Value: "<name>", Purpose: "Detached HEAD only; supplies the branch used for branch=env resolution."},
			{Name: "--tls", Value: "auto|internal", Default: "auto", Purpose: "Select automatic public TLS or internal TLS for synthesized routes."},
			{Name: "--rebuild", Purpose: "Refresh base images and bypass the container build cache."},
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
			"probe_failed", "dotenv_rejected", "host_key_changed",
		},
		Notes: []string{
			"Successful non-JSON stdout is exactly one URL plus a trailing newline; all phase lines go to stderr.",
			"Production refuses dirty worktrees and stale checkouts; Preview accepts dirty worktrees and creates the preview mapping if needed.",
		},
	},
	{
		Verb:    "init",
		Purpose: "Create a ship.toml manifest.",
		Usage:   "ship init [--name <app>] [--box <box>] [--host <host>] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "--name", Value: "<app>", Purpose: "App name. Defaults to package.json name or the directory name."},
			{Name: "--box", Value: "<box>", Default: "203.0.113.7", Purpose: "Box host written to the manifest."},
			{Name: "--host", Value: "<host>", Purpose: "Route host. Defaults to <app>.example.com."},
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
			`{"app":"api","envs":[{"class":"production","branch":"main","url":"https://...","env":"prod","release":"abc123","health":"healthy","ageSeconds":10,"expiresAt":"2026-07-10T10:00:00Z","pinned":false,"dirty":false,"shipped_by":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"processes":[{"process":"web","container":"...","state":"running","image":"...","release":"abc123","dirty":false,"base_commit":"...","created_at":"...","status":"Up 1 minute"}]}]}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"manifest_invalid", "ssh_unreachable", "box_not_initialized", "host_key_changed", "operation_failed"},
	},
	{
		Verb:    "logs",
		Purpose: "Print logs for the current branch environment.",
		Usage:   "ship logs [process] [--follow] [--tail N] [--json] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "process", Purpose: "Process name. Optional only when one process exists."},
			{Name: "--follow", Purpose: "Stream new log lines."},
			{Name: "--tail", Value: "<N>", Default: "100", Purpose: "Number of trailing lines. With --follow, use 0 to stream new lines only."},
			{Name: "--json", Purpose: "Emit captured log lines as JSON. Cannot be combined with --follow."},
		},
		JSONSchema: schema(
			`{"app":"api","env":"prod","process":"web","lines":["line 1","line 2"]}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"logs_follow_json_conflict", "unknown_preview_branch", "host_key_changed", "operation_failed"},
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
		Errors:    []string{"usage_error", "unknown_preview_branch", "no_deploys", "host_key_changed", "operation_failed"},
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
		Errors:    []string{"unknown_preview_branch", "no_deploys", "host_key_changed", "operation_failed"},
	},
	{
		Verb:      "rollback",
		Purpose:   "Move the current branch environment back to a previous release.",
		Usage:     "ship rollback [release] [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "release", Purpose: "Release to run. Omitted means previous local release."}},
		ExitCodes: normalExit,
		Errors:    []string{"unknown_preview_branch", "no_deploys", "host_key_changed", "operation_failed"},
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
		Verb:      "data fork",
		Purpose:   "Fork Production /data into the current branch Preview.",
		Usage:     "ship data fork [--config <path>]",
		Flags:     []Flag{configFlag},
		ExitCodes: normalExit,
		Errors:    []string{"data_fork_on_production", "no_preview_env", "approval_required", "host_key_changed", "missing_tool", "operation_failed"},
		Notes: []string{
			"Run from a Preview branch whose environment already exists. Production branches are refused.",
			"Requires owner or shipper. Agent-role keys mint `approval_required`; after `ship approve <id>`, retry the same command.",
			"SQLite files are copied on the box with `VACUUM INTO`; other files copy with `cp -a` using reflink when supported. The client never receives data contents.",
		},
	},
	{
		Verb:      "data rm",
		Purpose:   "Reset the current branch Preview /data to empty.",
		Usage:     "ship data rm [--config <path>]",
		Flags:     []Flag{configFlag},
		ExitCodes: normalExit,
		Errors:    []string{"data_fork_on_production", "no_preview_env", "approval_required", "host_key_changed", "operation_failed"},
		Notes: []string{
			"Run from a Preview branch whose environment already exists. Production branches are refused.",
			"Requires owner or shipper. Agent-role keys mint `approval_required`; after `ship approve <id>`, retry the same command.",
		},
	},
	{
		Verb:      "data save",
		Purpose:   "Save this environment's /data as a local snapshot.",
		Usage:     "ship data save [--out <path>] [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "--out", Value: "<path>", Purpose: "Local path for the snapshot."}},
		ExitCodes: normalExit,
		Errors:    []string{"approval_required", "host_key_changed", "missing_tool", "operation_failed"},
		Notes: []string{
			"Snapshots land at ~/.ship/backups/<app>/<env>-<release>-<utc>.data.tar.gz unless --out is supplied. stdout is exactly that local path; narration is stderr.",
			"SQLite files use VACUUM INTO and other files use cp -a. Consistency is per-file, not cross-file; live writes across files are not one atomic point in time.",
			"Snapshots contain metadata.json and data/ only. Secrets are never included.",
		},
	},
	{
		Verb:      "data restore",
		Purpose:   "Restore this environment's /data from a local snapshot.",
		Usage:     "ship data restore <id|path> [--confirm <app>] [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "id|path", Purpose: "Snapshot filename stem or local path."}, {Name: "--confirm", Value: "<app>", Purpose: "Required app-name confirmation when restoring Production."}},
		ExitCodes: normalExit,
		Errors:    []string{"rm_confirmation_required", "approval_required", "data_snapshot_invalid", "host_key_changed", "operation_failed"},
		Notes: []string{
			"The client uploads to /tmp/ship-deploy; the helper validates gzip/tar, metadata, app identity, and data/ before it stops containers or swaps /data. Snapshot env may differ from the target env.",
			"Production restore requires --confirm <app> and an owner role. Shippers may restore preview data; agents receive approval_required.",
		},
	},
	{
		Verb:      "data ls",
		Purpose:   "List local data snapshots for this app.",
		Usage:     "ship data ls [--json] [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "--json", Purpose: "Emit stable snapshot JSON."}},
		ExitCodes: normalExit,
		Errors:    []string{"operation_failed"},
	},
	{
		Verb:      "preview pin",
		Purpose:   "Pin a Preview environment so the reaper leaves it running.",
		Usage:     "ship preview pin <branch> [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "branch", Purpose: "Preview branch to pin."}},
		ExitCodes: normalExit,
		Errors:    []string{"production_branch_not_preview", "unmappable_branch_name", "unknown_preview_branch", "operation_failed"},
	},
	{
		Verb:      "preview unpin",
		Purpose:   "Unpin a Preview environment so normal expiry applies.",
		Usage:     "ship preview unpin <branch> [--config <path>]",
		Flags:     []Flag{configFlag, {Name: "branch", Purpose: "Preview branch to unpin."}},
		ExitCodes: normalExit,
		Errors:    []string{"production_branch_not_preview", "unmappable_branch_name", "unknown_preview_branch", "operation_failed"},
	},
	{
		Verb:    "preview share",
		Purpose: "Print or rotate this Preview's capability URL.",
		Usage:   "ship preview share [--rotate] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "--rotate", Purpose: "Generate a new Preview capability and rerender its routes."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"no_preview_env", "share_on_production", "approval_required", "host_key_changed", "operation_failed"},
		Notes: []string{
			"Requires a current Preview environment. Any member may read; owners and shippers may rotate; agent-role keys receive approval_required for rotation.",
			"Stdout is exactly the capability URL. Every Preview is protected and its capability dies when that Preview is reaped.",
		},
	},
	{
		Verb:      "ssh",
		Purpose:   "Open an SSH session to the box for the current app.",
		Usage:     "ship ssh [--config <path>]",
		Flags:     []Flag{configFlag},
		ExitCodes: "0 when SSH exits 0; SSH failures return 1; usage or manifest errors return 2.",
		Errors:    []string{"manifest_invalid", "ssh_unreachable", "host_key_changed", "operation_failed"},
	},
	{
		Verb:    "secret set",
		Purpose: "Read one secret value from stdin or bulk-import dotenv KEY=VALUE pairs.",
		Usage:   "ship secret set (<KEY>|--from <path> [--replace]) [--preview|--branch <name>] [--config <path>]",
		Flags: []Flag{
			configFlag,
			{Name: "KEY", Purpose: "Environment variable name, matching ^[A-Za-z_][A-Za-z0-9_]*$."},
			{Name: "--preview", Purpose: "Store the shared Preview value."},
			{Name: "--branch", Value: "<name>", Purpose: "Store the value for one branch Preview environment."},
			{Name: "--from", Value: "<path>", Purpose: "Bulk import dotenv KEY=VALUE pairs from a file. Cannot be combined with KEY."},
			{Name: "--replace", Purpose: "With --from, make the file authoritative for the selected scope and remove omitted keys."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"usage_error", "invalid_secret_key", "dotenv_malformed", "secret_scope_conflict", "unknown_preview_branch", "host_key_changed", "operation_failed"},
		Notes: []string{
			"Single-value mode reads the value from stdin. Bulk mode reads values from the file path; values are never echoed, placed in argv, or written into the repo.",
			"Without --preview or --branch, the current branch selects the secret scope: Production on the production branch, otherwise that branch's Preview.",
			"Bulk dotenv rules: blank lines and full-line # comments are ignored; an `export ` prefix is accepted; unquoted values are trimmed; matching single or double quotes around the whole value are stripped; inline # is treated as value text.",
			"Bulk merge is the default. `--replace` removes scope keys absent from the file and reports removed key names on stderr. Bulk stdout is empty.",
		},
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
		Errors:    []string{"secret_scope_conflict", "unknown_preview_branch", "host_key_changed", "operation_failed"},
		Notes:     []string{"Without --preview or --branch, lists the current branch's scope: Production on the production branch, otherwise that branch's Preview."},
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
		Errors:    []string{"invalid_secret_key", "secret_scope_conflict", "unknown_preview_branch", "host_key_changed", "operation_failed"},
		Notes:     []string{"Without --preview or --branch, removes from the current branch's scope: Production on the production branch, otherwise that branch's Preview."},
	},
	{
		Verb:    "box setup",
		Purpose: "Install or converge a box.",
		Usage:   "ship box setup <ssh-target> [flags]",
		Flags: []Flag{
			{Name: "ssh-target", Purpose: "Bootstrap SSH target like root@example.com or example.com."},
			{Name: "--mode", Value: "auto|local|remote", Default: "auto", Purpose: "Execution mode."},
			{Name: "--bootstrap-user", Value: "<user>", Purpose: "SSH user for remote bootstrap."},
			{Name: "--ssh-key", Value: "<path>", Purpose: "SSH private key for remote mode."},
			{Name: "--operator-ssh-public-key-file", Value: "<path>", Purpose: "SSH public key file for operator access."},
			{Name: "--deploy-ssh-public-key-file", Value: "<path>", Purpose: "SSH public key file for deploy access. Default: your ship identity becomes the first member."},
			{Name: "--check", Purpose: "Plan changes without mutating the host."},
		},
		ExitCodes: normalExit,
		Errors: []string{
			"usage_error", "invalid_box_target", "deploy_key_missing", "operator_key_missing",
			"ssh_private_key_missing", "ssh_public_key_file_missing", "ssh_public_key_file_empty",
			"host_install_requires_root", "host_install_ssh_failed", "unsupported_target_architecture",
			"host_helper_unavailable", "host_helper_download_failed", "host_install_unsupported_os",
			"host_install_missing_tool", "host_install_permission_denied", "host_install_apply_failed",
			"operation_failed",
		},
	},
	{
		Verb:    "member add",
		Purpose: "Authorize SSH public key access for a deploy member.",
		Usage:   "ship member add <github-user|key|path> [--role owner|shipper|agent] [--config <path>]",
		Flags: []Flag{
			{Name: "github-user|key|path", Purpose: "A GitHub username, literal SSH public key, or path to a .pub/.pem file."},
			{Name: "--role", Value: "owner|shipper|agent", Default: "shipper", Purpose: "Role recorded for newly added keys."},
			{Name: "--config", Value: "<path>", Default: "ship.toml", Purpose: "Path to the app manifest containing box."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"manifest_invalid", "invalid_box_target", "github_keys_unavailable", "ssh_public_key_invalid", "host_key_changed", "operation_failed"},
		Notes:     []string{"Bare GitHub usernames fetch https://github.com/<user>.keys. The command prints every fetched key as added or already authorized, with role and SHA256 fingerprint. Existing keys are deduplicated by key material. Agent-role keys are installed with a forced `agent-shell` command; owner and shipper keys remain plain authorized_keys entries."},
	},
	{
		Verb:    "member ls",
		Purpose: "List deploy members from authorized_keys.",
		Usage:   "ship member ls [--json] [--config <path>]",
		Flags: []Flag{
			{Name: "--config", Value: "<path>", Default: "ship.toml", Purpose: "Path to the app manifest containing box."},
			{Name: "--json", Purpose: "Emit structured JSON."},
		},
		JSONSchema: schema(
			`{"members":[{"name":"alice","role":"shipper","key_type":"ssh-ed25519","fingerprint":"SHA256:..."}]}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"manifest_invalid", "invalid_box_target", "host_key_changed", "operation_failed"},
	},
	{
		Verb:    "member rm",
		Purpose: "Remove all SSH keys for a deploy member.",
		Usage:   "ship member rm <name> [--config <path>]",
		Flags: []Flag{
			{Name: "name", Purpose: "Member name, matching the authorized key comment."},
			{Name: "--config", Value: "<path>", Default: "ship.toml", Purpose: "Path to the app manifest containing box."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"manifest_invalid", "invalid_box_target", "member_not_found", "member_last_key", "host_key_changed", "operation_failed"},
		Notes:     []string{"Removes every key whose comment equals the member name. Refuses to remove the last remaining authorized key."},
	},
	{
		Verb:    "approve",
		Purpose: "List or grant one-shot approvals for out-of-role requests.",
		Usage:   "ship approve [id] [--json] [--config <path>]",
		Flags: []Flag{
			{Name: "id", Purpose: "Approval id to grant. Omit to list pending approvals."},
			{Name: "--json", Purpose: "Emit structured pending approvals. Only valid for the list form."},
			{Name: "--config", Value: "<path>", Default: "ship.toml", Purpose: "Path to the app manifest containing box."},
		},
		JSONSchema: schema(
			`{"approvals":[{"id":"abc123xy","member":"alice","role":"agent","request":"app=api env=prod class=production release=abc123","expires":"2026-07-08T10:15:00Z"}]}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"approval_expired", "member_unknown", "role_denied", "host_key_changed", "operation_failed"},
		Notes:     []string{"Bare `ship approve` lists pending requests and prunes expired entries. `ship approve <id>` can be run only by owner or shipper and grants one retry by the original member."},
	},
	{
		Verb:    "box status",
		Purpose: "Show helper version, disk use, apps, and pending approvals for one box.",
		Usage:   "ship box status [<box>] [--json]",
		Flags: []Flag{
			{Name: "box", Purpose: "Box host. Defaults to ship.toml box when run in an app directory."},
			{Name: "--json", Purpose: "Emit {helper_version,client_version,last_client_version,update_available,helper_ahead,disk:{status,evidence},apps:[{app,env_count}],pending_approvals}."},
		},
		JSONSchema: schema(
			`{"helper_version":"v0.4.0","client_version":"v0.4.1","last_client_version":"v0.4.1","update_available":true,"helper_ahead":false,"disk":{"status":"ok","evidence":"/: used=10.0%"},"apps":[{"app":"api","env_count":2}],"pending_approvals":1}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"box_target_required", "invalid_box_target", "ssh_unreachable", "box_not_initialized", "host_key_changed", "operation_failed"},
		Notes:     []string{"Any member may read. When the helper is behind, text output includes `next: ship box update <box>`."},
	},
	{
		Verb:    "box update",
		Purpose: "Converge a box to this client helper version.",
		Usage:   "ship box update [<box>]",
		Flags: []Flag{
			{Name: "box", Purpose: "Box host. Defaults to ship.toml box when run in an app directory."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"approval_required", "client_behind_helper", "box_target_required", "invalid_box_target", "host_key_changed", "operation_failed"},
		Notes:     []string{"Only owners may update directly; other roles use the normal one-shot approval flow. `box update: already current` is the exact no-op output."},
	},
	{
		Verb:    "box doctor",
		Purpose: "Run box diagnostics.",
		Usage:   "ship box doctor [<box>] [--json]",
		Flags: []Flag{
			{Name: "box", Purpose: "Box host. Defaults to ship.toml box when run in an app directory."},
			{Name: "--json", Purpose: "Emit structured checks instead of text."},
		},
		JSONSchema: schema(
			`[{"id":"disk_space","status":"ok","evidence":"used=10%","remediation":"ship box doctor 203.0.113.7"}]`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"box_target_required", "invalid_box_target", "ssh_unreachable", "box_not_initialized", "host_key_changed", "operation_failed"},
	},
	{
		Verb:    "box config",
		Purpose: "Show effective box configuration and where every value comes from.",
		Usage:   "ship box config [<box>] [--json]",
		Flags: []Flag{
			{Name: "box", Purpose: "Box host. Defaults to ship.toml box when run in an app directory."},
			{Name: "--json", Purpose: "Emit stable effective config JSON."},
		},
		JSONSchema: schema(
			`{"config":{"notify.url":{"value":"https://ntfy.example/ship","default":"","source":"set"}}}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"box_config_key_unknown", "box_config_value_invalid", "box_target_required", "invalid_box_target", "host_key_changed", "operation_failed"},
		Notes:     []string{"Any member may read. Every key reports its effective value, default, and whether the value is default or explicitly set."},
	},
	{
		Verb:    "box config set",
		Purpose: "Set one schema-authorized box configuration value.",
		Usage:   "ship box config [<box>] set <key> <value>",
		Flags: []Flag{
			{Name: "box", Purpose: "Box host. Defaults to ship.toml box when run in an app directory."},
			{Name: "key", Purpose: "Configuration key. Current key: notify.url."},
			{Name: "value", Purpose: "Value validated by the key schema."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"approval_required", "box_config_key_unknown", "box_config_value_invalid", "box_target_required", "invalid_box_target", "host_key_changed", "operation_failed"},
		Notes:     []string{"Authorization is declared by the key schema. notify.url is owner-set; an out-of-role request mints one approval and succeeds once after ship approve <id>."},
	},
	{
		Verb:    "box config unset",
		Purpose: "Restore one box configuration key to its schema default.",
		Usage:   "ship box config [<box>] unset <key>",
		Flags: []Flag{
			{Name: "box", Purpose: "Box host. Defaults to ship.toml box when run in an app directory."},
			{Name: "key", Purpose: "Configuration key. Current key: notify.url."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"approval_required", "box_config_key_unknown", "box_target_required", "invalid_box_target", "host_key_changed", "operation_failed"},
		Notes:     []string{"Unset removes the explicit value and restores the schema default."},
	},
	{
		Verb:    "box notify",
		Purpose: "Read, set, or clear the box notification webhook.",
		Usage:   "ship box notify <box> [url] [--rm]",
		Flags: []Flag{
			{Name: "box", Purpose: "Box host. Omit only for a read in an app directory, which uses ship.toml box."},
			{Name: "url", Purpose: "Webhook URL to set. Omit to print the current URL."},
			{Name: "--rm", Purpose: "Clear the box webhook."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"usage_error", "box_config_value_invalid", "box_target_required", "invalid_box_target", "approval_required", "host_key_changed", "operation_failed"},
		Notes: []string{
			"Any member may read. Only owners may set or clear; other roles receive approval_required and retry after ship approve <id>.",
			"This is sugar over box config key notify.url; both paths share one value and journal shape.",
			"When unset, the command prints an unset notice and next: ship box notify <box> <url>.",
		},
	},
	{
		Verb:    "box apps",
		Purpose: "Show the box's app table.",
		Usage:   "ship box apps [<box>] [--json]",
		Flags: []Flag{
			{Name: "box", Purpose: "Box host. Defaults to ship.toml box when run in an app directory."},
			{Name: "--json", Purpose: "Emit the box app/environment list as JSON."},
		},
		JSONSchema: schema(
			`{"apps":[{"app":"api","envs":[{"class":"production","branch":"main","url":"https://api.example.com","env":"prod","current_release":"abc123","health":"healthy","age_seconds":60,"expires_at":"","pinned":false,"dirty":false,"shipped_by":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"processes":[{"process":"web","container":"...","state":"running","release":"abc123"}],"static":{"release":"abc123","routes":["api.example.com"]}}]}]}`,
		),
		ExitCodes: normalExit,
		Errors:    []string{"box_target_required", "invalid_box_target", "ssh_unreachable", "box_not_initialized", "host_key_changed", "operation_failed"},
	},
	{
		Verb:    "box rm",
		Purpose: "Destroy an app and all of its environments on a box.",
		Usage:   "ship box rm <app> [<box>] --confirm <app>",
		Flags: []Flag{
			{Name: "app", Purpose: "App name to destroy."},
			{Name: "box", Purpose: "Box host. Defaults to ship.toml box when run in an app directory."},
			{Name: "--confirm", Value: "<app>", Purpose: "Required app-name confirmation."},
		},
		ExitCodes: normalExit,
		Errors:    []string{"box_rm_confirmation_required", "box_target_required", "invalid_box_target", "host_key_changed", "operation_failed"},
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
		Verb:    "completion",
		Purpose: "Emit a static shell completion script.",
		Usage:   "ship completion <bash|zsh|fish>",
		Flags: []Flag{
			{Name: "bash|zsh|fish", Purpose: "Shell to generate completions for."},
		},
		ExitCodes: "0 success; 2 unsupported shell or usage error.",
		Errors:    []string{"usage_error"},
		Notes: []string{
			"Install bash: `ship completion bash > /etc/bash_completion.d/ship`.",
			"Install zsh: `mkdir -p ~/.zsh/completions && ship completion zsh > ~/.zsh/completions/_ship`.",
			"Install fish: `mkdir -p ~/.config/fish/completions && ship completion fish > ~/.config/fish/completions/ship.fish`.",
		},
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
- ` + "`box`" + `: one hardened Linux host reached over SSH. In ` + "`ship.toml`" + ` and
  box verbs it is a host only, never ` + "`user@host`" + `; setup alone accepts
  ` + "`user@host`" + ` for bootstrap.
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
- ` + "`ship preview pin <branch>`" + ` clears expiry; ` + "`ship preview unpin <branch>`" + ` restores it.
- The box reaper destroys expired previews and purges their secrets.
- Production is never reaped. ` + "`ship rm`" + ` on Production requires ` + "`--confirm <app>`" + `.
- Preview URLs are the preview env host, usually a synthesized sslip.io host
  unless a later wildcard-domain feature exists.

Truth stores:

- Manifest truth is the repo ` + "`ship.toml`" + ` plus the manifest snapshot stored with each
  release under the env release directory on the box.
- Box truth is host state: env identity files, preview mapping metadata,
  release metadata, deploy journals, members, roles, box notification settings,
  secrets, Podman labels, Caddy fragments, and doctor state.
- Members and approvals belong to the box; secrets, envs, and journals belong
  to the app.
- Use manifest snapshots to answer "what did this release intend?"
- Use box state to answer "what is live now?"

Member identity and approvals:

- Every client helper call carries the caller SSH public key fingerprint,
  computed locally from ` + "`~/.ssh/ship.pub`" + ` or the public half of ` + "`SHIP_SSH_KEY`" + `.
- Owner and shipper keys are the teammate trust tier: their authorized_keys
  entries are plain SSH keys, and the helper resolves the client-passed
  fingerprint through the box-global members store and authorized_keys.
- Agent keys are the pinned tier: their authorized_keys entries force
  ` + "`ship server agent-shell --member-fingerprint <fingerprint>`" + `. The forced command rejects
  interactive SSH and arbitrary commands, allows only the ship helper protocol
  and deploy upload staging, and overwrites any client fingerprint claim with
  the fingerprint bound to the authenticated key before the privileged helper runs.
- Members and approvals are box-scoped, not app-scoped.

Manifest env:

- ` + "`[env]`" + ` defines committed container environment variables for every deploy.
- Values are strings. ` + "`\"@secret\"`" + ` is the only secret form and names the secret after the env key.
- ` + "`[env.preview]`" + ` overlays ` + "`[env]`" + ` for Preview only. Keys merge, and the
  Preview value wins. Production ignores the overlay.
- ` + "`[env.preview]`" + ` secrets resolve through Preview secret scoping: branch first,
  then shared Preview, never Production.
- The scalar key ` + "`preview`" + ` is reserved under ` + "`[env]`" + `, and no other
  ` + "`[env.<name>]`" + ` table exists.

Secret scoping:

- ` + "`ship secret set KEY`" + ` stores a value for the current branch: Production on the production branch, otherwise that branch Preview.
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

## Data forks

- ` + "`ship data fork`" + ` copies Production ` + "`/data`" + ` into the current branch Preview and bounces the existing Preview containers.
- ` + "`ship data rm`" + ` empties the current branch Preview ` + "`/data`" + ` and bounces the existing Preview containers.
- ` + "`ship data save`" + ` streams a data-only gzip tar to the laptop; its default destination is ` + "`~/.ship/backups/<app>/<env>-<release>-<utc>.data.tar.gz`" + `. Its stdout is exactly the local path.
- ` + "`ship data restore <id|path>`" + ` uploads through ` + "`/tmp/ship-deploy`" + `, validates the archive before touching ` + "`/data`" + `, then stops containers, swaps data, and starts them. Snapshot envs may be restored into another env.
- ` + "`ship data ls [--json]`" + ` lists local snapshots only; it never calls the helper.
- ` + "`data fork`" + ` and ` + "`data rm`" + ` require an existing Preview environment. If none exists, the error code is ` + "`no_preview_env`" + ` with remediation ` + "`ship`" + `.
- ` + "`data fork`" + ` and ` + "`data rm`" + ` refuse Production branches with ` + "`data_fork_on_production`" + `.
- Owner and shipper roles may run data commands. Agents get ` + "`approval_required`" + ` because Production data is above the agent default role.
- ` + "`ship data fork`" + ` prints forked relative file names and byte sizes, the Preview URL, and this exact PII line: ` + "`note: Production data, including any PII, now exists in this less-guarded Preview.`" + `.
- If no SQLite files are found, ` + "`ship data fork`" + ` still copies non-database files and prints: ` + "`note: No SQLite files found; copied non-database files from /data only.`" + `.
- Data snapshots use SQLite ` + "`VACUUM INTO`" + ` and ` + "`cp -a`" + ` for other files. Their consistency guarantee is per-file, not cross-file.
- Snapshots never contain secrets. After a box loss: ` + "`ship box setup`" + `, ` + "`ship`" + `, ` + "`ship secret set --from .env`" + `, then ` + "`ship data restore`" + `.

## Deploy journal schema

Each env has an append-only ` + "`journal.jsonl`" + `. Each line is:

` + "```json" + `
{"schema_version":1,"app":"api","env":"prod","outcome":"deployed | aborted_build | aborted_release | aborted_probe | rolled_back","started_at":"2026-07-07T10:00:00Z","ended_at":"2026-07-07T10:00:10Z","previous_release":"abc123","attempted_release":"def456","failing_step":"build | release | probe","stderr_tail":"last scrubbed stderr lines","identity":{"ssh_key_comment":"alice","git_author":"Name <name@example.com>"},"probe":{"status":502,"body_snippet":"scrubbed response body"}}
` + "```" + `

## Notify payload schemas

All events POST ` + "`{\"app\",\"env\",\"event\",\"release\",\"summary\",\"why\",\"remediation\",\"ts\"}`" + ` and never fail the operation. Box events also include ` + "`box`" + ` (the box hostname).

App events go only to the affected app manifest ` + "`notify`" + ` URL: ` + "`deploy_aborted`" + `, ` + "`deploy_recovered`" + `, and ` + "`preview_reaped`" + `. Box events go once to the box URL configured by ` + "`ship box notify`" + `, never to app URLs: ` + "`doctor_degraded`" + ` and ` + "`approval_requested`" + `. No configured box URL silently drops box events; journals and doctor state are still recorded.

- ` + "`deploy_aborted`" + `: ` + "`why`" + ` is a deploy journal entry; ` + "`remediation`" + ` is ` + "`{\"command\":\"ship\",\"journal\":\"<entry>\"}`" + `.
- ` + "`deploy_recovered`" + `: ` + "`why`" + ` is ` + "`{\"previous_failure\":\"<entry>\",\"current\":\"<entry>\"}`" + `; ` + "`remediation`" + ` is ` + "`{\"command\":\"ship status\",\"journal\":\"<current>\",\"previous_failure\":\"<previous>\"}`" + `.
- ` + "`preview_reaped`" + `: ` + "`why`" + ` is ` + "`{\"branch\":\"feature/x\",\"env\":\"Preview feature/x\",\"expired_at\":\"...\"}`" + `; ` + "`remediation`" + ` is ` + "`{\"command\":\"git checkout feature/x && ship\",\"branch\":\"feature/x\",\"env\":\"Preview feature/x\"}`" + `.
- ` + "`doctor_degraded`" + `: box event; ` + "`why`" + ` is a doctor check ` + "`{\"id\",\"status\",\"evidence\",\"remediation\"}`" + `; ` + "`remediation`" + ` is ` + "`{\"command\":\"<check.remediation>\",\"check\":\"<doctor check>\"}`" + `.
- ` + "`approval_requested`" + `: box event; ` + "`why`" + ` is ` + "`{\"id\",\"member\",\"verb\",\"target\",\"expires\"}`" + `; ` + "`remediation`" + ` is ` + "`{\"command\":\"ship approve <id>\",\"request\":\"<approval request>\"}`" + `. The request target retains the affected app and env when present; box-target approvals have empty app/env.
`

package client

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/utils"
)

// secretValueFromStdin reads the secret value from this process's
// stdin and trims at most one trailing newline (the kind a tty `read`
// or an `echo` tacks on). Embedded newlines are rejected by the helper
// because Podman's env-file format cannot represent them safely.
func secretValueFromStdin() ([]byte, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, operationError(fmt.Sprintf("read secret value from stdin: %v", err), "ship secret set KEY")
	}
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data = data[:n-1]
	}
	return data, nil
}

const sharedPreviewSecretsEnvName = "preview"

type secretContext struct {
	AppContext *config.AppContext
	EnvName    string
	Runner     secretRunner
	Kind       string
	Branch     string
}

type secretRunner interface {
	sshRunner
	RunSSHWithStdin(server string, command string, stdin []byte) (string, string, int, error)
	Close()
}

func currentSecretContext(root, command string, preview bool, branch string, createBranch bool) (secretContext, error) {
	if preview && branch != "" {
		return secretContext{}, errcat.New(errcat.CodeSecretScopeConflict, errcat.Fields{
			"command": secretScopeConflictCommand(command),
		})
	}
	ctx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		return secretContext{}, err
	}
	if !preview && branch == "" {
		state, err := currentGitState(root)
		if err != nil {
			return secretContext{}, err
		}
		if state.Detached {
			return secretContext{}, errcat.New(errcat.CodeDetachedHeadRequiresBranch, errcat.Fields{"command": "git checkout <branch>"})
		}
		branch = state.Branch
	}
	runner, err := NewCommandRunner()
	if err != nil {
		return secretContext{}, err
	}
	secret := secretContext{
		AppContext: ctx,
		EnvName:    productionEnvName,
		Runner:     runner,
		Kind:       "Production",
		Branch:     ctx.ProductionBranch,
	}
	switch {
	case preview:
		secret.EnvName = sharedPreviewSecretsEnvName
		secret.Kind = "Preview"
		secret.Branch = ""
	case branch != "":
		if !names.ValidGitBranch(branch) {
			runner.Close()
			return secretContext{}, usageError(fmt.Sprintf("invalid preview branch mapping key: %q", branch), secretScopeConflictCommand(command))
		}
		if branch == ctx.ProductionBranch {
			return secret, nil
		}
		env, err := resolvePreviewEnv(runner, ctx, branch, createBranch)
		if err != nil {
			runner.Close()
			return secretContext{}, err
		}
		secret.EnvName = env
		secret.Kind = "Preview"
		secret.Branch = branch
	}
	return secret, nil
}

func secretScopeConflictCommand(command string) string {
	switch command {
	case "secret ls":
		return "ship secret ls --preview"
	case "secret rm":
		return "ship secret rm KEY --preview"
	case "secret set --from":
		return "ship secret set --from path/to/.env --preview"
	default:
		return "ship secret set KEY --preview"
	}
}

func (s secretContext) surface() string {
	if s.Kind == "Production" {
		return "Production " + s.Branch
	}
	if s.Branch != "" {
		return "Preview " + s.Branch
	}
	return "Preview"
}

type SecretSetOptions struct {
	Key     string
	From    string
	Preview bool
	Branch  string
	Replace bool
}

func CmdSecretSet(root string, opts SecretSetOptions) {
	if err := validateSecretSetOptions(opts); err != nil {
		utils.DieError(err, 1)
	}
	if opts.From != "" {
		imported, err := parseDotenvSecretFile(opts.From)
		if err != nil {
			utils.DieError(err, 1)
		}
		secret, err := currentSecretContext(root, "secret set --from", opts.Preview, opts.Branch, true)
		if err != nil {
			utils.DieError(err, 1)
		}
		defer secret.Runner.Close()
		summary, err := applySecretImport(secret, imported, opts.Replace, secretImportCommand(opts.From))
		if err != nil {
			utils.DieError(err, 1)
		}
		writeSecretImportSummary(os.Stderr, summary)
		return
	}
	if err := envKeyValid(opts.Key); err != nil {
		utils.DieError(err, 1)
	}
	secret, err := currentSecretContext(root, "secret set", opts.Preview, opts.Branch, true)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer secret.Runner.Close()
	value, err := secretValueFromStdin()
	if err != nil {
		utils.DieError(err, 1)
	}

	// Pipe the value over the helper's stdin — never argv, never a
	// file on disk between hops. The helper writes it straight to
	// /etc/ship/secrets/<app>/<env>/<key>.
	if err := putSecretValue(secret, opts.Key, value, "ship secret set "+opts.Key); err != nil {
		utils.DieError(err, 1)
	}
	// Don't echo stdout — it'd carry the helper's confirmation
	// (which already names the key but not the value). Print our own
	// mutation report on stderr.
	fmt.Fprintf(os.Stderr, "Stored secret %s for %s.\n", opts.Key, secret.surface())
	fmt.Fprintln(os.Stderr, "next: ship")
}

func validateSecretSetOptions(opts SecretSetOptions) error {
	switch {
	case opts.From != "" && opts.Key != "":
		return usageError("--from and KEY cannot be combined", "ship secret set --from path/to/.env")
	case opts.From == "" && opts.Key == "":
		return usageError("missing KEY or --from <path>", "ship secret set KEY")
	case opts.From == "" && opts.Replace:
		return usageError("--replace requires --from <path>", "ship secret set --from path/to/.env --replace")
	case opts.From != "" && strings.TrimSpace(opts.From) == "":
		return usageError("--from cannot be empty", "ship secret set --from path/to/.env")
	default:
		return nil
	}
}

type dotenvSecretImport struct {
	Keys   []string
	Values map[string][]byte
}

func parseDotenvSecretFile(path string) (dotenvSecretImport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return dotenvSecretImport{}, operationError(fmt.Sprintf("read dotenv file %q: %v", path, err), secretImportCommand(path))
	}
	return parseDotenvSecretData(path, data)
}

func parseDotenvSecretData(path string, data []byte) (dotenvSecretImport, error) {
	out := dotenvSecretImport{Values: map[string][]byte{}}
	for i, line := range strings.Split(string(data), "\n") {
		lineNo := i + 1
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "export ") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
		}
		key, value, err := parseDotenvSecretLine(path, lineNo, trimmed)
		if err != nil {
			return dotenvSecretImport{}, err
		}
		if _, seen := out.Values[key]; !seen {
			out.Keys = append(out.Keys, key)
		}
		out.Values[key] = []byte(value)
	}
	sort.Strings(out.Keys)
	return out, nil
}

func parseDotenvSecretLine(path string, lineNo int, line string) (string, string, error) {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return "", "", malformedDotenvError(path, lineNo, "expected KEY=VALUE")
	}
	key := strings.TrimSpace(line[:eq])
	if err := envKeyValid(key); err != nil {
		return "", "", malformedDotenvError(path, lineNo, fmt.Sprintf("invalid key %q; must match ^[A-Za-z_][A-Za-z0-9_]*$", key))
	}
	rawValue := strings.TrimSpace(line[eq+1:])
	value, err := parseDotenvValue(path, lineNo, rawValue)
	if err != nil {
		return "", "", err
	}
	if strings.ContainsRune(value, '\x00') {
		return "", "", malformedDotenvError(path, lineNo, "secret value cannot contain NUL bytes")
	}
	return key, value, nil
}

func parseDotenvValue(path string, lineNo int, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	quote := raw[0]
	if quote != '\'' && quote != '"' {
		return raw, nil
	}
	if len(raw) < 2 || raw[len(raw)-1] != quote {
		return "", malformedDotenvError(path, lineNo, "unterminated quoted value")
	}
	return raw[1 : len(raw)-1], nil
}

func malformedDotenvError(path string, lineNo int, detail string) error {
	if path == "" {
		path = "<input>"
	}
	return errcat.New(errcat.CodeDotenvMalformed, errcat.Fields{
		"detail":  fmt.Sprintf("%s:%d: %s", path, lineNo, detail),
		"command": secretImportCommand(path),
	})
}

func secretImportCommand(path string) string {
	if path == "" || path == "<input>" {
		path = "path/to/.env"
	}
	return "ship secret set --from " + utils.ShellEscape(path)
}

type secretImportSummary struct {
	Set     []string
	Removed []string
}

func applySecretImport(secret secretContext, imported dotenvSecretImport, replace bool, remediation string) (secretImportSummary, error) {
	for _, key := range imported.Keys {
		if err := putSecretValue(secret, key, imported.Values[key], remediation); err != nil {
			return secretImportSummary{}, err
		}
	}
	summary := secretImportSummary{Set: append([]string(nil), imported.Keys...)}
	if !replace {
		return summary, nil
	}
	existing, err := listSecretKeys(secret, remediation)
	if err != nil {
		return secretImportSummary{}, err
	}
	for _, key := range existing {
		if _, keep := imported.Values[key]; keep {
			continue
		}
		if err := removeSecretValue(secret, key, remediation); err != nil {
			return secretImportSummary{}, err
		}
		summary.Removed = append(summary.Removed, key)
	}
	sort.Strings(summary.Removed)
	return summary, nil
}

func putSecretValue(secret secretContext, key string, value []byte, remediation string) error {
	command := serverAppSecretSetCommand(secret.AppContext.AppName, secret.EnvName, key)
	_, err := runSecretCommandWithStdin(secret.Runner, secret.AppContext.Server, command, value, fmt.Sprintf("secret import failed while setting %s", key), remediation)
	return err
}

func removeSecretValue(secret secretContext, key string, remediation string) error {
	command := serverAppSecretRmCommand(secret.AppContext.AppName, secret.EnvName, key)
	_, err := runSecretCommand(secret.Runner, secret.AppContext.Server, command, fmt.Sprintf("secret import failed while removing %s", key), remediation)
	return err
}

func listSecretKeys(secret secretContext, remediation string) ([]string, error) {
	command := serverAppSecretListCommand(secret.AppContext.AppName, secret.EnvName, true)
	stdout, err := runSecretCommand(secret.Runner, secret.AppContext.Server, command, "secret import failed while listing existing keys", remediation)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		return nil, operationError(fmt.Sprintf("secret import failed while parsing existing keys: %v", err), remediation)
	}
	sort.Strings(payload.Keys)
	return payload.Keys, nil
}

func runSecretCommand(runner secretRunner, server, command, errMsg, remediation string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "no error detail", server)
		if outcome.TransportCoded != nil {
			return "", outcome.TransportCoded
		}
		return "", secretRemoteError(outcome, errMsg, remediation)
	}
	return stdout, nil
}

func runSecretCommandWithStdin(runner secretRunner, server, command string, stdin []byte, errMsg, remediation string) (string, error) {
	stdout, stderr, code, err := runner.RunSSHWithStdin(server, command, stdin)
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "no error detail", server)
		if outcome.TransportCoded != nil {
			return "", outcome.TransportCoded
		}
		return "", secretRemoteError(outcome, errMsg, remediation)
	}
	return stdout, nil
}

func secretRemoteError(outcome remoteOutcome, errMsg, remediation string) error {
	if outcome.RemoteCoded != nil {
		writeRemoteStderr(outcome)
		return outcome.RemoteCoded
	}
	if outcome.Detail != "" {
		errMsg += ": " + outcome.Detail
	}
	return operationError(errMsg, remediation)
}

func writeSecretImportSummary(w io.Writer, summary secretImportSummary) {
	if len(summary.Set) > 0 {
		fmt.Fprintf(w, "set: %s\n", strings.Join(summary.Set, ", "))
	}
	if len(summary.Removed) > 0 {
		fmt.Fprintf(w, "removed: %s\n", strings.Join(summary.Removed, ", "))
	}
	fmt.Fprintf(w, "set %d, removed %d\n", len(summary.Set), len(summary.Removed))
	fmt.Fprintln(w, "next: ship")
}

func CmdSecretList(root string, jsonFlag bool, preview bool, branch string) {
	secret, err := currentSecretContext(root, "secret ls", preview, branch, false)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer secret.Runner.Close()

	out := runSSHChecked(secret.Runner, secret.AppContext.Server, serverAppSecretListCommand(secret.AppContext.AppName, secret.EnvName, jsonFlag), "secret list failed", "ship secret ls")
	if jsonFlag {
		fmt.Print(out)
		return
	}
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		// No keys — print nothing rather than an explicit "no
		// secrets" line so the output stays pipeable.
		return
	}
	fmt.Println(out)
}

func CmdSecretRm(root string, key string, preview bool, branch string) {
	secret, err := currentSecretContext(root, "secret rm", preview, branch, false)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer secret.Runner.Close()
	if err := envKeyValid(key); err != nil {
		utils.DieError(err, 1)
	}

	out := runSSHChecked(secret.Runner, secret.AppContext.Server, serverAppSecretRmCommand(secret.AppContext.AppName, secret.EnvName, key), "secret rm failed", "ship secret rm "+key)
	if strings.Contains(out, "was not set") {
		fmt.Printf("Secret %s was not set for %s.\n", key, secret.surface())
		return
	}
	fmt.Printf("Removed secret %s for %s.\n", key, secret.surface())
}

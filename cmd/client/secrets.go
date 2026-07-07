package client

import (
	"fmt"
	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/errcat"
	"github.com/fprl/simple-vps/internal/names"
	"github.com/fprl/simple-vps/internal/utils"
	"io"
	"os"
	"strings"
)

// secretValueFromStdin reads the secret value from this process's
// stdin and trims at most one trailing newline (the kind a tty `read`
// or an `echo` tacks on). Returns the bytes verbatim past that — so
// a multi-line heredoc with intentional newlines comes through
// intact.
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
	Runner     *CommandRunner
	Kind       string
	Branch     string
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

func CmdSecretSet(root string, key string, preview bool, branch string) {
	secret, err := currentSecretContext(root, "secret set", preview, branch, true)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer secret.Runner.Close()
	if err := envKeyValid(key); err != nil {
		utils.DieError(err, 1)
	}
	value, err := secretValueFromStdin()
	if err != nil {
		utils.DieError(err, 1)
	}

	// Pipe the value over the helper's stdin — never argv, never a
	// file on disk between hops. The helper writes it straight to
	// /etc/simple-vps/secrets/<app>/<env>/<key>.
	stdout, stderr, code, err := secret.Runner.RunSSHWithStdin(secret.AppContext.Server, serverAppSecretSetCommand(secret.AppContext.AppName, secret.EnvName, key), value)
	if err != nil || code != 0 {
		remote := extractRemoteError(stdout, stderr, "no error detail")
		if remote.Coded != nil {
			utils.DieError(remote.Coded, 1)
		}
		utils.DieError(operationError(fmt.Sprintf("secret set failed: %s", remote.Detail), "ship secret set "+key), 1)
	}
	// Don't echo stdout — it'd carry the helper's confirmation
	// (which already names the key but not the value). Print our own.
	fmt.Printf("Stored secret %s for %s.\n", key, secret.surface())
	fmt.Fprintln(os.Stderr, "next: ship")
}

func CmdSecretList(root string, jsonFlag bool, preview bool, branch string) {
	secret, err := currentSecretContext(root, "secret ls", preview, branch, false)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer secret.Runner.Close()

	out := runSSHChecked(secret.Runner, secret.AppContext.Server, serverAppSecretListCommand(secret.AppContext.AppName, secret.EnvName, jsonFlag), "secret list failed")
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

	out := runSSHChecked(secret.Runner, secret.AppContext.Server, serverAppSecretRmCommand(secret.AppContext.AppName, secret.EnvName, key), "secret rm failed")
	if strings.Contains(out, "was not set") {
		fmt.Printf("Secret %s was not set for %s.\n", key, secret.surface())
		return
	}
	fmt.Printf("Removed secret %s for %s.\n", key, secret.surface())
}

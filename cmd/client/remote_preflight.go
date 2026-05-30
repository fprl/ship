package client

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/config"
)

func deployRemotePreflight(runner sshRunner, ctx *config.AppContext) error {
	if _, err := runSSHRequired(runner, ctx.Server, "true", fmt.Sprintf("SSH failed for %s", ctx.Server)); err != nil {
		return err
	}
	if _, err := runSSHRequired(runner, ctx.Server, "command -v rsync >/dev/null", "missing required server tool: rsync"); err != nil {
		return err
	}
	stdout, stderr, code, err := runner.RunSSH(ctx.Server, serverAppPreflightCommand(ctx.AppName, ctx.EnvName, secretRefKeys(ctx.SecretRefs)))
	if err == nil && code == 0 {
		return nil
	}
	detail := strings.TrimSpace(stdout)
	if detail == "" {
		detail = strings.TrimSpace(stderr)
	}
	if detail == "" {
		detail = "no error detail"
	}
	return fmt.Errorf("deploy preflight failed:\n%s", detail)
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

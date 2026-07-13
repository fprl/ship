package client

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
)

type deployedReleaseStatus struct {
	Release *struct {
		Release    string `json:"release"`
		BaseCommit string `json:"base_commit"`
	} `json:"release"`
}

func enforceProductionAncestry(root string, runner sshRunner, ctx *config.AppContext, headCommit string) error {
	deployed, ok, err := fetchDeployedCommit(runner, ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	ancestor, err := gitIsAncestor(root, deployed, headCommit)
	if err != nil {
		return behindProductionError(deployed, fmt.Sprintf("could not verify ancestry: %v", err))
	}
	if !ancestor {
		return behindProductionError(deployed, "is not an ancestor of HEAD")
	}
	return nil
}

func fetchDeployedCommit(runner sshRunner, ctx *config.AppContext) (string, bool, error) {
	out, err := runSSHRequired(runner, ctx.Server, serverAppStatusCommand(ctx.AppName, ctx.EnvName), "read deployed release failed", "ship status")
	if err != nil {
		return "", false, err
	}
	var status deployedReleaseStatus
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &status); err != nil {
		return "", false, operationError(fmt.Sprintf("read deployed release failed: invalid status JSON: %v", err), "ship status")
	}
	if status.Release == nil || status.Release.Release == "" {
		return "", false, nil
	}
	if status.Release.BaseCommit == "" {
		return "", false, operationError(fmt.Sprintf("read deployed release failed: active release %s has no base_commit", status.Release.Release), "ship status")
	}
	return status.Release.BaseCommit, true, nil
}

func behindProductionError(deployed, detail string) error {
	return errcat.New(errcat.CodeBehindProduction, errcat.Fields{
		"deployed": shortCommitForDisplay(deployed),
		"detail":   detail,
	})
}

func shortCommitForDisplay(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

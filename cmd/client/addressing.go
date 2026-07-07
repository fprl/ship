package client

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fprl/simple-vps/internal/config"
)

const productionEnvName = "prod"

type gitState struct {
	Branch   string
	Detached bool
}

type deployAddress struct {
	EnvName          string
	Branch           string
	ProductionBranch bool
	Dirty            bool
}

func sanitizeBranchEnvName(branch string) string {
	branch = strings.ToLower(branch)
	var b strings.Builder
	prevDash := false
	for _, r := range branch {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if valid {
			if r == '-' {
				if prevDash {
					continue
				}
				prevDash = true
			} else {
				prevDash = false
			}
			b.WriteRune(r)
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 28 {
		out = out[:28]
	}
	return strings.Trim(out, "-")
}

func resolveDeployAddress(root, explicitEnv, branchFlag string) (deployAddress, error) {
	baseEnv := explicitEnv
	if baseEnv == "" {
		baseEnv = productionEnvName
	}
	baseCtx, err := config.LoadAppContext(root, baseEnv)
	if err != nil {
		return deployAddress{}, err
	}
	state, err := currentGitState(root)
	if err != nil {
		return deployAddress{}, err
	}
	branch, err := deployBranch(state, branchFlag)
	if err != nil {
		return deployAddress{}, err
	}
	envName := explicitEnv
	if envName == "" {
		envName, err = envNameForBranch(branch, baseCtx.ProductionBranch)
		if err != nil {
			return deployAddress{}, err
		}
	}
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		return deployAddress{}, err
	}
	dirty, err := gitWorktreeDirty(root, staticServeDirs(ctx.Routes))
	if err != nil {
		return deployAddress{}, fmt.Errorf("git status failed\nnext: check that Git is installed and this is a valid Git worktree")
	}
	return deployAddress{
		EnvName:          envName,
		Branch:           branch,
		ProductionBranch: branch == baseCtx.ProductionBranch,
		Dirty:            dirty,
	}, nil
}

func resolveReadEnv(root, explicitEnv, branchFlag, command string) (string, error) {
	if explicitEnv != "" {
		return explicitEnv, nil
	}
	baseCtx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		return "", err
	}
	branch := branchFlag
	if branch == "" {
		state, err := currentGitState(root)
		if err != nil {
			return "", err
		}
		if state.Detached {
			return "", detachedHeadRequiresBranchError(command)
		}
		branch = state.Branch
	}
	return envNameForBranch(branch, baseCtx.ProductionBranch)
}

func envNameForBranch(branch, productionBranch string) (string, error) {
	if branch == productionBranch {
		return productionEnvName, nil
	}
	// TODO(§3): append and persist the random 4-char preview suffix on the box.
	envName := sanitizeBranchEnvName(branch)
	if envName == "" {
		return "", codedNextError(
			"unmappable_branch_name",
			fmt.Sprintf("branch %q does not produce a valid environment name", branch),
			"rename the branch",
		)
	}
	return envName, nil
}

func deployBranch(state gitState, branchFlag string) (string, error) {
	if state.Detached {
		if branchFlag == "" {
			return "", detachedHeadRequiresBranchError("deploy")
		}
		return branchFlag, nil
	}
	if branchFlag != "" {
		return "", codedNextError(
			"branch_flag_requires_detached_head",
			"--branch is only accepted on deploy when HEAD is detached",
			"remove --branch or check out the branch before deploying",
		)
	}
	return state.Branch, nil
}

func currentGitState(root string) (gitState, error) {
	insideOut, _, code, _ := runCommand("git", []string{"rev-parse", "--is-inside-work-tree"}, root)
	if code != 0 || strings.TrimSpace(insideOut) != "true" {
		return gitState{}, notAGitRepoError()
	}
	branchOut, _, code, _ := runCommand("git", []string{"symbolic-ref", "--quiet", "--short", "HEAD"}, root)
	if code == 0 {
		branch := strings.TrimSpace(branchOut)
		if branch != "" {
			return gitState{Branch: branch}, nil
		}
	}
	return gitState{Detached: true}, nil
}

func gitIsAncestor(root, ancestor, descendant string) (bool, error) {
	_, stderr, code, _ := runCommand("git", []string{"merge-base", "--is-ancestor", ancestor, descendant}, root)
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "git merge-base failed"
		}
		return false, errors.New(detail)
	}
}

func notAGitRepoError() error {
	return codedNextError(
		"not_a_git_repo",
		"current directory is not inside a Git worktree",
		"git init && git add . && git commit -m \"initial ship app\"",
	)
}

func detachedHeadRequiresBranchError(command string) error {
	return codedNextError(
		"detached_head_requires_branch",
		"HEAD is detached; pass --branch <name> so ship can resolve the environment",
		fmt.Sprintf("ship %s --branch <name>", command),
	)
}

func codedNextError(code, message, next string) error {
	return fmt.Errorf("%s: %s\nnext: %s", code, message, next)
}

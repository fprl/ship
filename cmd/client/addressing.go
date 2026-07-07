package client

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/errcat"
	"github.com/fprl/simple-vps/internal/names"
)

const productionEnvName = "prod"

type gitState struct {
	Branch   string
	Detached bool
}

type deployAddress struct {
	EnvName          string
	Branch           string
	PreviewBranch    string
	ProductionBranch bool
	Dirty            bool
}

type readAddress struct {
	EnvName          string
	PreviewBranch    string
	ProductionBranch bool
}

func resolveDeployAddress(root, branchFlag string) (deployAddress, error) {
	baseCtx, err := config.LoadAppContext(root, productionEnvName)
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
	envName, err := envNameForBranch(branch, baseCtx.ProductionBranch)
	if err != nil {
		return deployAddress{}, err
	}
	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		return deployAddress{}, err
	}
	dirty, err := gitWorktreeDirty(root, staticServeDirs(ctx.Routes))
	if err != nil {
		return deployAddress{}, errcat.New(errcat.CodeOperationFailed, errcat.Fields{
			"detail":  "git status failed; check that Git is installed and this is a valid Git worktree",
			"command": "git status",
		})
	}
	address := deployAddress{
		EnvName:          envName,
		Branch:           branch,
		ProductionBranch: branch == baseCtx.ProductionBranch,
		Dirty:            dirty,
	}
	if !address.ProductionBranch {
		address.PreviewBranch = branch
	}
	return address, nil
}

func resolveReadAddress(root, branchFlag, command string) (readAddress, error) {
	baseCtx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		return readAddress{}, err
	}
	branch := branchFlag
	if branch == "" {
		state, err := currentGitState(root)
		if err != nil {
			return readAddress{}, err
		}
		if state.Detached {
			return readAddress{}, detachedHeadRequiresBranchError(command)
		}
		branch = state.Branch
	}
	envName, err := envNameForBranch(branch, baseCtx.ProductionBranch)
	if err != nil {
		return readAddress{}, err
	}
	if branch == baseCtx.ProductionBranch {
		return readAddress{EnvName: envName, ProductionBranch: true}, nil
	}
	return readAddress{EnvName: envName, PreviewBranch: branch}, nil
}

func envNameForBranch(branch, productionBranch string) (string, error) {
	if branch == productionBranch {
		return productionEnvName, nil
	}
	envName := names.SanitizeBranchEnvName(branch)
	if envName == "" {
		return "", errcat.New(errcat.CodeUnmappableBranchName, errcat.Fields{"branch": fmt.Sprintf("%q", branch)})
	}
	return envName, nil
}

func deployBranch(state gitState, branchFlag string) (string, error) {
	if state.Detached {
		if branchFlag == "" {
			return "", detachedHeadRequiresBranchError("ship")
		}
		return branchFlag, nil
	}
	if branchFlag != "" {
		return "", errcat.New(errcat.CodeBranchFlagRequiresDetachedHead, nil)
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
	return errcat.New(errcat.CodeNotAGitRepo, nil)
}

func detachedHeadRequiresBranchError(command string) error {
	next := fmt.Sprintf("ship %s --branch <name>", command)
	if command == "ship" {
		next = "ship --branch <name>"
	}
	return errcat.New(errcat.CodeDetachedHeadRequiresBranch, errcat.Fields{"command": next})
}

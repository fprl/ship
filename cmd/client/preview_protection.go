package client

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
)

type previewShareResult struct {
	Capability   string
	URL          string
	URLLookupErr error
}

type previewShareContext struct {
	AppContext *config.AppContext
	EnvName    string
	Runner     sshRunner
}

// CmdPreviewShare prints the current Preview's capability URL, optionally
// replacing its token on the helper before printing it.
func CmdPreviewShare(root string, rotate bool) {
	read, err := currentReadContext(root, "preview share")
	if err != nil {
		if errcat.Is(err, errcat.CodeUnknownPreviewBranch) {
			if state, stateErr := currentGitState(root); stateErr == nil && !state.Detached {
				utils.DieError(noPreviewEnvError(state.Branch), 1)
			}
		}
		utils.DieError(err, 1)
	}
	defer read.Runner.Close()
	if read.Address.ProductionBranch {
		utils.DieError(errcat.New(errcat.CodeShareOnProduction, errcat.Fields{"branch": fmt.Sprintf("%q", read.AppContext.ProductionBranch)}), 1)
	}
	result, err := runPreviewShare(previewShareContext{AppContext: read.AppContext, EnvName: read.EnvName, Runner: read.Runner}, rotate)
	if err != nil {
		utils.DieError(err, 1)
	}
	stdout, stderr, err := renderPreviewShareOutput(result, rotate)
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Fprint(os.Stderr, stderr)
	fmt.Print(stdout)
}

func runPreviewShare(read previewShareContext, rotate bool) (previewShareResult, error) {
	out, err := runSSHDetail(read.Runner, read.AppContext.Server, serverAppPreviewShareCommand(read.AppContext.AppName, read.EnvName, rotate), "ship preview share")
	if err != nil {
		return previewShareResult{}, err
	}
	result := previewShareResult{Capability: strings.TrimSpace(out)}
	previewURL, err := liveEnvURL(read.Runner, read.AppContext.Server, read.AppContext.AppName, read.EnvName)
	if err != nil {
		result.URLLookupErr = err
		return result, nil
	}
	result.URL, err = previewShareURL(previewURL, result.Capability)
	if err != nil {
		result.URLLookupErr = err
	}
	return result, nil
}

func renderPreviewShareOutput(result previewShareResult, rotate bool) (string, string, error) {
	if result.URLLookupErr != nil {
		if !rotate {
			return "", "", result.URLLookupErr
		}
		return result.Capability + "\n", fmt.Sprintf("warning: preview URL lookup failed: %v\nnext: ship status\n", result.URLLookupErr), nil
	}
	return result.URL + "\n", "", nil
}

func previewShareURL(previewURL, capability string) (string, error) {
	parsed, err := url.Parse(previewURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", operationError(fmt.Sprintf("preview share failed: invalid preview URL %q", previewURL), "ship preview share")
	}
	query := parsed.Query()
	query.Set("ship", capability)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

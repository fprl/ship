package client

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
)

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
	out, err := runSSHDetail(read.Runner, read.AppContext.Server, serverAppPreviewShareCommand(read.AppContext.AppName, read.EnvName, rotate))
	if err != nil {
		utils.DieError(err, 1)
	}
	previewURL, err := liveEnvURL(read.Runner, read.AppContext.Server, read.AppContext.AppName, read.EnvName)
	if err != nil {
		utils.DieError(err, 1)
	}
	parsed, err := url.Parse(previewURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		utils.DieError(operationError(fmt.Sprintf("preview share failed: invalid preview URL %q", previewURL), "ship preview share"), 1)
	}
	query := parsed.Query()
	query.Set("ship", strings.TrimSpace(out))
	parsed.RawQuery = query.Encode()
	fmt.Println(parsed.String())
}

package client

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
)

// CmdPreviewPassword prints the app-wide team password and bypass token for
// the current Preview. The helper owns generation and rotation; neither value
// is ever sent in argv.
func CmdPreviewPassword(root string, rotate bool) {
	read, err := currentReadContext(root, "preview password")
	if err != nil {
		utils.DieError(err, 1)
	}
	defer read.Runner.Close()
	if read.Address.ProductionBranch {
		utils.DieError(errcat.New(errcat.CodeNoPreviewEnv, errcat.Fields{"branch": fmt.Sprintf("%q", read.AppContext.ProductionBranch)}), 1)
	}
	if !read.AppContext.PreviewProtected {
		utils.DieError(errcat.New(errcat.CodePreviewsNotProtected, nil), 1)
	}
	out, err := runSSHDetail(read.Runner, read.AppContext.Server, serverAppPreviewPasswordCommand(read.AppContext.AppName, read.EnvName, rotate))
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Print(out)
}

// CmdShare mints or revokes the current Preview's capability URL.
func CmdShare(root string, rm bool) {
	read, err := currentReadContext(root, "share")
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
	if !read.AppContext.PreviewProtected {
		utils.DieError(errcat.New(errcat.CodePreviewsNotProtected, nil), 1)
	}
	out, err := runSSHDetail(read.Runner, read.AppContext.Server, serverAppShareCommand(read.AppContext.AppName, read.EnvName, rm))
	if err != nil {
		utils.DieError(err, 1)
	}
	if rm {
		fmt.Print(out)
		return
	}
	previewURL, err := liveEnvURL(read.Runner, read.AppContext.Server, read.AppContext.AppName, read.EnvName)
	if err != nil {
		utils.DieError(err, 1)
	}
	parsed, err := url.Parse(previewURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		utils.DieError(operationError(fmt.Sprintf("share failed: invalid preview URL %q", previewURL), "ship share"), 1)
	}
	query := parsed.Query()
	query.Set("ship_share", strings.TrimSpace(out))
	parsed.RawQuery = query.Encode()
	fmt.Println(parsed.String())
}

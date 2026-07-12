package client

import (
	"fmt"

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

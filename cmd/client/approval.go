package client

import (
	"fmt"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
)

func CmdApprove(server, id string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "fix ship.toml box"}), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	command := serverApprovalListCommand(jsonFlag)
	remediation := "ship approve"
	if strings.TrimSpace(id) != "" {
		command = serverApprovalApproveCommand(id)
		remediation = "ship approve " + id
	}
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		remote := extractRemoteError(stdout, stderr, "")
		if remote.Coded != nil {
			writeRemoteStderr(stderr)
			utils.DieError(remote.Coded, 1)
		}
		detail := remote.Detail
		if detail == "" {
			detail = "approval command failed"
		}
		utils.DieError(operationError(detail, remediation), 1)
	}
	fmt.Print(stdout)
}

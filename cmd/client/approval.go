package client

import (
	"fmt"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/utils"
)

func CmdApprove(server, id string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, invalidBoxTargetManifestRemediation), 2)
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
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "")
		if outcome.TransportCoded != nil {
			utils.DieError(outcome.TransportCoded, 1)
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			utils.DieError(outcome.RemoteCoded, 1)
		}
		detail := outcome.Detail
		if detail == "" {
			detail = "approval command failed"
		}
		utils.DieError(operationError(detail, remediation), 1)
	}
	fmt.Print(stdout)
}

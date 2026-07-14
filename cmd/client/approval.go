package client

import (
	"fmt"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/utils"
)

func CmdBoxApprovalLs(server string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box approval ls"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverApprovalListCommand(jsonFlag))
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
			detail = "approvals failed"
		}
		utils.DieError(operationError(detail, "ship box approval ls "+server), 1)
	}
	fmt.Print(stdout)
}

func CmdBoxApprovalGrant(server, id string) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box approval grant "+id), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverApprovalApproveCommand(id))
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
			detail = "approve failed"
		}
		utils.DieError(operationError(detail, "ship box approval grant "+id+" "+server), 1)
	}
	fmt.Print(stdout)
}

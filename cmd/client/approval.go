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

	stdout, stderr, code, err := runner.RunSSH(server, serverApprovalLsCommand(jsonFlag))
	if err != nil || code != 0 {
		if err := sshResultError(stdout, stderr, code, err, "", "approvals failed", "ship box approval ls "+server); err != nil {
			utils.DieError(err, 1)
		}
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

	stdout, stderr, code, err := runner.RunSSH(server, serverApprovalGrantCommand(id))
	if err != nil || code != 0 {
		if err := sshResultError(stdout, stderr, code, err, "", "grant failed", "ship box approval grant "+id+" "+server); err != nil {
			utils.DieError(err, 1)
		}
	}
	fmt.Print(stdout)
}

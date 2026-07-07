package client

import (
	"encoding/json"
	"fmt"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
	"os"
)

func BoxTarget(root string) (string, error) {
	ctx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		return "", err
	}
	return ctx.Server, nil
}

func CmdBoxLs(server string, jsonFlag bool) {
	if !config.ValidateSshTarget(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "ship box ls deploy@example.com"}), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppListCommand(jsonFlag), "app list failed")
	fmt.Print(out)
}

func CmdBoxDoctor(server string, jsonFlag bool) {
	if !config.ValidateSshTarget(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "ship box doctor deploy@example.com"}), 2)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverDoctorCommand(server, jsonFlag))
	if err != nil || code != 0 {
		remote := extractRemoteError(stdout, stderr, "")
		if remote.Coded != nil {
			utils.DieError(remote.Coded, 1)
		}
		if jsonFlag && json.Valid([]byte(stdout)) {
			fmt.Print(stdout)
			os.Exit(1)
		}
		if remote.Detail != "" {
			utils.DieError(operationError(fmt.Sprintf("failed to run doctor: %s", remote.Detail), "ship box doctor "+server), 1)
		}
		utils.DieError(operationError("failed to run doctor", "ship box doctor "+server), 1)
	}
	fmt.Print(stdout)
}

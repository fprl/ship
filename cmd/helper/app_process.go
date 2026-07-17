package helper

import (
	"fmt"
	"os"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
)

func isEphemeralProcess(process string) bool {
	return process == "release" || process == "exec"
}

type processStartRuntime struct {
	ScrubValues []string
	UserID      string
	GroupID     string
	ImageID     string
	Routed      map[string]bool
	PreviewEnv  bool
	EnvFile     string
}

type startReleaseProcessesParams struct {
	App           string
	Env           string
	Release       string
	Activation    string
	Context       *config.AppContext
	OnlyPortful   bool
	ImageID       string
	EnvFile       string
	ScrubValues   []string
	BeforeStart   func(processStartRuntime) error
	ContainerName func(processName string, proc config.Process) string
}

type startReleaseProcessesResult struct {
	Started     []string
	ProcessName map[string]string
	ScrubValues []string
}

type processStartError struct{ Err error }

func (e processStartError) Error() string {
	return e.Err.Error()
}

func (e processStartError) Unwrap() error {
	return e.Err
}

func startReleaseProcesses(params startReleaseProcessesParams) (startReleaseProcessesResult, error) {
	if len(params.Context.Processes) == 0 {
		return startReleaseProcessesResult{}, fmt.Errorf("manifest must declare at least one [processes.<name>] block")
	}
	result := startReleaseProcessesResult{
		ProcessName: map[string]string{},
	}
	if params.EnvFile == "" {
		return result, fmt.Errorf("resolved activation env file is required")
	}
	if _, err := os.Stat(params.EnvFile); err != nil {
		return result, fmt.Errorf("resolved activation env file is unavailable: %w", err)
	}
	envFile := params.EnvFile
	scrubValues := append([]string(nil), params.ScrubValues...)
	result.ScrubValues = scrubValues

	userID, groupID, err := hostUserIDs(identity.SystemUser(params.App, params.Env))
	if err != nil {
		return result, err
	}
	runtime := processStartRuntime{
		ScrubValues: scrubValues,
		UserID:      userID,
		GroupID:     groupID,
		ImageID:     params.ImageID,
		EnvFile:     envFile,
		Routed:      routedProcessNames(params.Context.Routes),
	}
	runtime.PreviewEnv, err = isPreviewEnv(params.App, params.Env)
	if err != nil {
		return result, err
	}
	if params.BeforeStart != nil {
		if err := params.BeforeStart(runtime); err != nil {
			return result, err
		}
	}

	for _, processName := range sortedKeys(params.Context.Processes) {
		proc := params.Context.Processes[processName]
		if params.OnlyPortful && proc.Port == nil {
			continue
		}
		containerName := identity.ContainerName(params.App, params.Env, processName, params.Release)
		if params.ContainerName != nil {
			containerName = params.ContainerName(processName, proc)
		}
		result.Started = append(result.Started, containerName)
		if proc.Port != nil {
			result.ProcessName[processName] = containerName
		}
		if err := startProcessWithActivation(params.App, params.Env, processName, proc, runtime.ImageID, runtime.UserID, runtime.GroupID, params.Release, params.Activation, containerName, processProbe(runtime.Routed, processName, params.Context.Probe), runtime.PreviewEnv, runtime.ScrubValues, runtime.EnvFile); err != nil {
			return result, processStartError{Err: err}
		}
	}
	result.Started = uniqueContainerNames(result.Started)
	return result, nil
}

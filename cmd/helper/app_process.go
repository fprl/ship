package helper

import (
	"fmt"

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
	ImageTag    string
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
	BeforeStart   func(processStartRuntime) error
	BeforeProcess func(processName string, proc config.Process) error
	ContainerName func(processName string, proc config.Process) string
}

type startReleaseProcessesResult struct {
	Started     []string
	ProcessName map[string]string
	ScrubValues []string
}

type processStartError struct {
	Process string
	Err     error
}

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
	resolved, err := resolveEnv(params.App, params.Env, params.Context.Vars, params.Context.SecretRefs)
	if err != nil {
		return startReleaseProcessesResult{}, err
	}
	scrubValues := collectEnvValues(resolved)
	for key, value := range shipInjectedEnv(params.App, params.Env, params.Release, params.Context) {
		resolved[key] = value
	}
	result := startReleaseProcessesResult{
		ProcessName: map[string]string{},
		ScrubValues: scrubValues,
	}
	envFile, err := writeActivationEnvFile(params.App, params.Env, params.Activation, resolved)
	if err != nil {
		return result, err
	}

	userID, groupID, err := hostUserIDs(identity.SystemUser(params.App, params.Env))
	if err != nil {
		return result, err
	}
	runtime := processStartRuntime{
		ScrubValues: scrubValues,
		UserID:      userID,
		GroupID:     groupID,
		ImageTag:    identity.ImageTag(params.App, params.Env, params.Release),
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
		if params.BeforeProcess != nil {
			if err := params.BeforeProcess(processName, proc); err != nil {
				return result, err
			}
		}
		containerName := identity.ContainerName(params.App, params.Env, processName, params.Release)
		if params.ContainerName != nil {
			containerName = params.ContainerName(processName, proc)
		}
		result.Started = append(result.Started, containerName)
		if proc.Port != nil {
			result.ProcessName[processName] = containerName
		}
		if err := startProcessWithActivation(params.App, params.Env, processName, proc, runtime.ImageTag, runtime.UserID, runtime.GroupID, params.Release, params.Activation, containerName, processProbe(runtime.Routed, processName, params.Context.Probe), runtime.PreviewEnv, runtime.ScrubValues, runtime.EnvFile); err != nil {
			return result, processStartError{Process: processName, Err: err}
		}
	}
	result.Started = uniqueContainerNames(result.Started)
	return result, nil
}

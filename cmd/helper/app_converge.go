package helper

import (
	"fmt"

	"github.com/fprl/ship/internal/config"
)

// convergenceNextStep is intentionally deploy-shaped until the public
// converge verb lands in stage 5.
const convergenceNextStep = "rerun ship"

type convergeResult struct {
	StaleContainers []string
	Degraded        bool
}

type convergeError struct {
	Step string
	Err  error
}

func (e *convergeError) Error() string { return e.Err.Error() }
func (e *convergeError) Unwrap() error { return e.Err }

// convergeActive makes the runtime match active.json. It never changes the
// pointer and never restores an older derived artifact. Cleanup is returned to
// the caller so deploy/rollback can perform it after their success journal.
func convergeActive(app, env string) (convergeResult, error) {
	pointer, err := readActive(app, env)
	if err != nil {
		return convergeResult{}, err
	}
	ctx, cleanup, err := loadAppliedAppContext(app, env)
	if err != nil {
		return convergeResult{}, err
	}
	defer cleanup()
	if err := attachPreviewProtection(app, env, ctx); err != nil {
		return convergeResult{}, err
	}
	if err := addConfiguredPreviewAlias(app, env, ctx); err != nil {
		return convergeResult{}, err
	}
	if ctx.HasStaticRoutes {
		if err := verifyStaticRelease(app, env, pointer.Release, ctx.Routes); err != nil {
			return convergeResult{}, &convergeError{Step: "static", Err: err}
		}
	}

	entries, err := podmanPSContainers(app, env)
	if err != nil {
		return convergeResult{}, err
	}
	processNames := map[string]string{}
	var stale []string
	if ctx.NeedsImage {
		processNames, stale, err = convergeProcesses(app, env, pointer.Release, pointer.Activation, ctx, entries)
		if err != nil {
			return convergeResult{}, err
		}
	}
	if !ctx.NeedsImage {
		stale = staleAppContainers(entries, nil, "", "")
	}

	path := caddyfilePath(app, env)
	if err := renderAndReloadAppCaddy(path, app, env, ctx, pointer.Release, processNames); err != nil {
		return convergeResult{}, &convergeError{Step: "caddy", Err: caddyStageActionError(err, "converge", path)}
	}

	// Routed old containers remain untouched until the target fragment has
	// successfully reloaded. Workers were stopped before their replacement was
	// started; all stale containers are removed only after journaling.
	if len(stale) > 0 {
		if err := stopRunningContainers(entries, stale); err != nil {
			return convergeResult{StaleContainers: uniqueContainerNames(stale)}, &convergeError{Step: "containers", Err: err}
		}
	}
	return convergeResult{StaleContainers: uniqueContainerNames(stale)}, nil
}

func convergeProcesses(app, env, release, activationID string, ctx *config.AppContext, entries []containerEntry) (map[string]string, []string, error) {
	processNames := map[string]string{}
	for _, name := range sortedKeys(ctx.Processes) {
		proc := ctx.Processes[name]
		exact := runningExactProcessContainers(entries, name, release, activationID)
		if len(exact) > 0 {
			processNames[name] = exact[0]
		}
		if proc.Port != nil {
			if len(exact) == 0 {
				started, err := startConvergedProcess(app, env, release, activationID, ctx, name, proc, entries)
				if err != nil {
					return processNames, staleAppContainers(entries, processNames, release, activationID), &convergeError{Step: "process", Err: err}
				}
				processNames[name] = started
			}
			continue
		}

		// Workers are deliberately local and sequential. Every running old
		// instance is stopped before this worker's replacement is started.
		old := runningProcessContainersForActivation(entries, name, release, activationID)
		canonical := ""
		if len(old) > 0 {
			canonical = old[0]
			if _, err := stopContainers(old); err != nil {
				return processNames, staleAppContainers(entries, processNames, release, activationID), &convergeError{Step: "worker", Err: err}
			}
			markContainersExited(entries, old)
		}
		if len(exact) > 0 {
			processNames[name] = exact[0]
			continue
		}
		started, err := startConvergedProcess(app, env, release, activationID, ctx, name, proc, entries)
		if err != nil {
			if canonical != "" {
				if restartErr := startContainers([]string{canonical}); restartErr != nil {
					return processNames, staleAppContainers(entries, processNames, release, activationID), &convergeError{Step: "worker", Err: fmt.Errorf("new worker %s failed to start; degraded: %v; old worker restart failed: %w", name, err, restartErr)}
				}
				return processNames, staleAppContainers(entries, processNames, release, activationID), &convergeError{Step: "worker", Err: fmt.Errorf("new worker %s failed to start; degraded: %v; old worker restarted", name, err)}
			}
			return processNames, staleAppContainers(entries, processNames, release, activationID), &convergeError{Step: "worker", Err: err}
		}
		processNames[name] = started
	}
	return processNames, staleAppContainers(entries, processNames, release, activationID), nil
}

func startConvergedProcess(app, env, release, activationID string, ctx *config.AppContext, name string, proc config.Process, entries []containerEntry) (string, error) {
	copyCtx := *ctx
	copyCtx.Processes = map[string]config.Process{name: proc}
	started, err := startReleaseProcesses(startReleaseProcessesParams{
		App: app, Env: env, Release: release, Activation: activationID, Context: &copyCtx,
		ContainerName: func(string, config.Process) string {
			return nextProcessContainerName(entries, app, env, name, release, "converge")
		},
	})
	if err != nil {
		return "", err
	}
	if started.ProcessName[name] != "" {
		return started.ProcessName[name], nil
	}
	if len(started.Started) > 0 {
		return started.Started[0], nil
	}
	return "", fmt.Errorf("process %s did not start a container", name)
}

func containsName(names []string, wanted string) bool {
	for _, name := range names {
		if name == wanted {
			return true
		}
	}
	return false
}

func runningProcessContainer(entries []containerEntry, process, release string) string {
	for _, entry := range entries {
		if entry.Labels["ship.process"] != process || entry.Labels["ship.release"] != release || entry.State != "running" {
			continue
		}
		if len(entry.Names) > 0 {
			return entry.Names[0]
		}
	}
	return ""
}

func runningExactProcessContainers(entries []containerEntry, process, release, activationID string) []string {
	var names []string
	for _, entry := range entries {
		if entry.Labels["ship.process"] == process && entry.Labels["ship.release"] == release && entry.Labels["ship.activation"] == activationID && entry.State == "running" && len(entry.Names) > 0 {
			names = append(names, entry.Names[0])
		}
	}
	return uniqueContainerNames(names)
}

func runningProcessContainers(entries []containerEntry, process, release string) []string {
	return runningProcessContainersForActivation(entries, process, release, "")
}

func runningProcessContainersForActivation(entries []containerEntry, process, release, activationID string) []string {
	var names []string
	for _, entry := range entries {
		if entry.Labels["ship.process"] != process || entry.State != "running" {
			continue
		}
		if release != "" && entry.Labels["ship.release"] == release && (activationID == "" || entry.Labels["ship.activation"] == activationID) {
			continue
		}
		if len(entry.Names) > 0 {
			names = append(names, entry.Names[0])
		}
	}
	return uniqueContainerNames(names)
}

func markContainersExited(entries []containerEntry, names []string) {
	for i := range entries {
		if len(entries[i].Names) > 0 && containsName(names, entries[i].Names[0]) {
			entries[i].State = "exited"
		}
	}
}

func staleAppContainers(entries []containerEntry, desired map[string]string, release, activationID string) []string {
	var stale []string
	for _, entry := range entries {
		process := entry.Labels["ship.process"]
		if process == "" || isEphemeralProcess(process) || len(entry.Names) == 0 {
			continue
		}
		wanted := desired[process]
		if wanted == "" || entry.Labels["ship.release"] != release || entry.Labels["ship.activation"] != activationID || entry.Names[0] != wanted {
			stale = append(stale, entry.Names[0])
		}
	}
	return uniqueContainerNames(stale)
}

func stopRunningContainers(entries []containerEntry, names []string) error {
	running := map[string]bool{}
	for _, entry := range entries {
		if entry.State == "running" && len(entry.Names) > 0 {
			running[entry.Names[0]] = true
		}
	}
	var toStop []string
	for _, name := range names {
		if running[name] {
			toStop = append(toStop, name)
		}
	}
	if len(toStop) == 0 {
		return nil
	}
	_, err := stopContainers(uniqueContainerNames(toStop))
	return err
}

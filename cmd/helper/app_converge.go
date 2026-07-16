package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
)

const convergenceNextStep = "ship converge"

type convergeResult struct {
	StaleContainers []string
	Changed         bool
}

type activePointerReadError struct{ Err error }

func (e *activePointerReadError) Error() string { return e.Err.Error() }
func (e *activePointerReadError) Unwrap() error { return e.Err }

type convergeError struct {
	Step string
	Err  error
}

type appConvergeCmd struct {
	App  string `arg:"" help:"App name."`
	Env  string `arg:"" help:"Env name."`
	JSON bool   `name:"json" help:"Emit a structured convergence summary."`
}

type appConvergeSummary struct {
	App             string   `json:"app"`
	Env             string   `json:"env"`
	Release         string   `json:"release,omitempty"`
	Outcome         string   `json:"outcome"`
	StaleContainers []string `json:"stale_containers,omitempty"`
	Error           string   `json:"error,omitempty"`
}

var appendConvergeJournal = appendDeployJournalEntry
var convergeActiveForCommand = convergeActive

func (c appConvergeCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbShip, authTargetForAppEnv(c.App, c.Env, "converge"))
	var summary appConvergeSummary
	var runErr error
	withAppEnvLock(c.App, c.Env, func() {
		summary, runErr = c.runLocked()
	})
	if c.JSON {
		buf, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(buf))
	} else if runErr == nil {
		if summary.Release == "" {
			fmt.Printf("Converged %s (%s)\n", c.App, c.Env)
		} else {
			fmt.Printf("Converged %s (%s) at %s\n", c.App, c.Env, summary.Release)
		}
	}
	if runErr != nil {
		if errcat.Is(runErr, errcat.CodeNoDeploys) {
			utils.DieError(runErr, 1)
		}
		var pointerErr *activePointerReadError
		if errors.As(runErr, &pointerErr) {
			utils.DieError(runErr, 1)
		}
		utils.DieError(fmt.Errorf("committed but not converged; %s: %w", convergenceNextStep, runErr), 1)
	}
	return nil
}

func (c appConvergeCmd) runLocked() (appConvergeSummary, error) {
	startedAt := time.Now().UTC()
	summary := appConvergeSummary{App: c.App, Env: c.Env}
	if pointer, err := readActive(c.App, c.Env); err == nil {
		summary.Release = pointer.Release
	} else {
		if os.IsNotExist(err) {
			err = fmt.Errorf("nothing deployed yet: %w", noDeployJournalError(c.App, c.Env))
			summary.Error = err.Error()
			summary.Outcome = "no_deploys"
			return summary, err
		}
		err = &activePointerReadError{Err: fmt.Errorf("cannot determine committed state: read active.json: %w", err)}
		summary.Error = err.Error()
		summary.Outcome = "active_pointer_unreadable"
		return summary, err
	}
	result, err := convergeActiveForCommand(c.App, c.Env)
	summary.StaleContainers = result.StaleContainers
	if err != nil {
		summary.Outcome = "committed_unconverged"
		summary.Error = err.Error()
		journalErr := c.appendJournal(startedAt, summary, err)
		return summary, errors.Join(err, journalErr)
	}
	summary.Outcome = "converged"
	if !result.Changed && len(result.StaleContainers) == 0 {
		return summary, nil
	}
	if journalErr := c.appendJournal(startedAt, summary, nil); journalErr != nil {
		return summary, journalErr
	}
	removeContainers(result.StaleContainers)
	return summary, nil
}

func (c appConvergeCmd) appendJournal(startedAt time.Time, summary appConvergeSummary, convergeErr error) error {
	entry := deployJournalEntry{
		SchemaVersion:    deployJournalSchemaVersion,
		App:              c.App,
		Env:              c.Env,
		Outcome:          summary.Outcome,
		StartedAt:        startedAt.Format(time.RFC3339Nano),
		EndedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		AttemptedRelease: summary.Release,
		FailingStep:      "",
		Identity:         deployActor("", ""),
		Member:           currentServerMemberForJournal(),
	}
	if convergeErr != nil {
		entry.FailingStep = "converge"
		if stepErr, ok := convergeErr.(*convergeError); ok {
			entry.FailingStep = stepErr.Step
		}
		entry.StderrTail = convergeErr.Error()
	}
	return appendConvergeJournal(c.App, c.Env, entry, nil)
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
	ctx, cleanup, err := loadActiveEnvelopeContext(app, env)
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
	if len(stale) == 0 && caddyAlreadyConverged(path, app, env, ctx, pointer.Release, processNames) {
		return convergeResult{}, nil
	}
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
	return convergeResult{StaleContainers: uniqueContainerNames(stale), Changed: true}, nil
}

func caddyAlreadyConverged(path, app, env string, ctx *config.AppContext, release string, processNames map[string]string) bool {
	content, err := renderAppCaddyfileWithProcessNames(app, env, ctx, release, processNames)
	if err != nil {
		return false
	}
	fragment, err := os.ReadFile(path)
	return err == nil && string(fragment) == content && caddyReloadReceiptMatches(path, []byte(content))
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

func runningExactProcessContainers(entries []containerEntry, process, release, activationID string) []string {
	var names []string
	for _, entry := range entries {
		if entry.Labels["ship.process"] == process && entry.Labels["ship.release"] == release && entry.Labels["ship.activation"] == activationID && entry.State == "running" && len(entry.Names) > 0 {
			names = append(names, entry.Names[0])
		}
	}
	return uniqueContainerNames(names)
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

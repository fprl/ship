package helper

import (
	"fmt"
	"strings"

	"github.com/fprl/simple-vps/internal/utils"
)

// appRestartCmd bounces the running containers for one (app, env) pair
// in place via `podman restart`. Container config is preserved — same
// image, same env vars, same mounts, same labels; the only effect is
// the process inside is re-execed. To pick up manifest changes, use
// `ship deploy`.
//
// If a process argument is given, only that one bounces. Otherwise
// every container with matching app/env labels gets restarted, one at
// a time so a half-broken process can't hide behind a "restarted"
// summary. After each restart the helper re-queries `podman ps` and
// fails fast if the container didn't come back to `running` — the
// user finds out at restart time, not the next time they look at
// `ship status`.
//
// Read-modify-write of container state happens on the host with no
// new container started; the rolling order matches what `app apply`
// uses per ADR-0006 Cut 1.
type appRestartCmd struct {
	App     string `arg:"" help:"App name."`
	Env     string `arg:"" help:"Env name."`
	Process string `arg:"" optional:"" help:"Process to bounce. Omitted = all processes."`
}

func (c appRestartCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked()
	})
	return nil
}

func (c appRestartCmd) runLocked() {
	targets, err := resolveRestartTargets(c.App, c.Env, c.Process)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	results := make([]processStatus, 0, len(targets))
	for _, t := range targets {
		if _, err := utils.RunChecked("podman", []string{"restart", t.Container}, ""); err != nil {
			utils.Die(fmt.Sprintf("podman restart %s: %v", t.Container, err), 1)
		}
		// Verify the container came back. `podman restart` exits 0 the
		// instant it sends the stop signal; we want to know the
		// process re-spawned and didn't immediately crash-loop into
		// `exited`.
		post, err := podmanPSContainers(c.App, c.Env)
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		state := postRestartState(post, t.Container)
		if state != "running" {
			utils.Die(fmt.Sprintf("process %s did not return to running after restart (state=%s)", t.Process, state), 1)
		}
		results = append(results, processStatus{
			Process:   t.Process,
			Container: t.Container,
			State:     state,
			Image:     t.Image,
			Release:   t.Release,
		})
	}

	fmt.Print(renderRestartText(c.App, c.Env, results))
}

// resolveRestartTargets finds the labelled (app, env) processes and
// optionally narrows to one by name. Refuses to run when nothing is
// running for the (app, env) — that's the "use `deploy` to recreate"
// case and surfacing it as a clear error beats a silent no-op.
func resolveRestartTargets(app, env, process string) ([]processStatus, error) {
	entries, err := podmanPSContainers(app, env)
	if err != nil {
		return nil, err
	}
	return pickRestartTargets(app, env, process, containersToProcesses(entries))
}

// pickRestartTargets is the pure-function half of resolveRestartTargets:
// given the processes that `podman ps` returned, narrow to the one the
// user asked for (or hand back all of them, in deterministic order).
// Split out so tests can exercise the filter logic without mocking out
// the `podman ps` shell-out; the latter is covered by the smoke.
func pickRestartTargets(app, env, process string, processes []processStatus) ([]processStatus, error) {
	running := runningProcesses(processes)
	if len(running) == 0 {
		return nil, fmt.Errorf("no processes running for %s (%s)", app, env)
	}
	if process != "" {
		for _, s := range running {
			if s.Process == process {
				return []processStatus{s}, nil
			}
		}
		return nil, fmt.Errorf("no running process %q for %s (%s)", process, app, env)
	}
	// `containersToProcesses` already sorts; restate the assumption so
	// the rolling order is obvious at the call site.
	return running, nil
}

// postRestartState looks up one container by name in a fresh ps dump
// and returns its current State. "missing" if the container vanished
// (someone raced a `rm`); anything else is podman's verbatim string.
func postRestartState(entries []containerEntry, containerName string) string {
	for _, e := range entries {
		for _, n := range e.Names {
			if n == containerName {
				return e.State
			}
		}
	}
	return "missing"
}

func renderRestartText(app, env string, results []processStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", app, env)
	if len(results) == 0 {
		b.WriteString("  no processes restarted\n")
		return b.String()
	}
	for _, s := range results {
		fmt.Fprintf(&b, "  %-12s restarted (%s)\n", s.Process, s.State)
	}
	return b.String()
}

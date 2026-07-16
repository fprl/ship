package helper

import (
	"errors"
	"fmt"
	"os"
)

type convergeBootCmd struct{}

var (
	bootEnvs     = identityAppEnvs
	bootConverge = convergeActive
	bootLog      = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
)

func (c convergeBootCmd) BeforeApply() error { return requireRoot() }

func (c convergeBootCmd) Run() error {
	if err := runBootConvergence(); err != nil {
		return err
	}
	return nil
}

func runBootConvergence() error {
	envs, err := bootEnvs()
	if err != nil {
		return fmt.Errorf("enumerate app envs for boot convergence: %w", err)
	}
	var failures []error
	for _, item := range envs {
		lock, lockErr := acquireAppEnvLock(item.App, item.Env)
		if lockErr != nil {
			bootLog("boot convergence failed for %s (%s): %v", item.App, item.Env, lockErr)
			failures = append(failures, fmt.Errorf("%s (%s): %w", item.App, item.Env, lockErr))
			continue
		}
		result, convergeErr := bootConverge(item.App, item.Env)
		_ = lock.Release()
		if convergeErr != nil {
			bootLog("boot convergence failed for %s (%s): %v", item.App, item.Env, convergeErr)
			failures = append(failures, fmt.Errorf("%s (%s): %w", item.App, item.Env, convergeErr))
			continue
		}
		removeContainers(result.StaleContainers)
		if result.Changed || len(result.StaleContainers) > 0 {
			bootLog("boot convergence complete for %s (%s)", item.App, item.Env)
		} else {
			bootLog("boot convergence already current for %s (%s)", item.App, item.Env)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("boot convergence failed for %d environment(s): %w", len(failures), errors.Join(failures...))
	}
	return nil
}

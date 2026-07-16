package helper

import (
	"fmt"
	"os"
)

type convergeBootCmd struct{}

var (
	bootEnvs          = identityAppEnvs
	bootConverge      = convergeActive
	bootLog           = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
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
	for _, item := range envs {
		lock, lockErr := acquireAppEnvLock(item.App, item.Env)
		if lockErr != nil {
			bootLog("boot convergence failed for %s (%s): %v", item.App, item.Env, lockErr)
			continue
		}
		_, convergeErr := bootConverge(item.App, item.Env)
		_ = lock.Release()
		if convergeErr != nil {
			bootLog("boot convergence failed for %s (%s): %v", item.App, item.Env, convergeErr)
			continue
		}
		bootLog("boot convergence complete for %s (%s)", item.App, item.Env)
	}
	return nil
}

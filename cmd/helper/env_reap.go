package helper

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

type envCmd struct {
	Reap envReapCmd `cmd:"reap" help:"Destroy expired unpinned preview environments."`
}

func (c envCmd) BeforeApply() error {
	return requireRoot()
}

type envReapCmd struct{}

type destroyEnvFunc func(app, env string, purge bool) (destroySummary, error)

func (envReapCmd) Run() error {
	count, err := reapExpiredPreviews(time.Now().UTC(), destroyEnv)
	if err != nil {
		utils.DieError(err, 1)
	}
	if count == 0 {
		fmt.Println("No expired preview envs.")
	}
	return nil
}

func reapExpiredPreviews(now time.Time, destroy destroyEnvFunc) (int, error) {
	return reapExpiredPreviewsWithLock(now, destroy, acquireAppEnvLock)
}

func reapExpiredPreviewsWithLock(now time.Time, destroy destroyEnvFunc, acquire func(app, env string) (*appEnvLock, error)) (int, error) {
	files, err := allPreviewIdentities()
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, file := range files {
		if file.Env == productionEnvName || file.Preview == nil || file.Preview.Pinned || file.Preview.ExpiresAt == nil {
			continue
		}
		if file.Preview.ExpiresAt.After(now) {
			continue
		}
		lock, err := acquire(file.App, file.Env)
		if err != nil {
			return reaped, err
		}
		refreshed, err := readEnvIdentity(file.App, file.Env)
		if err != nil {
			releaseErr := lock.Release()
			if releaseErr != nil {
				return reaped, fmt.Errorf("release lock for %s (%s): %v", file.App, file.Env, releaseErr)
			}
			if os.IsNotExist(err) {
				continue
			}
			return reaped, err
		}
		if refreshed.Env == productionEnvName || refreshed.Preview == nil || refreshed.Preview.Pinned || refreshed.Preview.ExpiresAt == nil || refreshed.Preview.ExpiresAt.After(now) {
			if err := lock.Release(); err != nil {
				return reaped, fmt.Errorf("release lock for %s (%s): %v", file.App, file.Env, err)
			}
			continue
		}
		file = refreshed
		notifyURL := ""
		release := latestSuccessfulRelease(file.App, file.Env)
		if ctx, cleanup, err := loadAppliedAppContext(file.App, file.Env); err == nil {
			notifyURL = ctx.Notify
			cleanup()
		}
		_, destroyErr := destroy(file.App, file.Env, true)
		releaseErr := lock.Release()
		if destroyErr != nil {
			return reaped, destroyErr
		}
		if releaseErr != nil {
			return reaped, fmt.Errorf("release lock for %s (%s): %v", file.App, file.Env, releaseErr)
		}
		reaped++
		fmt.Printf("Reaped preview %s (%s) branch=%s expired_at=%s\n",
			file.App,
			file.Env,
			file.Preview.Branch,
			file.Preview.ExpiresAt.UTC().Format(time.RFC3339),
		)
		notifyPreviewReaped(notifyURL, file, release, now)
	}
	return reaped, nil
}

func allPreviewIdentities() ([]identity.EnvIdentity, error) {
	paths, err := filepath.Glob(identityGlob())
	if err != nil {
		return nil, err
	}
	var out []identity.EnvIdentity
	for _, path := range paths {
		file, err := readEnvIdentityFile(path)
		if err != nil {
			return nil, err
		}
		if file.Env == productionEnvName || file.Preview == nil {
			continue
		}
		if err := validatePreviewIdentity(file); err != nil {
			return nil, fmt.Errorf("invalid preview identity %s: %v", path, err)
		}
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].Env < out[j].Env
	})
	return out, nil
}

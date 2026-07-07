package helper

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/utils"
)

type envCmd struct {
	Reap envReapCmd `cmd:"reap" help:"Destroy expired unpinned preview environments."`
}

type envReapCmd struct{}

type destroyEnvFunc func(app, env string, purge bool) (destroySummary, error)

func (envReapCmd) Run() error {
	count, err := reapExpiredPreviews(time.Now().UTC(), destroyEnv)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if count == 0 {
		fmt.Println("No expired preview envs.")
	}
	return nil
}

func reapExpiredPreviews(now time.Time, destroy destroyEnvFunc) (int, error) {
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
		lock, err := acquireAppEnvLock(file.App, file.Env)
		if err != nil {
			return reaped, err
		}
		summary, destroyErr := destroy(file.App, file.Env, true)
		releaseErr := lock.Release()
		if destroyErr != nil {
			return reaped, destroyErr
		}
		if releaseErr != nil {
			return reaped, fmt.Errorf("release lock for %s (%s): %v", file.App, file.Env, releaseErr)
		}
		_ = summary
		reaped++
		fmt.Printf("Reaped preview %s (%s) branch=%s expired_at=%s\n",
			file.App,
			file.Env,
			file.Preview.Branch,
			file.Preview.ExpiresAt.UTC().Format(time.RFC3339),
		)
		// TODO(§7): fire notify webhook for reaped preview events.
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

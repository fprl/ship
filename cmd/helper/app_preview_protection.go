package helper

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/secrets"
	"github.com/fprl/ship/internal/utils"
)

const (
	previewProtectionNamespace = "_preview-protection"
	previewPasswordKey         = "TEAM_PASSWORD"
	previewBypassTokenKey      = "BYPASS_TOKEN"
)

type previewProtectionCredentials struct {
	Password    string
	BypassToken string
}

type appPreviewPasswordCmd struct {
	App    string `arg:"" help:"App name."`
	Env    string `arg:"" help:"A live preview environment."`
	Rotate bool   `name:"rotate" help:"Generate a new team password; the bypass token stays unchanged."`
}

func (c appPreviewPasswordCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if envClassForAuth(c.App, c.Env) != "preview" {
		utils.DieError(errcat.New(errcat.CodeNoPreviewEnv, errcat.Fields{"branch": "current branch"}), 1)
	}
	authorizeOrDie(helperVerbPreviewPassword, authTargetForAppEnv(c.App, c.Env, "preview-password"))

	lock, err := acquireAppNamedLock(c.App, "preview-protection")
	if err != nil {
		utils.DieError(err, 1)
	}
	defer func() { _ = lock.Release() }()

	app, cleanup, err := loadAppliedAppContext(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer cleanup()
	if !app.PreviewProtected {
		utils.DieError(errcat.New(errcat.CodePreviewsNotProtected, nil), 1)
	}
	credentials, err := ensurePreviewProtectionCredentials(c.App)
	if err != nil {
		utils.DieError(err, 1)
	}
	if c.Rotate {
		credentials.Password, err = generatePreviewCredential(24)
		if err != nil {
			utils.DieError(err, 1)
		}
		if err := secrets.PutApp(c.App, previewProtectionNamespace, previewPasswordKey, []byte(credentials.Password)); err != nil {
			utils.DieError(err, 1)
		}
		if err := rerenderProtectedPreviews(c.App, credentials); err != nil {
			utils.DieError(err, 1)
		}
	}
	fmt.Printf("team password: %s\nbypass token: %s\n", credentials.Password, credentials.BypassToken)
	return nil
}

// attachPreviewProtection generates app-wide credentials on the first
// protected preview apply. Production never loads credentials or auth config.
func attachPreviewProtection(appName, env string, app *config.AppContext) error {
	if env == productionEnvName || !app.PreviewProtected {
		return nil
	}
	credentials, err := ensurePreviewProtectionCredentials(appName)
	if err != nil {
		return err
	}
	app.PreviewPassword = credentials.Password
	app.PreviewBypassToken = credentials.BypassToken
	shareToken, err := previewShareToken(appName, env)
	if err != nil {
		return err
	}
	app.PreviewShareToken = shareToken
	return nil
}

func previewShareToken(app, env string) (string, error) {
	value, err := secrets.GetShareToken(app, env)
	if errors.Is(err, secrets.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func ensurePreviewProtectionCredentials(app string) (previewProtectionCredentials, error) {
	password, passwordErr := secrets.GetApp(app, previewProtectionNamespace, previewPasswordKey)
	if passwordErr != nil && !errors.Is(passwordErr, secrets.ErrNotFound) {
		return previewProtectionCredentials{}, passwordErr
	}
	bypass, bypassErr := secrets.GetApp(app, previewProtectionNamespace, previewBypassTokenKey)
	if bypassErr != nil && !errors.Is(bypassErr, secrets.ErrNotFound) {
		return previewProtectionCredentials{}, bypassErr
	}
	credentials := previewProtectionCredentials{Password: string(password), BypassToken: string(bypass)}
	if errors.Is(passwordErr, secrets.ErrNotFound) || credentials.Password == "" {
		value, err := generatePreviewCredential(24)
		if err != nil {
			return previewProtectionCredentials{}, err
		}
		if err := secrets.PutApp(app, previewProtectionNamespace, previewPasswordKey, []byte(value)); err != nil {
			return previewProtectionCredentials{}, err
		}
		credentials.Password = value
	}
	if errors.Is(bypassErr, secrets.ErrNotFound) || credentials.BypassToken == "" {
		value, err := generatePreviewCredential(32)
		if err != nil {
			return previewProtectionCredentials{}, err
		}
		if err := secrets.PutApp(app, previewProtectionNamespace, previewBypassTokenKey, []byte(value)); err != nil {
			return previewProtectionCredentials{}, err
		}
		credentials.BypassToken = value
	}
	return credentials, nil
}

func generatePreviewCredential(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate preview credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// rerenderProtectedPreviews updates every live protected preview after a
// password rotation; the bypass token is reused, so CI callers keep
// working. It keeps going past individual failures:
// stopping at the first error would strand the remaining previews on
// the old password. A preview reaped between listing and locking is
// gone, not broken — skip it.
func rerenderProtectedPreviews(appName string, credentials previewProtectionCredentials) error {
	envs, err := identityAppEnvs()
	if err != nil {
		return err
	}
	var failures []string
	for _, item := range envs {
		if item.App != appName || item.Preview == nil {
			continue
		}
		lock, err := acquireAppEnvLock(item.App, item.Env)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", item.Env, err))
			continue
		}
		err = rerenderProtectedPreviewLocked(item.App, item.Env, credentials)
		unlockErr := lock.Release()
		if err != nil {
			if _, statErr := os.Stat(identity.EnvRoot(item.App, item.Env)); os.IsNotExist(statErr) {
				continue
			}
			failures = append(failures, fmt.Sprintf("%s: %v", item.Env, err))
			continue
		}
		if unlockErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", item.Env, unlockErr))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("rerender protected previews (rerun to converge): %s", strings.Join(failures, "; "))
	}
	return nil
}

func rerenderProtectedPreviewLocked(appName, env string, credentials previewProtectionCredentials) error {
	app, cleanup, err := loadAppliedAppContext(appName, env)
	if err != nil {
		return err
	}
	defer cleanup()
	if !app.PreviewProtected {
		return nil
	}
	release, err := activeRelease(appName, env, app)
	if err != nil {
		return err
	}
	processes, err := podmanPSContainers(appName, env)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, process := range runningProcesses(containersToProcesses(processes)) {
		if process.Release == release && process.Container != "" {
			names[process.Process] = process.Container
		}
	}
	app.PreviewPassword = credentials.Password
	app.PreviewBypassToken = credentials.BypassToken
	app.PreviewShareToken, err = previewShareToken(appName, env)
	if err != nil {
		return err
	}
	path := caddyfilePath(appName, env)
	previous, existed, err := snapshotCaddyFragment(path)
	if err != nil {
		return err
	}
	if err := writeAppCaddyfileWithProcessNames(appName, env, app, release, names); err != nil {
		return err
	}
	if err := reloadCaddyOrRestore(path, previous, existed); err != nil {
		return caddyStageActionError(err, "updating preview protection", path)
	}
	return nil
}

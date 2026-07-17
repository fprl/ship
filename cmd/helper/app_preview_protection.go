package helper

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/secrets"
)

func generatePreviewCredential(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate preview credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// attachPreviewProtection only runs for a preview env. The cookie carries the
// current token itself, so changing this token invalidates every old cookie.
func attachPreviewProtection(appName, env string, app *config.AppContext) error {
	if env == productionEnvName {
		return nil
	}
	token, err := ensurePreviewCapability(appName, env)
	if err != nil {
		return err
	}
	app.PreviewCapabilityToken = token
	return nil
}

func rerenderPreviewCapabilityLocked(appName, env string) error {
	app, tuple, err := resolveActiveContext(appName, env)
	if err != nil {
		return err
	}
	if env == productionEnvName {
		return nil
	}
	if err := attachPreviewProtection(appName, env, app); err != nil {
		return err
	}
	if err := addConfiguredPreviewAlias(appName, env, app); err != nil {
		return err
	}
	release := tuple.Release
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
	path := caddyfilePath(appName, env)
	if err := renderAndReloadAppCaddy(path, appName, env, app, release, names); err != nil {
		return caddyStageActionError(err, "updating preview capability")
	}
	return nil
}

func previewCapability(app, env string) (string, error) {
	value, err := secrets.GetPreviewCapability(app, env)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

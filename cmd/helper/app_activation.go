package helper

import (
	"bytes"
	"fmt"
	"os"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

func readActive(app, env string) (activation.Pointer, error) {
	pointer, err := activation.Read(app, env)
	if err != nil && os.IsNotExist(err) {
		return activation.Pointer{}, errcat.WithCause(noDeployJournalError(app, env), "nothing deployed yet")
	}
	return pointer, err
}

func writeActive(app, env string, pointer activation.Pointer) error {
	return activation.WritePrepared(app, env, pointer, func(tempPath string) error {
		if _, err := utils.RunChecked("chown", []string{"root:root", tempPath}, ""); err != nil {
			return fmt.Errorf("chown active pointer: %v", err)
		}
		return nil
	})
}

func activeEnvFile(app, env string) (string, error) {
	pointer, err := readActive(app, env)
	if err != nil {
		return "", err
	}
	path := identity.ActivationEnvFile(app, env, pointer.Activation)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("frozen environment for active activation %s is gone: %v; next: ship", pointer.Activation, err)
	}
	return path, nil
}

func writeActivationEnvFile(app, env, activationID string, values map[string]string) (string, error) {
	path := identity.ActivationEnvFile(app, env, activationID)
	data := []byte(renderEnvFile(values))
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, data) {
			return "", fmt.Errorf("activation env file %s is immutable", activationID)
		}
		// Existing activation files are immutable in content, not in their
		// security properties. Re-apply both properties so a chmod/chown
		// drift is repaired instead of being accepted by an early return.
		if err := os.Chmod(path, 0600); err != nil {
			return "", fmt.Errorf("chmod activation env file: %v", err)
		}
		user := identity.SystemUser(app, env)
		if _, err := utils.RunChecked("chown", []string{user + ":" + user, path}, ""); err != nil {
			return "", fmt.Errorf("chown activation env file: %v", err)
		}
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := store.AtomicWrite(path, data, 0600); err != nil {
		return "", err
	}
	user := identity.SystemUser(app, env)
	if _, err := utils.RunChecked("chown", []string{user + ":" + user, path}, ""); err != nil {
		return "", fmt.Errorf("chown activation env file: %v", err)
	}
	return path, nil
}

func newActivationID(app, env, release string) (string, error) {
	for range 8 {
		id, err := identity.NewActivationID(release)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(identity.ActivationEnvFile(app, env, id)); os.IsNotExist(err) {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique activation for %s", release)
}

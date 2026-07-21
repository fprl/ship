package helper

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

func readActive(app, env string) (activationrecords.Pointer, error) {
	pointer, err := activationrecords.Read(app, env)
	if err != nil && os.IsNotExist(err) {
		return activationrecords.Pointer{}, errcat.WithCause(noDeployJournalError(app, env), "nothing deployed yet")
	}
	return pointer, err
}

func writeActive(app, env string, pointer activationrecords.Pointer) error {
	return activationrecords.Publish(app, env, pointer)
}

func writeActivationEnvFile(app, env, activationID string, values map[string]string) (string, error) {
	path := identity.ActivationEnvFile(app, env, activationID)
	data := []byte(renderEnvFile(values))
	if err := os.MkdirAll(identity.ActivationsDir(app, env), 0755); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	info, statErr := file.Stat()
	if statErr != nil || info.Size() != 0 {
		_ = file.Close()
		return "", fmt.Errorf("activation env file %s already contains data", activationID)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	user := identity.SystemUser(app, env)
	if _, err := utils.RunChecked("chown", []string{user + ":" + user, path}, ""); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("chown activation env file: %v", err)
	}
	return path, nil
}

func newActivationID(app, env, release string) (string, error) {
	id, err := identity.NewActivationID(release)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(identity.ActivationsDir(app, env), 0755); err != nil {
		return "", err
	}
	path := identity.ActivationEnvFile(app, env, id)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return "", fmt.Errorf("activation id collision for %s", release)
		}
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return id, nil
}

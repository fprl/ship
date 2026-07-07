package helper

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/utils"
)

const defaultAppEnvLockDir = "/run/simple-vps/locks"

type appEnvLock struct {
	file *os.File
}

func appEnvLockDir() string {
	if dir := os.Getenv("SHIP_LOCK_DIR"); dir != "" {
		return dir
	}
	return defaultAppEnvLockDir
}

func appEnvLockPath(app, env string) string {
	return filepath.Join(appEnvLockDir(), fmt.Sprintf("%s.lock", identity.InfraID(app, env)))
}

func acquireAppEnvLock(app, env string) (*appEnvLock, error) {
	if err := validateAppEnv(app, env); err != nil {
		return nil, err
	}
	path := appEnvLockPath(app, env)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &appEnvLock{file: file}, nil
}

func (l *appEnvLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func withAppEnvLock(app, env string, fn func()) {
	lock, err := acquireAppEnvLock(app, env)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			utils.Die(fmt.Sprintf("release lock for %s (%s): %v", app, env, err), 1)
		}
	}()
	fn()
}

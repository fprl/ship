package helper

import (
	"fmt"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/secrets"
	"github.com/fprl/ship/internal/utils"
)

type appPreviewShareCmd struct {
	App    string `arg:"" help:"App name."`
	Env    string `arg:"" help:"A live preview environment."`
	Rotate bool   `name:"rotate" help:"Generate a new preview capability."`
}

func (c appPreviewShareCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if envClassForAuth(c.App, c.Env) != "preview" {
		utils.DieError(errcat.New(errcat.CodeShareOnProduction, errcat.Fields{"branch": "current branch"}), 1)
	}
	verb := helperVerbRead
	if c.Rotate {
		verb = helperVerbShare
	}
	authorizeOrDie(verb, authTargetForAppEnv(c.App, c.Env, "preview-share"))

	lock, err := acquireAppEnvLock(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer func() { _ = lock.Release() }()

	token, err := ensurePreviewCapability(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	if c.Rotate {
		previous := token
		token, err = generatePreviewCredential(32)
		if err != nil {
			utils.DieError(err, 1)
		}
		if err := secrets.PutPreviewCapability(c.App, c.Env, []byte(token)); err != nil {
			utils.DieError(err, 1)
		}
		if err := rerenderPreviewCapabilityLocked(c.App, c.Env); err != nil {
			rollbackErr := secrets.PutPreviewCapability(c.App, c.Env, []byte(previous))
			utils.DieError(previewCapabilityRotationError(err, rollbackErr), 1)
		}
	}
	fmt.Println(token)
	return nil
}

func previewCapabilityRotationError(rerenderErr, rollbackErr error) error {
	if rollbackErr == nil {
		return rerenderErr
	}
	return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  fmt.Sprintf("preview capability rotation failed: %v; rollback failed: %v; preview capability state is ambiguous", rerenderErr, rollbackErr),
		"command": "ship preview share --rotate",
	})
}

package helper

import (
	"errors"
	"fmt"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/secrets"
	"github.com/fprl/ship/internal/utils"
)

type appShareCmd struct {
	App string `arg:"" help:"App name."`
	Env string `arg:"" help:"A live preview environment."`
	Rm  bool   `name:"rm" help:"Revoke this preview's share link."`
}

func (c appShareCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if envClassForAuth(c.App, c.Env) != "preview" {
		utils.DieError(errcat.New(errcat.CodeNoPreviewEnv, errcat.Fields{"branch": "current branch"}), 1)
	}
	authorizeOrDie(helperVerbShare, authTargetForAppEnv(c.App, c.Env, "share"))

	lock, err := acquireAppNamedLock(c.App, "preview-protection")
	if err != nil {
		utils.DieError(err, 1)
	}
	defer func() { _ = lock.Release() }()
	envLock, err := acquireAppEnvLock(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer func() { _ = envLock.Release() }()

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
	if c.Rm {
		previous, err := previewShareToken(c.App, c.Env)
		if err != nil {
			utils.DieError(err, 1)
		}
		if err := secrets.RmShareToken(c.App, c.Env); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			utils.DieError(err, 1)
		}
		if err := rerenderProtectedPreviewLocked(c.App, c.Env, credentials); err != nil {
			// Caddy still serves the old stanza; put the token back so
			// disk matches reality and a retried revoke can converge.
			if previous != "" {
				_ = secrets.PutShareToken(c.App, c.Env, []byte(previous))
			}
			utils.DieError(err, 1)
		}
		fmt.Println("Share link revoked.")
		return nil
	}
	token, err := previewShareToken(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	if token == "" {
		token, err = generatePreviewCredential(32)
		if err != nil {
			utils.DieError(err, 1)
		}
		if err := secrets.PutShareToken(c.App, c.Env, []byte(token)); err != nil {
			utils.DieError(err, 1)
		}
	}
	// Always re-render: the token is persisted before the fragment goes
	// live, so a failed render would otherwise leave a minted token that
	// short-circuits every later share into print-only, with the stanza
	// never reaching Caddy. Re-rendering is idempotent and self-healing.
	if err := rerenderProtectedPreviewLocked(c.App, c.Env, credentials); err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println(token)
	return nil
}

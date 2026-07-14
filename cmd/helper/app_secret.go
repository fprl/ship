package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/secrets"
	"github.com/fprl/ship/internal/utils"
)

// appSecretCmd is the host-side surface for the per-(app, env, key)
// secret store. Values land on disk under
// /etc/ship/secrets/<app>/<env>/<key> (mode 0600, root:root) and
// are resolved into the runtime env file by `server app apply`.
type appSecretCmd struct {
	Set  appSecretSetCmd  `cmd:"set" help:"Write a secret value from stdin."`
	List appSecretListCmd `cmd:"list" help:"List secret keys for an (app, env) pair."`
	Rm   appSecretRmCmd   `cmd:"rm" help:"Remove a secret key."`
}

type appSecretSetCmd struct {
	App string `arg:"" help:"App name."`
	Env string `arg:"" help:"Env name."`
	Key string `arg:"" help:"Secret key (env-var name)."`
}

func (c appSecretSetCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if err := secrets.ValidateKey(c.Key); err != nil {
		return errcat.New(errcat.CodeInvalidSecretKey, errcat.Fields{"key": fmt.Sprintf("%q", c.Key)})
	}
	authorizeOrDie(helperVerbSecretSet, authTargetForAppEnv(c.App, c.Env, "set", "key="+c.Key))
	// stdin only — never argv. The client owns the single trailing
	// TTY-newline trim before SSHing the value here; the helper stores
	// these bytes verbatim so the value never lands in the host's
	// process table or shell history.
	value, err := io.ReadAll(os.Stdin)
	if err != nil {
		utils.Die(fmt.Sprintf("read secret value: %v", err), 1)
	}
	valueWithoutTrailingNewline := value
	if len(valueWithoutTrailingNewline) > 0 && valueWithoutTrailingNewline[len(valueWithoutTrailingNewline)-1] == '\n' {
		valueWithoutTrailingNewline = valueWithoutTrailingNewline[:len(valueWithoutTrailingNewline)-1]
	}
	if strings.Contains(string(valueWithoutTrailingNewline), "\n") {
		return errcat.New(errcat.CodeSecretInvalid, errcat.Fields{"detail": "secret values cannot contain embedded newlines; encode multi-line material (for example base64) and decode it in the app"})
	}
	withAppEnvLock(c.App, c.Env, func() {
		if err := secrets.Put(c.App, c.Env, c.Key, value); err != nil {
			utils.DieError(err, 1)
		}
		// Never echo the value. Confirm the write by naming the key only.
		fmt.Printf("Stored secret %s for %s (%s)\n", c.Key, c.App, c.Env)
	})
	return nil
}

type appSecretListCmd struct {
	App  string `arg:"" help:"App name."`
	Env  string `arg:"" help:"Env name."`
	JSON bool   `name:"json" help:"Emit structured JSON instead of plain key lines."`
}

func (c appSecretListCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbSecretRead, authTargetForAppEnv(c.App, c.Env, "list", "secret-list"))
	keys, err := secrets.List(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	if c.JSON {
		payload := secretListPayloadFor(c.App, c.Env, keys)
		buf, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(buf))
		return nil
	}
	// Keys only. Never values.
	for _, k := range keys {
		fmt.Println(k)
	}
	return nil
}

type secretListPayload struct {
	App  string   `json:"app"`
	Env  string   `json:"env"`
	Keys []string `json:"keys"`
}

func secretListPayloadFor(app, env string, keys []string) secretListPayload {
	if keys == nil {
		keys = []string{}
	}
	return secretListPayload{App: app, Env: env, Keys: keys}
}

type appSecretRmCmd struct {
	App string `arg:"" help:"App name."`
	Env string `arg:"" help:"Env name."`
	Key string `arg:"" help:"Secret key to remove."`
}

func (c appSecretRmCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if err := secrets.ValidateKey(c.Key); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbSecretRemove, authTargetForAppEnv(c.App, c.Env, "rm", "key="+c.Key))
	withAppEnvLock(c.App, c.Env, func() {
		err := secrets.Rm(c.App, c.Env, c.Key)
		switch {
		case errors.Is(err, secrets.ErrNotFound):
			fmt.Printf("Secret %s was not set for %s (%s).\n", c.Key, c.App, c.Env)
			return
		case err != nil:
			utils.DieError(err, 1)
		}
		fmt.Printf("Removed secret %s for %s (%s)\n", c.Key, c.App, c.Env)
	})
	return nil
}

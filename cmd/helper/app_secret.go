package helper

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/fprl/simple-vps/internal/secrets"
	"github.com/fprl/simple-vps/internal/utils"
)

// appSecretCmd is the host-side surface for the per-(app, env, key)
// secret store. Values land on disk under
// /etc/simple-vps/secrets/<app>/<env>/<key> (mode 0600, root:root) and
// are resolved into the runtime env file by `server app apply`.
type appSecretCmd struct {
	Put  appSecretPutCmd  `cmd:"put" help:"Write a secret value from stdin."`
	List appSecretListCmd `cmd:"list" help:"List secret keys for an (app, env) pair."`
	Rm   appSecretRmCmd   `cmd:"rm" help:"Remove a secret key."`
}

type appSecretPutCmd struct {
	App string `arg:"" help:"App name."`
	Env string `arg:"" help:"Env name."`
	Key string `arg:"" help:"Secret key (env-var name)."`
}

func (c appSecretPutCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := secrets.ValidateKey(c.Key); err != nil {
		utils.Die(err.Error(), 1)
	}
	// stdin only — never argv. The client SSHes the value over the
	// helper's stdin so the value never lands in the host's process
	// table or shell history.
	value, err := io.ReadAll(os.Stdin)
	if err != nil {
		utils.Die(fmt.Sprintf("read secret value: %v", err), 1)
	}
	// Strip exactly one trailing newline if present — a TTY `read`
	// will tack one on, and most users don't actually want it as part
	// of the secret. Preserves intentional newlines elsewhere in the
	// value (so a heredoc with multiple lines comes through intact).
	if n := len(value); n > 0 && value[n-1] == '\n' {
		value = value[:n-1]
	}
	if err := secrets.Put(c.App, c.Env, c.Key, value); err != nil {
		utils.Die(err.Error(), 1)
	}
	// Never echo the value. Confirm the write by naming the key only.
	fmt.Printf("Stored secret %s for %s (%s)\n", c.Key, c.App, c.Env)
	return nil
}

type appSecretListCmd struct {
	App string `arg:"" help:"App name."`
	Env string `arg:"" help:"Env name."`
}

func (c appSecretListCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	keys, err := secrets.List(c.App, c.Env)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	// Keys only. Never values.
	for _, k := range keys {
		fmt.Println(k)
	}
	return nil
}

type appSecretRmCmd struct {
	App string `arg:"" help:"App name."`
	Env string `arg:"" help:"Env name."`
	Key string `arg:"" help:"Secret key to remove."`
}

func (c appSecretRmCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := secrets.ValidateKey(c.Key); err != nil {
		utils.Die(err.Error(), 1)
	}
	err := secrets.Rm(c.App, c.Env, c.Key)
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		fmt.Printf("Secret %s was not set for %s (%s).\n", c.Key, c.App, c.Env)
		return nil
	case err != nil:
		utils.Die(err.Error(), 1)
	}
	fmt.Printf("Removed secret %s for %s (%s)\n", c.Key, c.App, c.Env)
	return nil
}

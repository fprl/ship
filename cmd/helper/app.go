package helper

// Public surface for the `ship server app` namespace.

type appCmd struct {
	MemberFingerprint string           `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	SetupEnv          appSetupEnvCmd   `cmd:"setup-env" help:"Create the per-env Linux user, directories, and Podman network."`
	Preflight         appPreflightCmd  `cmd:"preflight" help:"Run read-only deploy preflight checks for one app env."`
	Destroy           appDestroyCmd    `cmd:"destroy" help:"Tear down every environment for one app."`
	DestroyEnv        appDestroyEnvCmd `cmd:"destroy-env" help:"Tear down one env: containers, files, user, network."`
	Apply             appApplyCmd      `cmd:"apply" help:"Build the image, start processes, and apply the Caddy fragment from an uploaded manifest."`
	Ls                appLsCmd         `cmd:"ls" help:"List app environments visible on this host."`
	Status            appStatusCmd     `cmd:"status" help:"Show running processes for one (app, env) pair."`
	Rollback          appRollbackCmd   `cmd:"rollback" help:"Run an older image release for one (app, env) pair."`
	Exec              appExecCmd       `cmd:"exec" help:"Run a one-off command in a fresh container for one (app, env) pair."`
	Why               appWhyCmd        `cmd:"why" help:"Show the latest deploy journal entry for one (app, env)."`
	Logs              appLogsCmd       `cmd:"logs" help:"Tail logs for one process via podman logs."`
	Secret            appSecretCmd     `cmd:"secret" help:"Manage the per-(app, env, key) secret store."`
	Preview           appPreviewCmd    `cmd:"preview" help:"Manage preview branch mappings."`
	Data              appDataCmd       `cmd:"data" help:"Manage app data."`
}

func (c appCmd) BeforeApply() error {
	return requireRoot()
}

func (c appCmd) AfterApply() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	return nil
}

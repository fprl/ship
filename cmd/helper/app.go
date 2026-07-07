package helper

// Public surface for the `ship server app` namespace.

type appCmd struct {
	SetupEnv   appSetupEnvCmd   `cmd:"setup-env" help:"Create the per-env Linux user, directories, and Podman network."`
	Preflight  appPreflightCmd  `cmd:"preflight" help:"Run read-only deploy preflight checks for one app env."`
	DestroyEnv appDestroyEnvCmd `cmd:"destroy-env" help:"Tear down one env: containers, files, user, network."`
	Apply      appApplyCmd      `cmd:"apply" help:"Build the image, start processes, and apply the Caddy fragment from an uploaded manifest."`
	List       appListCmd       `cmd:"list" help:"List app environments visible on this host."`
	Status     appStatusCmd     `cmd:"status" help:"Show running processes for one (app, env) pair."`
	Restart    appRestartCmd    `cmd:"restart" help:"Bounce running containers for one (app, env) pair via podman restart."`
	Rollback   appRollbackCmd   `cmd:"rollback" help:"Run an older image release for one (app, env) pair."`
	Backup     appBackupCmd     `cmd:"backup" help:"Create, list, remove, and restore app backups."`
	Logs       appLogsCmd       `cmd:"logs" help:"Tail logs for one process via podman logs."`
	Secret     appSecretCmd     `cmd:"secret" help:"Manage the per-(app, env, key) secret store."`
	Preview    appPreviewCmd    `cmd:"preview" help:"Manage preview branch mappings."`
}

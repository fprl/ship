package helper

// Public surface for the `simple-vps server app` namespace. The
// pre-cutover verbs that took uploaded systemd units, mutated per-app
// shared env files, or ran arbitrary commands as the app user are
// gone — ADR-0005 §15 / cutover item 18 explicitly narrows the helper
// to typed manifest input. Only the new per-(app, env) lifecycle
// remains.

type appCmd struct {
	SetupEnv   appSetupEnvCmd   `cmd:"setup-env" help:"Create the per-env Linux user, directories, and Podman network."`
	DestroyEnv appDestroyEnvCmd `cmd:"destroy-env" help:"Tear down one env: containers, files, user, network."`
	Apply      appApplyCmd      `cmd:"apply" help:"Build the image, start services, and apply the Caddy fragment from an uploaded manifest."`
	Secret     appSecretCmd     `cmd:"secret" help:"Manage the per-(app, env, key) secret store."`
}

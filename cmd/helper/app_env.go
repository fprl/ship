package helper

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/fprl/simple-vps/internal/host"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/secrets"
	"github.com/fprl/simple-vps/internal/utils"
)

// nameRe is a conservative regex for app and env names accepted by the
// per-env helper verbs. Matches the manifest validator's AppRe/ServiceRe
// shape: lowercase, hyphen-allowed, must start with a letter.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

func validateAppEnv(app, env string) error {
	if !nameRe.MatchString(app) {
		return fmt.Errorf("invalid app name: %q", app)
	}
	if !nameRe.MatchString(env) {
		return fmt.Errorf("invalid env name: %q", env)
	}
	return nil
}

// appSetupEnvCmd creates the per-env Linux user, on-disk layout, and
// Podman network for one (app, env) pair. Idempotent.
type appSetupEnvCmd struct {
	App string `arg:"" help:"App name."`
	Env string `arg:"" help:"Env name."`
}

func (c appSetupEnvCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}

	user := identity.SystemUser(c.App, c.Env)
	network := identity.Network(c.App, c.Env)
	envRoot := identity.AppEnvRoot(c.App, c.Env)
	shared := identity.SharedDir(c.App, c.Env)
	appRoot := identity.AppRoot(c.App)

	// 0. Make sure the shared deploy tmp dir exists with sticky +
	// world-writable perms. The provisioner creates this at install
	// time; setup-env ensures it for hosts that pre-date the
	// provisioner change so a deploy on an upgraded box doesn't fail
	// with "Permission denied" on the upload step.
	deployTmp := host.DeployTmpDir()
	if err := os.MkdirAll(deployTmp, 0755); err != nil {
		utils.Die(fmt.Sprintf("mkdir %s: %v", deployTmp, err), 1)
	}
	if err := os.Chmod(deployTmp, os.ModeSticky|0777); err != nil {
		utils.Die(fmt.Sprintf("chmod %s: %v", deployTmp, err), 1)
	}

	// 1. Ensure the per-env system user exists.
	if !host.CommandSucceeds("id", "-u", user) {
		if _, err := utils.RunChecked("useradd",
			[]string{"--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", user},
			"",
		); err != nil {
			utils.Die(fmt.Sprintf("useradd %s: %v", user, err), 1)
		}
	}

	// 2. Grant the SUDO_USER (the deploy user) group membership on the
	// per-env user so writes to /var/apps/<app>/<env>/shared from a
	// deploy step land with the right ownership.
	deployUser, err := host.DeployUserFromSudo()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if deployUser != "" {
		if _, err := utils.RunChecked("usermod", []string{"-aG", user, deployUser}, ""); err != nil {
			utils.Die(fmt.Sprintf("usermod -aG %s %s: %v", user, deployUser, err), 1)
		}
	}

	// 3. Create the on-disk layout.
	for _, dir := range []string{appRoot, envRoot, shared} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			utils.Die(err.Error(), 1)
		}
	}
	// shared/ is group-writable so the deploy user can drop files for the
	// container; sgid so new files inherit the group.
	if _, err := utils.RunChecked("chown", []string{"-R", user + ":" + user, envRoot}, ""); err != nil {
		utils.Die(fmt.Sprintf("chown %s: %v", envRoot, err), 1)
	}
	if _, err := utils.RunChecked("chmod", []string{"2775", shared}, ""); err != nil {
		utils.Die(fmt.Sprintf("chmod 2775 %s: %v", shared, err), 1)
	}

	// 4. Ensure the per-env Podman network exists. Containers join this
	// for intra-app DNS in addition to the shared `ingress` network.
	if !host.CommandSucceeds("podman", "network", "exists", network) {
		if _, err := utils.RunChecked("podman", []string{"network", "create", network}, ""); err != nil {
			utils.Die(fmt.Sprintf("podman network create %s: %v", network, err), 1)
		}
	}

	fmt.Printf("App %s (%s) is ready at %s\n", c.App, c.Env, envRoot)
	return nil
}

// appDestroyEnvCmd removes one env's containers, files, user, and
// network. Removes the parent app dir only when this was the last env.
type appDestroyEnvCmd struct {
	App   string `arg:"" help:"App name."`
	Env   string `arg:"" help:"Env name."`
	Purge bool   `name:"purge" help:"Also delete secrets for this app/env."`
}

func (c appDestroyEnvCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}

	app, env := c.App, c.Env
	user := identity.SystemUser(app, env)
	network := identity.Network(app, env)
	envRoot := identity.AppEnvRoot(app, env)
	appRoot := identity.AppRoot(app)

	// 1. Remove the Caddy fragment and reload first, so traffic stops
	// routing here before the containers disappear. Restore the
	// fragment if validation or reload fails; otherwise a healthy route
	// could be lost on a later reload even though destroy failed.
	caddyRemoved, err := removeAppCaddyfile(app, env)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	// 2. Stop and remove any containers belonging to this (app, env).
	containers, err := podmanPSContainers(app, env)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	removedContainers := destroyContainerNames(containersToServices(containers))
	for _, name := range removedContainers {
		_, _ = utils.RunChecked("podman", []string{"rm", "-f", name}, "")
	}

	// 3. Drop the env directory.
	if _, err := os.Stat(envRoot); err == nil {
		if err := os.RemoveAll(envRoot); err != nil {
			utils.Die(err.Error(), 1)
		}
	}

	// 4. Drop the per-env user (and its primary group).
	if host.CommandSucceeds("id", "-u", user) {
		_, _ = utils.RunChecked("userdel", []string{user}, "")
	}
	if host.CommandSucceeds("getent", "group", user) {
		_, _ = utils.RunChecked("groupdel", []string{user}, "")
	}

	// 5. Drop the per-env Podman network.
	if host.CommandSucceeds("podman", "network", "exists", network) {
		_, _ = utils.RunChecked("podman", []string{"network", "rm", network}, "")
	}

	secretsPurged := false
	if c.Purge {
		secretDir := secrets.EnvDir(app, env)
		if err := os.RemoveAll(secretDir); err != nil {
			utils.Die(fmt.Sprintf("remove secrets for %s (%s): %v", app, env, err), 1)
		}
		secretsPurged = true
		if appSecretDir := filepath.Dir(secretDir); dirEmpty(appSecretDir) {
			_ = os.Remove(appSecretDir)
		}
	}

	// 6. If the parent app dir is now empty (last env destroyed), drop it.
	if entries, err := os.ReadDir(appRoot); err == nil && len(entries) == 0 {
		_ = os.Remove(appRoot)
	}

	fmt.Print(renderDestroyText(app, env, destroySummary{
		Containers:    removedContainers,
		CaddyFragment: caddyRemoved,
		SecretsPurged: secretsPurged,
	}))
	return nil
}

type destroySummary struct {
	Containers    []string
	CaddyFragment bool
	SecretsPurged bool
}

func destroyContainerNames(services []serviceStatus) []string {
	names := make([]string, 0, len(services))
	for _, svc := range services {
		if svc.Container != "" {
			names = append(names, svc.Container)
		}
	}
	return names
}

func removeAppCaddyfile(app, env string) (bool, error) {
	path := caddyfilePath(app, env)
	prevFragment, prevExisted, err := snapshotCaddyFragment(path)
	if err != nil {
		return false, fmt.Errorf("snapshot existing fragment: %v", err)
	}
	if !prevExisted {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove caddy fragment %s: %v", path, err)
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}, ""); err != nil {
		if restoreErr := restoreCaddyFragment(path, prevFragment, prevExisted); restoreErr != nil {
			return false, fmt.Errorf("caddy validate after destroy failed AND restore failed (manual fix required at %s): %v (restore: %v)", path, err, restoreErr)
		}
		return false, fmt.Errorf("caddy validate after destroy failed, restored previous fragment: %v", err)
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, ""); err != nil {
		if restoreErr := restoreCaddyFragment(path, prevFragment, prevExisted); restoreErr != nil {
			return false, fmt.Errorf("caddy reload after destroy failed AND restore failed (manual fix required at %s): %v (restore: %v)", path, err, restoreErr)
		}
		return false, fmt.Errorf("caddy reload after destroy failed, restored previous fragment: %v", err)
	}
	return true, nil
}

func renderDestroyText(app, env string, summary destroySummary) string {
	out := fmt.Sprintf("Destroyed %s (%s)\n", app, env)
	if len(summary.Containers) == 0 {
		out += "  containers: none\n"
	} else {
		out += fmt.Sprintf("  containers: %d removed\n", len(summary.Containers))
	}
	if summary.CaddyFragment {
		out += "  route: removed\n"
	} else {
		out += "  route: none\n"
	}
	if summary.SecretsPurged {
		out += "  secrets: purged\n"
	} else {
		out += "  secrets: kept\n"
	}
	return out
}

func dirEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

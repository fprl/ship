package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fprl/simple-vps/internal/host"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/names"
	"github.com/fprl/simple-vps/internal/releaseid"
	"github.com/fprl/simple-vps/internal/secrets"
	"github.com/fprl/simple-vps/internal/utils"
)

func validateAppEnv(app, env string) error {
	if !names.AppRe.MatchString(app) {
		return fmt.Errorf("invalid app name: %q", app)
	}
	if !names.EnvRe.MatchString(env) {
		return fmt.Errorf("invalid env name: %q", env)
	}
	return nil
}

func validateRelease(release string) error {
	return releaseid.Validate(release)
}

// appSetupEnvCmd creates the per-env Linux user, on-disk layout, and
// Podman network for one (app, env) pair. Idempotent.
type appSetupEnvCmd struct {
	App string `arg:"" help:"App name."`
	Env string `arg:"" help:"Env name."`
}

func (c appSetupEnvCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked(true)
	})
	return nil
}

func (c appSetupEnvCmd) runLocked(printSummary bool) {
	if err := setupEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if printSummary {
		fmt.Printf("App %s (%s) is ready at %s\n", c.App, c.Env, identity.EnvRoot(c.App, c.Env))
	}
}

func setupEnv(app, env string) error {
	user := identity.SystemUser(app, env)
	network := identity.Network(app, env)
	envRoot := identity.EnvRoot(app, env)
	dataDir := identity.DataDir(app, env)
	runtimeDir := identity.RuntimeDir(app, env)
	staticDir := identity.StaticDir(app, env)
	releaseDir := identity.ReleaseDir(app, env)

	// 0. Make sure the deploy tmp dir exists with sticky +
	// world-writable perms. The client uploads tarballs and manifests
	// here before handing them to the privileged helper.
	deployTmp := host.DeployTmpDir()
	if err := os.MkdirAll(deployTmp, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %v", deployTmp, err)
	}
	if err := os.Chmod(deployTmp, os.ModeSticky|0777); err != nil {
		return fmt.Errorf("chmod %s: %v", deployTmp, err)
	}

	// 1. Ensure the per-env system user exists.
	if !host.CommandSucceeds("id", "-u", user) {
		if _, err := utils.RunChecked("useradd",
			[]string{"--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", user},
			"",
		); err != nil {
			return fmt.Errorf("useradd %s: %v", user, err)
		}
	}

	// 2. Grant the SUDO_USER (the deploy user) group membership on the
	// per-env user so deploy-time writes to /data-compatible host paths
	// land with the right ownership.
	deployUser, err := host.DeployUserFromSudo()
	if err != nil {
		return err
	}
	if deployUser != "" {
		if _, err := utils.RunChecked("usermod", []string{"-aG", user, deployUser}, ""); err != nil {
			return fmt.Errorf("usermod -aG %s %s: %v", user, deployUser, err)
		}
	}

	// 3. Create the on-disk layout.
	for _, dir := range []string{envRoot, dataDir, runtimeDir, staticDir, releaseDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", envRoot}, ""); err != nil {
		return fmt.Errorf("chown %s: %v", envRoot, err)
	}
	if _, err := utils.RunChecked("chmod", []string{"0755", envRoot}, ""); err != nil {
		return fmt.Errorf("chmod 0755 %s: %v", envRoot, err)
	}
	for _, dir := range []string{dataDir, staticDir} {
		if _, err := utils.RunChecked("chown", []string{"-R", user + ":" + user, dir}, ""); err != nil {
			return fmt.Errorf("chown %s: %v", dir, err)
		}
		if _, err := utils.RunChecked("chmod", []string{"2775", dir}, ""); err != nil {
			return fmt.Errorf("chmod 2775 %s: %v", dir, err)
		}
	}
	if _, err := utils.RunChecked("chown", []string{"-R", "root:root", releaseDir}, ""); err != nil {
		return fmt.Errorf("chown %s: %v", releaseDir, err)
	}
	if _, err := utils.RunChecked("chmod", []string{"0755", releaseDir}, ""); err != nil {
		return fmt.Errorf("chmod 0755 %s: %v", releaseDir, err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:" + user, runtimeDir}, ""); err != nil {
		return fmt.Errorf("chown %s: %v", runtimeDir, err)
	}
	if _, err := utils.RunChecked("chmod", []string{"0750", runtimeDir}, ""); err != nil {
		return fmt.Errorf("chmod 0750 %s: %v", runtimeDir, err)
	}
	if err := writeEnvIdentity(app, env); err != nil {
		return err
	}

	// 4. Ensure the per-env Podman network exists. Containers join this
	// for intra-app DNS in addition to the shared `ingress` network.
	if !host.CommandSucceeds("podman", "network", "exists", network) {
		if _, err := utils.RunChecked("podman", []string{"network", "create", network}, ""); err != nil {
			return fmt.Errorf("podman network create %s: %v", network, err)
		}
	}

	return nil
}

// appDestroyEnvCmd removes one env's containers, files, user, and network.
type appDestroyEnvCmd struct {
	App   string `arg:"" help:"App name."`
	Env   string `arg:"" help:"Env name."`
	Purge bool   `name:"purge" help:"Also delete secrets for this app/env."`
}

func (c appDestroyEnvCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked(true)
	})
	return nil
}

func (c appDestroyEnvCmd) runLocked(printSummary bool) {
	summary, err := destroyEnv(c.App, c.Env, c.Purge)
	if err != nil {
		utils.DieError(err, 1)
	}
	if printSummary {
		fmt.Print(renderDestroyText(c.App, c.Env, summary))
	}
}

func destroyEnv(app, env string, purge bool) (destroySummary, error) {
	user := identity.SystemUser(app, env)
	network := identity.Network(app, env)
	envRoot := identity.EnvRoot(app, env)

	// 1. Remove the Caddy fragment and reload first, so traffic stops
	// routing here before the containers disappear. Restore the
	// fragment if validation or reload fails; otherwise a healthy route
	// could be lost on a later reload even though destroy failed.
	caddyRemoved, err := removeAppCaddyfile(app, env)
	if err != nil {
		return destroySummary{}, err
	}

	// 2. Stop and remove any containers belonging to this (app, env).
	containers, err := podmanPSContainers(app, env)
	if err != nil {
		return destroySummary{}, err
	}
	removedContainers := destroyContainerNames(containersToProcesses(containers))
	for _, name := range removedContainers {
		_, _ = utils.RunChecked("podman", []string{"rm", "-f", name}, "")
	}

	// 3. Drop the env directory.
	if _, err := os.Stat(envRoot); err == nil {
		if err := os.RemoveAll(envRoot); err != nil {
			return destroySummary{}, err
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
	if purge {
		secretDir := secrets.EnvDir(app, env)
		if err := os.RemoveAll(secretDir); err != nil {
			return destroySummary{}, fmt.Errorf("remove secrets for %s (%s): %v", app, env, err)
		}
		secretsPurged = true
		if appSecretDir := filepath.Dir(secretDir); dirEmpty(appSecretDir) {
			_ = os.Remove(appSecretDir)
		}
	}

	return destroySummary{
		Containers:    removedContainers,
		CaddyFragment: caddyRemoved,
		SecretsPurged: secretsPurged,
	}, nil
}

type destroySummary struct {
	Containers    []string
	CaddyFragment bool
	SecretsPurged bool
}

func destroyContainerNames(processes []processStatus) []string {
	names := make([]string, 0, len(processes))
	for _, proc := range processes {
		if proc.Container != "" {
			names = append(names, proc.Container)
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

func writeEnvIdentity(app, env string) error {
	existing, _ := readEnvIdentity(app, env)
	return writeEnvIdentityWithPreview(app, env, existing.Preview)
}

func writeEnvIdentityWithPreview(app, env string, preview *identity.PreviewIdentity) error {
	path := identity.IdentityFile(app, env)
	dir := filepath.Dir(path)
	file := identity.EnvIdentity{
		Version: 1,
		App:     app,
		Env:     env,
		InfraID: identity.InfraID(app, env),
		Preview: preview,
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".simple-vps.json-*")
	if err != nil {
		return fmt.Errorf("create env identity temp file: %v", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write env identity temp file: %v", err)
	}
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod env identity temp file: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close env identity temp file: %v", err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", tmpPath}, ""); err != nil {
		return fmt.Errorf("chown env identity: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace env identity %s: %v", path, err)
	}
	cleanup = false
	return nil
}

func readEnvIdentity(app, env string) (identity.EnvIdentity, error) {
	return readEnvIdentityFile(identity.IdentityFile(app, env))
}

func readEnvIdentityFile(path string) (identity.EnvIdentity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return identity.EnvIdentity{}, err
	}
	var file identity.EnvIdentity
	if err := json.Unmarshal(data, &file); err != nil {
		return identity.EnvIdentity{}, err
	}
	if file.App == "" || file.Env == "" || file.InfraID != identity.InfraID(file.App, file.Env) {
		return identity.EnvIdentity{}, fmt.Errorf("invalid env identity %s", path)
	}
	return file, nil
}

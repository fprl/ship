package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/releaseid"
	"github.com/fprl/ship/internal/secrets"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
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
	if err := applyEnvLayoutPerms(app, env); err != nil {
		return err
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

func applyEnvLayoutPerms(app, env string) error {
	user := identity.SystemUser(app, env)
	envRoot := identity.EnvRoot(app, env)
	dataDir := identity.DataDir(app, env)
	runtimeDir := identity.RuntimeDir(app, env)
	staticDir := identity.StaticDir(app, env)
	releaseDir := identity.ReleaseDir(app, env)

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
	return nil
}

// appDestroyEnvCmd removes one env's containers, files, user, and network.
type appDestroyCmd struct {
	App string `arg:"" help:"App name."`
}

func (c appDestroyCmd) Run() error {
	if !names.AppRe.MatchString(c.App) {
		utils.DieError(fmt.Errorf("invalid app name: %q", c.App), 1)
	}
	authorizeOrDie(helperVerbBoxMutation, authTargetForBox("box rm app="+c.App, "app="+c.App))
	envs, err := appEnvsForDestroy(c.App)
	if err != nil {
		utils.DieError(err, 1)
	}
	if len(envs) == 0 {
		fmt.Printf("No environments found for %s\n", c.App)
		return nil
	}
	fmt.Printf("Destroying %s (%d envs)\n", c.App, len(envs))
	for _, env := range envs {
		withAppEnvLock(c.App, env, func() {
			summary, err := destroyEnv(c.App, env, true)
			if err != nil {
				utils.DieError(err, 1)
			}
			fmt.Print(renderDestroyText(c.App, env, summary))
		})
	}
	return nil
}

func appEnvsForDestroy(app string) ([]string, error) {
	apps, err := identityAppEnvs()
	if err != nil {
		return nil, err
	}
	var envs []string
	for _, item := range apps {
		if item.App == app {
			envs = append(envs, item.Env)
		}
	}
	return envs, nil
}

type appDestroyEnvCmd struct {
	App   string `arg:"" help:"App name."`
	Env   string `arg:"" help:"Env name."`
	Purge bool   `name:"purge" help:"Also delete secrets for this app/env."`
}

func (c appDestroyEnvCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbRemoveEnv, authTargetForAppEnv(c.App, c.Env, fmt.Sprintf("purge=%t", c.Purge)))
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

	// 3. Drop release images for this exact (app, env). Containers are gone,
	// so Podman can safely remove the tags while preserving shared layers
	// that other images still need.
	removedImages, err := purgeReleaseImagesForEnv(app, env)
	if err != nil {
		return destroySummary{}, fmt.Errorf("remove release images for %s (%s): %v", app, env, err)
	}

	// 4. Drop the env directory.
	if _, err := os.Stat(envRoot); err == nil {
		if err := os.RemoveAll(envRoot); err != nil {
			return destroySummary{}, err
		}
	}

	// 5. Drop the per-env user (and its primary group).
	if host.CommandSucceeds("id", "-u", user) {
		_, _ = utils.RunChecked("userdel", []string{user}, "")
	}
	if host.CommandSucceeds("getent", "group", user) {
		_, _ = utils.RunChecked("groupdel", []string{user}, "")
	}

	// 6. Drop the per-env Podman network.
	if host.CommandSucceeds("podman", "network", "exists", network) {
		_, _ = utils.RunChecked("podman", []string{"network", "rm", network}, "")
	}

	secretsPurged, err := cleanupDestroyedEnvCredentials(app, env, purge)
	if err != nil {
		return destroySummary{}, err
	}

	return destroySummary{
		Containers:    removedContainers,
		Images:        removedImages,
		CaddyFragment: caddyRemoved,
		SecretsPurged: secretsPurged,
	}, nil
}

func cleanupDestroyedEnvCredentials(app, env string, purge bool) (bool, error) {
	if err := secrets.RmShareToken(app, env); err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return false, fmt.Errorf("remove share token for %s (%s): %v", app, env, err)
	}

	secretDir := secrets.EnvDir(app, env)
	appSecretDir := filepath.Dir(secretDir)
	if purge {
		if err := os.RemoveAll(secretDir); err != nil {
			return false, fmt.Errorf("remove secrets for %s (%s): %v", app, env, err)
		}
	}

	// Preview-protection credentials are app-wide, so keep them while any env
	// remains and remove them with the final env. This is the same condition
	// used by the preview reaper, which always calls destroyEnv with purge.
	if remaining, err := identityAppEnvs(); err == nil {
		last := true
		for _, item := range remaining {
			if item.App == app {
				last = false
				break
			}
		}
		if last {
			if purge {
				if err := os.RemoveAll(appSecretDir); err != nil {
					return false, fmt.Errorf("remove app secrets for %s: %v", app, err)
				}
			} else if err := os.RemoveAll(secrets.AppDir(app, previewProtectionNamespace)); err != nil {
				return false, fmt.Errorf("remove preview protection credentials for %s: %v", app, err)
			}
		}
	}
	if dirEmpty(appSecretDir) {
		_ = os.Remove(appSecretDir)
	}
	return purge, nil
}

type destroySummary struct {
	Containers    []string
	Images        int
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
	if err := reloadCaddyOrRestore(path, prevFragment, prevExisted); err != nil {
		var caddyErr caddyReloadStageError
		if errors.As(err, &caddyErr) {
			switch {
			case caddyErr.Stage == "validate" && caddyErr.RestoreErr != nil:
				return false, fmt.Errorf("caddy validate after destroy failed AND restore failed (manual fix required at %s): %v (restore: %v)", path, caddyErr.Err, caddyErr.RestoreErr)
			case caddyErr.Stage == "validate":
				return false, fmt.Errorf("caddy validate after destroy failed, restored previous fragment: %v", caddyErr.Err)
			case caddyErr.Stage == "reload" && caddyErr.RestoreErr != nil:
				return false, fmt.Errorf("caddy reload after destroy failed AND restore failed (manual fix required at %s): %v (restore: %v)", path, caddyErr.Err, caddyErr.RestoreErr)
			case caddyErr.Stage == "reload":
				return false, fmt.Errorf("caddy reload after destroy failed, restored previous fragment: %v", caddyErr.Err)
			}
		}
		return false, err
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
	switch summary.Images {
	case 0:
		out += "  images: none\n"
	case 1:
		out += "  images: 1 removed\n"
	default:
		out += fmt.Sprintf("  images: %d removed\n", summary.Images)
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

	if err := store.AtomicWrite(path, data, 0644); err != nil {
		return fmt.Errorf("write env identity: %v", err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", path}, ""); err != nil {
		return fmt.Errorf("chown env identity: %v", err)
	}
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
	if file.Version != 1 {
		return identity.EnvIdentity{}, fmt.Errorf("unsupported identity version %d", file.Version)
	}
	if file.App == "" || file.Env == "" || file.InfraID != identity.InfraID(file.App, file.Env) {
		return identity.EnvIdentity{}, fmt.Errorf("invalid env identity %s", path)
	}
	return file, nil
}

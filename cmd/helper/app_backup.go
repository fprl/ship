package helper

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/secrets"
	"github.com/fprl/simple-vps/internal/utils"
)

type appBackupCmd struct {
	Args   []string `arg:"" optional:"" help:"Either <app> <env>, list <app> <env>, rm <app> <env> <id>, or restore <app> <env>."`
	To     string   `name:"to" help:"Destination directory. Supports plain paths and file:// URLs."`
	From   string   `name:"from" help:"Backup ID or path for restore. Supports plain paths and file:// URLs."`
	Dir    string   `name:"dir" help:"Backup directory for ID lookup. Supports plain paths and file:// URLs."`
	JSON   bool     `name:"json" help:"Emit structured JSON for list."`
	DryRun bool     `name:"dry-run" help:"Show what would be restored without writing."`
}

func (c appBackupCmd) Run() error {
	sub := "create"
	args := c.Args
	if len(args) > 0 {
		switch args[0] {
		case "list", "rm", "restore":
			sub = args[0]
			args = args[1:]
		}
	}
	switch sub {
	case "create":
		app, env := parseBackupAppEnv(args)
		withAppEnvLock(app, env, func() {
			path, err := createBackup(app, env, c.To, time.Now().UTC())
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Printf("Created backup %s\n", path)
		})
	case "list":
		app, env := parseBackupAppEnv(args)
		backups, err := listBackups(app, env, c.Dir)
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		if c.JSON {
			buf, err := json.MarshalIndent(struct {
				App     string       `json:"app"`
				Env     string       `json:"env"`
				Backups []backupInfo `json:"backups"`
			}{App: app, Env: env, Backups: backups}, "", "  ")
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			fmt.Println(string(buf))
			return nil
		}
		for _, b := range backups {
			fmt.Println(b.ID)
		}
	case "rm":
		if len(args) != 3 {
			utils.Die("backup rm requires <app> <env> <backup-id>", 1)
		}
		app, env := validateBackupAppEnv(args[0], args[1])
		path, err := resolveBackupPath(app, env, args[2], c.Dir)
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		if err := os.Remove(path); err != nil {
			utils.Die(fmt.Sprintf("remove backup %s: %v", path, err), 1)
		}
		fmt.Printf("Removed backup %s\n", filepath.Base(path))
	case "restore":
		app, env := parseBackupAppEnv(args)
		if c.From == "" {
			utils.Die("backup restore requires --from", 1)
		}
		withAppEnvLock(app, env, func() {
			result, err := restoreBackup(app, env, c.From, c.Dir, c.DryRun)
			if err != nil {
				utils.Die(err.Error(), 1)
			}
			if c.DryRun {
				fmt.Printf("Would restore %s (%s) from %s at release %s\n", result.App, result.Env, result.ID, result.Release)
				return
			}
			fmt.Printf("Restored %s (%s) from %s at release %s\n", result.App, result.Env, result.ID, result.Release)
		})
	}
	return nil
}

func parseBackupAppEnv(args []string) (string, string) {
	if len(args) != 2 {
		utils.Die("backup requires <app> <env>", 1)
	}
	return validateBackupAppEnv(args[0], args[1])
}

func validateBackupAppEnv(app, env string) (string, string) {
	if err := validateAppEnv(app, env); err != nil {
		utils.Die(err.Error(), 1)
	}
	return app, env
}

type backupMetadata struct {
	SchemaVersion int      `json:"schema_version"`
	App           string   `json:"app"`
	Env           string   `json:"env"`
	ID            string   `json:"id"`
	CreatedAt     string   `json:"created_at"`
	Release       string   `json:"release"`
	Shape         string   `json:"shape"`
	Processes     []string `json:"processes"`
	StaticRoutes  []string `json:"static_routes"`
}

type backupInfo struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at,omitempty"`
	Release   string `json:"release,omitempty"`
	Size      int64  `json:"size"`
}

type backupPayload struct {
	Metadata backupMetadata    `json:"metadata"`
	Secrets  map[string]string `json:"secrets"`
}

func createBackup(app, env, dest string, now time.Time) (string, error) {
	manifestPath := identity.ManifestFile(app, env)
	if _, err := os.Stat(manifestPath); err != nil {
		return "", fmt.Errorf("applied manifest not found at %s; deploy once before backup", manifestPath)
	}
	appCtx, cleanup, err := loadAppliedAppContext(app, env)
	if err != nil {
		return "", err
	}
	defer cleanup()

	var release string
	var processes []string
	if appCtx.NeedsImage {
		containers, err := podmanPSContainers(app, env)
		if err != nil {
			return "", err
		}
		running := runningProcesses(containersToProcesses(containers))
		release, err = currentRelease(running)
		if err != nil {
			return "", err
		}
		processes = processNamesFromStatuses(running)
	} else if appCtx.HasStaticRoutes {
		release, err = currentStaticRelease(app, env)
		if err != nil {
			return "", err
		}
	} else {
		return "", fmt.Errorf("no active release found")
	}
	if appCtx.HasStaticRoutes {
		if err := verifyStaticRelease(app, env, release, appCtx.Routes); err != nil {
			return "", err
		}
	}

	dir, err := backupDir(app, env, dest)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	id := now.Format("20060102T150405Z") + "-" + release
	path := filepath.Join(dir, id+".tar")
	payload := backupPayload{
		Metadata: backupMetadata{
			SchemaVersion: 1,
			App:           app,
			Env:           env,
			ID:            id,
			CreatedAt:     now.Format(time.RFC3339),
			Release:       release,
			Shape:         appCtx.Shape,
			Processes:     processes,
			StaticRoutes:  staticRouteNames(appCtx.Routes),
		},
		Secrets: readSecrets(app, env),
	}
	if err := writeBackupTar(path, app, env, manifestPath, payload, appCtx.HasStaticRoutes); err != nil {
		return "", err
	}
	return path, nil
}

func restoreBackup(app, env, from, dir string, dryRun bool) (backupMetadata, error) {
	path, err := resolveBackupPath(app, env, from, dir)
	if err != nil {
		return backupMetadata{}, err
	}
	tmp, err := os.MkdirTemp("", "simple-vps-restore-")
	if err != nil {
		return backupMetadata{}, err
	}
	defer os.RemoveAll(tmp)
	payload, err := extractBackupTar(path, tmp)
	if err != nil {
		return backupMetadata{}, err
	}
	meta := payload.Metadata
	if meta.App != app || meta.Env != env {
		return backupMetadata{}, fmt.Errorf("backup is for %s (%s), not %s (%s)", meta.App, meta.Env, app, env)
	}
	if err := validateRelease(meta.Release); err != nil {
		return backupMetadata{}, err
	}
	if dryRun {
		return meta, nil
	}
	dataDir := identity.DataDir(app, env)
	if err := ensureRestoreLayout(app, env); err != nil {
		return backupMetadata{}, err
	}
	_ = os.RemoveAll(dataDir)
	if err := copyDir(filepath.Join(tmp, "data"), dataDir); err != nil {
		return backupMetadata{}, err
	}
	if err := chownAppDir(app, env, dataDir); err != nil {
		return backupMetadata{}, err
	}
	if _, err := utils.RunChecked("chmod", []string{"2775", dataDir}, ""); err != nil {
		return backupMetadata{}, fmt.Errorf("chmod %s: %v", dataDir, err)
	}
	currentManifest := identity.ManifestFile(app, env)
	if err := copyFilePath(filepath.Join(tmp, "simple-vps.toml"), currentManifest, 0644); err != nil {
		return backupMetadata{}, err
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", currentManifest}, ""); err != nil {
		return backupMetadata{}, fmt.Errorf("chown applied manifest: %v", err)
	}
	releaseManifest := identity.ReleaseManifestFile(app, env, meta.Release)
	if err := copyFilePath(filepath.Join(tmp, "simple-vps.toml"), releaseManifest, 0644); err != nil {
		return backupMetadata{}, err
	}
	if _, err := utils.RunChecked("chown", []string{"root:root", releaseManifest}, ""); err != nil {
		return backupMetadata{}, fmt.Errorf("chown release manifest: %v", err)
	}
	if err := writeEnvIdentity(app, env); err != nil {
		return backupMetadata{}, err
	}
	for k, v := range payload.Secrets {
		if err := secrets.Put(app, env, k, []byte(v)); err != nil {
			return backupMetadata{}, err
		}
	}
	appCtx, cleanup, err := loadAppliedAppContext(app, env)
	if err != nil {
		return backupMetadata{}, err
	}
	defer cleanup()

	existing, err := podmanPSContainers(app, env)
	if err != nil {
		return backupMetadata{}, err
	}
	envSnapshot, err := snapshotEnvFile(app, env)
	if err != nil {
		return backupMetadata{}, err
	}
	staticSnapshot, err := snapshotStaticCurrent(app, env)
	if err != nil {
		return backupMetadata{}, err
	}
	caddyPath := caddyfilePath(app, env)
	prevFragment, prevExisted, err := snapshotCaddyFragment(caddyPath)
	if err != nil {
		return backupMetadata{}, fmt.Errorf("snapshot existing fragment: %v", err)
	}
	var containersToRemove []string
	var startedContainers []string
	if appCtx.HasStaticRoutes {
		if err := restoreStaticRelease(app, env, tmp, meta.Release); err != nil {
			return backupMetadata{}, err
		}
	} else if staticSnapshot.Existed {
		if err := clearStaticCurrent(app, env); err != nil {
			return backupMetadata{}, err
		}
	}
	if appCtx.NeedsImage {
		resolved, err := resolveEnv(app, env, appCtx.Vars, appCtx.SecretRefs)
		if err != nil {
			_ = restoreStaticCurrent(app, env, staticSnapshot)
			return backupMetadata{}, err
		}
		if err := writeEnvFile(app, env, resolved); err != nil {
			_ = restoreStaticCurrent(app, env, staticSnapshot)
			return backupMetadata{}, err
		}
		userID, groupID, err := hostUserIDs(identity.SystemUser(app, env))
		if err != nil {
			_ = restoreEnvFile(app, env, envSnapshot)
			_ = restoreStaticCurrent(app, env, staticSnapshot)
			return backupMetadata{}, err
		}
		imageTag := identity.ImageTag(app, env, meta.Release)
		for _, procName := range sortedKeys(appCtx.Processes) {
			startedContainers = append(startedContainers, identity.ContainerName(app, env, procName, meta.Release))
			if err := startProcess(app, env, procName, appCtx.Processes[procName], imageTag, userID, groupID, meta.Release); err != nil {
				removeContainers(startedContainers)
				_ = restoreEnvFile(app, env, envSnapshot)
				_ = restoreStaticCurrent(app, env, staticSnapshot)
				return backupMetadata{}, err
			}
		}
		containersToRemove = containersOutsideDesiredRelease(existing, app, env, appCtx.Processes, meta.Release)
	} else {
		containersToRemove = appContainerNames(existing)
	}

	if err := writeAppCaddyfile(app, env, appCtx, meta.Release); err != nil {
		removeContainers(startedContainers)
		_ = restoreEnvFile(app, env, envSnapshot)
		_ = restoreStaticCurrent(app, env, staticSnapshot)
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		return backupMetadata{}, err
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}, ""); err != nil {
		removeContainers(startedContainers)
		_ = restoreEnvFile(app, env, envSnapshot)
		_ = restoreStaticCurrent(app, env, staticSnapshot)
		if restoreErr := restoreCaddyFragment(caddyPath, prevFragment, prevExisted); restoreErr != nil {
			return backupMetadata{}, fmt.Errorf("caddy validate after restore failed AND fragment restore failed (manual fix required at %s): %v (restore: %v)", caddyPath, err, restoreErr)
		}
		return backupMetadata{}, fmt.Errorf("caddy validate after restore: %v", err)
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, ""); err != nil {
		removeContainers(startedContainers)
		_ = restoreEnvFile(app, env, envSnapshot)
		_ = restoreStaticCurrent(app, env, staticSnapshot)
		_ = restoreCaddyFragment(caddyPath, prevFragment, prevExisted)
		return backupMetadata{}, fmt.Errorf("caddy reload after restore: %v", err)
	}
	removeContainers(containersToRemove)
	return meta, nil
}

func containersOutsideDesiredRelease(entries []containerEntry, app, env string, processes map[string]config.Process, release string) []string {
	desired := map[string]bool{}
	for name := range processes {
		desired[identity.ContainerName(app, env, name, release)] = true
	}
	var names []string
	for _, e := range entries {
		process := e.Labels["simple-vps.process"]
		if process == "" || process == "release" {
			continue
		}
		if len(e.Names) == 0 {
			continue
		}
		name := e.Names[0]
		if desired[name] {
			continue
		}
		names = append(names, name)
	}
	return uniqueContainerNames(names)
}

func writeBackupTar(path, app, env, manifestPath string, payload backupPayload, includeStatic bool) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	if err := addJSON(tw, "metadata.json", payload.Metadata); err != nil {
		return err
	}
	if err := addJSON(tw, "secrets.json", payload.Secrets); err != nil {
		return err
	}
	if err := addFile(tw, manifestPath, "simple-vps.toml"); err != nil {
		return err
	}
	if err := addDir(tw, identity.DataDir(app, env), "data"); err != nil {
		return err
	}
	if includeStatic {
		staticReleaseDir := filepath.Join(identity.StaticDir(app, env), "releases", payload.Metadata.Release)
		return addDir(tw, staticReleaseDir, filepath.ToSlash(filepath.Join("static", "releases", payload.Metadata.Release)))
	}
	return nil
}

func extractBackupTar(path, dest string) (backupPayload, error) {
	f, err := os.Open(path)
	if err != nil {
		return backupPayload{}, err
	}
	defer f.Close()
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return backupPayload{}, err
	}
	tr := tar.NewReader(f)
	var payload backupPayload
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return backupPayload{}, err
		}
		target, err := safeExtractPath(destAbs, h.Name)
		if err != nil {
			return backupPayload{}, fmt.Errorf("unsafe backup path %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(h.Mode)); err != nil {
				return backupPayload{}, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return backupPayload{}, err
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return backupPayload{}, err
			}
			switch h.Name {
			case "metadata.json":
				if err := json.Unmarshal(data, &payload.Metadata); err != nil {
					return backupPayload{}, err
				}
			case "secrets.json":
				if err := json.Unmarshal(data, &payload.Secrets); err != nil {
					return backupPayload{}, err
				}
			}
			if err := os.WriteFile(target, data, os.FileMode(h.Mode)); err != nil {
				return backupPayload{}, err
			}
		}
	}
	if payload.Metadata.SchemaVersion != 1 {
		return backupPayload{}, fmt.Errorf("unsupported backup schema version %d", payload.Metadata.SchemaVersion)
	}
	if payload.Secrets == nil {
		payload.Secrets = map[string]string{}
	}
	return payload, nil
}

func safeExtractPath(dest, name string) (string, error) {
	target, err := filepath.Abs(filepath.Join(dest, filepath.Clean(name)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(dest, target)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes destination")
	}
	return target, nil
}

func addJSON(tw *tar.Writer, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeTarFile(tw, name, data, 0600)
}

func addFile(tw *tar.Writer, src, name string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return writeTarFile(tw, name, data, info.Mode().Perm())
}

func addDir(tw *tar.Writer, src, prefix string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(prefix, rel))
		if rel == "." {
			return tw.WriteHeader(&tar.Header{Name: prefix + "/", Mode: 0755, Typeflag: tar.TypeDir})
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: name + "/", Mode: int64(info.Mode().Perm()), Typeflag: tar.TypeDir})
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return addFile(tw, path, name)
	})
}

func writeTarFile(tw *tar.Writer, name string, data []byte, mode os.FileMode) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode), Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func backupDir(app, env, dest string) (string, error) {
	if dest == "" {
		return filepath.Join(utils.BackupDir(), app, env), nil
	}
	if strings.HasPrefix(dest, "file://") {
		dest = strings.TrimPrefix(dest, "file://")
	}
	if strings.Contains(dest, "://") {
		return "", fmt.Errorf("only local file backup destinations are supported in this release")
	}
	return dest, nil
}

func resolveBackupPath(app, env, idOrPath, dir string) (string, error) {
	if idOrPath == "" {
		return "", fmt.Errorf("backup id/path is required")
	}
	if strings.HasPrefix(idOrPath, "file://") {
		idOrPath = strings.TrimPrefix(idOrPath, "file://")
	}
	if filepath.IsAbs(idOrPath) || strings.Contains(idOrPath, string(os.PathSeparator)) {
		return idOrPath, nil
	}
	base, err := backupDir(app, env, dir)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(idOrPath, ".tar") {
		return filepath.Join(base, idOrPath), nil
	}
	return filepath.Join(base, idOrPath+".tar"), nil
}

func listBackups(app, env, dir string) ([]backupInfo, error) {
	base, err := backupDir(app, env, dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []backupInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tar") {
			continue
		}
		path := filepath.Join(base, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		item := backupInfo{ID: strings.TrimSuffix(entry.Name(), ".tar"), Path: path, Size: info.Size()}
		if payload, err := readBackupMetadata(path); err == nil {
			item.CreatedAt = payload.CreatedAt
			item.Release = payload.Release
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func readBackupMetadata(path string) (backupMetadata, error) {
	tmp, err := os.MkdirTemp("", "simple-vps-backup-meta-")
	if err != nil {
		return backupMetadata{}, err
	}
	defer os.RemoveAll(tmp)
	payload, err := extractBackupTar(path, tmp)
	if err != nil {
		return backupMetadata{}, err
	}
	return payload.Metadata, nil
}

func readSecrets(app, env string) map[string]string {
	out := map[string]string{}
	keys, err := secrets.List(app, env)
	if err != nil {
		return out
	}
	for _, key := range keys {
		val, err := secrets.Get(app, env, key)
		if err == nil {
			out[key] = string(val)
		}
	}
	return out
}

func processNamesFromStatuses(processes []processStatus) []string {
	out := make([]string, 0, len(processes))
	for _, proc := range processes {
		out = append(out, proc.Process)
	}
	sort.Strings(out)
	return out
}

func staticRouteNames(routes map[string]config.Route) []string {
	var out []string
	for name, route := range routes {
		if route.Serve != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func ensureRestoreLayout(app, env string) error {
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
		return fmt.Errorf("chmod %s: %v", envRoot, err)
	}
	for _, dir := range []string{dataDir, staticDir} {
		if _, err := utils.RunChecked("chown", []string{"-R", user + ":" + user, dir}, ""); err != nil {
			return fmt.Errorf("chown %s: %v", dir, err)
		}
		if _, err := utils.RunChecked("chmod", []string{"2775", dir}, ""); err != nil {
			return fmt.Errorf("chmod %s: %v", dir, err)
		}
	}
	if _, err := utils.RunChecked("chown", []string{"-R", "root:root", releaseDir}, ""); err != nil {
		return fmt.Errorf("chown %s: %v", releaseDir, err)
	}
	if _, err := utils.RunChecked("chmod", []string{"0755", releaseDir}, ""); err != nil {
		return fmt.Errorf("chmod %s: %v", releaseDir, err)
	}
	if _, err := utils.RunChecked("chown", []string{"root:" + user, runtimeDir}, ""); err != nil {
		return fmt.Errorf("chown %s: %v", runtimeDir, err)
	}
	if _, err := utils.RunChecked("chmod", []string{"0750", runtimeDir}, ""); err != nil {
		return fmt.Errorf("chmod %s: %v", runtimeDir, err)
	}
	return nil
}

func restoreStaticRelease(app, env, extractedRoot, release string) error {
	if err := validateRelease(release); err != nil {
		return err
	}
	staticDir := identity.StaticDir(app, env)
	src := filepath.Join(extractedRoot, "static", "releases", release)
	dst := filepath.Join(staticDir, "releases", release)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := copyDir(src, dst); err != nil {
		return err
	}
	if err := chownAppDir(app, env, staticDir); err != nil {
		return err
	}
	current := filepath.Join(staticDir, "current")
	if err := os.Remove(current); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(dst, current)
}

func chownAppDir(app, env, dir string) error {
	user := identity.SystemUser(app, env)
	if _, err := utils.RunChecked("chown", []string{"-R", user + ":" + user, dir}, ""); err != nil {
		return fmt.Errorf("chown %s: %v", dir, err)
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFilePath(path, target, info.Mode().Perm())
	})
}

func copyFilePath(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}

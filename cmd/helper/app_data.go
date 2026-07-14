package helper

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

const (
	sqliteHeaderMagic = "SQLite format 3\x00"
)

type appDataCmd struct {
	Fork    appDataForkCmd    `cmd:"fork" help:"Fork prod /data into a preview env."`
	Rm      appDataRmCmd      `cmd:"rm" help:"Empty a preview env /data dir."`
	Save    appDataSaveCmd    `cmd:"save" help:"Stream a data snapshot to stdout."`
	Restore appDataRestoreCmd `cmd:"restore" help:"Restore a staged data snapshot."`
}

type appDataForkCmd struct {
	App        string `arg:"" help:"App name."`
	ProdEnv    string `arg:"" help:"Production env name."`
	PreviewEnv string `arg:"" help:"Preview env name."`
}

type appDataRmCmd struct {
	App        string `arg:"" help:"App name."`
	PreviewEnv string `arg:"" help:"Preview env name."`
}

type appDataSaveCmd struct {
	App string `arg:"" help:"App name."`
	Env string `arg:"" help:"Env name."`
}

type appDataRestoreCmd struct {
	App     string `arg:"" help:"App name."`
	Env     string `arg:"" help:"Env name."`
	Archive string `name:"archive" required:"" help:"Staged snapshot path."`
}

type dataSnapshotMetadata struct {
	SchemaVersion int    `json:"schema_version"`
	App           string `json:"app"`
	Env           string `json:"env"`
	Release       string `json:"release"`
	CreatedAt     string `json:"created_at"`
}

func (c appDataSaveCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbData, authTargetForAppEnv(c.App, c.Env, "data=save"))
	withAppEnvLock(c.App, c.Env, func() {
		if err := saveAppData(c.App, c.Env, os.Stdout, time.Now().UTC()); err != nil {
			utils.DieError(err, 1)
		}
	})
	return nil
}

func (c appDataRestoreCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbData, authTargetForAppEnv(c.App, c.Env, "data=restore"))
	if c.Env == productionEnvName {
		// Production data replacement is an owner act even though preview restores
		// are available to shippers through the data role.
		authorizeOrDie(helperVerbBoxMutation, authTargetForAppEnv(c.App, c.Env, "data=restore"))
	}
	withAppEnvLock(c.App, c.Env, func() {
		meta, err := restoreAppData(c.App, c.Env, c.Archive)
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Fprintf(os.Stderr, "restored data snapshot from %s (%s), release %s\n", meta.Env, meta.CreatedAt, meta.Release)
		if meta.Env != c.Env {
			fmt.Fprintf(os.Stderr, "note: snapshot env %s restored into %s\n", meta.Env, c.Env)
		}
	})
	return nil
}

type dataForkSummary struct {
	Files       []dataForkFile `json:"files"`
	SQLiteFiles int            `json:"sqliteFiles"`
	Bytes       int64          `json:"bytes"`
}

type dataForkFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SQLite bool   `json:"sqlite"`
}

type dataForkOptions struct {
	BeforeSwap func() error
}

func (c appDataForkCmd) Run() error {
	if err := validateAppEnv(c.App, c.ProdEnv); err != nil {
		utils.DieError(err, 1)
	}
	if err := validateAppEnv(c.App, c.PreviewEnv); err != nil {
		utils.DieError(err, 1)
	}
	if c.PreviewEnv == productionEnvName || c.ProdEnv != productionEnvName {
		utils.DieError(dataForkOnProductionError("helper"), 1)
	}
	if err := ensureDataPreviewTarget(c.App, c.PreviewEnv); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbData, authTargetForAppEnv(c.App, c.PreviewEnv, "data=fork", "from="+c.ProdEnv))
	withAppEnvLock(c.App, c.PreviewEnv, func() {
		summary, err := forkAppData(c.App, c.ProdEnv, c.PreviewEnv, dataForkOptions{})
		if err != nil {
			utils.DieError(err, 1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(summary); err != nil {
			utils.DieError(err, 1)
		}
	})
	return nil
}

func (c appDataRmCmd) Run() error {
	if err := validateAppEnv(c.App, c.PreviewEnv); err != nil {
		utils.DieError(err, 1)
	}
	if c.PreviewEnv == productionEnvName {
		utils.DieError(dataForkOnProductionError("helper"), 1)
	}
	if err := ensureDataPreviewTarget(c.App, c.PreviewEnv); err != nil {
		utils.DieError(err, 1)
	}
	authorizeOrDie(helperVerbData, authTargetForAppEnv(c.App, c.PreviewEnv, "data=rm"))
	withAppEnvLock(c.App, c.PreviewEnv, func() {
		if err := resetAppData(c.App, c.PreviewEnv); err != nil {
			utils.DieError(err, 1)
		}
		fmt.Printf("Reset data for %s (%s)\n", c.App, c.PreviewEnv)
	})
	return nil
}

func forkAppData(app, prodEnv, previewEnv string, opts dataForkOptions) (summary dataForkSummary, err error) {
	if prodEnv != productionEnvName || previewEnv == productionEnvName {
		return dataForkSummary{}, dataForkOnProductionError("helper")
	}
	if err := ensureDataPreviewTarget(app, previewEnv); err != nil {
		return dataForkSummary{}, err
	}
	if err := applyEnvLayoutPerms(app, previewEnv); err != nil {
		return dataForkSummary{}, err
	}
	prodData := identity.DataDir(app, prodEnv)
	if info, err := os.Stat(prodData); err != nil {
		return dataForkSummary{}, fmt.Errorf("read production data dir: %v", err)
	} else if !info.IsDir() {
		return dataForkSummary{}, fmt.Errorf("production data path is not a directory")
	}

	tmp, err := os.MkdirTemp(identity.EnvRoot(app, previewEnv), ".data-fork-")
	if err != nil {
		return dataForkSummary{}, err
	}
	swapped := false
	defer func() {
		if !swapped {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := os.Chmod(tmp, 02775); err != nil {
		return dataForkSummary{}, err
	}

	summary, err = copyDataDirForFork(prodData, tmp)
	if err != nil {
		return dataForkSummary{}, err
	}
	if err := chownAppDir(app, previewEnv, tmp); err != nil {
		return dataForkSummary{}, err
	}
	if _, err := utils.RunChecked("chmod", []string{"2775", tmp}, ""); err != nil {
		return dataForkSummary{}, fmt.Errorf("chmod %s: %v", tmp, err)
	}
	if opts.BeforeSwap != nil {
		if err := opts.BeforeSwap(); err != nil {
			return dataForkSummary{}, err
		}
	}

	stopped, err := stopRunningAppContainers(app, previewEnv)
	if err != nil {
		warnOnRestartFailure(stopped)
		return dataForkSummary{}, err
	}
	defer warnOnRestartFailure(stopped)

	previewData := identity.DataDir(app, previewEnv)
	if err := exchangeDirs(previewData, tmp); err != nil {
		return dataForkSummary{}, fmt.Errorf("swap preview data dir: %v", err)
	}
	swapped = true
	_ = os.RemoveAll(tmp)
	// The swap above already succeeded; a layout-perms failure now is a
	// warning, not a fork failure (mirrors restoreAppData).
	if err := applyEnvLayoutPerms(app, previewEnv); err != nil {
		fmt.Fprintf(os.Stderr, "warning: data forked but layout perms need attention: %v\n", err)
	}
	return summary, nil
}

func resetAppData(app, previewEnv string) error {
	if previewEnv == productionEnvName {
		return dataForkOnProductionError("helper")
	}
	if err := ensureDataPreviewTarget(app, previewEnv); err != nil {
		return err
	}
	if err := applyEnvLayoutPerms(app, previewEnv); err != nil {
		return err
	}
	stopped, err := stopRunningAppContainers(app, previewEnv)
	if err != nil {
		warnOnRestartFailure(stopped)
		return err
	}
	defer warnOnRestartFailure(stopped)

	dataDir := identity.DataDir(app, previewEnv)
	if err := emptyDirContents(dataDir); err != nil {
		return err
	}
	// /data is already emptied; a layout-perms failure now is a warning,
	// not a reset failure (mirrors restoreAppData).
	if err := applyEnvLayoutPerms(app, previewEnv); err != nil {
		fmt.Fprintf(os.Stderr, "warning: data reset but layout perms need attention: %v\n", err)
	}
	return nil
}

func copyDataDirForFork(srcRoot, dstRoot string) (dataForkSummary, error) {
	var summary dataForkSummary
	err := filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dstRoot, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}

		// /data is owned by the unprivileged app user, so a symlink here
		// is attacker-controlled. Never follow it as root: preserve it as
		// a symlink so the tar writer and restore path (which reject
		// escaping targets) handle it, instead of copying the link target.
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
			summary.Files = append(summary.Files, dataForkFile{Path: filepath.ToSlash(rel)})
			return nil
		}

		sqlite, err := isSQLiteDataFile(path)
		if err != nil {
			return err
		}
		if sqlite {
			if _, err := exec.LookPath("sqlite3"); err != nil {
				return errcat.New(errcat.CodeMissingTool, errcat.Fields{"tool": "sqlite3"})
			}
			if err := vacuumSQLiteInto(path, target); err != nil {
				return err
			}
			info, err = os.Lstat(target)
			if err != nil {
				return err
			}
			summary.SQLiteFiles++
		} else if err := copyPathWithReflink(path, target); err != nil {
			return err
		}
		item := dataForkFile{
			Path:   filepath.ToSlash(rel),
			Size:   info.Size(),
			SQLite: sqlite,
		}
		summary.Files = append(summary.Files, item)
		summary.Bytes += item.Size
		return nil
	})
	if err != nil {
		return dataForkSummary{}, err
	}
	sort.Slice(summary.Files, func(i, j int) bool {
		return summary.Files[i].Path < summary.Files[j].Path
	})
	return summary, nil
}

func isSQLiteDataFile(path string) (bool, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".db", ".sqlite", ".sqlite3":
	default:
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var header [16]byte
	n, err := f.Read(header[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return n == len(header) && string(header[:]) == sqliteHeaderMagic, nil
}

func vacuumSQLiteInto(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	sql := "VACUUM INTO " + sqliteStringLiteral(dst)
	if _, err := utils.RunChecked("sqlite3", []string{src, sql}, ""); err != nil {
		return fmt.Errorf("sqlite3 VACUUM INTO: %v", err)
	}
	return nil
}

func sqliteStringLiteral(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "''") + "'"
}

func copyPathWithReflink(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if _, err := utils.RunChecked("cp", []string{"-a", "--reflink=auto", src, dst}, ""); err == nil {
			return nil
		}
	}
	if _, err := utils.RunChecked("cp", []string{"-a", src, dst}, ""); err != nil {
		return fmt.Errorf("cp -a: %v", err)
	}
	return nil
}

func stopRunningAppContainers(app, env string) ([]string, error) {
	entries, err := podmanPSContainers(app, env)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.State != "running" || len(entry.Names) == 0 {
			continue
		}
		process := entry.Labels["ship.process"]
		if process == "" || isEphemeralProcess(process) {
			continue
		}
		names = append(names, entry.Names[0])
	}
	names = uniqueContainerNames(names)
	return stopContainers(names)
}

func emptyDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func ensureDataPreviewTarget(app, env string) error {
	if env == productionEnvName {
		return dataForkOnProductionError("helper")
	}
	file, err := readEnvIdentity(app, env)
	if err != nil || file.Preview == nil {
		return errcat.New(errcat.CodeNoPreviewEnv, errcat.Fields{"branch": fmt.Sprintf("%q", env)})
	}
	return nil
}

func dataForkOnProductionError(branch string) error {
	if branch == "" {
		branch = "Production"
	}
	return errcat.New(errcat.CodeDataForkOnProduction, errcat.Fields{"branch": fmt.Sprintf("%q", branch)})
}

func saveAppData(app, env string, out io.Writer, now time.Time) error {
	if err := sweepDataSnapshotStaging(app, env); err != nil {
		return err
	}
	if err := applyEnvLayoutPerms(app, env); err != nil {
		return err
	}
	dataDir := identity.DataDir(app, env)
	if info, err := os.Stat(dataDir); err != nil {
		return fmt.Errorf("read data dir: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("data path is not a directory: %s", dataDir)
	}
	work, err := os.MkdirTemp(identity.EnvRoot(app, env), ".data-save-")
	if err != nil {
		return fmt.Errorf("create data snapshot staging: %w", err)
	}
	defer os.RemoveAll(work)
	if err := os.Chmod(work, 02775); err != nil {
		return err
	}
	stagedData := filepath.Join(work, "data")
	if err := os.MkdirAll(stagedData, 0755); err != nil {
		return err
	}
	summary, err := copyDataDirForFork(dataDir, stagedData)
	if err != nil {
		return fmt.Errorf("stage data snapshot: %w", err)
	}
	release, err := currentDataRelease(app, env)
	if err != nil {
		return err
	}
	meta := dataSnapshotMetadata{SchemaVersion: 1, App: app, Env: env, Release: release, CreatedAt: now.UTC().Format(time.RFC3339)}
	for _, file := range summary.Files {
		kind := "file"
		if file.SQLite {
			kind = "sqlite"
		}
		fmt.Fprintf(os.Stderr, "%s %s %d bytes\n", kind, file.Path, file.Size)
	}
	if err := writeDataSnapshotTar(out, work, meta); err != nil {
		fmt.Fprintf(os.Stderr, "data save failed: %v\n", err)
		return fmt.Errorf("write data snapshot: %w", err)
	}
	return nil
}

func sweepDataSnapshotStaging(app, env string) error {
	root := identity.EnvRoot(app, env)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read data snapshot staging: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".data-save-") && !strings.HasPrefix(entry.Name(), ".data-restore-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("remove data snapshot staging %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func currentDataRelease(app, env string) (string, error) {
	ctx, cleanup, err := loadAppliedAppContext(app, env)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if ctx.NeedsImage {
		containers, err := podmanPSContainers(app, env)
		if err != nil {
			return "", err
		}
		return currentRelease(runningProcesses(containersToProcesses(containers)))
	}
	return currentStaticRelease(app, env)
}

func writeDataSnapshotTar(out io.Writer, work string, meta dataSnapshotMetadata) error {
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	close := func() error {
		if err := tw.Close(); err != nil {
			return err
		}
		return gz.Close()
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := writeDataTarFile(tw, "metadata.json", append(metaBytes, '\n'), 0600); err != nil {
		return err
	}
	if err := writeDataTarDir(tw, filepath.Join(work, "data"), "data"); err != nil {
		return err
	}
	return close()
}

func writeDataTarDir(tw *tar.Writer, root, prefix string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := prefix
		if rel != "." {
			name = filepath.ToSlash(filepath.Join(prefix, rel))
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: name + "/", Mode: int64(info.Mode().Perm()), Typeflag: tar.TypeDir})
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return tw.WriteHeader(&tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Typeflag: tar.TypeSymlink, Linkname: link})
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func writeDataTarFile(tw *tar.Writer, name string, data []byte, mode os.FileMode) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode), Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func restoreAppData(app, env, archive string) (dataSnapshotMetadata, error) {
	archive, err := host.ValidateDeployTmpSource(archive)
	if err != nil {
		return dataSnapshotMetadata{}, err
	}
	defer os.Remove(archive)
	if err := sweepDataSnapshotStaging(app, env); err != nil {
		return dataSnapshotMetadata{}, err
	}
	dataDir := identity.DataDir(app, env)
	if info, err := os.Stat(dataDir); err != nil {
		return dataSnapshotMetadata{}, fmt.Errorf("read data dir: %w", err)
	} else if !info.IsDir() {
		return dataSnapshotMetadata{}, fmt.Errorf("data path is not a directory: %s", dataDir)
	}
	stage, err := os.MkdirTemp(identity.EnvRoot(app, env), ".data-restore-")
	if err != nil {
		return dataSnapshotMetadata{}, fmt.Errorf("create restore staging: %w", err)
	}
	defer os.RemoveAll(stage)
	meta, err := extractDataSnapshot(archive, stage)
	if err != nil {
		return dataSnapshotMetadata{}, err
	}
	if meta.App != app {
		return dataSnapshotMetadata{}, invalidDataSnapshot("snapshot is for app " + meta.App)
	}
	stagedData := filepath.Join(stage, "data")
	if err := chownAppDir(app, env, stagedData); err != nil {
		return dataSnapshotMetadata{}, err
	}
	if _, err := utils.RunChecked("chmod", []string{"2775", stagedData}, ""); err != nil {
		return dataSnapshotMetadata{}, fmt.Errorf("chmod %s: %w", stagedData, err)
	}

	stopped, err := stopRunningAppContainers(app, env)
	if err != nil {
		warnOnRestartFailure(stopped)
		return dataSnapshotMetadata{}, err
	}
	defer warnOnRestartFailure(stopped)
	// Validation and staging complete above; nothing may mutate live /data before this exchange.
	if err := exchangeDirs(dataDir, stagedData); err != nil {
		return dataSnapshotMetadata{}, fmt.Errorf("swap restored data dir: %w", err)
	}
	if err := applyEnvLayoutPerms(app, env); err != nil {
		fmt.Fprintf(os.Stderr, "warning: data restored but layout perms need attention: %v\n", err)
	}
	return meta, nil
}

func extractDataSnapshot(path, dest string) (dataSnapshotMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return dataSnapshotMetadata{}, invalidDataSnapshot("cannot open snapshot")
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return dataSnapshotMetadata{}, invalidDataSnapshot("not a gzip tar archive")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return dataSnapshotMetadata{}, err
	}
	var meta dataSnapshotMetadata
	seenMetadata, seenData := false, false
	type pendingLink struct{ path, target string }
	var links []pendingLink
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return dataSnapshotMetadata{}, invalidDataSnapshot("cannot read tar payload")
		}
		name := strings.TrimSuffix(filepath.ToSlash(h.Name), "/")
		if name == "" || (name != "metadata.json" && name != "data" && !strings.HasPrefix(name, "data/")) {
			return dataSnapshotMetadata{}, invalidDataSnapshot("unexpected archive path " + h.Name)
		}
		for _, part := range strings.Split(name, "/") {
			if part == "." || part == ".." {
				return dataSnapshotMetadata{}, invalidDataSnapshot("archive path escapes data payload")
			}
		}
		if name == "metadata.json" {
			if seenMetadata || h.Typeflag != tar.TypeReg {
				return dataSnapshotMetadata{}, invalidDataSnapshot("metadata.json is invalid")
			}
			data, readErr := io.ReadAll(tr)
			if readErr != nil || json.Unmarshal(data, &meta) != nil {
				return dataSnapshotMetadata{}, invalidDataSnapshot("metadata.json cannot be read")
			}
			seenMetadata = true
			continue
		}
		if name == "data" {
			if seenData || h.Typeflag != tar.TypeDir {
				return dataSnapshotMetadata{}, invalidDataSnapshot("data/ directory is missing")
			}
			seenData = true
		}
		target, err := safeDataExtractPath(destAbs, h.Name)
		if err != nil {
			return dataSnapshotMetadata{}, invalidDataSnapshot("archive path escapes data payload")
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, os.FileMode(h.Mode))
		case tar.TypeReg:
			err = os.MkdirAll(filepath.Dir(target), 0755)
			if err == nil {
				var out *os.File
				out, err = os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode))
				if err == nil {
					_, err = io.Copy(out, tr)
					closeErr := out.Close()
					if err == nil {
						err = closeErr
					}
				}
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) {
				return dataSnapshotMetadata{}, invalidDataSnapshot("absolute symlink target")
			}
			linkTarget, linkErr := safeDataExtractPath(destAbs, filepath.Join(filepath.Dir(h.Name), h.Linkname))
			if linkErr != nil {
				return dataSnapshotMetadata{}, invalidDataSnapshot("symlink escapes data payload")
			}
			rel, relErr := filepath.Rel(filepath.Join(destAbs, "data"), linkTarget)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
				return dataSnapshotMetadata{}, invalidDataSnapshot("symlink escapes data payload")
			}
			links = append(links, pendingLink{path: target, target: h.Linkname})
			continue
		default:
			return dataSnapshotMetadata{}, invalidDataSnapshot("unsupported archive entry")
		}
		if err != nil {
			return dataSnapshotMetadata{}, invalidDataSnapshot("cannot extract data payload")
		}
	}
	if !seenMetadata || !seenData {
		return dataSnapshotMetadata{}, invalidDataSnapshot("metadata.json or data/ directory is missing")
	}
	if meta.SchemaVersion != 1 || meta.App == "" || meta.Env == "" || meta.Release == "" || meta.CreatedAt == "" {
		return dataSnapshotMetadata{}, invalidDataSnapshot("metadata.json has an unsupported schema")
	}
	if _, err := time.Parse(time.RFC3339, meta.CreatedAt); err != nil {
		return dataSnapshotMetadata{}, invalidDataSnapshot("metadata.json has an invalid created_at")
	}
	for _, link := range links {
		if err := os.MkdirAll(filepath.Dir(link.path), 0755); err != nil {
			return dataSnapshotMetadata{}, invalidDataSnapshot("cannot extract symlink")
		}
		if err := os.Symlink(link.target, link.path); err != nil {
			return dataSnapshotMetadata{}, invalidDataSnapshot("cannot extract symlink")
		}
	}
	return meta, nil
}

func safeDataExtractPath(dest, name string) (string, error) {
	target, err := filepath.Abs(filepath.Join(dest, filepath.Clean(name)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(dest, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes destination")
	}
	return target, nil
}

func invalidDataSnapshot(detail string) error {
	return errcat.New(errcat.CodeDataSnapshotInvalid, errcat.Fields{"detail": detail})
}

func chownAppDir(app, env, dir string) error {
	user := identity.SystemUser(app, env)
	if _, err := utils.RunChecked("chown", []string{"-R", user + ":" + user, dir}, ""); err != nil {
		return fmt.Errorf("chown %s: %w", dir, err)
	}
	return nil
}

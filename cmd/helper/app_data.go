package helper

import (
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

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

const (
	sqliteHeaderMagic = "SQLite format 3\x00"
)

type appDataCmd struct {
	Fork appDataForkCmd `cmd:"fork" help:"Fork prod /data into a preview env."`
	Rm   appDataRmCmd   `cmd:"rm" help:"Empty a preview env /data dir."`
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
		return dataForkSummary{}, err
	}
	defer startContainers(stopped)

	previewData := identity.DataDir(app, previewEnv)
	if err := exchangeDirs(previewData, tmp); err != nil {
		return dataForkSummary{}, fmt.Errorf("swap preview data dir: %v", err)
	}
	swapped = true
	_ = os.RemoveAll(tmp)
	if err := applyEnvLayoutPerms(app, previewEnv); err != nil {
		return dataForkSummary{}, err
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
		return err
	}
	defer startContainers(stopped)

	dataDir := identity.DataDir(app, previewEnv)
	if err := emptyDirContents(dataDir); err != nil {
		return err
	}
	return applyEnvLayoutPerms(app, previewEnv)
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
	return names, stopContainers(names)
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

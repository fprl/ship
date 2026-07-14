package client

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
)

const (
	DataForkPIINote      = "note: Production data, including any PII, now exists in this less-guarded Preview."
	DataForkNoSQLiteNote = "note: No SQLite files found; copied non-database files from /data only."
)

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

type dataContext struct {
	AppContext    *config.AppContext
	PreviewBranch string
	EnvName       string
	Runner        dataRunner
}

type dataRunner interface {
	sshRunner
	Close()
}

type dataRestoreRunner interface {
	sshRunner
	Upload(local, remote, server string) error
}

type dataRestoreContext struct {
	AppContext *config.AppContext
	Address    readAddress
	EnvName    string
	Runner     dataRestoreRunner
}

type dataSaveRunner interface {
	RunSSHToFile(server, command, path string) (string, string, int, error)
}

type dataSaveContext struct {
	AppContext *config.AppContext
	EnvName    string
	Runner     dataSaveRunner
}

type dataSnapshotMetadata struct {
	SchemaVersion int    `json:"schema_version"`
	App           string `json:"app"`
	Env           string `json:"env"`
	Release       string `json:"release"`
	CreatedAt     string `json:"created_at"`
}

type dataSnapshotInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created string `json:"created"`
	Env     string `json:"env"`
	Release string `json:"release"`
}

type dataForkResult struct {
	Summary      dataForkSummary
	URL          string
	URLLookupErr error
}

type dataResetResult struct {
	URL          string
	URLLookupErr error
}

func CmdDataFork(root string) {
	data, err := currentDataContext(root)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer data.Runner.Close()

	result, err := runDataFork(data)
	if err != nil {
		utils.DieError(err, 1)
	}
	stdout, stderr := renderDataForkOutput(data.PreviewBranch, result)
	fmt.Fprint(os.Stderr, stderr)
	fmt.Print(stdout)
}

func CmdDataReset(root string) {
	data, err := currentDataContext(root)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer data.Runner.Close()

	result, err := runDataReset(data)
	if err != nil {
		utils.DieError(err, 1)
	}
	stdout, stderr := renderDataResetOutput(data.PreviewBranch, result)
	fmt.Fprint(os.Stderr, stderr)
	fmt.Print(stdout)
}

func CmdDataSave(root, outPath string) {
	read, err := currentReadContext(root, "data save")
	if err != nil {
		utils.DieError(err, 1)
	}
	defer read.Runner.Close()
	savedPath, err := runDataSave(dataSaveContext{AppContext: read.AppContext, EnvName: read.EnvName, Runner: read.Runner}, outPath, time.Now().UTC())
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Println(savedPath)
}

func runDataSave(data dataSaveContext, outPath string, now time.Time) (string, error) {
	dir := filepath.Dir(outPath)
	if outPath == "" {
		var err error
		dir, err = dataSnapshotDir(data.AppContext.AppName)
		if err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", operationError(fmt.Sprintf("create snapshot directory: %v", err), "ship data save")
	}
	// Stream into a temporary file, then hard-link the completed archive to a
	// unique final name so a save never replaces an existing snapshot.
	tmp, err := os.CreateTemp(dir, ".ship-data-save-*.partial")
	if err != nil {
		return "", operationError(fmt.Sprintf("create snapshot temp file: %v", err), "ship data save")
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	failure, stderr, code, runErr := data.Runner.RunSSHToFile(data.AppContext.Server, serverAppDataSaveCommand(data.AppContext.AppName, data.EnvName), tmpPath)
	if runErr != nil || code != 0 {
		_ = os.Remove(tmpPath)
		outcome := decodeRemoteOutcome(failure, stderr, code, runErr, "data save failed")
		if outcome.TransportCoded != nil {
			return "", outcome.TransportCoded
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			return "", outcome.RemoteCoded
		}
		return "", operationError("data save failed: "+outcome.Detail, "ship data save")
	}
	if outPath == "" {
		meta, err := readDataSnapshotMetadata(tmpPath)
		if err != nil {
			_ = os.Remove(tmpPath)
			return "", operationError(fmt.Sprintf("read snapshot metadata: %v", err), "ship data save")
		}
		outPath, err = defaultDataSnapshotPath(data.AppContext.AppName, meta.Env, meta.Release, now)
		if err != nil {
			_ = os.Remove(tmpPath)
			return "", err
		}
	}
	finalPath, err := claimDataSnapshotPath(tmpPath, outPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", operationError(fmt.Sprintf("finalize snapshot: %v", err), "ship data save")
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	return finalPath, nil
}

func CmdDataRestore(root, idOrPath, confirm string) {
	if err := RunDataRestore(root, idOrPath, confirm); err != nil {
		utils.DieError(err, 1)
	}
}

func RunDataRestore(root, idOrPath, confirm string) error {
	read, err := currentReadContext(root, "data restore")
	if err != nil {
		return err
	}
	defer read.Runner.Close()
	return runDataRestore(dataRestoreContext{
		AppContext: read.AppContext,
		Address:    read.Address,
		EnvName:    read.EnvName,
		Runner:     read.Runner,
	}, idOrPath, confirm)
}

func runDataRestore(data dataRestoreContext, idOrPath, confirm string) error {
	if data.Address.ProductionBranch && confirm != data.AppContext.AppName {
		return errcat.New(errcat.CodeRmConfirmationRequired, errcat.Fields{
			"app": data.AppContext.AppName, "branch": data.AppContext.ProductionBranch,
		})
	}
	path, err := resolveDataSnapshotPath(data.AppContext.AppName, idOrPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return operationError(fmt.Sprintf("read snapshot %s: %v", path, err), "ship data ls")
	}
	// Stage under a unique subdir with the same mkdir+chmod+rm-rf shape the
	// deploy path uses, so an agent member's forced shell allows it (a bare
	// mkdir on the parent, or rm -f on a file, is outside the agent allowlist).
	remoteDir := fmt.Sprintf("%s/data-restore-%s-%d", RemoteDeployTmpDir, data.EnvName, time.Now().UnixNano())
	remote := remoteDir + "/snapshot.data.tar.gz"
	mkdirCmd := fmt.Sprintf("mkdir -p %s && chmod 0700 %s", utils.ShellEscape(remoteDir), utils.ShellEscape(remoteDir))
	defer func() {
		_, _, _, _ = data.Runner.RunSSH(data.AppContext.Server, "rm -rf "+utils.ShellEscape(remoteDir))
	}()
	if _, err := runSSHRequired(data.Runner, data.AppContext.Server, mkdirCmd, "create snapshot staging failed", "ship data restore"); err != nil {
		return err
	}
	if err := data.Runner.Upload(path, remote, data.AppContext.Server); err != nil {
		return operationError(fmt.Sprintf("upload snapshot: %v", err), "ship data restore")
	}
	stdout, stderr, code, runErr := data.Runner.RunSSH(data.AppContext.Server, serverAppDataRestoreCommand(data.AppContext.AppName, data.EnvName, remote))
	if runErr != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, runErr, "data restore failed")
		if outcome.TransportCoded != nil {
			return outcome.TransportCoded
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			return outcome.RemoteCoded
		}
		return operationError("data restore failed: "+outcome.Detail, "ship data ls")
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	return nil
}

func CmdDataLs(root string, jsonFlag bool) {
	ctx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		utils.DieError(err, 1)
	}
	items, err := listDataSnapshots(ctx.AppName)
	if err != nil {
		utils.DieError(err, 1)
	}
	if jsonFlag {
		data, err := json.MarshalIndent(struct {
			Snapshots []dataSnapshotInfo `json:"snapshots"`
		}{items}, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(data))
		return
	}
	for _, item := range items {
		fmt.Printf("%s  %d  %s  %s  %s\n", item.Name, item.Size, item.Created, item.Env, item.Release)
	}
}

func dataSnapshotDir(app string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ship", "backups", app), nil
}

func defaultDataSnapshotPath(app, env, release string, now time.Time) (string, error) {
	dir, err := dataSnapshotDir(app)
	if err != nil {
		return "", err
	}
	// Collision suffixes are allocated by claimDataSnapshotPath at link
	// time — the only claim that can actually reserve a name.
	return filepath.Join(dir, fmt.Sprintf("%s-%s-%s.data.tar.gz", env, release, now.UTC().Format("20060102T150405Z"))), nil
}

func claimDataSnapshotPath(tmpPath, path string) (string, error) {
	const extension = ".data.tar.gz"
	base, suffixExtension := path, ""
	if strings.HasSuffix(path, extension) {
		base, suffixExtension = strings.TrimSuffix(path, extension), extension
	}
	for suffix := 1; ; suffix++ {
		candidate := base + suffixExtension
		if suffix > 1 {
			candidate = base + "-" + strconv.Itoa(suffix) + suffixExtension
		}
		if err := os.Link(tmpPath, candidate); err == nil {
			if err := os.Remove(tmpPath); err != nil {
				return "", err
			}
			return candidate, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
}

func resolveDataSnapshotPath(app, idOrPath string) (string, error) {
	if idOrPath == "" {
		return "", usageError("ship data restore requires a snapshot ID or path", "ship data ls")
	}
	if filepath.IsAbs(idOrPath) || strings.Contains(idOrPath, string(os.PathSeparator)) {
		return idOrPath, nil
	}
	dir, err := dataSnapshotDir(app)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(idOrPath, ".data.tar.gz") {
		idOrPath += ".data.tar.gz"
	}
	return filepath.Join(dir, idOrPath), nil
}

func listDataSnapshots(app string) ([]dataSnapshotInfo, error) {
	dir, err := dataSnapshotDir(app)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []dataSnapshotInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := []dataSnapshotInfo{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".data.tar.gz") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		item := dataSnapshotInfo{ID: strings.TrimSuffix(entry.Name(), ".data.tar.gz"), Name: entry.Name(), Size: info.Size(), Created: info.ModTime().UTC().Format(time.RFC3339)}
		if meta, err := readDataSnapshotMetadata(filepath.Join(dir, entry.Name())); err == nil {
			item.Created, item.Env, item.Release = meta.CreatedAt, meta.Env, meta.Release
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name > items[j].Name })
	return items, nil
}

func readDataSnapshotMetadata(path string) (dataSnapshotMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return dataSnapshotMetadata{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return dataSnapshotMetadata{}, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return dataSnapshotMetadata{}, err
		}
		if h.Name != "metadata.json" {
			continue
		}
		var meta dataSnapshotMetadata
		if err := json.NewDecoder(tr).Decode(&meta); err != nil {
			return dataSnapshotMetadata{}, err
		}
		return meta, nil
	}
	return dataSnapshotMetadata{}, fmt.Errorf("metadata.json missing")
}

func currentDataContext(root string) (dataContext, error) {
	baseCtx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		return dataContext{}, err
	}
	state, err := currentGitState(root)
	if err != nil {
		return dataContext{}, err
	}
	if state.Detached {
		return dataContext{}, errcat.New(errcat.CodeDetachedHeadRequiresBranch, errcat.Fields{"command": "git checkout <branch>"})
	}
	envName, err := envNameForBranch(state.Branch, baseCtx.ProductionBranch)
	if err != nil {
		return dataContext{}, err
	}
	if state.Branch == baseCtx.ProductionBranch {
		return dataContext{}, errcat.New(errcat.CodeDataForkOnProduction, errcat.Fields{"branch": fmt.Sprintf("%q", state.Branch)})
	}
	runner, err := NewCommandRunner()
	if err != nil {
		return dataContext{}, err
	}
	resolved, err := resolvePreviewEnv(runner, baseCtx, state.Branch, false)
	if err != nil {
		runner.Close()
		if errcat.Is(err, errcat.CodeUnknownPreviewBranch) {
			return dataContext{}, noPreviewEnvError(state.Branch)
		}
		return dataContext{}, err
	}
	if resolved == productionEnvName || envName == productionEnvName {
		runner.Close()
		return dataContext{}, errcat.New(errcat.CodeDataForkOnProduction, errcat.Fields{"branch": fmt.Sprintf("%q", state.Branch)})
	}
	return dataContext{AppContext: baseCtx, PreviewBranch: state.Branch, EnvName: resolved, Runner: runner}, nil
}

func noPreviewEnvError(branch string) error {
	return errcat.New(errcat.CodeNoPreviewEnv, errcat.Fields{"branch": fmt.Sprintf("%q", branch)})
}

func dataPreviewURL(data dataContext) (string, error) {
	url, err := liveEnvURL(data.Runner, data.AppContext.Server, data.AppContext.AppName, data.EnvName)
	if err != nil {
		return "", err
	}
	if url != "" {
		return url, nil
	}
	boxIP := resolveBoxIPv4(data.Runner, data.AppContext.Server)
	plan, err := prepareDeployRoutes(data.AppContext, data.EnvName, deployRouteOptions{
		Preview: true,
		TLS:     "",
		BoxIP:   boxIP,
	})
	if err != nil {
		return "", err
	}
	return deploymentURLForBoxIP(plan.Context, data.EnvName, boxIP), nil
}

func runDataFork(data dataContext) (dataForkResult, error) {
	out, err := runSSHDetail(data.Runner, data.AppContext.Server, serverAppDataForkCommand(data.AppContext.AppName, productionEnvName, data.EnvName))
	if err != nil {
		return dataForkResult{}, err
	}
	var summary dataForkSummary
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &summary); err != nil {
		return dataForkResult{}, operationError(fmt.Sprintf("data fork failed: invalid helper JSON: %v", err), "ship data fork")
	}
	url, err := dataPreviewURL(data)
	return dataForkResult{Summary: summary, URL: url, URLLookupErr: err}, nil
}

func runDataReset(data dataContext) (dataResetResult, error) {
	if _, err := runSSHDetail(data.Runner, data.AppContext.Server, serverAppDataResetCommand(data.AppContext.AppName, data.EnvName)); err != nil {
		return dataResetResult{}, err
	}
	url, err := dataPreviewURL(data)
	return dataResetResult{URL: url, URLLookupErr: err}, nil
}

func liveEnvURL(runner sshRunner, server, app, env string) (string, error) {
	out, err := runSSHDetail(runner, server, serverAppListCommand(true))
	if err != nil {
		return "", err
	}
	var payload appListJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		return "", operationError(fmt.Sprintf("data command failed: invalid app list JSON: %v", err), "ship status")
	}
	for _, item := range payload.Apps {
		if item.App != app {
			continue
		}
		for _, envItem := range item.Envs {
			if envItem.Env == env {
				if envItem.CapabilityURL != "" {
					return envItem.CapabilityURL, nil
				}
				return envItem.URL, nil
			}
		}
	}
	return "", nil
}

func renderDataForkSummary(branch string, summary dataForkSummary) string {
	sort.Slice(summary.Files, func(i, j int) bool {
		return summary.Files[i].Path < summary.Files[j].Path
	})
	var b strings.Builder
	fmt.Fprintf(&b, "Forked data for Preview %s\n", branch)
	if len(summary.Files) == 0 {
		b.WriteString("files: none\n")
	} else {
		b.WriteString("files:\n")
		for _, file := range summary.Files {
			suffix := ""
			if file.SQLite {
				suffix = " (sqlite)"
			}
			fmt.Fprintf(&b, "  %s %s%s\n", file.Path, formatDataSize(file.Size), suffix)
		}
	}
	if summary.SQLiteFiles == 0 {
		b.WriteString(DataForkNoSQLiteNote)
		b.WriteByte('\n')
	}
	b.WriteString(DataForkPIINote)
	b.WriteByte('\n')
	return b.String()
}

func renderDataForkOutput(branch string, result dataForkResult) (string, string) {
	stderr := renderDataForkSummary(branch, result.Summary)
	if result.URLLookupErr != nil {
		return "", stderr + fmt.Sprintf("warning: preview URL lookup failed: %v\nnext: ship status\n", result.URLLookupErr)
	}
	return result.URL + "\n", stderr
}

func renderDataResetOutput(branch string, result dataResetResult) (string, string) {
	stderr := fmt.Sprintf("Reset data for Preview %s\n", branch)
	if result.URLLookupErr != nil {
		return "", stderr + fmt.Sprintf("warning: preview URL lookup failed: %v\nnext: ship status\n", result.URLLookupErr)
	}
	return result.URL + "\n", stderr
}

func formatDataSize(size int64) string {
	if size == 1 {
		return "1 byte"
	}
	return fmt.Sprintf("%d bytes", size)
}

package client

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
	Runner        *CommandRunner
}

func CmdDataFork(root string) {
	data, err := currentDataContext(root)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer data.Runner.Close()

	out, err := runSSHDetail(data.Runner, data.AppContext.Server, serverAppDataForkCommand(data.AppContext.AppName, productionEnvName, data.EnvName))
	if err != nil {
		utils.DieError(err, 1)
	}
	var summary dataForkSummary
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &summary); err != nil {
		utils.DieError(operationError(fmt.Sprintf("data fork failed: invalid helper JSON: %v", err), "ship data fork"), 1)
	}
	url, err := dataPreviewURL(data)
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Print(renderDataForkSummary(data.PreviewBranch, url, summary))
}

func CmdDataRm(root string) {
	data, err := currentDataContext(root)
	if err != nil {
		utils.DieError(err, 1)
	}
	defer data.Runner.Close()

	if _, err := runSSHDetail(data.Runner, data.AppContext.Server, serverAppDataRmCommand(data.AppContext.AppName, data.EnvName)); err != nil {
		utils.DieError(err, 1)
	}
	url, err := dataPreviewURL(data)
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Printf("Reset data for Preview %s\npreview: %s\n", data.PreviewBranch, url)
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
	if url, err := liveEnvURL(data.Runner, data.AppContext.Server, data.AppContext.AppName, data.EnvName); err == nil && url != "" {
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
				return envItem.URL, nil
			}
		}
	}
	return "", nil
}

func renderDataForkSummary(branch, url string, summary dataForkSummary) string {
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
	fmt.Fprintf(&b, "preview: %s\n", url)
	b.WriteString(DataForkPIINote)
	b.WriteByte('\n')
	return b.String()
}

func formatDataSize(size int64) string {
	if size == 1 {
		return "1 byte"
	}
	return fmt.Sprintf("%d bytes", size)
}

package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/deploybundle"
	"github.com/fprl/ship/internal/deployevent"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/addressing"
)

type ShipResult struct {
	URL        string   `json:"url"`
	Env        string   `json:"env"`
	Release    string   `json:"release"`
	Processes  []string `json:"processes"`
	DurationMs int64    `json:"durationMs"`
}

func formatPhaseDuration(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func CmdShip(root string, branchName string, tlsMode string, jsonFlag bool, rebuild bool, logs bool) {
	start := time.Now()
	progress := newShipProgress(logs)
	defer progress.close()
	result, err := runShip(root, branchName, tlsMode, rebuild, logs, progress)
	if err != nil {
		progress.close()
		utils.DieError(err, 1)
	}
	result.DurationMs = time.Since(start).Milliseconds()
	writeShipResult(result, jsonFlag)
}

func writeShipResult(result ShipResult, jsonFlag bool) {
	if jsonFlag {
		buf, err := json.Marshal(result)
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(buf))
		return
	}
	fmt.Println(result.URL)
}

func runShip(root string, branchName string, tlsMode string, rebuild bool, logs bool, progress *shipProgress) (ShipResult, error) {
	state, err := shipAddressPhase(root, branchName)
	if err != nil {
		return ShipResult{}, err
	}
	defer state.Runner.Close()
	if err := shipPreflightPhase(&state); err != nil {
		return ShipResult{}, err
	}
	if err := shipPlanPhase(&state); err != nil {
		return ShipResult{}, err
	}
	if err := shipRoutesPhase(&state, tlsMode); err != nil {
		return ShipResult{}, err
	}
	progress.timed("Preflight")
	if err := shipTarPhase(&state); err != nil {
		return ShipResult{}, err
	}
	progress.timed("Package")
	defer os.RemoveAll(state.TarDir)
	if err := shipApplyPhase(&state, rebuild, tlsMode, logs, progress); err != nil {
		return ShipResult{}, err
	}
	progress.line("Live")
	if state.Address.ProductionBranch && state.RoutePlan.NoConfiguredDomain {
		progress.line(prodNoDomainNextLine(state.BoxIP))
	}
	result, err := shipOutputPhase(state)
	if err != nil {
		return ShipResult{}, err
	}
	return result, nil
}

type shipRunState struct {
	Root          string
	Manifest      *config.Manifest
	Address       deployAddress
	Context       *config.AppContext
	Runner        *CommandRunner
	BoxIP         string
	Plan          localDeployPlan
	RoutePlan     deployRoutePlan
	TarDir        string
	LocalTar      string
	LocalManifest string
	LocalBundle   string
	Bundle        deploybundle.Metadata
}

func shipAddressPhase(root, branchName string) (shipRunState, error) {
	manifest, err := config.ReadManifest(root)
	if err != nil {
		return shipRunState{}, err
	}
	address, err := resolveDeployAddressForManifest(root, branchName, manifest)
	if err != nil {
		return shipRunState{}, err
	}
	baseEnv := baseEnvForPreview(address.EnvName, address.PreviewBranch)
	ctx, err := config.LoadAppContextFromManifest(root, baseEnv, manifest)
	if err != nil {
		return shipRunState{}, err
	}
	dirty, err := gitWorktreeDirty(root, staticServeDirs(ctx.Routes))
	if err != nil {
		return shipRunState{}, errcat.New(errcat.CodeOperationFailed, errcat.Fields{
			"detail":  "git status failed; check that Git is installed and this is a valid Git worktree",
			"command": "git status",
		})
	}
	address.Dirty = dirty
	if address.ProductionBranch && address.Dirty {
		return shipRunState{}, errcat.New(errcat.CodeDirtyWorktree, errcat.Fields{"branch": fmt.Sprintf("%q", address.Branch)})
	}
	runner, err := NewCommandRunner()
	if err != nil {
		return shipRunState{}, err
	}
	return shipRunState{Root: root, Manifest: manifest, Address: address, Context: ctx, Runner: runner}, nil
}

func shipPreflightPhase(state *shipRunState) error {
	resolvedEnv, err := resolveDeployPreviewEnv(state.Runner, state.Context, state.Address)
	if err != nil {
		return err
	}
	state.Address.EnvName = resolvedEnv
	ctx, err := config.LoadAppContextFromManifest(state.Root, resolvedEnv, state.Manifest)
	if err != nil {
		return err
	}
	state.Context = ctx
	state.BoxIP = resolveBoxIPv4(state.Runner, ctx.Server)
	return nil
}

func shipPlanPhase(state *shipRunState) error {
	dirty := state.Address.Dirty
	plan, diags, err := buildLocalDeployPlanForManifest(state.Root, state.Address.EnvName, state.Manifest, localDeployOptions{
		Dirty: &dirty,
	})
	if err != nil {
		return err
	}
	diags.printTo(os.Stderr)
	if diags.hasErrors() {
		return deployDiagnosticsError(diags)
	}
	state.Plan = plan
	return nil
}

func shipRoutesPhase(state *shipRunState, tlsMode string) error {
	routePlan, err := prepareDeployRoutes(state.Plan.Context, state.Address.EnvName, deployRouteOptions{
		Preview: state.Address.PreviewBranch != "",
		TLS:     tlsMode,
		BoxIP:   state.BoxIP,
	})
	if err != nil {
		return err
	}
	state.RoutePlan = routePlan
	state.Plan.Context = routePlan.Context
	state.Context = routePlan.Context
	warnRouteDNSPreflight(state.Context, state.BoxIP)
	if state.Address.ProductionBranch {
		if err := enforceProductionAncestry(state.Root, state.Runner, state.Context, state.Plan.BaseCommit); err != nil {
			return err
		}
	}
	return ensureRemoteEnvReadyForDeploy(state.Runner, state.Context)
}

func shipTarPhase(state *shipRunState) error {
	tarDir, err := os.MkdirTemp("", "ship-deploy-")
	if err != nil {
		return err
	}
	state.TarDir = tarDir
	state.LocalTar = filepath.Join(tarDir, "source.tar")
	state.LocalManifest = filepath.Join(tarDir, "ship.toml")
	state.LocalBundle = filepath.Join(tarDir, "deploy.tar")
	if err := writeSourceTar(state.Root, state.LocalTar, state.Plan.Dirty, state.Plan.ServeDirs); err != nil {
		return err
	}
	if state.RoutePlan.RewritesManifest {
		if err := writeDeployManifest(filepath.Join(state.Root, ManifestFile), state.LocalManifest, state.Context.Routes); err != nil {
			return operationError(fmt.Sprintf("write deploy manifest: %v", err), "ship")
		}
	} else if err := copyFile(filepath.Join(state.Root, ManifestFile), state.LocalManifest); err != nil {
		return operationError(fmt.Sprintf("copy manifest: %v", err), "ship")
	}
	metadata, err := deploybundle.Write(state.LocalBundle, state.LocalTar, state.LocalManifest)
	if err != nil {
		return operationError(fmt.Sprintf("package deploy bundle: %v", err), "ship")
	}
	state.Bundle = metadata
	return nil
}

func shipApplyPhase(state *shipRunState, rebuild bool, tlsMode string, logs bool, progress *shipProgress) error {
	applyCmd := serverAppApplyCommand(state.Context.AppName, state.Address.EnvName,
		state.Bundle,
		state.Plan,
		deployIdentity(state.Root, state.Runner, state.Context.Server),
		rebuild,
		logs,
		tlsMode,
		state.RoutePlan.PreviewAlias,
	)
	stdout, stderr, code, runErr := state.Runner.RunSSHStreamingFile(state.Context.Server, applyCmd, state.LocalBundle, func(line string) bool {
		event, ok := deployevent.Parse(line)
		if ok {
			progress.event(event)
		}
		return ok
	})
	if runErr != nil || code != 0 {
		return sshResultError(state.Context.Server, stdout, stderr, code, runErr, "deploy failed", "deploy failed", "ship")
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	return nil
}

func shipOutputPhase(state shipRunState) (ShipResult, error) {
	url := deploymentURLForBoxIP(state.Context, state.Address.EnvName, state.BoxIP)
	if state.Address.PreviewBranch != "" {
		liveURL, err := liveEnvURL(state.Runner, state.Context.Server, state.Context.AppName, state.Address.EnvName)
		if err != nil {
			return ShipResult{}, err
		}
		if liveURL == "" {
			return ShipResult{}, operationError("deployed, but the preview capability URL could not be read", "ship status")
		}
		url = liveURL
	}
	return ShipResult{
		URL:       url,
		Env:       state.Address.EnvName,
		Release:   state.Plan.Release,
		Processes: processNames(state.Plan.Context.Processes),
	}, nil
}

func deployDiagnosticsError(diags diagnostics) error {
	items := diags.errors()
	messages := diags.errorMessages()
	if diagnosticHasKind(items, diagnosticKindDockerfileMissing) {
		return errcat.New(errcat.CodeDockerfileMissing, nil)
	}
	if localDeployDiagnostic(items) {
		return errcat.New(errcat.CodeDeployBlockedLocalChecks, errcat.Fields{
			"detail":  strings.Join(messages, "\n"),
			"command": localDeployRemediation(items),
		})
	}
	return errcat.New(errcat.CodeManifestInvalid, errcat.Fields{
		"details": manifestDetailsForError(messages),
		"command": "fix ship.toml, then ship",
	})
}

func diagnosticHasKind(items []diagnostic, kind diagnosticKind) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func localDeployRemediation(items []diagnostic) string {
	for _, item := range items {
		switch item.Kind {
		case diagnosticKindGitNotRepo:
			return "git init && git add . && git commit -m \"initial ship app\""
		case diagnosticKindGitNoCommits:
			return "git add . && git commit -m \"initial ship app\""
		case diagnosticKindDotenv:
			return "exclude the dotenv file from deploy content or rename it to an allowed template, then ship"
		case diagnosticKindStaticHash:
			return "<build command> && ship"
		}
	}
	return "fix local checks"
}

func localDeployDiagnostic(items []diagnostic) bool {
	for _, item := range items {
		switch item.Kind {
		case diagnosticKindGit,
			diagnosticKindGitNotRepo,
			diagnosticKindGitNoCommits,
			diagnosticKindStaticHash,
			diagnosticKindDotenv:
			return true
		}
	}
	return false
}

func manifestDetailsForError(details []string) string {
	if len(details) == 1 {
		return details[0]
	}
	lines := []string{fmt.Sprintf("manifest has %d validation errors:", len(details))}
	for _, detail := range details {
		lines = append(lines, "  - "+detail)
	}
	return strings.Join(lines, "\n")
}

func processNames(processes map[string]config.Process) []string {
	out := make([]string, 0, len(processes))
	for name := range processes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func deploymentURL(ctx *config.AppContext, envName string) string {
	return deploymentURLForBoxIP(ctx, envName, resolveBoxIPv4(nil, ctx.Server))
}

func routedDeploymentURL(ctx *config.AppContext) string {
	url, _ := addressing.PrimaryURL(ctx.Routes, true)
	return url
}

func boxHost(target string) string {
	if _, host, ok := strings.Cut(target, "@"); ok {
		return host
	}
	if target == "" {
		return "box"
	}
	return target
}

func deploymentURLForBoxIP(ctx *config.AppContext, envName string, boxIP string) string {
	return addressing.URL(ctx, envName, boxIP)
}

func writeSourceTar(root string, dest string, dirty bool, staticDirs []string) error {
	if dirty {
		if err := writeDirtySourceTar(root, dest); err != nil {
			return err
		}
	} else if err := writeCleanSourceTar(root, dest); err != nil {
		return err
	}
	if len(staticDirs) > 0 {
		return appendStaticDirsToTar(root, dest, staticDirs)
	}
	return nil
}

func writeDirtySourceTar(root, dest string) error {
	files, err := dirtyArtifactFiles(root)
	if err != nil {
		return err
	}
	cmd := exec.Command("tar", "-C", root, "--null", "-T", "-", "-cf", dest)
	cmd.Stdin = strings.NewReader(strings.Join(files, "\x00") + "\x00")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func dirtyArtifactFiles(root string) ([]string, error) {
	out, stderr, code, _ := runCommand("git", []string{"ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "."}, root)
	if code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "git ls-files failed"
		}
		return nil, errors.New(detail)
	}
	var files []string
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		files = append(files, rel)
	}
	return files, nil
}

func writeCleanSourceTar(root string, dest string) error {
	repoRoot, treeish, err := gitArchiveTreeish(root)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", repoRoot, "archive", "--format=tar", "-o", dest, treeish)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func gitArchiveTreeish(root string) (repoRoot string, treeish string, err error) {
	repoRootOut, stderr, code, _ := runCommand("git", []string{"rev-parse", "--show-toplevel"}, root)
	if code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "git rev-parse --show-toplevel failed"
		}
		return "", "", errors.New(detail)
	}
	prefixOut, stderr, code, _ := runCommand("git", []string{"rev-parse", "--show-prefix"}, root)
	if code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "git rev-parse --show-prefix failed"
		}
		return "", "", errors.New(detail)
	}
	repoRoot = strings.TrimSpace(repoRootOut)
	prefix := strings.Trim(strings.TrimSpace(prefixOut), "/")
	if repoRoot == "" {
		return "", "", fmt.Errorf("git rev-parse --show-toplevel returned an empty path")
	}
	if prefix == "" {
		return repoRoot, "HEAD", nil
	}
	return repoRoot, "HEAD:" + prefix, nil
}

func staticServeDirs(routes map[string]config.Route) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, route := range routes {
		if route.Serve == "" || seen[route.Serve] {
			continue
		}
		seen[route.Serve] = true
		dirs = append(dirs, route.Serve)
	}
	sort.Strings(dirs)
	return dirs
}

func appendStaticDirsToTar(root, dest string, dirs []string) error {
	for _, dir := range dirs {
		cmd := exec.Command("tar", "-C", root, "-rf", dest, dir)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("append static dir %s: %v", dir, err)
		}
	}
	return nil
}

func staticTreeHash(root string, dirs []string) (string, error) {
	sum := sha256.New()
	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		if err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			switch {
			case info.Mode().IsDir():
				_, _ = fmt.Fprintf(sum, "dir\x00%s\x00", rel)
			case info.Mode().IsRegular():
				_, _ = fmt.Fprintf(sum, "file\x00%s\x00%d\x00", rel, info.Size())
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				if _, err := io.Copy(sum, f); err != nil {
					_ = f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
			case info.Mode()&os.ModeSymlink != 0:
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(sum, "symlink\x00%s\x00%s\x00", rel, target)
			}
			return nil
		}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func validateDeployArtifactDotenv(root string, dirty bool, staticDirs []string) error {
	var artifactFiles []string
	var err error
	if dirty {
		artifactFiles, err = dirtyArtifactFiles(root)
	} else {
		artifactFiles, err = cleanArtifactFiles(root)
	}
	if err != nil {
		return err
	}
	var dotenvs []string
	for _, rel := range artifactFiles {
		if blockedDotenv(rel) {
			dotenvs = append(dotenvs, rel)
		}
	}
	staticDotenvs, err := dotenvsInStaticDirs(root, staticDirs)
	if err != nil {
		return err
	}
	dotenvs = append(dotenvs, staticDotenvs...)
	return dotenvError(dotenvs)
}

func cleanArtifactFiles(root string) ([]string, error) {
	repoRoot, treeish, err := gitArchiveTreeish(root)
	if err != nil {
		return nil, err
	}
	out, stderr, code, _ := runCommand("git", []string{"-C", repoRoot, "ls-tree", "-r", "--name-only", "-z", treeish}, "")
	if code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "git ls-tree failed"
		}
		return nil, errors.New(detail)
	}
	var files []string
	for _, path := range strings.Split(out, "\x00") {
		if path == "" {
			continue
		}
		files = append(files, filepath.ToSlash(path))
	}
	return files, nil
}

func dotenvsInStaticDirs(root string, dirs []string) ([]string, error) {
	seen := map[string]bool{}
	var dotenvs []string
	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		if err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if blockedDotenv(rel) && !seen[rel] {
				seen[rel] = true
				dotenvs = append(dotenvs, rel)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return dotenvs, nil
}

func blockedDotenv(rel string) bool {
	name := filepath.Base(rel)
	return strings.HasPrefix(name, ".env") && !allowedDotenvName(name)
}

func allowedDotenvName(name string) bool {
	switch name {
	case ".env.example", ".env.sample", ".env.defaults":
		return true
	default:
		return false
	}
}

func dotenvError(dotenvs []string) error {
	if len(dotenvs) == 0 {
		return nil
	}
	dotenvs = uniqueStrings(dotenvs)
	sort.Strings(dotenvs)
	return errcat.New(errcat.CodeDotenvRejected, errcat.Fields{
		"files": strings.Join(dotenvs, ", "),
		"file":  dotenvs[0],
	})
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

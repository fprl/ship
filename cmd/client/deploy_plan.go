package client

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/releaseid"
)

type localDeployOptions struct {
	AllowDirty    bool
	IncludeDotenv bool
}

type localDeployPlan struct {
	Context    *config.AppContext
	Release    string
	BaseCommit string
	Dirty      bool
	CreatedAt  time.Time
	ServeDirs  []string
}

const timeRFC3339UTC = time.RFC3339Nano

func checkDiagnostics(root, envName string) (diagnostics, error) {
	errors, warnings, err := config.CheckManifest(root, envName)
	if err != nil {
		return nil, err
	}
	out := manifestDiagnostics(errors, warnings)
	if envName == "" || out.hasErrors() {
		return out, nil
	}
	plan, deployDiags, err := buildLocalDeployPlan(root, envName, localDeployOptions{})
	if err != nil {
		return nil, err
	}
	out = append(out, deployDiags...)
	if !out.hasErrors() {
		out = append(out, secretReferenceDiagnostics(plan.Context)...)
	}
	return out, nil
}

func buildLocalDeployPlan(root, envName string, opts localDeployOptions) (localDeployPlan, diagnostics, error) {
	errors, warnings, err := config.CheckManifest(root, envName)
	if err != nil {
		return localDeployPlan{}, nil, err
	}
	diags := manifestDiagnostics(errors, warnings)
	if diags.hasErrors() {
		return localDeployPlan{}, diags, nil
	}

	ctx, err := config.LoadAppContext(root, envName)
	if err != nil {
		return localDeployPlan{}, nil, err
	}
	plan := localDeployPlan{
		Context:   ctx,
		CreatedAt: time.Now().UTC(),
		ServeDirs: staticServeDirs(ctx.Routes),
	}

	shortCommit, fullCommit, err := gitCommit(root)
	if err != nil {
		diags = append(diags, diagnostic{
			Level:   diagnosticError,
			Message: err.Error(),
			Hint:    gitCommitHint(err),
		})
		return plan, diags, nil
	}
	plan.Release = shortCommit
	plan.BaseCommit = fullCommit

	dirty, err := gitWorktreeDirty(root, plan.ServeDirs)
	if err != nil {
		diags = append(diags, diagnostic{
			Level:   diagnosticError,
			Message: err.Error(),
			Hint:    "Check that Git is installed and the app root is a valid Git worktree.",
		})
		return plan, diags, nil
	}
	plan.Dirty = dirty
	if dirty && !opts.AllowDirty {
		diags = append(diags, diagnostic{
			Level:   diagnosticError,
			Message: "working tree is dirty",
			Hint:    fmt.Sprintf("Commit changes, or run `ship deploy --env %s --dirty` to deploy the current filesystem snapshot.", envName),
		})
		return plan, diags, nil
	}
	if dirty {
		plan.Release = dirtyReleaseID(shortCommit, plan.CreatedAt)
	}

	if len(plan.ServeDirs) > 0 {
		hash, err := staticTreeHash(root, plan.ServeDirs)
		if err != nil {
			diags = append(diags, diagnostic{
				Level:   diagnosticError,
				Message: fmt.Sprintf("hash static assets: %v", err),
				Hint:    "Run your framework build first so every serve directory exists and is readable.",
			})
			return plan, diags, nil
		}
		release, err := releaseid.WithStaticHash(plan.Release, hash[:12])
		if err != nil {
			diags = append(diags, diagnostic{
				Level:   diagnosticError,
				Message: err.Error(),
			})
			return plan, diags, nil
		}
		plan.Release = release
	}

	if !opts.IncludeDotenv {
		if err := validateDeployArtifactDotenv(root, plan.Dirty, plan.ServeDirs); err != nil {
			diags = append(diags, diagnostic{
				Level:   diagnosticError,
				Message: err.Error(),
				Hint:    "Use [env] and @secret references instead. Pass --include-dotenv only when you intentionally want dotenv files in the deploy artifact.",
			})
		}
	}

	return plan, diags, nil
}

func gitCommit(root string) (short string, full string, err error) {
	insideOut, _, code, _ := runCommand("git", []string{"rev-parse", "--is-inside-work-tree"}, root)
	if code != 0 || strings.TrimSpace(insideOut) != "true" {
		return "", "", fmt.Errorf("git repository not found")
	}
	fullOut, stderr, code, _ := runCommand("git", []string{"rev-parse", "HEAD"}, root)
	if code != 0 {
		if strings.Contains(stderr, "ambiguous argument") || strings.Contains(stderr, "unknown revision") {
			return "", "", fmt.Errorf("git repository has no commits")
		}
		return "", "", fmt.Errorf("git rev-parse failed")
	}
	shortOut, _, code, _ := runCommand("git", []string{"rev-parse", "--short=12", "HEAD"}, root)
	if code != 0 {
		return "", "", fmt.Errorf("git rev-parse --short failed")
	}
	full = strings.TrimSpace(fullOut)
	short = strings.TrimSpace(shortOut)
	if full == "" || short == "" {
		return "", "", fmt.Errorf("git rev-parse returned an empty commit")
	}
	return short, full, nil
}

func gitCommitHint(err error) string {
	switch err.Error() {
	case "git repository not found":
		return "ship uses Git commits to name reproducible releases.\nRun:\n  git init\n  git add .\n  git commit -m \"initial ship app\""
	case "git repository has no commits":
		return "Create the first release identity:\n  git add .\n  git commit -m \"initial ship app\""
	default:
		return "Run this from a committed Git checkout. Dirty deploys still need a base commit."
	}
}

func secretReferenceDiagnostics(ctx *config.AppContext) diagnostics {
	keys := secretRefKeys(ctx.SecretRefs)
	if len(keys) == 0 {
		return nil
	}
	out := make(diagnostics, 0, len(keys))
	for _, key := range keys {
		out = append(out, diagnostic{
			Level:   diagnosticWarning,
			Message: fmt.Sprintf("secret %s must be set before deploy", key),
			Hint:    fmt.Sprintf("Run:\n  printf '%%s' \"$%s\" | ship secret set %s --env %s", key, key, ctx.EnvName),
		})
	}
	return out
}

func gitWorktreeDirty(root string, ignoreDirs []string) (bool, error) {
	statusOut, _, code, _ := runCommand("git", []string{"status", "--porcelain=v1", "-z", "--", "."}, root)
	if code != 0 {
		return false, fmt.Errorf("git status failed")
	}
	fields := strings.Split(statusOut, "\x00")
	ignore := cleanIgnoredDirs(ignoreDirs)
	for i := 0; i < len(fields); i++ {
		record := fields[i]
		if record == "" {
			continue
		}
		if len(record) < 4 {
			return true, nil
		}
		status := record[:2]
		path := record[3:]
		if !pathInIgnoredDirs(path, ignore) {
			return true, nil
		}
		if strings.ContainsAny(status, "RC") && i+1 < len(fields) {
			i++
			oldPath := fields[i]
			if oldPath != "" && !pathInIgnoredDirs(oldPath, ignore) {
				return true, nil
			}
		}
	}
	return false, nil
}

func dirtyReleaseID(shortCommit string, at time.Time) string {
	return releaseid.Dirty(shortCommit, at)
}

func cleanIgnoredDirs(dirs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		clean := filepath.ToSlash(filepath.Clean(dir))
		if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

func pathInIgnoredDirs(path string, dirs []string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	for _, dir := range dirs {
		if clean == dir || strings.HasPrefix(clean, dir+"/") {
			return true
		}
	}
	return false
}

package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/utils"
)

type InitResult struct {
	AppName    string
	Root       string
	ConfigPath string
	Created    []string
	Kept       []string
}

type normalizedInit struct {
	name   string
	server string
}

// CmdInit writes a v1 manifest. Existing files are never overwritten.
func CmdInit(root string) {
	result, err := RunInit(root)
	if err != nil {
		utils.DieError(err, 1)
	}
	renderInitResult(result)
}

func RunInit(root string) (InitResult, error) {
	normalized, err := normalizeInitOptions(root)
	if err != nil {
		return InitResult{}, err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return InitResult{}, err
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return InitResult{}, err
	}
	manifestPath := filepath.Join(absRoot, ManifestFile)
	result := InitResult{
		AppName:    normalized.name,
		Root:       absRoot,
		ConfigPath: manifestPath,
	}

	if info, exists, err := lstatInitPath(absRoot, ManifestFile); err != nil {
		return InitResult{}, err
	} else if exists {
		if info.Mode()&os.ModeSymlink != 0 {
			return InitResult{}, operationError(fmt.Sprintf("%s already exists and is a symlink", ManifestFile), "ship init")
		}
		if info.IsDir() {
			return InitResult{}, operationError(fmt.Sprintf("%s already exists and is a directory", ManifestFile), "ship init")
		}
		result.Kept = append(result.Kept, ManifestFile)
		return result, nil
	}

	if err := writeNewInitFile(manifestPath, initManifest(normalized)); err != nil {
		return InitResult{}, err
	}
	result.Created = append(result.Created, ManifestFile)
	return result, nil
}

func normalizeInitOptions(root string) (normalizedInit, error) {
	name := defaultAppName(root)
	if pkgName := packageJSONName(root); pkgName != "" {
		name = pkgName
	}
	name = normalizeAppName(name)
	if !names.AppRe.MatchString(name) {
		return normalizedInit{}, usageError(fmt.Sprintf("invalid app name %q: must match %s", name, names.AppPattern), "ship init")
	}
	return normalizedInit{name: name, server: DefaultBoxTarget}, nil
}

func packageJSONName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return pkg.Name
}

func lstatInitPath(root string, rel string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(filepath.Join(root, rel))
	if err == nil {
		return info, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func writeNewInitFile(path string, body string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(body)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func initManifest(init normalizedInit) string {
	return fmt.Sprintf(`name = "%s"
box = "%s"

[processes]
web = {}
`, init.name, init.server)
}

func renderInitResult(result InitResult) {
	for _, path := range result.Created {
		fmt.Printf("Created %s\n", initDisplayPath(result.Root, path))
	}
	for _, path := range result.Kept {
		fmt.Printf("Kept existing %s\n", initDisplayPath(result.Root, path))
	}
	gitPrefix := initGitPrefix(result.Root)
	steps := []string{
		"review " + initDisplayPath(result.Root, ManifestFile),
	}
	if !initRootInsideGitWorktree(result.Root) {
		steps = append(steps, gitPrefix+"init")
	}
	steps = append(steps,
		gitPrefix+"add .",
		gitPrefix+"commit -m \"initial ship app\"",
	)
	fmt.Fprintln(os.Stderr, "Next:")
	for i, step := range steps {
		fmt.Fprintf(os.Stderr, "%d. %s\n", i+1, step)
	}
	fmt.Fprintln(os.Stderr, "next: ship")
}

func initGitPrefix(root string) string {
	if root == "" {
		return "git "
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "git -C " + utils.ShellEscape(root) + " "
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return "git -C " + utils.ShellEscape(root) + " "
	}
	if root == absCwd {
		return "git "
	}
	return "git -C " + utils.ShellEscape(root) + " "
}

func initRootInsideGitWorktree(root string) bool {
	if root == "" {
		root = "."
	}
	out, _, code, _ := runCommand("git", []string{"rev-parse", "--is-inside-work-tree"}, root)
	return code == 0 && strings.TrimSpace(out) == "true"
}

func initDisplayPath(root, rel string) string {
	if root == "" {
		return rel
	}
	path := filepath.Join(root, rel)
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	display, err := filepath.Rel(cwd, path)
	if err != nil || display == "." || strings.HasPrefix(display, ".."+string(filepath.Separator)) || display == ".." {
		return path
	}
	return filepath.ToSlash(display)
}

func defaultAppName(root string) string {
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	return normalizeAppName(filepath.Base(root))
}

func normalizeAppName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if idx := strings.LastIndex(value, "/"); idx >= 0 {
		value = value[idx+1:]
	}

	var b strings.Builder
	prevDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}

	candidate := strings.Trim(b.String(), "-")
	if candidate == "" {
		candidate = "app"
	}
	if candidate[0] < 'a' || candidate[0] > 'z' {
		candidate = "app-" + candidate
	}
	if len(candidate) > 41 {
		candidate = strings.Trim(candidate[:41], "-")
	}
	if len(candidate) < 2 {
		candidate += "p"
	}
	if !names.AppRe.MatchString(candidate) {
		return "app"
	}
	return candidate
}

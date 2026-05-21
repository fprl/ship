package systemd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/fprl/simple-vps/internal/store"
	"github.com/fprl/simple-vps/internal/utils"
)

var (
	ServiceActions = map[string]bool{
		"start":     true,
		"stop":      true,
		"restart":   true,
		"reload":    true,
		"status":    true,
		"is-active": true,
		"enable":    true,
		"disable":   true,
	}
)

func AppRoot() string {
	if p := os.Getenv("SIMPLE_VPS_APP_ROOT"); p != "" {
		return p
	}
	return "/var/apps"
}

func AppPath(name string) string {
	return filepath.Join(AppRoot(), name)
}

func AppUser(name string) string {
	return "app-" + name
}

func ServiceUnitName(name string, service string) string {
	return fmt.Sprintf("simple-%s-%s.service", name, service)
}

func SystemdUnitDir() string {
	if p := os.Getenv("SIMPLE_VPS_SYSTEMD_UNIT_DIR"); p != "" {
		return p
	}
	return "/etc/systemd/system"
}

func DeployTmpDir() string {
	if p := os.Getenv("SIMPLE_VPS_DEPLOY_TMP_DIR"); p != "" {
		return p
	}
	return "/tmp/simple-vps-deploy"
}

func RequireRoot() {
	if os.Geteuid() != 0 {
		utils.Die("this command must run as root", 1)
	}
}

func PathIsRelativeTo(target string, base string) bool {
	tClean := filepath.Clean(target)
	bClean := filepath.Clean(base)
	rel, err := filepath.Rel(bClean, tClean)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

func CommandSucceeds(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	return cmd.Run() == nil
}

func SystemServiceStatus(service string) string {
	cmd := exec.Command(utils.SystemctlBin(), "is-active", service)
	output, err := cmd.CombinedOutput()
	value := strings.TrimSpace(string(output))
	if value != "" {
		return value
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Sprintf("exit %d", exitErr.ExitCode())
		}
		return "error"
	}
	return "active"
}

func normalizeRequiredApp(name string) (string, error) {
	normalized, err := store.NormalizeApp(name)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", errors.New("app name is required")
	}
	return normalized, nil
}

func DeployUserFromSudo() (string, error) {
	user := os.Getenv("SUDO_USER")
	if user == "" || user == "root" {
		return "", nil
	}
	systemUserRe := store.SystemUserRe
	if !systemUserRe.MatchString(user) {
		return "", fmt.Errorf("invalid SUDO_USER")
	}
	if !CommandSucceeds("id", "-u", user) {
		return "", fmt.Errorf("sudo user does not exist: %s", user)
	}
	return user, nil
}

func EnsureAppUserExists(name string) error {
	user := AppUser(name)
	if CommandSucceeds("id", "-u", user) {
		return nil
	}
	_, err := utils.RunChecked("useradd", []string{"--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", user}, "")
	return err
}

func EnsureAppUserAbsent(name string) error {
	user := AppUser(name)
	if CommandSucceeds("id", "-u", user) {
		_, err := utils.RunChecked("userdel", []string{user}, "")
		return err
	}
	return nil
}

func EnsureAppGroupAbsent(name string) error {
	group := AppUser(name)
	if CommandSucceeds("getent", "group", group) {
		_, err := utils.RunChecked("groupdel", []string{group}, "")
		return err
	}
	return nil
}

func GrantDeployUserAccess(name string) error {
	user, err := DeployUserFromSudo()
	if err != nil {
		return err
	}
	if user == "" {
		return nil
	}
	_, err = utils.RunChecked("usermod", []string{"-aG", AppUser(name), user}, "")
	return err
}

func EnsureAppDirectories(name string) error {
	root := AppPath(name)
	shared := filepath.Join(root, "shared")
	deployTmp := DeployTmpDir()

	if err := os.MkdirAll(deployTmp, 0755); err != nil {
		return err
	}
	if err := os.Chmod(deployTmp, os.ModeSticky|0777); err != nil {
		return err
	}

	paths := []string{
		root,
		filepath.Join(root, "releases"),
		filepath.Join(root, "systemd"),
		shared,
		filepath.Join(shared, "db"),
		filepath.Join(shared, "storage"),
		filepath.Join(shared, "logs"),
	}

	for _, path := range paths {
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
	}

	envFile := filepath.Join(shared, ".env")
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		if err := os.WriteFile(envFile, []byte(""), 0600); err != nil {
			return err
		}
	}

	_, err := utils.RunChecked("chown", []string{"-R", fmt.Sprintf("%s:%s", AppUser(name), AppUser(name)), root}, "")
	if err != nil {
		return err
	}

	// Chmods
	_ = os.Chmod(root, 02775)
	_ = os.Chmod(filepath.Join(root, "releases"), 02775)
	_ = os.Chmod(filepath.Join(root, "systemd"), 0750)
	_ = os.Chmod(shared, 0750)
	_ = os.Chmod(filepath.Join(shared, "db"), 0750)
	_ = os.Chmod(filepath.Join(shared, "storage"), 0750)
	_ = os.Chmod(filepath.Join(shared, "logs"), 0750)

	return nil
}

func AppCreate(name string) error {
	name, err := normalizeRequiredApp(name)
	if err != nil {
		return err
	}
	if err := EnsureAppUserExists(name); err != nil {
		return err
	}
	if err := GrantDeployUserAccess(name); err != nil {
		return err
	}
	if err := EnsureAppDirectories(name); err != nil {
		return err
	}
	return store.Default().RegisterApp(name, AppPath(name))
}

func AppDestroy(name string) error {
	name, err := normalizeRequiredApp(name)
	if err != nil {
		return err
	}
	root := AppPath(name)
	if _, err := os.Stat(root); err == nil {
		if err := os.RemoveAll(root); err != nil {
			return err
		}
	}
	if err := EnsureAppUserAbsent(name); err != nil {
		return err
	}
	if err := EnsureAppGroupAbsent(name); err != nil {
		return err
	}
	return store.Default().UnregisterApp(name)
}

func AppReadEnv(name string) (string, error) {
	name, err := normalizeRequiredApp(name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(AppPath(name), "shared", ".env")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func ValidateDeployTmpSource(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		resolved = absPath // best effort if doesn't exist
	}
	tmpRoot, err := filepath.EvalSymlinks(DeployTmpDir())
	if err != nil {
		tmpRoot = DeployTmpDir()
	}
	if !PathIsRelativeTo(resolved, tmpRoot) {
		return "", fmt.Errorf("source file must live under %s", tmpRoot)
	}

	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("source file does not exist: %s", path)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("source path is a directory: %s", path)
	}

	sudoUid := os.Getenv("SUDO_UID")
	if sudoUid != "" {
		expectedUid, err := strconv.Atoi(sudoUid)
		if err != nil {
			return "", fmt.Errorf("invalid SUDO_UID")
		}
		if err := verifyFileOwner(resolved, expectedUid); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

func ValidateEnvironmentContent(content string) error {
	if strings.Contains(content, "\x00") {
		return errors.New("env file cannot contain NUL bytes")
	}
	lines := strings.Split(content, "\n")
	envKeyRe := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	for i, rawLine := range lines {
		lineIndex := i + 1
		line := strings.TrimSuffix(rawLine, "\r")
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		if strings.HasPrefix(stripped, "export ") {
			return fmt.Errorf("line %d: export is not supported", lineIndex)
		}
		if !strings.Contains(line, "=") {
			return fmt.Errorf("line %d: expected KEY=value", lineIndex)
		}
		parts := strings.SplitN(line, "=", 2)
		key := parts[0]
		value := parts[1]

		if strings.TrimSpace(key) != key {
			return fmt.Errorf("line %d: whitespace around keys is not supported", lineIndex)
		}
		if !envKeyRe.MatchString(key) {
			return fmt.Errorf("line %d: invalid env key: %s", lineIndex, key)
		}
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			return fmt.Errorf("line %d: quoted values are not supported", lineIndex)
		}
		// Inline comments: search for non-escaped ` #`
		if regexp.MustCompile(`\s+#`).MatchString(value) {
			return fmt.Errorf("line %d: inline comments are not supported", lineIndex)
		}
	}
	return nil
}

func AppInstallEnv(name string, pathToEnvFile string) error {
	name, err := normalizeRequiredApp(name)
	if err != nil {
		return err
	}
	source, err := ValidateDeployTmpSource(pathToEnvFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := ValidateEnvironmentContent(string(data)); err != nil {
		return err
	}

	target := filepath.Join(AppPath(name), "shared", ".env")
	tmpTarget := filepath.Join(AppPath(name), "shared", ".env.new")

	if err := os.WriteFile(tmpTarget, data, 0600); err != nil {
		return err
	}
	// Chown
	_, err = utils.RunChecked("chown", []string{fmt.Sprintf("%s:%s", AppUser(name), AppUser(name)), tmpTarget}, "")
	if err != nil {
		_ = os.Remove(tmpTarget)
		return err
	}
	if err := os.Rename(tmpTarget, target); err != nil {
		_ = os.Remove(tmpTarget)
		return err
	}
	_ = os.Remove(source)
	return nil
}

func ValidateUnitSource(path string, name string, service string) (string, error) {
	source, err := ValidateDeployTmpSource(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	content := string(data)
	if !strings.HasPrefix(content, "[Unit]") {
		return "", errors.New("unit file must start with [Unit]")
	}

	lines := strings.Split(content, "\n")
	expectedUser := fmt.Sprintf("User=%s", AppUser(name))
	foundUser := false
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == expectedUser {
			foundUser = true
		}
		if strings.HasPrefix(line, "User=") && line != expectedUser {
			return "", errors.New("unit file contains unexpected User= directive")
		}
		if strings.HasPrefix(line, "Group=") && line != fmt.Sprintf("Group=%s", AppUser(name)) {
			return "", errors.New("unit file contains unexpected Group= directive")
		}
	}
	if !foundUser {
		return "", fmt.Errorf("unit file must contain %s", expectedUser)
	}

	unitName := ServiceUnitName(name, service)
	if filepath.Base(source) != unitName {
		return "", fmt.Errorf("unit file name must be %s", unitName)
	}

	return source, nil
}

func AppInstallUnit(name string, service string, pathToUnitFile string) error {
	name, err := normalizeRequiredApp(name)
	if err != nil {
		return err
	}
	service, err = store.NormalizeService(service)
	if err != nil {
		return err
	}
	source, err := ValidateUnitSource(pathToUnitFile, name, service)
	if err != nil {
		return err
	}

	unitName := ServiceUnitName(name, service)
	systemdTarget := filepath.Join(SystemdUnitDir(), unitName)
	appTarget := filepath.Join(AppPath(name), "systemd", unitName)

	if err := copyFile(source, appTarget); err != nil {
		return err
	}
	if err := copyFile(source, systemdTarget); err != nil {
		return err
	}

	_ = os.Chmod(appTarget, 0644)
	_ = os.Chmod(systemdTarget, 0644)
	return store.Default().RegisterAppService(name, service)
}

func AppUninstallUnit(name string, service string) error {
	name, err := normalizeRequiredApp(name)
	if err != nil {
		return err
	}
	service, err = store.NormalizeService(service)
	if err != nil {
		return err
	}

	unitName := ServiceUnitName(name, service)
	targets := []string{
		filepath.Join(SystemdUnitDir(), unitName),
		filepath.Join(AppPath(name), "systemd", unitName),
	}

	for _, path := range targets {
		if _, err := os.Stat(path); err == nil {
			_ = os.Remove(path)
		}
	}
	return store.Default().UnregisterAppService(name, service)
}

func AppDaemonReload() error {
	_, err := utils.RunChecked(utils.SystemctlBin(), []string{"daemon-reload"}, "")
	return err
}

func AppServiceAction(action string, name string, service string) (string, error) {
	action = strings.TrimSpace(action)
	if !ServiceActions[action] {
		return "", fmt.Errorf("invalid service action: %s", action)
	}
	name, err := normalizeRequiredApp(name)
	if err != nil {
		return "", err
	}
	service, err = store.NormalizeService(service)
	if err != nil {
		return "", err
	}

	unitName := ServiceUnitName(name, service)
	cmdArgs := []string{action, unitName}

	if action == "status" || action == "is-active" {
		cmd := exec.Command(utils.SystemctlBin(), cmdArgs...)
		output, err := cmd.CombinedOutput()
		// status and is-active exit codes are propagated
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return string(output), exitErr
			}
			return string(output), err
		}
		return string(output), nil
	}

	_, err = utils.RunChecked(utils.SystemctlBin(), cmdArgs, "")
	return "", err
}

func NormalizeCwd(name string, value string) (string, error) {
	root, err := filepath.EvalSymlinks(AppPath(name))
	if err != nil {
		root = AppPath(name)
	}

	var cwdVal string
	if value == "" {
		cwdVal = root
	} else {
		cwdVal = value
	}

	absCwd, err := filepath.Abs(cwdVal)
	if err != nil {
		return "", err
	}
	cwd, err := filepath.EvalSymlinks(absCwd)
	if err != nil {
		cwd = absCwd
	}

	if !PathIsRelativeTo(cwd, root) {
		return "", fmt.Errorf("cwd must be under %s", root)
	}

	fi, err := os.Stat(cwd)
	if err != nil {
		return "", fmt.Errorf("cwd does not exist: %s", cwd)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("cwd is not a directory: %s", cwd)
	}
	return cwd, nil
}

func ValidateRunCommand(command []string) []string {
	if len(command) == 0 {
		utils.Die("missing command after --", 1)
	}
	if command[0] == "--" {
		command = command[1:]
	}
	if len(command) == 0 {
		utils.Die("missing command after --", 1)
	}
	for _, arg := range command {
		if strings.ContainsAny(arg, "\x00\n\r") {
			utils.Die("command arguments cannot contain NULs or newlines", 1)
		}
	}
	return command
}

func AppRunAs(name string, cwdArg string, runCommand []string) error {
	name, err := normalizeRequiredApp(name)
	if err != nil {
		return err
	}

	if cwdArg == "" && len(runCommand) >= 2 && runCommand[0] == "--cwd" {
		cwdArg = runCommand[1]
		runCommand = runCommand[2:]
	}

	if cwdArg == "" {
		return errors.New("missing --cwd")
	}

	cwd, err := NormalizeCwd(name, cwdArg)
	if err != nil {
		return err
	}

	command := ValidateRunCommand(runCommand)
	args := append([]string{"-u", AppUser(name), "--"}, command...)
	_, err = utils.RunChecked("runuser", args, cwd)
	return err
}

// Copy helper
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

func verifyFileOwner(path string, expectedUid int) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(stat.Uid) != expectedUid {
		return fmt.Errorf("source file must be owned by invoking sudo user (uid %d)", expectedUid)
	}
	return nil
}

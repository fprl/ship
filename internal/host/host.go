// Package host holds host-side primitives shared by the privileged helper.
package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

// DeployTmpDir is the world-writable + sticky directory the client
// uploads source tarballs and manifests to. The provisioner creates
// it at host install; setup-env re-creates it as a safety net.
func DeployTmpDir() string {
	if p := os.Getenv("SHIP_DEPLOY_TMP_DIR"); p != "" {
		return p
	}
	return "/tmp/ship-deploy"
}

// PathIsRelativeTo reports whether target is the same as base or lives
// under it after symlink-free cleanup. Used by ValidateDeployTmpSource
// to confine uploaded paths to /tmp/ship-deploy.
func PathIsRelativeTo(target string, base string) bool {
	tClean := filepath.Clean(target)
	bClean := filepath.Clean(base)
	rel, err := filepath.Rel(bClean, tClean)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

// CommandSucceeds runs `name args...` and reports whether it exited 0.
// Intended for short presence probes (`id -u user`, `getent group g`,
// `podman network exists ingress`) where the output is irrelevant.
func CommandSucceeds(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	return cmd.Run() == nil
}

// SystemServiceStatus returns a one-word state for a systemd unit (or
// `exit N` / `error` on failure). Used by host doctor checks.
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

// DeployUserFromSudo returns the SUDO_USER that invoked the helper,
// validated against the SystemUserRe schema and checked for existence.
// Returns ("", nil) when not invoked via sudo or invoked by root.
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

// ValidateDeployTmpSource resolves symlinks on the supplied path,
// requires the resolved path to live under DeployTmpDir(), requires it
// to be a regular file, and (when invoked via sudo) verifies the file
// is owned by SUDO_UID. This closes the door on a local user seeding
// files into /tmp/ship-deploy for the helper to act on.
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

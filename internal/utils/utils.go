package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var shellEscapeRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func ShellEscape(value string) string {
	if shellEscapeRe.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func BackupDir() string {
	if p := os.Getenv("SHIP_BACKUP_DIR"); p != "" {
		return p
	}
	return "/etc/simple-vps/backups"
}

func CaddyBin() string {
	if b := os.Getenv("SHIP_CADDY_BIN"); b != "" {
		return b
	}
	return "caddy"
}

func SystemctlBin() string {
	if b := os.Getenv("SHIP_SYSTEMCTL_BIN"); b != "" {
		return b
	}
	return "systemctl"
}

type CommandError struct {
	Name     string
	Args     []string
	Stdout   string
	Stderr   string
	Err      error
	TimedOut bool
	Timeout  time.Duration
}

func (e *CommandError) Error() string {
	if e.TimedOut {
		return fmt.Sprintf("command timed out after %s: %s %v", e.Timeout, e.Name, e.Args)
	}
	return fmt.Sprintf("command failed: %s %v: %v", e.Name, e.Args, e.Err)
}

func (e *CommandError) Unwrap() error {
	return e.Err
}

func (e *CommandError) CombinedOutput() string {
	switch {
	case e.Stderr != "" && e.Stdout != "":
		return e.Stderr + "\n" + e.Stdout
	case e.Stderr != "":
		return e.Stderr
	default:
		return e.Stdout
	}
}

func Die(message string, code int) {
	if code == 1 && usageOrManifestFailure(message) {
		code = 2
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", message)
	os.Exit(code)
}

func usageOrManifestFailure(message string) bool {
	switch {
	case strings.Contains(message, "ship.toml"),
		strings.Contains(message, "manifest"),
		strings.Contains(message, "--config"),
		strings.Contains(message, "invalid app name"),
		strings.Contains(message, "invalid env name"),
		strings.Contains(message, "invalid template"),
		strings.Contains(message, "box target is required"):
		return true
	default:
		return false
	}
}

func RunChecked(name string, args []string, cwd string) ([]byte, error) {
	return runChecked(nil, 0, name, args, cwd)
}

func RunCheckedWithTimeout(name string, args []string, cwd string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runChecked(ctx, timeout, name, args, cwd)
}

func runChecked(ctx context.Context, timeout time.Duration, name string, args []string, cwd string) ([]byte, error) {
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		cmd = exec.Command(name, args...)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if stderr.Len() > 0 {
			os.Stderr.Write(stderr.Bytes())
		}
		if stdout.Len() > 0 {
			os.Stderr.Write(stdout.Bytes())
		}
		cmdErr := &CommandError{
			Name:     name,
			Args:     append([]string(nil), args...),
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Err:      err,
			TimedOut: ctx != nil && ctx.Err() == context.DeadlineExceeded,
			Timeout:  timeout,
		}
		return nil, cmdErr
	}
	return stdout.Bytes(), nil
}

func BackupFile(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	}

	backupDir := BackupDir()
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", err
	}

	stamp := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("%s.%s", filepath.Base(path), stamp)
	backupPath := filepath.Join(backupDir, filename)

	srcFile, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer srcFile.Close()

	destFile, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return "", err
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, srcFile); err != nil {
		return "", err
	}

	return backupPath, nil
}
